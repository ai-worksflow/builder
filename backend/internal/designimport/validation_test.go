package designimport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

func TestCapabilitiesExposeUploadsAndFailClosedRemoteConnectors(t *testing.T) {
	capabilities := SupportedCapabilities()
	if len(capabilities.Sources) != 7 {
		t.Fatalf("expected seven source entries, got %d", len(capabilities.Sources))
	}
	wanted := map[SourceKind]bool{
		SourceFigma: true, SourcePenpot: true, SourceExcalidraw: true, SourceTLDraw: true,
		SourceStorybook: true, SourceLadle: true, SourceUpload: true,
	}
	for _, capability := range capabilities.Sources {
		delete(wanted, capability.SourceKind)
		if !capability.UploadEnabled || capability.RemoteEnabled || capability.RemoteReason == "" || capability.MaxUploadBytes != MaxUploadBytes {
			t.Fatalf("unexpected fail-closed capability: %#v", capability)
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("missing sources: %#v", wanted)
	}
}

func TestRemoteURLValidationRejectsSSRFAndCredentials(t *testing.T) {
	for _, raw := range []string{
		"http://figma.com/file/1",
		"https://localhost/file/1",
		"https://127.0.0.1/file/1",
		"https://169.254.169.254/latest/meta-data",
		"https://user:secret@figma.com/file/1",
		"https://figma.com/file/1?access_token=secret",
		"https://intranet/file/1",
	} {
		if _, err := validateRemoteURL(raw); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected %q to be rejected, got %v", raw, err)
		}
	}
	if normalized, err := validateRemoteURL("https://www.figma.com/file/abc?node-id=1#selection"); err != nil || normalized != "https://www.figma.com/file/abc?node-id=1" {
		t.Fatalf("unexpected normalized URL %q err=%v", normalized, err)
	}
}

func TestUploadValidationChecksSourceShapeAndActiveContent(t *testing.T) {
	excalidraw := []byte(`{"type":"excalidraw","elements":[]}`)
	validated, err := validateUpload(SourceExcalidraw, UploadFile{
		Name: "flow.excalidraw", MediaType: "application/json",
		ContentBase64: base64.StdEncoding.EncodeToString(excalidraw),
	})
	if err != nil || validated.RawContentHash == "" || len(validated.Catalog.Pages) == 0 {
		t.Fatalf("valid Excalidraw upload rejected: %#v err=%v", validated, err)
	}
	invalidTLDraw := base64.StdEncoding.EncodeToString([]byte(`{"not":"a tldraw store"}`))
	if _, err := validateUpload(SourceTLDraw, UploadFile{Name: "canvas.json", MediaType: "application/json", ContentBase64: invalidTLDraw}); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("expected invalid tldraw shape rejection, got %v", err)
	}
	activeSVG := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if _, err := validateUpload(SourceUpload, UploadFile{Name: "active.svg", MediaType: "image/svg+xml", ContentBase64: activeSVG}); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("expected active SVG rejection, got %v", err)
	}
	externalSVG := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/><image href="https://attacker.example/pixel"/><style>.x{fill:url(https://attacker.example/a)}</style></svg>`))
	if _, err := validateUpload(SourceUpload, UploadFile{Name: "external.svg", MediaType: "image/svg+xml", ContentBase64: externalSVG}); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("expected external SVG reference rejection, got %v", err)
	}
	for _, malformed := range []struct {
		kind SourceKind
		name string
	}{
		{kind: SourceFigma, name: "figma.json"},
		{kind: SourcePenpot, name: "penpot.json"},
	} {
		payload := base64.StdEncoding.EncodeToString([]byte(`{"arbitrary":"json"}`))
		if _, err := validateUpload(malformed.kind, UploadFile{Name: malformed.name, MediaType: "application/json", ContentBase64: payload}); !errors.Is(err, ErrUnsupportedMediaType) {
			t.Fatalf("expected strict %s shape rejection, got %v", malformed.kind, err)
		}
	}
}

func TestCatalogExtractionIsBoundedAndReportsTruncation(t *testing.T) {
	deep := map[string]any{"id": "root", "name": "Root", "type": "DOCUMENT"}
	cursor := deep
	for index := 0; index < maxFigmaCatalogDepth+10; index++ {
		child := map[string]any{"id": fmt.Sprintf("frame-%d", index), "name": "Frame", "type": "FRAME"}
		cursor["children"] = []any{child}
		cursor = child
	}
	payload, _ := json.Marshal(map[string]any{"document": deep})
	catalog := extractCatalog(SourceFigma, "application/json", payload)
	if !catalog.Truncated || catalog.TruncationReason == "" {
		t.Fatalf("deep Figma catalog was not explicitly truncated: %#v", catalog)
	}
	pages := make([]map[string]any, 0, maxCatalogItems+100)
	for index := 0; index < maxCatalogItems+100; index++ {
		pages = append(pages, map[string]any{"id": fmt.Sprintf("page-%d", index), "name": strings.Repeat("x", 400)})
	}
	payload, _ = json.Marshal(map[string]any{"pages": pages})
	catalog = extractCatalog(SourceUpload, "application/json", payload)
	if !catalog.Truncated || len(catalog.Pages) != maxCatalogItems || len([]rune(catalog.Pages[0].Name)) != maxCatalogTextRunes {
		t.Fatalf("catalog bound failed: pages=%d truncated=%v nameRunes=%d", len(catalog.Pages), catalog.Truncated, len([]rune(catalog.Pages[0].Name)))
	}
}

func TestUploadCapabilityTracksSnapshotStoreCapacity(t *testing.T) {
	if limit := uploadLimitForSnapshotStore(1024); limit != 0 {
		t.Fatalf("tiny snapshot store must disable uploads, got %d", limit)
	}
	capabilities := supportedCapabilities(uploadLimitForSnapshotStore(1024))
	for _, capability := range capabilities.Sources {
		if capability.UploadEnabled || capability.MaxUploadBytes != 0 {
			t.Fatalf("tiny store advertised an unusable upload: %#v", capability)
		}
	}
	oneMiB := uploadLimitForSnapshotStore(1 << 20)
	if oneMiB <= 0 || oneMiB >= 1<<20 {
		t.Fatalf("unexpected derived one-MiB store limit: %d", oneMiB)
	}
	tooLarge := base64.StdEncoding.EncodeToString(make([]byte, oneMiB+1))
	if _, err := validateUploadWithLimit(SourceUpload, UploadFile{Name: "large.json", MediaType: "application/json", ContentBase64: tooLarge}, oneMiB); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("dynamic upload limit not enforced: %v", err)
	}
}

func TestBuildPrototypeContentPinsPageSpecAndPassesValidation(t *testing.T) {
	pageSpec := json.RawMessage(`{
  "title":"Home","route":"/","goal":"Open the home page","blueprintPageNodeId":"page-home",
  "states":[
    {"id":"state-ready","key":"ready","title":"Ready"},
    {"id":"state-loading","key":"loading","title":"Loading"},
    {"id":"state-empty","key":"empty","title":"Empty"},
    {"id":"state-error","key":"error","title":"Error"}
  ],
  "acceptanceCriterionIds":["ac-1"],"dataBindings":[],"interactions":[]
}`)
	importID := uuid.New()
	pageArtifactID := uuid.New()
	pageRevisionID := uuid.New()
	fileName := "figma.json"
	selected, _ := json.Marshal([]string{"frame-home"})
	model := importModel{
		ID: importID, ProjectID: uuid.New(), SourceKind: string(SourceFigma), SourceName: "Home design",
		FileName: &fileName, MediaType: "application/json", ByteSize: 10,
		RawContentHash: "sha256:raw", SnapshotContentHash: "sha256:snapshot", SelectedFrameIDs: selected,
		PageSpecArtifactID: pageArtifactID, PageSpecRevisionID: pageRevisionID, PageSpecContentHash: "sha256:page",
	}
	envelope := SnapshotEnvelope{
		CapturedAt: time.Now(), ExtractedCatalog: ImportCatalog{Pages: []CatalogItem{
			{ID: "frame-home", Name: "Home", Kind: "frame"},
			{ID: "frame-admin", Name: "Admin", Kind: "frame"},
		}},
	}
	payload, err := buildPrototypeContent(model, envelope, &core.ArtifactRevision{
		ID: pageRevisionID.String(), ArtifactID: pageArtifactID.String(), ContentHash: "sha256:page", Content: pageSpec,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	report := core.ValidateArtifactContent("prototype", payload)
	if !report.Valid {
		t.Fatalf("generated Prototype is invalid: %#v\npayload=%s", report.Findings, payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	layers := decoded["layers"].(map[string]any)
	root := layers["import-root"].(map[string]any)
	children := root["childIds"].([]any)
	if len(children) != 1 {
		t.Fatalf("selected frame filtering failed: %#v", root)
	}
	if root["kind"] != "frame" || root["layout"] == nil || root["style"] == nil || root["properties"] == nil || root["fieldMetadata"] == nil {
		t.Fatalf("root layer is not canonical: %#v", root)
	}
	breakpoint := decoded["breakpoints"].([]any)[0].(map[string]any)
	if breakpoint["minWidth"] == nil || breakpoint["viewportWidth"] == nil || breakpoint["viewportHeight"] == nil {
		t.Fatalf("breakpoint is not canonical: %#v", breakpoint)
	}
	state := decoded["states"].([]any)[0].(map[string]any)
	if state["pageStateId"] == nil || state["fixtureIds"] == nil || state["required"] == nil {
		t.Fatalf("state is not canonical: %#v", state)
	}
	for _, requiredArray := range []string{"overrides", "tokenBindings", "componentBindings", "assets", "traceLinks"} {
		if _, ok := decoded[requiredArray].([]any); !ok {
			t.Fatalf("canonical array %s is missing: %#v", requiredArray, decoded[requiredArray])
		}
	}
	ref := decoded["pageSpecRevision"].(map[string]any)
	if ref["revisionId"] != pageRevisionID.String() || ref["contentHash"] != "sha256:page" {
		t.Fatalf("PageSpec pin changed: %#v", ref)
	}
}

func TestExistingPrototypeMustUseTheExactRequestedPageSpec(t *testing.T) {
	expected := core.VersionRef{ArtifactID: "page-a", RevisionID: "page-a-r1", ContentHash: "sha256:a"}
	target := core.VersionedArtifact{
		LatestRevision: &core.ArtifactRevision{SourceVersions: []core.ArtifactSource{{
			VersionRef: core.VersionRef{ArtifactID: "page-b", RevisionID: "page-b-r1", ContentHash: "sha256:b"},
			Purpose:    "page_spec", Required: true,
		}}},
	}
	if err := ensureTargetPageSpec(target, expected); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-PageSpec target must fail closed, got %v", err)
	}
	target.LatestRevision.SourceVersions = []core.ArtifactSource{{VersionRef: expected, Purpose: "page_spec", Required: true}}
	if err := ensureTargetPageSpec(target, expected); err != nil {
		t.Fatalf("exact PageSpec target rejected: %v", err)
	}
}

func TestCreateReplayComparesFullNormalizedRequestSemantics(t *testing.T) {
	fileName := "home.json"
	frames, _ := json.Marshal([]string{"frame-home"})
	targetID := uuid.New()
	model := importModel{
		SourceKind: string(SourceFigma), SourceMode: "upload", SourceName: "Home import",
		FileName: &fileName, MediaType: "application/json", RawContentHash: "sha256:raw",
		PageSpecArtifactID:  uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		PageSpecRevisionID:  uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		PageSpecContentHash: "sha256:page", SelectedFrameIDs: frames,
		ExpectedPrototypeArtifactID: targetID,
	}
	input := CreateInput{
		SourceKind: SourceFigma, Mode: "upload", Title: "Home import",
		SelectedFrameIDs:          []string{"frame-home"},
		PageSpecRevision:          core.VersionRef{ArtifactID: model.PageSpecArtifactID.String(), RevisionID: model.PageSpecRevisionID.String(), ContentHash: model.PageSpecContentHash},
		TargetPrototypeArtifactID: targetID.String(),
	}
	upload := validatedUpload{FileName: fileName, MediaType: model.MediaType, RawContentHash: model.RawContentHash}
	if !sameCreateRequest(model, input, upload) {
		t.Fatal("exact normalized request did not replay")
	}
	changedTitle := input
	changedTitle.Title = "Different title"
	if sameCreateRequest(model, changedTitle, upload) {
		t.Fatal("changed title incorrectly replayed")
	}
	changedFile := upload
	changedFile.FileName = "renamed.json"
	if sameCreateRequest(model, input, changedFile) {
		t.Fatal("changed filename incorrectly replayed")
	}
}
