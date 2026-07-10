package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

var appTestNow = time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

type memoryTx struct{}

func (memoryTx) WithinTransaction(ctx context.Context, operation func(context.Context) error) error {
	return operation(ctx)
}

type artifactMemory struct{ items map[string]*domain.Artifact }

func newArtifactMemory() *artifactMemory {
	return &artifactMemory{items: map[string]*domain.Artifact{}}
}
func (m *artifactMemory) Create(_ context.Context, value *domain.Artifact) error {
	if _, exists := m.items[value.ID]; exists {
		return domain.ErrConflict
	}
	copyValue := *value
	m.items[value.ID] = &copyValue
	return nil
}
func (m *artifactMemory) Get(_ context.Context, id string) (*domain.Artifact, error) {
	value, exists := m.items[id]
	if !exists {
		return nil, domain.ErrNotFound
	}
	copyValue := *value
	return &copyValue, nil
}
func (m *artifactMemory) Save(_ context.Context, value *domain.Artifact, expected uint64) error {
	stored, exists := m.items[value.ID]
	if !exists {
		return domain.ErrNotFound
	}
	if stored.Version != expected || value.Version != expected+1 {
		return domain.ErrConflict
	}
	copyValue := *value
	m.items[value.ID] = &copyValue
	return nil
}

type revisionMemory struct {
	items      map[string]domain.Revision
	byArtifact map[string][]string
}

func newRevisionMemory() *revisionMemory {
	return &revisionMemory{items: map[string]domain.Revision{}, byArtifact: map[string][]string{}}
}
func (m *revisionMemory) Create(_ context.Context, value domain.Revision) error {
	if _, exists := m.items[value.ID()]; exists {
		return domain.ErrConflict
	}
	m.items[value.ID()] = value
	m.byArtifact[value.ArtifactID()] = append(m.byArtifact[value.ArtifactID()], value.ID())
	return nil
}
func (m *revisionMemory) Get(_ context.Context, id string) (domain.Revision, error) {
	value, exists := m.items[id]
	if !exists {
		return domain.Revision{}, domain.ErrNotFound
	}
	return value, nil
}
func (m *revisionMemory) LatestNumber(_ context.Context, artifactID string) (int, error) {
	ids := m.byArtifact[artifactID]
	if len(ids) == 0 {
		return 0, domain.ErrNotFound
	}
	latest := 0
	for _, id := range ids {
		if m.items[id].Number() > latest {
			latest = m.items[id].Number()
		}
	}
	return latest, nil
}

type draftMemory struct{ items map[string]*domain.Draft }

func newDraftMemory() *draftMemory { return &draftMemory{items: map[string]*domain.Draft{}} }
func cloneDraft(value *domain.Draft) *domain.Draft {
	copyValue := *value
	copyValue.Content = append(json.RawMessage(nil), value.Content...)
	if value.BaseRevision != nil {
		ref := *value.BaseRevision
		copyValue.BaseRevision = &ref
	}
	return &copyValue
}
func (m *draftMemory) Create(_ context.Context, value *domain.Draft) error {
	if _, exists := m.items[value.ID]; exists {
		return domain.ErrConflict
	}
	m.items[value.ID] = cloneDraft(value)
	return nil
}
func (m *draftMemory) Get(_ context.Context, id string) (*domain.Draft, error) {
	value, exists := m.items[id]
	if !exists {
		return nil, domain.ErrNotFound
	}
	return cloneDraft(value), nil
}
func (m *draftMemory) Save(_ context.Context, value *domain.Draft, expected uint64) error {
	stored, exists := m.items[value.ID]
	if !exists {
		return domain.ErrNotFound
	}
	if stored.Version != expected || value.Version != expected+1 {
		return domain.ErrConflict
	}
	m.items[value.ID] = cloneDraft(value)
	return nil
}

type reviewMemory struct{ items map[string]*domain.Review }

func newReviewMemory() *reviewMemory { return &reviewMemory{items: map[string]*domain.Review{}} }
func (m *reviewMemory) Create(_ context.Context, value *domain.Review) error {
	copyValue := *value
	m.items[value.ID] = &copyValue
	return nil
}
func (m *reviewMemory) Get(_ context.Context, id string) (*domain.Review, error) {
	value, ok := m.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	copyValue := *value
	return &copyValue, nil
}
func (m *reviewMemory) Save(_ context.Context, value *domain.Review, expected uint64) error {
	stored, ok := m.items[value.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.Version != expected || value.Version != expected+1 {
		return domain.ErrConflict
	}
	copyValue := *value
	m.items[value.ID] = &copyValue
	return nil
}

type manifestMemory struct {
	items map[string]domain.InputManifest
}

func newManifestMemory() *manifestMemory {
	return &manifestMemory{items: map[string]domain.InputManifest{}}
}
func (m *manifestMemory) Create(_ context.Context, value domain.InputManifest) error {
	if _, ok := m.items[value.ID]; ok {
		return domain.ErrConflict
	}
	m.items[value.ID] = value
	return nil
}
func (m *manifestMemory) Get(_ context.Context, id string) (domain.InputManifest, error) {
	value, ok := m.items[id]
	if !ok {
		return domain.InputManifest{}, domain.ErrNotFound
	}
	return value, nil
}

type proposalMemory struct {
	items map[string]*domain.OutputProposal
}

func newProposalMemory() *proposalMemory {
	return &proposalMemory{items: map[string]*domain.OutputProposal{}}
}
func cloneProposal(value *domain.OutputProposal) *domain.OutputProposal {
	copyValue := *value
	copyValue.Operations = append([]domain.ProposalOperation(nil), value.Operations...)
	for index := range copyValue.Operations {
		copyValue.Operations[index].Value = append(json.RawMessage(nil), value.Operations[index].Value...)
		copyValue.Operations[index].DependsOn = append([]string(nil), value.Operations[index].DependsOn...)
	}
	return &copyValue
}
func (m *proposalMemory) Create(_ context.Context, value *domain.OutputProposal) error {
	if _, ok := m.items[value.ID]; ok {
		return domain.ErrConflict
	}
	m.items[value.ID] = cloneProposal(value)
	return nil
}
func (m *proposalMemory) Get(_ context.Context, id string) (*domain.OutputProposal, error) {
	value, ok := m.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return cloneProposal(value), nil
}
func (m *proposalMemory) Save(_ context.Context, value *domain.OutputProposal, expected uint64) error {
	stored, ok := m.items[value.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.Version != expected || value.Version != expected+1 {
		return domain.ErrConflict
	}
	m.items[value.ID] = cloneProposal(value)
	return nil
}

type definitionMemory struct {
	items map[string]map[int]domain.WorkflowDefinition
}

func newDefinitionMemory() *definitionMemory {
	return &definitionMemory{items: map[string]map[int]domain.WorkflowDefinition{}}
}
func (m *definitionMemory) Create(_ context.Context, value domain.WorkflowDefinition) error {
	if m.items[value.ID] == nil {
		m.items[value.ID] = map[int]domain.WorkflowDefinition{}
	}
	if _, ok := m.items[value.ID][value.Version]; ok {
		return domain.ErrConflict
	}
	m.items[value.ID][value.Version] = value
	return nil
}
func (m *definitionMemory) Get(_ context.Context, id string, version int) (domain.WorkflowDefinition, error) {
	versions := m.items[id]
	value, ok := versions[version]
	if !ok {
		return domain.WorkflowDefinition{}, domain.ErrNotFound
	}
	return value, nil
}
func (m *definitionMemory) LatestVersion(_ context.Context, id string) (int, error) {
	versions := m.items[id]
	if len(versions) == 0 {
		return 0, domain.ErrNotFound
	}
	latest := 0
	for version := range versions {
		if version > latest {
			latest = version
		}
	}
	return latest, nil
}

type runMemory struct {
	items map[string]*domain.WorkflowRun
}

func newRunMemory() *runMemory { return &runMemory{items: map[string]*domain.WorkflowRun{}} }
func cloneRun(value *domain.WorkflowRun) *domain.WorkflowRun {
	copyValue := *value
	copyValue.Nodes = map[string]*domain.NodeRun{}
	for id, node := range value.Nodes {
		copyNode := *node
		copyValue.Nodes[id] = &copyNode
	}
	return &copyValue
}
func (m *runMemory) Create(_ context.Context, value *domain.WorkflowRun) error {
	m.items[value.ID] = cloneRun(value)
	return nil
}
func (m *runMemory) Get(_ context.Context, id string) (*domain.WorkflowRun, error) {
	value, ok := m.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return cloneRun(value), nil
}
func (m *runMemory) Save(_ context.Context, value *domain.WorkflowRun, expected uint64) error {
	stored, ok := m.items[value.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.Version != expected || value.Version != expected+1 {
		return domain.ErrConflict
	}
	m.items[value.ID] = cloneRun(value)
	return nil
}

func revisionForAppTest(t *testing.T, id, artifactID string, number int, content string) domain.Revision {
	t.Helper()
	revision, err := domain.NewRevision(id, artifactID, number, nil, "", json.RawMessage(content), "author", appTestNow)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestArtifactServiceReviewAndImmutableRevisionLifecycle(t *testing.T) {
	ctx := context.Background()
	artifacts := newArtifactMemory()
	revisions := newRevisionMemory()
	drafts := newDraftMemory()
	reviews := newReviewMemory()
	service := ArtifactService{Artifacts: artifacts, Revisions: revisions, Drafts: drafts, Reviews: reviews, Tx: memoryTx{}, Clock: fixedClock{appTestNow}}
	artifact, err := service.CreateArtifact(ctx, CreateArtifactCommand{ID: "requirements", ProjectID: "project", Type: domain.ArtifactDocument, Title: "Requirements"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.CreateDraft(ctx, CreateDraftCommand{ID: "draft-1", ArtifactID: artifact.ID, AuthorID: "author", Content: json.RawMessage(`{"requirements":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.SubmitReview(ctx, SubmitReviewCommand{ReviewID: "review-1", DraftID: draft.ID, ReviewerID: "reviewer", ExpectedDraftVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DecideReview(ctx, DecideReviewCommand{ReviewID: review.ID, ActorID: "author", Decision: ApproveReview, ExpectedReviewVersion: 1, ExpectedDraftVersion: 2}); !errors.Is(err, domain.ErrSelfApproval) {
		t.Fatalf("expected self-approval guard, got %v", err)
	}
	if err := service.DecideReview(ctx, DecideReviewCommand{ReviewID: review.ID, ActorID: "reviewer", Decision: ApproveReview, ExpectedReviewVersion: 1, ExpectedDraftVersion: 2}); err != nil {
		t.Fatal(err)
	}
	revision, err := service.PublishApprovedDraft(ctx, PublishDraftCommand{DraftID: draft.ID, ActorID: "owner", RevisionID: "requirements-v1", ExpectedDraftVersion: 3, ExpectedArtifactVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if revision.Number() != 1 || artifacts.items[artifact.ID].CurrentRevisionID != revision.ID() || drafts.items[draft.ID].Status != domain.DraftApplied {
		t.Fatalf("unexpected published state")
	}
	if _, err := service.PublishApprovedDraft(ctx, PublishDraftCommand{DraftID: draft.ID, ActorID: "owner", RevisionID: "requirements-v2", ExpectedDraftVersion: 4, ExpectedArtifactVersion: 2}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected one-time publish, got %v", err)
	}
}

func TestProposalServicePartialApplyAndStaleGuard(t *testing.T) {
	ctx := context.Background()
	artifacts := newArtifactMemory()
	revisions := newRevisionMemory()
	drafts := newDraftMemory()
	manifests := newManifestMemory()
	proposals := newProposalMemory()
	artifact, _ := domain.NewArtifact("blueprint", "project", domain.ArtifactBlueprint, "Blueprint", appTestNow)
	revision := revisionForAppTest(t, "bp-v1", artifact.ID, 1, `{"name":"old","remove":true}`)
	_ = revisions.Create(ctx, revision)
	_ = artifact.AdvanceRevision(revision.Ref(""), 1, appTestNow)
	_ = artifacts.Create(ctx, artifact)
	service := ProposalService{Artifacts: artifacts, Revisions: revisions, Drafts: drafts, Manifests: manifests, Proposals: proposals, Tx: memoryTx{}, Clock: fixedClock{appTestNow}}
	manifest, err := service.CreateManifest(ctx, CreateManifestCommand{ID: "manifest", ProjectID: "project", JobType: "update_blueprint", BaseRevision: ptrRef(revision.Ref("")), Sources: []domain.ManifestSource{{Ref: revision.Ref(""), Purpose: "base"}}, OutputSchemaVersion: "v1", CreatedBy: "author"})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := service.CreateProposal(ctx, CreateProposalCommand{ID: "proposal", ManifestID: manifest.ID, ArtifactID: artifact.ID, CreatedBy: "ai", Operations: []domain.ProposalOperation{{ID: "rename", Kind: domain.OperationReplace, Path: "/name", Value: json.RawMessage(`"new"`)}, {ID: "remove", Kind: domain.OperationRemove, Path: "/remove"}}})
	if err != nil {
		t.Fatal(err)
	}
	proposal, _ = service.Decide(ctx, proposal.ID, "rename", domain.DecisionAccepted, "reviewer", "", 1)
	proposal, _ = service.Decide(ctx, proposal.ID, "remove", domain.DecisionRejected, "reviewer", "keep it", 2)
	draft, err := service.Apply(ctx, ApplyProposalCommand{ProposalID: proposal.ID, DraftID: "proposal-draft", ActorID: "editor", ExpectedProposalVersion: 3, ExpectedArtifactVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(draft.Content) != `{"name":"new","remove":true}` || proposals.items[proposal.ID].Status != domain.ProposalPartiallyApplied {
		t.Fatalf("unexpected partial apply: %s %+v", draft.Content, proposals.items[proposal.ID])
	}

	staleProposal, err := domain.NewOutputProposal("stale", "project", artifact.ID, manifest.Ref(), revision.Ref(""), []domain.ProposalOperation{{ID: "rename", Kind: domain.OperationReplace, Path: "/name", Value: json.RawMessage(`"later"`)}}, nil, nil, "ai", appTestNow)
	if err != nil {
		t.Fatal(err)
	}
	_ = proposals.Create(ctx, staleProposal)
	_, _ = service.Decide(ctx, "stale", "rename", domain.DecisionAccepted, "reviewer", "", 1)
	revision2, _ := domain.NewRevision("bp-v2", artifact.ID, 2, ptrRef(revision.Ref("")), "", json.RawMessage(`{"name":"other"}`), "owner", appTestNow)
	_ = revisions.Create(ctx, revision2)
	storedArtifact, _ := artifacts.Get(ctx, artifact.ID)
	_ = storedArtifact.AdvanceRevision(revision2.Ref(""), 2, appTestNow)
	_ = artifacts.Save(ctx, storedArtifact, 2)
	_, err = service.Apply(ctx, ApplyProposalCommand{ProposalID: "stale", DraftID: "never", ActorID: "editor", ExpectedProposalVersion: 2, ExpectedArtifactVersion: 3})
	if !errors.Is(err, domain.ErrStaleProposal) || proposals.items["stale"].Status != domain.ProposalStale {
		t.Fatalf("expected persisted stale proposal, got %v / %s", err, proposals.items["stale"].Status)
	}
}

func ptrRef(value domain.ArtifactRef) *domain.ArtifactRef { return &value }

func workflowNodesForApp() []domain.NodeDefinition {
	empty := json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	value := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)
	return []domain.NodeDefinition{{ID: "draft", Name: "Draft", Type: domain.NodeHumanTask, InputSchema: empty, OutputSchema: value, HumanTask: &domain.HumanTaskNodeConfig{TaskType: "draft", RequiredRole: "editor"}}, {ID: "generate", Name: "Generate", Type: domain.NodeAI, InputSchema: value, OutputSchema: empty, AI: &domain.AINodeConfig{JobType: "generate", OutputSchemaVersion: "v1"}}}
}

func TestWorkflowServiceVersionAndManifestPins(t *testing.T) {
	ctx := context.Background()
	definitions := newDefinitionMemory()
	runs := newRunMemory()
	manifests := newManifestMemory()
	service := WorkflowService{Definitions: definitions, Runs: runs, Manifests: manifests, Clock: fixedClock{appTestNow}}
	definition, err := service.RegisterDefinition(ctx, RegisterWorkflowCommand{ID: "delivery", Version: 1, Name: "Delivery", SchemaVersion: "v1", Nodes: workflowNodesForApp(), Edges: []domain.WorkflowEdge{{ID: "e1", From: "draft", To: "generate"}}, CreatedBy: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterDefinition(ctx, RegisterWorkflowCommand{ID: "delivery", Version: 3, Name: "Gap", SchemaVersion: "v1", Nodes: workflowNodesForApp(), CreatedBy: "owner"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected contiguous version guard, got %v", err)
	}
	revision := revisionForAppTest(t, "req-v1", "requirements", 1, `{}`)
	manifest, _ := domain.NewInputManifest("manifest", "project", "workflow", "", nil, []domain.ManifestSource{{Ref: revision.Ref(""), Purpose: "requirements"}}, nil, "v1", "owner", appTestNow)
	_ = manifests.Create(ctx, manifest)
	run, err := service.StartWorkflow(ctx, StartWorkflowCommand{RunID: "run", ProjectID: "project", CreatedBy: "owner", Definition: definition.Ref(), Manifest: manifest.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	badHash, _ := domain.CanonicalHash(map[string]string{"different": "hash"})
	if _, err := service.StartNode(ctx, StartNodeCommand{RunID: run.ID, NodeID: "draft", Manifest: domain.ManifestRef{ID: manifest.ID, Hash: badHash}, ExpectedVersion: 2}); !errors.Is(err, domain.ErrManifestUnpinned) {
		t.Fatalf("expected exact manifest pin, got %v", err)
	}
	run, err = service.StartNode(ctx, StartNodeCommand{RunID: run.ID, NodeID: "draft", Manifest: manifest.Ref(), ExpectedVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	run, err = service.CompleteNode(ctx, CompleteNodeCommand{RunID: run.ID, NodeID: "draft", ExpectedVersion: run.Version})
	if err != nil {
		t.Fatal(err)
	}
	if run.Nodes["generate"].Status != domain.NodeRunReady {
		t.Fatalf("expected DAG successor ready, got %s", run.Nodes["generate"].Status)
	}
	if _, err := service.StartNode(ctx, StartNodeCommand{RunID: run.ID, NodeID: "generate", Manifest: manifest.Ref(), ExpectedVersion: 2}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected optimistic concurrency conflict, got %v", err)
	}
}

func TestRepositoryFakesEnforceExpectedVersion(t *testing.T) {
	repo := newArtifactMemory()
	artifact, _ := domain.NewArtifact("a", "p", domain.ArtifactDocument, "A", appTestNow)
	_ = repo.Create(context.Background(), artifact)
	copyValue, _ := repo.Get(context.Background(), "a")
	revision := revisionForAppTest(t, "r", "a", 1, `{}`)
	_ = copyValue.AdvanceRevision(revision.Ref(""), 1, appTestNow)
	if err := repo.Save(context.Background(), copyValue, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected repository conflict, got %v", err)
	}
	if fmt.Sprint(repo.items["a"].Version) != "1" {
		t.Fatal("failed save mutated repository")
	}
}
