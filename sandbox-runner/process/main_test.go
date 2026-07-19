package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProcessRunnerCapturesBoundedOutputAndExactStatus(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	t.Setenv("WORKSFLOW_PROCESS_TEST_MODE", "1")
	t.Setenv("WORKSFLOW_PROCESS_WORKSPACE", workspace)
	t.Setenv("WORKSFLOW_PROCESS_ROOT", state)
	id := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	if err := runProcess([]string{
		"--id", id, "--cwd", ".", "--log-limit", "5", "--",
		"/usr/bin/printf", "123456789",
	}); err != nil {
		t.Fatal(err)
	}
	status, err := loadState(filepath.Join(state, id))
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "exited" || status.ExitCode == nil || *status.ExitCode != 0 ||
		status.LogBytes != 5 || !status.LogTruncated {
		t.Fatalf("status = %#v", status)
	}
	output, err := os.ReadFile(filepath.Join(state, id, "output.log"))
	if err != nil || !bytes.Equal(output, []byte("12345")) {
		t.Fatalf("output = %q, %v", output, err)
	}
}

func TestProcessStateRejectsUnknownFieldsAndWorkingDirectoryEscape(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	t.Setenv("WORKSFLOW_PROCESS_TEST_MODE", "1")
	t.Setenv("WORKSFLOW_PROCESS_WORKSPACE", workspace)
	t.Setenv("WORKSFLOW_PROCESS_ROOT", state)
	if _, _, err := resolveWorkingDirectory("../escape"); err == nil {
		t.Fatal("working directory escape was accepted")
	}
	id := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	root := filepath.Join(state, id)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{
		"schemaVersion": processContract, "id": id, "state": "running", "unknown": true,
	})
	if err := os.WriteFile(filepath.Join(root, "state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(root); err == nil {
		t.Fatal("unknown process state field was accepted")
	}
}
