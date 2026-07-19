package qualificationreceipt

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	trustedGitBinary                    = "/usr/bin/git"
	gitOutputLimit                      = 32 << 20
	maxTrackedFiles                     = 100000
	SourceContentTreeCommitmentSchemaV1 = "worksflow-source-content-tree/v1"
)

// SourceContentTreeEntry is the object-format-independent material committed
// by SourceBinding.TreeDigest. Mode is the canonical Git file mode (100644 or
// 100755), while SHA256 is always calculated from the actual tracked bytes.
type SourceContentTreeEntry struct {
	Path      string
	Mode      string
	SizeBytes int64
	SHA256    string
}

// ComputeSourceContentTreeDigest calculates the canonical content authority
// shared by the root authority issuer and this verifier. Its binary framing is:
// domain NUL | u32 entry-count | repeated(u32 path-len | raw UTF-8 path |
// six-byte mode | u64 size | 32-byte SHA-256), with entries sorted by raw path
// bytes. No Git SHA-1 or SHA-256 object identifier is included.
func ComputeSourceContentTreeDigest(entries []SourceContentTreeEntry) (string, error) {
	if len(entries) == 0 || len(entries) > maxTrackedFiles {
		return "", fmt.Errorf("source content tree must contain 1..%d entries", maxTrackedFiles)
	}
	ordered := append([]SourceContentTreeEntry(nil), entries...)
	sort.Slice(ordered, func(left, right int) bool {
		return bytes.Compare([]byte(ordered[left].Path), []byte(ordered[right].Path)) < 0
	})

	hasher := sha256.New()
	_, _ = hasher.Write([]byte(SourceContentTreeCommitmentSchemaV1))
	_, _ = hasher.Write([]byte{0})
	var uint32Buffer [4]byte
	binary.BigEndian.PutUint32(uint32Buffer[:], uint32(len(ordered)))
	_, _ = hasher.Write(uint32Buffer[:])
	var uint64Buffer [8]byte
	var aggregateSize int64
	for index, entry := range ordered {
		if !validGitWorktreePath(entry.Path) || (entry.Mode != "100644" && entry.Mode != "100755") ||
			entry.SizeBytes < 0 || entry.SizeBytes > maxArtifactBytes ||
			aggregateSize > maxArtifactBytes-entry.SizeBytes || !validDigest(entry.SHA256) {
			return "", fmt.Errorf("source content tree entry %d is non-canonical", index)
		}
		if index > 0 && bytes.Equal([]byte(ordered[index-1].Path), []byte(entry.Path)) {
			return "", fmt.Errorf("source content tree contains duplicate path %q", entry.Path)
		}
		aggregateSize += entry.SizeBytes
		contentDigest, err := hex.DecodeString(strings.TrimPrefix(entry.SHA256, "sha256:"))
		if err != nil || len(contentDigest) != sha256.Size {
			return "", fmt.Errorf("source content tree entry %d has an invalid content digest", index)
		}
		pathBytes := []byte(entry.Path)
		binary.BigEndian.PutUint32(uint32Buffer[:], uint32(len(pathBytes)))
		_, _ = hasher.Write(uint32Buffer[:])
		_, _ = hasher.Write(pathBytes)
		_, _ = hasher.Write([]byte(entry.Mode))
		binary.BigEndian.PutUint64(uint64Buffer[:], uint64(entry.SizeBytes))
		_, _ = hasher.Write(uint64Buffer[:])
		_, _ = hasher.Write(contentDigest)
	}
	return fmt.Sprintf("sha256:%x", hasher.Sum(nil)), nil
}

// VerifyRepositorySource binds promotion to one clean, read-only Git snapshot.
// Git fsck, commit, and blob IDs establish historical provenance. Promotion
// content authority is separately established by Source.TreeDigest over actual
// bytes, so a SHA-1 repository never makes SHA-1 collision resistance part of
// the promotion decision.
func VerifyRepositorySource(repositoryRoot string, expected SourceBinding, gitExecutableDigest string) error {
	if validateSource(expected) != nil || !validDigest(gitExecutableDigest) || !filepath.IsAbs(repositoryRoot) || filepath.Clean(repositoryRoot) != repositoryRoot {
		return errors.New("repository snapshot authority is invalid")
	}
	resolvedRoot, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil || resolvedRoot != repositoryRoot {
		return errors.New("repository snapshot root must not contain symlink components")
	}
	if err := requireReadOnlyMount(repositoryRoot); err != nil {
		return fmt.Errorf("repository snapshot must be a read-only mount: %w", err)
	}
	gitDirectory := filepath.Join(repositoryRoot, ".git")
	gitInfo, err := os.Lstat(gitDirectory)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("repository snapshot must contain an in-tree, non-symlink .git directory")
	}
	if err := requireReadOnlyMount(gitDirectory); err != nil {
		return fmt.Errorf("repository metadata must be on a read-only mount: %w", err)
	}
	if err := verifySealedGitMetadata(gitDirectory); err != nil {
		return err
	}
	for _, alternate := range []string{
		filepath.Join(gitDirectory, "objects", "info", "alternates"),
		filepath.Join(gitDirectory, "objects", "info", "http-alternates"),
	} {
		if _, err := os.Lstat(alternate); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("repository snapshot must not configure alternate object directories")
		}
	}
	if err := rejectReplacementRefs(gitDirectory); err != nil {
		return err
	}
	if err := validateTrustedGitBinary(gitExecutableDigest); err != nil {
		return err
	}
	if _, err := runTrustedGit(repositoryRoot, "fsck", "--strict", "--full", "--no-progress"); err != nil {
		return errors.New("repository object database failed strict full integrity verification")
	}
	commit, err := runTrustedGit(repositoryRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSuffix(commit, "\n") != expected.Commit {
		return errors.New("repository HEAD commit does not match the server-owned source authority")
	}
	objectFormat, err := runTrustedGit(repositoryRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return errors.New("repository object format could not be verified")
	}
	treeDigest, err := verifyExactGitWorktree(repositoryRoot, strings.TrimSuffix(objectFormat, "\n"))
	if err != nil {
		return err
	}
	if treeDigest != expected.TreeDigest {
		return errors.New("repository actual-byte SHA-256 content tree does not match the server-owned source authority")
	}
	if err := validateTrustedGitBinary(gitExecutableDigest); err != nil {
		return errors.New("trusted Git executable changed while repository source was verified")
	}
	return nil
}

func validateTrustedGitBinary(expectedDigest string) error {
	if !validDigest(expectedDigest) {
		return errors.New("trusted Git executable digest is invalid")
	}
	info, err := os.Lstat(trustedGitBinary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("trusted Git binary must be a non-writable, non-symlink regular file")
	}
	if err := validateRootOwnedAuthorityAncestors(filepath.Dir(trustedGitBinary)); err != nil {
		return fmt.Errorf("trusted Git binary hierarchy: %w", err)
	}
	if !validRootOwnedExecutableInfo(info) {
		return errors.New("trusted Git binary must be root-owned, executable, and single-linked")
	}
	descriptor, err := unix.Open(trustedGitBinary, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open trusted Git executable: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), trustedGitBinary)
	if file == nil {
		unix.Close(descriptor)
		return errors.New("open trusted Git executable descriptor")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !validRootOwnedExecutableInfo(opened) || opened.Size() <= 0 || opened.Size() > maxArtifactBytes {
		return errors.New("trusted Git executable identity changed while opening")
	}
	hasher := sha256.New()
	read, err := io.Copy(hasher, io.LimitReader(file, maxArtifactBytes+1))
	if err != nil || read != opened.Size() {
		return errors.New("trusted Git executable could not be hashed completely")
	}
	after, err := file.Stat()
	pathInfo, pathErr := os.Lstat(trustedGitBinary)
	if err != nil || pathErr != nil || !os.SameFile(opened, after) || !os.SameFile(after, pathInfo) ||
		after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || !validRootOwnedExecutableInfo(after) {
		return errors.New("trusted Git executable changed while hashing")
	}
	actualDigest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	if actualDigest != expectedDigest {
		return errors.New("trusted Git executable does not match the server-owned authority digest")
	}
	return nil
}

func validRootOwnedExecutableInfo(info os.FileInfo) bool {
	if info == nil || info.Mode().Perm()&0o111 == 0 || hardLinkCount(info) != 1 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0

}

func verifySealedGitMetadata(gitDirectory string) error {
	for _, externalPointer := range []string{
		filepath.Join(gitDirectory, "commondir"), filepath.Join(gitDirectory, "gitdir"),
	} {
		if _, err := os.Lstat(externalPointer); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("repository metadata must not use linked-worktree or external common-directory pointers")
		}
	}
	rootInfo, err := os.Lstat(gitDirectory)
	if err != nil {
		return err
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("repository metadata device identity is unavailable")
	}
	var aggregate int64
	err = filepath.WalkDir(gitDirectory, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := requireReadOnlyMount(current); err != nil {
			return fmt.Errorf("repository metadata path %q is not on the immutable filesystem: %w", current, err)
		}
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("repository metadata contains an unsafe path %q", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Dev != rootStat.Dev {
			return fmt.Errorf("repository metadata path %q escapes the sealed snapshot device", current)
		}
		if info.Mode().IsRegular() {
			if hardLinkCount(info) != 1 {
				return fmt.Errorf("repository metadata file %q must have exactly one link", current)
			}
			if info.Size() < 0 || info.Size() > maxArtifactBytes || aggregate > maxArtifactBytes-info.Size() {
				return errors.New("repository metadata exceeds the aggregate inspection limit")
			}
			aggregate += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	configPath := filepath.Join(gitDirectory, "config")
	config, err := readBoundedRegularFile(configPath, 1<<20, false)
	if err != nil {
		return errors.New("repository local config could not be safely inspected")
	}
	lowerConfig := strings.ToLower(string(config))
	if strings.Contains(lowerConfig, "[include") || strings.Contains(lowerConfig, "worktree") || strings.Contains(lowerConfig, "alternates") {
		return errors.New("repository local config contains an external include, worktree, or alternate-object directive")
	}
	return nil
}

func verifyExactGitWorktree(repositoryRoot, objectFormat string) (string, error) {
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return "", errors.New("repository uses an unsupported Git object format")
	}
	listing, err := runTrustedGit(repositoryRoot, "ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if err != nil {
		return "", errors.New("repository HEAD tree could not be enumerated")
	}
	entries := strings.Split(listing, "\x00")
	if len(entries) == 0 || len(entries)-1 > maxTrackedFiles || entries[len(entries)-1] != "" {
		return "", errors.New("repository HEAD tree enumeration is empty, oversized, or non-canonical")
	}
	tracked := make(map[string]struct{}, len(entries)-1)
	contentEntries := make([]SourceContentTreeEntry, 0, len(entries)-1)
	var aggregateSize int64
	for _, entry := range entries[:len(entries)-1] {
		tab := strings.IndexByte(entry, '\t')
		if tab <= 0 || tab == len(entry)-1 {
			return "", errors.New("repository HEAD tree entry is malformed")
		}
		metadata := strings.Fields(entry[:tab])
		path := entry[tab+1:]
		if len(metadata) != 3 || metadata[1] != "blob" || (metadata[0] != "100644" && metadata[0] != "100755") ||
			!validGitWorktreePath(path) || !validGitObjectID(metadata[2], objectFormat) {
			return "", fmt.Errorf("repository HEAD contains unsupported mode, object, or path %q", path)
		}
		if _, duplicate := tracked[path]; duplicate {
			return "", fmt.Errorf("repository HEAD contains duplicate path %q", path)
		}
		absolute := filepath.Join(repositoryRoot, filepath.FromSlash(path))
		info, err := os.Lstat(absolute)
		if err != nil || info.Size() < 0 || info.Size() > maxArtifactBytes || aggregateSize > maxArtifactBytes-info.Size() {
			return "", errors.New("repository tracked source exceeds the aggregate size limit")
		}
		aggregateSize += info.Size()
		if err := requireReadOnlyMount(absolute); err != nil {
			return "", fmt.Errorf("tracked source %q is not on the immutable filesystem: %w", path, err)
		}
		actualObjectID, contentDigest, executable, size, err := hashStableGitBlob(absolute, objectFormat)
		if err != nil {
			return "", fmt.Errorf("verify tracked source %q: %w", path, err)
		}
		if actualObjectID != metadata[2] || executable != (metadata[0] == "100755") {
			return "", fmt.Errorf("tracked source %q does not exactly match HEAD", path)
		}
		if size != info.Size() {
			return "", fmt.Errorf("tracked source %q size changed during verification", path)
		}
		contentEntries = append(contentEntries, SourceContentTreeEntry{
			Path: path, Mode: metadata[0], SizeBytes: size, SHA256: contentDigest,
		})
		tracked[path] = struct{}{}
	}
	seen := 0
	err = filepath.WalkDir(repositoryRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == filepath.Join(repositoryRoot, ".git") {
			return filepath.SkipDir
		}
		if current == repositoryRoot || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(repositoryRoot, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("repository snapshot contains non-regular source path %q", relative)
		}
		if _, exists := tracked[relative]; !exists {
			return fmt.Errorf("repository snapshot contains untracked source path %q", relative)
		}
		seen++
		return nil
	})
	if err != nil {
		return "", err
	}
	if seen != len(tracked) {
		return "", errors.New("repository snapshot file closure does not match HEAD")
	}
	contentTreeDigest, err := ComputeSourceContentTreeDigest(contentEntries)
	if err != nil {
		return "", fmt.Errorf("compute repository source content tree: %w", err)
	}
	return contentTreeDigest, nil
}

func validGitWorktreePath(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, utf8.RuneError) ||
		strings.Contains(value, "\\") || strings.ContainsAny(value, "\r\n\x00") || path.IsAbs(value) || path.Clean(value) != value ||
		value == "." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || component == ".git" {
			return false
		}
	}
	return true
}

func validGitObjectID(value, objectFormat string) bool {
	expectedLength := 40
	if objectFormat == "sha256" {
		expectedLength = 64
	}
	if len(value) != expectedLength {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func hashStableGitBlob(filePath, objectFormat string) (string, string, bool, int64, error) {
	before, err := os.Lstat(filePath)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maxArtifactBytes || hardLinkCount(before) != 1 {
		return "", "", false, 0, errors.New("source must be a bounded, single-linked regular file")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", false, 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", "", false, 0, errors.New("source identity changed while opening")
	}
	var hasher hash.Hash
	if objectFormat == "sha1" {
		hasher = sha1.New() // Git object identity; promotion trust still uses SHA-256 evidence digests.
	} else {
		hasher = sha256.New()
	}
	if _, err := fmt.Fprintf(hasher, "blob %d%c", opened.Size(), byte(0)); err != nil {
		return "", "", false, 0, err
	}
	contentHasher := sha256.New()
	read, err := io.Copy(io.MultiWriter(hasher, contentHasher), io.LimitReader(file, maxArtifactBytes+1))
	if err != nil || read != opened.Size() {
		return "", "", false, 0, errors.New("source changed size or failed while hashing")
	}
	after, err := os.Lstat(filePath)
	if err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || hardLinkCount(after) != 1 {
		return "", "", false, 0, errors.New("source changed while hashing")
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), fmt.Sprintf("sha256:%x", contentHasher.Sum(nil)), opened.Mode().Perm()&0o111 != 0, opened.Size(), nil
}

func rejectReplacementRefs(gitDirectory string) error {
	replaceDirectory := filepath.Join(gitDirectory, "refs", "replace")
	entries, err := os.ReadDir(replaceDirectory)
	if err == nil && len(entries) != 0 {
		return errors.New("repository snapshot must not contain replacement refs")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("repository replacement refs could not be inspected")
	}
	packedRefs := filepath.Join(gitDirectory, "packed-refs")
	encoded, err := os.ReadFile(packedRefs)
	if err == nil {
		if len(encoded) > gitOutputLimit || bytes.Contains(encoded, []byte(" refs/replace/")) {
			return errors.New("repository packed refs contain replacement refs or exceed the inspection limit")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("repository packed refs could not be inspected")
	}
	return nil
}

func runTrustedGit(repositoryRoot string, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commandArguments := []string{
		"--no-replace-objects", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"--git-dir=" + filepath.Join(repositoryRoot, ".git"), "--work-tree=" + repositoryRoot,
	}
	commandArguments = append(commandArguments, arguments...)
	command := exec.CommandContext(ctx, trustedGitBinary, commandArguments...)
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=/nonexistent", "XDG_CONFIG_HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	}
	var stdout, stderr limitedBuffer
	stdout.maximum = gitOutputLimit
	stderr.maximum = 64 << 10
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("trusted Git command failed: %w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	maximum int
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.maximum - buffer.Len()
	if remaining <= 0 || len(value) > remaining {
		return 0, errors.New("command output exceeds the trusted limit")
	}
	return buffer.Buffer.Write(value)
}
