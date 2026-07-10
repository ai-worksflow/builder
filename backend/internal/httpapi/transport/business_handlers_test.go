package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/generation"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
)

func TestBusinessRouteRegistrationCoversCoreResources(t *testing.T) {
	router := newBusinessRouter(t, transport.Services{})
	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, expected := range []string{
		"POST /v1/projects/:projectId/artifacts",
		"GET /v1/artifacts/:artifactId/draft",
		"PATCH /v1/drafts/:draftId",
		"POST /v1/revisions/:revisionId/reviews",
		"GET /v1/reviews/:reviewId",
		"POST /v1/artifacts/:artifactId/comments",
		"POST /v1/projects/:projectId/requirement-baselines",
		"POST /v1/projects/:projectId/impact-reports",
		"POST /v1/projects/:projectId/input-manifests",
		"POST /v1/output-proposals/:proposalId/decisions",
		"POST /v1/output-proposals/:proposalId/apply",
		"POST /v1/projects/:projectId/workbench-bundles",
		"POST /v1/implementation-proposals/:implementationProposalId/apply",
		"POST /v1/generation/artifact-proposals",
		"GET /v1/notifications",
		"GET /v1/projects/:projectId/audit",
		"POST /v1/projects/:projectId/presence/heartbeat",
	} {
		if !routes[expected] {
			t.Errorf("missing route %s", expected)
		}
	}
}

func TestCreateArtifactRequiresIdempotencyAndUsesAuthenticatedActor(t *testing.T) {
	artifacts := &fakeArtifactService{created: core.VersionedArtifact{
		Artifact: core.Artifact{ID: uuid.NewString(), ProjectID: testProjectID, ETag: `"artifact:test:1"`},
	}}
	router := newBusinessRouter(t, transport.Services{Artifacts: artifacts})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	body := []byte(`{"kind":"blueprint","title":"Flow","content":{"nodes":[],"edges":[]}}`)

	missingKey := performRequest(router, http.MethodPost, "/v1/projects/"+testProjectID+"/artifacts", body, headers)
	assertProblem(t, missingKey, http.StatusBadRequest, "invalid_idempotency_key")
	if artifacts.createCalls != 0 {
		t.Fatal("artifact service called before idempotency validation")
	}

	headers.Set("Idempotency-Key", "artifact-create-1")
	response := performRequest(router, http.MethodPost, "/v1/projects/"+testProjectID+"/artifacts", body, headers)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if artifacts.actor != testUserID || artifacts.projectID != testProjectID || artifacts.createCalls != 1 {
		t.Fatalf("actor = %q, project = %q, calls = %d", artifacts.actor, artifacts.projectID, artifacts.createCalls)
	}
	if response.Header().Get("ETag") == "" || response.Header().Get("Location") == "" {
		t.Fatalf("headers = %#v", response.Header())
	}
}

func TestBusinessPersistenceRunsAfterIdempotencyCapture(t *testing.T) {
	artifacts := &fakeArtifactService{created: core.VersionedArtifact{Artifact: core.Artifact{ID: uuid.NewString(), ETag: `"artifact:test:1"`}}}
	capturedKey := ""
	persist := func(context *gin.Context) {
		capturedKey = worksmiddleware.IdempotencyKey(context)
		context.Next()
	}
	router := newBusinessRouter(t, transport.Services{Artifacts: artifacts}, persist)
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "persisted-create-1")
	response := performRequest(router, http.MethodPost, "/v1/projects/"+testProjectID+"/artifacts", []byte(`{"kind":"blueprint","title":"Flow"}`), headers)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if capturedKey != "persisted-create-1" {
		t.Fatalf("captured idempotency key = %q", capturedKey)
	}
}

func TestDraftUpdateRequiresIfMatchAndMapsConflictTo412(t *testing.T) {
	artifacts := &fakeArtifactService{updateErr: core.ErrConflict}
	router := newBusinessRouter(t, transport.Services{Artifacts: artifacts})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	body := []byte(`{"content":{"nodes":[]}}`)
	draftID := uuid.NewString()

	missing := performRequest(router, http.MethodPatch, "/v1/drafts/"+draftID, body, headers)
	assertProblem(t, missing, http.StatusPreconditionRequired, "if_match_required")

	headers.Set("If-Match", `"draft:`+draftID+`:2"`)
	response := performRequest(router, http.MethodPatch, "/v1/drafts/"+draftID, body, headers)
	assertProblem(t, response, http.StatusPreconditionFailed, "etag_mismatch")
	if artifacts.expectedETag != headers.Get("If-Match") || artifacts.draftID != draftID {
		t.Fatalf("etag = %q, draft = %q", artifacts.expectedETag, artifacts.draftID)
	}
}

func TestProposalDecisionUsesETagVersionAndRejectsStaleRequest(t *testing.T) {
	proposalID := uuid.NewString()
	proposals := &fakeProposalService{proposal: domain.OutputProposal{
		ID: proposalID, ProjectID: testProjectID, ArtifactID: uuid.NewString(),
		Status: domain.ProposalOpen, Version: 3,
	}}
	router := newBusinessRouter(t, transport.Services{Proposals: proposals})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "proposal-decision-1")
	body := []byte(`{"operationId":"op-1","decision":"accepted"}`)

	headers.Set("If-Match", `"output-proposal:`+proposalID+`:2"`)
	stale := performRequest(router, http.MethodPost, "/v1/output-proposals/"+proposalID+"/decisions", body, headers)
	assertProblem(t, stale, http.StatusPreconditionFailed, "etag_mismatch")
	if proposals.decideCalls != 0 {
		t.Fatal("proposal decision applied with a stale ETag")
	}

	headers.Set("If-Match", `"output-proposal:`+proposalID+`:3"`)
	response := performRequest(router, http.MethodPost, "/v1/output-proposals/"+proposalID+"/decisions", body, headers)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if proposals.decision.Version != 3 || proposals.actor != testUserID || proposals.decideCalls != 1 {
		t.Fatalf("decision = %#v, actor = %q, calls = %d", proposals.decision, proposals.actor, proposals.decideCalls)
	}
	if response.Header().Get("ETag") != `"output-proposal:`+proposalID+`:4"` {
		t.Fatalf("ETag = %q", response.Header().Get("ETag"))
	}
}

func TestCanonicalAndAggregateReviewRoutesCallSameService(t *testing.T) {
	artifactID := uuid.NewString()
	revisionID := uuid.NewString()
	artifacts := &fakeArtifactService{
		getRevision: core.ArtifactRevision{ID: revisionID, ArtifactID: artifactID},
		getArtifact: core.VersionedArtifact{Artifact: core.Artifact{ID: artifactID, ProjectID: testProjectID}},
	}
	reviews := &fakeReviewService{}
	router := newBusinessRouter(t, transport.Services{Artifacts: artifacts, Reviews: reviews})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "review-create-1")

	canonical := performRequest(router, http.MethodPost, "/v1/revisions/"+revisionID+"/reviews", []byte(`{"reviewerIds":[]}`), headers)
	if canonical.Code != http.StatusCreated {
		t.Fatalf("canonical status = %d, body = %s", canonical.Code, canonical.Body.String())
	}
	headers.Set("Idempotency-Key", "review-create-2")
	aggregateBody, _ := json.Marshal(map[string]any{"artifactId": artifactID, "revisionId": revisionID, "reviewerIds": []string{}})
	aggregate := performRequest(router, http.MethodPost, "/v1/projects/"+testProjectID+"/reviews", aggregateBody, headers)
	if aggregate.Code != http.StatusCreated {
		t.Fatalf("aggregate status = %d, body = %s", aggregate.Code, aggregate.Body.String())
	}
	if reviews.submitCalls != 2 || reviews.projectID != testProjectID || reviews.artifactID != artifactID || reviews.input.RevisionID != revisionID {
		t.Fatalf("review calls = %d, project = %q, artifact = %q, input = %#v", reviews.submitCalls, reviews.projectID, reviews.artifactID, reviews.input)
	}
}

func TestListArtifactsAppliesOpaquePagination(t *testing.T) {
	artifacts := &fakeArtifactService{listed: []core.Artifact{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	router := newBusinessRouter(t, transport.Services{Artifacts: artifacts})
	response := performRequest(router, http.MethodGet, "/v1/projects/"+testProjectID+"/artifacts?limit=2", nil, authenticatedHeaders(false))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var page struct {
		Items      []core.Artifact `json:"items"`
		Total      int             `json:"total"`
		NextCursor string          `json:"nextCursor"`
	}
	decodeResponse(t, response, &page)
	if len(page.Items) != 2 || page.Total != 3 || page.NextCursor == "" {
		t.Fatalf("page = %#v", page)
	}
}

func TestDomainValidationErrorsUseRFC9457FieldErrors(t *testing.T) {
	proposals := &fakeProposalService{createErr: &domain.DomainError{
		Kind: domain.ErrInvalidArgument, Field: "proposal.operations", Message: "at least one operation is required",
	}}
	router := newBusinessRouter(t, transport.Services{Proposals: proposals})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "proposal-create-invalid")
	response := performRequest(router, http.MethodPost, "/v1/projects/"+testProjectID+"/output-proposals", []byte(`{"inputManifestId":"manifest","artifactId":"artifact","operations":[]}`), headers)
	assertProblem(t, response, http.StatusUnprocessableEntity, "invalid_input")
	var body struct {
		Errors map[string][]string `json:"errors"`
	}
	decodeResponse(t, response, &body)
	if len(body.Errors["proposal.operations"]) != 1 {
		t.Fatalf("errors = %#v", body.Errors)
	}
}

func TestGenerationProviderErrorsHaveStableProblemStatus(t *testing.T) {
	generator := &fakeGenerationService{artifactErr: ai.ErrRateLimited}
	router := newBusinessRouter(t, transport.Services{Generation: generator})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "generation-1")
	response := performRequest(router, http.MethodPost, "/v1/generation/artifact-proposals", []byte(`{"manifestId":"manifest-1","model":"gpt-test"}`), headers)
	assertProblem(t, response, http.StatusTooManyRequests, "ai_rate_limited")
	if generator.actor != testUserID || generator.manifestID != "manifest-1" || generator.model != "gpt-test" {
		t.Fatalf("generation call = actor %q, manifest %q, model %q", generator.actor, generator.manifestID, generator.model)
	}
}

func newBusinessRouter(t *testing.T, services transport.Services, persistence ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if services.Auth == nil {
		services.Auth = &fakeAuthService{}
	}
	cfg := testConfig()
	server := transport.NewServer(services, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.Use(worksmiddleware.RequestID())
	protected := router.Group("/v1", worksmiddleware.RequireAuthentication(services.Auth, cfg.Security))
	transport.RegisterBusinessRoutes(protected, server, persistence...)
	return router
}

type fakeArtifactService struct {
	created      core.VersionedArtifact
	listed       []core.Artifact
	getArtifact  core.VersionedArtifact
	getRevision  core.ArtifactRevision
	updateErr    error
	createCalls  int
	projectID    string
	actor        string
	draftID      string
	expectedETag string
}

func (f *fakeArtifactService) Create(_ context.Context, projectID, actor string, _ core.CreateArtifactInput) (core.VersionedArtifact, error) {
	f.createCalls++
	f.projectID = projectID
	f.actor = actor
	return f.created, nil
}

func (f *fakeArtifactService) List(context.Context, string, string, string, string) ([]core.Artifact, error) {
	return f.listed, nil
}

func (f *fakeArtifactService) Get(context.Context, string, string, bool) (core.VersionedArtifact, error) {
	return f.getArtifact, nil
}

func (f *fakeArtifactService) UpdateDraft(_ context.Context, draftID, _ string, expectedETag string, _ core.UpdateDraftInput) (core.ArtifactDraft, error) {
	f.draftID = draftID
	f.expectedETag = expectedETag
	return core.ArtifactDraft{}, f.updateErr
}

func (*fakeArtifactService) CreateRevision(context.Context, string, string, string, core.CreateRevisionInput) (core.ArtifactRevision, error) {
	return core.ArtifactRevision{}, nil
}

func (*fakeArtifactService) ListRevisions(context.Context, string, string) ([]core.ArtifactRevision, error) {
	return nil, nil
}

func (f *fakeArtifactService) GetRevision(context.Context, string, string) (core.ArtifactRevision, error) {
	return f.getRevision, nil
}

type fakeReviewService struct {
	submitCalls int
	projectID   string
	artifactID  string
	input       core.SubmitReviewInput
}

func (f *fakeReviewService) Submit(_ context.Context, projectID, artifactID, _ string, input core.SubmitReviewInput) (core.ReviewRequest, error) {
	f.submitCalls++
	f.projectID = projectID
	f.artifactID = artifactID
	f.input = input
	return core.ReviewRequest{ID: uuid.NewString(), ProjectID: projectID, ArtifactID: artifactID, RevisionID: input.RevisionID, ETag: `"review:test:open"`}, nil
}

func (*fakeReviewService) List(context.Context, string, string) ([]core.ReviewRequest, error) {
	return nil, nil
}

func (*fakeReviewService) Decide(context.Context, string, string, core.DecideReviewInput) (core.ReviewRequest, error) {
	return core.ReviewRequest{}, nil
}

type fakeProposalService struct {
	proposal    domain.OutputProposal
	decision    core.DecideProposalInput
	actor       string
	decideCalls int
	createErr   error
}

func (*fakeProposalService) CreateManifest(context.Context, string, string, core.CreateManifestInput) (domain.InputManifest, error) {
	return domain.InputManifest{}, nil
}

func (f *fakeProposalService) CreateProposal(context.Context, string, string, core.CreateProposalInput) (domain.OutputProposal, error) {
	return domain.OutputProposal{}, f.createErr
}

func (*fakeProposalService) GetManifest(context.Context, string, string) (domain.InputManifest, error) {
	return domain.InputManifest{}, nil
}

func (f *fakeProposalService) GetProposal(context.Context, string, string) (domain.OutputProposal, error) {
	return f.proposal, nil
}

func (*fakeProposalService) ListProposals(context.Context, string, string, string) ([]domain.OutputProposal, error) {
	return nil, nil
}

func (f *fakeProposalService) Decide(_ context.Context, _ string, actor string, input core.DecideProposalInput) (domain.OutputProposal, error) {
	f.decideCalls++
	f.actor = actor
	f.decision = input
	updated := f.proposal
	updated.Version++
	return updated, nil
}

func (*fakeProposalService) Apply(context.Context, string, string, core.ApplyProposalInput) (core.ArtifactDraft, error) {
	return core.ArtifactDraft{}, nil
}

type fakeGenerationService struct {
	artifactErr error
	actor       string
	manifestID  string
	model       string
}

func (f *fakeGenerationService) GenerateArtifactProposal(_ context.Context, manifestID, actor, model string) (generation.ArtifactGenerationResult, error) {
	f.manifestID = manifestID
	f.actor = actor
	f.model = model
	return generation.ArtifactGenerationResult{}, f.artifactErr
}

func (*fakeGenerationService) GenerateImplementation(context.Context, string, string, string, string) (generation.ImplementationGenerationResult, error) {
	return generation.ImplementationGenerationResult{}, nil
}
