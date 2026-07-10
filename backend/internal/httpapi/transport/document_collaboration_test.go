package transport_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	documentcollaboration "github.com/worksflow/builder/backend/internal/collaboration"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
)

type fakeDocumentCollaborationService struct {
	downstreamInput documentcollaboration.GenerateDownstreamDocumentInput
	bindingsInput   []documentcollaboration.DocumentMemberBindingInput
	expectedETag    string
	projectID       string
	artifactID      string
	actorID         string
	downstreamCalls int
	bindingCalls    int
	bindingErr      error
}

func (f *fakeDocumentCollaborationService) GetMemberBindings(
	context.Context, string, string,
) (documentcollaboration.DocumentMemberBindingSet, error) {
	return documentcollaboration.DocumentMemberBindingSet{ETag: `"artifact-bindings:test:v1"`}, nil
}

func (f *fakeDocumentCollaborationService) ReplaceMemberBindings(
	_ context.Context,
	artifactID, actorID, expectedETag string,
	items []documentcollaboration.DocumentMemberBindingInput,
) (documentcollaboration.DocumentMemberBindingSet, error) {
	f.bindingCalls++
	f.artifactID, f.actorID, f.expectedETag, f.bindingsInput = artifactID, actorID, expectedETag, items
	return documentcollaboration.DocumentMemberBindingSet{ArtifactID: artifactID, ETag: `"artifact-bindings:test:v2"`}, f.bindingErr
}

func (f *fakeDocumentCollaborationService) GetDocumentGraph(
	_ context.Context, projectID, actorID string,
) (documentcollaboration.DocumentGraph, error) {
	f.projectID, f.actorID = projectID, actorID
	return documentcollaboration.DocumentGraph{ProjectID: projectID, Nodes: []documentcollaboration.DocumentGraphNode{}, Edges: []documentcollaboration.DocumentGraphEdge{}}, nil
}

func (f *fakeDocumentCollaborationService) GenerateDownstreamDocument(
	_ context.Context,
	projectID, actorID string,
	input documentcollaboration.GenerateDownstreamDocumentInput,
) (documentcollaboration.DownstreamDocumentGeneration, error) {
	f.downstreamCalls++
	f.projectID, f.actorID, f.downstreamInput = projectID, actorID, input
	return documentcollaboration.DownstreamDocumentGeneration{
		Proposal: domain.OutputProposal{ID: uuid.NewString()}, CommandID: uuid.NewString(),
	}, nil
}

func (*fakeDocumentCollaborationService) CreateSyncBackProposal(
	context.Context, string, string, documentcollaboration.CreateSyncBackProposalInput,
) (documentcollaboration.SyncBackProposal, error) {
	return documentcollaboration.SyncBackProposal{Proposal: domain.OutputProposal{ID: uuid.NewString()}}, nil
}

func TestDownstreamDocumentTransportRequiresAndForwardsDurableCommandKey(t *testing.T) {
	service := &fakeDocumentCollaborationService{}
	router := newBusinessRouter(t, transport.Services{Collaboration: service})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	body := []byte(`{
		"sourceRevision":{"artifactId":"` + uuid.NewString() + `","revisionId":"` + uuid.NewString() + `","contentHash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"targetKind":"api_contract","targetTitle":"API","instruction":"Generate reviewed API"
	}`)
	path := "/v1/projects/" + testProjectID + "/documents/generate-downstream"

	missing := performRequest(router, http.MethodPost, path, body, headers)
	assertProblem(t, missing, http.StatusBadRequest, "invalid_idempotency_key")
	if service.downstreamCalls != 0 {
		t.Fatal("downstream service ran without an idempotency key")
	}
	headers.Set("Idempotency-Key", "downstream-transport-0001")
	response := performRequest(router, http.MethodPost, path, body, headers)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.downstreamCalls != 1 || service.downstreamInput.CommandKey != "downstream-transport-0001" ||
		service.projectID != testProjectID || service.actorID != testUserID {
		t.Fatalf("downstream call=%d input=%+v project=%q actor=%q", service.downstreamCalls, service.downstreamInput, service.projectID, service.actorID)
	}
	if response.Header().Get("Location") == "" {
		t.Fatal("downstream response omitted proposal Location")
	}
}

func TestDocumentMemberBindingTransportEnforcesCASAndMapsStaleETag(t *testing.T) {
	service := &fakeDocumentCollaborationService{bindingErr: core.ErrConflict}
	router := newBusinessRouter(t, transport.Services{Collaboration: service})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "document-bindings-0001")
	artifactID := uuid.NewString()
	body := []byte(`{"items":[{"userId":"` + testUserID + `","role":"owner"},{"userId":"` + uuid.NewString() + `","role":"downstreamOwner"}]}`)
	path := "/v1/artifacts/" + artifactID + "/member-bindings"

	missing := performRequest(router, http.MethodPut, path, body, headers)
	assertProblem(t, missing, http.StatusPreconditionRequired, "if_match_required")
	if service.bindingCalls != 0 {
		t.Fatal("binding service ran without If-Match")
	}
	headers.Set("If-Match", `"artifact-bindings:`+artifactID+`:v1"`)
	response := performRequest(router, http.MethodPut, path, body, headers)
	assertProblem(t, response, http.StatusPreconditionFailed, "etag_mismatch")
	if service.bindingCalls != 1 || service.expectedETag != headers.Get("If-Match") || service.actorID != testUserID ||
		len(service.bindingsInput) != 2 || service.bindingsInput[1].Role != documentcollaboration.DocumentDownstreamOwner {
		t.Fatalf("binding call=%d etag=%q actor=%q items=%+v", service.bindingCalls, service.expectedETag, service.actorID, service.bindingsInput)
	}
}
