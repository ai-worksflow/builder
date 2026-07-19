package verification

import (
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const CanonicalReceiptSchemaVersion = "canonical-verification-receipt/v1"

type CanonicalReleaseArtifact struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Store       string `json:"store"`
	Ref         string `json:"ref"`
	ContentHash string `json:"contentHash"`
	MediaType   string `json:"mediaType"`
	ByteSize    int64  `json:"byteSize"`
}

type NewCanonicalReceiptInput struct {
	ID                string
	RunID             string
	ProjectID         string
	Subject           CanonicalPlanSubject
	BuildManifest     repository.ExactReference
	BuildContract     repository.ExactReference
	FullStackTemplate repository.ExactReference
	Profile           ProfileReference
	Plan              PlanReference
	AttemptIDs        []string
	Checks            []CheckResult
	Obligations       []ObligationRequirement
	ReleaseArtifacts  []CanonicalReleaseArtifact
	ExecutionError    string
	CreatedBy         string
	CreatedAt         time.Time
}

type CanonicalReceipt struct {
	SchemaVersion      string                     `json:"schemaVersion"`
	ID                 string                     `json:"id"`
	RunID              string                     `json:"runId"`
	Scope              Scope                      `json:"scope"`
	ProjectID          string                     `json:"projectId"`
	Subject            CanonicalPlanSubject       `json:"subject"`
	BuildManifest      repository.ExactReference  `json:"buildManifest"`
	BuildContract      repository.ExactReference  `json:"buildContract"`
	FullStackTemplate  repository.ExactReference  `json:"fullStackTemplate"`
	Profile            ProfileReference           `json:"verificationProfile"`
	Plan               PlanReference              `json:"plan"`
	AttemptIDs         []string                   `json:"attemptIds"`
	Checks             []CheckResult              `json:"checks"`
	ObligationCoverage []ObligationCoverage       `json:"obligationCoverage"`
	ReleaseArtifacts   []CanonicalReleaseArtifact `json:"releaseArtifacts"`
	MustCount          int                        `json:"mustCount"`
	MustPassedCount    int                        `json:"mustPassedCount"`
	BlockerCount       int                        `json:"blockerCount"`
	WarningCount       int                        `json:"warningCount"`
	Decision           Decision                   `json:"decision"`
	ExecutionError     string                     `json:"executionError,omitempty"`
	PayloadHash        string                     `json:"payloadHash"`
	CreatedBy          string                     `json:"createdBy"`
	CreatedAt          time.Time                  `json:"createdAt"`
}

func NewCanonicalReceipt(input NewCanonicalReceiptInput) (CanonicalReceipt, error) {
	if !validUUIDs(input.ID, input.RunID, input.ProjectID, input.CreatedBy) || input.CreatedAt.IsZero() {
		return CanonicalReceipt{}, invalid("canonical receipt identity, actor, or time")
	}
	subject, err := normalizeCanonicalPlanSubject(input.Subject)
	if err != nil {
		return CanonicalReceipt{}, err
	}
	manifest, err := normalizeExactReference(input.BuildManifest, "build manifest")
	if err != nil {
		return CanonicalReceipt{}, err
	}
	contract, err := normalizeExactReference(input.BuildContract, "build contract")
	if err != nil {
		return CanonicalReceipt{}, err
	}
	fullStack, err := normalizeExactReference(input.FullStackTemplate, "full-stack template")
	if err != nil {
		return CanonicalReceipt{}, err
	}
	profile, err := normalizeProfile(input.Profile)
	if err != nil {
		return CanonicalReceipt{}, err
	}
	plan, err := normalizePlan(input.Plan)
	if err != nil {
		return CanonicalReceipt{}, err
	}
	attemptIDs, err := normalizeUUIDList(input.AttemptIDs, 1, 64, "attempt IDs")
	if err != nil {
		return CanonicalReceipt{}, err
	}
	checks, err := normalizeChecks(input.Checks, stringSet(attemptIDs))
	if err != nil {
		return CanonicalReceipt{}, err
	}
	obligations, err := normalizeObligations(input.Obligations)
	if err != nil {
		return CanonicalReceipt{}, err
	}
	artifacts, err := normalizeCanonicalReleaseArtifacts(input.ReleaseArtifacts)
	if err != nil {
		return CanonicalReceipt{}, err
	}
	executionError := strings.TrimSpace(input.ExecutionError)
	if len(executionError) > 2000 || strings.ContainsRune(executionError, '\x00') {
		return CanonicalReceipt{}, invalid("canonical execution error")
	}
	coverage, mustCount, mustPassed := buildCoverage(obligations, checks)
	blockers, warnings, hasExecutionError := receiptCounts(checks, coverage, executionError)
	if !canonicalReleaseChecksPassed(checks) {
		blockers++
	}
	if len(artifacts) == 0 {
		blockers++
	}
	decision := DecisionPassed
	if hasExecutionError {
		decision = DecisionError
	} else if blockers > 0 {
		decision = DecisionFailed
	}
	receipt := CanonicalReceipt{
		SchemaVersion: CanonicalReceiptSchemaVersion, ID: input.ID, RunID: input.RunID,
		Scope: ScopeCanonical, ProjectID: input.ProjectID, Subject: subject,
		BuildManifest: manifest, BuildContract: contract, FullStackTemplate: fullStack,
		Profile: profile, Plan: plan, AttemptIDs: attemptIDs, Checks: checks,
		ObligationCoverage: coverage, ReleaseArtifacts: artifacts,
		MustCount: mustCount, MustPassedCount: mustPassed, BlockerCount: blockers,
		WarningCount: warnings, Decision: decision, ExecutionError: executionError,
		CreatedBy: input.CreatedBy, CreatedAt: input.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(canonicalReceiptHashPayload(receipt))
	if err != nil {
		return CanonicalReceipt{}, err
	}
	receipt.PayloadHash = "sha256:" + hash
	return receipt, nil
}

func ParseCanonicalReceipt(receipt CanonicalReceipt) (CanonicalReceipt, error) {
	if receipt.SchemaVersion != CanonicalReceiptSchemaVersion || receipt.Scope != ScopeCanonical ||
		!validUUIDs(receipt.ID, receipt.RunID, receipt.ProjectID, receipt.CreatedBy) || receipt.CreatedAt.IsZero() ||
		!exactSHA256(receipt.PayloadHash) {
		return CanonicalReceipt{}, invalid("canonical receipt envelope")
	}
	input := NewCanonicalReceiptInput{
		ID: receipt.ID, RunID: receipt.RunID, ProjectID: receipt.ProjectID, Subject: receipt.Subject,
		BuildManifest: receipt.BuildManifest, BuildContract: receipt.BuildContract,
		FullStackTemplate: receipt.FullStackTemplate, Profile: receipt.Profile, Plan: receipt.Plan,
		AttemptIDs: receipt.AttemptIDs, Checks: receipt.Checks, ReleaseArtifacts: receipt.ReleaseArtifacts,
		ExecutionError: receipt.ExecutionError, CreatedBy: receipt.CreatedBy, CreatedAt: receipt.CreatedAt,
	}
	input.Obligations = make([]ObligationRequirement, len(receipt.ObligationCoverage))
	for index, coverage := range receipt.ObligationCoverage {
		input.Obligations[index] = ObligationRequirement{
			ID: coverage.ObligationID, Level: coverage.Level, OracleIDs: coverage.OracleIDs,
		}
	}
	expected, err := NewCanonicalReceipt(input)
	if err != nil || expected.PayloadHash != receipt.PayloadHash || expected.Decision != receipt.Decision ||
		expected.MustCount != receipt.MustCount || expected.MustPassedCount != receipt.MustPassedCount ||
		expected.BlockerCount != receipt.BlockerCount || expected.WarningCount != receipt.WarningCount ||
		!equalCoverage(expected.ObligationCoverage, receipt.ObligationCoverage) ||
		!equalStrings(expected.AttemptIDs, receipt.AttemptIDs) || !equalChecks(expected.Checks, receipt.Checks) ||
		!equalCanonicalReleaseArtifacts(expected.ReleaseArtifacts, receipt.ReleaseArtifacts) {
		return CanonicalReceipt{}, invalid("canonical receipt content or derived projection")
	}
	return expected, nil
}

func (receipt CanonicalReceipt) PassedReference() (repository.ExactReference, error) {
	parsed, err := ParseCanonicalReceipt(receipt)
	if err != nil || parsed.Decision != DecisionPassed || parsed.BlockerCount != 0 ||
		parsed.MustPassedCount != parsed.MustCount || len(parsed.ReleaseArtifacts) == 0 {
		return repository.ExactReference{}, invalid("canonical receipt is not a passing exact result")
	}
	return repository.ExactReference{ID: parsed.ID, ContentHash: parsed.PayloadHash}, nil
}

func normalizeCanonicalReleaseArtifacts(values []CanonicalReleaseArtifact) ([]CanonicalReleaseArtifact, error) {
	if len(values) > 128 {
		return nil, invalid("canonical release artifacts")
	}
	result := append([]CanonicalReleaseArtifact(nil), values...)
	seen := map[string]bool{}
	for index := range result {
		value := result[index]
		value.ID, value.Kind = strings.TrimSpace(value.ID), strings.TrimSpace(value.Kind)
		value.Store, value.Ref = strings.TrimSpace(value.Store), strings.TrimSpace(value.Ref)
		value.MediaType = strings.TrimSpace(value.MediaType)
		if !stableIDPattern.MatchString(value.ID) || !stableIDPattern.MatchString(value.Kind) || seen[value.ID] ||
			value.Store == "" || len(value.Store) > 80 || value.Ref == "" || len(value.Ref) > 2000 ||
			!exactSHA256(value.ContentHash) || value.MediaType == "" || len(value.MediaType) > 256 ||
			value.ByteSize < 0 || value.ByteSize > 10<<30 {
			return nil, invalid("canonical release artifact")
		}
		seen[value.ID] = true
		result[index] = value
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func canonicalReleaseChecksPassed(checks []CheckResult) bool {
	required := map[string]string{
		"release-artifacts":        "release-manifest",
		"release-sbom":             "sbom",
		"release-vulnerability":    "vulnerability",
		"release-container-policy": "container-policy",
	}
	passed := map[string]bool{}
	for _, check := range checks {
		if kind, exists := required[check.ID]; exists && check.Kind == kind && check.Required &&
			check.Status == CheckPassed && !check.Truncated {
			passed[check.ID] = true
		}
	}
	for id := range required {
		if !passed[id] {
			return false
		}
	}
	return true
}

func canonicalReceiptHashPayload(receipt CanonicalReceipt) any {
	copy := receipt
	copy.PayloadHash = ""
	return copy
}

func equalCanonicalReleaseArtifacts(left, right []CanonicalReleaseArtifact) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
