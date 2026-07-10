package dataruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type publicRuntimeRepositoryStub struct {
	PublicRuntimeRepository
	policy       PublicTablePolicy
	policyErr    error
	find         publicCapabilityRecord
	findErr      error
	prepared     publicCapabilityRecord
	prepareInput PreparePublicCapabilityInput
	prepareID    string
	prepareHash  []byte
	prepareCalls int
}

func (r *publicRuntimeRepositoryStub) GetPublicTablePolicy(context.Context, string, string) (PublicTablePolicy, error) {
	return r.policy, r.policyErr
}

func (r *publicRuntimeRepositoryStub) FindPublicCapability(context.Context, string) (publicCapabilityRecord, error) {
	return r.find, r.findErr
}

func (r *publicRuntimeRepositoryStub) PreparePublicCapability(
	_ context.Context,
	input PreparePublicCapabilityInput,
	capabilityID string,
	digest []byte,
	origins []string,
	expiresAt time.Time,
) (publicCapabilityRecord, error) {
	r.prepareCalls++
	r.prepareInput = input
	r.prepareID = capabilityID
	r.prepareHash = append([]byte(nil), digest...)
	record := r.prepared
	record.ID = capabilityID
	record.ProjectID = input.ProjectID
	record.DeploymentID = input.DeploymentID
	record.DeploymentVersionID = input.DeploymentVersionID
	record.TokenDigest = append([]byte(nil), digest...)
	record.AllowedOrigins = append([]string(nil), origins...)
	record.ExpiresAt = expiresAt
	record.Status = "pending"
	return record, nil
}

type publicDataRepositoryStub struct {
	Repository
	table           Table
	page            RecordPage
	record          Record
	listCalls       int
	updateCalls     int
	lastMutation    MutationContext
	lastRecordInput RecordInput
}

func (r *publicDataRepositoryStub) GetTable(context.Context, string, string) (Table, error) {
	return r.table, nil
}

func (r *publicDataRepositoryStub) ListTables(context.Context, string) ([]Table, error) {
	if r.table.ID == "" {
		return []Table{}, nil
	}
	return []Table{r.table}, nil
}

func (r *publicDataRepositoryStub) ListRecords(context.Context, string, string, int, int) (RecordPage, error) {
	r.listCalls++
	return r.page, nil
}

func (r *publicDataRepositoryStub) UpdateRecord(_ context.Context, _, _, _ string, mutation MutationContext, input RecordInput) (Record, error) {
	r.updateCalls++
	r.lastMutation = mutation
	r.lastRecordInput = input
	return r.record, nil
}

func newPublicServiceForTest(t *testing.T, data Repository, runtime PublicRuntimeRepository, now time.Time) *PublicRuntimeService {
	t.Helper()
	service, err := NewPublicRuntimeService(PublicRuntimeDependencies{
		Data: data, Runtime: runtime, Access: &accessStub{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestPublicRuntimeIsDefaultDenyWhenTableHasNoPolicy(t *testing.T) {
	t.Parallel()

	projectID, deploymentID, tableID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	data := &publicDataRepositoryStub{}
	runtime := &publicRuntimeRepositoryStub{policyErr: NotFound("Public table policy")}
	service := newPublicServiceForTest(t, data, runtime, time.Now())
	capability := PublicCapability{
		ID: uuid.NewString(), ProjectID: projectID, DeploymentID: deploymentID,
		authenticated: true,
	}
	_, err := service.ListPublicRecords(context.Background(), capability, tableID, 10, 0)
	runtimeError, ok := AsRuntimeError(err)
	if !ok || runtimeError.Code != CodePublicPolicyDenied || data.listCalls != 0 {
		t.Fatalf("missing policy did not fail closed: err=%v listCalls=%d", err, data.listCalls)
	}
}

func TestPublicRuntimeFiltersReadsAndRejectsFieldsOutsideWriteAllowlist(t *testing.T) {
	t.Parallel()

	projectID, deploymentID, tableID, recordID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	name := json.RawMessage(`"Ada"`)
	secret := json.RawMessage(`"hidden"`)
	data := &publicDataRepositoryStub{
		page:   RecordPage{Records: []Record{{ID: recordID, Values: map[string]json.RawMessage{"name": name, "secret": secret}}}, Total: 1, Limit: 10},
		record: Record{ID: recordID, Values: map[string]json.RawMessage{"name": name, "secret": secret}},
	}
	runtime := &publicRuntimeRepositoryStub{policy: PublicTablePolicy{
		ProjectID: projectID, TableID: tableID, AllowRead: true, AllowUpdate: true,
		ReadableFields: []string{"name"}, WritableFields: []string{"name"},
	}}
	service := newPublicServiceForTest(t, data, runtime, time.Now())
	capability := PublicCapability{
		ID: uuid.NewString(), ProjectID: projectID, DeploymentID: deploymentID,
		authenticated: true,
	}
	page, err := service.ListPublicRecords(context.Background(), capability, tableID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || string(page.Records[0].Values["name"]) != `"Ada"` || page.Records[0].Values["secret"] != nil {
		t.Fatalf("read allowlist was not applied: %+v", page.Records)
	}

	_, err = service.UpdatePublicRecord(context.Background(), capability, tableID, recordID, "request-1", RecordInput{
		Values: map[string]json.RawMessage{"secret": json.RawMessage(`"changed"`)},
	})
	if runtimeError, ok := AsRuntimeError(err); !ok || runtimeError.Code != CodeInvalidRequest || data.updateCalls != 0 {
		t.Fatalf("write allowlist did not reject secret field: err=%v calls=%d", err, data.updateCalls)
	}

	updated, err := service.UpdatePublicRecord(context.Background(), capability, tableID, recordID, "request-2", RecordInput{
		Values: map[string]json.RawMessage{"name": json.RawMessage(`"Grace"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if data.updateCalls != 1 || data.lastMutation.PublicDeploymentID != deploymentID || data.lastMutation.PublicCapabilityID != capability.ID || data.lastMutation.ActorID != "" || updated.Values["secret"] != nil {
		t.Fatalf("public mutation identity or response filtering failed: mutation=%+v updated=%+v", data.lastMutation, updated)
	}
}

func TestPublicCapabilityIsHashedBoundToDeploymentAndOrigin(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	capabilityID, token, err := newPublicCapabilityToken()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	projectID, deploymentID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	runtime := &publicRuntimeRepositoryStub{find: publicCapabilityRecord{
		ID: capabilityID, ProjectID: projectID, DeploymentID: deploymentID,
		DeploymentVersionID: versionID, TokenDigest: digest[:], Status: "active",
		AllowedOrigins: []string{"https://app.example"}, ExpiresAt: now.Add(time.Hour),
	}}
	service := newPublicServiceForTest(t, &publicDataRepositoryStub{}, runtime, now)
	capability, err := service.Authenticate(context.Background(), deploymentID, token)
	if err != nil || capability.ProjectID != projectID || capability.DeploymentVersionID != versionID {
		t.Fatalf("valid capability was rejected: capability=%+v err=%v", capability, err)
	}
	if err := service.ValidateOrigin(capability, "https://APP.EXAMPLE:443/"); err != nil {
		t.Fatalf("normalized allowed origin was rejected: %v", err)
	}
	if err := service.ValidateOrigin(capability, "https://evil.example"); err == nil {
		t.Fatal("unexpected origin was accepted")
	}
	if _, err := service.Authenticate(context.Background(), uuid.NewString(), token); err == nil {
		t.Fatal("capability was not bound to its deployment")
	}
	altered := token[:len(token)-1] + "A"
	if altered == token {
		altered = token[:len(token)-1] + "B"
	}
	if _, err := service.Authenticate(context.Background(), deploymentID, altered); err == nil {
		t.Fatal("altered capability token was accepted")
	}
}

func TestPreparedCapabilityReturnsPlaintextOnceAndPersistsOnlyDigest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	capabilityID, token, err := newPublicCapabilityToken()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &publicRuntimeRepositoryStub{}
	service, err := NewPublicRuntimeService(PublicRuntimeDependencies{
		Data: &publicDataRepositoryStub{}, Runtime: runtime, Access: &accessStub{},
		Now:         func() time.Time { return now },
		TokenSource: func() (string, string, error) { return capabilityID, token, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	input := PreparePublicCapabilityInput{
		ProjectID: uuid.NewString(), DeploymentID: uuid.NewString(), DeploymentVersionID: uuid.NewString(),
		Environment:    ScopeProduction,
		AllowedOrigins: []string{"https://APP.example/"}, ExpiresAt: now.Add(24 * time.Hour),
	}
	config, err := service.PrepareDeploymentCapability(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(token))
	if config.CapabilityToken != token || config.CapabilityID != capabilityID || runtime.prepareCalls != 1 {
		t.Fatalf("prepared config is incomplete: %+v calls=%d", config, runtime.prepareCalls)
	}
	if string(runtime.prepareHash) != string(expected[:]) || string(runtime.prepareHash) == token {
		t.Fatalf("repository did not receive only the token digest: %x", runtime.prepareHash)
	}
	if len(config.AllowedOrigins) != 1 || config.AllowedOrigins[0] != "https://app.example" {
		t.Fatalf("origins were not normalized: %v", config.AllowedOrigins)
	}
}

func TestPreparedPreviewCapabilityUsesShortDefaultExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	capabilityID, token, err := newPublicCapabilityToken()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &publicRuntimeRepositoryStub{}
	service, err := NewPublicRuntimeService(PublicRuntimeDependencies{
		Data: &publicDataRepositoryStub{}, Runtime: runtime, Access: &accessStub{},
		Now:         func() time.Time { return now },
		TokenSource: func() (string, string, error) { return capabilityID, token, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := service.PrepareDeploymentCapability(context.Background(), PreparePublicCapabilityInput{
		ProjectID: uuid.NewString(), DeploymentID: uuid.NewString(), DeploymentVersionID: uuid.NewString(),
		Environment: ScopePreview, AllowedOrigins: []string{"https://preview.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !config.ExpiresAt.Equal(now.Add(DefaultPreviewCapabilityTTL)) {
		t.Fatalf("preview capability has unsafe default expiry: %s", config.ExpiresAt)
	}
}

func TestPublicPolicyValidationRequiresKnownExplicitFields(t *testing.T) {
	t.Parallel()

	table := Table{Columns: []Column{{Name: "name"}, {Name: "email"}}}
	input := PublicTablePolicyInput{AllowRead: true, ReadableFields: []string{"name", "unknown"}}
	if err := ValidatePublicTablePolicy(&input, table); err == nil {
		t.Fatal("unknown public field was accepted")
	}
	input = PublicTablePolicyInput{ReadableFields: []string{"name"}}
	if err := ValidatePublicTablePolicy(&input, table); err == nil {
		t.Fatal("read fields were accepted while public read was disabled")
	}
	input = PublicTablePolicyInput{
		AllowRead: true, AllowCreate: true,
		ReadableFields: []string{"email", "name"}, WritableFields: []string{"name"},
	}
	if err := ValidatePublicTablePolicy(&input, table); err != nil {
		t.Fatal(err)
	}
}

type publicPolicyCASRepository struct {
	PublicRuntimeRepository
	mu     sync.Mutex
	policy *PublicTablePolicy
}

func (r *publicPolicyCASRepository) ListPublicTablePolicies(context.Context, string) ([]PublicTablePolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.policy == nil {
		return []PublicTablePolicy{}, nil
	}
	return []PublicTablePolicy{*r.policy}, nil
}

func (r *publicPolicyCASRepository) PutPublicTablePolicy(_ context.Context, projectID, tableID, _ string, expectedVersion uint64, _ PublicTablePolicyInput) (PublicTablePolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := uint64(0)
	if r.policy != nil {
		current = r.policy.Version
	}
	if current != expectedVersion {
		return PublicTablePolicy{}, PreconditionFailed("The public table policy changed since it was loaded")
	}
	next := PublicTablePolicy{ProjectID: projectID, TableID: tableID, TableName: "messages", AllowRead: true, Version: current + 1}
	r.policy = &next
	return next, nil
}

func (r *publicPolicyCASRepository) DeletePublicTablePolicy(_ context.Context, _, _ string, _ string, expectedVersion uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.policy == nil || r.policy.Version != expectedVersion {
		return PreconditionFailed("The public table policy changed since it was loaded")
	}
	r.policy = nil
	return nil
}

func TestPublicPolicyListIncludesVersionZeroDefaultAndETag(t *testing.T) {
	t.Parallel()
	projectID, tableID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	table := Table{ID: tableID, Name: "messages", Columns: []Column{{Name: "body"}}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	service := newPublicServiceForTest(t, &publicDataRepositoryStub{table: table}, &publicPolicyCASRepository{}, time.Now())
	policies, err := service.ListPolicies(context.Background(), projectID, actorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].Version != 0 || policies[0].AllowRead || policies[0].ETag != PublicTablePolicyETag(projectID, tableID, 0) {
		t.Fatalf("default-deny policy state is not conditionally writable: %+v", policies)
	}
}

func TestPublicPolicyConcurrentWritersCannotLoseUpdate(t *testing.T) {
	t.Parallel()
	projectID, tableID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	table := Table{ID: tableID, Name: "messages", Columns: []Column{{Name: "body"}}}
	repository := &publicPolicyCASRepository{policy: &PublicTablePolicy{ProjectID: projectID, TableID: tableID, TableName: table.Name, Version: 1}}
	service := newPublicServiceForTest(t, &publicDataRepositoryStub{table: table}, repository, time.Now())
	input := PublicTablePolicyInput{AllowRead: true, ReadableFields: []string{"body"}}
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, err := service.PutPolicy(context.Background(), projectID, tableID, actorID, 1, input)
			results <- err
		}()
	}
	close(start)
	succeeded, stale := 0, 0
	for index := 0; index < 2; index++ {
		err := <-results
		if err == nil {
			succeeded++
			continue
		}
		if typed, ok := AsRuntimeError(err); ok && typed.Code == CodePreconditionFailed {
			stale++
			continue
		}
		t.Fatalf("unexpected concurrent policy error: %v", err)
	}
	if succeeded != 1 || stale != 1 {
		t.Fatalf("concurrent writers were not serialized by version: success=%d stale=%d", succeeded, stale)
	}
}
