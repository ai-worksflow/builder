package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

var (
	ErrPlanningBlocked = errors.New("agent task planning is blocked")
	ErrPlanningDrift   = errors.New("agent task planning source changed")
)

// PlanningFacts is trusted server input. A browser may choose TaskKey and add
// a bounded instruction, but it cannot supply exact revisions, paths, tools,
// commands, network policy, or budgets.
type PlanningFacts struct {
	ProjectID                 string
	SandboxSessionID          string
	CandidateID               string
	CandidateVersion          uint64
	CandidateSessionEpoch     uint64
	CandidateWriterLeaseEpoch uint64
	BaseCandidateTreeHash     string
	BuildContract             repository.ExactReference
	TemplateReleases          []repository.ExactReference
	TaskKey                   string
	Objective                 string
	ObligationIDs             []string
	AcceptanceCriterionIDs    []string
	ReadSet                   []string
	WriteSet                  []string
	ProtectedPaths            []string
	ContextItems              []ContextItem
	Preconditions             []string
	Postconditions            []string
	VerificationCommandIDs    []string
	AllowedTools              []string
	NetworkPolicy             NetworkPolicy
	Budgets                   TaskBudgets
	OutputSchemaHash          string
}

type PlanningSource interface {
	LoadPlanningFacts(context.Context, string, string, string) (PlanningFacts, error)
}

type PlanTaskInput struct {
	ProjectID        string
	SandboxSessionID string
	TaskKey          string
	Instruction      string
	ActorID          string
	ContextPackID    string
	TaskCapsuleID    string
}

type TaskPlanner interface {
	Plan(context.Context, PlanTaskInput) (TaskPlan, error)
}

// DeterministicPlanner seals authoritative source facts into immutable domain
// objects. It performs no model call: planning identity remains stable across
// model providers, retries, and process restarts.
type DeterministicPlanner struct {
	source PlanningSource
	now    func() time.Time
}

func NewDeterministicPlanner(source PlanningSource, now func() time.Time) (*DeterministicPlanner, error) {
	if source == nil || now == nil {
		return nil, errors.New("agent PlanningSource and clock are required")
	}
	return &DeterministicPlanner{source: source, now: now}, nil
}

func (planner *DeterministicPlanner) Plan(
	ctx context.Context,
	input PlanTaskInput,
) (TaskPlan, error) {
	if ctx == nil {
		return TaskPlan{}, fmt.Errorf("%w: context is required", ErrInvalidTaskCapsule)
	}
	if !validUUIDs(
		input.ProjectID,
		input.SandboxSessionID,
		input.ActorID,
		input.ContextPackID,
		input.TaskCapsuleID,
	) {
		return TaskPlan{}, fmt.Errorf("%w: exact plan identity", ErrInvalidTaskCapsule)
	}
	taskKey, err := normalizeStableValue(input.TaskKey, 160)
	if err != nil || taskKey != input.TaskKey {
		return TaskPlan{}, fmt.Errorf("%w: task key", ErrInvalidTaskCapsule)
	}
	instruction := strings.TrimSpace(input.Instruction)
	if instruction == "" || len(instruction) > 2000 || strings.ContainsRune(instruction, '\x00') {
		return TaskPlan{}, fmt.Errorf("%w: a bounded user instruction is required", ErrInvalidTaskCapsule)
	}

	facts, err := planner.source.LoadPlanningFacts(
		ctx, input.ProjectID, input.SandboxSessionID, taskKey,
	)
	if err != nil {
		return TaskPlan{}, err
	}
	if facts.ProjectID != input.ProjectID || facts.SandboxSessionID != input.SandboxSessionID ||
		facts.TaskKey != taskKey {
		return TaskPlan{}, fmt.Errorf("%w: PlanningSource returned a different project, Session, or task", ErrPlanningDrift)
	}
	objective := strings.TrimSpace(facts.Objective)
	if objective == "" {
		return TaskPlan{}, fmt.Errorf("%w: authoritative objective is missing", ErrPlanningBlocked)
	}
	objective += userIntentMarker + instruction
	if len(objective) > 4000 {
		return TaskPlan{}, fmt.Errorf("%w: composed objective exceeds the TaskCapsule limit", ErrPlanningBlocked)
	}
	now := planner.now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return TaskPlan{}, fmt.Errorf("%w: planner clock returned zero", ErrInvalidTaskCapsule)
	}
	pack, err := NewContextPack(NewContextPackInput{
		ID: input.ContextPackID, ProjectID: facts.ProjectID, CandidateID: facts.CandidateID,
		BaseCandidateTreeHash: facts.BaseCandidateTreeHash, BuildContract: facts.BuildContract,
		Items: facts.ContextItems, CreatedBy: input.ActorID,
	}, now)
	if err != nil {
		return TaskPlan{}, fmt.Errorf("%w: compile ContextPack: %v", ErrPlanningBlocked, err)
	}
	capsule, err := NewTaskCapsule(NewTaskCapsuleInput{
		ID: input.TaskCapsuleID, TaskKey: taskKey, ProjectID: facts.ProjectID,
		SandboxSessionID: facts.SandboxSessionID, CandidateID: facts.CandidateID,
		CandidateVersion: facts.CandidateVersion, CandidateSessionEpoch: facts.CandidateSessionEpoch,
		CandidateWriterLeaseEpoch: facts.CandidateWriterLeaseEpoch,
		BaseCandidateTreeHash:     facts.BaseCandidateTreeHash, BuildContract: facts.BuildContract,
		TemplateReleases: facts.TemplateReleases, Objective: objective,
		ObligationIDs: facts.ObligationIDs, AcceptanceCriterionIDs: facts.AcceptanceCriterionIDs,
		ReadSet: facts.ReadSet, WriteSet: facts.WriteSet, ProtectedPaths: facts.ProtectedPaths,
		Preconditions: facts.Preconditions, Postconditions: facts.Postconditions,
		VerificationCommandIDs: facts.VerificationCommandIDs, AllowedTools: facts.AllowedTools,
		NetworkPolicy: facts.NetworkPolicy, Budgets: facts.Budgets,
		OutputSchemaHash: facts.OutputSchemaHash, CreatedBy: input.ActorID,
	}, pack, now)
	if err != nil {
		return TaskPlan{}, fmt.Errorf("%w: compile TaskCapsule: %v", ErrPlanningBlocked, err)
	}
	return TaskPlan{ContextPack: pack, TaskCapsule: capsule}, nil
}

var _ TaskPlanner = (*DeterministicPlanner)(nil)
