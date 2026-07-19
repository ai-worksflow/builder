package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const TicketRequestSchemaVersion = "sandbox-lsp-ticket-request/v1"

type TicketRequest struct {
	SchemaVersion   string               `json:"schemaVersion"`
	Mode            TicketMode           `json:"mode"`
	Head            SandboxHeadFence     `json:"sandboxHeadFence"`
	TemplateRelease ExactTemplateRelease `json:"templateRelease"`
	ProfileIDs      []string             `json:"profileIds"`
}

// DecodeTicketRequest is the only browser ticket-request decoder. It rejects
// aliases, duplicate/unknown fields, nulls, widened numbers, non-canonical
// profile ordering, and trailing values before any authority lookup occurs.
func DecodeTicketRequest(value []byte) (TicketRequest, error) {
	if len(value) == 0 || len(value) > 64<<10 {
		return TicketRequest{}, ErrTicketInvalid
	}
	if err := validateBrowserJSONDepth(value, 5); err != nil {
		return TicketRequest{}, fmt.Errorf("%w: %v", ErrTicketInvalid, err)
	}
	fields, err := decodeExactObject(value, []string{
		"schemaVersion", "mode", "sandboxHeadFence", "templateRelease", "profileIds",
	})
	if err != nil {
		return TicketRequest{}, fmt.Errorf("%w: %v", ErrTicketInvalid, err)
	}
	var result TicketRequest
	if decodeString(fields["schemaVersion"], &result.SchemaVersion) != nil ||
		result.SchemaVersion != TicketRequestSchemaVersion {
		return TicketRequest{}, invalidField(ErrTicketInvalid, "schemaVersion")
	}
	var mode string
	if decodeString(fields["mode"], &mode) != nil {
		return TicketRequest{}, invalidField(ErrTicketInvalid, "mode")
	}
	result.Mode = TicketMode(mode)
	if result.Mode != TicketModeSnapshot && result.Mode != TicketModeEditor {
		return TicketRequest{}, invalidField(ErrTicketInvalid, "mode")
	}
	result.Head, err = DecodeSandboxHeadFence(fields["sandboxHeadFence"])
	if err != nil {
		return TicketRequest{}, err
	}
	result.TemplateRelease, err = decodeExactTemplateRelease(fields["templateRelease"])
	if err != nil {
		return TicketRequest{}, err
	}
	result.ProfileIDs, err = decodeProfileIDs(fields["profileIds"])
	if err != nil {
		return TicketRequest{}, err
	}
	return result, nil
}

func decodeExactTemplateRelease(value []byte) (ExactTemplateRelease, error) {
	fields, err := decodeExactObject(value, []string{"id", "contentHash"})
	if err != nil {
		return ExactTemplateRelease{}, fmt.Errorf("%w: templateRelease: %v", ErrTicketInvalid, err)
	}
	var result ExactTemplateRelease
	if decodeString(fields["id"], &result.ID) != nil ||
		decodeString(fields["contentHash"], &result.ContentHash) != nil || result.Validate() != nil {
		return ExactTemplateRelease{}, invalidField(ErrTicketInvalid, "templateRelease")
	}
	return result, nil
}

func decodeProfileIDs(value []byte) ([]string, error) {
	if len(value) == 0 || string(value) == "null" {
		return nil, invalidField(ErrTicketInvalid, "profileIds")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	var result []string
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, invalidField(ErrTicketInvalid, "profileIds")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, invalidField(ErrTicketInvalid, "profileIds")
	}
	validated, err := validateRequestedProfiles(result)
	if err != nil {
		return nil, invalidField(ErrTicketInvalid, "profileIds")
	}
	return validated, nil
}
