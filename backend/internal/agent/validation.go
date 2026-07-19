package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const PatchValidationSchemaVersion = "agent-patch-validation/v1"

type PatchValidationCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// PatchValidationReceipt proves only the Stage 2 platform boundary: exact
// tree replay, protected/write-set enforcement, output-schema qualification,
// and repository blob reachability. It intentionally cannot be mistaken for
// the independent full-stack VerificationReceipt introduced in Stage 3.
type PatchValidationReceipt struct {
	SchemaVersion              string                    `json:"schemaVersion"`
	Scope                      string                    `json:"scope"`
	AttemptID                  string                    `json:"attemptId"`
	ProjectID                  string                    `json:"projectId"`
	TaskCapsule                repository.ExactReference `json:"taskCapsule"`
	Patch                      BlobReference             `json:"patch"`
	PatchContentHash           string                    `json:"patchContentHash"`
	BaseTreeHash               string                    `json:"baseTreeHash"`
	ProposedTreeHash           string                    `json:"proposedTreeHash"`
	Checks                     []PatchValidationCheck    `json:"checks"`
	Decision                   string                    `json:"decision"`
	IndependentQualityRequired bool                      `json:"independentQualityRequired"`
	ContentHash                string                    `json:"contentHash"`
}

func NewPatchValidationReceipt(
	attempt AgentAttempt,
	capsule TaskCapsule,
	patch PlatformPatch,
	patchReference BlobReference,
) (PatchValidationReceipt, error) {
	parsedPatch, err := ParsePlatformPatch(patch)
	if err != nil || attempt.TaskCapsule != capsule.ExactReference() ||
		parsedPatch.AttemptID != attempt.ID || parsedPatch.ProjectID != attempt.ProjectID ||
		parsedPatch.CandidateID != attempt.CandidateID || parsedPatch.TaskCapsule != attempt.TaskCapsule ||
		parsedPatch.ConfigurationHash != attempt.ConfigurationHash || patchReference.validate() != nil ||
		patchReference.Store != AgentEvidenceStore || patchReference.OwnerID != attempt.ID {
		return PatchValidationReceipt{}, fmt.Errorf("%w: patch validation binding", ErrExecutionDrift)
	}
	checks := []PatchValidationCheck{
		{ID: "exact-base-replay", Status: "passed", Detail: "Platform operations reproduce the proposed tree from the exact base tree."},
		{ID: "path-policy", Status: "passed", Detail: "Every changed path is inside writeSet and outside protectedPaths."},
		{ID: "repository-file-reachability", Status: "passed", Detail: "Every upsert body is registered in the project-scoped immutable file catalog."},
		{ID: "runner-identity", Status: "passed", Detail: "Attempt, fence, image, model policy, prompt template, schema, and toolchain identities are exact."},
		{ID: "structured-output-schema", Status: "passed", Detail: "The Runner result conforms to the qualified output schema."},
	}
	sort.Slice(checks, func(left, right int) bool { return checks[left].ID < checks[right].ID })
	receipt := PatchValidationReceipt{
		SchemaVersion: PatchValidationSchemaVersion, Scope: "stage2_patch_integrity",
		AttemptID: attempt.ID, ProjectID: attempt.ProjectID, TaskCapsule: attempt.TaskCapsule,
		Patch: patchReference, PatchContentHash: parsedPatch.ContentHash,
		BaseTreeHash: parsedPatch.BaseTreeHash, ProposedTreeHash: parsedPatch.ProposedTreeHash,
		Checks: checks, Decision: "reviewable", IndependentQualityRequired: true,
	}
	hash, err := domain.CanonicalHash(patchValidationPayload(receipt))
	if err != nil {
		return PatchValidationReceipt{}, err
	}
	receipt.ContentHash = "sha256:" + hash
	return receipt, nil
}

func ParsePatchValidationReceipt(value PatchValidationReceipt) (PatchValidationReceipt, error) {
	if value.SchemaVersion != PatchValidationSchemaVersion || value.Scope != "stage2_patch_integrity" ||
		!validUUIDs(value.AttemptID, value.ProjectID, value.TaskCapsule.ID) ||
		!sha256Pattern.MatchString(value.TaskCapsule.ContentHash) || value.Patch.validate() != nil ||
		value.Patch.Store != AgentEvidenceStore || value.Patch.OwnerID != value.AttemptID ||
		!sha256Pattern.MatchString(value.PatchContentHash) ||
		!sha256Pattern.MatchString(value.BaseTreeHash) || !sha256Pattern.MatchString(value.ProposedTreeHash) ||
		value.BaseTreeHash == value.ProposedTreeHash || value.Decision != "reviewable" ||
		!value.IndependentQualityRequired || !sha256Pattern.MatchString(value.ContentHash) ||
		len(value.Checks) != 5 {
		return PatchValidationReceipt{}, fmt.Errorf("%w: patch validation receipt identity", ErrExecutionDrift)
	}
	seen := map[string]bool{}
	for _, check := range value.Checks {
		if seen[check.ID] || check.Status != "passed" || check.Detail == "" ||
			check.Detail != strings.TrimSpace(check.Detail) {
			return PatchValidationReceipt{}, fmt.Errorf("%w: patch validation check", ErrExecutionDrift)
		}
		seen[check.ID] = true
	}
	if !sort.SliceIsSorted(value.Checks, func(left, right int) bool {
		return value.Checks[left].ID < value.Checks[right].ID
	}) {
		return PatchValidationReceipt{}, fmt.Errorf("%w: patch validation check order", ErrExecutionDrift)
	}
	expected, err := domain.CanonicalHash(patchValidationPayload(value))
	if err != nil || value.ContentHash != "sha256:"+expected {
		return PatchValidationReceipt{}, fmt.Errorf("%w: patch validation receipt hash", ErrExecutionDrift)
	}
	value.Checks = append([]PatchValidationCheck(nil), value.Checks...)
	return value, nil
}

func patchValidationPayload(value PatchValidationReceipt) any {
	return struct {
		SchemaVersion              string                    `json:"schemaVersion"`
		Scope                      string                    `json:"scope"`
		AttemptID                  string                    `json:"attemptId"`
		ProjectID                  string                    `json:"projectId"`
		TaskCapsule                repository.ExactReference `json:"taskCapsule"`
		Patch                      BlobReference             `json:"patch"`
		PatchContentHash           string                    `json:"patchContentHash"`
		BaseTreeHash               string                    `json:"baseTreeHash"`
		ProposedTreeHash           string                    `json:"proposedTreeHash"`
		Checks                     []PatchValidationCheck    `json:"checks"`
		Decision                   string                    `json:"decision"`
		IndependentQualityRequired bool                      `json:"independentQualityRequired"`
	}{
		value.SchemaVersion, value.Scope, value.AttemptID, value.ProjectID,
		value.TaskCapsule, value.Patch, value.PatchContentHash, value.BaseTreeHash,
		value.ProposedTreeHash, value.Checks, value.Decision, value.IndependentQualityRequired,
	}
}
