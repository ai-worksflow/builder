package workflow

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

// MemoryStore implements the same CAS and lease rules as GORMStore. It is useful
// for deterministic engine tests and local single-process deployments.
type MemoryStore struct {
	mu                 sync.Mutex
	ids                IDGenerator
	definitions        map[string]map[int]DefinitionRecord
	definitionVersions map[string]DefinitionRecord
	manifests          map[string]domain.InputManifest
	proposals          map[string]*domain.OutputProposal
	runs               map[string]*RunRecord
	events             map[string][]Event
	slices             map[string]SliceRecord
}

func NewMemoryStore(ids IDGenerator) *MemoryStore {
	if ids == nil {
		ids = UUIDGenerator{}
	}
	return &MemoryStore{ids: ids, definitions: map[string]map[int]DefinitionRecord{}, definitionVersions: map[string]DefinitionRecord{}, manifests: map[string]domain.InputManifest{}, proposals: map[string]*domain.OutputProposal{}, runs: map[string]*RunRecord{}, events: map[string][]Event{}, slices: map[string]SliceRecord{}}
}

func (s *MemoryStore) SaveDefinition(_ context.Context, record DefinitionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := record.Definition.Validate(); err != nil {
		return err
	}
	versions := s.definitions[record.Definition.ID]
	if versions == nil {
		if record.Definition.Version != 1 {
			return ErrCASConflict
		}
		s.definitions[record.Definition.ID] = map[int]DefinitionRecord{}
		versions = s.definitions[record.Definition.ID]
	} else {
		latest := 0
		var base DefinitionRecord
		for version, existing := range versions {
			if version > latest {
				latest, base = version, existing
			}
		}
		if record.Definition.Version != latest+1 || base.ProjectID != record.ProjectID || base.Key != record.Key || base.Title != record.Title || base.Description != record.Description {
			return ErrCASConflict
		}
	}
	if _, exists := versions[record.Definition.Version]; exists {
		return ErrCASConflict
	}
	if _, exists := s.definitionVersions[record.VersionID]; exists {
		return ErrCASConflict
	}
	copyRecord := cloneDefinitionRecord(record)
	versions[record.Definition.Version] = copyRecord
	s.definitionVersions[record.VersionID] = copyRecord
	return nil
}

func (s *MemoryStore) GetDefinition(_ context.Context, id string, version int) (DefinitionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.definitions[id][version]
	if !exists {
		return DefinitionRecord{}, domain.ErrNotFound
	}
	return cloneDefinitionRecord(record), nil
}

func (s *MemoryStore) GetDefinitionVersion(_ context.Context, id string) (DefinitionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.definitionVersions[id]
	if !exists {
		return DefinitionRecord{}, domain.ErrNotFound
	}
	return cloneDefinitionRecord(record), nil
}

func (s *MemoryStore) ListDefinitions(_ context.Context, projectID string) ([]DefinitionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]DefinitionRecord, 0)
	for _, versions := range s.definitions {
		latest := 0
		for version, record := range versions {
			if (record.ProjectID == "" || record.ProjectID == projectID) && version > latest {
				latest = version
			}
		}
		if latest > 0 {
			result = append(result, cloneDefinitionRecord(versions[latest]))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func (s *MemoryStore) ListDefinitionVersions(_ context.Context, id string) ([]DefinitionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.definitions[id]
	if len(versions) == 0 {
		return nil, domain.ErrNotFound
	}
	numbers := make([]int, 0, len(versions))
	for version := range versions {
		numbers = append(numbers, version)
	}
	sort.Ints(numbers)
	result := make([]DefinitionRecord, 0, len(numbers))
	for _, version := range numbers {
		result = append(result, cloneDefinitionRecord(versions[version]))
	}
	return result, nil
}

func (s *MemoryStore) PublishDefinitionVersion(_ context.Context, projectID, definitionID, versionID, _ string) (DefinitionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.definitionVersions[versionID]
	if !exists || record.Definition.ID != definitionID || (record.ProjectID != "" && record.ProjectID != projectID) {
		return DefinitionRecord{}, domain.ErrNotFound
	}
	record.Published = true
	s.definitionVersions[versionID] = record
	s.definitions[definitionID][record.Definition.Version] = record
	return cloneDefinitionRecord(record), nil
}

func (s *MemoryStore) UnpublishDefinitionVersion(_ context.Context, projectID, definitionID, versionID, _ string) (DefinitionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.definitionVersions[versionID]
	if !exists || record.Definition.ID != definitionID || (record.ProjectID != "" && record.ProjectID != projectID) {
		return DefinitionRecord{}, domain.ErrNotFound
	}
	record.Published = false
	s.definitionVersions[versionID] = record
	s.definitions[definitionID][record.Definition.Version] = record
	return cloneDefinitionRecord(record), nil
}

func (s *MemoryStore) SaveManifest(_ context.Context, manifest domain.InputManifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := manifest.Validate(); err != nil {
		return err
	}
	if _, exists := s.manifests[manifest.ID]; exists {
		return ErrCASConflict
	}
	s.manifests[manifest.ID] = cloneManifest(manifest)
	return nil
}

func (s *MemoryStore) GetManifest(_ context.Context, id string) (domain.InputManifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, exists := s.manifests[id]
	if !exists {
		return domain.InputManifest{}, domain.ErrNotFound
	}
	return cloneManifest(manifest), nil
}

func (s *MemoryStore) SaveProposal(_ context.Context, proposal *domain.OutputProposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if proposal == nil {
		return domain.ErrInvalidArgument
	}
	if err := proposal.ValidatePayloadHash(); err != nil {
		return err
	}
	current, exists := s.proposals[proposal.ID]
	if exists && proposal.Version <= current.Version {
		return ErrCASConflict
	}
	s.proposals[proposal.ID] = cloneProposal(proposal)
	return nil
}

func (s *MemoryStore) GetProposal(_ context.Context, id string) (*domain.OutputProposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal, exists := s.proposals[id]
	if !exists {
		return nil, domain.ErrNotFound
	}
	return cloneProposal(proposal), nil
}

func (s *MemoryStore) CreateRun(_ context.Context, run *RunRecord, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := run.Validate(); err != nil {
		return err
	}
	if _, exists := s.runs[run.ID]; exists {
		return ErrCASConflict
	}
	copyRun := cloneRunRecord(run)
	copyRun.EventCursor = uint64(len(events))
	run.EventCursor = copyRun.EventCursor
	for index := range events {
		events[index].Sequence = uint64(index + 1)
		if events[index].ID == "" {
			events[index].ID = s.ids.NewID()
		}
	}
	s.runs[run.ID] = copyRun
	s.events[run.ID] = cloneEvents(events)
	return nil
}

func (s *MemoryStore) GetRun(_ context.Context, id string) (*RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.runs[id]
	if !exists {
		return nil, domain.ErrNotFound
	}
	return cloneRunRecord(run), nil
}

func (s *MemoryStore) ListRuns(_ context.Context, projectID string, filter StoreRunFilter) ([]RunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]RunSummary, 0)
	for _, run := range s.runs {
		if run.ProjectID != projectID || filter.Status != "" && run.Status != filter.Status {
			continue
		}
		if filter.BeforeCreatedAt != nil {
			if run.CreatedAt.After(*filter.BeforeCreatedAt) ||
				(run.CreatedAt.Equal(*filter.BeforeCreatedAt) && run.ID >= filter.BeforeID) {
				continue
			}
		}
		items = append(items, summaryFromRun(run))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func summaryFromRun(run *RunRecord) RunSummary {
	return RunSummary{
		ID: run.ID, ProjectID: run.ProjectID, DefinitionVersionID: run.DefinitionVersionID,
		Status: run.Status, EventCursor: run.EventCursor, StartedBy: run.StartedBy,
		StartedAt: run.StartedAt, CompletedAt: run.CompletedAt, CancelledAt: run.CancelledAt,
		Failure: cloneRaw(run.Failure), CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
	}
}

func (s *MemoryStore) ClaimRunnable(_ context.Context, workerID string, now time.Time, leaseDuration time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type candidate struct {
		run  *RunRecord
		node *NodeRecord
	}
	candidates := make([]candidate, 0)
	for _, run := range s.runs {
		if run.Status.Terminal() {
			continue
		}
		for _, node := range run.Nodes {
			ready := node.Status == NodeReady && !node.AvailableAt.After(now)
			expired := node.Status == NodeRunning && node.LeaseExpiresAt != nil && node.LeaseExpiresAt.Before(now)
			if ready || expired {
				candidates = append(candidates, candidate{run, node})
			}
		}
	}
	if len(candidates) == 0 {
		return Lease{}, ErrNoRunnableNode
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].node.AvailableAt.Equal(candidates[j].node.AvailableAt) {
			return candidates[i].node.ID < candidates[j].node.ID
		}
		return candidates[i].node.AvailableAt.Before(candidates[j].node.AvailableAt)
	})
	selected := candidates[0]
	expires := now.UTC().Add(leaseDuration)
	started := now.UTC()
	selected.node.Status = NodeRunning
	selected.node.Attempt++
	selected.node.LeaseOwner = workerID
	selected.node.LeaseExpiresAt = &expires
	selected.node.UpdatedAt = now.UTC()
	if selected.node.StartedAt == nil {
		selected.node.StartedAt = &started
	}
	selected.run.Status = RunRunning
	selected.run.EventCursor++
	selected.run.UpdatedAt = now.UTC()
	event := Event{ID: s.ids.NewID(), RunID: selected.run.ID, Sequence: selected.run.EventCursor, Type: "node.claimed", NodeKey: selected.node.Key, Payload: mustJSON(map[string]any{"workerId": workerID, "attempt": selected.node.Attempt, "leaseExpiresAt": expires}), CreatedAt: now.UTC()}
	s.events[selected.run.ID] = append(s.events[selected.run.ID], event)
	return Lease{RunID: selected.run.ID, NodeID: selected.node.ID, NodeKey: selected.node.Key, WorkerID: workerID, Attempt: selected.node.Attempt, LeaseExpiresAt: expires}, nil
}

func (s *MemoryStore) RenewLease(_ context.Context, lease Lease, now time.Time, duration time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.runs[lease.RunID]
	if !exists {
		return Lease{}, domain.ErrNotFound
	}
	node := findNodeByID(run, lease.NodeID)
	if node == nil || node.Status != NodeRunning || node.LeaseOwner != lease.WorkerID || node.LeaseExpiresAt == nil || node.LeaseExpiresAt.Before(now) {
		return Lease{}, ErrLeaseLost
	}
	expires := now.UTC().Add(duration)
	node.LeaseExpiresAt = &expires
	node.UpdatedAt = now.UTC()
	lease.LeaseExpiresAt = expires
	return lease, nil
}

func (s *MemoryStore) Commit(_ context.Context, mutation RunMutation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.runs[mutation.RunID]
	if !exists {
		return domain.ErrNotFound
	}
	if run.EventCursor != mutation.ExpectedCursor {
		return ErrCASConflict
	}
	if len(mutation.Events) == 0 {
		return domain.ErrInvalidArgument
	}
	for _, nodeMutation := range mutation.Nodes {
		current := findNodeByID(run, nodeMutation.Node.ID)
		if current == nil || current.Status != nodeMutation.ExpectedStatus {
			return ErrLeaseLost
		}
		if nodeMutation.ExpectedOwner != "" {
			if current.LeaseOwner != nodeMutation.ExpectedOwner || current.LeaseExpiresAt == nil || current.LeaseExpiresAt.Before(mutation.UpdatedAt) {
				return ErrLeaseLost
			}
		}
	}
	for _, node := range mutation.NewNodes {
		if _, exists := run.Nodes[node.Key]; exists {
			return ErrCASConflict
		}
	}
	run.Status = mutation.Status
	run.Context = cloneRunContext(mutation.Context)
	run.Failure = cloneRaw(mutation.Failure)
	run.CompletedAt = cloneTime(mutation.CompletedAt)
	run.CancelledAt = cloneTime(mutation.CancelledAt)
	run.UpdatedAt = mutation.UpdatedAt
	for _, nodeMutation := range mutation.Nodes {
		run.Nodes[nodeMutation.Node.Key] = cloneNodeRecord(&nodeMutation.Node)
	}
	for index := range mutation.NewNodes {
		node := mutation.NewNodes[index]
		run.Nodes[node.Key] = cloneNodeRecord(&node)
	}
	for _, slice := range mutation.Slices {
		s.slices[slice.ID] = slice
	}
	for index, event := range mutation.Events {
		event.Sequence = mutation.ExpectedCursor + uint64(index) + 1
		if event.ID == "" {
			event.ID = s.ids.NewID()
		}
		s.events[run.ID] = append(s.events[run.ID], event)
	}
	run.EventCursor += uint64(len(mutation.Events))
	return nil
}

func (s *MemoryStore) ListEvents(_ context.Context, runID string, after uint64, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[runID]; !exists {
		return nil, domain.ErrNotFound
	}
	if limit <= 0 {
		limit = 100
	}
	result := make([]Event, 0, limit)
	for _, event := range s.events[runID] {
		if event.Sequence > after {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return cloneEvents(result), nil
}

func cloneDefinitionRecord(record DefinitionRecord) DefinitionRecord {
	encoded, _ := json.Marshal(record.Definition)
	_ = json.Unmarshal(encoded, &record.Definition)
	return record
}
func cloneManifest(value domain.InputManifest) domain.InputManifest {
	encoded, _ := json.Marshal(value)
	var clone domain.InputManifest
	_ = json.Unmarshal(encoded, &clone)
	return clone
}
func cloneProposal(value *domain.OutputProposal) *domain.OutputProposal {
	encoded, _ := json.Marshal(value)
	var clone domain.OutputProposal
	_ = json.Unmarshal(encoded, &clone)
	return &clone
}
func cloneRunRecord(value *RunRecord) *RunRecord {
	copyRun := *value
	copyRun.Scope = cloneRaw(value.Scope)
	copyRun.Failure = cloneRaw(value.Failure)
	copyRun.Context = cloneRunContext(value.Context)
	copyRun.StartedAt = cloneTime(value.StartedAt)
	copyRun.CompletedAt = cloneTime(value.CompletedAt)
	copyRun.CancelledAt = cloneTime(value.CancelledAt)
	copyRun.Nodes = map[string]*NodeRecord{}
	for key, node := range value.Nodes {
		copyRun.Nodes[key] = cloneNodeRecord(node)
	}
	return &copyRun
}
func cloneRunContext(value RunContext) RunContext {
	encoded, _ := json.Marshal(value)
	var clone RunContext
	_ = json.Unmarshal(encoded, &clone)
	clone.ensureMaps()
	return clone
}
func cloneNodeRecord(value *NodeRecord) *NodeRecord {
	copyNode := *value
	copyNode.Failure = cloneRaw(value.Failure)
	copyNode.StartedAt = cloneTime(value.StartedAt)
	copyNode.CompletedAt = cloneTime(value.CompletedAt)
	copyNode.LeaseExpiresAt = cloneTime(value.LeaseExpiresAt)
	if value.InputManifest != nil {
		ref := *value.InputManifest
		copyNode.InputManifest = &ref
	}
	if value.OutputProposal != nil {
		ref := *value.OutputProposal
		copyNode.OutputProposal = &ref
	}
	return &copyNode
}
func cloneRaw(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }
func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
func cloneEvents(events []Event) []Event {
	clones := append([]Event(nil), events...)
	for index := range clones {
		clones[index].Payload = cloneRaw(clones[index].Payload)
	}
	return clones
}
func findNodeByID(run *RunRecord, id string) *NodeRecord {
	for _, node := range run.Nodes {
		if node.ID == id {
			return node
		}
	}
	return nil
}
