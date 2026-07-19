package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	processContract  = "sandbox-process/v1"
	defaultStateRoot = "/tmp/worksflow-processes"
	defaultWorkspace = "/workspace"
	defaultLogLimit  = int64(8 << 20)
	maxLogRead       = int64(1 << 20)
)

var processIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type processState struct {
	SchemaVersion string    `json:"schemaVersion"`
	ID            string    `json:"id"`
	State         string    `json:"state"`
	PID           int       `json:"pid,omitempty"`
	Argv          []string  `json:"argv"`
	WorkingDir    string    `json:"workingDirectory"`
	ExitCode      *int      `json:"exitCode,omitempty"`
	Failure       string    `json:"failure,omitempty"`
	LogBytes      int64     `json:"logBytes"`
	LogTruncated  bool      `json:"logTruncated"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt,omitempty"`
}

type logResult struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Offset        int64  `json:"offset"`
	NextOffset    int64  `json:"nextOffset"`
	Data          string `json:"data"`
	EOF           bool   `json:"eof"`
	Truncated     bool   `json:"truncated"`
}

func main() {
	if len(os.Args) < 2 {
		fatalUsage("a process command is required")
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runProcess(os.Args[2:])
	case "status":
		err = statusProcess(os.Args[2:])
	case "signal":
		err = signalProcess(os.Args[2:])
	case "logs":
		err = readLogs(os.Args[2:])
	default:
		err = errors.New("unknown process command")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runProcess(arguments []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "process ID")
	workingDir := flags.String("cwd", ".", "workspace-relative directory")
	logLimit := flags.Int64("log-limit", defaultLogLimit, "maximum retained output")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	argv := flags.Args()
	if err := validateProcessID(*id); err != nil || len(argv) == 0 || len(argv) > 64 ||
		*logLimit < 1 || *logLimit > 64<<20 {
		return errors.New("invalid process identity, argv, or log limit")
	}
	for _, argument := range argv {
		if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, 0) {
			return errors.New("invalid process argument")
		}
	}
	executable := strings.ToLower(filepath.Base(argv[0]))
	if executable == "sh" || executable == "bash" || executable == "dash" || executable == "zsh" ||
		executable == "fish" || executable == "cmd" || executable == "powershell" || executable == "pwsh" {
		return errors.New("shell commands are not allowed")
	}
	_, directory, err := resolveWorkingDirectory(*workingDir)
	if err != nil {
		return err
	}
	root := stateRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	processRoot := filepath.Join(root, *id)
	if err := os.Mkdir(processRoot, 0o700); err != nil {
		return fmt.Errorf("create exclusive process state: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(processRoot, "output.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	state := processState{
		SchemaVersion: processContract, ID: *id, State: "starting",
		Argv: append([]string(nil), argv...), WorkingDir: filepath.ToSlash(*workingDir),
		StartedAt: time.Now().UTC(),
	}
	if err := writeState(processRoot, state); err != nil {
		return err
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = directory
	command.Env = append([]string(nil), os.Environ()...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	writer := &boundedWriter{target: logFile, limit: *logLimit}
	command.Stdout, command.Stderr = writer, writer
	if err := command.Start(); err != nil {
		state.State, state.Failure, state.FinishedAt = "failed", boundedError(err), time.Now().UTC()
		exitCode := 1
		state.ExitCode = &exitCode
		_ = writeState(processRoot, state)
		return err
	}
	state.State, state.PID = "running", command.Process.Pid
	if err := writeState(processRoot, state); err != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		return err
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var waitErr error
	for {
		select {
		case forwarded := <-signals:
			if value, ok := forwarded.(syscall.Signal); ok {
				_ = syscall.Kill(-command.Process.Pid, value)
			}
		case waitErr = <-done:
			goto finished
		}
	}

finished:
	_ = logFile.Sync()
	state.LogBytes, state.LogTruncated = writer.result()
	state.FinishedAt = time.Now().UTC()
	exitCode := 0
	if waitErr != nil {
		exitCode = 1
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			exitCode = exit.ExitCode()
		}
		state.State, state.Failure = "failed", boundedError(waitErr)
	} else {
		state.State = "exited"
	}
	state.ExitCode = &exitCode
	if err := writeState(processRoot, state); err != nil {
		return err
	}
	return nil
}

func statusProcess(arguments []string) error {
	id, err := singleIDFlag("status", arguments)
	if err != nil {
		return err
	}
	state, err := loadState(filepath.Join(stateRoot(), id))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(state)
}

func signalProcess(arguments []string) error {
	flags := flag.NewFlagSet("signal", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "process ID")
	name := flags.String("name", "TERM", "signal")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || validateProcessID(*id) != nil {
		return errors.New("invalid signal request")
	}
	allowed := map[string]syscall.Signal{
		"INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP,
	}
	requested, ok := allowed[strings.ToUpper(strings.TrimSpace(*name))]
	if !ok {
		return errors.New("unsupported signal")
	}
	state, err := loadState(filepath.Join(stateRoot(), *id))
	if err != nil {
		return err
	}
	if state.State != "running" || state.PID < 2 {
		return errors.New("process is not running")
	}
	if err := syscall.Kill(-state.PID, requested); err != nil {
		return err
	}
	return nil
}

func readLogs(arguments []string) error {
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "process ID")
	offset := flags.Int64("offset", 0, "byte offset")
	limit := flags.Int64("limit", 64<<10, "read limit")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || validateProcessID(*id) != nil ||
		*offset < 0 || *limit < 1 || *limit > maxLogRead {
		return errors.New("invalid log request")
	}
	processRoot := filepath.Join(stateRoot(), *id)
	state, err := loadState(processRoot)
	if err != nil {
		return err
	}
	file, err := os.Open(filepath.Join(processRoot, "output.log"))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || *offset > info.Size() {
		return errors.New("log offset is outside retained output")
	}
	if _, err := file.Seek(*offset, io.SeekStart); err != nil {
		return err
	}
	buffer := make([]byte, *limit)
	read, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	result := logResult{
		SchemaVersion: processContract, ID: *id, Offset: *offset,
		NextOffset: *offset + int64(read), Data: base64.RawStdEncoding.EncodeToString(buffer[:read]),
		EOF:       *offset+int64(read) >= info.Size() && state.State != "running" && state.State != "starting",
		Truncated: state.LogTruncated,
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func resolveWorkingDirectory(value string) (string, string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" {
		value = "."
	}
	if filepath.IsAbs(value) || filepath.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") ||
		strings.ContainsRune(value, 0) {
		return "", "", errors.New("unsafe working directory")
	}
	workspace := workspaceRoot()
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", "", err
	}
	directory := filepath.Join(resolvedWorkspace, filepath.FromSlash(value))
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(resolvedWorkspace, resolvedDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("working directory escapes workspace")
	}
	info, err := os.Stat(resolvedDirectory)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("working directory is not a directory")
	}
	return resolvedWorkspace, resolvedDirectory, nil
}

func singleIDFlag(name string, arguments []string) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "process ID")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || validateProcessID(*id) != nil {
		return "", errors.New("invalid process identity")
	}
	return *id, nil
}

func validateProcessID(value string) error {
	if !processIDPattern.MatchString(value) {
		return errors.New("invalid process ID")
	}
	return nil
}

func stateRoot() string {
	if os.Getenv("WORKSFLOW_PROCESS_TEST_MODE") == "1" {
		if value := strings.TrimSpace(os.Getenv("WORKSFLOW_PROCESS_ROOT")); filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
	}
	return defaultStateRoot
}

func workspaceRoot() string {
	if os.Getenv("WORKSFLOW_PROCESS_TEST_MODE") == "1" {
		if value := strings.TrimSpace(os.Getenv("WORKSFLOW_PROCESS_WORKSPACE")); filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
	}
	return defaultWorkspace
}

func writeState(root string, state processState) error {
	state.SchemaVersion = processContract
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := filepath.Join(root, ".state-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(root, "state.json")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func loadState(root string) (processState, error) {
	file, err := os.Open(filepath.Join(root, "state.json"))
	if err != nil {
		return processState{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 128<<10))
	decoder.DisallowUnknownFields()
	var state processState
	if err := decoder.Decode(&state); err != nil || state.SchemaVersion != processContract || validateProcessID(state.ID) != nil {
		return processState{}, errors.New("invalid process state")
	}
	return state, nil
}

type boundedWriter struct {
	mu        sync.Mutex
	target    *os.File
	limit     int64
	written   int64
	truncated bool
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	wanted := len(value)
	remaining := writer.limit - writer.written
	if remaining <= 0 {
		writer.truncated = true
		return wanted, nil
	}
	write := value
	if int64(len(write)) > remaining {
		write = write[:remaining]
		writer.truncated = true
	}
	written, err := writer.target.Write(write)
	writer.written += int64(written)
	if err != nil {
		return 0, err
	}
	return wanted, nil
}

func (writer *boundedWriter) result() (int64, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.written, writer.truncated
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 1000 {
		value = value[:1000]
	}
	return value
}

func fatalUsage(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(64)
}
