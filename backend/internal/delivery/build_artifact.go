package delivery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

var staticOutputDirectories = []string{"dist", "out", "build"}

func captureBuildArtifact(directory string, reference core.VersionRef, allowStaticRoot bool) (BuildArtifact, error) {
	root, err := selectStaticOutputRoot(directory, allowStaticRoot)
	if err != nil {
		return BuildArtifact{}, err
	}
	files := make([]BuildArtifactFile, 0)
	totalBytes := int64(0)
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return NewError(CodeUnsafePath, 422, "static build output contains a symbolic link")
		}
		if entry.IsDir() {
			name := strings.ToLower(entry.Name())
			if name == ".git" || name == "node_modules" || name == ".next" || name == ".worksflow" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return NewError(CodeUnsafePath, 422, "static build output contains a non-regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		safePath, err := SanitizePath(relative)
		if err != nil {
			return err
		}
		canonical := strings.ToLower(safePath)
		if seen[canonical] {
			return Invalid("buildArtifact.files", "static build output contains duplicate case-insensitive paths")
		}
		seen[canonical] = true
		if info.Size() > MaxWorkspaceFileSize {
			return NewError(CodeOutputLimit, 413, "static build output contains a file larger than the configured limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		totalBytes += int64(len(data))
		if totalBytes > MaxBuildArtifactBytes {
			return NewError(CodeOutputLimit, 413, "static build output exceeds the content-addressed artifact limit")
		}
		if SensitivePath(safePath) {
			return NewError(CodeSensitiveContent, 409, "static build output contains a secret-bearing path")
		}
		if utf8.Valid(data) {
			if _, found := SensitiveFinding(string(data)); found {
				return NewError(CodeSensitiveContent, 409, "static build output contains an embedded credential")
			}
		}
		files = append(files, BuildArtifactFile{Path: safePath, ContentBase64: base64.StdEncoding.EncodeToString(data)})
		if len(files) > MaxWorkspaceFiles {
			return NewError(CodeOutputLimit, 413, "static build output contains too many files")
		}
		return nil
	})
	if err != nil {
		if deliveryError, ok := AsError(err); ok {
			return BuildArtifact{}, deliveryError
		}
		return BuildArtifact{}, wrapInternal("capture static build output", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	entryPath, err := selectBuildEntry(files, "")
	if err != nil {
		return BuildArtifact{}, err
	}
	buildHash, err := hashBuildFiles(files)
	if err != nil {
		return BuildArtifact{}, err
	}
	return BuildArtifact{
		ID: uuid.NewString(), WorkspaceRevision: reference, BuildHash: buildHash,
		EntryPath: entryPath, Files: files, FileCount: len(files), TotalBytes: totalBytes,
	}, nil
}

func selectStaticOutputRoot(workspace string, allowStaticRoot bool) (string, error) {
	for _, candidate := range staticOutputDirectories {
		root := filepath.Join(workspace, candidate)
		if regularFile(filepath.Join(root, "index.html")) {
			return root, nil
		}
	}
	if allowStaticRoot && regularFile(filepath.Join(workspace, "index.html")) {
		return workspace, nil
	}
	return "", Invalid("buildArtifact.entryPath", "quality passed no publishable static entry; expected index.html in dist, out, build, or an explicitly static workspace root")
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func selectBuildEntry(files []BuildArtifactFile, requested string) (string, error) {
	byPath := make(map[string]bool, len(files))
	for _, file := range files {
		byPath[file.Path] = true
	}
	if requested != "" {
		path, err := SanitizePath(requested)
		if err != nil {
			return "", err
		}
		if !byPath[path] || !strings.HasSuffix(strings.ToLower(path), ".html") {
			return "", Invalid("entryPath", "entryPath must identify an HTML file in the immutable build artifact")
		}
		return path, nil
	}
	if byPath["index.html"] {
		return "index.html", nil
	}
	return "", Invalid("buildArtifact.entryPath", "immutable build artifact must contain index.html at its root")
}

func decodeBuildFile(file BuildArtifactFile) ([]byte, error) {
	path, err := SanitizePath(file.Path)
	if err != nil || path != file.Path {
		return nil, Invalid("buildArtifact.files", "build artifact contains an unsafe path")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(file.ContentBase64)
	if err != nil {
		return nil, Invalid("buildArtifact.files", "build artifact contains invalid base64 content")
	}
	if len(decoded) > MaxWorkspaceFileSize {
		return nil, NewError(CodeOutputLimit, 413, "build artifact file exceeds the configured limit")
	}
	return decoded, nil
}

func hashBuildFiles(files []BuildArtifactFile) (string, error) {
	ordered := append([]BuildArtifactFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	hash := sha256.New()
	seen := map[string]bool{}
	total := int64(0)
	for _, file := range ordered {
		data, err := decodeBuildFile(file)
		if err != nil {
			return "", err
		}
		canonical := strings.ToLower(file.Path)
		if seen[canonical] {
			return "", Invalid("buildArtifact.files", "build artifact contains duplicate case-insensitive paths")
		}
		seen[canonical] = true
		total += int64(len(data))
		if total > MaxBuildArtifactBytes {
			return "", NewError(CodeOutputLimit, 413, "build artifact exceeds the configured limit")
		}
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func validateBuildArtifact(artifact BuildArtifact) error {
	if _, err := uuid.Parse(artifact.ID); err != nil {
		return Invalid("buildArtifact.id", "build artifact id must be a UUID")
	}
	if err := ValidateVersionRef(artifact.WorkspaceRevision); err != nil {
		return err
	}
	if len(artifact.Files) == 0 || len(artifact.Files) > MaxWorkspaceFiles {
		return Invalid("buildArtifact.files", "build artifact must contain bounded static files")
	}
	entryPath, err := selectBuildEntry(artifact.Files, artifact.EntryPath)
	if err != nil || entryPath != artifact.EntryPath {
		return Invalid("buildArtifact.entryPath", "build artifact entry is invalid")
	}
	hash, err := hashBuildFiles(artifact.Files)
	if err != nil {
		return err
	}
	if hash != artifact.BuildHash {
		return conflict("immutable build artifact hash does not match its files")
	}
	count := len(artifact.Files)
	total := int64(0)
	for _, file := range artifact.Files {
		data, err := decodeBuildFile(file)
		if err != nil {
			return err
		}
		total += int64(len(data))
	}
	if artifact.FileCount != count || artifact.TotalBytes != total {
		return conflict("immutable build artifact counts do not match its files")
	}
	return nil
}

func referenceForBuild(artifact BuildArtifact, contentRef, contentHash string) BuildArtifactReference {
	return BuildArtifactReference{
		ID: artifact.ID, ContentRef: contentRef, ContentHash: contentHash,
		BuildHash: artifact.BuildHash, EntryPath: artifact.EntryPath,
		FileCount: artifact.FileCount, TotalBytes: artifact.TotalBytes,
	}
}

func validateBuildReference(reference BuildArtifactReference) error {
	if _, err := uuid.Parse(reference.ID); err != nil {
		return Invalid("buildArtifact.id", "build artifact id must be a UUID")
	}
	if _, err := uuid.Parse(reference.ContentRef); err != nil {
		return Invalid("buildArtifact.contentRef", "build artifact content reference must be a UUID")
	}
	if !validSHA256(reference.ContentHash) || !validSHA256(reference.BuildHash) {
		return Invalid("buildArtifact", "build artifact content and tree hashes must be sha256 digests")
	}
	if _, err := SanitizePath(reference.EntryPath); err != nil {
		return err
	}
	if reference.FileCount <= 0 || reference.FileCount > MaxWorkspaceFiles || reference.TotalBytes < 0 || reference.TotalBytes > MaxBuildArtifactBytes {
		return Invalid("buildArtifact", "build artifact counts exceed configured limits")
	}
	return nil
}

func validSHA256(value string) bool {
	digest := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size && value == "sha256:"+strings.ToLower(digest)
}

func buildArtifactMatchesReference(artifact BuildArtifact, reference BuildArtifactReference) bool {
	return artifact.ID == reference.ID && artifact.BuildHash == reference.BuildHash &&
		artifact.EntryPath == reference.EntryPath && artifact.FileCount == reference.FileCount &&
		artifact.TotalBytes == reference.TotalBytes
}

func wrapBuildCaptureError(err error) error {
	if err == nil {
		return nil
	}
	var typed *DeliveryError
	if errors.As(err, &typed) {
		return typed
	}
	return fmt.Errorf("capture immutable build artifact: %w", err)
}
