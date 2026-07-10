package transport_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
)

type fakeWorkbenchRebaseService struct {
	createInput    core.CreateWorkbenchBundleInput
	createCalls    int
	result         core.WorkbenchBundle
	err            error
	bundleID       string
	actorID        string
	input          core.RebaseWorkbenchBundleInput
	calls          int
	lineageState   core.WorkbenchLineageState
	lineageErr     error
	lineageRootID  string
	lineageActorID string
	lineageCalls   int
}

func (f *fakeWorkbenchRebaseService) CreateBundle(_ context.Context, _, _ string, input core.CreateWorkbenchBundleInput) (core.WorkbenchBundle, error) {
	f.createCalls++
	f.createInput = input
	return core.WorkbenchBundle{}, nil
}

func (*fakeWorkbenchRebaseService) GetBundle(context.Context, string, string) (core.WorkbenchBundle, error) {
	return core.WorkbenchBundle{}, nil
}

func (f *fakeWorkbenchRebaseService) GetLineageState(_ context.Context, rootID, actorID string) (core.WorkbenchLineageState, error) {
	f.lineageCalls++
	f.lineageRootID = rootID
	f.lineageActorID = actorID
	return f.lineageState, f.lineageErr
}

func (f *fakeWorkbenchRebaseService) Rebase(_ context.Context, bundleID, actorID string, input core.RebaseWorkbenchBundleInput) (core.WorkbenchBundle, error) {
	f.calls++
	f.bundleID = bundleID
	f.actorID = actorID
	f.input = input
	return f.result, f.err
}

func TestBuildManifestRebaseRouteUsesExactWorkspaceRevisionAndAuthenticatedActor(t *testing.T) {
	sourceBundleID := uuid.NewString()
	workspaceRevision := core.VersionRef{
		ArtifactID:  uuid.NewString(),
		RevisionID:  uuid.NewString(),
		ContentHash: "sha256:" + strings.Repeat("a", 64),
	}
	rebased := core.WorkbenchBundle{
		ID:           uuid.NewString(),
		ProjectID:    testProjectID,
		ManifestHash: "sha256:" + strings.Repeat("b", 64),
	}
	workbench := &fakeWorkbenchRebaseService{result: rebased}
	router := newBusinessRouter(t, transport.Services{Workbench: workbench})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "rebase-build-manifest-1")
	body := []byte(`{"workspaceRevision":{"artifactId":"` + workspaceRevision.ArtifactID + `","revisionId":"` + workspaceRevision.RevisionID + `","contentHash":"` + workspaceRevision.ContentHash + `"}}`)

	response := performRequest(router, http.MethodPost, "/v1/build-manifests/"+sourceBundleID+"/rebase", body, headers)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if workbench.calls != 1 || workbench.bundleID != sourceBundleID || workbench.actorID != testUserID || workbench.input.WorkspaceRevision != workspaceRevision {
		t.Fatalf("rebase call = calls %d bundle %q actor %q input %#v", workbench.calls, workbench.bundleID, workbench.actorID, workbench.input)
	}
	if response.Header().Get("Location") != "/v1/build-manifests/"+rebased.ID {
		t.Fatalf("Location = %q", response.Header().Get("Location"))
	}
	expectedETag := `"workbench-bundle:` + rebased.ID + `:` + rebased.ManifestHash + `"`
	if response.Header().Get("ETag") != expectedETag {
		t.Fatalf("ETag = %q, want %q", response.Header().Get("ETag"), expectedETag)
	}
	var responseBundle core.WorkbenchBundle
	decodeResponse(t, response, &responseBundle)
	if responseBundle.ID != rebased.ID || responseBundle.ManifestHash != rebased.ManifestHash {
		t.Fatalf("response bundle = %#v", responseBundle)
	}
}

func TestBuildManifestRebaseRequiresIdempotencyAndStrictDTO(t *testing.T) {
	workbench := &fakeWorkbenchRebaseService{}
	router := newBusinessRouter(t, transport.Services{Workbench: workbench})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	body := []byte(`{"workspaceRevision":{"artifactId":"` + uuid.NewString() + `","revisionId":"` + uuid.NewString() + `","contentHash":"sha256:` + strings.Repeat("c", 64) + `"}}`)
	path := "/v1/build-manifests/" + uuid.NewString() + "/rebase"

	missingKey := performRequest(router, http.MethodPost, path, body, headers)
	assertProblem(t, missingKey, http.StatusBadRequest, "invalid_idempotency_key")
	if workbench.calls != 0 {
		t.Fatal("rebase service called before idempotency validation")
	}

	headers.Set("Idempotency-Key", "rebase-build-manifest-strict-dto")
	unknown := []byte(`{"workspaceRevision":{"artifactId":"` + uuid.NewString() + `","revisionId":"` + uuid.NewString() + `","contentHash":"sha256:` + strings.Repeat("d", 64) + `"},"allowStale":true}`)
	strict := performRequest(router, http.MethodPost, path, unknown, headers)
	assertProblem(t, strict, http.StatusBadRequest, "unknown_json_field")
	if workbench.calls != 0 {
		t.Fatal("rebase service called with an unknown DTO field")
	}
}

func TestPublicBuildManifestCreateRejectsWorkflowLineageInjection(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		field string
		value string
	}{
		{name: "workflow run", field: "workflowRunId", value: `"` + uuid.NewString() + `"`},
		{name: "root ordinal", field: "rootOrdinal", value: `0`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workbench := &fakeWorkbenchRebaseService{}
			router := newBusinessRouter(t, transport.Services{Workbench: workbench})
			headers := authenticatedHeaders(true)
			headers.Set("Content-Type", "application/json")
			headers.Set("Idempotency-Key", "manifest-lineage-injection-"+strings.ReplaceAll(testCase.name, " ", "-"))
			body := []byte(`{"prototypeRevision":{"artifactId":"` + uuid.NewString() +
				`","revisionId":"` + uuid.NewString() + `","contentHash":"sha256:` + strings.Repeat("a", 64) +
				`"},"` + testCase.field + `":` + testCase.value + `}`)

			response := performRequest(
				router, http.MethodPost, "/v1/projects/"+testProjectID+"/build-manifests", body, headers,
			)
			assertProblem(t, response, http.StatusBadRequest, "unknown_json_field")
			if workbench.createCalls != 0 {
				t.Fatalf("workbench create called with public %s injection: %#v", testCase.field, workbench.createInput)
			}
		})
	}
}

func TestBusinessRoutesRegisterBuildManifestRebase(t *testing.T) {
	router := newBusinessRouter(t, transport.Services{})
	for _, route := range router.Routes() {
		if route.Method == http.MethodPost && route.Path == "/v1/build-manifests/:bundleId/rebase" {
			return
		}
	}
	t.Fatal("missing POST /v1/build-manifests/:bundleId/rebase")
}

func TestBuildManifestLineageStateUsesAuthenticatedActorAndStableETag(t *testing.T) {
	rootID := uuid.NewString()
	requestedBundleID := uuid.NewString()
	manifestHash := "sha256:" + strings.Repeat("e", 64)
	proposalHash := "sha256:" + strings.Repeat("f", 64)
	currentWorkspace := core.VersionRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: "sha256:" + strings.Repeat("9", 64),
	}
	state := core.WorkbenchLineageState{
		RootBundleID: rootID,
		ActiveBundle: core.WorkbenchBundle{
			ID: rootID, ProjectID: testProjectID, RootBuildManifestID: rootID, ManifestHash: manifestHash,
		},
		CurrentWorkspaceRevision: &currentWorkspace,
		CurrentProposal:          &core.ImplementationProposal{ID: uuid.NewString(), PayloadHash: proposalHash, Version: 3},
		Lineage: []core.WorkbenchLineageEntry{{
			BundleID: rootID, Status: "frozen",
		}},
	}
	workbench := &fakeWorkbenchRebaseService{lineageState: state}
	router := newBusinessRouter(t, transport.Services{Workbench: workbench})
	path := "/v1/build-manifests/" + requestedBundleID + "/lineage-state"

	response := performRequest(router, http.MethodGet, path, nil, authenticatedHeaders(false))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if workbench.lineageCalls != 1 || workbench.lineageRootID != requestedBundleID || workbench.lineageActorID != testUserID {
		t.Fatalf("lineage call = calls %d root %q actor %q", workbench.lineageCalls, workbench.lineageRootID, workbench.lineageActorID)
	}
	stateHash, err := domain.CanonicalHash(state)
	if err != nil {
		t.Fatal(err)
	}
	expectedETag := `"build-manifest-lineage-state:` + rootID + `:` + stateHash + `"`
	etag := response.Header().Get("ETag")
	if etag != expectedETag {
		t.Fatalf("ETag = %q, want %q", etag, expectedETag)
	}
	var received core.WorkbenchLineageState
	decodeResponse(t, response, &received)
	if received.RootBundleID != rootID || received.ActiveBundle.ID != rootID || received.CurrentWorkspaceRevision == nil || *received.CurrentWorkspaceRevision != currentWorkspace || len(received.Lineage) != 1 {
		t.Fatalf("lineage state = %#v", received)
	}

	headers := authenticatedHeaders(false)
	headers.Set("If-None-Match", etag)
	notModified := performRequest(router, http.MethodGet, path, nil, headers)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != etag {
		t.Fatalf("conditional status = %d headers = %#v body = %s", notModified.Code, notModified.Header(), notModified.Body.String())
	}

	updatedWorkspace := currentWorkspace
	updatedWorkspace.RevisionID = uuid.NewString()
	updatedWorkspace.ContentHash = "sha256:" + strings.Repeat("8", 64)
	workbench.lineageState.CurrentWorkspaceRevision = &updatedWorkspace
	workbench.lineageState.Lineage[0].LatestProposal = &core.WorkbenchLineageProposalSummary{
		ID: uuid.NewString(), Status: "ready", Version: 4,
	}
	changed := performRequest(router, http.MethodGet, path, nil, authenticatedHeaders(false))
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag {
		t.Fatalf("workspace/lineage change reused stale ETag: status=%d etag=%q", changed.Code, changed.Header().Get("ETag"))
	}
}

func TestBuildManifestLineageStateRequiresAuthentication(t *testing.T) {
	workbench := &fakeWorkbenchRebaseService{}
	router := newBusinessRouter(t, transport.Services{Workbench: workbench})
	response := performRequest(router, http.MethodGet, "/v1/build-manifests/"+uuid.NewString()+"/lineage-state", nil, nil)
	assertProblem(t, response, http.StatusUnauthorized, "authentication_required")
	if workbench.lineageCalls != 0 {
		t.Fatal("lineage service called before authentication")
	}
}

func TestBuildManifestLineageStatePreservesCrossProjectErrors(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "forbidden", err: core.ErrForbidden, status: http.StatusForbidden, code: "forbidden"},
		{name: "concealed", err: core.ErrNotFound, status: http.StatusNotFound, code: "not_found"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workbench := &fakeWorkbenchRebaseService{lineageErr: testCase.err}
			router := newBusinessRouter(t, transport.Services{Workbench: workbench})
			response := performRequest(router, http.MethodGet, "/v1/build-manifests/"+uuid.NewString()+"/lineage-state", nil, authenticatedHeaders(false))
			assertProblem(t, response, testCase.status, testCase.code)
			if workbench.lineageCalls != 1 || workbench.lineageActorID != testUserID {
				t.Fatalf("lineage error call = calls %d actor %q", workbench.lineageCalls, workbench.lineageActorID)
			}
		})
	}
}

func TestBusinessRoutesRegisterBuildManifestLineageState(t *testing.T) {
	router := newBusinessRouter(t, transport.Services{})
	for _, route := range router.Routes() {
		if route.Method == http.MethodGet && route.Path == "/v1/build-manifests/:bundleId/lineage-state" {
			return
		}
	}
	t.Fatal("missing GET /v1/build-manifests/:bundleId/lineage-state")
}
