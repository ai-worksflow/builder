package verification

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const ReceiptSchemaVersion = "verification-receipt/v1"

var (
	ErrInvalidReceipt = errors.New("invalid verification receipt")

	stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$`)
	imagePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,255}@sha256:[0-9a-f]{64}$`)
)

type Scope string

const (
	ScopeCandidate Scope = "candidate"
	ScopeCanonical Scope = "canonical"
)

type Decision string

const (
	DecisionPassed Decision = "passed"
	DecisionFailed Decision = "failed"
	DecisionError  Decision = "error"
)

type CheckStatus string

const (
	CheckPassed CheckStatus = "passed"
	CheckFailed CheckStatus = "failed"
	CheckError  CheckStatus = "error"
)

type DiagnosticSeverity string

const (
	SeverityBlocker DiagnosticSeverity = "blocker"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityInfo    DiagnosticSeverity = "info"
)

type CandidateSubject struct {
	SessionID           string `json:"sessionId"`
	CandidateID         string `json:"candidateId"`
	CandidateSnapshotID string `json:"candidateSnapshotId"`
	CandidateVersion    uint64 `json:"candidateVersion"`
	JournalSequence     uint64 `json:"journalSequence"`
	SessionEpoch        uint64 `json:"sessionEpoch"`
	WriterLeaseEpoch    uint64 `json:"writerLeaseEpoch"`
	TreeHash            string `json:"treeHash"`
}

type ProfileReference struct {
	ID          string `json:"id"`
	Version     uint64 `json:"version"`
	ContentHash string `json:"contentHash"`
}

type PlanReference struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type BlobReference struct {
	Store       string `json:"store"`
	OwnerID     string `json:"ownerId"`
	Ref         string `json:"ref"`
	ContentHash string `json:"contentHash"`
	ByteSize    int64  `json:"byteSize"`
}

type Diagnostic struct {
	ID         string             `json:"id"`
	Code       string             `json:"code"`
	Severity   DiagnosticSeverity `json:"severity"`
	Message    string             `json:"message"`
	Path       string             `json:"path,omitempty"`
	Line       int                `json:"line,omitempty"`
	Column     int                `json:"column,omitempty"`
	Suggestion string             `json:"suggestion,omitempty"`
}

type CheckResult struct {
	ID                     string         `json:"id"`
	Kind                   string         `json:"kind"`
	ServiceID              string         `json:"serviceId,omitempty"`
	CommandID              string         `json:"commandId,omitempty"`
	Required               bool           `json:"required"`
	Status                 CheckStatus    `json:"status"`
	AttemptID              string         `json:"attemptId"`
	VerifierImageDigest    string         `json:"verifierImageDigest"`
	Argv                   []string       `json:"argv"`
	WorkingDirectory       string         `json:"workingDirectory"`
	ExitCode               *int           `json:"exitCode,omitempty"`
	StartedAt              time.Time      `json:"startedAt"`
	CompletedAt            time.Time      `json:"completedAt"`
	DurationMS             int64          `json:"durationMs"`
	AttemptCount           uint64         `json:"attemptCount"`
	Stdout                 *BlobReference `json:"stdout,omitempty"`
	Stderr                 *BlobReference `json:"stderr,omitempty"`
	Truncated              bool           `json:"truncated"`
	RedactionCount         int            `json:"redactionCount"`
	OracleIDs              []string       `json:"oracleIds"`
	AcceptanceCriterionIDs []string       `json:"acceptanceCriterionIds"`
	ObligationIDs          []string       `json:"obligationIds"`
	Diagnostics            []Diagnostic   `json:"diagnostics"`
}

type ObligationRequirement struct {
	ID        string   `json:"id"`
	Level     string   `json:"level"`
	OracleIDs []string `json:"oracleIds"`
}

type ObligationCoverage struct {
	ObligationID string   `json:"obligationId"`
	Level        string   `json:"level"`
	OracleIDs    []string `json:"oracleIds"`
	CheckIDs     []string `json:"checkIds"`
	Status       string   `json:"status"`
}

type NewCandidateReceiptInput struct {
	ID                string
	RunID             string
	ProjectID         string
	Subject           CandidateSubject
	BuildManifest     repository.ExactReference
	BuildContract     repository.ExactReference
	FullStackTemplate repository.ExactReference
	Profile           ProfileReference
	Plan              PlanReference
	AttemptIDs        []string
	Checks            []CheckResult
	Obligations       []ObligationRequirement
	ExecutionError    string
	CreatedBy         string
	CreatedAt         time.Time
}

type Receipt struct {
	SchemaVersion      string                    `json:"schemaVersion"`
	ID                 string                    `json:"id"`
	RunID              string                    `json:"runId"`
	Scope              Scope                     `json:"scope"`
	ProjectID          string                    `json:"projectId"`
	Subject            CandidateSubject          `json:"subject"`
	BuildManifest      repository.ExactReference `json:"buildManifest"`
	BuildContract      repository.ExactReference `json:"buildContract"`
	FullStackTemplate  repository.ExactReference `json:"fullStackTemplate"`
	Profile            ProfileReference          `json:"verificationProfile"`
	Plan               PlanReference             `json:"plan"`
	AttemptIDs         []string                  `json:"attemptIds"`
	Checks             []CheckResult             `json:"checks"`
	ObligationCoverage []ObligationCoverage      `json:"obligationCoverage"`
	MustCount          int                       `json:"mustCount"`
	MustPassedCount    int                       `json:"mustPassedCount"`
	BlockerCount       int                       `json:"blockerCount"`
	WarningCount       int                       `json:"warningCount"`
	Decision           Decision                  `json:"decision"`
	ExecutionError     string                    `json:"executionError,omitempty"`
	PayloadHash        string                    `json:"payloadHash"`
	CreatedBy          string                    `json:"createdBy"`
	CreatedAt          time.Time                 `json:"createdAt"`
}

func NewCandidateReceipt(input NewCandidateReceiptInput) (Receipt, error) {
	if !validUUIDs(input.ID, input.RunID, input.ProjectID, input.CreatedBy) || input.CreatedAt.IsZero() {
		return Receipt{}, invalid("receipt identity, actor, or time")
	}
	subject, err := normalizeCandidateSubject(input.Subject)
	if err != nil {
		return Receipt{}, err
	}
	buildManifest, err := normalizeExactReference(input.BuildManifest, "build manifest")
	if err != nil {
		return Receipt{}, err
	}
	buildContract, err := normalizeExactReference(input.BuildContract, "build contract")
	if err != nil {
		return Receipt{}, err
	}
	fullStackTemplate, err := normalizeExactReference(input.FullStackTemplate, "full-stack template")
	if err != nil {
		return Receipt{}, err
	}
	profile, err := normalizeProfile(input.Profile)
	if err != nil {
		return Receipt{}, err
	}
	plan, err := normalizePlan(input.Plan)
	if err != nil {
		return Receipt{}, err
	}
	attemptIDs, err := normalizeUUIDList(input.AttemptIDs, 1, 64, "attempt IDs")
	if err != nil {
		return Receipt{}, err
	}
	attemptSet := stringSet(attemptIDs)
	checks, err := normalizeChecks(input.Checks, attemptSet)
	if err != nil {
		return Receipt{}, err
	}
	obligations, err := normalizeObligations(input.Obligations)
	if err != nil {
		return Receipt{}, err
	}
	executionError := strings.TrimSpace(input.ExecutionError)
	if len(executionError) > 2000 || strings.ContainsRune(executionError, '\x00') {
		return Receipt{}, invalid("execution error")
	}

	coverage, mustCount, mustPassed := buildCoverage(obligations, checks)
	blockers, warnings, hasExecutionError := receiptCounts(checks, coverage, executionError)
	decision := DecisionPassed
	if hasExecutionError {
		decision = DecisionError
	} else if blockers > 0 {
		decision = DecisionFailed
	}
	receipt := Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: input.ID, RunID: input.RunID,
		Scope: ScopeCandidate, ProjectID: input.ProjectID, Subject: subject,
		BuildManifest: buildManifest, BuildContract: buildContract, FullStackTemplate: fullStackTemplate,
		Profile: profile, Plan: plan, AttemptIDs: attemptIDs, Checks: checks,
		ObligationCoverage: coverage, MustCount: mustCount, MustPassedCount: mustPassed,
		BlockerCount: blockers, WarningCount: warnings, Decision: decision,
		ExecutionError: executionError, CreatedBy: input.CreatedBy,
		CreatedAt: input.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(receiptHashPayload(receipt))
	if err != nil {
		return Receipt{}, err
	}
	receipt.PayloadHash = "sha256:" + hash
	return receipt, nil
}

func ParseReceipt(receipt Receipt) (Receipt, error) {
	if receipt.SchemaVersion != ReceiptSchemaVersion || receipt.Scope != ScopeCandidate ||
		!validUUIDs(receipt.ID, receipt.RunID, receipt.ProjectID, receipt.CreatedBy) || receipt.CreatedAt.IsZero() ||
		!exactSHA256(receipt.PayloadHash) {
		return Receipt{}, invalid("receipt envelope")
	}
	input := NewCandidateReceiptInput{
		ID: receipt.ID, RunID: receipt.RunID, ProjectID: receipt.ProjectID, Subject: receipt.Subject,
		BuildManifest: receipt.BuildManifest, BuildContract: receipt.BuildContract,
		FullStackTemplate: receipt.FullStackTemplate, Profile: receipt.Profile, Plan: receipt.Plan,
		AttemptIDs: receipt.AttemptIDs, Checks: receipt.Checks, ExecutionError: receipt.ExecutionError,
		CreatedBy: receipt.CreatedBy, CreatedAt: receipt.CreatedAt,
	}
	input.Obligations = make([]ObligationRequirement, len(receipt.ObligationCoverage))
	for index, item := range receipt.ObligationCoverage {
		input.Obligations[index] = ObligationRequirement{ID: item.ObligationID, Level: item.Level, OracleIDs: item.OracleIDs}
	}
	expected, err := NewCandidateReceipt(input)
	if err != nil || expected.PayloadHash != receipt.PayloadHash || expected.Decision != receipt.Decision ||
		expected.MustCount != receipt.MustCount || expected.MustPassedCount != receipt.MustPassedCount ||
		expected.BlockerCount != receipt.BlockerCount || expected.WarningCount != receipt.WarningCount ||
		!equalCoverage(expected.ObligationCoverage, receipt.ObligationCoverage) ||
		!equalStrings(expected.AttemptIDs, receipt.AttemptIDs) || !equalChecks(expected.Checks, receipt.Checks) {
		return Receipt{}, invalid("receipt content or derived projection")
	}
	return expected, nil
}

func (receipt Receipt) PassedReference() (repository.ExactReference, error) {
	parsed, err := ParseReceipt(receipt)
	if err != nil || parsed.Decision != DecisionPassed || parsed.BlockerCount != 0 ||
		parsed.MustPassedCount != parsed.MustCount {
		return repository.ExactReference{}, invalid("receipt is not a passing exact result")
	}
	return repository.ExactReference{ID: parsed.ID, ContentHash: parsed.PayloadHash}, nil
}

func normalizeCandidateSubject(value CandidateSubject) (CandidateSubject, error) {
	if !validUUIDs(value.SessionID, value.CandidateID, value.CandidateSnapshotID) ||
		value.CandidateVersion == 0 || value.SessionEpoch == 0 || value.WriterLeaseEpoch == 0 ||
		!exactSHA256(value.TreeHash) {
		return CandidateSubject{}, invalid("Candidate subject")
	}
	return value, nil
}

func normalizeExactReference(value repository.ExactReference, field string) (repository.ExactReference, error) {
	if !validUUIDs(value.ID) || !exactSHA256(value.ContentHash) {
		return repository.ExactReference{}, invalid(field)
	}
	return value, nil
}

func normalizeProfile(value ProfileReference) (ProfileReference, error) {
	value.ID = strings.TrimSpace(value.ID)
	if !stableIDPattern.MatchString(value.ID) || value.Version == 0 || !exactSHA256(value.ContentHash) {
		return ProfileReference{}, invalid("verification profile")
	}
	return value, nil
}

func normalizePlan(value PlanReference) (PlanReference, error) {
	if !validUUIDs(value.ID) || !exactSHA256(value.ContentHash) {
		return PlanReference{}, invalid("verification plan")
	}
	return value, nil
}

func normalizeChecks(values []CheckResult, attemptIDs map[string]bool) ([]CheckResult, error) {
	if len(values) == 0 || len(values) > 512 {
		return nil, invalid("verification checks")
	}
	result := append([]CheckResult(nil), values...)
	seen := map[string]bool{}
	for index := range result {
		check := result[index]
		check.ID = strings.TrimSpace(check.ID)
		check.Kind = strings.TrimSpace(check.Kind)
		check.ServiceID = strings.TrimSpace(check.ServiceID)
		check.CommandID = strings.TrimSpace(check.CommandID)
		check.WorkingDirectory = strings.TrimSpace(check.WorkingDirectory)
		if !stableIDPattern.MatchString(check.ID) || !stableIDPattern.MatchString(check.Kind) || seen[check.ID] ||
			!attemptIDs[check.AttemptID] || !imagePattern.MatchString(check.VerifierImageDigest) ||
			(check.Status != CheckPassed && check.Status != CheckFailed && check.Status != CheckError) ||
			check.AttemptCount == 0 || check.RedactionCount < 0 || check.DurationMS < 0 ||
			check.StartedAt.IsZero() || check.CompletedAt.IsZero() || check.CompletedAt.Before(check.StartedAt) ||
			check.CompletedAt.Sub(check.StartedAt).Milliseconds() != check.DurationMS ||
			(check.WorkingDirectory != "." && !validRelativePath(check.WorkingDirectory)) {
			return nil, invalid(fmt.Sprintf("verification check %d", index))
		}
		if check.ServiceID != "" && !stableIDPattern.MatchString(check.ServiceID) ||
			check.CommandID != "" && !stableIDPattern.MatchString(check.CommandID) {
			return nil, invalid(fmt.Sprintf("verification check %d target", index))
		}
		if len(check.Argv) == 0 || len(check.Argv) > 64 {
			return nil, invalid(fmt.Sprintf("verification check %d argv", index))
		}
		for _, argument := range check.Argv {
			if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
				return nil, invalid(fmt.Sprintf("verification check %d argv", index))
			}
		}
		if check.Status == CheckPassed && (check.ExitCode == nil || *check.ExitCode != 0) ||
			check.Status == CheckFailed && check.ExitCode == nil ||
			check.Status == CheckError && check.ExitCode != nil {
			return nil, invalid(fmt.Sprintf("verification check %d exit code", index))
		}
		if check.Status == CheckPassed && check.Truncated {
			return nil, invalid(fmt.Sprintf("verification check %d passed with truncated evidence", index))
		}
		if err := validateBlob(check.Stdout, check.AttemptID); err != nil {
			return nil, err
		}
		if err := validateBlob(check.Stderr, check.AttemptID); err != nil {
			return nil, err
		}
		var err error
		check.OracleIDs, err = normalizeStableList(check.OracleIDs, 0, 256, "check oracle IDs")
		if err != nil {
			return nil, err
		}
		check.AcceptanceCriterionIDs, err = normalizeStableList(check.AcceptanceCriterionIDs, 0, 256, "check acceptance criterion IDs")
		if err != nil {
			return nil, err
		}
		check.ObligationIDs, err = normalizeStableList(check.ObligationIDs, 0, 256, "check obligation IDs")
		if err != nil {
			return nil, err
		}
		check.Diagnostics, err = normalizeDiagnostics(check.Diagnostics)
		if err != nil {
			return nil, err
		}
		check.Argv = append([]string(nil), check.Argv...)
		check.StartedAt = check.StartedAt.UTC().Truncate(time.Microsecond)
		check.CompletedAt = check.CompletedAt.UTC().Truncate(time.Microsecond)
		seen[check.ID] = true
		result[index] = check
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func normalizeDiagnostics(values []Diagnostic) ([]Diagnostic, error) {
	if len(values) > 4096 {
		return nil, invalid("verification diagnostics")
	}
	result := make([]Diagnostic, len(values))
	copy(result, values)
	seen := map[string]bool{}
	for index := range result {
		item := result[index]
		item.ID = strings.TrimSpace(item.ID)
		item.Code = strings.TrimSpace(item.Code)
		item.Message = strings.TrimSpace(item.Message)
		item.Path = strings.TrimSpace(item.Path)
		item.Suggestion = strings.TrimSpace(item.Suggestion)
		if !stableIDPattern.MatchString(item.ID) || !stableIDPattern.MatchString(item.Code) || seen[item.ID] ||
			(item.Severity != SeverityBlocker && item.Severity != SeverityWarning && item.Severity != SeverityInfo) ||
			item.Message == "" || len(item.Message) > 4000 || strings.ContainsRune(item.Message, '\x00') ||
			item.Line < 0 || item.Column < 0 || len(item.Suggestion) > 4000 {
			return nil, invalid(fmt.Sprintf("verification diagnostic %d", index))
		}
		if item.Path != "" && !validRelativePath(item.Path) {
			return nil, invalid(fmt.Sprintf("verification diagnostic %d path", index))
		}
		seen[item.ID] = true
		result[index] = item
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func normalizeObligations(values []ObligationRequirement) ([]ObligationRequirement, error) {
	if len(values) == 0 || len(values) > 10_000 {
		return nil, invalid("verification obligations")
	}
	result := append([]ObligationRequirement(nil), values...)
	seen := map[string]bool{}
	for index := range result {
		item := result[index]
		item.ID = strings.TrimSpace(item.ID)
		if !stableIDPattern.MatchString(item.ID) || seen[item.ID] || (item.Level != "must" && item.Level != "should") {
			return nil, invalid(fmt.Sprintf("verification obligation %d", index))
		}
		var err error
		item.OracleIDs, err = normalizeStableList(item.OracleIDs, 1, 256, "obligation oracle IDs")
		if err != nil {
			return nil, err
		}
		seen[item.ID] = true
		result[index] = item
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func buildCoverage(obligations []ObligationRequirement, checks []CheckResult) ([]ObligationCoverage, int, int) {
	result := make([]ObligationCoverage, 0, len(obligations))
	mustCount, mustPassed := 0, 0
	for _, obligation := range obligations {
		checkIDs := []string{}
		oracleSet := stringSet(obligation.OracleIDs)
		for _, check := range checks {
			if check.Status != CheckPassed || !contains(check.ObligationIDs, obligation.ID) || !intersects(check.OracleIDs, oracleSet) {
				continue
			}
			checkIDs = append(checkIDs, check.ID)
		}
		status := "missing"
		if len(checkIDs) > 0 {
			status = "passed"
		}
		if obligation.Level == "must" {
			mustCount++
			if status == "passed" {
				mustPassed++
			}
		}
		result = append(result, ObligationCoverage{
			ObligationID: obligation.ID, Level: obligation.Level,
			OracleIDs: append([]string(nil), obligation.OracleIDs...), CheckIDs: checkIDs, Status: status,
		})
	}
	return result, mustCount, mustPassed
}

func receiptCounts(checks []CheckResult, coverage []ObligationCoverage, executionError string) (int, int, bool) {
	blockers, warnings := 0, 0
	hasError := executionError != ""
	for _, check := range checks {
		if check.Required && check.Status != CheckPassed {
			blockers++
		}
		if check.Status == CheckError {
			hasError = true
		}
		if !check.Required && check.Status != CheckPassed {
			warnings++
		}
		for _, diagnostic := range check.Diagnostics {
			switch diagnostic.Severity {
			case SeverityBlocker:
				blockers++
			case SeverityWarning:
				warnings++
			}
		}
	}
	for _, item := range coverage {
		if item.Level == "must" && item.Status != "passed" {
			blockers++
		}
	}
	if hasError && blockers == 0 {
		blockers = 1
	}
	return blockers, warnings, hasError
}

func validateBlob(value *BlobReference, ownerID string) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(value.Store) == "" || strings.TrimSpace(value.Ref) == "" ||
		value.OwnerID != ownerID || !exactSHA256(value.ContentHash) || value.ByteSize < 0 {
		return invalid("verification log reference")
	}
	return nil
}

func normalizeUUIDList(values []string, minimum, maximum int, field string) ([]string, error) {
	if len(values) < minimum || len(values) > maximum {
		return nil, invalid(field)
	}
	result := make([]string, len(values))
	copy(result, values)
	for _, value := range result {
		if !validUUIDs(value) {
			return nil, invalid(field)
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, invalid(field)
		}
	}
	return result, nil
}

func normalizeStableList(values []string, minimum, maximum int, field string) ([]string, error) {
	if len(values) < minimum || len(values) > maximum {
		return nil, invalid(field)
	}
	result := make([]string, len(values))
	copy(result, values)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
		if !stableIDPattern.MatchString(result[index]) {
			return nil, invalid(field)
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, invalid(field)
		}
	}
	return result, nil
}

func receiptHashPayload(receipt Receipt) any {
	copy := receipt
	copy.PayloadHash = ""
	return copy
}

func validRelativePath(value string) bool {
	_, err := repository.NormalizePath(value)
	return err == nil
}

func exactSHA256(value string) bool {
	return strings.HasPrefix(value, "sha256:") && value == strings.ToLower(value) && domain.IsCanonicalHash(value)
}

func validUUIDs(values ...string) bool {
	for _, value := range values {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return false
		}
	}
	return true
}

func invalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidReceipt, detail)
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func contains(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}

func intersects(values []string, targets map[string]bool) bool {
	for _, value := range values {
		if targets[value] {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
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

func equalCoverage(left, right []ObligationCoverage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ObligationID != right[index].ObligationID || left[index].Level != right[index].Level ||
			left[index].Status != right[index].Status || !equalStrings(left[index].OracleIDs, right[index].OracleIDs) ||
			!equalStrings(left[index].CheckIDs, right[index].CheckIDs) {
			return false
		}
	}
	return true
}

func equalChecks(left, right []CheckResult) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftHash, leftErr := domain.CanonicalHash(left[index])
		rightHash, rightErr := domain.CanonicalHash(right[index])
		if leftErr != nil || rightErr != nil || leftHash != rightHash {
			return false
		}
	}
	return true
}
