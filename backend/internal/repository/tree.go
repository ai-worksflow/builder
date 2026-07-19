package repository

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	TreeSchemaVersion = "repository-tree/v1"
	MaxTreeFiles      = 20_000
	MaxFileBytes      = int64(4 << 20)
	MaxTreeBytes      = int64(64 << 20)
)

var (
	ErrInvalidTree      = errors.New("invalid repository tree")
	ErrTreeConflict     = errors.New("repository tree conflict")
	ErrProtectedPath    = errors.New("repository path is protected")
	ErrTreeLimit        = errors.New("repository tree limit exceeded")
	ErrUnsupportedMode  = errors.New("unsupported repository file mode")
	ErrOperationMissing = errors.New("repository operation target is missing")
	ErrFilePrecondition = errors.New("repository file hash precondition is required")
)

type TreeFile struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	ContentHash string `json:"contentHash"`
	ByteSize    int64  `json:"byteSize"`
}

type TreeManifest struct {
	SchemaVersion string     `json:"schemaVersion"`
	Files         []TreeFile `json:"files"`
	TreeHash      string     `json:"treeHash"`
}

type OperationKind string

const (
	OperationUpsert OperationKind = "file.upsert"
	OperationDelete OperationKind = "file.delete"
	OperationRename OperationKind = "file.rename"
)

type FileOperation struct {
	ID           string        `json:"id"`
	Kind         OperationKind `json:"kind"`
	Path         string        `json:"path"`
	FromPath     string        `json:"fromPath,omitempty"`
	ExpectedHash string        `json:"expectedHash,omitempty"`
	ContentHash  string        `json:"contentHash,omitempty"`
	ByteSize     int64         `json:"byteSize,omitempty"`
	Mode         string        `json:"mode,omitempty"`
}

func NewTree(files []TreeFile) (TreeManifest, error) {
	if len(files) > MaxTreeFiles {
		return TreeManifest{}, fmt.Errorf("%w: more than %d files", ErrTreeLimit, MaxTreeFiles)
	}
	normalized := append([]TreeFile(nil), files...)
	total := int64(0)
	seen := make(map[string]bool, len(normalized))
	for index := range normalized {
		file, err := normalizeTreeFile(normalized[index])
		if err != nil {
			return TreeManifest{}, fmt.Errorf("files[%d]: %w", index, err)
		}
		if seen[file.Path] {
			return TreeManifest{}, fmt.Errorf("%w: duplicate path %s", ErrInvalidTree, file.Path)
		}
		seen[file.Path] = true
		total += file.ByteSize
		if total > MaxTreeBytes {
			return TreeManifest{}, fmt.Errorf("%w: more than %d bytes", ErrTreeLimit, MaxTreeBytes)
		}
		normalized[index] = file
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Path < normalized[j].Path })
	manifest := TreeManifest{SchemaVersion: TreeSchemaVersion, Files: normalized}
	hash, err := treeHash(manifest)
	if err != nil {
		return TreeManifest{}, err
	}
	manifest.TreeHash = hash
	return manifest, nil
}

func ParseTree(manifest TreeManifest) (TreeManifest, error) {
	expected, err := NewTree(manifest.Files)
	if err != nil {
		return TreeManifest{}, err
	}
	if manifest.SchemaVersion != TreeSchemaVersion || manifest.TreeHash != expected.TreeHash {
		return TreeManifest{}, fmt.Errorf("%w: schema version or tree hash mismatch", ErrInvalidTree)
	}
	for index := range manifest.Files {
		if manifest.Files[index] != expected.Files[index] {
			return TreeManifest{}, fmt.Errorf("%w: files must be canonical and sorted", ErrInvalidTree)
		}
	}
	return expected, nil
}

func ApplyOperation(current TreeManifest, operation FileOperation) (TreeManifest, error) {
	current, err := ParseTree(current)
	if err != nil {
		return TreeManifest{}, err
	}
	operation, err = NormalizeOperation(operation)
	if err != nil {
		return TreeManifest{}, err
	}
	files := make(map[string]TreeFile, len(current.Files)+1)
	for _, file := range current.Files {
		files[file.Path] = file
	}
	target, exists := files[operation.Path]
	switch operation.Kind {
	case OperationUpsert:
		if exists && operation.ExpectedHash == "" {
			return TreeManifest{}, fmt.Errorf("%w: existing file %s", ErrFilePrecondition, operation.Path)
		}
		if operation.ExpectedHash != "" && (!exists || target.ContentHash != operation.ExpectedHash) {
			return TreeManifest{}, fmt.Errorf("%w: expected content hash for %s", ErrTreeConflict, operation.Path)
		}
		file, normalizeErr := normalizeTreeFile(TreeFile{
			Path: operation.Path, Mode: operation.Mode, ContentHash: operation.ContentHash, ByteSize: operation.ByteSize,
		})
		if normalizeErr != nil {
			return TreeManifest{}, normalizeErr
		}
		files[operation.Path] = file
	case OperationDelete:
		if !exists {
			return TreeManifest{}, fmt.Errorf("%w: %s", ErrOperationMissing, operation.Path)
		}
		if operation.ExpectedHash != "" && target.ContentHash != operation.ExpectedHash {
			return TreeManifest{}, fmt.Errorf("%w: expected content hash for %s", ErrTreeConflict, operation.Path)
		}
		delete(files, operation.Path)
	case OperationRename:
		fromPath := operation.FromPath
		source, sourceExists := files[fromPath]
		if !sourceExists {
			return TreeManifest{}, fmt.Errorf("%w: %s", ErrOperationMissing, fromPath)
		}
		if exists {
			return TreeManifest{}, fmt.Errorf("%w: rename target %s exists", ErrTreeConflict, operation.Path)
		}
		if operation.ExpectedHash != "" && source.ContentHash != operation.ExpectedHash {
			return TreeManifest{}, fmt.Errorf("%w: expected content hash for %s", ErrTreeConflict, fromPath)
		}
		delete(files, fromPath)
		source.Path = operation.Path
		files[operation.Path] = source
	default:
		return TreeManifest{}, fmt.Errorf("%w: operation kind %q", ErrInvalidTree, operation.Kind)
	}
	result := make([]TreeFile, 0, len(files))
	for _, file := range files {
		result = append(result, file)
	}
	return NewTree(result)
}

// NormalizeOperation returns the only representation that may be persisted in a
// candidate journal. This makes replay independent from transport whitespace,
// optional sha256 prefixes, and omitted default file modes.
func NormalizeOperation(operation FileOperation) (FileOperation, error) {
	if operation.ID == "" || operation.ID != strings.TrimSpace(operation.ID) || len(operation.ID) > 160 {
		return FileOperation{}, fmt.Errorf("%w: operation id", ErrInvalidTree)
	}
	var err error
	operation.Path, err = NormalizePath(operation.Path)
	if err != nil {
		return FileOperation{}, err
	}
	if operation.ExpectedHash != "" {
		operation.ExpectedHash, err = canonicalSHA256(operation.ExpectedHash)
		if err != nil {
			return FileOperation{}, fmt.Errorf("%w: expected content hash", ErrInvalidTree)
		}
	}
	switch operation.Kind {
	case OperationUpsert:
		if operation.FromPath != "" {
			return FileOperation{}, fmt.Errorf("%w: upsert cannot declare fromPath", ErrInvalidTree)
		}
		file, normalizeErr := normalizeTreeFile(TreeFile{
			Path: operation.Path, Mode: operation.Mode, ContentHash: operation.ContentHash, ByteSize: operation.ByteSize,
		})
		if normalizeErr != nil {
			return FileOperation{}, normalizeErr
		}
		operation.Mode = file.Mode
		operation.ContentHash = file.ContentHash
	case OperationDelete:
		if operation.ExpectedHash == "" {
			return FileOperation{}, fmt.Errorf("%w: delete", ErrFilePrecondition)
		}
		if operation.FromPath != "" || operation.ContentHash != "" || operation.Mode != "" || operation.ByteSize != 0 {
			return FileOperation{}, fmt.Errorf("%w: delete contains unused fields", ErrInvalidTree)
		}
	case OperationRename:
		if operation.ExpectedHash == "" {
			return FileOperation{}, fmt.Errorf("%w: rename", ErrFilePrecondition)
		}
		operation.FromPath, err = NormalizePath(operation.FromPath)
		if err != nil {
			return FileOperation{}, fmt.Errorf("fromPath: %w", err)
		}
		if operation.FromPath == operation.Path {
			return FileOperation{}, fmt.Errorf("%w: rename source equals target", ErrTreeConflict)
		}
		if operation.ContentHash != "" || operation.Mode != "" || operation.ByteSize != 0 {
			return FileOperation{}, fmt.Errorf("%w: rename contains unused fields", ErrInvalidTree)
		}
	default:
		return FileOperation{}, fmt.Errorf("%w: operation kind %q", ErrInvalidTree, operation.Kind)
	}
	return operation, nil
}

func NormalizePath(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." {
		return "", fmt.Errorf("%w: path must be a normalized relative UTF-8 path", ErrInvalidTree)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: path contains a control character", ErrInvalidTree)
		}
	}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: unsafe path segment", ErrInvalidTree)
		}
		lower := strings.ToLower(segment)
		if lower == ".git" || lower == ".env" || strings.HasPrefix(lower, ".env.") ||
			lower == "node_modules" || lower == ".next" || lower == "dist" || lower == "build" || lower == "__pycache__" {
			return "", fmt.Errorf("%w: %s", ErrProtectedPath, value)
		}
	}
	return value, nil
}

func normalizeTreeFile(file TreeFile) (TreeFile, error) {
	normalizedPath, err := NormalizePath(file.Path)
	if err != nil {
		return TreeFile{}, err
	}
	file.Path = normalizedPath
	file.Mode = strings.TrimSpace(file.Mode)
	if file.Mode == "" {
		file.Mode = "100644"
	}
	if file.Mode != "100644" && file.Mode != "100755" {
		return TreeFile{}, fmt.Errorf("%w: %s", ErrUnsupportedMode, file.Mode)
	}
	file.ContentHash, err = canonicalSHA256(file.ContentHash)
	if err != nil {
		return TreeFile{}, fmt.Errorf("%w: content hash for %s", ErrInvalidTree, file.Path)
	}
	if file.ByteSize < 0 || file.ByteSize > MaxFileBytes {
		return TreeFile{}, fmt.Errorf("%w: file %s exceeds size policy", ErrTreeLimit, file.Path)
	}
	return file, nil
}

func canonicalSHA256(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !domain.IsCanonicalHash(value) {
		return "", ErrInvalidTree
	}
	digest := strings.TrimPrefix(value, "sha256:")
	if digest != strings.ToLower(digest) {
		return "", ErrInvalidTree
	}
	return "sha256:" + digest, nil
}

func treeHash(manifest TreeManifest) (string, error) {
	payload := struct {
		SchemaVersion string     `json:"schemaVersion"`
		Files         []TreeFile `json:"files"`
	}{SchemaVersion: TreeSchemaVersion, Files: manifest.Files}
	hash, err := domain.CanonicalHash(payload)
	if err != nil {
		return "", fmt.Errorf("hash repository tree: %w", err)
	}
	return "sha256:" + hash, nil
}
