package transport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/verification"
)

const (
	verificationTransportActor      = "20000000-0000-4000-8000-000000000001"
	verificationTransportProject    = "20000000-0000-4000-8000-000000000002"
	verificationTransportSession    = "20000000-0000-4000-8000-000000000003"
	verificationTransportCandidate  = "20000000-0000-4000-8000-000000000004"
	verificationTransportCheckpoint = "20000000-0000-4000-8000-000000000005"
	verificationTransportRun        = "20000000-0000-4000-8000-000000000006"
	verificationTransportParentRun  = "20000000-0000-4000-8000-000000000007"
	verificationTransportReceipt    = "20000000-0000-4000-8000-000000000008"
)

const verificationTransportHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type verificationSessionResolverFake struct {
	sessionID string
	actorID   string
	err       error
}

func (resolver *verificationSessionResolverFake) ResolveProject(
	_ context.Context,
	sessionID, actorID string,
) (string, error) {
	resolver.sessionID, resolver.actorID = sessionID, actorID
	return verificationTransportProject, resolver.err
}

type verificationAPIFake struct {
	listProjectID string
	listSessionID string
	listActorID   string
	listLimit     int
	profiles      []verification.ProfileSummary
	receipt       verification.Receipt
	createInput   verification.CreateCandidateRunRequest
	cancelInput   verification.CancelCandidateRunRequest
	retryInput    verification.RetryCandidateRunRequest
	view          verification.RunView
	createErr     error
	getErr        error
	cancelErr     error
	retryErr      error
}

type canonicalVerificationAPIFake struct {
	profiles    []verification.ProfileSummary
	runs        []verification.CanonicalRunView
	createInput verification.CreateCanonicalRunRequest
}

func (api *canonicalVerificationAPIFake) ListCanonicalProfiles(
	_ context.Context,
	_ string,
	_ verification.CanonicalPlanSubject,
	_ string,
) ([]verification.ProfileSummary, error) {
	return api.profiles, nil
}

func (api *canonicalVerificationAPIFake) ListCanonicalRuns(
	_ context.Context,
	_ string,
	_ verification.CanonicalPlanSubject,
	_ string,
	_ int,
) ([]verification.CanonicalRunView, error) {
	return api.runs, nil
}

func (api *canonicalVerificationAPIFake) CreateCanonicalRun(
	_ context.Context,
	input verification.CreateCanonicalRunRequest,
) (verification.CanonicalRunView, error) {
	api.createInput = input
	return api.runs[0], nil
}

func (api *canonicalVerificationAPIFake) GetCanonicalRun(
	_ context.Context,
	_, _, _ string,
) (verification.CanonicalRunView, error) {
	return api.runs[0], nil
}

func (api *verificationAPIFake) ListActiveProfiles(
	_ context.Context,
	projectID, sessionID, actorID string,
) ([]verification.ProfileSummary, error) {
	if projectID != verificationTransportProject || sessionID != verificationTransportSession || actorID != verificationTransportActor {
		return nil, verification.ErrInvalidRun
	}
	return append([]verification.ProfileSummary(nil), api.profiles...), nil
}

func (api *verificationAPIFake) ListCandidateRunsForSession(
	_ context.Context,
	projectID, sessionID, actorID string,
	limit int,
) ([]verification.RunView, error) {
	api.listProjectID = projectID
	api.listSessionID = sessionID
	api.listActorID = actorID
	api.listLimit = limit
	return []verification.RunView{api.view}, nil
}

func (api *verificationAPIFake) ListChecksForRunByID(
	_ context.Context,
	runID, actorID string,
	offset, limit int,
) (verification.CheckPage, error) {
	if actorID != verificationTransportActor {
		return verification.CheckPage{}, verification.ErrInvalidRun
	}
	return verification.CheckPage{
		RunID:   runID,
		Receipt: verificationstoreReference(verificationTransportReceipt),
		Offset:  offset, Limit: limit, TotalCount: 1,
		Checks: []verification.CheckResult{{
			ID: "typecheck", Kind: "command", Required: true,
			Status: verification.CheckPassed, Diagnostics: []verification.Diagnostic{},
		}},
	}, nil
}

func verificationstoreReference(id string) repository.ExactReference {
	return repository.ExactReference{ID: id, ContentHash: verificationTransportHash}
}

func (api *verificationAPIFake) GetReceiptByID(
	_ context.Context,
	receiptID, actorID string,
) (verification.Receipt, error) {
	if receiptID != api.receipt.ID || actorID != verificationTransportActor {
		return verification.Receipt{}, verification.ErrReceiptNotFound
	}
	return api.receipt, nil
}

func (api *verificationAPIFake) CreateCandidateRun(
	_ context.Context,
	input verification.CreateCandidateRunRequest,
) (verification.RunView, error) {
	api.createInput = input
	return api.view, api.createErr
}

func (api *verificationAPIFake) GetRunByID(
	_ context.Context,
	runID, actorID string,
) (verification.RunView, error) {
	view := api.view
	view.Run.ID = runID
	view.Run.CreatedBy = actorID
	return view, api.getErr
}

func (api *verificationAPIFake) CancelCandidateRun(
	_ context.Context,
	input verification.CancelCandidateRunRequest,
) (verification.RunView, error) {
	api.cancelInput = input
	view := api.view
	view.Run.ID = input.RunID
	view.Run.State = verification.RunCancelled
	view.Run.Version = input.ExpectedVersion + 1
	return view, api.cancelErr
}

func (api *verificationAPIFake) RetryCandidateRun(
	_ context.Context,
	input verification.RetryCandidateRunRequest,
) (verification.RunView, error) {
	api.retryInput = input
	view := api.view
	view.Run.ID = verificationTransportRun
	view.Run.ParentRunID = input.ParentRunID
	return view, api.retryErr
}

func TestVerificationTransportCreatesExactSourceDerivedRun(t *testing.T) {
	api := &verificationAPIFake{view: verificationTransportView()}
	resolver := &verificationSessionResolverFake{}
	router := verificationTransportRouter(t, api, resolver)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+verificationTransportSession+"/verification-runs",
		bytes.NewBufferString(`{
			"candidateId":"`+verificationTransportCandidate+`",
			"checkpointId":"`+verificationTransportCheckpoint+`",
			"expectedSessionVersion":4,
			"expectedSessionEpoch":2,
			"expectedCandidateVersion":7,
			"expectedWriterLeaseEpoch":3,
			"verificationProfile":{
				"id":"react-fastapi-postgres-v1",
				"version":1,
				"contentHash":"`+verificationTransportHash+`"
			},
			"reason":"verify exact checkpoint"
		}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "verify-operation-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("POST verification status=%d body=%s", response.Code, response.Body.String())
	}
	if resolver.sessionID != verificationTransportSession ||
		resolver.actorID != verificationTransportActor ||
		api.createInput.ProjectID != verificationTransportProject ||
		api.createInput.SessionID != verificationTransportSession ||
		api.createInput.CandidateID != verificationTransportCandidate ||
		api.createInput.CheckpointID != verificationTransportCheckpoint ||
		api.createInput.ActorID != verificationTransportActor ||
		api.createInput.OperationID != "verify-operation-1" ||
		api.createInput.ExpectedSessionVersion != 4 ||
		api.createInput.ExpectedSessionEpoch != 2 ||
		api.createInput.ExpectedCandidateVersion != 7 ||
		api.createInput.ExpectedWriterLeaseEpoch != 3 {
		t.Fatalf("create verification input = %#v resolver=%#v", api.createInput, resolver)
	}
	if response.Header().Get("Location") != "/v1/verification-runs/"+verificationTransportRun ||
		response.Header().Get("ETag") !=
			`"candidate-verification-run:`+verificationTransportRun+`:1:0"` ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("verification response headers = %#v", response.Header())
	}
}

func TestVerificationTransportGetsCancelsAndRetriesUsingAuthoritativeRunProject(t *testing.T) {
	api := &verificationAPIFake{view: verificationTransportView()}
	router := verificationTransportRouter(t, api, &verificationSessionResolverFake{})

	getRequest := httptest.NewRequest(
		http.MethodGet, "/verification-runs/"+verificationTransportParentRun, nil,
	)
	getResponse := httptest.NewRecorder()
	router.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK ||
		getResponse.Header().Get("X-Verification-Fence-Epoch") != "0" {
		t.Fatalf("GET verification status=%d headers=%v body=%s",
			getResponse.Code, getResponse.Header(), getResponse.Body.String())
	}

	cancelRequest := httptest.NewRequest(
		http.MethodPost,
		"/verification-runs/"+verificationTransportParentRun+":cancel",
		bytes.NewBufferString(`{"expectedVersion":1,"expectedFenceEpoch":0,"reason":"stop"}`),
	)
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelRequest.Header.Set("Idempotency-Key", "cancel-verification-1")
	cancelResponse := httptest.NewRecorder()
	router.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK ||
		api.cancelInput.ProjectID != verificationTransportProject ||
		api.cancelInput.RunID != verificationTransportParentRun ||
		api.cancelInput.ActorID != verificationTransportActor ||
		api.cancelInput.ExpectedVersion != 1 ||
		api.cancelInput.ExpectedFenceEpoch != 0 ||
		api.cancelInput.Reason != "stop" {
		t.Fatalf("cancel status=%d input=%#v body=%s",
			cancelResponse.Code, api.cancelInput, cancelResponse.Body.String())
	}

	retryRequest := httptest.NewRequest(
		http.MethodPost,
		"/verification-runs/"+verificationTransportParentRun+":retry",
		bytes.NewBufferString(`{"reason":"retry deterministic failure"}`),
	)
	retryRequest.Header.Set("Content-Type", "application/json")
	retryRequest.Header.Set("Idempotency-Key", "retry-verification-1")
	retryResponse := httptest.NewRecorder()
	router.ServeHTTP(retryResponse, retryRequest)
	if retryResponse.Code != http.StatusCreated ||
		api.retryInput.ProjectID != verificationTransportProject ||
		api.retryInput.ParentRunID != verificationTransportParentRun ||
		api.retryInput.ActorID != verificationTransportActor ||
		api.retryInput.OperationID != "retry-verification-1" ||
		api.retryInput.Reason != "retry deterministic failure" {
		t.Fatalf("retry status=%d input=%#v body=%s",
			retryResponse.Code, api.retryInput, retryResponse.Body.String())
	}
}

func TestVerificationTransportListsAuthoritativeProfilesAndReadsImmutableReceipt(t *testing.T) {
	profile := verification.ProfileSummary{
		VerificationProfile: verification.ProfileReference{
			ID: "react-fastapi-postgres-v1", Version: 1, ContentHash: verificationTransportHash,
		},
		SupportedTemplateRoles: []string{"api", "web"},
	}
	api := &verificationAPIFake{
		view: verificationTransportView(), profiles: []verification.ProfileSummary{profile},
		receipt: verification.Receipt{
			SchemaVersion: verification.ReceiptSchemaVersion,
			ID:            verificationTransportReceipt, ProjectID: verificationTransportProject,
			PayloadHash: verificationTransportHash,
		},
	}
	resolver := &verificationSessionResolverFake{}
	router := verificationTransportRouter(t, api, resolver)

	profilesRequest := httptest.NewRequest(
		http.MethodGet,
		"/sandbox-sessions/"+verificationTransportSession+"/verification-profiles",
		nil,
	)
	profilesResponse := httptest.NewRecorder()
	router.ServeHTTP(profilesResponse, profilesRequest)
	if profilesResponse.Code != http.StatusOK ||
		resolver.sessionID != verificationTransportSession ||
		!strings.Contains(profilesResponse.Body.String(), "\"verificationProfile\"") ||
		!strings.Contains(profilesResponse.Body.String(), profile.VerificationProfile.ContentHash) {
		t.Fatalf("profile list status=%d resolver=%#v body=%s",
			profilesResponse.Code, resolver, profilesResponse.Body.String())
	}

	runsRequest := httptest.NewRequest(
		http.MethodGet,
		"/sandbox-sessions/"+verificationTransportSession+"/verification-runs?limit=7",
		nil,
	)
	runsResponse := httptest.NewRecorder()
	router.ServeHTTP(runsResponse, runsRequest)
	if runsResponse.Code != http.StatusOK ||
		api.listProjectID != verificationTransportProject ||
		api.listSessionID != verificationTransportSession ||
		api.listActorID != verificationTransportActor ||
		api.listLimit != 7 ||
		!strings.Contains(runsResponse.Body.String(), verificationTransportRun) {
		t.Fatalf("Run history status=%d input=%s/%s/%s/%d body=%s",
			runsResponse.Code, api.listProjectID, api.listSessionID,
			api.listActorID, api.listLimit, runsResponse.Body.String())
	}

	checksRequest := httptest.NewRequest(
		http.MethodGet,
		"/verification-runs/"+verificationTransportRun+"/checks?offset=0&limit=1",
		nil,
	)
	checksResponse := httptest.NewRecorder()
	router.ServeHTTP(checksResponse, checksRequest)
	if checksResponse.Code != http.StatusOK ||
		!strings.Contains(checksResponse.Body.String(), "\"typecheck\"") ||
		!strings.Contains(checksResponse.Body.String(), verificationTransportReceipt) {
		t.Fatalf("Run checks status=%d body=%s", checksResponse.Code, checksResponse.Body.String())
	}

	receiptRequest := httptest.NewRequest(
		http.MethodGet, "/verification-receipts/"+verificationTransportReceipt, nil,
	)
	receiptResponse := httptest.NewRecorder()
	router.ServeHTTP(receiptResponse, receiptRequest)
	if receiptResponse.Code != http.StatusOK ||
		receiptResponse.Header().Get("ETag") !=
			`"verification-receipt:`+verificationTransportReceipt+":"+verificationTransportHash+`"` ||
		!strings.Contains(receiptResponse.Body.String(), verificationTransportReceipt) {
		t.Fatalf("Receipt status=%d headers=%v body=%s",
			receiptResponse.Code, receiptResponse.Header(), receiptResponse.Body.String())
	}
}

func TestVerificationTransportMapsStaleCancelFenceToPreconditionFailure(t *testing.T) {
	api := &verificationAPIFake{
		view: verificationTransportView(), cancelErr: verification.ErrRunVersionConflict,
	}
	router := verificationTransportRouter(t, api, &verificationSessionResolverFake{})
	request := httptest.NewRequest(
		http.MethodPost,
		"/verification-runs/"+verificationTransportParentRun+":cancel",
		bytes.NewBufferString(`{"expectedVersion":1,"expectedFenceEpoch":0,"reason":"stop"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "cancel-verification-stale")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale cancel status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestVerificationTransportExposesCanonicalWorkspaceRunWithoutCandidateAuthority(t *testing.T) {
	profile := verification.ProfileReference{ID: "release-v1", Version: 1, ContentHash: verificationTransportHash}
	view := verification.CanonicalRunView{
		Run: verification.CanonicalRun{
			SchemaVersion: "canonical-verification-run/v1", ID: verificationTransportRun,
			ProjectID: verificationTransportProject, Plan: verification.PlanReference{
				ID: verificationTransportCheckpoint, ContentHash: verificationTransportHash,
			},
			RequestKey: "canonical-operation", RequestHash: verificationTransportHash,
			Reason: "release", State: verification.RunQueued, Version: 1,
			CreatedBy: verificationTransportActor, UpdatedBy: verificationTransportActor,
		},
		Subject: verification.CanonicalPlanSubject{
			WorkspaceArtifactID:  verificationTransportCandidate,
			WorkspaceRevisionID:  verificationTransportCheckpoint,
			WorkspaceContentHash: verificationTransportHash,
		},
		Profile: profile, AllowedActions: []verification.RunAction{},
		BlockingReasons: []verification.RunBlockingReason{},
	}
	canonical := &canonicalVerificationAPIFake{
		profiles: []verification.ProfileSummary{{VerificationProfile: profile, SupportedTemplateRoles: []string{"web", "api"}}},
		runs:     []verification.CanonicalRunView{view},
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: verificationTransportActor}}, Transport: "bearer",
		})
		c.Next()
	})
	handler, err := NewVerificationHandler(VerificationDependencies{
		Service:   &verificationAPIFake{view: verificationTransportView()},
		Canonical: canonical, Sessions: &verificationSessionResolverFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterVerificationRoutes(router, handler, worksmiddleware.CaptureIdempotencyKey(true)); err != nil {
		t.Fatal(err)
	}
	body := `{"workspaceRevision":{"artifactId":"` + verificationTransportCandidate + `","revisionId":"` + verificationTransportCheckpoint + `","contentHash":"` + verificationTransportHash + `"},"verificationProfile":{"id":"release-v1","version":1,"contentHash":"` + verificationTransportHash + `"},"reason":"release"}`
	request := httptest.NewRequest(http.MethodPost, "/projects/"+verificationTransportProject+"/canonical-verification-runs", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "canonical-operation")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || canonical.createInput.WorkspaceRevision.WorkspaceRevisionID != verificationTransportCheckpoint ||
		canonical.createInput.OperationID != "canonical-operation" {
		t.Fatalf("Canonical create status=%d input=%#v body=%s", response.Code, canonical.createInput, response.Body.String())
	}
}

func verificationTransportRouter(
	t *testing.T,
	api VerificationAPI,
	resolver VerificationSessionResolver,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session:   auth.Session{User: auth.User{ID: verificationTransportActor}},
			Transport: "bearer",
		})
		c.Next()
	})
	handler, err := NewVerificationHandler(VerificationDependencies{
		Service: api, Sessions: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterVerificationRoutes(
		router, handler, worksmiddleware.CaptureIdempotencyKey(true),
	); err != nil {
		t.Fatal(err)
	}
	return router
}

func verificationTransportView() verification.RunView {
	return verification.RunView{
		Run: verification.Run{
			SchemaVersion: verification.RunSchemaVersion,
			ID:            verificationTransportRun, ProjectID: verificationTransportProject,
			Plan: verification.PlanReference{
				ID: verificationTransportCheckpoint, ContentHash: verificationTransportHash,
			},
			RequestKey: "verify-operation-1", RequestHash: verificationTransportHash,
			Reason: "verify exact checkpoint", State: verification.RunQueued,
			Version: 1, FenceEpoch: 0, CreatedBy: verificationTransportActor,
			UpdatedBy: verificationTransportActor,
		},
		Subject: verification.CandidatePlanSubject{
			SessionID:           verificationTransportSession,
			CandidateID:         verificationTransportCandidate,
			CandidateSnapshotID: verificationTransportCheckpoint,
		},
		Receipt: nil, ReceiptDecision: nil,
		AllowedActions:  []verification.RunAction{verification.RunActionCancel},
		BlockingReasons: []verification.RunBlockingReason{},
	}
}
