package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

type buildArtifactContentStore struct {
	stored content.StoredContent
}

func buildTestRef() core.VersionRef {
	return core.VersionRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(),
		ContentHash: "sha256:" + string(bytes.Repeat([]byte("c"), 64)),
	}
}

func (s buildArtifactContentStore) PutPending(context.Context, string, string, string, int, json.RawMessage) (content.Reference, error) {
	return content.Reference{}, nil
}
func (s buildArtifactContentStore) Finalize(context.Context, string) error { return nil }
func (s buildArtifactContentStore) Abort(context.Context, string) error    { return nil }
func (s buildArtifactContentStore) Get(context.Context, string, string) (content.StoredContent, error) {
	return s.stored, nil
}

func TestCaptureBuildArtifactUsesViteDistAndPreservesBinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestBuildFile(t, root, "index.html", []byte("source must not be deployed"))
	writeTestBuildFile(t, root, "src/main.ts", []byte("source"))
	writeTestBuildFile(t, root, "dist/index.html", []byte("<html><body>vite</body></html>"))
	binary := []byte{0, 1, 2, 0xff, 0xfe}
	writeTestBuildFile(t, root, "dist/assets/logo.bin", binary)

	artifact, err := captureBuildArtifact(root, buildTestRef(), false)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.EntryPath != "index.html" || artifact.FileCount != 2 {
		t.Fatalf("Vite output was not isolated: %+v", artifact)
	}
	for _, file := range artifact.Files {
		if file.Path == "src/main.ts" {
			t.Fatal("source file entered immutable build artifact")
		}
		if file.Path == "assets/logo.bin" {
			decoded, err := base64.StdEncoding.Strict().DecodeString(file.ContentBase64)
			if err != nil || !bytes.Equal(decoded, binary) {
				t.Fatalf("binary build output was corrupted: %x %v", decoded, err)
			}
		}
	}
	if err := validateBuildArtifact(artifact); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureBuildArtifactUsesNextStaticOut(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestBuildFile(t, root, "out/index.html", []byte("<html>next export</html>"))
	writeTestBuildFile(t, root, "out/_next/static/app.js", []byte("ok"))
	artifact, err := captureBuildArtifact(root, buildTestRef(), false)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.EntryPath != "index.html" || artifact.FileCount != 2 {
		t.Fatalf("Next static output was not captured: %+v", artifact)
	}
}

func TestCaptureBuildArtifactWithoutStaticEntryFailsClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestBuildFile(t, root, "dist/app.js", []byte("no html"))
	if _, err := captureBuildArtifact(root, buildTestRef(), false); err == nil {
		t.Fatal("quality accepted a build without a static entry")
	}
}

func TestCaptureBuildArtifactAllowsExplicitStaticWorkspace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestBuildFile(t, root, "index.html", []byte("<html>static</html>"))
	writeTestBuildFile(t, root, "assets/app.js", []byte("ready=true"))
	artifact, err := captureBuildArtifact(root, buildTestRef(), true)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.FileCount != 2 || artifact.EntryPath != "index.html" {
		t.Fatalf("explicit static workspace was not captured: %+v", artifact)
	}
}

func TestQualityLoadsOnlyMatchingImmutableBuildPayload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestBuildFile(t, root, "index.html", []byte("<html>static</html>"))
	artifact, err := captureBuildArtifact(root, buildTestRef(), true)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	projectID := artifact.WorkspaceRevision.ArtifactID
	reference := referenceForBuild(artifact, "5a8b098f-aef0-475b-8188-21f847e618b8", "sha256:"+string(bytes.Repeat([]byte("a"), 64)))
	service := &QualityService{contents: buildArtifactContentStore{stored: content.StoredContent{
		Reference: content.Reference{ID: reference.ContentRef, ContentHash: reference.ContentHash},
		ProjectID: projectID, AggregateType: "quality_build_artifact", AggregateID: artifact.ID, Payload: payload,
	}}}
	loaded, err := service.LoadBuildArtifact(context.Background(), projectID, reference)
	if err != nil || loaded.BuildHash != artifact.BuildHash {
		t.Fatalf("matching immutable build payload did not load: %+v %v", loaded, err)
	}
	reference.BuildHash = "sha256:" + string(bytes.Repeat([]byte("b"), 64))
	if _, err := service.LoadBuildArtifact(context.Background(), projectID, reference); err == nil {
		t.Fatal("tampered build reference was accepted")
	}
}

func writeTestBuildFile(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
