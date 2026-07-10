package dataruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

type ProjectAuthorizer interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type ConnectionProber interface {
	Probe(context.Context, SupabaseConnectionInput) (SupabaseConnectionResult, error)
}

type MutationContext struct {
	ActorID            string
	RequestID          string
	PublicDeploymentID string
	PublicCapabilityID string
}

type Repository interface {
	Snapshot(context.Context, string) (ProjectSnapshot, error)
	ListTables(context.Context, string) ([]Table, error)
	GetTable(context.Context, string, string) (Table, error)
	CreateTable(context.Context, string, MutationContext, TableInput) (Table, error)
	RenameTable(context.Context, string, string, MutationContext, string) (Table, error)
	DeleteTable(context.Context, string, string, MutationContext) error

	ListRecords(context.Context, string, string, int, int) (RecordPage, error)
	GetRecord(context.Context, string, string, string) (Record, error)
	CreateRecord(context.Context, string, string, MutationContext, RecordInput) (Record, error)
	UpdateRecord(context.Context, string, string, string, MutationContext, RecordInput) (Record, error)
	DeleteRecord(context.Context, string, string, string, MutationContext) error

	ListMetadata(context.Context, string, MetadataKind) ([]MetadataItem, error)
	GetMetadata(context.Context, string, MetadataKind, string) (MetadataItem, error)
	CreateMetadata(context.Context, string, MetadataKind, MutationContext, json.RawMessage, string) (MetadataItem, error)
	UpdateMetadata(context.Context, string, MetadataKind, string, MutationContext, map[string]json.RawMessage) (MetadataItem, error)
	DeleteMetadata(context.Context, string, MetadataKind, string, MutationContext) error

	ListVariables(context.Context, string) ([]EnvironmentVariable, error)
	PublicEnvironment(context.Context, string, EnvironmentScope) (map[string]string, error)
	SetVariable(context.Context, string, MutationContext, EnvironmentVariableInput) (EnvironmentVariable, error)
	DeleteVariable(context.Context, string, string, MutationContext) error

	PreviewMigration(context.Context, string, MutationContext, []MigrationOperation) (MigrationPreview, error)
	ApplyMigration(context.Context, string, MutationContext, string) (ApplyMigrationResult, error)
	SaveConnection(context.Context, string, MutationContext, SupabaseConnectionResult) (ConnectionMetadata, error)
}

type Dependencies struct {
	Repository Repository
	Access     ProjectAuthorizer
	Prober     ConnectionProber
}

type Service struct {
	repository Repository
	access     ProjectAuthorizer
	prober     ConnectionProber
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.Access == nil || dependencies.Prober == nil {
		return nil, errors.New("data runtime repository, access control, and connection prober are required")
	}
	return &Service{
		repository: dependencies.Repository,
		access:     dependencies.Access,
		prober:     dependencies.Prober,
	}, nil
}

func (s *Service) Snapshot(ctx context.Context, projectID, actorID string) (ProjectSnapshot, error) {
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return ProjectSnapshot{}, err
	}
	return s.repository.Snapshot(ctx, projectID)
}

func (s *Service) ListTables(ctx context.Context, projectID, actorID string) ([]Table, error) {
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	return s.repository.ListTables(ctx, projectID)
}

func (s *Service) GetTable(ctx context.Context, projectID, tableID, actorID string) (Table, error) {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return Table{}, err
	}
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return Table{}, err
	}
	return s.repository.GetTable(ctx, projectID, tableID)
}

func (s *Service) CreateTable(ctx context.Context, projectID, actorID string, input TableInput) (Table, error) {
	if err := ValidateTableInput(&input); err != nil {
		return Table{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return Table{}, err
	}
	return s.repository.CreateTable(ctx, projectID, mutation, input)
}

func (s *Service) RenameTable(ctx context.Context, projectID, tableID, actorID, name string) (Table, error) {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return Table{}, err
	}
	normalized, err := normalizeDatabaseIdentifier(name, "name")
	if err != nil {
		return Table{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return Table{}, err
	}
	return s.repository.RenameTable(ctx, projectID, tableID, mutation, normalized)
}

func (s *Service) DeleteTable(ctx context.Context, projectID, tableID, actorID string) error {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return err
	}
	return s.repository.DeleteTable(ctx, projectID, tableID, mutation)
}

func (s *Service) ListRecords(ctx context.Context, projectID, tableID, actorID string, limit, offset int) (RecordPage, error) {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return RecordPage{}, err
	}
	if limit < 1 || limit > 100 {
		return RecordPage{}, Invalid("limit", "limit must be between 1 and 100")
	}
	if offset < 0 || offset > MaxRecordsPerTable {
		return RecordPage{}, Invalid("offset", fmt.Sprintf("offset must be between 0 and %d", MaxRecordsPerTable))
	}
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return RecordPage{}, err
	}
	return s.repository.ListRecords(ctx, projectID, tableID, limit, offset)
}

func (s *Service) GetRecord(ctx context.Context, projectID, tableID, recordID, actorID string) (Record, error) {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return Record{}, err
	}
	if err := validateUUID(recordID, "recordId"); err != nil {
		return Record{}, err
	}
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return Record{}, err
	}
	return s.repository.GetRecord(ctx, projectID, tableID, recordID)
}

func (s *Service) CreateRecord(ctx context.Context, projectID, tableID, actorID string, input RecordInput) (Record, error) {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return Record{}, err
	}
	if err := ValidateRecordInput(&input); err != nil {
		return Record{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return Record{}, err
	}
	return s.repository.CreateRecord(ctx, projectID, tableID, mutation, input)
}

func (s *Service) UpdateRecord(ctx context.Context, projectID, tableID, recordID, actorID string, input RecordInput) (Record, error) {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return Record{}, err
	}
	if err := validateUUID(recordID, "recordId"); err != nil {
		return Record{}, err
	}
	if err := ValidateRecordInput(&input); err != nil {
		return Record{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return Record{}, err
	}
	return s.repository.UpdateRecord(ctx, projectID, tableID, recordID, mutation, input)
}

func (s *Service) DeleteRecord(ctx context.Context, projectID, tableID, recordID, actorID string) error {
	if err := validateUUID(tableID, "tableId"); err != nil {
		return err
	}
	if err := validateUUID(recordID, "recordId"); err != nil {
		return err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return err
	}
	return s.repository.DeleteRecord(ctx, projectID, tableID, recordID, mutation)
}

func (s *Service) ListMetadata(ctx context.Context, projectID, actorID string, kind MetadataKind) ([]MetadataItem, error) {
	if _, err := ParseMetadataKind(string(kind)); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	return s.repository.ListMetadata(ctx, projectID, kind)
}

func (s *Service) GetMetadata(ctx context.Context, projectID, itemID, actorID string, kind MetadataKind) (MetadataItem, error) {
	if _, err := ParseMetadataKind(string(kind)); err != nil {
		return MetadataItem{}, err
	}
	if err := validateUUID(itemID, "itemId"); err != nil {
		return MetadataItem{}, err
	}
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return MetadataItem{}, err
	}
	return s.repository.GetMetadata(ctx, projectID, kind, itemID)
}

func (s *Service) CreateMetadata(ctx context.Context, projectID, actorID string, kind MetadataKind, patch map[string]json.RawMessage) (MetadataItem, error) {
	if patch == nil {
		return MetadataItem{}, Invalid("metadata", "metadata item must be a JSON object")
	}
	payload, uniqueKey, err := NormalizeMetadataPatch(kind, patch, nil)
	if err != nil {
		return MetadataItem{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		return MetadataItem{}, err
	}
	return s.repository.CreateMetadata(ctx, projectID, kind, mutation, payload, uniqueKey)
}

func (s *Service) UpdateMetadata(ctx context.Context, projectID, itemID, actorID string, kind MetadataKind, patch map[string]json.RawMessage) (MetadataItem, error) {
	if patch == nil {
		return MetadataItem{}, Invalid("metadata", "metadata item must be a JSON object")
	}
	if err := validateUUID(itemID, "itemId"); err != nil {
		return MetadataItem{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		return MetadataItem{}, err
	}
	return s.repository.UpdateMetadata(ctx, projectID, kind, itemID, mutation, patch)
}

func (s *Service) DeleteMetadata(ctx context.Context, projectID, itemID, actorID string, kind MetadataKind) error {
	if _, err := ParseMetadataKind(string(kind)); err != nil {
		return err
	}
	if err := validateUUID(itemID, "itemId"); err != nil {
		return err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		return err
	}
	return s.repository.DeleteMetadata(ctx, projectID, kind, itemID, mutation)
}

func (s *Service) ListVariables(ctx context.Context, projectID, actorID string) ([]EnvironmentVariable, error) {
	if err := s.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	return s.repository.ListVariables(ctx, projectID)
}

// PublicEnvironment is an internal publish-time capability, not an HTTP data
// route. It can return only values explicitly marked plain; the repository has
// no corresponding operation capable of decrypting a secret variable.
func (s *Service) PublicEnvironment(ctx context.Context, projectID, actorID string, scope EnvironmentScope) (map[string]string, error) {
	if scope != ScopePreview && scope != ScopeProduction {
		return nil, Invalid("scope", "public environment scope must be preview or production")
	}
	action := core.ActionPublish
	if scope == ScopePreview {
		action = core.ActionEdit
	}
	if err := s.authorize(ctx, projectID, actorID, action); err != nil {
		return nil, err
	}
	values, err := s.repository.PublicEnvironment(ctx, projectID, scope)
	if err != nil {
		return nil, err
	}
	public := make(map[string]string, len(values))
	for name, value := range values {
		if IsPublicEnvironmentName(name) {
			public[name] = value
		}
	}
	return public, nil
}

func (s *Service) SetVariable(ctx context.Context, projectID, actorID string, input EnvironmentVariableInput) (EnvironmentVariable, error) {
	if err := ValidateEnvironmentVariable(&input); err != nil {
		return EnvironmentVariable{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		return EnvironmentVariable{}, err
	}
	return s.repository.SetVariable(ctx, projectID, mutation, input)
}

func (s *Service) DeleteVariable(ctx context.Context, projectID, variableID, actorID string) error {
	if err := validateUUID(variableID, "variableId"); err != nil {
		return err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		return err
	}
	return s.repository.DeleteVariable(ctx, projectID, variableID, mutation)
}

func (s *Service) PreviewMigration(ctx context.Context, projectID, actorID string, operations []MigrationOperation) (MigrationPreview, error) {
	if err := ValidateMigrationOperations(operations); err != nil {
		return MigrationPreview{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return MigrationPreview{}, err
	}
	return s.repository.PreviewMigration(ctx, projectID, mutation, operations)
}

func (s *Service) ApplyMigration(ctx context.Context, projectID, actorID, confirmationToken string) (ApplyMigrationResult, error) {
	confirmationToken = strings.TrimSpace(confirmationToken)
	if err := ValidateConfirmationToken(confirmationToken); err != nil {
		return ApplyMigrationResult{}, err
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		return ApplyMigrationResult{}, err
	}
	return s.repository.ApplyMigration(ctx, projectID, mutation, confirmationToken)
}

func (s *Service) ConnectSupabase(ctx context.Context, projectID, actorID string, input SupabaseConnectionInput) (SupabaseConnectionResult, error) {
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if input.Endpoint == "" || len(input.Endpoint) > 500 {
		return SupabaseConnectionResult{}, Invalid("endpoint", "endpoint must contain between 1 and 500 characters")
	}
	if input.Key == "" || len(input.Key) > 8_000 {
		return SupabaseConnectionResult{}, Invalid("key", "key must contain between 1 and 8000 characters")
	}
	mutation, err := s.authorizeMutation(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		return SupabaseConnectionResult{}, err
	}
	result, err := s.prober.Probe(ctx, input)
	if err != nil {
		return SupabaseConnectionResult{}, err
	}
	if result.Message == "" || len(result.Message) > 500 || strings.Contains(result.Message, input.Key) {
		if result.OK {
			result.Message = "Supabase REST connection succeeded."
		} else {
			result.Message = "Supabase REST endpoint returned an error."
		}
	}
	if result.OK {
		expected, normalizeErr := NormalizeSupabaseEndpoint(input.Endpoint)
		if normalizeErr != nil {
			return SupabaseConnectionResult{}, normalizeErr
		}
		expectedOrigin := expected.Scheme + "://" + expected.Host
		if result.Endpoint != expectedOrigin {
			return SupabaseConnectionResult{}, NewError(CodeConnectionFailed, 502, "Supabase connection result did not match the requested endpoint")
		}
		if err := validateSuccessfulConnectionResult(&result); err != nil {
			return SupabaseConnectionResult{}, err
		}
		if _, err := s.repository.SaveConnection(ctx, projectID, mutation, result); err != nil {
			return SupabaseConnectionResult{}, err
		}
	}
	return result, nil
}

func validateSuccessfulConnectionResult(result *SupabaseConnectionResult) error {
	if result == nil || !result.OK || result.Status < 200 || result.Status >= 300 || result.LatencyMS < 0 || result.LatencyMS > 600_000 {
		return NewError(CodeConnectionFailed, 502, "Supabase connection result was invalid")
	}
	endpoint, err := NormalizeSupabaseEndpoint(result.Endpoint)
	if err != nil {
		return NewError(CodeConnectionFailed, 502, "Supabase connection result contained an invalid endpoint")
	}
	result.Endpoint = endpoint.Scheme + "://" + endpoint.Host
	if len(result.SchemaTables) > 256 {
		return NewError(CodeConnectionFailed, 502, "Supabase schema summary was invalid")
	}
	unique := make(map[string]struct{}, len(result.SchemaTables))
	for _, name := range result.SchemaTables {
		if !schemaTablePattern.MatchString(name) {
			return NewError(CodeConnectionFailed, 502, "Supabase schema summary was invalid")
		}
		unique[name] = struct{}{}
	}
	result.SchemaTables = result.SchemaTables[:0]
	for name := range unique {
		result.SchemaTables = append(result.SchemaTables, name)
	}
	sort.Strings(result.SchemaTables)
	return nil
}

func (s *Service) authorize(ctx context.Context, projectID, actorID string, action core.Action) error {
	if err := validateUUID(projectID, "projectId"); err != nil {
		return err
	}
	if err := validateUUID(actorID, "actorId"); err != nil {
		return err
	}
	_, err := s.access.Authorize(ctx, projectID, actorID, action)
	return err
}

func (s *Service) authorizeMutation(ctx context.Context, projectID, actorID string, action core.Action) (MutationContext, error) {
	if err := s.authorize(ctx, projectID, actorID, action); err != nil {
		return MutationContext{}, err
	}
	return MutationContext{ActorID: actorID, RequestID: core.RequestIDFromContext(ctx)}, nil
}

func validateUUID(value, field string) error {
	if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
		return Invalid(field, field+" must be a UUID")
	}
	return nil
}
