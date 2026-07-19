package sandbox

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
)

type runtimeStreamFake struct {
	input  *captureWriteCloser
	output io.ReadCloser
	wait   error
}

func (stream *runtimeStreamFake) Input() io.WriteCloser { return stream.input }
func (stream *runtimeStreamFake) Output() io.ReadCloser { return stream.output }
func (stream *runtimeStreamFake) Wait() error           { return stream.wait }

type captureWriteCloser struct {
	mu     sync.Mutex
	value  bytes.Buffer
	closed bool
}

func (writer *captureWriteCloser) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.value.Write(value)
}
func (writer *captureWriteCloser) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.closed = true
	return nil
}

type terminalRuntimeExecutorFake struct {
	*containerRuntimeExecutorFake
	args   []string
	stream *runtimeStreamFake
}

func (executor *terminalRuntimeExecutorFake) Start(_ context.Context, args ...string) (runtimeCommandStream, error) {
	executor.args = append([]string(nil), args...)
	return executor.stream, nil
}

func TestContainerRuntimeOpensOnlyFixedPTYHelper(t *testing.T) {
	manager, base, runtimeSpec := containerRuntimeFixture(t)
	if _, err := manager.Start(context.Background(), runtimeSpec); err != nil {
		t.Fatal(err)
	}
	executor := &terminalRuntimeExecutorFake{
		containerRuntimeExecutorFake: base,
		stream:                       &runtimeStreamFake{input: &captureWriteCloser{}, output: io.NopCloser(bytes.NewReader([]byte("ready\r\n")))},
	}
	manager.executor = executor
	manager.streamer = executor
	terminal, err := manager.OpenTerminal(context.Background(), RuntimeTerminalSpec{
		Runtime: runtimeSpec, ID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		WorkingDirectory: ".", Rows: 24, Columns: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.args) < 5 || executor.args[0] != "exec" || executor.args[1] != "-i" ||
		executor.args[3] != runtimeTerminalExecutable {
		t.Fatalf("unsafe runtime args: %#v", executor.args)
	}
	if err := terminal.WriteInput([]byte("pwd\r")); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Resize(40, 120); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Signal("INT"); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	var output []byte
	for value := range terminal.Output() {
		output = append(output, value...)
	}
	if string(output) != "ready\r\n" {
		t.Fatalf("unexpected output: %q", output)
	}
	<-terminal.Done()
	if executor.stream.input.value.Len() == 0 || !executor.stream.input.closed {
		t.Fatal("PTY control stream was not written and closed")
	}
}

func TestContainerRuntimeRejectsUnsafePTYShape(t *testing.T) {
	manager, _, runtimeSpec := containerRuntimeFixture(t)
	for _, directory := range []string{"../outside", "/workspace", "frontend/../frontend"} {
		_, err := manager.OpenTerminal(context.Background(), RuntimeTerminalSpec{
			Runtime: runtimeSpec, ID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
			WorkingDirectory: directory, Rows: 24, Columns: 80,
		})
		if err == nil {
			t.Fatalf("unsafe directory accepted: %q", directory)
		}
	}
}
