package delivery

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type ExportService struct {
	database *gorm.DB
	loader   *RevisionLoader
}

func NewExportService(database *gorm.DB, loader *RevisionLoader) (*ExportService, error) {
	if database == nil || loader == nil {
		return nil, errors.New("export database and revision loader are required")
	}
	return &ExportService{database: database, loader: loader}, nil
}

type archiveEntry struct {
	Name string
	Data []byte
}

func (s *ExportService) Export(ctx context.Context, projectID, actorID string, input ExportInput) (Archive, error) {
	if input.Kind != ExportSource && input.Kind != ExportDocument && input.Kind != ExportBlueprint && input.Kind != ExportPrototype {
		return Archive{}, Invalid("kind", "kind must be source, document, blueprint or prototype")
	}
	if (input.Revision == nil) == (strings.TrimSpace(input.BuildManifestID) == "") {
		return Archive{}, Invalid("revision", "provide exactly one exact revision or buildManifestId")
	}
	if !input.RedactSensitive {
		if _, err := s.loader.access.Authorize(ctx, projectID, actorID, core.ActionAdmin); err != nil {
			return Archive{}, err
		}
	}
	entries := []archiveEntry{}
	references := []core.VersionRef{}
	redactions := []string{}
	if input.Revision != nil {
		resolved, err := s.entriesForRevision(ctx, projectID, actorID, input.Kind, *input.Revision, input.RedactSensitive)
		if err != nil {
			return Archive{}, err
		}
		entries, redactions = append(entries, resolved.entries...), append(redactions, resolved.redactions...)
		references = append(references, *input.Revision)
	} else {
		bundle, err := s.loader.LoadBuildManifest(ctx, projectID, actorID, input.BuildManifestID, core.ActionView)
		if err != nil {
			return Archive{}, err
		}
		bundlePayload, _ := json.MarshalIndent(bundle, "", "  ")
		if input.RedactSensitive {
			redacted, changed, err := redactJSON(bundlePayload)
			if err != nil {
				return Archive{}, err
			}
			if changed {
				redactions = append(redactions, "build-manifest.json:redacted-content")
			}
			bundlePayload = redacted
		}
		entries = append(entries, archiveEntry{Name: "build-manifest.json", Data: bundlePayload})
		selected, err := refsForExport(bundle, input.Kind)
		if err != nil {
			return Archive{}, err
		}
		for _, reference := range selected {
			resolved, err := s.entriesForRevision(ctx, projectID, actorID, input.Kind, reference, input.RedactSensitive)
			if err != nil {
				return Archive{}, err
			}
			entries, redactions = append(entries, resolved.entries...), append(redactions, resolved.redactions...)
			references = append(references, reference)
		}
	}
	sort.Strings(redactions)
	manifest := map[string]any{
		"schemaVersion": 1, "kind": input.Kind, "projectId": projectID,
		"references": references, "redactSensitive": input.RedactSensitive,
		"redactions": redactions,
	}
	if input.BuildManifestID != "" {
		manifest["buildManifestId"] = input.BuildManifestID
	}
	manifestPayload, _ := json.MarshalIndent(manifest, "", "  ")
	entries = append(entries, archiveEntry{Name: "worksflow-export.json", Data: manifestPayload})
	archiveData, err := createArchive(entries)
	if err != nil {
		return Archive{}, err
	}
	projectName := "worksflow-project"
	var project storage.ProjectModel
	if s.database.WithContext(ctx).Select("name").Where("id = ?", projectID).Take(&project).Error == nil && strings.TrimSpace(project.Name) != "" {
		projectName = project.Name
	}
	checksum := sha256.Sum256(archiveData)
	return Archive{
		Filename: safeArchiveName(projectName, input.Kind), ContentType: "application/zip",
		Data: archiveData, FileCount: len(entries), Checksum: "sha256:" + hex.EncodeToString(checksum[:]),
		Redactions: redactions,
	}, nil
}

type resolvedEntries struct {
	entries    []archiveEntry
	redactions []string
}

func (s *ExportService) entriesForRevision(ctx context.Context, projectID, actorID string, kind ExportKind, reference core.VersionRef, redact bool) (resolvedEntries, error) {
	switch kind {
	case ExportSource:
		workspace, err := s.loader.LoadFrozenWorkspace(ctx, projectID, actorID, reference, core.ActionView)
		if err != nil {
			return resolvedEntries{}, err
		}
		result := resolvedEntries{}
		for _, file := range workspace.Files {
			if redact && SensitivePath(file.Path) {
				result.redactions = append(result.redactions, file.Path+":excluded-sensitive-path")
				continue
			}
			content := file.Content
			if redact {
				if redacted, changed := RedactSensitive(content); changed {
					content = redacted
					result.redactions = append(result.redactions, file.Path+":redacted-content")
				}
			}
			// Source files live in an explicit namespace so a workspace cannot
			// collide with the signed export metadata at the archive root.
			result.entries = append(result.entries, archiveEntry{Name: "source/" + file.Path, Data: []byte(content)})
		}
		return result, nil
	case ExportDocument:
		loaded, err := s.loader.Load(ctx, projectID, actorID, reference, core.ActionView,
			"project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source", "change_request", "requirement_baseline", "api_contract", "data_contract", "permission_contract")
		if err != nil {
			return resolvedEntries{}, err
		}
		return artifactJSONEntry("documents", loaded, redact)
	case ExportBlueprint:
		loaded, err := s.loader.Load(ctx, projectID, actorID, reference, core.ActionView, "blueprint", "page_spec")
		if err != nil {
			return resolvedEntries{}, err
		}
		return artifactJSONEntry("blueprints", loaded, redact)
	case ExportPrototype:
		loaded, err := s.loader.Load(ctx, projectID, actorID, reference, core.ActionView, "prototype", "prototype_flow")
		if err != nil {
			return resolvedEntries{}, err
		}
		return artifactJSONEntry("prototypes", loaded, redact)
	default:
		return resolvedEntries{}, Invalid("kind", "unsupported export kind")
	}
}

func artifactJSONEntry(directory string, loaded RevisionContent, redact bool) (resolvedEntries, error) {
	payload := cloneJSON(loaded.Payload)
	redactions := []string{}
	if redact {
		redacted, changed, err := redactJSON(payload)
		if err != nil {
			return resolvedEntries{}, err
		}
		payload = redacted
		if changed {
			redactions = append(redactions, directory+"/"+loaded.Artifact.ID.String()+"/"+loaded.Revision.ID.String()+".json:redacted-content")
		}
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, payload, "", "  "); err != nil {
		return resolvedEntries{}, Invalid("revision", "artifact revision content is not valid JSON")
	}
	formatted.WriteByte('\n')
	name := directory + "/" + loaded.Artifact.ID.String() + "/" + loaded.Revision.ID.String() + ".json"
	return resolvedEntries{entries: []archiveEntry{{Name: name, Data: formatted.Bytes()}}, redactions: redactions}, nil
}

func refsForExport(bundle core.WorkbenchBundle, kind ExportKind) ([]core.VersionRef, error) {
	switch kind {
	case ExportSource:
		if bundle.CurrentWorkspaceRevision == nil {
			return nil, conflict("build manifest does not pin a current WorkspaceRevision")
		}
		return []core.VersionRef{*bundle.CurrentWorkspaceRevision}, nil
	case ExportDocument:
		if len(bundle.RequirementRevisions) == 0 {
			return nil, conflict("build manifest has no requirement revisions")
		}
		return append([]core.VersionRef(nil), bundle.RequirementRevisions...), nil
	case ExportBlueprint:
		return []core.VersionRef{bundle.BlueprintRevision}, nil
	case ExportPrototype:
		return []core.VersionRef{bundle.PrototypeRevision}, nil
	default:
		return nil, Invalid("kind", "unsupported export kind")
	}
}

func redactJSON(payload json.RawMessage) (json.RawMessage, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, Invalid("revision", "artifact revision content is not valid JSON")
	}
	changed := false
	var visit func(any) any
	visit = func(current any) any {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "apikey") || strings.Contains(lower, "api_key") {
					if _, isString := child.(string); isString {
						typed[key] = "[REDACTED]"
						changed = true
						continue
					}
				}
				typed[key] = visit(child)
			}
			return typed
		case []any:
			for index := range typed {
				typed[index] = visit(typed[index])
			}
			return typed
		case string:
			redacted, didChange := RedactSensitive(typed)
			changed = changed || didChange
			return redacted
		default:
			return typed
		}
	}
	value = visit(value)
	encoded, err := json.Marshal(value)
	return encoded, changed, err
}

func createArchive(entries []archiveEntry) ([]byte, error) {
	if len(entries) == 0 || len(entries) > MaxWorkspaceFiles+10 {
		return nil, Invalid("archive", "archive contains an invalid number of entries")
	}
	seen := map[string]bool{}
	uncompressedBytes := 0
	for _, entry := range entries {
		if len(entry.Data) > MaxArchiveBytes-uncompressedBytes {
			return nil, NewError(CodeOutputLimit, 413, "export archive exceeds the configured uncompressed size limit")
		}
		uncompressedBytes += len(entry.Data)
	}
	var output bytes.Buffer
	limited := &archiveLimitWriter{writer: &output, limit: MaxArchiveBytes}
	archive := zip.NewWriter(limited)
	fixedTime := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, entry := range entries {
		name, err := SanitizePath(entry.Name)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		canonical := strings.ToLower(name)
		if seen[canonical] {
			_ = archive.Close()
			return nil, Invalid("archive", "archive contains duplicate paths")
		}
		seen[canonical] = true
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(fixedTime)
		header.SetMode(0o600)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			return nil, wrapInternal("create archive entry", err)
		}
		if _, err := writer.Write(entry.Data); err != nil {
			_ = archive.Close()
			if errors.Is(err, errArchiveLimit) {
				return nil, NewError(CodeOutputLimit, 413, "export archive exceeds the configured size limit")
			}
			return nil, wrapInternal("write archive entry", err)
		}
	}
	if err := archive.Close(); err != nil {
		if errors.Is(err, errArchiveLimit) {
			return nil, NewError(CodeOutputLimit, 413, "export archive exceeds the configured size limit")
		}
		return nil, wrapInternal("finalize export archive", err)
	}
	return output.Bytes(), nil
}

var errArchiveLimit = errors.New("archive size limit exceeded")

type archiveLimitWriter struct {
	writer io.Writer
	limit  int
	wrote  int
}

func (w *archiveLimitWriter) Write(value []byte) (int, error) {
	if len(value) > w.limit-w.wrote {
		return 0, errArchiveLimit
	}
	written, err := w.writer.Write(value)
	w.wrote += written
	return written, err
}

var archiveSlugPattern = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func safeArchiveName(projectName string, kind ExportKind) string {
	slug := strings.Trim(archiveSlugPattern.ReplaceAllString(projectName, "-"), "-")
	if len(slug) > 80 {
		slug = slug[:80]
	}
	if slug == "" {
		slug = "worksflow-project"
	}
	return fmt.Sprintf("%s-%s.zip", slug, kind)
}
