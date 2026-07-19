package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	verificationCleanupTimeout = 15 * time.Second
	verificationLockDirectory  = ".execution-locks"
	verificationRuntimeFence   = ".runtime-fence"
)

var ErrVerificationOperationNotQuiesced = errors.New("verification operation did not quiesce")

// VerificationExecutionFence identifies one exact claimed Attempt generation.
// Cleanup authority is deliberately narrower than a Run or Attempt ID because
// either may already have been reclaimed under a newer fence.
type VerificationExecutionFence struct {
	ProjectID         string
	RunID             string
	AttemptID         string
	AttemptFenceEpoch uint64
}

// VerificationEnvironmentCleanup tells an execution environment whether the
// exact fence still owns attempt-wide resources whose runtime names predate
// fence-qualified naming. Exact-fence resources must always be cleaned; shared
// resources may only be removed when OwnsSharedRuntime is true.
type VerificationEnvironmentCleanup struct {
	Fence             VerificationExecutionFence
	OwnsSharedRuntime bool
}

func candidateExecutionFence(lease CandidateExecutionLease) VerificationExecutionFence {
	return VerificationExecutionFence{
		ProjectID: lease.ProjectID, RunID: lease.RunID, AttemptID: lease.AttemptID,
		AttemptFenceEpoch: lease.AttemptFenceEpoch,
	}
}

func canonicalExecutionFence(lease CanonicalExecutionLease) VerificationExecutionFence {
	return VerificationExecutionFence{
		ProjectID: lease.ProjectID, RunID: lease.RunID, AttemptID: lease.AttemptID,
		AttemptFenceEpoch: lease.AttemptFenceEpoch,
	}
}

func validateVerificationExecutionFence(fence VerificationExecutionFence) error {
	if !validUUIDs(fence.ProjectID, fence.RunID, fence.AttemptID) || fence.AttemptFenceEpoch == 0 {
		return fmt.Errorf("%w: invalid exact execution fence", ErrCandidateMaterialization)
	}
	return nil
}

func verificationCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), verificationCleanupTimeout)
}

func verificationCleanupLeaseDuration(executionLease time.Duration) time.Duration {
	minimum := verificationCleanupTimeout * 2
	if executionLease > minimum {
		return executionLease
	}
	return minimum
}

func awaitVerificationOperationQuiescence(completed <-chan error, cause error) error {
	timer := time.NewTimer(verificationCleanupTimeout)
	defer timer.Stop()
	select {
	case <-completed:
		return cause
	case <-timer.C:
		return errors.Join(cause, ErrVerificationOperationNotQuiesced)
	}
}

func withVerificationAttemptLock(
	ctx context.Context,
	root string,
	attemptID string,
	operation func(string) error,
) error {
	if ctx == nil || !validUUIDs(attemptID) || operation == nil {
		return fmt.Errorf("%w: invalid execution cleanup lock", ErrCandidateMaterialization)
	}
	lockRoot := filepath.Join(root, verificationLockDirectory)
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		return fmt.Errorf("%w: create execution lock root: %v", ErrCandidateMaterialization, err)
	}
	lockInfo, err := os.Lstat(lockRoot)
	if err != nil || !lockInfo.IsDir() || lockInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: unsafe execution lock root", ErrCandidateMaterialization)
	}
	lockPath := filepath.Join(lockRoot, attemptID)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("%w: open Attempt cleanup lock: %v", ErrCandidateMaterialization, err)
	}
	defer lock.Close()

	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("%w: acquire Attempt cleanup lock: %v", ErrCandidateMaterialization, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // best-effort advisory unlock
	if err := ctx.Err(); err != nil {
		return err
	}
	attemptRoot := filepath.Join(root, attemptID)
	if !pathWithinVerificationRoot(root, attemptRoot) {
		return fmt.Errorf("%w: Attempt cleanup root escaped", ErrCandidateMaterialization)
	}
	if err := os.MkdirAll(attemptRoot, 0o700); err != nil {
		return fmt.Errorf("%w: create Attempt cleanup root: %v", ErrCandidateMaterialization, err)
	}
	info, err := os.Lstat(attemptRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: unsafe Attempt cleanup root", ErrCandidateMaterialization)
	}
	return operation(attemptRoot)
}

func verificationAttemptFences(attemptRoot string) ([]uint64, error) {
	entries, err := os.ReadDir(attemptRoot)
	if err != nil {
		return nil, err
	}
	fences := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == verificationRuntimeFence {
			continue
		}
		if strings.HasPrefix(name, ".runtime-fence-") && entry.Type().IsRegular() {
			if err := os.Remove(filepath.Join(attemptRoot, name)); err != nil {
				return nil, fmt.Errorf("remove abandoned runtime marker %q: %w", name, err)
			}
			continue
		}
		fence, parseErr := strconv.ParseUint(name, 10, 64)
		if parseErr != nil || fence == 0 || !entry.IsDir() {
			return nil, fmt.Errorf("unsafe entry %q in Attempt execution root", name)
		}
		fences = append(fences, fence)
	}
	sort.Slice(fences, func(left, right int) bool { return fences[left] < fences[right] })
	return fences, nil
}

func removeEmptyVerificationAttemptRoot(attemptRoot string) error {
	entries, err := os.ReadDir(attemptRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || len(entries) != 0 {
		return err
	}
	return os.Remove(attemptRoot)
}

func readVerificationRuntimeFence(attemptRoot string) (uint64, error) {
	value, err := os.ReadFile(filepath.Join(attemptRoot, verificationRuntimeFence))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	fence, err := strconv.ParseUint(strings.TrimSpace(string(value)), 10, 64)
	if err != nil || fence == 0 {
		return 0, errors.New("invalid runtime fence marker")
	}
	return fence, nil
}

func writeVerificationRuntimeFence(attemptRoot string, fence uint64) error {
	if fence == 0 {
		return errors.New("invalid runtime fence")
	}
	staging, err := os.CreateTemp(attemptRoot, ".runtime-fence-")
	if err != nil {
		return err
	}
	stagingPath := staging.Name()
	defer os.Remove(stagingPath)
	if err := staging.Chmod(0o600); err != nil {
		_ = staging.Close()
		return err
	}
	if _, err := staging.WriteString(strconv.FormatUint(fence, 10) + "\n"); err != nil {
		_ = staging.Close()
		return err
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()
		return err
	}
	if err := staging.Close(); err != nil {
		return err
	}
	return os.Rename(stagingPath, filepath.Join(attemptRoot, verificationRuntimeFence))
}

func removeVerificationRuntimeFence(attemptRoot string, expected uint64) error {
	actual, err := readVerificationRuntimeFence(attemptRoot)
	if err != nil {
		return err
	}
	if actual != expected {
		return nil
	}
	return os.Remove(filepath.Join(attemptRoot, verificationRuntimeFence))
}

func containsNewerVerificationFence(fences []uint64, marker, fence uint64) bool {
	if marker > fence {
		return true
	}
	return len(fences) > 0 && fences[len(fences)-1] > fence
}

func uniqueVerificationFences(values []uint64) []uint64 {
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	result := values[:0]
	var previous uint64
	for _, value := range values {
		if value == 0 || value == previous {
			continue
		}
		result = append(result, value)
		previous = value
	}
	return result
}
