package generation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCompletedConversationRecoveryChecksFullClaimIdentityPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	defer transaction.Rollback()
	if err := transaction.Exec(`
CREATE TEMP TABLE implementation_generation_claims (
  id uuid PRIMARY KEY,
  build_manifest_id uuid NOT NULL,
  project_id uuid NOT NULL,
  application_build_contract_id uuid,
  application_build_contract_hash text,
  root_manifest_id uuid NOT NULL,
  request_key uuid NOT NULL UNIQUE,
  reserved_proposal_id uuid NOT NULL UNIQUE,
  execution_source text NOT NULL,
  conversation_command_id uuid,
  governance_manifest_id uuid,
  governance_manifest_hash text,
  governance_source_refs jsonb,
  instruction jsonb NOT NULL,
  instruction_hash text NOT NULL,
  requested_model text NOT NULL,
  generation_contract_version text NOT NULL,
  system_prompt_hash text NOT NULL,
  output_schema_hash text NOT NULL,
  actor_id uuid NOT NULL,
  expected_active_proposal_id uuid,
  expected_active_proposal_version bigint,
  claim_token uuid,
  claim_expires_at timestamptz,
  status text NOT NULL,
  attempt_count integer NOT NULL,
  completed_proposal_id uuid,
  last_failure text,
  last_failed_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
) ON COMMIT DROP;
CREATE TEMP TABLE application_build_manifests (
  id uuid PRIMARY KEY, project_id uuid NOT NULL, workflow_run_id uuid, root_manifest_id uuid NOT NULL,
  derived_from_id uuid, workspace_revision_id uuid, root_ordinal integer, manifest_group_key text,
  delivery_slice_id text,
  schema_version integer NOT NULL, content_store text NOT NULL, content_ref text NOT NULL,
  content_hash text NOT NULL, manifest_hash text NOT NULL, status text NOT NULL, created_by uuid NOT NULL,
  created_at timestamptz NOT NULL, invalidated_at timestamptz, invalidation_reason text
) ON COMMIT DROP;
CREATE TEMP TABLE implementation_proposals (
  id uuid PRIMARY KEY, project_id uuid NOT NULL, build_manifest_id uuid NOT NULL,
  status text NOT NULL, version bigint NOT NULL, accepted_count integer NOT NULL DEFAULT 0,
  rejected_count integer NOT NULL DEFAULT 0, applied_at timestamptz, execution_source text NOT NULL
) ON COMMIT DROP;
CREATE TEMP TABLE audit_events (
  id uuid PRIMARY KEY, project_id uuid, actor_id uuid, request_id text, action text NOT NULL,
  target_type text NOT NULL, target_id text NOT NULL, metadata jsonb NOT NULL, created_at timestamptz NOT NULL
) ON COMMIT DROP;
CREATE TEMP TABLE outbox_events (
  id uuid PRIMARY KEY, aggregate_type text NOT NULL, aggregate_id text NOT NULL, event_type text NOT NULL,
  subject text NOT NULL, payload jsonb NOT NULL, headers jsonb NOT NULL, attempts integer NOT NULL,
  available_at timestamptz NOT NULL, published_at timestamptz, last_error text, created_at timestamptz NOT NULL
) ON COMMIT DROP`).Error; err != nil {
		t.Fatal(err)
	}
	projectID, leafID, rootID := uuid.New(), uuid.New(), uuid.New()
	commandID, actorA, actorB := uuid.New(), uuid.New(), uuid.New()
	governanceManifestID := uuid.New()
	governanceHash := generationSHA256([]byte("governance"))
	governanceRefs := json.RawMessage(`[{"artifactId":"a","revisionId":"r","contentHash":"sha256:content"}]`)
	instruction, instructionJSON, instructionHash, err := CanonicalImplementationInstruction("Build reviewed app", []string{"Keep traces"})
	if err != nil {
		t.Fatal(err)
	}
	applicationBuildContractID := uuid.New()
	applicationBuildContract := core.ApplicationBuildContractRef{
		ID: applicationBuildContractID.String(), ContractHash: strings.Repeat("a", 64),
	}
	replay, err := currentImplementationGenerationReplayIdentity(
		instructionJSON, instructionHash, "gpt-5", applicationBuildContract,
	)
	if err != nil {
		t.Fatal(err)
	}
	completedProposalID := commandID
	now := time.Now().UTC()
	if err := transaction.Create(&storage.ImplementationGenerationClaimModel{
		ID: uuid.New(), BuildManifestID: leafID, ProjectID: projectID, RootManifestID: rootID,
		ApplicationBuildContractID:   &applicationBuildContractID,
		ApplicationBuildContractHash: &applicationBuildContract.ContractHash,
		RequestKey:                   commandID, ReservedProposalID: commandID,
		ExecutionSource: string(core.ImplementationSourceConversationCommand), ConversationCommandID: &commandID,
		GovernanceManifestID: &governanceManifestID, GovernanceManifestHash: &governanceHash,
		GovernanceSourceRefs: governanceRefs,
		Instruction:          replay.Instruction, InstructionHash: instructionHash, RequestedModel: replay.RequestedModel,
		GenerationContractVersion: replay.GenerationContractVersion, SystemPromptHash: replay.SystemPromptHash,
		OutputSchemaHash: replay.OutputSchemaHash, ActorID: actorA, Status: "completed", AttemptCount: 1,
		CompletedProposalID: &completedProposalID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	commandIDString := commandID.String()
	request := ImplementationGenerationRequest{
		ActorID: actorB.String(), Model: "gpt-5", Instruction: instruction,
		ApplicationBuildContract: applicationBuildContract,
		ExecutionSource:          core.ImplementationSourceConversationCommand,
		RequestKey:               commandIDString, ProposalID: commandIDString, ConversationCommandID: &commandIDString,
		GovernanceManifest:   &domain.ManifestRef{ID: governanceManifestID.String(), Hash: governanceHash},
		GovernanceSourceRefs: []domain.ArtifactRef{{ArtifactID: "a", RevisionID: "r", ContentHash: "sha256:content"}},
	}
	bundle := core.WorkbenchBundle{ID: leafID.String(), ProjectID: projectID.String()}
	proposal := core.ImplementationProposal{
		ID: commandIDString, BuildManifestID: leafID.String(), CreatedBy: actorA.String(), Status: "open", Version: 1,
		ExecutionSource: core.ImplementationSourceConversationCommand, ConversationCommandID: &commandIDString,
		InstructionHash:          instructionHash,
		ApplicationBuildContract: &applicationBuildContract,
	}
	service := &Service{database: transaction}
	if _, err := service.recoverCompletedImplementationClaim(
		context.Background(), bundle, rootID.String(), request, replay, proposal,
	); err != nil {
		t.Fatalf("authorized cross-editor recovery failed: %v", err)
	}

	changedModel := replay
	changedModel.RequestedModel = "different-model"
	if _, err := service.recoverCompletedImplementationClaim(
		context.Background(), bundle, rootID.String(), request, changedModel, proposal,
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed requested model recovery error = %v, want conflict", err)
	}
	changedContract := replay
	changedContract.GenerationContractVersion = "implementation-proposal-generation/v3"
	if _, err := service.recoverCompletedImplementationClaim(
		context.Background(), bundle, rootID.String(), request, changedContract, proposal,
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed generation contract recovery error = %v, want conflict", err)
	}
	changedBuildContractRequest := request
	changedBuildContractRequest.ApplicationBuildContract = core.ApplicationBuildContractRef{
		ID: uuid.NewString(), ContractHash: applicationBuildContract.ContractHash,
	}
	if _, err := service.recoverCompletedImplementationClaim(
		context.Background(), bundle, rootID.String(), changedBuildContractRequest, replay, proposal,
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed Application Build Contract recovery error = %v, want conflict", err)
	}
	changedGovernance := request
	changedGovernance.GovernanceManifest = &domain.ManifestRef{
		ID: governanceManifestID.String(), Hash: generationSHA256([]byte("changed-governance")),
	}
	if _, err := service.recoverCompletedImplementationClaim(
		context.Background(), bundle, rootID.String(), changedGovernance, replay, proposal,
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed governance recovery error = %v, want conflict", err)
	}

	failedCommandID := uuid.New()
	failedProposalID := failedCommandID
	failure := "ai_unavailable"
	failedAt := now.Add(-time.Minute)
	if err := transaction.Create(&storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: projectID, RootManifestID: rootID, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: "root", ContentHash: generationSHA256([]byte("root")),
		ManifestHash: generationSHA256([]byte("root-manifest")), Status: "frozen", CreatedBy: actorA, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	rootIDCopy := rootID
	if err := transaction.Create(&storage.ApplicationBuildManifestModel{
		ID: leafID, ProjectID: projectID, RootManifestID: rootID, DerivedFromID: &rootIDCopy, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: "leaf", ContentHash: generationSHA256([]byte("leaf")),
		ManifestHash: generationSHA256([]byte("leaf-manifest")), Status: "frozen", CreatedBy: actorA, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := transaction.Create(&storage.ImplementationGenerationClaimModel{
		ID: uuid.New(), BuildManifestID: leafID, ProjectID: projectID, RootManifestID: rootID,
		ApplicationBuildContractID:   &applicationBuildContractID,
		ApplicationBuildContractHash: &applicationBuildContract.ContractHash,
		RequestKey:                   failedCommandID, ReservedProposalID: failedProposalID,
		ExecutionSource: string(core.ImplementationSourceConversationCommand), ConversationCommandID: &failedCommandID,
		GovernanceManifestID: &governanceManifestID, GovernanceManifestHash: &governanceHash,
		GovernanceSourceRefs: governanceRefs,
		Instruction:          replay.Instruction, InstructionHash: instructionHash, RequestedModel: replay.RequestedModel,
		GenerationContractVersion: replay.GenerationContractVersion, SystemPromptHash: replay.SystemPromptHash,
		OutputSchemaHash: replay.OutputSchemaHash, ActorID: actorA, Status: "failed", AttemptCount: 1,
		LastFailure: &failure, LastFailedAt: &failedAt, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	failedCommandString, failedProposalString := failedCommandID.String(), failedProposalID.String()
	retryRequest := request
	retryRequest.RequestKey, retryRequest.ProposalID, retryRequest.ConversationCommandID = failedCommandString, failedProposalString, &failedCommandString
	service.claimLease = 5 * time.Minute
	service.now = func() time.Time { return now }
	claimed, recoveredProposal, err := service.acquireImplementationClaim(
		context.Background(), bundle, rootID.String(), retryRequest, replay,
	)
	if err != nil {
		t.Fatalf("actor B could not take over actor A's failed claim before any product: %v", err)
	}
	if recoveredProposal != uuid.Nil || claimed.ActorID != actorB || claimed.ReservedProposalID != failedProposalID {
		t.Fatalf("failed-claim takeover returned the wrong identity: claim=%+v recovered=%s", claimed, recoveredProposal)
	}
	var retried storage.ImplementationGenerationClaimModel
	if err := transaction.Where("request_key = ?", failedCommandID).Take(&retried).Error; err != nil {
		t.Fatal(err)
	}
	if retried.Status != "processing" || retried.ActorID != actorB || retried.AttemptCount != 2 || retried.ClaimToken == nil {
		t.Fatalf("failed-claim takeover was not durably fenced: %+v", retried)
	}
}
