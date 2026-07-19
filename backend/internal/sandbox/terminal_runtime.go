package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const runtimeTerminalExecutable = "/usr/local/bin/worksflow-sandbox-pty"

type RuntimeTerminalSpec struct {
	Runtime          RuntimeSpec
	ID               string
	WorkingDirectory string
	Rows             uint16
	Columns          uint16
}

type RuntimeTerminalExit struct {
	ExitCode   int
	Failure    string
	FinishedAt time.Time
}

type RuntimeTerminal interface {
	WriteInput([]byte) error
	Resize(uint16, uint16) error
	Signal(string) error
	Close() error
	Output() <-chan []byte
	Done() <-chan RuntimeTerminalExit
}

type RuntimeTerminalManager interface {
	OpenTerminal(context.Context, RuntimeTerminalSpec) (RuntimeTerminal, error)
}

type runtimeCommandStream interface {
	Input() io.WriteCloser
	Output() io.ReadCloser
	Wait() error
}

type runtimeStreamExecutor interface {
	Start(context.Context, ...string) (runtimeCommandStream, error)
}

type containerCLIStream struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  io.ReadCloser
	stderr  *boundedRuntimeBuffer
	args    []string
	wait    sync.Once
	waitErr error
}

func (stream *containerCLIStream) Input() io.WriteCloser { return stream.input }
func (stream *containerCLIStream) Output() io.ReadCloser { return stream.output }
func (stream *containerCLIStream) Wait() error {
	stream.wait.Do(func() {
		if err := stream.command.Wait(); err != nil {
			stream.waitErr = &runtimeCommandError{
				args: append([]string(nil), stream.args...), output: stream.stderr.String(), cause: err,
			}
		}
	})
	return stream.waitErr
}

func (executor *containerCLIExecutor) Start(ctx context.Context, args ...string) (runtimeCommandStream, error) {
	if ctx == nil || executor == nil || executor.path == "" {
		return nil, ErrRuntimeUnavailable
	}
	limit := executor.outputLimit
	if limit <= 0 || limit > 8<<20 {
		limit = 1 << 20
	}
	command := exec.CommandContext(ctx, executor.path, args...)
	command.Env = append([]string(nil), executor.environment...)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: create runtime stream input: %v", ErrRuntimeUnavailable, err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("%w: create runtime stream output: %v", ErrRuntimeUnavailable, err)
	}
	stderr := &boundedRuntimeBuffer{limit: limit}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, &runtimeCommandError{args: append([]string(nil), args...), output: stderr.String(), cause: err}
	}
	return &containerCLIStream{
		command: command, input: input, output: output, stderr: stderr, args: append([]string(nil), args...),
	}, nil
}

type containerRuntimeTerminal struct {
	stream runtimeCommandStream
	input  io.WriteCloser
	output chan []byte
	done   chan RuntimeTerminalExit
	mu     sync.Mutex
	closed bool
	close  sync.Once
}

func (manager *ContainerRuntime) OpenTerminal(
	ctx context.Context,
	spec RuntimeTerminalSpec,
) (RuntimeTerminal, error) {
	if err := manager.validateTerminal(ctx, spec); err != nil {
		return nil, err
	}
	status, err := manager.Inspect(ctx, spec.Runtime)
	if err != nil || status.State != "running" || !status.Healthy {
		if err == nil {
			err = ErrRuntimeNotReady
		}
		return nil, err
	}
	if manager.streamer == nil {
		return nil, ErrRuntimeUnavailable
	}
	stream, err := manager.streamer.Start(
		ctx, "exec", "-i", manager.containerName(spec.Runtime.SessionID), runtimeTerminalExecutable,
		"--id", spec.ID, "--cwd", spec.WorkingDirectory,
		"--rows", strconv.Itoa(int(spec.Rows)), "--columns", strconv.Itoa(int(spec.Columns)),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: open fixed sandbox PTY: %v", ErrRuntimeUnavailable, err)
	}
	terminal := &containerRuntimeTerminal{
		stream: stream, input: stream.Input(), output: make(chan []byte, 32), done: make(chan RuntimeTerminalExit, 1),
	}
	go terminal.readOutput()
	return terminal, nil
}

func (manager *ContainerRuntime) validateTerminal(ctx context.Context, spec RuntimeTerminalSpec) error {
	if manager == nil || ctx == nil || validateRuntimeSpec(spec.Runtime) != nil || !validUUID(spec.ID) ||
		!validTerminalSize(spec.Rows, spec.Columns) {
		return ErrTerminalInvalid
	}
	directory := strings.TrimSpace(spec.WorkingDirectory)
	if directory == "" || path.IsAbs(directory) || path.Clean(directory) != directory ||
		directory == ".." || strings.HasPrefix(directory, "../") || strings.Contains(directory, "\\") ||
		strings.ContainsRune(directory, 0) {
		return ErrTerminalInvalid
	}
	return nil
}

func (terminal *containerRuntimeTerminal) WriteInput(value []byte) error {
	if len(value) == 0 || len(value) > 64<<10 {
		return ErrTerminalInvalid
	}
	return terminal.writePacket(1, value)
}

func (terminal *containerRuntimeTerminal) Resize(rows, columns uint16) error {
	if !validTerminalSize(rows, columns) {
		return ErrTerminalInvalid
	}
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[:2], rows)
	binary.BigEndian.PutUint16(payload[2:], columns)
	return terminal.writePacket(2, payload)
}

func (terminal *containerRuntimeTerminal) Signal(value string) error {
	value = strings.ToUpper(strings.TrimSpace(value))
	if !validTerminalSignal(value) {
		return ErrTerminalInvalid
	}
	return terminal.writePacket(3, []byte(value))
}

func (terminal *containerRuntimeTerminal) Close() error {
	var result error
	terminal.close.Do(func() {
		result = terminal.writePacket(4, nil)
		terminal.mu.Lock()
		terminal.closed = true
		closeErr := terminal.input.Close()
		terminal.mu.Unlock()
		if result == nil && closeErr != nil {
			result = closeErr
		}
	})
	return result
}

func (terminal *containerRuntimeTerminal) Output() <-chan []byte            { return terminal.output }
func (terminal *containerRuntimeTerminal) Done() <-chan RuntimeTerminalExit { return terminal.done }

func (terminal *containerRuntimeTerminal) writePacket(kind byte, payload []byte) error {
	if len(payload) > 64<<10 {
		return ErrTerminalInvalid
	}
	packet := make([]byte, 5+len(payload))
	packet[0] = kind
	binary.BigEndian.PutUint32(packet[1:5], uint32(len(payload)))
	copy(packet[5:], payload)
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.closed {
		return io.ErrClosedPipe
	}
	_, err := io.Copy(terminal.input, bytes.NewReader(packet))
	if err != nil {
		return fmt.Errorf("%w: write PTY control: %v", ErrRuntimeUnavailable, err)
	}
	return nil
}

func (terminal *containerRuntimeTerminal) readOutput() {
	defer close(terminal.output)
	buffer := make([]byte, 32<<10)
	for {
		count, err := terminal.stream.Output().Read(buffer)
		if count > 0 {
			value := append([]byte(nil), buffer[:count]...)
			terminal.output <- value
		}
		if err != nil {
			break
		}
	}
	waitErr := terminal.stream.Wait()
	exit := RuntimeTerminalExit{FinishedAt: time.Now().UTC()}
	if waitErr != nil {
		exit.ExitCode = 1
		exit.Failure = boundedTerminalFailure(waitErr.Error())
		var commandError *runtimeCommandError
		if errors.As(waitErr, &commandError) {
			var exitError *exec.ExitError
			if errors.As(commandError.cause, &exitError) {
				exit.ExitCode = exitError.ExitCode()
			}
		}
	}
	terminal.done <- exit
	close(terminal.done)
}

func boundedTerminalFailure(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\x00", ""), "\r", " "))
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 1000 {
		value = value[:1000]
	}
	return value
}

var _ RuntimeTerminalManager = (*ContainerRuntime)(nil)
