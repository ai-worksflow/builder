package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type planningSourceFake struct {
	facts PlanningFacts
	err   error
	calls int
}

func (source *planningSourceFake) LoadPlanningFacts(
	_ context.Context,
	_, _, _ string,
) (PlanningFacts, error) {
	source.calls++
	return source.facts, source.err
}

func TestDeterministicPlannerKeepsClientOutOfConstraintSurface(t *testing.T) {
	fixture := newAgentFixture(t)
	source := &planningSourceFake{facts: planningFactsFromFixture(fixture)}
	planner, err := NewDeterministicPlanner(source, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	input := PlanTaskInput{
		ProjectID: fixture.taskInput.ProjectID, SandboxSessionID: fixture.taskInput.SandboxSessionID,
		TaskKey:     fixture.taskInput.TaskKey,
		Instruction: "Add the contract-bound streaming interaction without changing deployment policy.",
		ActorID:     fixture.actorID, ContextPackID: uuid.NewString(), TaskCapsuleID: uuid.NewString(),
	}
	first, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContextPack.ContentHash != second.ContextPack.ContentHash ||
		first.TaskCapsule.ContentHash != second.TaskCapsule.ContentHash {
		t.Fatalf("deterministic plan hashes drifted: %#v != %#v", first, second)
	}
	if len(first.TaskCapsule.WriteSet) != len(fixture.taskInput.WriteSet) ||
		first.TaskCapsule.WriteSet[0] != fixture.taskInput.WriteSet[0] ||
		first.TaskCapsule.NetworkPolicy.Mode != "none" {
		t.Fatalf("planner widened server constraints: %#v", first.TaskCapsule)
	}
	if source.calls != 2 {
		t.Fatalf("PlanningSource calls = %d, want 2", source.calls)
	}
}

func TestDeterministicPlannerFailsClosedOnSourceDriftAndMissingInstruction(t *testing.T) {
	fixture := newAgentFixture(t)
	facts := planningFactsFromFixture(fixture)
	facts.ProjectID = uuid.NewString()
	planner, err := NewDeterministicPlanner(&planningSourceFake{facts: facts}, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	input := PlanTaskInput{
		ProjectID: fixture.taskInput.ProjectID, SandboxSessionID: fixture.taskInput.SandboxSessionID,
		TaskKey: fixture.taskInput.TaskKey, Instruction: "Implement the exact task.",
		ActorID: fixture.actorID, ContextPackID: uuid.NewString(), TaskCapsuleID: uuid.NewString(),
	}
	if _, err := planner.Plan(context.Background(), input); !errors.Is(err, ErrPlanningDrift) {
		t.Fatalf("source drift error = %v", err)
	}

	input.Instruction = "  "
	if _, err := planner.Plan(context.Background(), input); !errors.Is(err, ErrInvalidTaskCapsule) {
		t.Fatalf("missing instruction error = %v", err)
	}
}

func planningFactsFromFixture(fixture agentFixture) PlanningFacts {
	return PlanningFacts{
		ProjectID: fixture.taskInput.ProjectID, SandboxSessionID: fixture.taskInput.SandboxSessionID,
		CandidateID: fixture.taskInput.CandidateID, CandidateVersion: fixture.taskInput.CandidateVersion,
		CandidateSessionEpoch:     fixture.taskInput.CandidateSessionEpoch,
		CandidateWriterLeaseEpoch: fixture.taskInput.CandidateWriterLeaseEpoch,
		BaseCandidateTreeHash:     fixture.taskInput.BaseCandidateTreeHash,
		BuildContract:             fixture.taskInput.BuildContract, TemplateReleases: fixture.taskInput.TemplateReleases,
		TaskKey:                fixture.taskInput.TaskKey,
		Objective:              "Implement the server-selected vertical slice and satisfy every bound Must obligation.",
		ObligationIDs:          fixture.taskInput.ObligationIDs,
		AcceptanceCriterionIDs: fixture.taskInput.AcceptanceCriterionIDs,
		ReadSet:                fixture.taskInput.ReadSet, WriteSet: fixture.taskInput.WriteSet,
		ProtectedPaths: fixture.taskInput.ProtectedPaths, ContextItems: fixture.contextInput.Items,
		Preconditions: fixture.taskInput.Preconditions, Postconditions: fixture.taskInput.Postconditions,
		VerificationCommandIDs: fixture.taskInput.VerificationCommandIDs,
		AllowedTools:           fixture.taskInput.AllowedTools, NetworkPolicy: fixture.taskInput.NetworkPolicy,
		Budgets: fixture.taskInput.Budgets, OutputSchemaHash: fixture.taskInput.OutputSchemaHash,
	}
}
