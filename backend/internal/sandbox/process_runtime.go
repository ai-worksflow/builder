package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const RuntimeProcessSchemaVersion = "sandbox-process/v1"

const runtimeProcessExecutable = "/usr/local/bin/worksflow-sandbox-process"

var (
	ErrProcessInvalid  = errors.New("invalid sandbox process request")
	ErrProcessNotFound = errors.New("sandbox process was not found")
)

var processNamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,79}$`)

type RuntimeProcessSpec struct {
	Runtime          RuntimeSpec
	ID               string
	CommandID        string
	WorkingDirectory string
	Argv             []string
	LogLimitBytes    int64
}

type RuntimeProcessStatus struct {
	SchemaVersion    string    `json:"schemaVersion"`
	ID               string    `json:"id"`
	State            string    `json:"state"`
	PID              int       `json:"pid,omitempty"`
	Argv             []string  `json:"argv"`
	WorkingDirectory string    `json:"workingDirectory"`
	ExitCode         *int      `json:"exitCode,omitempty"`
	Failure          string    `json:"failure,omitempty"`
	LogBytes         int64     `json:"logBytes"`
	LogTruncated     bool      `json:"logTruncated"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt,omitempty"`
}

type RuntimeProcessLog struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Offset        int64  `json:"offset"`
	NextOffset    int64  `json:"nextOffset"`
	Value         []byte `json:"value"`
	EOF           bool   `json:"eof"`
	Truncated     bool   `json:"truncated"`
}

type RuntimeProcessManager interface {
	StartProcess(context.Context, RuntimeProcessSpec) (RuntimeProcessStatus, error)
	InspectProcess(context.Context, RuntimeProcessSpec) (RuntimeProcessStatus, error)
	SignalProcess(context.Context, RuntimeProcessSpec, string) (RuntimeProcessStatus, error)
	ReadProcessLog(context.Context, RuntimeProcessSpec, int64, int64) (RuntimeProcessLog, error)
}

func (manager *ContainerRuntime) StartProcess(
	ctx context.Context,
	spec RuntimeProcessSpec,
) (RuntimeProcessStatus, error) {
	if err := manager.validateProcess(ctx, spec); err != nil {
		return RuntimeProcessStatus{}, err
	}
	runtimeStatus, err := manager.Inspect(ctx, spec.Runtime)
	if err != nil || runtimeStatus.State != "running" || !runtimeStatus.Healthy {
		if err == nil {
			err = ErrRuntimeNotReady
		}
		return RuntimeProcessStatus{}, err
	}
	if existing, err := manager.inspectProcess(ctx, spec); err == nil {
		if sameRuntimeProcess(existing, spec) {
			return existing, nil
		}
		return RuntimeProcessStatus{}, ErrRuntimeConflict
	} else if !errors.Is(err, ErrProcessNotFound) {
		return RuntimeProcessStatus{}, err
	}
	args := []string{
		"exec", "--detach", manager.containerName(spec.Runtime.SessionID), runtimeProcessExecutable,
		"run", "--id", spec.ID, "--cwd", spec.WorkingDirectory,
		"--log-limit", strconv.FormatInt(spec.LogLimitBytes, 10), "--",
	}
	args = append(args, spec.Argv...)
	if _, err := manager.runCommand(ctx, args...); err != nil {
		if recovered, inspectErr := manager.inspectProcess(ctx, spec); inspectErr == nil && sameRuntimeProcess(recovered, spec) {
			return recovered, nil
		}
		return RuntimeProcessStatus{}, fmt.Errorf("%w: start supervised process: %v", ErrRuntimeUnavailable, err)
	}
	deadline := time.Now().Add(manager.commandTimeout)
	for {
		status, inspectErr := manager.inspectProcess(ctx, spec)
		if inspectErr == nil {
			if !sameRuntimeProcess(status, spec) {
				return RuntimeProcessStatus{}, ErrRuntimeConflict
			}
			return status, nil
		}
		if !errors.Is(inspectErr, ErrProcessNotFound) {
			return RuntimeProcessStatus{}, inspectErr
		}
		if time.Now().After(deadline) {
			return RuntimeProcessStatus{}, fmt.Errorf("%w: process supervisor did not publish state", ErrRuntimeNotReady)
		}
		select {
		case <-ctx.Done():
			return RuntimeProcessStatus{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (manager *ContainerRuntime) InspectProcess(
	ctx context.Context,
	spec RuntimeProcessSpec,
) (RuntimeProcessStatus, error) {
	if err := manager.validateProcess(ctx, spec); err != nil {
		return RuntimeProcessStatus{}, err
	}
	status, err := manager.inspectProcess(ctx, spec)
	if err != nil {
		return RuntimeProcessStatus{}, err
	}
	if !sameRuntimeProcess(status, spec) {
		return RuntimeProcessStatus{}, ErrRuntimeConflict
	}
	return status, nil
}

func (manager *ContainerRuntime) SignalProcess(
	ctx context.Context,
	spec RuntimeProcessSpec,
	signal string,
) (RuntimeProcessStatus, error) {
	if err := manager.validateProcess(ctx, spec); err != nil {
		return RuntimeProcessStatus{}, err
	}
	signal = strings.ToUpper(strings.TrimSpace(signal))
	if signal != "INT" && signal != "TERM" && signal != "KILL" && signal != "HUP" {
		return RuntimeProcessStatus{}, ErrProcessInvalid
	}
	current, err := manager.InspectProcess(ctx, spec)
	if err != nil {
		return RuntimeProcessStatus{}, err
	}
	if current.State != "running" {
		return current, nil
	}
	if _, err := manager.runCommand(
		ctx, "exec", manager.containerName(spec.Runtime.SessionID), runtimeProcessExecutable,
		"signal", "--id", spec.ID, "--name", signal,
	); err != nil {
		return RuntimeProcessStatus{}, fmt.Errorf("%w: signal supervised process: %v", ErrRuntimeUnavailable, err)
	}
	return manager.InspectProcess(ctx, spec)
}

func (manager *ContainerRuntime) ReadProcessLog(
	ctx context.Context,
	spec RuntimeProcessSpec,
	offset, limit int64,
) (RuntimeProcessLog, error) {
	if err := manager.validateProcess(ctx, spec); err != nil || offset < 0 || limit < 1 || limit > 1<<20 {
		return RuntimeProcessLog{}, ErrProcessInvalid
	}
	output, err := manager.runCommand(
		ctx, "exec", manager.containerName(spec.Runtime.SessionID), runtimeProcessExecutable,
		"logs", "--id", spec.ID, "--offset", strconv.FormatInt(offset, 10), "--limit", strconv.FormatInt(limit, 10),
	)
	if err != nil {
		if isRuntimeProcessNotFound(err) {
			return RuntimeProcessLog{}, ErrProcessNotFound
		}
		return RuntimeProcessLog{}, fmt.Errorf("%w: read supervised process log: %v", ErrRuntimeUnavailable, err)
	}
	var encoded struct {
		SchemaVersion string `json:"schemaVersion"`
		ID            string `json:"id"`
		Offset        int64  `json:"offset"`
		NextOffset    int64  `json:"nextOffset"`
		Data          string `json:"data"`
		EOF           bool   `json:"eof"`
		Truncated     bool   `json:"truncated"`
	}
	if err := decodeRuntimeJSON(output, &encoded); err != nil || encoded.SchemaVersion != RuntimeProcessSchemaVersion ||
		encoded.ID != spec.ID || encoded.Offset != offset || encoded.NextOffset < offset ||
		encoded.NextOffset-offset > limit {
		return RuntimeProcessLog{}, ErrRuntimeConflict
	}
	value, err := base64.RawStdEncoding.DecodeString(encoded.Data)
	if err != nil || int64(len(value)) != encoded.NextOffset-offset {
		return RuntimeProcessLog{}, ErrRuntimeConflict
	}
	return RuntimeProcessLog{
		SchemaVersion: RuntimeProcessSchemaVersion, ID: spec.ID,
		Offset: offset, NextOffset: encoded.NextOffset, Value: value,
		EOF: encoded.EOF, Truncated: encoded.Truncated,
	}, nil
}

func (manager *ContainerRuntime) inspectProcess(
	ctx context.Context,
	spec RuntimeProcessSpec,
) (RuntimeProcessStatus, error) {
	output, err := manager.runCommand(
		ctx, "exec", manager.containerName(spec.Runtime.SessionID), runtimeProcessExecutable,
		"status", "--id", spec.ID,
	)
	if err != nil {
		if isRuntimeProcessNotFound(err) {
			return RuntimeProcessStatus{}, ErrProcessNotFound
		}
		return RuntimeProcessStatus{}, fmt.Errorf("%w: inspect supervised process: %v", ErrRuntimeUnavailable, err)
	}
	var status RuntimeProcessStatus
	if err := decodeRuntimeJSON(output, &status); err != nil || validateRuntimeProcessStatus(status) != nil {
		return RuntimeProcessStatus{}, ErrRuntimeConflict
	}
	status.Argv = append([]string(nil), status.Argv...)
	if status.ExitCode != nil {
		exitCode := *status.ExitCode
		status.ExitCode = &exitCode
	}
	return status, nil
}

func (manager *ContainerRuntime) validateProcess(ctx context.Context, spec RuntimeProcessSpec) error {
	if manager == nil || ctx == nil || validateRuntimeSpec(spec.Runtime) != nil || !validUUID(spec.ID) ||
		!processNamePattern.MatchString(spec.CommandID) || spec.LogLimitBytes < 1 || spec.LogLimitBytes > 64<<20 {
		return ErrProcessInvalid
	}
	workingDirectory := strings.TrimSpace(spec.WorkingDirectory)
	if workingDirectory == "" {
		return ErrProcessInvalid
	}
	if strings.Contains(workingDirectory, "\\") || path.IsAbs(workingDirectory) || path.Clean(workingDirectory) != workingDirectory ||
		workingDirectory == ".." || strings.HasPrefix(workingDirectory, "../") || strings.ContainsRune(workingDirectory, 0) {
		return ErrProcessInvalid
	}
	if len(spec.Argv) == 0 || len(spec.Argv) > 64 {
		return ErrProcessInvalid
	}
	for _, argument := range spec.Argv {
		if strings.TrimSpace(argument) == "" || len(argument) > 4096 || strings.ContainsAny(argument, "\x00\r\n") {
			return ErrProcessInvalid
		}
	}
	executable := strings.ToLower(path.Base(spec.Argv[0]))
	for _, shell := range []string{"sh", "bash", "dash", "zsh", "fish", "cmd", "powershell", "pwsh"} {
		if executable == shell {
			return ErrProcessInvalid
		}
	}
	if spec.Runtime.SessionID == "" || spec.Runtime.SessionEpoch == 0 {
		return ErrProcessInvalid
	}
	return nil
}

func validateRuntimeProcessStatus(status RuntimeProcessStatus) error {
	if status.SchemaVersion != RuntimeProcessSchemaVersion || !validUUID(status.ID) || status.StartedAt.IsZero() ||
		len(status.Argv) == 0 || status.LogBytes < 0 || len(status.Failure) > 1000 {
		return ErrRuntimeConflict
	}
	switch status.State {
	case "starting":
		if status.PID != 0 || status.ExitCode != nil || !status.FinishedAt.IsZero() {
			return ErrRuntimeConflict
		}
	case "running":
		if status.PID < 2 || status.ExitCode != nil || !status.FinishedAt.IsZero() {
			return ErrRuntimeConflict
		}
	case "exited":
		if status.PID < 2 || status.ExitCode == nil || status.FinishedAt.IsZero() || status.FinishedAt.Before(status.StartedAt) {
			return ErrRuntimeConflict
		}
	case "failed":
		if status.PID < 0 || status.ExitCode == nil || status.FinishedAt.IsZero() || status.FinishedAt.Before(status.StartedAt) {
			return ErrRuntimeConflict
		}
	default:
		return ErrRuntimeConflict
	}
	return nil
}

func sameRuntimeProcess(status RuntimeProcessStatus, spec RuntimeProcessSpec) bool {
	if status.ID != spec.ID || status.WorkingDirectory != spec.WorkingDirectory || len(status.Argv) != len(spec.Argv) {
		return false
	}
	for index := range status.Argv {
		if status.Argv[index] != spec.Argv[index] {
			return false
		}
	}
	return true
}

func decodeRuntimeJSON(value []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrRuntimeConflict
	}
	return nil
}

func isRuntimeProcessNotFound(err error) bool {
	var commandError *runtimeCommandError
	if !errors.As(err, &commandError) {
		return false
	}
	value := strings.ToLower(commandError.output)
	return strings.Contains(value, "no such file") || strings.Contains(value, "not found") ||
		strings.Contains(value, "invalid process state")
}

var _ RuntimeProcessManager = (*ContainerRuntime)(nil)
