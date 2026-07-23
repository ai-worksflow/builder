package canonicalreviewreceipt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	hashPrefix       = "worksflow-canonical-review-authority-hash/v1"
	canonicalTime    = "2006-01-02T15:04:05.000000Z"
	maximumWireBytes = 1 << 20
	maximumSafeInt   = int64(9007199254740991)
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var ErrInvalid = errors.New("invalid canonical review approval receipt")

func CanonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrInvalid, err)
	}
	if len(encoded) == 0 || len(encoded) > maximumWireBytes || !utf8.Valid(encoded) {
		return nil, fmt.Errorf("%w: size or UTF-8", ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrInvalid, err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	if err := validateCanonicalValue(generic); err != nil {
		return nil, err
	}
	canonical := make([]byte, 0, len(encoded))
	canonical, err = appendCanonicalJSON(canonical, generic)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 || len(canonical) > maximumWireBytes {
		return nil, fmt.Errorf("%w: canonical size", ErrInvalid)
	}
	return canonical, nil
}

// appendCanonicalJSON deliberately does not use encoding/json for strings.
// encoding/json escapes HTML metacharacters and U+2028/U+2029, while the
// PostgreSQL JSONB canonicalizer preserves those valid UTF-8 characters. This
// writer is the byte contract shared by the two implementations: object names
// are UTF-8 byte ordered and strings escape only JSON syntax and control bytes.
func appendCanonicalJSON(destination []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return append(destination, "null"...), nil
	case bool:
		return strconv.AppendBool(destination, typed), nil
	case string:
		return appendCanonicalJSONString(destination, typed), nil
	case json.Number:
		return append(destination, typed.String()...), nil
	case []any:
		destination = append(destination, '[')
		for index, item := range typed {
			if index > 0 {
				destination = append(destination, ',')
			}
			var err error
			destination, err = appendCanonicalJSON(destination, item)
			if err != nil {
				return nil, err
			}
		}
		return append(destination, ']'), nil
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		destination = append(destination, '{')
		for index, name := range names {
			if index > 0 {
				destination = append(destination, ',')
			}
			destination = appendCanonicalJSONString(destination, name)
			destination = append(destination, ':')
			var err error
			destination, err = appendCanonicalJSON(destination, typed[name])
			if err != nil {
				return nil, err
			}
		}
		return append(destination, '}'), nil
	default:
		return nil, fmt.Errorf("%w: unsupported canonical JSON type", ErrInvalid)
	}
}

func appendCanonicalJSONString(destination []byte, value string) []byte {
	const hexadecimal = "0123456789abcdef"
	destination = append(destination, '"')
	for index := 0; index < len(value); {
		character := value[index]
		if character >= utf8.RuneSelf {
			_, size := utf8.DecodeRuneInString(value[index:])
			destination = append(destination, value[index:index+size]...)
			index += size
			continue
		}
		index++
		switch character {
		case '"', '\\':
			destination = append(destination, '\\', character)
		case '\b':
			destination = append(destination, '\\', 'b')
		case '\f':
			destination = append(destination, '\\', 'f')
		case '\n':
			destination = append(destination, '\\', 'n')
		case '\r':
			destination = append(destination, '\\', 'r')
		case '\t':
			destination = append(destination, '\\', 't')
		default:
			if character < 0x20 {
				destination = append(destination, '\\', 'u', '0', '0', hexadecimal[character>>4], hexadecimal[character&0x0f])
			} else {
				destination = append(destination, character)
			}
		}
	}
	return append(destination, '"')
}

func DomainHash(domain string, value []byte) string {
	material := make([]byte, 0, len(hashPrefix)+len(domain)+len(value)+2)
	material = append(material, hashPrefix...)
	material = append(material, 0)
	material = append(material, domain...)
	material = append(material, 0)
	material = append(material, value...)
	digest := sha256.Sum256(material)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func Compile(receipt Receipt) (Compiled, error) {
	receipt.SchemaVersion = ReceiptSchemaVersion
	receipt.MediaType = ReceiptMediaType
	receipt.ReviewRequest.SchemaVersion = ReviewRequestSchemaVersion
	receipt.Revision.SchemaVersion = RevisionSchemaVersion
	receipt.Policy.SchemaVersion = PolicySchemaVersion
	receipt.Decisions.SchemaVersion = DecisionsSchemaVersion
	receipt.Governance.SchemaVersion = GovernanceSchemaVersion
	receipt.Approval.SchemaVersion = ApprovalSchemaVersion

	parts := map[string][]byte{}
	partValues := []struct {
		name   string
		domain string
		value  any
		set    func(string)
	}{
		{"reviewRequest", ReviewRequestHashDomain, receipt.ReviewRequest, func(v string) { receipt.ComponentDigests.ReviewRequest = v }},
		{"revision", RevisionHashDomain, receipt.Revision, func(v string) { receipt.ComponentDigests.Revision = v }},
		{"policy", PolicyHashDomain, receipt.Policy, func(v string) { receipt.ComponentDigests.Policy = v }},
		{"decisions", DecisionsHashDomain, receipt.Decisions, func(v string) { receipt.ComponentDigests.Decisions = v }},
		{"governance", GovernanceHashDomain, receipt.Governance, func(v string) { receipt.ComponentDigests.Governance = v }},
		{"approval", ApprovalHashDomain, receipt.Approval, func(v string) { receipt.ComponentDigests.Approval = v }},
	}
	for _, part := range partValues {
		encoded, err := CanonicalJSON(part.value)
		if err != nil {
			return Compiled{}, err
		}
		parts[part.name] = encoded
		part.set(DomainHash(part.domain, encoded))
	}
	if err := validateReceipt(receipt); err != nil {
		return Compiled{}, err
	}
	encoded, err := CanonicalJSON(receipt)
	if err != nil {
		return Compiled{}, err
	}
	return Compiled{Receipt: receipt, Bytes: encoded, Hash: DomainHash(ReceiptHashDomain, encoded), Parts: parts}, nil
}

func Decode(encoded []byte, expectedHash string) (Receipt, error) {
	if len(encoded) == 0 || len(encoded) > maximumWireBytes || !utf8.Valid(encoded) {
		return Receipt{}, fmt.Errorf("%w: size or UTF-8", ErrInvalid)
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return Receipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var receipt Receipt
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, fmt.Errorf("%w: strict decode: %v", ErrInvalid, err)
	}
	if err := requireEOF(decoder); err != nil {
		return Receipt{}, err
	}
	compiled, err := Compile(receipt)
	if err != nil {
		return Receipt{}, err
	}
	if !bytes.Equal(encoded, compiled.Bytes) {
		return Receipt{}, fmt.Errorf("%w: wire is not exact canonical JSON", ErrInvalid)
	}
	if expectedHash != "" && (!digestPattern.MatchString(expectedHash) || compiled.Hash != expectedHash) {
		return Receipt{}, fmt.Errorf("%w: receipt hash mismatch", ErrInvalid)
	}
	return compiled.Receipt, nil
}

// StrictDecode rejects duplicate and unknown object names. It is shared by
// the review-policy reader so a widened durable policy cannot be silently
// interpreted as a narrower approval contract.
func StrictDecode(encoded []byte, destination any) error {
	if len(encoded) == 0 || len(encoded) > maximumWireBytes || !utf8.Valid(encoded) {
		return fmt.Errorf("%w: size or UTF-8", ErrInvalid)
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: strict decode: %v", ErrInvalid, err)
	}
	return requireEOF(decoder)
}

func validateReceipt(value Receipt) error {
	if value.SchemaVersion != ReceiptSchemaVersion || value.MediaType != ReceiptMediaType {
		return fmt.Errorf("%w: root type", ErrInvalid)
	}
	if value.ReviewRequest.SchemaVersion != ReviewRequestSchemaVersion || value.ReviewRequest.ReviewAuthorityVersion != 1 ||
		value.ReviewRequest.Status != "approved" || value.Revision.SchemaVersion != RevisionSchemaVersion ||
		value.Revision.WorkflowStatus != "approved" || value.Policy.SchemaVersion != PolicySchemaVersion ||
		value.Decisions.SchemaVersion != DecisionsSchemaVersion || value.Governance.SchemaVersion != GovernanceSchemaVersion ||
		value.Approval.SchemaVersion != ApprovalSchemaVersion {
		return fmt.Errorf("%w: component type or state", ErrInvalid)
	}
	ids := []string{value.ReviewRequest.ID, value.ReviewRequest.ProjectID, value.ReviewRequest.ArtifactID,
		value.ReviewRequest.RevisionID, value.ReviewRequest.RequestedBy, value.ReviewRequest.ClosedByDecisionID,
		value.Revision.ID, value.Revision.ArtifactID, value.Revision.CreatedBy, value.Approval.ClosedByDecisionID,
		value.Approval.ProjectID, value.Approval.ArtifactID, value.Approval.RevisionID,
		value.Approval.ArtifactLatestApprovedID, value.Approval.ArtifactLatestRevisionID, value.Approval.SubjectAuthorID}
	for _, id := range ids {
		if !validUUID(id) {
			return fmt.Errorf("%w: UUID", ErrInvalid)
		}
	}
	if !digestPattern.MatchString(value.ReviewRequest.ContentHash) || value.ReviewRequest.ContentHash != value.Revision.ContentHash ||
		value.Approval.RevisionContentHash != value.Revision.ContentHash || value.ReviewRequest.RevisionID != value.Revision.ID ||
		value.ReviewRequest.ArtifactID != value.Revision.ArtifactID || value.Approval.RevisionID != value.Revision.ID ||
		value.Approval.ProjectID != value.ReviewRequest.ProjectID || value.Approval.ArtifactID != value.Revision.ArtifactID ||
		value.Approval.SubjectAuthorID != value.Revision.CreatedBy || value.Approval.ClosedByDecisionID != value.ReviewRequest.ClosedByDecisionID {
		return fmt.Errorf("%w: target closure", ErrInvalid)
	}
	if value.Approval.ArtifactLatestApprovedID != value.Revision.ID || value.Approval.ArtifactLatestRevisionID != value.Revision.ID ||
		value.Approval.ArtifactLifecycle != "active" || value.Approval.ArtifactVersion < 1 ||
		value.Approval.ArtifactVersion > maximumSafeInt || !validArtifactKind(value.Approval.ArtifactKind) {
		return fmt.Errorf("%w: artifact closure", ErrInvalid)
	}
	if value.Revision.RevisionNumber < 1 || value.Revision.RevisionNumber > maximumSafeInt ||
		value.Revision.ArtifactSchemaVersion < 1 || int64(value.Revision.ArtifactSchemaVersion) > maximumSafeInt ||
		value.Revision.ByteSize < 0 || value.Revision.ByteSize > maximumSafeInt || value.Revision.SupersededAt != nil ||
		!validBoundedString(value.Revision.ContentStore, 1, 128) || !validBoundedString(value.Revision.ContentRef, 1, 65536) ||
		!validBoundedString(value.Revision.ChangeSummary, 0, 4096) || !validChangeSource(value.Revision.ChangeSource) ||
		!validOptionalUUID(value.Revision.ParentRevisionID) || !validOptionalUUID(value.Revision.SourceManifestID) ||
		!validOptionalUUID(value.Revision.ProposalID) || !validOptionalUUID(value.Revision.ImplementationProposalID) {
		return fmt.Errorf("%w: revision closure", ErrInvalid)
	}
	policy := value.Policy.Value
	if policy.MinimumApprovals < 1 || policy.MinimumApprovals > 20 || !policy.ProhibitSelfReview ||
		value.Approval.MinimumApprovals != value.Policy.Value.MinimumApprovals || value.Approval.ApprovalCount != value.Policy.Value.MinimumApprovals ||
		len(value.Decisions.Decisions) != value.Approval.ApprovalCount || len(value.Approval.ApprovalDecisionIDs) != value.Approval.ApprovalCount ||
		value.Decisions.Decisions == nil || value.Approval.ApprovalDecisionIDs == nil || policy.ReviewerIDs == nil ||
		len(policy.ReviewerIDs) > 20 || (len(policy.ReviewerIDs) > 0 && policy.MinimumApprovals > len(policy.ReviewerIDs)) {
		return fmt.Errorf("%w: approval threshold", ErrInvalid)
	}
	if policy.GovernanceMode != value.Governance.Mode || (policy.GovernanceMode != "solo" && policy.GovernanceMode != "team") {
		return fmt.Errorf("%w: policy governance", ErrInvalid)
	}
	reviewers := make(map[string]struct{}, len(policy.ReviewerIDs))
	for _, reviewerID := range policy.ReviewerIDs {
		if !validUUID(reviewerID) {
			return fmt.Errorf("%w: policy reviewer", ErrInvalid)
		}
		if _, duplicate := reviewers[reviewerID]; duplicate {
			return fmt.Errorf("%w: duplicate policy reviewer", ErrInvalid)
		}
		reviewers[reviewerID] = struct{}{}
	}
	if policy.SoloSelfReviewOwnerID != nil {
		if policy.GovernanceMode != "solo" || !validUUID(*policy.SoloSelfReviewOwnerID) ||
			value.Governance.SoleOwnerID == nil || *policy.SoloSelfReviewOwnerID != *value.Governance.SoleOwnerID ||
			*policy.SoloSelfReviewOwnerID != value.Revision.CreatedBy {
			return fmt.Errorf("%w: policy Solo Owner", ErrInvalid)
		}
		if _, assigned := reviewers[*policy.SoloSelfReviewOwnerID]; !assigned {
			return fmt.Errorf("%w: unassigned policy Solo Owner", ErrInvalid)
		}
	} else if value.Approval.SoloSelfReview {
		return fmt.Errorf("%w: missing policy Solo Owner", ErrInvalid)
	}
	if !validTime(value.IssuedAt) || !validTime(value.ReviewRequest.RequestedAt) || !validTime(value.ReviewRequest.ClosedAt) ||
		!validTime(value.Revision.CreatedAt) || !validTime(value.Revision.ApprovedAt) || value.IssuedAt != value.ReviewRequest.ClosedAt ||
		value.Revision.ApprovedAt != value.ReviewRequest.ClosedAt || value.Approval.ApprovedAt != value.ReviewRequest.ClosedAt ||
		value.Revision.CreatedAt > value.ReviewRequest.RequestedAt || value.ReviewRequest.RequestedAt > value.ReviewRequest.ClosedAt ||
		value.Revision.CreatedAt > value.Revision.ApprovedAt {
		return fmt.Errorf("%w: time closure", ErrInvalid)
	}
	seenIDs := map[string]struct{}{}
	seenReviewers := map[string]struct{}{}
	previous := ""
	previousCreatedAtNano := int64(0)
	anySolo := false
	for index, decision := range value.Decisions.Decisions {
		facts := decision.AuthorityFacts
		expectedPrecondition := fmt.Sprintf(`"review:%s:open:%d:%d"`, value.ReviewRequest.ID, index, previousCreatedAtNano)
		if !validUUID(decision.ID) || !validUUID(decision.ReviewerID) || decision.Decision != "approve" || facts.Version != 1 ||
			!validTime(decision.CreatedAt) || decision.CreatedAt < value.ReviewRequest.RequestedAt || decision.CreatedAt > value.ReviewRequest.ClosedAt ||
			!validBoundedString(decision.Summary, 0, 4096) || strings.TrimSpace(decision.Summary) != decision.Summary ||
			facts.PreconditionETag != expectedPrecondition ||
			!validReviewerRole(facts.ReviewerRole) || facts.GovernanceMode != value.Governance.Mode ||
			facts.OwnerCount != value.Governance.OwnerCount || !equalOptionalString(facts.SoleOwnerID, value.Governance.SoleOwnerID) {
			return fmt.Errorf("%w: decision", ErrInvalid)
		}
		createdAt, err := time.Parse(canonicalTime, decision.CreatedAt)
		if err != nil {
			return fmt.Errorf("%w: decision time", ErrInvalid)
		}
		if createdAtNano := createdAt.UnixNano(); createdAtNano > previousCreatedAtNano {
			previousCreatedAtNano = createdAtNano
		}
		if len(reviewers) > 0 {
			if _, assigned := reviewers[decision.ReviewerID]; !assigned {
				return fmt.Errorf("%w: decision reviewer is not assigned", ErrInvalid)
			}
		}
		orderKey := decision.CreatedAt + "\x00" + decision.ID
		if previous != "" && previous >= orderKey {
			return fmt.Errorf("%w: decision order", ErrInvalid)
		}
		previous = orderKey
		if _, exists := seenIDs[decision.ID]; exists || value.Approval.ApprovalDecisionIDs[index] != decision.ID {
			return fmt.Errorf("%w: decision identity", ErrInvalid)
		}
		seenIDs[decision.ID] = struct{}{}
		if _, exists := seenReviewers[decision.ReviewerID]; exists {
			return fmt.Errorf("%w: duplicate decision reviewer", ErrInvalid)
		}
		seenReviewers[decision.ReviewerID] = struct{}{}
		if decision.SoloSelfReview {
			anySolo = true
			if facts.ReviewerRole != "owner" || facts.GovernanceMode != "solo" || facts.OwnerCount != 1 ||
				facts.SoleOwnerID == nil || *facts.SoleOwnerID != decision.ReviewerID || !facts.ExplicitConfirmation ||
				decision.ReviewerID != value.Revision.CreatedBy || strings.TrimSpace(decision.Summary) == "" ||
				policy.SoloSelfReviewOwnerID == nil || *policy.SoloSelfReviewOwnerID != decision.ReviewerID {
				return fmt.Errorf("%w: solo owner proof", ErrInvalid)
			}
		} else if facts.ExplicitConfirmation || decision.ReviewerID == value.Revision.CreatedBy {
			return fmt.Errorf("%w: spurious solo confirmation", ErrInvalid)
		}
	}
	if _, ok := seenIDs[value.ReviewRequest.ClosedByDecisionID]; !ok ||
		value.Decisions.Decisions[len(value.Decisions.Decisions)-1].ID != value.ReviewRequest.ClosedByDecisionID ||
		value.Decisions.Decisions[len(value.Decisions.Decisions)-1].CreatedAt != value.ReviewRequest.ClosedAt ||
		value.Approval.SoloSelfReview != anySolo {
		return fmt.Errorf("%w: closing decision", ErrInvalid)
	}
	if value.Governance.Mode != "solo" && value.Governance.Mode != "team" || value.Governance.OwnerCount < 1 ||
		value.Governance.OwnerCount > 1000000 || (value.Governance.OwnerCount == 1) != (value.Governance.SoleOwnerID != nil) ||
		!validOptionalUUID(value.Governance.SoleOwnerID) {
		return fmt.Errorf("%w: governance", ErrInvalid)
	}
	for _, digest := range []string{value.ComponentDigests.ReviewRequest, value.ComponentDigests.Revision, value.ComponentDigests.Policy,
		value.ComponentDigests.Decisions, value.ComponentDigests.Governance, value.ComponentDigests.Approval} {
		if !digestPattern.MatchString(digest) {
			return fmt.Errorf("%w: component digest", ErrInvalid)
		}
	}
	return nil
}

func validBoundedString(value string, minimum, maximum int) bool {
	length := len([]byte(value))
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && length >= minimum && length <= maximum
}

func validChangeSource(value string) bool {
	switch value {
	case "human", "ai_proposal", "import", "merge", "rollback", "system":
		return true
	default:
		return false
	}
}

func validArtifactKind(value string) bool {
	switch value {
	case "project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source",
		"change_request", "requirement_baseline", "blueprint", "page_spec", "prototype", "prototype_flow",
		"fixture_bundle", "design_system", "token_set", "component_registry", "api_contract", "data_contract",
		"permission_contract", "ai_runtime_contract", "deployment_contract", "verification_contract",
		"workspace", "test_report", "quality_report":
		return true
	default:
		return false
	}
}

func validReviewerRole(value string) bool {
	return value == "owner" || value == "admin" || value == "editor"
}

func validOptionalUUID(value *string) bool {
	return value == nil || validUUID(*value)
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validTime(value string) bool {
	parsed, err := time.Parse(canonicalTime, value)
	return err == nil && parsed.Year() >= 1678 && parsed.Year() < 2262 &&
		parsed.UTC().Format(canonicalTime) == value
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing JSON: %v", ErrInvalid, err)
	}
	return fmt.Errorf("%w: multiple JSON values", ErrInvalid)
}

func validateCanonicalValue(value any) error {
	switch typed := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(typed) || strings.ContainsRune(typed, '\x00') {
			return fmt.Errorf("%w: string", ErrInvalid)
		}
		return nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE+") || text == "-0" {
			return fmt.Errorf("%w: number", ErrInvalid)
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil || integer < -maximumSafeInt || integer > maximumSafeInt {
			return fmt.Errorf("%w: number range", ErrInvalid)
		}
		return nil
	case []any:
		for _, item := range typed {
			if err := validateCanonicalValue(item); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for name, item := range typed {
			if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
				return fmt.Errorf("%w: object name", ErrInvalid)
			}
			if err := validateCanonicalValue(item); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported JSON type", ErrInvalid)
	}
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
		return fmt.Errorf("%w: tokenize: %v", ErrInvalid, err)
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("%w: object name: %v", ErrInvalid, err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("%w: object name", ErrInvalid)
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("%w: duplicate field %q", ErrInvalid, name)
			}
			seen[name] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("%w: object end", ErrInvalid)
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("%w: array end", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: delimiter", ErrInvalid)
	}
	return nil
}
