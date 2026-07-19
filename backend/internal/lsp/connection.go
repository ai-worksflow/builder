package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

const (
	ConnectionSchemaVersion = "sandbox-lsp-connection/v1"
	BindingSchemaVersion    = "sandbox-lsp-binding/v1"
)

var (
	ErrConnectionMalformed  = errors.New("malformed LSP connection message")
	ErrBindingStale         = errors.New("stale LSP binding")
	ErrServerBoundMalformed = errors.New("malformed LSP server.bound message")
)

type ConnectionHello struct {
	SchemaVersion   string               `json:"schemaVersion"`
	Kind            string               `json:"kind"`
	ConnectionID    string               `json:"connectionId"`
	TicketID        string               `json:"ticketId"`
	Sequence        uint64               `json:"sequence"`
	Head            SandboxHeadFence     `json:"sandboxHeadFence"`
	TemplateRelease ExactTemplateRelease `json:"templateRelease"`
	Profiles        []ProfileIdentity    `json:"profiles"`
	Limits          EffectiveLimits      `json:"limits"`
	BindDeadlineAt  time.Time            `json:"bindDeadlineAt"`
}

type ClientBind struct {
	SchemaVersion string           `json:"schemaVersion"`
	Kind          string           `json:"kind"`
	ConnectionID  string           `json:"connectionId"`
	BindingID     *string          `json:"bindingId"`
	Sequence      uint64           `json:"sequence"`
	Head          SandboxHeadFence `json:"sandboxHeadFence"`
	Profile       ProfileIdentity  `json:"languageServerProfile"`
	Documents     []DocumentFence  `json:"documents"`
}

type BoundLanguageServerIdentity struct {
	ProfileID               string `json:"profileId"`
	ProfileContentHash      string `json:"profileContentHash"`
	RuntimeImageDigest      string `json:"runtimeImageDigest"`
	ExecutableDigest        string `json:"executableDigest"`
	ServerName              string `json:"serverName"`
	ServerVersion           string `json:"serverVersion"`
	CapabilityAllowlistHash string `json:"capabilityAllowlistHash"`
}

// ServerBound is a binding fact, not a post-binding message envelope. Its
// identity and capability fields are derived from the successfully decoded
// initialize result, while runtime/profile commitments remain exact
// TemplateRelease facts.
type ServerBound struct {
	SchemaVersion         string                      `json:"schemaVersion"`
	Kind                  string                      `json:"kind"`
	ConnectionID          string                      `json:"connectionId"`
	BindingID             string                      `json:"bindingId"`
	Sequence              uint64                      `json:"sequence"`
	Head                  SandboxHeadFence            `json:"sandboxHeadFence"`
	LanguageServer        BoundLanguageServerIdentity `json:"languageServer"`
	Documents             []DocumentFence             `json:"documents"`
	EffectiveCapabilities []string                    `json:"effectiveCapabilities"`
	Limits                EffectiveLimits             `json:"limits"`
}

type ServerBoundExpectation struct {
	ConnectionID string
	BindingID    string
	Head         SandboxHeadFence
	Profile      ProfileIdentity
	Initialized  InitializedServer
	Documents    []DocumentFence
}

func NewServerBound(expected ServerBoundExpectation) (ServerBound, error) {
	if err := validateServerBoundExpectation(expected); err != nil {
		return ServerBound{}, err
	}
	return ServerBound{
		SchemaVersion: BindingSchemaVersion,
		Kind:          "server.bound",
		ConnectionID:  expected.ConnectionID,
		BindingID:     expected.BindingID,
		Sequence:      1,
		Head:          expected.Head,
		LanguageServer: BoundLanguageServerIdentity{
			ProfileID:               expected.Profile.ID,
			ProfileContentHash:      expected.Profile.ContentHash,
			RuntimeImageDigest:      expected.Profile.Runtime.Image,
			ExecutableDigest:        expected.Profile.Runtime.ExecutableDigest,
			ServerName:              expected.Initialized.ServerInfo.Name,
			ServerVersion:           expected.Initialized.ServerInfo.Version,
			CapabilityAllowlistHash: expected.Initialized.CapabilityHash,
		},
		Documents:             append([]DocumentFence(nil), expected.Documents...),
		EffectiveCapabilities: append([]string(nil), expected.Initialized.Methods...),
		Limits:                expected.Profile.EffectiveLimits,
	}, nil
}

func DecodeServerBound(value []byte, expected ServerBoundExpectation) (ServerBound, error) {
	want, err := NewServerBound(expected)
	if err != nil || len(value) == 0 || int64(len(value)) > expected.Profile.EffectiveLimits.MaxFrameBytes ||
		validateStrictJSONDocument(value, 14) != nil {
		return ServerBound{}, ErrServerBoundMalformed
	}
	fields, err := decodeExactObject(value, []string{
		"schemaVersion", "kind", "connectionId", "bindingId", "sequence",
		"sandboxHeadFence", "languageServer", "documents", "effectiveCapabilities", "limits",
	})
	if err != nil {
		return ServerBound{}, fmt.Errorf("%w: %v", ErrServerBoundMalformed, err)
	}
	var schemaVersion, kind, connectionID, bindingID string
	var sequence uint64
	if decodeString(fields["schemaVersion"], &schemaVersion) != nil || schemaVersion != BindingSchemaVersion ||
		decodeString(fields["kind"], &kind) != nil || kind != "server.bound" ||
		decodeString(fields["connectionId"], &connectionID) != nil || connectionID != expected.ConnectionID ||
		decodeString(fields["bindingId"], &bindingID) != nil || bindingID != expected.BindingID ||
		decodeMethodUint(fields["sequence"], &sequence) != nil || sequence != 1 {
		return ServerBound{}, ErrServerBoundMalformed
	}
	head, err := DecodeSandboxHeadFence(fields["sandboxHeadFence"])
	if err != nil || !head.Equal(expected.Head) {
		return ServerBound{}, ErrBindingStale
	}
	identity, err := decodeBoundLanguageServerIdentity(fields["languageServer"])
	if err != nil || identity != want.LanguageServer {
		return ServerBound{}, ErrProfileNotDeclared
	}
	documents, err := decodeDocumentFences(fields["documents"], head, expected.Profile.EffectiveLimits)
	if err != nil || !equalDocumentSequences(documents, expected.Documents) {
		return ServerBound{}, ErrBindingStale
	}
	capabilities, err := decodeBoundCapabilities(fields["effectiveCapabilities"])
	if err != nil || !slices.Equal(capabilities, expected.Initialized.Methods) {
		return ServerBound{}, ErrProfileNotDeclared
	}
	if err := decodeExactBoundLimits(fields["limits"], expected.Profile.EffectiveLimits); err != nil {
		return ServerBound{}, err
	}
	return want, nil
}

func validateServerBoundExpectation(expected ServerBoundExpectation) error {
	if !canonicalUUID(expected.ConnectionID) || !canonicalUUID(expected.BindingID) ||
		expected.ConnectionID == expected.BindingID || expected.Head.Validate() != nil ||
		expected.Profile.Validate() != nil || expected.Initialized.ServerInfo != expected.Profile.ServerInfo ||
		ValidateCanonicalProductionV1MethodAllowlist(expected.Initialized.Methods) != nil ||
		ValidateProductionV1CapabilityCommitment(
			expected.Initialized.Methods, expected.Initialized.CapabilityHash,
		) != nil || len(expected.Documents) == 0 ||
		len(expected.Documents) > expected.Profile.EffectiveLimits.MaxOpenDocuments {
		return ErrServerBoundMalformed
	}
	for _, method := range expected.Initialized.Methods {
		if _, found := slices.BinarySearch(expected.Profile.Methods, method); !found {
			return ErrServerBoundMalformed
		}
	}
	if validateCurrentDocuments(expected.Head, expected.Documents) != nil {
		return ErrServerBoundMalformed
	}
	return nil
}

func decodeBoundLanguageServerIdentity(value []byte) (BoundLanguageServerIdentity, error) {
	fields, err := decodeExactObject(value, []string{
		"profileId", "profileContentHash", "runtimeImageDigest", "executableDigest",
		"serverName", "serverVersion", "capabilityAllowlistHash",
	})
	if err != nil {
		return BoundLanguageServerIdentity{}, ErrServerBoundMalformed
	}
	var result BoundLanguageServerIdentity
	for raw, target := range map[string]*string{
		"profileId": &result.ProfileID, "profileContentHash": &result.ProfileContentHash,
		"runtimeImageDigest": &result.RuntimeImageDigest, "executableDigest": &result.ExecutableDigest,
		"serverName": &result.ServerName, "serverVersion": &result.ServerVersion,
		"capabilityAllowlistHash": &result.CapabilityAllowlistHash,
	} {
		if decodeString(fields[raw], target) != nil {
			return BoundLanguageServerIdentity{}, ErrServerBoundMalformed
		}
	}
	return result, nil
}

func decodeBoundCapabilities(value []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var raw []json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil || len(raw) == 0 || len(raw) > 32 {
		return nil, ErrServerBoundMalformed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrServerBoundMalformed
	}
	result := make([]string, len(raw))
	for index, encoded := range raw {
		if decodeString(encoded, &result[index]) != nil ||
			(index > 0 && result[index-1] >= result[index]) {
			return nil, ErrServerBoundMalformed
		}
	}
	return result, nil
}

func decodeExactBoundLimits(value []byte, expected EffectiveLimits) error {
	fields, err := decodeExactObject(value, []string{
		"startupTimeoutMillis", "requestTimeoutMillis", "shutdownTimeoutMillis",
		"cpuMillis", "memoryBytes", "pidLimit", "tempBytes", "cacheBytes",
		"maxOpenDocuments", "maxDocumentBytes", "maxTotalSyncBytes", "maxFrameBytes",
		"maxResultBytes", "maxConcurrentRequests", "requestsPerSecond", "requestBurst",
		"maxDiagnosticsPerDocument", "maxCompletionItems", "maxNavigationLocations",
	})
	if err != nil {
		return ErrServerBoundMalformed
	}
	values := map[string]int64{
		"startupTimeoutMillis":  int64(expected.StartupTimeoutMillis),
		"requestTimeoutMillis":  int64(expected.RequestTimeoutMillis),
		"shutdownTimeoutMillis": int64(expected.ShutdownTimeoutMillis),
		"cpuMillis":             int64(expected.CPUMillis), "memoryBytes": expected.MemoryBytes,
		"pidLimit": int64(expected.PIDLimit), "tempBytes": expected.TempBytes,
		"cacheBytes": expected.CacheBytes, "maxOpenDocuments": int64(expected.MaxOpenDocuments),
		"maxDocumentBytes": expected.MaxDocumentBytes, "maxTotalSyncBytes": expected.MaxTotalSyncBytes,
		"maxFrameBytes": expected.MaxFrameBytes, "maxResultBytes": expected.MaxResultBytes,
		"maxConcurrentRequests": int64(expected.MaxConcurrentRequests),
		"requestsPerSecond":     int64(expected.RequestsPerSecond), "requestBurst": int64(expected.RequestBurst),
		"maxDiagnosticsPerDocument": int64(expected.MaxDiagnosticsPerDocument),
		"maxCompletionItems":        int64(expected.MaxCompletionItems),
		"maxNavigationLocations":    int64(expected.MaxNavigationLocations),
	}
	for name, expectedValue := range values {
		var actual uint64
		if expectedValue < 0 || decodeMethodUint(fields[name], &actual) != nil || actual != uint64(expectedValue) {
			return ErrServerBoundMalformed
		}
	}
	return nil
}

func NewConnectionHello(grant TicketGrant, connectionID string, deadline time.Time) (ConnectionHello, error) {
	if validateTicketGrant(grant, time.Time{}) != nil || !canonicalUUID(connectionID) || deadline.IsZero() {
		return ConnectionHello{}, ErrConnectionMalformed
	}
	limits, err := minimumEffectiveLimits(grant.Profiles)
	if err != nil {
		return ConnectionHello{}, err
	}
	return ConnectionHello{
		SchemaVersion: ConnectionSchemaVersion, Kind: "server.hello",
		ConnectionID: connectionID, TicketID: grant.ID, Sequence: 0,
		Head: grant.Head, TemplateRelease: grant.TemplateRelease,
		Profiles: cloneProfiles(grant.Profiles), Limits: limits, BindDeadlineAt: deadline.UTC(),
	}, nil
}

func DecodeClientBind(value []byte, grant TicketGrant, connectionID string) (ClientBind, error) {
	if len(value) == 0 || len(value) > 512<<10 || validateTicketGrant(grant, time.Time{}) != nil ||
		!canonicalUUID(connectionID) {
		return ClientBind{}, ErrConnectionMalformed
	}
	if err := validateStrictJSONDocument(value, 12); err != nil {
		return ClientBind{}, fmt.Errorf("%w: %v", ErrConnectionMalformed, err)
	}
	fields, err := decodeExactObject(value, []string{
		"schemaVersion", "kind", "connectionId", "bindingId", "sequence",
		"sandboxHeadFence", "languageServerProfile", "documents",
	})
	if err != nil {
		return ClientBind{}, fmt.Errorf("%w: %v", ErrConnectionMalformed, err)
	}
	var result ClientBind
	if decodeString(fields["schemaVersion"], &result.SchemaVersion) != nil ||
		result.SchemaVersion != BindingSchemaVersion ||
		decodeString(fields["kind"], &result.Kind) != nil || result.Kind != "client.bind" ||
		decodeString(fields["connectionId"], &result.ConnectionID) != nil || result.ConnectionID != connectionID ||
		string(bytes.TrimSpace(fields["bindingId"])) != "null" ||
		decodeUint(fields["sequence"], &result.Sequence) != nil || result.Sequence != 1 {
		return ClientBind{}, ErrConnectionMalformed
	}
	result.Head, err = DecodeSandboxHeadFence(fields["sandboxHeadFence"])
	if err != nil || !result.Head.Equal(grant.Head) {
		return ClientBind{}, ErrBindingStale
	}
	result.Profile, err = decodeProfileIdentity(fields["languageServerProfile"])
	if err != nil || result.Profile.TemplateRelease != grant.TemplateRelease ||
		!profileInGrant(result.Profile, grant.Profiles) {
		return ClientBind{}, ErrProfileNotDeclared
	}
	result.Documents, err = decodeDocumentFences(fields["documents"], result.Head, result.Profile.EffectiveLimits)
	if err != nil {
		return ClientBind{}, err
	}
	return result, nil
}

func decodeProfileIdentity(value []byte) (ProfileIdentity, error) {
	if err := validateStrictJSONDocument(value, 10); err != nil {
		return ProfileIdentity{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var result ProfileIdentity
	if err := decoder.Decode(&result); err != nil {
		return ProfileIdentity{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ProfileIdentity{}, ErrConnectionMalformed
	}
	if result.Validate() != nil {
		return ProfileIdentity{}, ErrConnectionMalformed
	}
	return result, nil
}

func decodeDocumentFences(
	value []byte,
	head SandboxHeadFence,
	limits EffectiveLimits,
) ([]DocumentFence, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var raw []json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil || len(raw) == 0 || len(raw) > limits.MaxOpenDocuments {
		return nil, ErrConnectionMalformed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrConnectionMalformed
	}
	result := make([]DocumentFence, len(raw))
	for index, encoded := range raw {
		document, err := DecodeDocumentFence(encoded)
		if err != nil || document.ValidateAgainstHead(head) != nil ||
			(index > 0 && result[index-1].ModelURI >= document.ModelURI) {
			return nil, ErrBindingStale
		}
		result[index] = document
	}
	return result, nil
}

func profileInGrant(expected ProfileIdentity, profiles []ProfileIdentity) bool {
	for _, profile := range profiles {
		if equalProfiles([]ProfileIdentity{expected}, []ProfileIdentity{profile}) {
			return true
		}
	}
	return false
}

func minimumEffectiveLimits(profiles []ProfileIdentity) (EffectiveLimits, error) {
	if len(profiles) == 0 || len(profiles) > 4 {
		return EffectiveLimits{}, ErrProfileNotDeclared
	}
	values := cloneProfiles(profiles)
	sortProfileIdentities(values)
	if !equalProfileOrder(values, profiles) {
		return EffectiveLimits{}, ErrProfileNotDeclared
	}
	result := values[0].EffectiveLimits
	for index, profile := range values {
		if profile.Validate() != nil || (index > 0 && values[index-1].ID >= profile.ID) {
			return EffectiveLimits{}, ErrProfileNotDeclared
		}
		result = minLimits(result, profile.EffectiveLimits)
	}
	return result, nil
}

func equalProfileOrder(left, right []ProfileIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID {
			return false
		}
	}
	return true
}

func minLimits(left, right EffectiveLimits) EffectiveLimits {
	result := left
	result.StartupTimeoutMillis = min(left.StartupTimeoutMillis, right.StartupTimeoutMillis)
	result.RequestTimeoutMillis = min(left.RequestTimeoutMillis, right.RequestTimeoutMillis)
	result.ShutdownTimeoutMillis = min(left.ShutdownTimeoutMillis, right.ShutdownTimeoutMillis)
	result.CPUMillis = min(left.CPUMillis, right.CPUMillis)
	result.MemoryBytes = min(left.MemoryBytes, right.MemoryBytes)
	result.PIDLimit = min(left.PIDLimit, right.PIDLimit)
	result.TempBytes = min(left.TempBytes, right.TempBytes)
	result.CacheBytes = min(left.CacheBytes, right.CacheBytes)
	result.MaxOpenDocuments = min(left.MaxOpenDocuments, right.MaxOpenDocuments)
	result.MaxDocumentBytes = min(left.MaxDocumentBytes, right.MaxDocumentBytes)
	result.MaxTotalSyncBytes = min(left.MaxTotalSyncBytes, right.MaxTotalSyncBytes)
	result.MaxFrameBytes = min(left.MaxFrameBytes, right.MaxFrameBytes)
	result.MaxResultBytes = min(left.MaxResultBytes, right.MaxResultBytes)
	result.MaxConcurrentRequests = min(left.MaxConcurrentRequests, right.MaxConcurrentRequests)
	result.RequestsPerSecond = min(left.RequestsPerSecond, right.RequestsPerSecond)
	result.RequestBurst = min(left.RequestBurst, right.RequestBurst)
	result.MaxDiagnosticsPerDocument = min(left.MaxDiagnosticsPerDocument, right.MaxDiagnosticsPerDocument)
	result.MaxCompletionItems = min(left.MaxCompletionItems, right.MaxCompletionItems)
	result.MaxNavigationLocations = min(left.MaxNavigationLocations, right.MaxNavigationLocations)
	return result
}

func validateStrictJSONDocument(value []byte, maximumDepth int) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := validateStrictJSONValue(decoder, 0, maximumDepth); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are forbidden")
		}
		return err
	}
	return nil
}

func validateStrictJSONValue(decoder *json.Decoder, depth, maximumDepth int) error {
	if depth > maximumDepth {
		return errors.New("JSON nesting depth exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid JSON object field")
			}
			seen[name] = true
			if err := validateStrictJSONValue(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateStrictJSONValue(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}
