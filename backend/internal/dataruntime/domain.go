package dataruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxRequestBytes                 = 1_000_000
	MaxTablesPerProject             = 64
	MaxColumnsPerTable              = 64
	MaxRecordsPerTable              = 10_000
	MaxRecordBytes                  = 64_000
	MaxMetadataBytes                = 32_000
	MaxVariableValueBytes           = 16_000
	MaxVariablesPerProject          = 100
	MaxMigrationOperations          = 40
	MaxMetadataItemsPerKind         = 1_000
	MaxPendingMigrationsPerProject  = 25
	DefaultMigrationConfirmationTTL = 10 * time.Minute
)

type ErrorCode string

const (
	CodeInvalidRequest       ErrorCode = "invalid_request"
	CodeRequestTooLarge      ErrorCode = "request_too_large"
	CodeNotFound             ErrorCode = "not_found"
	CodeConflict             ErrorCode = "conflict"
	CodePreconditionFailed   ErrorCode = "etag_mismatch"
	CodeConfirmationRequired ErrorCode = "confirmation_required"
	CodeConfirmationExpired  ErrorCode = "confirmation_expired"
	CodeConnectionFailed     ErrorCode = "connection_failed"
	CodeUnsafeEndpoint       ErrorCode = "unsafe_endpoint"
	CodeInternal             ErrorCode = "internal_error"
)

type RuntimeError struct {
	Code    ErrorCode
	Message string
	Status  int
	Fields  map[string][]string
	Cause   error
}

func (e *RuntimeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *RuntimeError) Unwrap() error { return e.Cause }

func NewError(code ErrorCode, status int, message string) *RuntimeError {
	return &RuntimeError{Code: code, Status: status, Message: message}
}

func Invalid(field, message string) *RuntimeError {
	return &RuntimeError{
		Code: CodeInvalidRequest, Status: http.StatusBadRequest, Message: message,
		Fields: map[string][]string{field: {message}},
	}
}

func NotFound(resource string) *RuntimeError {
	return NewError(CodeNotFound, http.StatusNotFound, resource+" was not found")
}

func Conflict(message string) *RuntimeError {
	return NewError(CodeConflict, http.StatusConflict, message)
}

func PreconditionFailed(message string) *RuntimeError {
	return NewError(CodePreconditionFailed, http.StatusPreconditionFailed, message)
}

func AsRuntimeError(err error) (*RuntimeError, bool) {
	var target *RuntimeError
	ok := errors.As(err, &target)
	return target, ok
}

type ColumnType string

const (
	ColumnText    ColumnType = "text"
	ColumnNumber  ColumnType = "number"
	ColumnBoolean ColumnType = "boolean"
	ColumnDate    ColumnType = "date"
	ColumnJSON    ColumnType = "json"
)

type ColumnInput struct {
	Name         string          `json:"name"`
	Type         ColumnType      `json:"type"`
	Required     bool            `json:"required,omitempty"`
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
}

type Column struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Type         ColumnType      `json:"type"`
	Required     bool            `json:"required"`
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
}

type TableInput struct {
	Name    string        `json:"name"`
	Columns []ColumnInput `json:"columns,omitempty"`
}

type Table struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Columns     []Column  `json:"columns"`
	RecordCount int64     `json:"recordCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type RecordInput struct {
	Values map[string]json.RawMessage `json:"values"`
}

type Record struct {
	ID        string                     `json:"id"`
	Values    map[string]json.RawMessage `json:"values"`
	CreatedAt time.Time                  `json:"createdAt"`
	UpdatedAt time.Time                  `json:"updatedAt"`
}

type RecordPage struct {
	Records []Record `json:"records"`
	Total   int64    `json:"total"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
}

type MetadataKind string

const (
	MetadataAuthUsers       MetadataKind = "auth-users"
	MetadataStorageObjects  MetadataKind = "storage-objects"
	MetadataServerFunctions MetadataKind = "server-functions"
)

// MetadataItem keeps the polymorphic, frontend-compatible item flat while the
// repository stores only the strictly validated kind-specific payload.
type MetadataItem struct {
	ID        string          `json:"-"`
	Kind      MetadataKind    `json:"-"`
	Payload   json.RawMessage `json:"-"`
	CreatedAt time.Time       `json:"-"`
	UpdatedAt time.Time       `json:"-"`
}

func (m MetadataItem) MarshalJSON() ([]byte, error) {
	object := map[string]any{}
	if len(m.Payload) > 0 {
		if err := json.Unmarshal(m.Payload, &object); err != nil {
			return nil, err
		}
	}
	object["id"] = m.ID
	object["createdAt"] = m.CreatedAt
	object["updatedAt"] = m.UpdatedAt
	return json.Marshal(object)
}

type EnvironmentScope string
type EnvironmentVariableKind string

const (
	ScopeDevelopment EnvironmentScope        = "development"
	ScopePreview     EnvironmentScope        = "preview"
	ScopeProduction  EnvironmentScope        = "production"
	VariablePlain    EnvironmentVariableKind = "plain"
	VariableSecret   EnvironmentVariableKind = "secret"
)

type EnvironmentVariableInput struct {
	Name  string                  `json:"name"`
	Scope EnvironmentScope        `json:"scope"`
	Kind  EnvironmentVariableKind `json:"kind,omitempty"`
	Value string                  `json:"value"`
}

type EnvironmentVariable struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Scope       EnvironmentScope        `json:"scope"`
	Kind        EnvironmentVariableKind `json:"kind"`
	MaskedValue string                  `json:"maskedValue"`
	ValueBytes  int                     `json:"valueBytes"`
	CreatedAt   time.Time               `json:"createdAt"`
	UpdatedAt   time.Time               `json:"updatedAt"`
}

type MigrationOperationType string

const (
	MigrationCreateTable  MigrationOperationType = "create-table"
	MigrationRenameTable  MigrationOperationType = "rename-table"
	MigrationDropTable    MigrationOperationType = "drop-table"
	MigrationAddColumn    MigrationOperationType = "add-column"
	MigrationRenameColumn MigrationOperationType = "rename-column"
	MigrationDropColumn   MigrationOperationType = "drop-column"
)

type MigrationOperation struct {
	Type     MigrationOperationType `json:"type"`
	Table    *TableInput            `json:"table,omitempty"`
	TableID  string                 `json:"tableId,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Column   *ColumnInput           `json:"column,omitempty"`
	ColumnID string                 `json:"columnId,omitempty"`
}

type MigrationPreviewInput struct {
	Operations []MigrationOperation `json:"operations"`
}

type MigrationChange struct {
	Operation   MigrationOperationType `json:"operation"`
	Summary     string                 `json:"summary"`
	Destructive bool                   `json:"destructive"`
}

type MigrationPreview struct {
	ID                string            `json:"id"`
	ProjectID         string            `json:"projectId"`
	ConfirmationToken string            `json:"confirmationToken"`
	ExpiresAt         time.Time         `json:"expiresAt"`
	Changes           []MigrationChange `json:"changes"`
	ResultingTables   []Table           `json:"resultingTables"`
}

type AppliedMigration struct {
	ID        string            `json:"id"`
	PreviewID string            `json:"previewId"`
	AppliedAt time.Time         `json:"appliedAt"`
	Changes   []MigrationChange `json:"changes"`
}

type ApplyMigrationInput struct {
	ConfirmationToken string `json:"confirmationToken"`
}

type AuditEvent struct {
	ID         string                     `json:"id"`
	Action     string                     `json:"action"`
	Resource   string                     `json:"resource"`
	ResourceID string                     `json:"resourceId,omitempty"`
	CreatedAt  time.Time                  `json:"createdAt"`
	Details    map[string]json.RawMessage `json:"details,omitempty"`
}

type ConnectionMetadata struct {
	Provider     string    `json:"provider"`
	Endpoint     string    `json:"endpoint"`
	Status       string    `json:"status"`
	ConnectedAt  time.Time `json:"connectedAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	HTTPStatus   int       `json:"httpStatus"`
	LatencyMS    int64     `json:"latencyMs"`
	SchemaTables []string  `json:"schemaTables"`
}

type SupabaseConnectionInput struct {
	Endpoint string `json:"endpoint"`
	Key      string `json:"key"`
}

type SupabaseConnectionResult struct {
	OK           bool     `json:"ok"`
	Endpoint     string   `json:"endpoint"`
	LatencyMS    int64    `json:"latencyMs"`
	Status       int      `json:"status"`
	Message      string   `json:"message"`
	SchemaTables []string `json:"schemaTables,omitempty"`
}

type ProjectSnapshot struct {
	ProjectID       string                `json:"projectId"`
	Tables          []Table               `json:"tables"`
	AuthUsers       []MetadataItem        `json:"authUsers"`
	StorageObjects  []MetadataItem        `json:"storageObjects"`
	ServerFunctions []MetadataItem        `json:"serverFunctions"`
	Variables       []EnvironmentVariable `json:"variables"`
	Migrations      []AppliedMigration    `json:"migrations"`
	Audit           []AuditEvent          `json:"audit"`
	Connection      *ConnectionMetadata   `json:"connection,omitempty"`
	UpdatedAt       time.Time             `json:"updatedAt"`
}

type ApplyMigrationResult struct {
	Migration AppliedMigration `json:"migration"`
	Tables    []Table          `json:"tables"`
	Project   ProjectSnapshot  `json:"project"`
}

var (
	databaseIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	variableNamePattern       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	publicVariableNamePattern = regexp.MustCompile(`^(?:PUBLIC_|NEXT_PUBLIC_|VITE_|REACT_APP_)[A-Z][A-Z0-9_]{0,127}$`)
	confirmationTokenPattern  = regexp.MustCompile(`^confirm_[a-zA-Z0-9_-]{20,140}$`)
	emailPattern              = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	rolePattern               = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
	checksumPattern           = regexp.MustCompile(`^[a-zA-Z0-9:+/=_-]+$`)
	contentTypePattern        = regexp.MustCompile(`^[\w.+-]+/[\w.+-]+(?:;\s*charset=[\w-]+)?$`)
	schemaTablePattern        = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,62}$`)
	sensitiveKeyPattern       = regexp.MustCompile(`(?i)(?:password|passwd|authorization|api_?key|private_?key|secret|token|credential)`)
)

func ValidateTableInput(input *TableInput) error {
	if input == nil {
		return Invalid("table", "table must be an object")
	}
	name, err := normalizeDatabaseIdentifier(input.Name, "name")
	if err != nil {
		return err
	}
	input.Name = name
	if len(input.Columns) > MaxColumnsPerTable {
		return Invalid("columns", fmt.Sprintf("columns may contain at most %d items", MaxColumnsPerTable))
	}
	names := make(map[string]struct{}, len(input.Columns))
	for index := range input.Columns {
		if err := ValidateColumnInput(&input.Columns[index], fmt.Sprintf("columns[%d]", index)); err != nil {
			return err
		}
		if _, duplicate := names[input.Columns[index].Name]; duplicate {
			return Invalid("columns", "columns contains duplicate names")
		}
		names[input.Columns[index].Name] = struct{}{}
	}
	return nil
}

func ValidateColumnInput(input *ColumnInput, field string) error {
	if input == nil {
		return Invalid(field, field+" must be an object")
	}
	name, err := normalizeDatabaseIdentifier(input.Name, field+".name")
	if err != nil {
		return err
	}
	input.Name = name
	switch input.Type {
	case ColumnText, ColumnNumber, ColumnBoolean, ColumnDate, ColumnJSON:
	default:
		return Invalid(field+".type", field+".type must be text, number, boolean, date, or json")
	}
	if len(input.DefaultValue) > 0 {
		value, err := validateJSONValue(input.DefaultValue, field+".defaultValue", 8_000)
		if err != nil {
			return err
		}
		if !valueMatchesColumn(value, input.Type) {
			return Invalid(field+".defaultValue", field+".defaultValue does not match column type "+string(input.Type))
		}
		if input.Required && value == nil {
			return Invalid(field+".defaultValue", field+".defaultValue cannot be null when the column is required")
		}
	}
	return nil
}

func ValidateRecordInput(input *RecordInput) error {
	if input == nil || input.Values == nil {
		return Invalid("values", "values must be an object")
	}
	encoded, err := json.Marshal(input.Values)
	if err != nil || len(encoded) > MaxRecordBytes {
		return Invalid("values", fmt.Sprintf("values exceeds %d bytes", MaxRecordBytes))
	}
	for key, raw := range input.Values {
		if key == "" || len(key) > 100 || dangerousJSONKey(key) {
			return Invalid("values", "values contains an unsafe object key")
		}
		if _, err := validateJSONValue(raw, "values."+key, MaxRecordBytes); err != nil {
			return err
		}
	}
	return nil
}

func ValidateEnvironmentVariable(input *EnvironmentVariableInput) error {
	input.Name = strings.ToUpper(strings.TrimSpace(input.Name))
	if !variableNamePattern.MatchString(input.Name) {
		return Invalid("name", "name must begin with a letter and contain only uppercase letters, numbers, or underscores")
	}
	switch input.Scope {
	case ScopeDevelopment, ScopePreview, ScopeProduction:
	default:
		return Invalid("scope", "scope must be development, preview, or production")
	}
	if input.Kind == "" {
		input.Kind = VariableSecret
	}
	if input.Kind != VariablePlain && input.Kind != VariableSecret {
		return Invalid("kind", "kind must be plain or secret")
	}
	if input.Value == "" {
		return Invalid("value", "value cannot be empty")
	}
	if len([]byte(input.Value)) > MaxVariableValueBytes {
		return Invalid("value", fmt.Sprintf("value exceeds %d bytes", MaxVariableValueBytes))
	}
	if !utf8.ValidString(input.Value) {
		return Invalid("value", "value must be valid UTF-8")
	}
	return nil
}

func IsPublicEnvironmentName(name string) bool {
	return publicVariableNamePattern.MatchString(strings.TrimSpace(name))
}

func ValidateMigrationOperations(operations []MigrationOperation) error {
	if len(operations) == 0 || len(operations) > MaxMigrationOperations {
		return Invalid("operations", fmt.Sprintf("operations must contain between 1 and %d items", MaxMigrationOperations))
	}
	for index := range operations {
		op := &operations[index]
		field := fmt.Sprintf("operations[%d]", index)
		switch op.Type {
		case MigrationCreateTable:
			if op.Table == nil || op.TableID != "" || op.Column != nil || op.ColumnID != "" || op.Name != "" {
				return Invalid(field, field+" has fields that are invalid for create-table")
			}
			if err := ValidateTableInput(op.Table); err != nil {
				return err
			}
		case MigrationRenameTable:
			if op.TableID == "" || op.Name == "" || op.Table != nil || op.Column != nil || op.ColumnID != "" {
				return Invalid(field, field+" has fields that are invalid for rename-table")
			}
			name, err := normalizeDatabaseIdentifier(op.Name, field+".name")
			if err != nil {
				return err
			}
			op.Name = name
		case MigrationDropTable:
			if op.TableID == "" || op.Table != nil || op.Column != nil || op.ColumnID != "" || op.Name != "" {
				return Invalid(field, field+" has fields that are invalid for drop-table")
			}
		case MigrationAddColumn:
			if op.TableID == "" || op.Column == nil || op.Table != nil || op.ColumnID != "" || op.Name != "" {
				return Invalid(field, field+" has fields that are invalid for add-column")
			}
			if err := ValidateColumnInput(op.Column, field+".column"); err != nil {
				return err
			}
		case MigrationRenameColumn:
			if op.TableID == "" || op.ColumnID == "" || op.Name == "" || op.Table != nil || op.Column != nil {
				return Invalid(field, field+" has fields that are invalid for rename-column")
			}
			name, err := normalizeDatabaseIdentifier(op.Name, field+".name")
			if err != nil {
				return err
			}
			op.Name = name
		case MigrationDropColumn:
			if op.TableID == "" || op.ColumnID == "" || op.Table != nil || op.Column != nil || op.Name != "" {
				return Invalid(field, field+" has fields that are invalid for drop-column")
			}
		default:
			return Invalid(field+".type", field+".type is not supported")
		}
		if op.TableID != "" {
			if _, err := uuid.Parse(op.TableID); err != nil {
				return Invalid(field+".tableId", field+".tableId must be a UUID")
			}
		}
		if op.ColumnID != "" {
			if _, err := uuid.Parse(op.ColumnID); err != nil {
				return Invalid(field+".columnId", field+".columnId must be a UUID")
			}
		}
	}
	return nil
}

func ValidateConfirmationToken(token string) error {
	if !confirmationTokenPattern.MatchString(strings.TrimSpace(token)) {
		return Invalid("confirmationToken", "confirmationToken is not valid")
	}
	return nil
}

func ParseMetadataKind(value string) (MetadataKind, error) {
	kind := MetadataKind(strings.TrimSpace(value))
	switch kind {
	case MetadataAuthUsers, MetadataStorageObjects, MetadataServerFunctions:
		return kind, nil
	default:
		return "", Invalid("kind", "kind must be auth-users, storage-objects, or server-functions")
	}
}

// NormalizeMetadataPatch validates an exact kind-specific DTO, merges PATCH
// fields when existing is supplied, applies defaults, and returns persistence
// payload plus its kind-specific uniqueness key.
func NormalizeMetadataPatch(kind MetadataKind, patch map[string]json.RawMessage, existing json.RawMessage) (json.RawMessage, string, error) {
	allowed := metadataAllowedFields(kind)
	if allowed == nil {
		return nil, "", Invalid("kind", "metadata kind is invalid")
	}
	merged := map[string]json.RawMessage{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &merged); err != nil {
			return nil, "", fmt.Errorf("decode stored metadata: %w", err)
		}
	}
	for key, value := range patch {
		if _, ok := allowed[key]; !ok {
			return nil, "", Invalid(key, "unknown metadata field "+key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(merged, key)
		} else {
			merged[key] = value
		}
	}
	if kind == MetadataAuthUsers {
		if _, ok := merged["status"]; !ok {
			merged["status"] = json.RawMessage(`"active"`)
		}
	} else if kind == MetadataStorageObjects {
		if _, ok := merged["sizeBytes"]; !ok {
			merged["sizeBytes"] = json.RawMessage(`0`)
		}
	} else {
		if _, ok := merged["runtime"]; !ok {
			merged["runtime"] = json.RawMessage(`"edge"`)
		}
		if _, ok := merged["status"]; !ok {
			merged["status"] = json.RawMessage(`"draft"`)
		}
	}
	unique, err := validateMetadataFields(kind, merged)
	if err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) > MaxMetadataBytes {
		return nil, "", Invalid("metadata", fmt.Sprintf("metadata exceeds %d bytes", MaxMetadataBytes))
	}
	return encoded, unique, nil
}

func metadataAllowedFields(kind MetadataKind) map[string]struct{} {
	fields := []string{}
	switch kind {
	case MetadataAuthUsers:
		fields = []string{"email", "displayName", "role", "status", "lastSignInAt", "metadata"}
	case MetadataStorageObjects:
		fields = []string{"bucket", "path", "contentType", "sizeBytes", "checksum", "metadata"}
	case MetadataServerFunctions:
		fields = []string{"name", "description", "runtime", "entryPath", "status", "metadata"}
	default:
		return nil
	}
	result := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		result[field] = struct{}{}
	}
	return result
}

func validateMetadataFields(kind MetadataKind, object map[string]json.RawMessage) (string, error) {
	stringValue := func(field string, required bool, maximum int) (string, error) {
		raw, ok := object[field]
		if !ok {
			if required {
				return "", Invalid(field, field+" is required")
			}
			return "", nil
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", Invalid(field, field+" must be a string")
		}
		value = strings.TrimSpace(value)
		if value == "" && !required {
			delete(object, field)
			return "", nil
		}
		if value == "" || len(value) > maximum || containsControl(value) {
			return "", Invalid(field, field+" is invalid")
		}
		object[field], _ = json.Marshal(value)
		return value, nil
	}
	validatePublicMetadata := func() error {
		raw, ok := object["metadata"]
		if !ok {
			return nil
		}
		var value any
		if err := decodeJSON(raw, &value); err != nil {
			return Invalid("metadata", "metadata must be a JSON object")
		}
		if _, ok := value.(map[string]any); !ok {
			return Invalid("metadata", "metadata must be a JSON object")
		}
		if err := inspectJSON(value, "metadata", 0); err != nil {
			return err
		}
		return inspectPublicMetadata(value, "metadata", 0)
	}

	switch kind {
	case MetadataAuthUsers:
		email, err := stringValue("email", false, 254)
		if err != nil {
			return "", err
		}
		email = strings.ToLower(email)
		if email != "" && !emailPattern.MatchString(email) {
			return "", Invalid("email", "email is not valid")
		}
		if email != "" {
			object["email"], _ = json.Marshal(email)
		}
		if _, err := stringValue("displayName", false, 120); err != nil {
			return "", err
		}
		role, err := stringValue("role", false, 50)
		if err != nil || (role != "" && !rolePattern.MatchString(role)) {
			return "", Invalid("role", "role is not valid")
		}
		status, err := stringValue("status", true, 16)
		if err != nil || (status != "invited" && status != "active" && status != "disabled") {
			return "", Invalid("status", "status must be invited, active, or disabled")
		}
		date, err := stringValue("lastSignInAt", false, 50)
		if err != nil {
			return "", err
		}
		if date != "" {
			if _, err := time.Parse(time.RFC3339Nano, date); err != nil {
				if _, dateErr := time.Parse("2006-01-02", date); dateErr != nil {
					return "", Invalid("lastSignInAt", "lastSignInAt must be an ISO date")
				}
			}
		}
		if err := validatePublicMetadata(); err != nil {
			return "", err
		}
		return strings.ToLower(email), nil
	case MetadataStorageObjects:
		bucket, err := stringValue("bucket", true, 63)
		if err != nil {
			return "", err
		}
		bucket, err = normalizeDatabaseIdentifier(bucket, "bucket")
		if err != nil {
			return "", err
		}
		object["bucket"], _ = json.Marshal(bucket)
		path, err := stringValue("path", true, 480)
		if err != nil {
			return "", err
		}
		path, err = normalizeStoragePath(path, "path")
		if err != nil {
			return "", err
		}
		object["path"], _ = json.Marshal(path)
		contentType, err := stringValue("contentType", false, 120)
		if err != nil || (contentType != "" && !contentTypePattern.MatchString(contentType)) {
			return "", Invalid("contentType", "contentType is not valid")
		}
		if raw, ok := object["sizeBytes"]; ok {
			var size int64
			if err := json.Unmarshal(raw, &size); err != nil || size < 0 || size > 10_000_000_000 {
				return "", Invalid("sizeBytes", "sizeBytes must be a non-negative safe integer")
			}
		}
		checksum, err := stringValue("checksum", false, 128)
		if err != nil || (checksum != "" && !checksumPattern.MatchString(checksum)) {
			return "", Invalid("checksum", "checksum is not valid")
		}
		if err := validatePublicMetadata(); err != nil {
			return "", err
		}
		return bucket + "/" + path, nil
	case MetadataServerFunctions:
		name, err := stringValue("name", true, 63)
		if err != nil {
			return "", err
		}
		name, err = normalizeDatabaseIdentifier(name, "name")
		if err != nil {
			return "", err
		}
		object["name"], _ = json.Marshal(name)
		if _, err := stringValue("description", false, 500); err != nil {
			return "", err
		}
		runtime, err := stringValue("runtime", true, 10)
		if err != nil || (runtime != "edge" && runtime != "node") {
			return "", Invalid("runtime", "runtime must be edge or node")
		}
		entryPath, err := stringValue("entryPath", false, 480)
		if err != nil {
			return "", err
		}
		if entryPath != "" {
			entryPath, err = normalizeStoragePath(entryPath, "entryPath")
			if err != nil {
				return "", err
			}
			for _, segment := range strings.Split(entryPath, "/") {
				if segment == ".git" || segment == ".next" || segment == "node_modules" {
					return "", Invalid("entryPath", "entryPath points to a forbidden directory")
				}
			}
			object["entryPath"], _ = json.Marshal(entryPath)
		}
		status, err := stringValue("status", true, 16)
		if err != nil || (status != "draft" && status != "active" && status != "disabled") {
			return "", Invalid("status", "status must be draft, active, or disabled")
		}
		if err := validatePublicMetadata(); err != nil {
			return "", err
		}
		return name, nil
	default:
		return "", Invalid("kind", "metadata kind is invalid")
	}
}

func normalizeDatabaseIdentifier(value, field string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !databaseIdentifierPattern.MatchString(value) {
		return "", Invalid(field, field+" must begin with a letter and contain only lowercase letters, numbers, or underscores")
	}
	return value, nil
}

func normalizeStoragePath(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") || regexp.MustCompile(`^[a-zA-Z]:[\\/]`).MatchString(value) {
		return "", Invalid(field, field+" must be a relative object path")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "./")
	for strings.Contains(value, "//") {
		value = strings.ReplaceAll(value, "//", "/")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || containsControl(segment) {
			return "", Invalid(field, field+" contains an unsafe path segment")
		}
	}
	return value, nil
}

func validateJSONValue(raw json.RawMessage, field string, maximum int) (any, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return nil, Invalid(field, fmt.Sprintf("%s exceeds %d bytes", field, maximum))
	}
	var value any
	if err := decodeJSON(raw, &value); err != nil {
		return nil, Invalid(field, field+" must contain a valid JSON value")
	}
	if err := inspectJSON(value, field, 0); err != nil {
		return nil, err
	}
	return value, nil
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return nil
}

func inspectJSON(value any, field string, depth int) error {
	if depth > 8 {
		return Invalid(field, field+" exceeds the maximum nesting depth")
	}
	switch typed := value.(type) {
	case nil, bool, string, json.Number:
		return nil
	case []any:
		if len(typed) > 500 {
			return Invalid(field, field+" contains too many array items")
		}
		for index, item := range typed {
			if err := inspectJSON(item, fmt.Sprintf("%s[%d]", field, index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(typed) > 200 {
			return Invalid(field, field+" contains too many object properties")
		}
		for key, item := range typed {
			if key == "" || len(key) > 100 || dangerousJSONKey(key) {
				return Invalid(field, field+" contains an unsafe object key")
			}
			if err := inspectJSON(item, field+"."+key, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return Invalid(field, field+" contains a non-JSON value")
	}
}

func inspectPublicMetadata(value any, field string, depth int) error {
	if depth > 8 {
		return Invalid(field, field+" exceeds the maximum nesting depth")
	}
	switch typed := value.(type) {
	case []any:
		for index, item := range typed {
			if err := inspectPublicMetadata(item, fmt.Sprintf("%s[%d]", field, index), depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if sensitiveKeyPattern.MatchString(key) {
				return Invalid(field+"."+key, field+"."+key+" may not contain secrets or credentials")
			}
			if err := inspectPublicMetadata(typed[key], field+"."+key, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func valueMatchesColumn(value any, columnType ColumnType) bool {
	if value == nil || columnType == ColumnJSON {
		return true
	}
	switch columnType {
	case ColumnText:
		_, ok := value.(string)
		return ok
	case ColumnNumber:
		_, ok := value.(json.Number)
		return ok
	case ColumnBoolean:
		_, ok := value.(bool)
		return ok
	case ColumnDate:
		date, ok := value.(string)
		if !ok {
			return false
		}
		_, err := time.Parse(time.RFC3339Nano, date)
		if err == nil {
			return true
		}
		_, err = time.Parse("2006-01-02", date)
		return err == nil
	default:
		return false
	}
}

func dangerousJSONKey(key string) bool {
	return key == "__proto__" || key == "constructor" || key == "prototype"
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 32 || character == 127 {
			return true
		}
	}
	return false
}
