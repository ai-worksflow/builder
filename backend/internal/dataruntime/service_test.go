package dataruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

type accessStub struct {
	mu      sync.Mutex
	err     error
	actions []core.Action
}

func (a *accessStub) Authorize(_ context.Context, _, _ string, action core.Action) (core.Role, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
	return core.RoleOwner, a.err
}

type proberStub struct {
	result SupabaseConnectionResult
	err    error
	calls  int
}

func (p *proberStub) Probe(context.Context, SupabaseConnectionInput) (SupabaseConnectionResult, error) {
	p.calls++
	return p.result, p.err
}

type repositoryStub struct {
	Repository
	setInput  EnvironmentVariableInput
	saveCalls int
	saved     SupabaseConnectionResult
	public    map[string]string
}

func (r *repositoryStub) SetVariable(_ context.Context, _ string, _ MutationContext, input EnvironmentVariableInput) (EnvironmentVariable, error) {
	r.setInput = input
	return EnvironmentVariable{ID: uuid.NewString(), Name: input.Name, Scope: input.Scope, Kind: input.Kind, MaskedValue: "••••••••", ValueBytes: len(input.Value)}, nil
}

func (r *repositoryStub) SaveConnection(_ context.Context, _ string, _ MutationContext, result SupabaseConnectionResult) (ConnectionMetadata, error) {
	r.saveCalls++
	r.saved = result
	return ConnectionMetadata{Provider: "supabase", Endpoint: result.Endpoint}, nil
}

func (r *repositoryStub) PublicEnvironment(context.Context, string, EnvironmentScope) (map[string]string, error) {
	result := make(map[string]string, len(r.public))
	for key, value := range r.public {
		result[key] = value
	}
	return result, nil
}

func TestSuccessfulProbeResultMustRemainBoundToRequestedEndpoint(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{}
	access := &accessStub{}
	prober := &proberStub{result: SupabaseConnectionResult{
		OK: true, Endpoint: "https://attacker.example", Status: 200, Message: "fake",
	}}
	service, err := NewService(Dependencies{Repository: repository, Access: access, Prober: prober})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ConnectSupabase(context.Background(), uuid.NewString(), uuid.NewString(), SupabaseConnectionInput{
		Endpoint: "https://project.supabase.co", Key: "server-key",
	})
	if runtimeErr, ok := AsRuntimeError(err); !ok || runtimeErr.Code != CodeConnectionFailed || repository.saveCalls != 0 {
		t.Fatalf("mismatched probe result was trusted: err=%v saves=%d", err, repository.saveCalls)
	}

	prober.result = SupabaseConnectionResult{
		OK: true, Endpoint: "https://project.supabase.co", Status: 200, LatencyMS: 5,
		SchemaTables: []string{"users", "accounts", "users"}, Message: "connected",
	}
	_, err = service.ConnectSupabase(context.Background(), uuid.NewString(), uuid.NewString(), SupabaseConnectionInput{
		Endpoint: "https://project.supabase.co", Key: "server-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.saveCalls != 1 || len(repository.saved.SchemaTables) != 2 || repository.saved.SchemaTables[0] != "accounts" {
		t.Fatalf("schema result was not normalized: %+v", repository.saved)
	}
}

func TestEveryDataServiceMethodChecksProjectRBAC(t *testing.T) {
	t.Parallel()

	projectID, actorID := uuid.NewString(), uuid.NewString()
	tableID, recordID, itemID, variableID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	access := &accessStub{err: core.ErrForbidden}
	service, err := NewService(Dependencies{Repository: &repositoryStub{}, Access: access, Prober: &proberStub{}})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]json.RawMessage{}
	metadata := map[string]json.RawMessage{"email": json.RawMessage(`"owner@example.com"`)}
	operation := []MigrationOperation{{Type: MigrationCreateTable, Table: &TableInput{Name: "users"}}}

	tests := []struct {
		name   string
		action core.Action
		call   func() error
	}{
		{"snapshot", core.ActionView, func() error { _, err := service.Snapshot(context.Background(), projectID, actorID); return err }},
		{"list tables", core.ActionView, func() error { _, err := service.ListTables(context.Background(), projectID, actorID); return err }},
		{"get table", core.ActionView, func() error {
			_, err := service.GetTable(context.Background(), projectID, tableID, actorID)
			return err
		}},
		{"create table", core.ActionEdit, func() error {
			_, err := service.CreateTable(context.Background(), projectID, actorID, TableInput{Name: "users"})
			return err
		}},
		{"rename table", core.ActionEdit, func() error {
			_, err := service.RenameTable(context.Background(), projectID, tableID, actorID, "accounts")
			return err
		}},
		{"delete table", core.ActionEdit, func() error { return service.DeleteTable(context.Background(), projectID, tableID, actorID) }},
		{"list records", core.ActionView, func() error {
			_, err := service.ListRecords(context.Background(), projectID, tableID, actorID, 50, 0)
			return err
		}},
		{"get record", core.ActionView, func() error {
			_, err := service.GetRecord(context.Background(), projectID, tableID, recordID, actorID)
			return err
		}},
		{"create record", core.ActionEdit, func() error {
			_, err := service.CreateRecord(context.Background(), projectID, tableID, actorID, RecordInput{Values: values})
			return err
		}},
		{"update record", core.ActionEdit, func() error {
			_, err := service.UpdateRecord(context.Background(), projectID, tableID, recordID, actorID, RecordInput{Values: values})
			return err
		}},
		{"delete record", core.ActionEdit, func() error { return service.DeleteRecord(context.Background(), projectID, tableID, recordID, actorID) }},
		{"list metadata", core.ActionView, func() error {
			_, err := service.ListMetadata(context.Background(), projectID, actorID, MetadataAuthUsers)
			return err
		}},
		{"get metadata", core.ActionView, func() error {
			_, err := service.GetMetadata(context.Background(), projectID, itemID, actorID, MetadataAuthUsers)
			return err
		}},
		{"create metadata", core.ActionAdmin, func() error {
			_, err := service.CreateMetadata(context.Background(), projectID, actorID, MetadataAuthUsers, metadata)
			return err
		}},
		{"update metadata", core.ActionAdmin, func() error {
			_, err := service.UpdateMetadata(context.Background(), projectID, itemID, actorID, MetadataAuthUsers, metadata)
			return err
		}},
		{"delete metadata", core.ActionAdmin, func() error {
			return service.DeleteMetadata(context.Background(), projectID, itemID, actorID, MetadataAuthUsers)
		}},
		{"list variables", core.ActionView, func() error { _, err := service.ListVariables(context.Background(), projectID, actorID); return err }},
		{"public environment", core.ActionEdit, func() error {
			_, err := service.PublicEnvironment(context.Background(), projectID, actorID, ScopePreview)
			return err
		}},
		{"set variable", core.ActionAdmin, func() error {
			_, err := service.SetVariable(context.Background(), projectID, actorID, EnvironmentVariableInput{Name: "TOKEN", Scope: ScopePreview, Value: "secret"})
			return err
		}},
		{"delete variable", core.ActionAdmin, func() error { return service.DeleteVariable(context.Background(), projectID, variableID, actorID) }},
		{"preview migration", core.ActionEdit, func() error {
			_, err := service.PreviewMigration(context.Background(), projectID, actorID, operation)
			return err
		}},
		{"apply migration", core.ActionAdmin, func() error {
			_, err := service.ApplyMigration(context.Background(), projectID, actorID, "confirm_abcdefghijklmnopqrstuvwxyz")
			return err
		}},
		{"connect", core.ActionAdmin, func() error {
			_, err := service.ConnectSupabase(context.Background(), projectID, actorID, SupabaseConnectionInput{Endpoint: "https://demo.supabase.co", Key: "test-key"})
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(access.actions)
			err := test.call()
			if !errors.Is(err, core.ErrForbidden) {
				t.Fatalf("expected RBAC denial, got %v", err)
			}
			if len(access.actions) != before+1 || access.actions[before] != test.action {
				t.Fatalf("authorization action=%v calls=%v", test.action, access.actions[before:])
			}
		})
	}
}

func TestVariableDTOIsMaskedAndFailedConnectionIsNotPersisted(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{public: map[string]string{"PUBLIC_ORIGIN": "https://preview.example", "INTERNAL_ORIGIN": "https://internal.example"}}
	access := &accessStub{}
	prober := &proberStub{result: SupabaseConnectionResult{
		OK: false, Endpoint: "https://demo.supabase.co", Status: 401,
		Message: "Supabase rejected the supplied key.",
	}}
	service, err := NewService(Dependencies{Repository: repository, Access: access, Prober: prober})
	if err != nil {
		t.Fatal(err)
	}
	projectID, actorID := uuid.NewString(), uuid.NewString()
	variable, err := service.SetVariable(context.Background(), projectID, actorID, EnvironmentVariableInput{
		Name: "api_token", Scope: ScopeProduction, Value: "do-not-return",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(variable)
	if string(encoded) == "" || json.Valid(encoded) == false || containsBytes(encoded, []byte("do-not-return")) {
		t.Fatalf("variable DTO leaked plaintext: %s", encoded)
	}
	if repository.setInput.Kind != VariableSecret || repository.setInput.Name != "API_TOKEN" {
		t.Fatalf("variable defaults were not normalized: %+v", repository.setInput)
	}
	public, err := service.PublicEnvironment(context.Background(), projectID, actorID, ScopePreview)
	if err != nil || public["PUBLIC_ORIGIN"] != "https://preview.example" || public["INTERNAL_ORIGIN"] != "" || access.actions[len(access.actions)-1] != core.ActionEdit {
		t.Fatalf("publish environment hook failed: public=%v err=%v actions=%v", public, err, access.actions)
	}
	result, err := service.ConnectSupabase(context.Background(), projectID, actorID, SupabaseConnectionInput{
		Endpoint: "https://demo.supabase.co", Key: "not-persisted",
	})
	if err != nil || result.OK || repository.saveCalls != 0 || prober.calls != 1 {
		t.Fatalf("failed connection handling result=%+v err=%v saves=%d", result, err, repository.saveCalls)
	}
}

func containsBytes(value, substring []byte) bool {
	return string(value) != "" && string(substring) != "" && jsonContains(value, substring)
}

func jsonContains(value, substring []byte) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if string(value[index:index+len(substring)]) == string(substring) {
			return true
		}
	}
	return false
}
