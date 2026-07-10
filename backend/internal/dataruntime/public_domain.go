package dataruntime

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	MaxPublicRequestBytes       = 96_000
	MaxPublicOrigins            = 16
	MaxPublicOriginBytes        = 2_048
	DefaultPreviewCapabilityTTL = 7 * 24 * time.Hour
	DefaultProductionTTL        = 180 * 24 * time.Hour
	MaxPublicCapabilityTTL      = 366 * 24 * time.Hour
)

const (
	CodePublicCapabilityInvalid ErrorCode = "public_capability_invalid"
	CodePublicPolicyDenied      ErrorCode = "public_policy_denied"
	CodePublicOriginDenied      ErrorCode = "public_origin_denied"
	CodePublicRateLimited       ErrorCode = "public_rate_limited"
	CodePublicRuntimeUnavailable ErrorCode = "public_runtime_unavailable"
)

type PublicTablePolicyInput struct {
	AllowRead      bool     `json:"allowRead"`
	AllowCreate    bool     `json:"allowCreate"`
	AllowUpdate    bool     `json:"allowUpdate"`
	AllowDelete    bool     `json:"allowDelete"`
	ReadableFields []string `json:"readableFields"`
	WritableFields []string `json:"writableFields"`
}

type PublicTablePolicy struct {
	ProjectID      string    `json:"projectId"`
	TableID        string    `json:"tableId"`
	TableName      string    `json:"tableName"`
	AllowRead      bool      `json:"allowRead"`
	AllowCreate    bool      `json:"allowCreate"`
	AllowUpdate    bool      `json:"allowUpdate"`
	AllowDelete    bool      `json:"allowDelete"`
	ReadableFields []string  `json:"readableFields"`
	WritableFields []string  `json:"writableFields"`
	Version        uint64    `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (p PublicTablePolicy) permits(operation PublicDataOperation) bool {
	switch operation {
	case PublicOperationRead:
		return p.AllowRead
	case PublicOperationCreate:
		return p.AllowCreate
	case PublicOperationUpdate:
		return p.AllowUpdate
	case PublicOperationDelete:
		return p.AllowDelete
	default:
		return false
	}
}

type PublicDataOperation string

const (
	PublicOperationRead   PublicDataOperation = "read"
	PublicOperationCreate PublicDataOperation = "create"
	PublicOperationUpdate PublicDataOperation = "update"
	PublicOperationDelete PublicDataOperation = "delete"
)

type PreparePublicCapabilityInput struct {
	ProjectID            string    `json:"projectId"`
	DeploymentID         string    `json:"deploymentId"`
	DeploymentVersionID  string    `json:"deploymentVersionId"`
	AllowedOrigins       []string  `json:"allowedOrigins"`
	ExpiresAt            time.Time `json:"expiresAt,omitempty"`
}

// PreparedPublicRuntimeConfig contains the capability exactly once. Only the
// digest is persisted. A delivery provider should inject this object as a
// per-deployment runtime overlay, never into the immutable build artifact.
type PreparedPublicRuntimeConfig struct {
	APIBasePath          string    `json:"apiBasePath"`
	ProjectID            string    `json:"projectId"`
	DeploymentID         string    `json:"deploymentId"`
	DeploymentVersionID  string    `json:"deploymentVersionId"`
	CapabilityID         string    `json:"capabilityId"`
	CapabilityToken      string    `json:"capabilityToken"`
	AllowedOrigins       []string  `json:"allowedOrigins"`
	ExpiresAt            time.Time `json:"expiresAt"`
}

type PublicDeploymentRuntime struct {
	APIBasePath          string     `json:"apiBasePath"`
	ProjectID            string     `json:"projectId"`
	DeploymentID         string     `json:"deploymentId"`
	DeploymentVersionID  string     `json:"deploymentVersionId"`
	CapabilityID         string     `json:"capabilityId"`
	AllowedOrigins       []string   `json:"allowedOrigins"`
	ExpiresAt            time.Time  `json:"expiresAt"`
	ActivatedAt          *time.Time `json:"activatedAt,omitempty"`
}

type PublicCapability struct {
	ID                  string
	ProjectID           string
	DeploymentID        string
	DeploymentVersionID string
	AllowedOrigins      []string
	ExpiresAt           time.Time
	authenticated       bool
}

type PublicTable struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Columns          []Column              `json:"columns"`
	ReadableFields   []string              `json:"readableFields"`
	WritableFields   []string              `json:"writableFields"`
	Permissions      PublicTablePermission `json:"permissions"`
}

type PublicTablePermission struct {
	Read   bool `json:"read"`
	Create bool `json:"create"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

type publicCapabilityRecord struct {
	ID                  string
	ProjectID           string
	DeploymentID        string
	DeploymentVersionID string
	TokenDigest         []byte
	AllowedOrigins      []string
	Status              string
	ExpiresAt           time.Time
	ActivatedAt         *time.Time
}

func ValidatePublicTablePolicy(input *PublicTablePolicyInput, table Table) error {
	if input == nil {
		return Invalid("policy", "policy must be an object")
	}
	known := make(map[string]struct{}, len(table.Columns))
	for _, column := range table.Columns {
		known[column.Name] = struct{}{}
	}
	var err error
	input.ReadableFields, err = normalizePublicFields(input.ReadableFields, known, "readableFields")
	if err != nil {
		return err
	}
	input.WritableFields, err = normalizePublicFields(input.WritableFields, known, "writableFields")
	if err != nil {
		return err
	}
	if !input.AllowRead && len(input.ReadableFields) != 0 {
		return Invalid("readableFields", "readableFields must be empty when anonymous read is disabled")
	}
	if !input.AllowCreate && !input.AllowUpdate && len(input.WritableFields) != 0 {
		return Invalid("writableFields", "writableFields must be empty when anonymous create and update are disabled")
	}
	return nil
}

func normalizePublicFields(values []string, known map[string]struct{}, field string) ([]string, error) {
	if len(values) > MaxColumnsPerTable {
		return nil, Invalid(field, fmt.Sprintf("%s may contain at most %d fields", field, MaxColumnsPerTable))
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if _, ok := known[value]; !ok {
			return nil, Invalid(fmt.Sprintf("%s[%d]", field, index), "public field must match a column in the selected table")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, Invalid(field, field+" contains duplicate fields")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func NormalizePublicOrigins(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > MaxPublicOrigins {
		return nil, Invalid("allowedOrigins", fmt.Sprintf("allowedOrigins must contain between 1 and %d origins", MaxPublicOrigins))
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "*" || len(value) > MaxPublicOriginBytes {
			return nil, Invalid(fmt.Sprintf("allowedOrigins[%d]", index), "allowed origin must be an explicit http or https origin")
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return nil, Invalid(fmt.Sprintf("allowedOrigins[%d]", index), "allowed origin must contain only scheme, host, and optional port")
		}
		if parsed.Scheme != "https" && parsed.Scheme != "http" {
			return nil, Invalid(fmt.Sprintf("allowedOrigins[%d]", index), "allowed origin scheme must be http or https")
		}
		if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
			return nil, Invalid(fmt.Sprintf("allowedOrigins[%d]", index), "non-local public origins must use https")
		}
		normalized := parsed.Scheme + "://" + strings.ToLower(parsed.Host)
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result, nil
}

func newPublicCapabilityToken() (string, string, error) {
	id := uuid.New()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("create public data capability: %w", err)
	}
	token := "wfpub_" + id.String() + "." + base64.RawURLEncoding.EncodeToString(secret)
	return id.String(), token, nil
}

func parsePublicCapabilityToken(token string) (string, error) {
	trimmed := strings.TrimSpace(token)
	if len(trimmed) != len("wfpub_")+36+1+43 || !strings.HasPrefix(trimmed, "wfpub_") {
		return "", NewError(CodePublicCapabilityInvalid, http.StatusUnauthorized, "The public data capability is invalid or expired")
	}
	parts := strings.Split(strings.TrimPrefix(trimmed, "wfpub_"), ".")
	if len(parts) != 2 {
		return "", NewError(CodePublicCapabilityInvalid, http.StatusUnauthorized, "The public data capability is invalid or expired")
	}
	if _, err := uuid.Parse(parts[0]); err != nil {
		return "", NewError(CodePublicCapabilityInvalid, http.StatusUnauthorized, "The public data capability is invalid or expired")
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(secret) != 32 {
		return "", NewError(CodePublicCapabilityInvalid, http.StatusUnauthorized, "The public data capability is invalid or expired")
	}
	return parts[0], nil
}

func publicRecordValues(record Record, fields []string) Record {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	filtered := make(map[string]json.RawMessage, len(fields))
	for name, value := range record.Values {
		if _, ok := allowed[name]; ok {
			filtered[name] = cloneRaw(value)
		}
	}
	record.Values = filtered
	return record
}
