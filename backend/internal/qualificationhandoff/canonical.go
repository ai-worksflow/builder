package qualificationhandoff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationpromotionv2"
)

type completionBundleWire struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	HandoffID         string                    `json:"handoffId"`
	Completion        CompletionMaterial        `json:"completion"`
	RevisionAuthority RevisionAuthorityMaterial `json:"revisionAuthority"`
	OutputRevision    OutputRevision            `json:"outputRevision"`
	Workflow          WorkflowProjection        `json:"workflow"`
}

type completeBundleWire struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	HandoffID         string                    `json:"handoffId"`
	Completion        CompletionMaterial        `json:"completion"`
	RevisionAuthority RevisionAuthorityMaterial `json:"revisionAuthority"`
	OutputRevision    OutputRevision            `json:"outputRevision"`
	Workflow          WorkflowProjection        `json:"workflow"`
	Idempotent        *bool                     `json:"idempotent"`
}

func DecodeCompleteBundle(encoded []byte) (Record, error) {
	var wire completeBundleWire
	if err := decodeStrictTransport(encoded, &wire); err != nil {
		return Record{}, err
	}
	if err := validateRawBundleShape(encoded, true); err != nil {
		return Record{}, err
	}
	if wire.Idempotent == nil {
		return Record{}, invalid("bundle.idempotent", "is required on completion responses")
	}
	bundle := CompletionBundle{
		SchemaVersion: wire.SchemaVersion, HandoffID: wire.HandoffID,
		Completion: wire.Completion, RevisionAuthority: wire.RevisionAuthority,
		OutputRevision: wire.OutputRevision, Workflow: wire.Workflow,
	}
	record, err := validatedRecord(bundle, *wire.Idempotent)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func DecodeInspectBundle(encoded []byte) (Record, error) {
	var wire completionBundleWire
	if err := decodeStrictTransport(encoded, &wire); err != nil {
		return Record{}, err
	}
	if err := validateRawBundleShape(encoded, false); err != nil {
		return Record{}, err
	}
	bundle := CompletionBundle{
		SchemaVersion: wire.SchemaVersion, HandoffID: wire.HandoffID,
		Completion: wire.Completion, RevisionAuthority: wire.RevisionAuthority,
		OutputRevision: wire.OutputRevision, Workflow: wire.Workflow,
	}
	return validatedRecord(bundle, true)
}

func validatedRecord(bundle CompletionBundle, idempotent bool) (Record, error) {
	if err := ValidateBundle(bundle); err != nil {
		return Record{}, err
	}
	handoffID, err := uuid.Parse(bundle.HandoffID)
	if err != nil {
		return Record{}, invalid("bundle.handoffId", "is invalid")
	}
	return Record{HandoffID: handoffID, Bundle: bundle, Idempotent: idempotent}, nil
}

// HandoffDomainHash mirrors qualification_handoff_v1_hash byte-for-byte.
func HandoffDomainHash(domain string, canonicalBytes []byte) string {
	material := make([]byte, 0, len(HandoffHashPrefixV1)+len(domain)+len(canonicalBytes)+2)
	material = append(material, HandoffHashPrefixV1...)
	material = append(material, 0)
	material = append(material, domain...)
	material = append(material, 0)
	material = append(material, canonicalBytes...)
	digest := sha256.Sum256(material)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateRetainedDocument(name, domain, retainedHash, bytesHex string, document any) error {
	if !validDigest(retainedHash) {
		return invalid(name+".hash", "must be a canonical SHA-256 digest")
	}
	if bytesHex == "" || len(bytesHex) > MaximumRetainedBytes*2 || len(bytesHex)%2 != 0 ||
		strings.ToLower(bytesHex) != bytesHex {
		return invalid(name+".bytesHex", "must be bounded lowercase hexadecimal")
	}
	exactBytes, err := hex.DecodeString(bytesHex)
	if err != nil || len(exactBytes) == 0 || len(exactBytes) > MaximumRetainedBytes || !utf8.Valid(exactBytes) ||
		bytes.HasPrefix(exactBytes, []byte{0xef, 0xbb, 0xbf}) {
		return invalid(name+".bytesHex", "does not encode bounded BOM-free UTF-8")
	}
	canonical, err := qualificationpromotionv2.CanonicalJSON(document)
	if err != nil {
		return invalid(name+".document", "cannot be canonically encoded")
	}
	if !bytes.Equal(canonical, exactBytes) {
		return invalid(name+".document", "does not equal the exact retained canonical bytes")
	}
	if HandoffDomainHash(domain, exactBytes) != retainedHash {
		return invalid(name+".hash", "does not authenticate the retained canonical bytes")
	}
	return nil
}

func decodeStrictTransport(encoded []byte, destination any) error {
	if len(encoded) == 0 || len(encoded) > MaximumBundleBytes || !utf8.Valid(encoded) ||
		bytes.HasPrefix(encoded, []byte{0xef, 0xbb, 0xbf}) {
		return invalid("bundle", "must be bounded BOM-free UTF-8")
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return invalid("bundle", "strict decode failed")
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	if err := validateSecretFree("bundle", encoded); err != nil {
		return err
	}
	return nil
}

// validateRawBundleShape preserves the distinction between a required JSON
// member and the same Go zero value produced when that member is absent. The
// database response is a closed protocol: omitting a nullable member is not
// equivalent to retaining it explicitly as null, and omitting false/zero is
// not equivalent to transporting the scalar.
func validateRawBundleShape(encoded []byte, completionResponse bool) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return invalid("bundle", "cannot validate raw member shape")
	}
	root, err := exactRawObject("bundle", decoded, append(
		[]string{"schemaVersion", "handoffId", "completion", "revisionAuthority", "outputRevision", "workflow"},
		map[bool][]string{true: {"idempotent"}}[completionResponse]...,
	))
	if err != nil {
		return err
	}
	if completionResponse {
		if _, ok := root["idempotent"].(bool); !ok {
			return invalid("bundle.idempotent", "must be an explicit boolean")
		}
	}
	completion, err := exactRawObject("bundle.completion", root["completion"], []string{"hash", "bytesHex", "document"})
	if err != nil {
		return err
	}
	authority, err := exactRawObject("bundle.revisionAuthority", root["revisionAuthority"], []string{"hash", "bytesHex", "document"})
	if err != nil {
		return err
	}
	completionDocument, err := exactRawObject("bundle.completion.document", completion["document"], []string{
		"schemaVersion", "handoffId", "operationId", "consumptionHash", "outputRevisionId",
		"outputRevisionContentHash", "projectId", "workflowRunId", "nodeRunId", "nodeKey",
		"publishNodeRunId", "workflowEvents", "outboxEvents", "completedAt",
	})
	if err != nil {
		return err
	}
	if err := exactRawObjectArray("bundle.completion.document.workflowEvents", completionDocument["workflowEvents"], []string{
		"role", "eventId", "eventSequence", "eventType", "nodeRunId", "nodeKey",
	}); err != nil {
		return err
	}
	if err := exactRawObjectArray("bundle.completion.document.outboxEvents", completionDocument["outboxEvents"], []string{
		"role", "outboxEventId", "workflowEventId", "eventType",
	}); err != nil {
		return err
	}
	authorityDocument, err := exactRawObject("bundle.revisionAuthority.document", authority["document"], []string{
		"schemaVersion", "handoffId", "operationId", "outputRevisionId", "workflowInput", "plan",
		"receipt", "promotion", "target", "revisionStateAtHandoff", "copiedLineage",
	})
	if err != nil {
		return err
	}
	for name, names := range map[string][]string{
		"workflowInput":          {"authorityId", "authorityHash"},
		"plan":                   {"authorityId", "authorityHash"},
		"receipt":                {"receiptId", "envelopeHash"},
		"promotion":              {"requestHash", "closureHash", "revisionIntentHash", "consumptionHash"},
		"target":                 {"artifactId", "nodeKey", "nodeRunId", "projectId", "revisionContentHash", "revisionId", "stageGate", "subject", "workflowRunId"},
		"revisionStateAtHandoff": {"workflowStatus", "approvedAt", "supersededAt", "parentWorkflowStatus", "parentApprovedAt", "parentSupersededAt"},
		"copiedLineage":          {"schemaVersion", "rootHash", "sourceCount", "dependencyCount", "traceCount"},
	} {
		if _, err := exactRawObject("bundle.revisionAuthority.document."+name, authorityDocument[name], names); err != nil {
			return err
		}
	}
	output, err := exactRawObject("bundle.outputRevision", root["outputRevision"], []string{
		"id", "artifactId", "parentRevisionId", "revisionNumber", "schemaVersion", "contentStore",
		"contentRef", "contentHash", "byteSize", "stateAtHandoff", "promotionHandoffId", "createdAt",
	})
	if err != nil {
		return err
	}
	if _, err := exactRawObject("bundle.outputRevision.stateAtHandoff", output["stateAtHandoff"], []string{
		"workflowStatus", "approvedAt", "supersededAt",
	}); err != nil {
		return err
	}
	workflow, err := exactRawObject("bundle.workflow", root["workflow"], []string{
		"projectId", "workflowRunId", "gateNodeRunId", "gateNodeKey", "publishNodeRunId", "publishNodeKey",
		"eventCursorBefore", "eventCursorAfter", "qualityResult", "gateStatusAtHandoff",
		"publishStatusAtHandoff", "runStatusAtHandoff",
	})
	if err != nil {
		return err
	}
	quality, err := exactRawObject("bundle.workflow.qualityResult", workflow["qualityResult"], []string{
		"passed", "findings", "qualityRunId", "workspaceRevision", "buildManifest",
	})
	if err != nil {
		return err
	}
	findings, err := exactRawObject("bundle.workflow.qualityResult.findings", quality["findings"], []string{
		"checks", "diagnostics", "qualityRunId", "reportArtifactId", "reportRevisionId", "score", "workspaceRevision",
	})
	if err != nil {
		return err
	}
	manifest, err := exactRawObject("bundle.workflow.qualityResult.buildManifest", quality["buildManifest"], []string{
		"schemaVersion", "projectId", "runId", "manifestGroupKey", "sliceIds", "bundleIds", "sources",
		"constraints", "createdAt", "hash",
	})
	if err != nil {
		return err
	}
	if err := exactRawArtifactReference("bundle.workflow.qualityResult.workspaceRevision", quality["workspaceRevision"]); err != nil {
		return err
	}
	if err := exactRawArtifactReference("bundle.workflow.qualityResult.findings.workspaceRevision", findings["workspaceRevision"]); err != nil {
		return err
	}
	sources, ok := manifest["sources"].([]any)
	if !ok {
		return invalid("bundle.workflow.qualityResult.buildManifest.sources", "must be an explicit array")
	}
	for index, source := range sources {
		if err := exactRawArtifactReference(fmt.Sprintf("bundle.workflow.qualityResult.buildManifest.sources[%d]", index), source); err != nil {
			return err
		}
	}
	return nil
}

func exactRawObject(path string, value any, names []string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok || len(object) != len(names) {
		return nil, invalid(path, "must contain the exact closed member set")
	}
	for _, name := range names {
		if _, exists := object[name]; !exists {
			return nil, invalid(path, "must contain the exact closed member set")
		}
	}
	return object, nil
}

func exactRawObjectArray(path string, value any, names []string) error {
	array, ok := value.([]any)
	if !ok {
		return invalid(path, "must be an explicit array")
	}
	for index, member := range array {
		if _, err := exactRawObject(fmt.Sprintf("%s[%d]", path, index), member, names); err != nil {
			return err
		}
	}
	return nil
}

func exactRawArtifactReference(path string, value any) error {
	object, ok := value.(map[string]any)
	if !ok || len(object) < 3 || len(object) > 4 {
		return invalid(path, "must contain the exact ArtifactReference member set")
	}
	for _, name := range []string{"artifactId", "revisionId", "contentHash"} {
		if _, exists := object[name]; !exists {
			return invalid(path, "must contain the exact ArtifactReference member set")
		}
	}
	if len(object) == 4 {
		anchor, exists := object["anchorId"]
		if !exists {
			return invalid(path, "contains an unknown ArtifactReference member")
		}
		if _, ok := anchor.(string); !ok {
			return invalid(path+".anchorId", "must be a string when present")
		}
	}
	return nil
}

func rejectDuplicateNames(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return invalid("bundle", "is malformed")
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return invalid("bundle", "contains a malformed object name")
			}
			name, ok := nameToken.(string)
			if !ok {
				return invalid("bundle", "contains a non-string object name")
			}
			if _, duplicate := seen[name]; duplicate {
				return invalid("bundle", "contains duplicate field %q", name)
			}
			seen[name] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return invalid("bundle", "contains an unclosed object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return invalid("bundle", "contains an unclosed array")
		}
	default:
		return invalid("bundle", "contains an unexpected delimiter")
	}
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return invalid("bundle", "contains a trailing JSON value")
	}
	return invalid("bundle", "contains trailing data")
}

func invalid(field, format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if field == "" {
		return fmt.Errorf("%w: %s", ErrInvalid, detail)
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, detail)
}
