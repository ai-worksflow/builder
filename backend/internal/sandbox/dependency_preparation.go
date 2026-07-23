package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/delivery"
)

const interactiveDependencyFileLimit = 16 << 20

// ProcessDependencyPreparer hydrates ephemeral runtime dependencies before a
// fixed template command starts. It never changes Candidate source authority.
type ProcessDependencyPreparer interface {
	Prepare(context.Context, WorkspaceMount, ResolvedProcessCommand) error
}

type FixedProcessDependencyPreparer struct {
	resolver delivery.DependencyPreparer
	locks    [64]sync.Mutex
}

func NewProcessDependencyPreparer(resolver delivery.DependencyPreparer) (*FixedProcessDependencyPreparer, error) {
	if resolver == nil {
		return nil, errors.New("interactive dependency resolver is required")
	}
	return &FixedProcessDependencyPreparer{resolver: resolver}, nil
}

func (preparer *FixedProcessDependencyPreparer) Prepare(
	ctx context.Context,
	mount WorkspaceMount,
	command ResolvedProcessCommand,
) error {
	if preparer == nil || preparer.resolver == nil || ctx == nil || command.CommandID == "install" {
		return nil
	}
	workingDirectory := filepath.Join(mount.Workspace, filepath.FromSlash(command.WorkingDirectory))
	if !pathWithin(mount.Workspace, workingDirectory) {
		return ErrProcessInvalid
	}
	lock := &preparer.locks[dependencyLockIndex(mount.SessionRoot, command.ServiceID)]
	lock.Lock()
	defer lock.Unlock()

	ecosystem, manifestName, lockName, found, err := dependencyFiles(workingDirectory)
	if err != nil || !found {
		return err
	}
	manifest, err := readRegularDependencyFile(filepath.Join(workingDirectory, manifestName))
	if err != nil {
		return err
	}
	lockFile, err := readRegularDependencyFile(filepath.Join(workingDirectory, lockName))
	if err != nil {
		return err
	}
	digest := dependencyDigest(manifest, lockFile)
	dependencyRoot := filepath.Join(mount.Workspace, ".worksflow", "dependencies")
	if err := os.MkdirAll(dependencyRoot, 0o700); err != nil {
		return fmt.Errorf("create interactive dependency root: %w", err)
	}
	marker := filepath.Join(dependencyRoot, command.ServiceID+"."+ecosystem+".sha256")
	target := filepath.Join(workingDirectory, "node_modules")
	if ecosystem == "go" {
		target = filepath.Join(dependencyRoot, "go", "pkg", "mod")
	}
	if dependencyCacheCurrent(target, marker, digest) {
		return nil
	}

	cache, err := delivery.PrepareDependencyCache(ctx, dependencyRoot, []delivery.WorkspaceFile{
		{Path: manifestName, Content: string(manifest)},
		{Path: lockName, Content: string(lockFile)},
	}, preparer.resolver)
	if err != nil {
		return fmt.Errorf("prepare interactive dependencies: %w", err)
	}
	defer cache.Cleanup()
	if cache.Ecosystem != ecosystem {
		return errors.New("interactive dependency resolver returned an unexpected ecosystem")
	}
	prepared := filepath.Join(cache.Directory, "node_modules")
	if ecosystem == "go" {
		prepared = filepath.Join(cache.Directory, "pkg", "mod")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create interactive dependency cache parent: %w", err)
	}
	if err := replaceDependencyDirectory(target, prepared); err != nil {
		return err
	}
	if err := writeDependencyMarker(marker, digest); err != nil {
		return err
	}
	return nil
}

func dependencyFiles(workingDirectory string) (ecosystem, manifest, lock string, found bool, err error) {
	for _, candidate := range []struct {
		ecosystem string
		manifest  string
		lock      string
	}{
		{ecosystem: "node", manifest: "package.json", lock: "package-lock.json"},
		{ecosystem: "go", manifest: "go.mod", lock: "go.sum"},
	} {
		_, inspectErr := os.Lstat(filepath.Join(workingDirectory, candidate.manifest))
		if inspectErr == nil {
			return candidate.ecosystem, candidate.manifest, candidate.lock, true, nil
		}
		if !errors.Is(inspectErr, os.ErrNotExist) {
			return "", "", "", false, fmt.Errorf("inspect interactive dependency manifest: %w", inspectErr)
		}
	}
	return "", "", "", false, nil
}

func readRegularDependencyFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect interactive dependency file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > interactiveDependencyFileLimit {
		return nil, errors.New("interactive dependency manifest or lock file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open interactive dependency file: %w", err)
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, interactiveDependencyFileLimit+1))
	if err != nil || len(value) > interactiveDependencyFileLimit {
		return nil, errors.New("read interactive dependency file within limit")
	}
	return value, nil
}

func dependencyDigest(manifest, lockFile []byte) string {
	digest := sha256.New()
	_, _ = digest.Write(manifest)
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(lockFile)
	return fmt.Sprintf("sha256:%x", digest.Sum(nil))
}

func dependencyCacheCurrent(target, marker, digest string) bool {
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	value, err := os.ReadFile(marker)
	return err == nil && strings.TrimSpace(string(value)) == digest
}

func replaceDependencyDirectory(target, prepared string) error {
	info, err := os.Lstat(prepared)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prepared interactive dependency directory is invalid")
	}
	backup := filepath.Join(filepath.Dir(prepared), "previous-node_modules-"+uuid.NewString())
	hadTarget := false
	if info, err = os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("existing interactive dependency directory is invalid")
		}
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("stage existing interactive dependencies: %w", err)
		}
		hadTarget = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing interactive dependencies: %w", err)
	}
	if err := os.Rename(prepared, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("publish interactive dependencies: %w", err)
	}
	if hadTarget {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func writeDependencyMarker(path, digest string) error {
	temporary := path + ".tmp-" + uuid.NewString()
	if err := os.WriteFile(temporary, []byte(digest+"\n"), 0o600); err != nil {
		return fmt.Errorf("write interactive dependency marker: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish interactive dependency marker: %w", err)
	}
	return nil
}

func dependencyLockIndex(sessionRoot, serviceID string) int {
	var hash uint32 = 2166136261
	for _, value := range []string{sessionRoot, serviceID} {
		for index := 0; index < len(value); index++ {
			hash ^= uint32(value[index])
			hash *= 16777619
		}
	}
	return int(hash % 64)
}
