package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	ContextPackSchemaVersion = "agent-context-pack/v1"
	TaskCapsuleSchemaVersion = "agent-task-capsule/v1"
	AttemptSchemaVersion     = "agent-attempt/v1"
	AttemptEventSchema       = "agent-attempt-event/v1"
)

var (
	ErrInvalidContextPack = errors.New("invalid agent context pack")
	ErrInvalidTaskCapsule = errors.New("invalid agent task capsule")
	ErrInvalidAttempt     = errors.New("invalid agent attempt")
	ErrAttemptState       = errors.New("invalid agent attempt state transition")
	ErrAttemptFenced      = errors.New("agent attempt worker is fenced")
	ErrAttemptLease       = errors.New("agent attempt worker lease is unavailable")

	sha256Pattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	exactHashPattern = regexp.MustCompile(`^(sha256:)?[0-9a-f]{64}$`)
	stableIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:~/@-]*$`)
	hostPattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
)

type BlobReference struct {
	Store       string `json:"store"`
	OwnerID     string `json:"ownerId"`
	Ref         string `json:"ref"`
	ContentHash string `json:"contentHash"`
	ByteSize    int64  `json:"byteSize"`
}

type ContextItemKind string

const (
	ContextBuildContract   ContextItemKind = "build_contract"
	ContextSourceRevision  ContextItemKind = "source_revision"
	ContextRepositoryFile  ContextItemKind = "repository_file"
	ContextTemplateRule    ContextItemKind = "template_rule"
	ContextContractSection ContextItemKind = "contract_section"
	ContextPrototype       ContextItemKind = "prototype"
	ContextAcceptance      ContextItemKind = "acceptance"
	ContextDiagnostics     ContextItemKind = "diagnostics"
)

type ContextItem struct {
	Key      string                     `json:"key"`
	Kind     ContextItemKind            `json:"kind"`
	Source   *repository.ExactReference `json:"source,omitempty"`
	Path     string                     `json:"path,omitempty"`
	Content  BlobReference              `json:"content"`
	Required bool                       `json:"required"`
}

type NewContextPackInput struct {
	ID                    string
	ProjectID             string
	CandidateID           string
	BaseCandidateTreeHash string
	BuildContract         repository.ExactReference
	Items                 []ContextItem
	CreatedBy             string
}

type ContextPack struct {
	SchemaVersion         string                    `json:"schemaVersion"`
	ID                    string                    `json:"id"`
	ProjectID             string                    `json:"projectId"`
	CandidateID           string                    `json:"candidateId"`
	BaseCandidateTreeHash string                    `json:"baseCandidateTreeHash"`
	BuildContract         repository.ExactReference `json:"buildContract"`
	Items                 []ContextItem             `json:"items"`
	ContentHash           string                    `json:"contentHash"`
	CreatedBy             string                    `json:"createdBy"`
	CreatedAt             time.Time                 `json:"createdAt"`
}

type ContextPackReference struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

func (pack ContextPack) ExactReference() ContextPackReference {
	return ContextPackReference{ID: pack.ID, ContentHash: pack.ContentHash}
}

type NetworkPolicy struct {
	Mode         string   `json:"mode"`
	AllowedHosts []string `json:"allowedHosts"`
}

type TaskBudgets struct {
	WallTimeSeconds int64 `json:"wallTimeSeconds"`
	MaxInputTokens  int64 `json:"maxInputTokens"`
	MaxOutputTokens int64 `json:"maxOutputTokens"`
	MaxCommands     int64 `json:"maxCommands"`
	MaxLogBytes     int64 `json:"maxLogBytes"`
	MaxPatchBytes   int64 `json:"maxPatchBytes"`
}

type NewTaskCapsuleInput struct {
	ID                        string
	TaskKey                   string
	ProjectID                 string
	SandboxSessionID          string
	CandidateID               string
	CandidateVersion          uint64
	CandidateSessionEpoch     uint64
	CandidateWriterLeaseEpoch uint64
	BaseCandidateTreeHash     string
	BuildContract             repository.ExactReference
	TemplateReleases          []repository.ExactReference
	Objective                 string
	ObligationIDs             []string
	AcceptanceCriterionIDs    []string
	ReadSet                   []string
	WriteSet                  []string
	ProtectedPaths            []string
	Preconditions             []string
	Postconditions            []string
	VerificationCommandIDs    []string
	AllowedTools              []string
	NetworkPolicy             NetworkPolicy
	Budgets                   TaskBudgets
	OutputSchemaHash          string
	CreatedBy                 string
}

type TaskCapsule struct {
	SchemaVersion             string                      `json:"schemaVersion"`
	ID                        string                      `json:"taskId"`
	TaskKey                   string                      `json:"taskKey"`
	ProjectID                 string                      `json:"projectId"`
	SandboxSessionID          string                      `json:"sandboxSessionId"`
	CandidateID               string                      `json:"candidateId"`
	CandidateVersion          uint64                      `json:"candidateVersion"`
	CandidateSessionEpoch     uint64                      `json:"candidateSessionEpoch"`
	CandidateWriterLeaseEpoch uint64                      `json:"candidateWriterLeaseEpoch"`
	BaseCandidateTreeHash     string                      `json:"baseCandidateTreeHash"`
	BuildContract             repository.ExactReference   `json:"buildContract"`
	TemplateReleases          []repository.ExactReference `json:"templateReleases"`
	ContextPack               ContextPackReference        `json:"contextPack"`
	Objective                 string                      `json:"objective"`
	ObligationIDs             []string                    `json:"obligationIds"`
	AcceptanceCriterionIDs    []string                    `json:"acceptanceCriterionIds"`
	ReadSet                   []string                    `json:"readSet"`
	WriteSet                  []string                    `json:"writeSet"`
	ProtectedPaths            []string                    `json:"protectedPaths"`
	Preconditions             []string                    `json:"preconditions"`
	Postconditions            []string                    `json:"postconditions"`
	VerificationCommandIDs    []string                    `json:"verificationCommandIds"`
	AllowedTools              []string                    `json:"allowedTools"`
	NetworkPolicy             NetworkPolicy               `json:"networkPolicy"`
	Budgets                   TaskBudgets                 `json:"budgets"`
	OutputSchemaHash          string                      `json:"outputSchemaHash"`
	ContentHash               string                      `json:"contentHash"`
	CreatedBy                 string                      `json:"createdBy"`
	CreatedAt                 time.Time                   `json:"createdAt"`
}

func (capsule TaskCapsule) ExactReference() repository.ExactReference {
	return repository.ExactReference{ID: capsule.ID, ContentHash: capsule.ContentHash}
}

func NewContextPack(input NewContextPackInput, now time.Time) (ContextPack, error) {
	if !validUUIDs(input.ID, input.ProjectID, input.CandidateID, input.CreatedBy) || now.IsZero() ||
		!sha256Pattern.MatchString(input.BaseCandidateTreeHash) {
		return ContextPack{}, fmt.Errorf("%w: identity, actor, time, or base tree", ErrInvalidContextPack)
	}
	buildContract, err := normalizeExactReference(input.BuildContract)
	if err != nil {
		return ContextPack{}, fmt.Errorf("%w: BuildContract: %v", ErrInvalidContextPack, err)
	}
	items, err := normalizeContextItems(input.Items)
	if err != nil {
		return ContextPack{}, err
	}
	pack := ContextPack{
		SchemaVersion: ContextPackSchemaVersion,
		ID:            input.ID, ProjectID: input.ProjectID, CandidateID: input.CandidateID,
		BaseCandidateTreeHash: input.BaseCandidateTreeHash, BuildContract: buildContract,
		Items: items, CreatedBy: input.CreatedBy, CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
	hash, err := semanticHash(contextPackPayload(pack))
	if err != nil {
		return ContextPack{}, err
	}
	pack.ContentHash = hash
	return pack, nil
}

func ParseContextPack(pack ContextPack) (ContextPack, error) {
	parsed, err := NewContextPack(NewContextPackInput{
		ID: pack.ID, ProjectID: pack.ProjectID, CandidateID: pack.CandidateID,
		BaseCandidateTreeHash: pack.BaseCandidateTreeHash, BuildContract: pack.BuildContract,
		Items: pack.Items, CreatedBy: pack.CreatedBy,
	}, pack.CreatedAt)
	if err != nil || pack.SchemaVersion != ContextPackSchemaVersion || pack.ContentHash != parsed.ContentHash ||
		!pack.CreatedAt.Equal(parsed.CreatedAt) || !equalJSON(pack.Items, parsed.Items) {
		if err != nil {
			return ContextPack{}, err
		}
		return ContextPack{}, fmt.Errorf("%w: schema, hash, timestamp, or canonical item order", ErrInvalidContextPack)
	}
	return parsed, nil
}

func NewTaskCapsule(input NewTaskCapsuleInput, contextPack ContextPack, now time.Time) (TaskCapsule, error) {
	contextPack, err := ParseContextPack(contextPack)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: ContextPack: %v", ErrInvalidTaskCapsule, err)
	}
	if !validUUIDs(input.ID, input.ProjectID, input.SandboxSessionID, input.CandidateID, input.CreatedBy) || now.IsZero() ||
		input.ProjectID != contextPack.ProjectID || input.CandidateID != contextPack.CandidateID ||
		input.BaseCandidateTreeHash != contextPack.BaseCandidateTreeHash || input.CandidateVersion == 0 ||
		input.CandidateSessionEpoch == 0 || !sha256Pattern.MatchString(input.BaseCandidateTreeHash) {
		return TaskCapsule{}, fmt.Errorf("%w: identity or exact Candidate/ContextPack fence", ErrInvalidTaskCapsule)
	}
	taskKey, err := normalizeStableValue(input.TaskKey, 160)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: task key", ErrInvalidTaskCapsule)
	}
	objective := strings.TrimSpace(input.Objective)
	if objective == "" || len(objective) > 4000 {
		return TaskCapsule{}, fmt.Errorf("%w: objective is required and bounded", ErrInvalidTaskCapsule)
	}
	buildContract, err := normalizeExactReference(input.BuildContract)
	if err != nil || buildContract != contextPack.BuildContract {
		return TaskCapsule{}, fmt.Errorf("%w: exact BuildContract does not match ContextPack", ErrInvalidTaskCapsule)
	}
	templateReleases, err := normalizeExactReferences(input.TemplateReleases, 16)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: TemplateRelease refs: %v", ErrInvalidTaskCapsule, err)
	}
	obligations, err := normalizeStableList(input.ObligationIDs, 1, 512, 160)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: obligation IDs", ErrInvalidTaskCapsule)
	}
	criteria, err := normalizeStableList(input.AcceptanceCriterionIDs, 1, 512, 160)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: acceptance criterion IDs", ErrInvalidTaskCapsule)
	}
	readSet, err := normalizePaths(input.ReadSet, 1, 2048)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: read set: %v", ErrInvalidTaskCapsule, err)
	}
	writeSet, err := normalizePaths(input.WriteSet, 1, 1024)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: write set: %v", ErrInvalidTaskCapsule, err)
	}
	protectedPaths, err := normalizePaths(input.ProtectedPaths, 1, 1024)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: protected paths: %v", ErrInvalidTaskCapsule, err)
	}
	for _, writable := range writeSet {
		for _, protected := range protectedPaths {
			if pathContains(protected, writable) || pathContains(writable, protected) {
				return TaskCapsule{}, fmt.Errorf("%w: write path %q overlaps protected path %q", ErrInvalidTaskCapsule, writable, protected)
			}
		}
	}
	preconditions, err := normalizeStatements(input.Preconditions, 1, 256)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: preconditions", ErrInvalidTaskCapsule)
	}
	postconditions, err := normalizeStatements(input.Postconditions, 1, 256)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: postconditions", ErrInvalidTaskCapsule)
	}
	commands, err := normalizeStableList(input.VerificationCommandIDs, 1, 128, 160)
	if err != nil {
		return TaskCapsule{}, fmt.Errorf("%w: verification command IDs", ErrInvalidTaskCapsule)
	}
	tools, err := normalizeTools(input.AllowedTools)
	if err != nil {
		return TaskCapsule{}, err
	}
	network, err := normalizeNetworkPolicy(input.NetworkPolicy)
	if err != nil {
		return TaskCapsule{}, err
	}
	if err := input.Budgets.validate(); err != nil {
		return TaskCapsule{}, err
	}
	if !sha256Pattern.MatchString(input.OutputSchemaHash) {
		return TaskCapsule{}, fmt.Errorf("%w: output schema hash", ErrInvalidTaskCapsule)
	}
	capsule := TaskCapsule{
		SchemaVersion: TaskCapsuleSchemaVersion, ID: input.ID, TaskKey: taskKey,
		ProjectID: input.ProjectID, SandboxSessionID: input.SandboxSessionID,
		CandidateID: input.CandidateID, CandidateVersion: input.CandidateVersion,
		CandidateSessionEpoch:     input.CandidateSessionEpoch,
		CandidateWriterLeaseEpoch: input.CandidateWriterLeaseEpoch,
		BaseCandidateTreeHash:     input.BaseCandidateTreeHash, BuildContract: buildContract,
		TemplateReleases: templateReleases, ContextPack: contextPack.ExactReference(), Objective: objective,
		ObligationIDs: obligations, AcceptanceCriterionIDs: criteria,
		ReadSet: readSet, WriteSet: writeSet, ProtectedPaths: protectedPaths,
		Preconditions: preconditions, Postconditions: postconditions,
		VerificationCommandIDs: commands, AllowedTools: tools, NetworkPolicy: network,
		Budgets: input.Budgets, OutputSchemaHash: input.OutputSchemaHash,
		CreatedBy: input.CreatedBy, CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
	hash, err := semanticHash(taskCapsulePayload(capsule))
	if err != nil {
		return TaskCapsule{}, err
	}
	capsule.ContentHash = hash
	return capsule, nil
}

func ParseTaskCapsule(capsule TaskCapsule, contextPack ContextPack) (TaskCapsule, error) {
	parsed, err := NewTaskCapsule(NewTaskCapsuleInput{
		ID: capsule.ID, TaskKey: capsule.TaskKey, ProjectID: capsule.ProjectID,
		SandboxSessionID: capsule.SandboxSessionID, CandidateID: capsule.CandidateID,
		CandidateVersion: capsule.CandidateVersion, CandidateSessionEpoch: capsule.CandidateSessionEpoch,
		CandidateWriterLeaseEpoch: capsule.CandidateWriterLeaseEpoch,
		BaseCandidateTreeHash:     capsule.BaseCandidateTreeHash, BuildContract: capsule.BuildContract,
		TemplateReleases: capsule.TemplateReleases, Objective: capsule.Objective,
		ObligationIDs: capsule.ObligationIDs, AcceptanceCriterionIDs: capsule.AcceptanceCriterionIDs,
		ReadSet: capsule.ReadSet, WriteSet: capsule.WriteSet, ProtectedPaths: capsule.ProtectedPaths,
		Preconditions: capsule.Preconditions, Postconditions: capsule.Postconditions,
		VerificationCommandIDs: capsule.VerificationCommandIDs, AllowedTools: capsule.AllowedTools,
		NetworkPolicy: capsule.NetworkPolicy, Budgets: capsule.Budgets,
		OutputSchemaHash: capsule.OutputSchemaHash, CreatedBy: capsule.CreatedBy,
	}, contextPack, capsule.CreatedAt)
	if err != nil || capsule.SchemaVersion != TaskCapsuleSchemaVersion || capsule.ContentHash != parsed.ContentHash ||
		!capsule.CreatedAt.Equal(parsed.CreatedAt) || !equalJSON(taskCapsulePayload(capsule), taskCapsulePayload(parsed)) {
		if err != nil {
			return TaskCapsule{}, err
		}
		return TaskCapsule{}, fmt.Errorf("%w: schema, hash, timestamp, or canonical ordering", ErrInvalidTaskCapsule)
	}
	return parsed, nil
}

func normalizeContextItems(values []ContextItem) ([]ContextItem, error) {
	if len(values) == 0 || len(values) > 512 {
		return nil, fmt.Errorf("%w: ContextPack must contain 1..512 exact items", ErrInvalidContextPack)
	}
	allowed := map[ContextItemKind]bool{
		ContextBuildContract: true, ContextSourceRevision: true, ContextRepositoryFile: true,
		ContextTemplateRule: true, ContextContractSection: true, ContextPrototype: true,
		ContextAcceptance: true, ContextDiagnostics: true,
	}
	result := append([]ContextItem(nil), values...)
	seen := make(map[string]bool, len(result))
	total := int64(0)
	for index := range result {
		item := result[index]
		key, err := normalizeStableValue(item.Key, 240)
		if err != nil || !allowed[item.Kind] {
			return nil, fmt.Errorf("%w: item %d key or kind", ErrInvalidContextPack, index)
		}
		item.Key = key
		identity := string(item.Kind) + ":" + key
		if seen[identity] {
			return nil, fmt.Errorf("%w: duplicate item %s", ErrInvalidContextPack, identity)
		}
		seen[identity] = true
		if item.Source != nil {
			source, sourceErr := normalizeExactReference(*item.Source)
			if sourceErr != nil {
				return nil, fmt.Errorf("%w: item %s source", ErrInvalidContextPack, identity)
			}
			item.Source = &source
		}
		if item.Path != "" {
			path, pathErr := repository.NormalizePath(item.Path)
			if pathErr != nil || path != item.Path {
				return nil, fmt.Errorf("%w: item %s path", ErrInvalidContextPack, identity)
			}
		}
		if item.Kind == ContextRepositoryFile && item.Path == "" {
			return nil, fmt.Errorf("%w: repository file context requires a path", ErrInvalidContextPack)
		}
		if err := item.Content.validate(); err != nil {
			return nil, fmt.Errorf("%w: item %s content: %v", ErrInvalidContextPack, identity, err)
		}
		total += item.Content.ByteSize
		if total > 32<<20 {
			return nil, fmt.Errorf("%w: total referenced context exceeds 32 MiB", ErrInvalidContextPack)
		}
		result[index] = item
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Kind != result[right].Kind {
			return result[left].Kind < result[right].Kind
		}
		return result[left].Key < result[right].Key
	})
	return result, nil
}

func (ref BlobReference) validate() error {
	store := strings.TrimSpace(ref.Store)
	contentRef := strings.TrimSpace(ref.Ref)
	if store == "" || store != ref.Store || len(store) > 80 || !validUUIDs(ref.OwnerID) ||
		contentRef == "" || contentRef != ref.Ref || len(contentRef) > 2000 ||
		!sha256Pattern.MatchString(ref.ContentHash) || ref.ByteSize < 0 || ref.ByteSize > 4<<20 {
		return errors.New("invalid bounded content-addressed blob reference")
	}
	return nil
}

func (budgets TaskBudgets) validate() error {
	if budgets.WallTimeSeconds < 1 || budgets.WallTimeSeconds > 8*60*60 ||
		budgets.MaxInputTokens < 1 || budgets.MaxInputTokens > 4_000_000 ||
		budgets.MaxOutputTokens < 1 || budgets.MaxOutputTokens > 1_000_000 ||
		budgets.MaxCommands < 1 || budgets.MaxCommands > 10_000 ||
		budgets.MaxLogBytes < 1024 || budgets.MaxLogBytes > 1<<30 ||
		budgets.MaxPatchBytes < 1 || budgets.MaxPatchBytes > repository.MaxTreeBytes {
		return fmt.Errorf("%w: budgets are missing or outside platform bounds", ErrInvalidTaskCapsule)
	}
	return nil
}

func normalizeExactReference(value repository.ExactReference) (repository.ExactReference, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.ContentHash = strings.TrimSpace(value.ContentHash)
	if !validUUIDs(value.ID) || !exactHashPattern.MatchString(value.ContentHash) {
		return repository.ExactReference{}, errors.New("exact UUID and canonical sha256 are required")
	}
	return value, nil
}

func normalizeExactReferences(values []repository.ExactReference, maximum int) ([]repository.ExactReference, error) {
	if len(values) == 0 || len(values) > maximum {
		return nil, errors.New("bounded non-empty exact references are required")
	}
	result := append([]repository.ExactReference(nil), values...)
	seen := make(map[string]string, len(result))
	for index := range result {
		ref, err := normalizeExactReference(result[index])
		if err != nil || !sha256Pattern.MatchString(ref.ContentHash) {
			return nil, err
		}
		if hash, exists := seen[ref.ID]; exists {
			if hash != ref.ContentHash {
				return nil, errors.New("one exact reference ID has conflicting hashes")
			}
			return nil, errors.New("duplicate exact reference")
		}
		seen[ref.ID] = ref.ContentHash
		result[index] = ref
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func normalizeStableValue(value string, maximum int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximum || !stableIDPattern.MatchString(value) {
		return "", errors.New("stable identifier is invalid")
	}
	return value, nil
}

func normalizeStableList(values []string, minimum, maximum, maxLength int) ([]string, error) {
	if len(values) < minimum || len(values) > maximum {
		return nil, errors.New("stable identifier list is not bounded")
	}
	result := append([]string(nil), values...)
	seen := make(map[string]bool, len(result))
	for index, value := range result {
		normalized, err := normalizeStableValue(value, maxLength)
		if err != nil || seen[normalized] {
			return nil, errors.New("stable identifier list contains invalid or duplicate values")
		}
		seen[normalized] = true
		result[index] = normalized
	}
	sort.Strings(result)
	return result, nil
}

func normalizePaths(values []string, minimum, maximum int) ([]string, error) {
	if len(values) < minimum || len(values) > maximum {
		return nil, errors.New("path set is not bounded")
	}
	result := append([]string(nil), values...)
	seen := make(map[string]bool, len(result))
	for index, value := range result {
		normalized, err := repository.NormalizePath(value)
		if err != nil || normalized != value || seen[normalized] {
			return nil, errors.New("path set contains a non-canonical or duplicate path")
		}
		seen[normalized] = true
		result[index] = normalized
	}
	sort.Strings(result)
	return result, nil
}

func normalizeStatements(values []string, minimum, maximum int) ([]string, error) {
	if len(values) < minimum || len(values) > maximum {
		return nil, errors.New("statement set is not bounded")
	}
	result := append([]string(nil), values...)
	seen := make(map[string]bool, len(result))
	for index, value := range result {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 2000 || seen[value] {
			return nil, errors.New("statement set contains an invalid or duplicate value")
		}
		seen[value] = true
		result[index] = value
	}
	sort.Strings(result)
	return result, nil
}

func normalizeTools(values []string) ([]string, error) {
	result, err := normalizeStableList(values, 1, 16, 80)
	if err != nil {
		return nil, fmt.Errorf("%w: allowed tools", ErrInvalidTaskCapsule)
	}
	allowed := map[string]bool{
		"file.read": true, "file.write": true, "file.search": true,
		"shell.exec": true, "diagnostic.read": true,
	}
	for _, value := range result {
		if !allowed[value] {
			return nil, fmt.Errorf("%w: unsupported tool %q", ErrInvalidTaskCapsule, value)
		}
	}
	return result, nil
}

func normalizeNetworkPolicy(value NetworkPolicy) (NetworkPolicy, error) {
	value.Mode = strings.TrimSpace(value.Mode)
	if value.Mode != "none" && value.Mode != "registry_proxy" {
		return NetworkPolicy{}, fmt.Errorf("%w: network mode must be none or registry_proxy", ErrInvalidTaskCapsule)
	}
	hosts := make([]string, len(value.AllowedHosts))
	seen := make(map[string]bool, len(hosts))
	for index, host := range value.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || len(host) > 253 || net.ParseIP(host) != nil || !hostPattern.MatchString(host) ||
			strings.Contains(host, "..") || seen[host] {
			return NetworkPolicy{}, fmt.Errorf("%w: network host is invalid or duplicate", ErrInvalidTaskCapsule)
		}
		seen[host] = true
		hosts[index] = host
	}
	if value.Mode == "none" && len(hosts) != 0 || value.Mode == "registry_proxy" && len(hosts) == 0 {
		return NetworkPolicy{}, fmt.Errorf("%w: allowed hosts do not match network mode", ErrInvalidTaskCapsule)
	}
	sort.Strings(hosts)
	value.AllowedHosts = hosts
	return value, nil
}

func contextPackPayload(pack ContextPack) any {
	return struct {
		SchemaVersion         string                    `json:"schemaVersion"`
		ProjectID             string                    `json:"projectId"`
		CandidateID           string                    `json:"candidateId"`
		BaseCandidateTreeHash string                    `json:"baseCandidateTreeHash"`
		BuildContract         repository.ExactReference `json:"buildContract"`
		Items                 []ContextItem             `json:"items"`
	}{pack.SchemaVersion, pack.ProjectID, pack.CandidateID, pack.BaseCandidateTreeHash, pack.BuildContract, pack.Items}
}

func taskCapsulePayload(capsule TaskCapsule) any {
	return struct {
		SchemaVersion             string                      `json:"schemaVersion"`
		TaskKey                   string                      `json:"taskKey"`
		ProjectID                 string                      `json:"projectId"`
		SandboxSessionID          string                      `json:"sandboxSessionId"`
		CandidateID               string                      `json:"candidateId"`
		CandidateVersion          uint64                      `json:"candidateVersion"`
		CandidateSessionEpoch     uint64                      `json:"candidateSessionEpoch"`
		CandidateWriterLeaseEpoch uint64                      `json:"candidateWriterLeaseEpoch"`
		BaseCandidateTreeHash     string                      `json:"baseCandidateTreeHash"`
		BuildContract             repository.ExactReference   `json:"buildContract"`
		TemplateReleases          []repository.ExactReference `json:"templateReleases"`
		ContextPack               ContextPackReference        `json:"contextPack"`
		Objective                 string                      `json:"objective"`
		ObligationIDs             []string                    `json:"obligationIds"`
		AcceptanceCriterionIDs    []string                    `json:"acceptanceCriterionIds"`
		ReadSet                   []string                    `json:"readSet"`
		WriteSet                  []string                    `json:"writeSet"`
		ProtectedPaths            []string                    `json:"protectedPaths"`
		Preconditions             []string                    `json:"preconditions"`
		Postconditions            []string                    `json:"postconditions"`
		VerificationCommandIDs    []string                    `json:"verificationCommandIds"`
		AllowedTools              []string                    `json:"allowedTools"`
		NetworkPolicy             NetworkPolicy               `json:"networkPolicy"`
		Budgets                   TaskBudgets                 `json:"budgets"`
		OutputSchemaHash          string                      `json:"outputSchemaHash"`
	}{
		capsule.SchemaVersion, capsule.TaskKey, capsule.ProjectID, capsule.SandboxSessionID,
		capsule.CandidateID, capsule.CandidateVersion, capsule.CandidateSessionEpoch,
		capsule.CandidateWriterLeaseEpoch, capsule.BaseCandidateTreeHash, capsule.BuildContract,
		capsule.TemplateReleases, capsule.ContextPack, capsule.Objective, capsule.ObligationIDs,
		capsule.AcceptanceCriterionIDs, capsule.ReadSet, capsule.WriteSet, capsule.ProtectedPaths,
		capsule.Preconditions, capsule.Postconditions, capsule.VerificationCommandIDs,
		capsule.AllowedTools, capsule.NetworkPolicy, capsule.Budgets, capsule.OutputSchemaHash,
	}
}

func semanticHash(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func equalJSON(left, right any) bool {
	leftPayload, leftErr := json.Marshal(left)
	rightPayload, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftPayload) == string(rightPayload)
}

func validUUIDs(values ...string) bool {
	for _, value := range values {
		if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func pathContains(root, target string) bool {
	return target == root || strings.HasPrefix(target, root+"/")
}
