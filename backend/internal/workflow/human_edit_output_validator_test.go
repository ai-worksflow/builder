package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type fakeHumanEditArtifacts struct {
	artifacts map[string]core.VersionedArtifact
	revisions map[string]core.ArtifactRevision
}

func (f *fakeHumanEditArtifacts) Get(_ context.Context, artifactID, _ string, _ bool) (core.VersionedArtifact, error) {
	artifact, exists := f.artifacts[artifactID]
	if !exists {
		return core.VersionedArtifact{}, core.ErrNotFound
	}
	return artifact, nil
}

func (f *fakeHumanEditArtifacts) GetRevision(_ context.Context, revisionID, _ string) (core.ArtifactRevision, error) {
	revision, exists := f.revisions[revisionID]
	if !exists {
		return core.ArtifactRevision{}, core.ErrNotFound
	}
	return revision, nil
}

type fakeHumanEditProposals struct {
	manifests map[string]domain.InputManifest
	proposals map[string]domain.OutputProposal
}

func (f *fakeHumanEditProposals) GetManifest(_ context.Context, id, _ string) (domain.InputManifest, error) {
	manifest, exists := f.manifests[id]
	if !exists {
		return domain.InputManifest{}, core.ErrNotFound
	}
	return manifest, nil
}

func (f *fakeHumanEditProposals) GetProposal(_ context.Context, id, _ string) (domain.OutputProposal, error) {
	proposal, exists := f.proposals[id]
	if !exists {
		return domain.OutputProposal{}, core.ErrNotFound
	}
	return proposal, nil
}

type humanEditProposalFixture struct {
	validator CoreHumanEditOutputValidator
	execution Execution
	base      domain.ArtifactRef
	applied   domain.ArtifactRef
	current   domain.ArtifactRef
	manifest  domain.InputManifest
	proposal  domain.OutputProposal
}

func newHumanEditProposalFixture(t *testing.T) humanEditProposalFixture {
	t.Helper()
	projectID, actorID, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := humanEditRef(artifactID, "base")
	upstream := humanEditRef(uuid.NewString(), "upstream")
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, "derive_requirements", "", &base,
		[]domain.ManifestSource{{Ref: upstream, Purpose: "project_brief"}},
		json.RawMessage(`{"strict":true}`), "requirements-proposal/v1", actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := domain.NewOutputProposal(
		uuid.NewString(), projectID, artifactID, manifest.Ref(), base,
		[]domain.ProposalOperation{{ID: "replace-title", Kind: domain.OperationReplace, Path: "/title", Value: json.RawMessage(`"Reviewed"`)}},
		nil, nil, actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	applied := humanEditRef(artifactID, "proposal-applied")
	current := humanEditRef(artifactID, "manual-follow-up")
	baseParent := base.RevisionID
	appliedParent := applied.RevisionID
	proposalID := proposal.ID
	artifacts := &fakeHumanEditArtifacts{
		artifacts: map[string]core.VersionedArtifact{
			artifactID: {Artifact: core.Artifact{ID: artifactID, ProjectID: projectID, Kind: "product_requirements"}},
		},
		revisions: map[string]core.ArtifactRevision{
			base.RevisionID: {
				ID: base.RevisionID, ArtifactID: artifactID, ContentHash: base.ContentHash,
			},
			applied.RevisionID: {
				ID: applied.RevisionID, ArtifactID: artifactID, ContentHash: applied.ContentHash,
				ParentRevisionID: &baseParent, ProposalID: &proposalID,
			},
			current.RevisionID: {
				ID: current.RevisionID, ArtifactID: artifactID, ContentHash: current.ContentHash,
				ParentRevisionID: &appliedParent,
			},
		},
	}
	proposals := &fakeHumanEditProposals{
		manifests: map[string]domain.InputManifest{manifest.ID: manifest},
		proposals: map[string]domain.OutputProposal{proposal.ID: *proposal},
	}
	inputs := humanEditProposalInputs(t, manifest.Ref(), domain.ProposalRef{ID: proposal.ID, PayloadHash: proposal.PayloadHash})
	execution := Execution{
		Run:  RunRecord{ID: uuid.NewString(), ProjectID: projectID, StartedBy: actorID},
		Node: NodeRecord{ID: uuid.NewString(), Key: "requirements-edit", Type: domain.NodeHumanEdit},
		Definition: domain.NodeDefinition{
			ID: "requirements-edit", Name: "Edit requirements", Type: domain.NodeHumanEdit,
			HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, ArtifactKind: "product_requirements", RequiredRole: "editor"},
		},
		Inputs: inputs,
	}
	return humanEditProposalFixture{
		validator: CoreHumanEditOutputValidator{Artifacts: artifacts, Proposals: proposals},
		execution: execution, base: base, applied: applied, current: current, manifest: manifest, proposal: *proposal,
	}
}

func humanEditProposalInputs(t *testing.T, manifest domain.ManifestRef, proposal domain.ProposalRef) domain.NodeInputEnvelope {
	t.Helper()
	envelope, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "ai-edit", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: uuid.NewString(), NodeKey: "requirements-ai", DefinitionNodeID: "requirements-ai",
			InputManifest: &manifest, OutputProposal: &proposal,
		},
		Output: json.RawMessage(`{}`), Value: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func humanEditRef(artifactID, label string) domain.ArtifactRef {
	hash, _ := domain.CanonicalHash(map[string]any{"humanEdit": label})
	return domain.ArtifactRef{ArtifactID: artifactID, RevisionID: uuid.NewString(), ContentHash: hash}
}

func humanEditOutput(t *testing.T, ref domain.ArtifactRef) json.RawMessage {
	t.Helper()
	encoded, err := domain.CanonicalJSON(map[string]any{"artifactRevision": ref})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestCoreHumanEditOutputValidatorAcceptsOnlyExactProposalDescendant(t *testing.T) {
	fixture := newHumanEditProposalFixture(t)
	validated, err := fixture.validator.ValidateHumanEdit(
		context.Background(), fixture.execution, humanEditOutput(t, fixture.current), fixture.execution.Run.StartedBy,
	)
	if err != nil {
		t.Fatal(err)
	}
	if validated.ArtifactKind != "product_requirements" || !validated.Primary.Equal(fixture.current) {
		t.Fatalf("unexpected validated output: %+v", validated)
	}

	t.Run("proposal base cannot masquerade as generated output", func(t *testing.T) {
		if _, err := fixture.validator.ValidateHumanEdit(context.Background(), fixture.execution, humanEditOutput(t, fixture.base), fixture.execution.Run.StartedBy); err == nil {
			t.Fatal("proposal base revision bypassed strict descendant validation")
		}
	})

	t.Run("descendant chain must contain proposal identity", func(t *testing.T) {
		artifacts := fixture.validator.Artifacts.(*fakeHumanEditArtifacts)
		withoutProposal := *artifacts
		withoutProposal.revisions = make(map[string]core.ArtifactRevision, len(artifacts.revisions))
		for id, revision := range artifacts.revisions {
			revision.ProposalID = nil
			withoutProposal.revisions[id] = revision
		}
		validator := fixture.validator
		validator.Artifacts = &withoutProposal
		if _, err := validator.ValidateHumanEdit(context.Background(), fixture.execution, humanEditOutput(t, fixture.current), fixture.execution.Run.StartedBy); err == nil {
			t.Fatal("revision ancestry without the bound proposal id was accepted")
		}
	})

	t.Run("proposal payload hash is exact", func(t *testing.T) {
		wrongHash, _ := domain.CanonicalHash(map[string]any{"wrong": true})
		execution := fixture.execution
		execution.Inputs = humanEditProposalInputs(t, fixture.manifest.Ref(), domain.ProposalRef{ID: fixture.proposal.ID, PayloadHash: wrongHash})
		if _, err := fixture.validator.ValidateHumanEdit(context.Background(), execution, humanEditOutput(t, fixture.current), execution.Run.StartedBy); err == nil {
			t.Fatal("wrong proposal payload hash was accepted")
		}
	})

	t.Run("proposal manifest is exact", func(t *testing.T) {
		wrongHash, _ := domain.CanonicalHash(map[string]any{"otherManifest": true})
		execution := fixture.execution
		execution.Inputs = humanEditProposalInputs(t, domain.ManifestRef{ID: uuid.NewString(), Hash: wrongHash}, domain.ProposalRef{ID: fixture.proposal.ID, PayloadHash: fixture.proposal.PayloadHash})
		if _, err := fixture.validator.ValidateHumanEdit(context.Background(), execution, humanEditOutput(t, fixture.current), execution.Run.StartedBy); err == nil {
			t.Fatal("proposal bound to another manifest was accepted")
		}
	})

	t.Run("exact-kind node cannot smuggle an extra revision", func(t *testing.T) {
		output, err := domain.CanonicalJSON(map[string]any{
			"artifactRevision": fixture.current, "artifactRevisions": []domain.ArtifactRef{fixture.applied},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.validator.ValidateHumanEdit(context.Background(), fixture.execution, output, fixture.execution.Run.StartedBy); err == nil {
			t.Fatal("exact-kind HumanEdit accepted multiple output revisions")
		}
	})
}

func TestCoreHumanEditOutputValidatorRejectsProjectKindAndHashBypass(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*humanEditProposalFixture) json.RawMessage
	}{
		{
			name: "project",
			mutate: func(f *humanEditProposalFixture) json.RawMessage {
				artifacts := f.validator.Artifacts.(*fakeHumanEditArtifacts)
				value := artifacts.artifacts[f.current.ArtifactID]
				value.Artifact.ProjectID = uuid.NewString()
				artifacts.artifacts[f.current.ArtifactID] = value
				return humanEditOutput(t, f.current)
			},
		},
		{
			name: "kind",
			mutate: func(f *humanEditProposalFixture) json.RawMessage {
				artifacts := f.validator.Artifacts.(*fakeHumanEditArtifacts)
				value := artifacts.artifacts[f.current.ArtifactID]
				value.Artifact.Kind = "project_brief"
				artifacts.artifacts[f.current.ArtifactID] = value
				return humanEditOutput(t, f.current)
			},
		},
		{
			name: "hash",
			mutate: func(f *humanEditProposalFixture) json.RawMessage {
				ref := f.current
				ref.ContentHash, _ = domain.CanonicalHash(map[string]any{"forged": true})
				return humanEditOutput(t, ref)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newHumanEditProposalFixture(t)
			output := testCase.mutate(&fixture)
			if _, err := fixture.validator.ValidateHumanEdit(context.Background(), fixture.execution, output, fixture.execution.Run.StartedBy); err == nil {
				t.Fatal("forged human edit reference was accepted")
			}
		})
	}
}

func TestCoreHumanEditOutputValidatorKeepsProjectBriefEntryArtifactID(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	entry := humanEditRef(uuid.NewString(), "entry")
	other := humanEditRef(uuid.NewString(), "other")
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, "workflow_start", "", &entry,
		[]domain.ManifestSource{{Ref: entry, Purpose: "project_brief"}}, json.RawMessage(`{}`), "workflow/v1", actorID, time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := &fakeHumanEditArtifacts{artifacts: map[string]core.VersionedArtifact{}, revisions: map[string]core.ArtifactRevision{}}
	for _, ref := range []domain.ArtifactRef{entry, other} {
		artifacts.artifacts[ref.ArtifactID] = core.VersionedArtifact{Artifact: core.Artifact{ID: ref.ArtifactID, ProjectID: projectID, Kind: "project_brief"}}
		artifacts.revisions[ref.RevisionID] = core.ArtifactRevision{ID: ref.RevisionID, ArtifactID: ref.ArtifactID, ContentHash: ref.ContentHash}
	}
	proposals := &fakeHumanEditProposals{manifests: map[string]domain.InputManifest{manifest.ID: manifest}, proposals: map[string]domain.OutputProposal{}}
	validator := CoreHumanEditOutputValidator{Artifacts: artifacts, Proposals: proposals}
	execution := Execution{
		Run:        RunRecord{ID: uuid.NewString(), ProjectID: projectID, StartedBy: actorID, InputManifest: ptrManifest(manifest.Ref())},
		Definition: domain.NodeDefinition{ID: "brief-edit", Type: domain.NodeHumanEdit, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, ArtifactKind: "project_brief", RequiredRole: "editor"}},
	}
	if _, err := validator.ValidateHumanEdit(context.Background(), execution, humanEditOutput(t, entry), actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := validator.ValidateHumanEdit(context.Background(), execution, humanEditOutput(t, other), actorID); err == nil {
		t.Fatal("Project Brief human edit switched away from the workflow entry artifact")
	}
}

func TestHumanEditSliceLineageUsesResolvedExactKind(t *testing.T) {
	blueprint := humanEditRef(uuid.NewString(), "blueprint")
	pageSpec := humanEditRef(uuid.NewString(), "page-spec")
	prototype := humanEditRef(uuid.NewString(), "prototype")
	run := &RunRecord{Context: NewRunContext()}
	run.Context.Slices["slice"] = SliceContext{ID: "slice", Blueprint: blueprint}
	if err := applyHumanEditSliceLineage(run, "slice", HumanEditValidation{ArtifactRefs: []domain.ArtifactRef{pageSpec}, Primary: pageSpec, ArtifactKind: "page_spec"}); err != nil {
		t.Fatal(err)
	}
	if run.Context.Slices["slice"].PageSpec == nil || !run.Context.Slices["slice"].PageSpec.Equal(pageSpec) || !run.Context.Slices["slice"].Blueprint.Equal(blueprint) {
		t.Fatalf("PageSpec was written into the wrong slice slot: %+v", run.Context.Slices["slice"])
	}
	if err := applyHumanEditSliceLineage(run, "slice", HumanEditValidation{ArtifactRefs: []domain.ArtifactRef{prototype}, Primary: prototype, ArtifactKind: "prototype"}); err != nil {
		t.Fatal(err)
	}
	if run.Context.Slices["slice"].Prototype == nil || !run.Context.Slices["slice"].Prototype.Equal(prototype) {
		t.Fatalf("Prototype was written into the wrong slice slot: %+v", run.Context.Slices["slice"])
	}
}

func TestHumanWorkflowContextRequiresSchemaOrServerAllowlist(t *testing.T) {
	declaredSchema := json.RawMessage(`{
		"type":"object",
		"properties":{"workflowContext":{"type":"object","properties":{"decision":{"type":"string"}}}},
		"additionalProperties":true
	}`)
	definition := domain.NodeDefinition{
		ID: "edit", Type: domain.NodeHumanEdit, OutputSchema: declaredSchema,
		HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, RequiredRole: "editor"},
	}
	engine := &Engine{HumanWorkflowContextKeys: map[string]struct{}{"serverNote": {}, "deliverySlices": {}}}
	accepted, err := engine.validatedHumanWorkflowContext(definition, json.RawMessage(`{"workflowContext":{"decision":"approved","serverNote":"kept"}}`))
	if err != nil || len(accepted) != 2 {
		t.Fatalf("declared/allowlisted workflow context rejected: values=%v err=%v", accepted, err)
	}
	if _, err := engine.validatedHumanWorkflowContext(definition, json.RawMessage(`{"workflowContext":{"undeclared":true}}`)); err == nil {
		t.Fatal("undeclared client workflow context was accepted")
	}
	if _, err := engine.validatedHumanWorkflowContext(definition, json.RawMessage(`{"workflowContext":{"deliverySlices":[]}}`)); err == nil {
		t.Fatal("client-built deliverySlices bypassed the trusted fan-out boundary")
	}
}

func TestHumanEditValidatorIsFailClosedWhenMissing(t *testing.T) {
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	if engine.HumanEditOutput != nil {
		t.Fatal("bare engine unexpectedly installed a permissive human edit validator")
	}
	if !errors.Is(humanEditValidationError("artifactRevision", "blocked"), domain.ErrValidation) {
		t.Fatal("human edit validation errors must remain typed validation failures")
	}
}
