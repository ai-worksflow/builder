package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

const (
	workspaceRoot = "/workspace"
	shellPath     = "/bin/bash"
	maxPacketSize = 64 << 10

	packetInput  byte = 1
	packetResize byte = 2
	packetSignal byte = 3
	packetClose  byte = 4
)

type terminalOptions struct {
	id               string
	workingDirectory string
	rows             uint16
	columns          uint16
}

type terminalPacket struct {
	kind    byte
	payload []byte
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	workingDirectory, err := secureWorkingDirectory(workspaceRoot, options.workingDirectory)
	if err != nil {
		return err
	}
	command := exec.Command(shellPath, "--noprofile", "--norc")
	command.Dir = workingDirectory
	command.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"PS1=[worksflow] \\w \\$ ",
	)
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: options.rows, Cols: options.columns})
	if err != nil {
		return fmt.Errorf("start fixed non-root shell: %w", err)
	}
	defer terminal.Close()

	copyDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(output, terminal)
		copyDone <- copyErr
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	controlDone := make(chan error, 1)
	go func() { controlDone <- serveControls(input, terminal, command.Process.Pid) }()

	select {
	case waitErr := <-waitDone:
		_ = terminal.Close()
		<-copyDone
		return exitError(waitErr)
	case controlErr := <-controlDone:
		_ = signalProcessGroup(command.Process.Pid, syscall.SIGHUP)
		waitErr := <-waitDone
		_ = terminal.Close()
		<-copyDone
		if controlErr != nil && !errors.Is(controlErr, io.EOF) {
			return controlErr
		}
		return exitError(waitErr)
	}
}

func parseOptions(args []string) (terminalOptions, error) {
	set := flag.NewFlagSet("worksflow-sandbox-pty", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	id := set.String("id", "", "terminal UUID")
	cwd := set.String("cwd", ".", "workspace-relative directory")
	rows := set.Uint("rows", 24, "terminal rows")
	columns := set.Uint("columns", 80, "terminal columns")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || !validUUID(*id) ||
		*rows < 2 || *rows > 500 || *columns < 2 || *columns > 500 {
		return terminalOptions{}, errors.New("invalid PTY options")
	}
	return terminalOptions{
		id: *id, workingDirectory: strings.TrimSpace(*cwd),
		rows: uint16(*rows), columns: uint16(*columns),
	}, nil
}

func secureWorkingDirectory(root, relative string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || relative == "" ||
		filepath.IsAbs(relative) || strings.Contains(relative, "\\") || strings.ContainsRune(relative, 0) {
		return "", errors.New("invalid PTY working directory")
	}
	cleaned := filepath.Clean(relative)
	if cleaned != relative || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid PTY working directory")
	}
	candidate := filepath.Join(root, cleaned)
	if candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return "", errors.New("PTY working directory escapes workspace")
	}
	current := root
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("PTY workspace root is not a real directory")
	}
	if cleaned != "." {
		for _, segment := range strings.Split(cleaned, string(filepath.Separator)) {
			current = filepath.Join(current, segment)
			info, statErr := os.Lstat(current)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("PTY working directory contains a missing, non-directory, or symlink component")
			}
		}
	}
	return candidate, nil
}

func serveControls(input io.Reader, terminal *os.File, pid int) error {
	reader := bufio.NewReaderSize(input, maxPacketSize+5)
	for {
		packet, err := readPacket(reader)
		if err != nil {
			return err
		}
		switch packet.kind {
		case packetInput:
			if len(packet.payload) == 0 {
				continue
			}
			if _, err := terminal.Write(packet.payload); err != nil {
				return fmt.Errorf("write PTY input: %w", err)
			}
		case packetResize:
			rows := binary.BigEndian.Uint16(packet.payload[:2])
			columns := binary.BigEndian.Uint16(packet.payload[2:])
			if rows < 2 || rows > 500 || columns < 2 || columns > 500 {
				return errors.New("invalid PTY resize")
			}
			if err := pty.Setsize(terminal, &pty.Winsize{Rows: rows, Cols: columns}); err != nil {
				return fmt.Errorf("resize PTY: %w", err)
			}
		case packetSignal:
			signal, ok := terminalSignal(string(packet.payload))
			if !ok {
				return errors.New("invalid PTY signal")
			}
			if err := signalProcessGroup(pid, signal); err != nil {
				return fmt.Errorf("signal PTY process group: %w", err)
			}
		case packetClose:
			return io.EOF
		default:
			return errors.New("unknown PTY control packet")
		}
	}
}

func readPacket(reader io.Reader) (terminalPacket, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return terminalPacket{}, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxPacketSize {
		return terminalPacket{}, errors.New("PTY control packet is too large")
	}
	packet := terminalPacket{kind: header[0], payload: make([]byte, int(length))}
	if _, err := io.ReadFull(reader, packet.payload); err != nil {
		return terminalPacket{}, err
	}
	switch packet.kind {
	case packetInput:
	case packetResize:
		if len(packet.payload) != 4 {
			return terminalPacket{}, errors.New("invalid PTY resize packet")
		}
	case packetSignal:
		if len(packet.payload) < 3 || len(packet.payload) > 4 {
			return terminalPacket{}, errors.New("invalid PTY signal packet")
		}
	case packetClose:
		if len(packet.payload) != 0 {
			return terminalPacket{}, errors.New("invalid PTY close packet")
		}
	default:
		return terminalPacket{}, errors.New("unknown PTY control packet")
	}
	return packet, nil
}

func terminalSignal(value string) (syscall.Signal, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "INT":
		return syscall.SIGINT, true
	case "TERM":
		return syscall.SIGTERM, true
	case "KILL":
		return syscall.SIGKILL, true
	case "HUP":
		return syscall.SIGHUP, true
	default:
		return 0, false
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid < 2 {
		return errors.New("invalid PTY process group")
	}
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func exitError(err error) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("PTY shell exited with status %s", strconv.Itoa(exit.ExitCode()))
	}
	return err
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return value[14] >= '1' && value[14] <= '8' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b')
}
