package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

func TestContextPackAndTaskCapsuleAreCanonicalAndContentAddressed(t *testing.T) {
	fixture := newAgentFixture(t)
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if pack.SchemaVersion != ContextPackSchemaVersion || !sha256Pattern.MatchString(pack.ContentHash) ||
		pack.Items[0].Kind != ContextBuildContract || pack.Items[1].Kind != ContextRepositoryFile {
		t.Fatalf("ContextPack was not canonical: %#v", pack)
	}

	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if capsule.SchemaVersion != TaskCapsuleSchemaVersion || !sha256Pattern.MatchString(capsule.ContentHash) ||
		capsule.ContextPack != pack.ExactReference() || capsule.ObligationIDs[0] != "OBL-1" ||
		capsule.TemplateReleases[0].ID > capsule.TemplateReleases[1].ID {
		t.Fatalf("TaskCapsule was not canonical: %#v", capsule)
	}

	reordered := fixture.taskInput
	reordered.ObligationIDs = []string{"OBL-1", "OBL-2"}
	reordered.AcceptanceCriterionIDs = []string{"AC-1", "AC-2"}
	reordered.ReadSet = []string{"apps/api", "apps/web"}
	reordered.TemplateReleases = []repository.ExactReference{
		fixture.taskInput.TemplateReleases[1], fixture.taskInput.TemplateReleases[0],
	}
	repeated, err := NewTaskCapsule(reordered, pack, fixture.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ContentHash != capsule.ContentHash {
		t.Fatalf("semantic hash changed with input order: %s != %s", repeated.ContentHash, capsule.ContentHash)
	}

	tampered := capsule
	tampered.Objective = "silently widened objective"
	if _, err := ParseTaskCapsule(tampered, pack); !errors.Is(err, ErrInvalidTaskCapsule) {
		t.Fatalf("tampered capsule parsed: %v", err)
	}
}

func TestTaskCapsuleRejectsProtectedWriteAndUnboundedNetwork(t *testing.T) {
	fixture := newAgentFixture(t)
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}

	protected := fixture.taskInput
	protected.WriteSet = []string{".github/workflows/release.yml"}
	if _, err := NewTaskCapsule(protected, pack, fixture.now.Add(time.Second)); !errors.Is(err, ErrInvalidTaskCapsule) {
		t.Fatalf("protected write accepted: %v", err)
	}

	publicNetwork := fixture.taskInput
	publicNetwork.NetworkPolicy = NetworkPolicy{Mode: "none", AllowedHosts: []string{"api.openai.com"}}
	if _, err := NewTaskCapsule(publicNetwork, pack, fixture.now.Add(time.Second)); !errors.Is(err, ErrInvalidTaskCapsule) {
		t.Fatalf("network escape accepted: %v", err)
	}

	noOracle := fixture.taskInput
	noOracle.VerificationCommandIDs = nil
	if _, err := NewTaskCapsule(noOracle, pack, fixture.now.Add(time.Second)); !errors.Is(err, ErrInvalidTaskCapsule) {
		t.Fatalf("capsule without verification command accepted: %v", err)
	}
}

type agentFixture struct {
	now          time.Time
	actorID      string
	contextInput NewContextPackInput
	taskInput    NewTaskCapsuleInput
}

func newAgentFixture(t *testing.T) agentFixture {
	t.Helper()
	now := time.Date(2026, 7, 16, 12, 0, 0, 123456000, time.UTC)
	actorID := uuid.NewString()
	projectID := uuid.NewString()
	candidateID := uuid.NewString()
	// ApplicationBuildContract currently uses the platform's canonical raw
	// 64-hex contract hash, while repository/blob/template hashes are prefixed.
	contract := repository.ExactReference{ID: uuid.NewString(), ContentHash: testHash("1")[7:]}
	contextInput := NewContextPackInput{
		ID: uuid.NewString(), ProjectID: projectID, CandidateID: candidateID,
		BaseCandidateTreeHash: testHash("2"), BuildContract: contract,
		Items: []ContextItem{
			{
				Key: "repo:web", Kind: ContextRepositoryFile, Path: "apps/web/page.tsx", Required: true,
				Content: testBlob("3", 128),
			},
			{
				Key: "contract:root", Kind: ContextBuildContract, Required: true,
				Source: &contract, Content: testBlob("4", 512),
			},
		},
		CreatedBy: actorID,
	}
	taskInput := NewTaskCapsuleInput{
		ID: uuid.NewString(), TaskKey: "vertical-conversation-slice", ProjectID: projectID,
		SandboxSessionID: uuid.NewString(), CandidateID: candidateID,
		CandidateVersion: 3, CandidateSessionEpoch: 2, CandidateWriterLeaseEpoch: 4,
		BaseCandidateTreeHash: contextInput.BaseCandidateTreeHash, BuildContract: contract,
		TemplateReleases: []repository.ExactReference{
			{ID: uuid.NewString(), ContentHash: testHash("6")},
			{ID: uuid.NewString(), ContentHash: testHash("5")},
		},
		Objective:     "Implement one contract-bound vertical conversation slice.",
		ObligationIDs: []string{"OBL-2", "OBL-1"}, AcceptanceCriterionIDs: []string{"AC-2", "AC-1"},
		ReadSet: []string{"apps/web", "apps/api"}, WriteSet: []string{"apps/web/features/conversation"},
		ProtectedPaths:         []string{".github", "infra/production"},
		Preconditions:          []string{"The exact BuildContract is ready."},
		Postconditions:         []string{"Every Must acceptance criterion has executable evidence."},
		VerificationCommandIDs: []string{"test-contract", "typecheck-web"},
		AllowedTools:           []string{"shell.exec", "file.search", "file.read", "file.write"},
		NetworkPolicy:          NetworkPolicy{Mode: "none"},
		Budgets: TaskBudgets{
			WallTimeSeconds: 900, MaxInputTokens: 200000, MaxOutputTokens: 50000,
			MaxCommands: 100, MaxLogBytes: 4 << 20, MaxPatchBytes: 16 << 20,
		},
		OutputSchemaHash: testHash("7"), CreatedBy: actorID,
	}
	return agentFixture{now: now, actorID: actorID, contextInput: contextInput, taskInput: taskInput}
}

func testBlob(digit string, byteSize int64) BlobReference {
	return BlobReference{
		Store: "content", OwnerID: uuid.NewString(), Ref: "agent-fixture-" + digit,
		ContentHash: testHash(digit), ByteSize: byteSize,
	}
}

func testHash(digit string) string { return "sha256:" + repeat(digit, 64) }

func repeat(value string, count int) string {
	result := ""
	for index := 0; index < count; index++ {
		result += value
	}
	return result
}
