package dataruntime

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

const publicDataAPIBasePath = "/v1/public/data/deployments"

type PublicRuntimeRepository interface {
	ListPublicTablePolicies(context.Context, string) ([]PublicTablePolicy, error)
	GetPublicTablePolicy(context.Context, string, string) (PublicTablePolicy, error)
	PutPublicTablePolicy(context.Context, string, string, string, uint64, PublicTablePolicyInput) (PublicTablePolicy, error)
	DeletePublicTablePolicy(context.Context, string, string, string, uint64) error

	PreparePublicCapability(context.Context, PreparePublicCapabilityInput, string, []byte, []string, time.Time) (publicCapabilityRecord, error)
	ActivatePublicCapability(context.Context, string, string, string) (publicCapabilityRecord, error)
	RevokePublicCapability(context.Context, string, string, string) error
	RevokeDeploymentPublicCapabilities(context.Context, string, string) error
	GetActivePublicDeploymentRuntime(context.Context, string, string) (publicCapabilityRecord, error)
	FindPublicCapability(context.Context, string) (publicCapabilityRecord, error)
	PublicPreflightOrigins(context.Context, string) ([]string, error)
}

type PublicRuntimeDependencies struct {
	Data        Repository
	Runtime     PublicRuntimeRepository
	Access      ProjectAuthorizer
	Now         func() time.Time
	TokenSource func() (string, string, error)
}

type PublicRuntimeService struct {
	data        Repository
	runtime     PublicRuntimeRepository
	access      ProjectAuthorizer
	now         func() time.Time
	tokenSource func() (string, string, error)
}

var _ DeploymentPublicRuntimeProvisioner = (*PublicRuntimeService)(nil)

func NewPublicRuntimeService(dependencies PublicRuntimeDependencies) (*PublicRuntimeService, error) {
	if dependencies.Data == nil || dependencies.Runtime == nil || dependencies.Access == nil {
		return nil, errors.New("public data repository, runtime repository, and access control are required")
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.TokenSource == nil {
		dependencies.TokenSource = newPublicCapabilityToken
	}
	return &PublicRuntimeService{
		data: dependencies.Data, runtime: dependencies.Runtime, access: dependencies.Access,
		now: dependencies.Now, tokenSource: dependencies.TokenSource,
	}, nil
}

func (s *PublicRuntimeService) ListPolicies(ctx context.Context, projectID, actorID string) ([]PublicTablePolicy, error) {
	if err := s.authorizeProject(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	policies, err := s.runtime.ListPublicTablePolicies(ctx, projectID)
	if err != nil {
		return nil, err
	}
	tables, err := s.data.ListTables(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byTable := make(map[string]PublicTablePolicy, len(policies))
	for _, policy := range policies {
		policy.ETag = PublicTablePolicyETag(policy.ProjectID, policy.TableID, policy.Version)
		byTable[policy.TableID] = policy
	}
	result := make([]PublicTablePolicy, 0, len(tables))
	for _, table := range tables {
		policy, exists := byTable[table.ID]
		if !exists {
			policy = PublicTablePolicy{
				ProjectID: projectID, TableID: table.ID, TableName: table.Name,
				ReadableFields: []string{}, WritableFields: []string{}, Version: 0,
				ETag:      PublicTablePolicyETag(projectID, table.ID, 0),
				CreatedAt: table.CreatedAt, UpdatedAt: table.UpdatedAt,
			}
		}
		result = append(result, policy)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].TableName == result[right].TableName {
			return result[left].TableID < result[right].TableID
		}
		return result[left].TableName < result[right].TableName
	})
	return result, nil
}

func (s *PublicRuntimeService) PutPolicy(ctx context.Context, projectID, tableID, actorID string, expectedVersion uint64, input PublicTablePolicyInput) (PublicTablePolicy, error) {
	if err := validateUUID(projectID, "projectId"); err != nil {
		return PublicTablePolicy{}, err
	}
	if err := validateUUID(tableID, "tableId"); err != nil {
		return PublicTablePolicy{}, err
	}
	if err := s.authorizeProject(ctx, projectID, actorID, core.ActionAdmin); err != nil {
		return PublicTablePolicy{}, err
	}
	table, err := s.data.GetTable(ctx, projectID, tableID)
	if err != nil {
		return PublicTablePolicy{}, err
	}
	if err := ValidatePublicTablePolicy(&input, table); err != nil {
		return PublicTablePolicy{}, err
	}
	policy, err := s.runtime.PutPublicTablePolicy(ctx, projectID, tableID, actorID, expectedVersion, input)
	if err != nil {
		return PublicTablePolicy{}, err
	}
	policy.ETag = PublicTablePolicyETag(policy.ProjectID, policy.TableID, policy.Version)
	return policy, nil
}

func (s *PublicRuntimeService) DeletePolicy(ctx context.Context, projectID, tableID, actorID string, expectedVersion uint64) error {
	if err := validateUUID(projectID, "projectId"); err != nil {
		return err
	}
	if err := validateUUID(tableID, "tableId"); err != nil {
		return err
	}
	if err := s.authorizeProject(ctx, projectID, actorID, core.ActionAdmin); err != nil {
		return err
	}
	return s.runtime.DeletePublicTablePolicy(ctx, projectID, tableID, actorID, expectedVersion)
}

// PrepareDeploymentCapability creates a pending, one-time-readable capability.
// The currently active capability remains valid until ActivateDeploymentCapability
// succeeds after the provider has published the matching runtime overlay.
func (s *PublicRuntimeService) PrepareDeploymentCapability(ctx context.Context, input PreparePublicCapabilityInput) (PreparedPublicRuntimeConfig, error) {
	if err := validateUUID(input.ProjectID, "projectId"); err != nil {
		return PreparedPublicRuntimeConfig{}, err
	}
	if err := validateUUID(input.DeploymentID, "deploymentId"); err != nil {
		return PreparedPublicRuntimeConfig{}, err
	}
	if err := validateUUID(input.DeploymentVersionID, "deploymentVersionId"); err != nil {
		return PreparedPublicRuntimeConfig{}, err
	}
	if input.Environment != ScopePreview && input.Environment != ScopeProduction {
		return PreparedPublicRuntimeConfig{}, Invalid("environment", "environment must be preview or production")
	}
	origins, err := NormalizePublicOrigins(input.AllowedOrigins)
	if err != nil {
		return PreparedPublicRuntimeConfig{}, err
	}
	now := s.now().UTC()
	expiresAt := input.ExpiresAt.UTC()
	if input.ExpiresAt.IsZero() {
		ttl := DefaultProductionTTL
		if input.Environment == ScopePreview {
			ttl = DefaultPreviewCapabilityTTL
		}
		expiresAt = now.Add(ttl)
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(MaxPublicCapabilityTTL)) {
		return PreparedPublicRuntimeConfig{}, Invalid("expiresAt", "expiresAt must be in the future and no more than 366 days away")
	}
	capabilityID, token, err := s.tokenSource()
	if err != nil {
		return PreparedPublicRuntimeConfig{}, err
	}
	parsedID, err := parsePublicCapabilityToken(token)
	if err != nil || parsedID != capabilityID {
		return PreparedPublicRuntimeConfig{}, errors.New("public capability token source returned an invalid token")
	}
	digest := sha256.Sum256([]byte(token))
	record, err := s.runtime.PreparePublicCapability(ctx, input, capabilityID, digest[:], origins, expiresAt)
	if err != nil {
		return PreparedPublicRuntimeConfig{}, err
	}
	return PreparedPublicRuntimeConfig{
		APIBasePath: publicDataAPIBasePath, ProjectID: record.ProjectID,
		DeploymentID: record.DeploymentID, DeploymentVersionID: record.DeploymentVersionID,
		CapabilityID: record.ID, CapabilityToken: token,
		AllowedOrigins: append([]string(nil), record.AllowedOrigins...), ExpiresAt: record.ExpiresAt.UTC(),
	}, nil
}

func (s *PublicRuntimeService) ActivateDeploymentCapability(ctx context.Context, projectID, deploymentID, capabilityID string) (PublicDeploymentRuntime, error) {
	if err := validatePublicRuntimeIDs(projectID, deploymentID, capabilityID); err != nil {
		return PublicDeploymentRuntime{}, err
	}
	record, err := s.runtime.ActivatePublicCapability(ctx, projectID, deploymentID, capabilityID)
	if err != nil {
		return PublicDeploymentRuntime{}, err
	}
	return publicDeploymentRuntime(record), nil
}

func (s *PublicRuntimeService) RevokeDeploymentCapability(ctx context.Context, projectID, deploymentID, capabilityID string) error {
	if err := validatePublicRuntimeIDs(projectID, deploymentID, capabilityID); err != nil {
		return err
	}
	return s.runtime.RevokePublicCapability(ctx, projectID, deploymentID, capabilityID)
}

func (s *PublicRuntimeService) RevokeDeployment(ctx context.Context, projectID, deploymentID string) error {
	if err := validateUUID(projectID, "projectId"); err != nil {
		return err
	}
	if err := validateUUID(deploymentID, "deploymentId"); err != nil {
		return err
	}
	return s.runtime.RevokeDeploymentPublicCapabilities(ctx, projectID, deploymentID)
}

func (s *PublicRuntimeService) ActiveDeploymentRuntime(ctx context.Context, projectID, deploymentID string) (PublicDeploymentRuntime, error) {
	if err := validateUUID(projectID, "projectId"); err != nil {
		return PublicDeploymentRuntime{}, err
	}
	if err := validateUUID(deploymentID, "deploymentId"); err != nil {
		return PublicDeploymentRuntime{}, err
	}
	record, err := s.runtime.GetActivePublicDeploymentRuntime(ctx, projectID, deploymentID)
	if err != nil {
		return PublicDeploymentRuntime{}, err
	}
	return publicDeploymentRuntime(record), nil
}

func (s *PublicRuntimeService) ActiveDeploymentRuntimeForActor(ctx context.Context, projectID, deploymentID, actorID string) (PublicDeploymentRuntime, error) {
	if err := s.authorizeProject(ctx, projectID, actorID, core.ActionView); err != nil {
		return PublicDeploymentRuntime{}, err
	}
	return s.ActiveDeploymentRuntime(ctx, projectID, deploymentID)
}

func (s *PublicRuntimeService) RevokeDeploymentForActor(ctx context.Context, projectID, deploymentID, actorID string) error {
	if err := s.authorizeProject(ctx, projectID, actorID, core.ActionPublish); err != nil {
		return err
	}
	return s.RevokeDeployment(ctx, projectID, deploymentID)
}

func (s *PublicRuntimeService) Authenticate(ctx context.Context, deploymentID, token string) (PublicCapability, error) {
	if _, err := uuid.Parse(deploymentID); err != nil {
		return PublicCapability{}, publicCapabilityInvalid()
	}
	capabilityID, err := parsePublicCapabilityToken(token)
	if err != nil {
		return PublicCapability{}, publicCapabilityInvalid()
	}
	record, err := s.runtime.FindPublicCapability(ctx, capabilityID)
	if err != nil {
		return PublicCapability{}, publicCapabilityInvalid()
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	if subtle.ConstantTimeCompare(record.TokenDigest, digest[:]) != 1 || record.Status != "active" || record.DeploymentID != deploymentID || !record.ExpiresAt.After(s.now().UTC()) {
		return PublicCapability{}, publicCapabilityInvalid()
	}
	return PublicCapability{
		ID: record.ID, ProjectID: record.ProjectID, DeploymentID: record.DeploymentID,
		DeploymentVersionID: record.DeploymentVersionID,
		AllowedOrigins:      append([]string(nil), record.AllowedOrigins...),
		ExpiresAt:           record.ExpiresAt.UTC(), authenticated: true,
	}, nil
}

func (s *PublicRuntimeService) ValidateOrigin(capability PublicCapability, origin string) error {
	if !capability.authenticated {
		return publicCapabilityInvalid()
	}
	value := strings.TrimSpace(origin)
	if value == "" {
		return nil
	}
	normalized, err := NormalizePublicOrigins([]string{value})
	if err != nil {
		return NewError(CodePublicOriginDenied, http.StatusForbidden, "The request origin is not allowed for this deployment")
	}
	for _, allowed := range capability.AllowedOrigins {
		if subtle.ConstantTimeCompare([]byte(allowed), []byte(normalized[0])) == 1 {
			return nil
		}
	}
	return NewError(CodePublicOriginDenied, http.StatusForbidden, "The request origin is not allowed for this deployment")
}

func (s *PublicRuntimeService) PreflightOrigins(ctx context.Context, deploymentID string) ([]string, error) {
	if _, err := uuid.Parse(deploymentID); err != nil {
		return nil, NotFound("Deployment")
	}
	return s.runtime.PublicPreflightOrigins(ctx, deploymentID)
}

func (s *PublicRuntimeService) ListPublicTables(ctx context.Context, capability PublicCapability) ([]PublicTable, error) {
	if err := validateCapability(capability); err != nil {
		return nil, err
	}
	policies, err := s.runtime.ListPublicTablePolicies(ctx, capability.ProjectID)
	if err != nil {
		return nil, err
	}
	result := make([]PublicTable, 0, len(policies))
	for _, policy := range policies {
		if !policy.AllowRead && !policy.AllowCreate && !policy.AllowUpdate && !policy.AllowDelete {
			continue
		}
		table, err := s.data.GetTable(ctx, capability.ProjectID, policy.TableID)
		if err != nil {
			return nil, err
		}
		result = append(result, publicTableFromPolicy(table, policy))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (s *PublicRuntimeService) GetPublicTable(ctx context.Context, capability PublicCapability, tableID string) (PublicTable, error) {
	policy, err := s.policy(ctx, capability, tableID, "")
	if err != nil {
		return PublicTable{}, err
	}
	table, err := s.data.GetTable(ctx, capability.ProjectID, tableID)
	if err != nil {
		return PublicTable{}, err
	}
	return publicTableFromPolicy(table, policy), nil
}

func (s *PublicRuntimeService) ListPublicRecords(ctx context.Context, capability PublicCapability, tableID string, limit, offset int) (RecordPage, error) {
	policy, err := s.policy(ctx, capability, tableID, PublicOperationRead)
	if err != nil {
		return RecordPage{}, err
	}
	if limit < 1 || limit > 100 {
		return RecordPage{}, Invalid("limit", "limit must be between 1 and 100")
	}
	if offset < 0 || offset > MaxRecordsPerTable {
		return RecordPage{}, Invalid("offset", fmt.Sprintf("offset must be between 0 and %d", MaxRecordsPerTable))
	}
	page, err := s.data.ListRecords(ctx, capability.ProjectID, tableID, limit, offset)
	if err != nil {
		return RecordPage{}, err
	}
	for index := range page.Records {
		page.Records[index] = publicRecordValues(page.Records[index], policy.ReadableFields)
	}
	return page, nil
}

func (s *PublicRuntimeService) GetPublicRecord(ctx context.Context, capability PublicCapability, tableID, recordID string) (Record, error) {
	policy, err := s.policy(ctx, capability, tableID, PublicOperationRead)
	if err != nil {
		return Record{}, err
	}
	if err := validateUUID(recordID, "recordId"); err != nil {
		return Record{}, err
	}
	record, err := s.data.GetRecord(ctx, capability.ProjectID, tableID, recordID)
	if err != nil {
		return Record{}, err
	}
	return publicRecordValues(record, policy.ReadableFields), nil
}

func (s *PublicRuntimeService) CreatePublicRecord(ctx context.Context, capability PublicCapability, tableID, requestID string, input RecordInput) (Record, error) {
	policy, err := s.policy(ctx, capability, tableID, PublicOperationCreate)
	if err != nil {
		return Record{}, err
	}
	if err := validatePublicWrite(input, policy.WritableFields); err != nil {
		return Record{}, err
	}
	record, err := s.data.CreateRecord(ctx, capability.ProjectID, tableID, MutationContext{
		RequestID: requestID, PublicDeploymentID: capability.DeploymentID, PublicCapabilityID: capability.ID,
	}, input)
	if err != nil {
		return Record{}, err
	}
	return publicRecordValues(record, policy.ReadableFields), nil
}

func (s *PublicRuntimeService) UpdatePublicRecord(ctx context.Context, capability PublicCapability, tableID, recordID, requestID string, input RecordInput) (Record, error) {
	policy, err := s.policy(ctx, capability, tableID, PublicOperationUpdate)
	if err != nil {
		return Record{}, err
	}
	if err := validateUUID(recordID, "recordId"); err != nil {
		return Record{}, err
	}
	if err := validatePublicWrite(input, policy.WritableFields); err != nil {
		return Record{}, err
	}
	record, err := s.data.UpdateRecord(ctx, capability.ProjectID, tableID, recordID, MutationContext{
		RequestID: requestID, PublicDeploymentID: capability.DeploymentID, PublicCapabilityID: capability.ID,
	}, input)
	if err != nil {
		return Record{}, err
	}
	return publicRecordValues(record, policy.ReadableFields), nil
}

func (s *PublicRuntimeService) DeletePublicRecord(ctx context.Context, capability PublicCapability, tableID, recordID, requestID string) error {
	if _, err := s.policy(ctx, capability, tableID, PublicOperationDelete); err != nil {
		return err
	}
	if err := validateUUID(recordID, "recordId"); err != nil {
		return err
	}
	return s.data.DeleteRecord(ctx, capability.ProjectID, tableID, recordID, MutationContext{
		RequestID: requestID, PublicDeploymentID: capability.DeploymentID, PublicCapabilityID: capability.ID,
	})
}

func (s *PublicRuntimeService) policy(ctx context.Context, capability PublicCapability, tableID string, operation PublicDataOperation) (PublicTablePolicy, error) {
	if err := validateCapability(capability); err != nil {
		return PublicTablePolicy{}, err
	}
	if err := validateUUID(tableID, "tableId"); err != nil {
		return PublicTablePolicy{}, err
	}
	policy, err := s.runtime.GetPublicTablePolicy(ctx, capability.ProjectID, tableID)
	if err != nil {
		if runtimeError, ok := AsRuntimeError(err); ok && runtimeError.Code == CodeNotFound {
			return PublicTablePolicy{}, publicPolicyDenied()
		}
		return PublicTablePolicy{}, err
	}
	if operation != "" && !policy.permits(operation) {
		return PublicTablePolicy{}, publicPolicyDenied()
	}
	return policy, nil
}

func (s *PublicRuntimeService) authorizeProject(ctx context.Context, projectID, actorID string, action core.Action) error {
	if err := validateUUID(projectID, "projectId"); err != nil {
		return err
	}
	if _, err := s.access.Authorize(ctx, projectID, actorID, action); err != nil {
		return err
	}
	return nil
}

func validatePublicWrite(input RecordInput, fields []string) error {
	if err := ValidateRecordInput(&input); err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for field := range input.Values {
		if _, ok := allowed[field]; !ok {
			return Invalid("values."+field, "field is not writable through the public data API")
		}
	}
	return nil
}

func validateCapability(capability PublicCapability) error {
	if !capability.authenticated || capability.ID == "" || capability.ProjectID == "" || capability.DeploymentID == "" {
		return publicCapabilityInvalid()
	}
	return nil
}

func validatePublicRuntimeIDs(projectID, deploymentID, capabilityID string) error {
	if err := validateUUID(projectID, "projectId"); err != nil {
		return err
	}
	if err := validateUUID(deploymentID, "deploymentId"); err != nil {
		return err
	}
	return validateUUID(capabilityID, "capabilityId")
}

func publicTableFromPolicy(table Table, policy PublicTablePolicy) PublicTable {
	visibleNames := make(map[string]struct{}, len(policy.ReadableFields)+len(policy.WritableFields))
	for _, field := range policy.ReadableFields {
		visibleNames[field] = struct{}{}
	}
	for _, field := range policy.WritableFields {
		visibleNames[field] = struct{}{}
	}
	columns := make([]Column, 0, len(visibleNames))
	for _, column := range table.Columns {
		if _, ok := visibleNames[column.Name]; ok {
			columns = append(columns, column)
		}
	}
	return PublicTable{
		ID: table.ID, Name: table.Name, Columns: columns,
		ReadableFields: append([]string(nil), policy.ReadableFields...),
		WritableFields: append([]string(nil), policy.WritableFields...),
		Permissions: PublicTablePermission{
			Read: policy.AllowRead, Create: policy.AllowCreate,
			Update: policy.AllowUpdate, Delete: policy.AllowDelete,
		},
	}
}

func publicDeploymentRuntime(record publicCapabilityRecord) PublicDeploymentRuntime {
	return PublicDeploymentRuntime{
		APIBasePath: publicDataAPIBasePath, ProjectID: record.ProjectID,
		DeploymentID: record.DeploymentID, DeploymentVersionID: record.DeploymentVersionID,
		CapabilityID: record.ID, AllowedOrigins: append([]string(nil), record.AllowedOrigins...),
		ExpiresAt: record.ExpiresAt.UTC(), ActivatedAt: record.ActivatedAt,
	}
}

func publicCapabilityInvalid() error {
	return NewError(CodePublicCapabilityInvalid, http.StatusUnauthorized, "The public data capability is invalid or expired")
}

func publicPolicyDenied() error {
	return NewError(CodePublicPolicyDenied, http.StatusForbidden, "This table operation is not enabled for anonymous application access")
}
