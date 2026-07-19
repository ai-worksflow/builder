package credentialset

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type issuancePredicate struct {
	Audience             string          `json:"audience"`
	ExpiresAt            string          `json:"expiresAt"`
	FixtureID            string          `json:"fixtureId"`
	IssuedAt             string          `json:"issuedAt"`
	Issuer               string          `json:"issuer"`
	MemberBindingsDigest string          `json:"memberBindingsDigest"`
	MemberCount          int             `json:"memberCount"`
	Members              []MemberBinding `json:"members"`
	RunID                string          `json:"runId"`
	SchemaVersion        string          `json:"schemaVersion"`
	SetHandleHash        string          `json:"setHandleHash"`
	Status               string          `json:"status"`
}

type revocationPredicate struct {
	Audience             string          `json:"audience"`
	ExpiresAt            string          `json:"expiresAt"`
	FixtureID            string          `json:"fixtureId"`
	IssuedAt             string          `json:"issuedAt"`
	Issuer               string          `json:"issuer"`
	MemberBindingsDigest string          `json:"memberBindingsDigest"`
	MemberCount          int             `json:"memberCount"`
	Members              []MemberBinding `json:"members"`
	RevokedAt            string          `json:"revokedAt"`
	RunID                string          `json:"runId"`
	SchemaVersion        string          `json:"schemaVersion"`
	SetHandleHash        string          `json:"setHandleHash"`
	Status               string          `json:"status"`
}

type envelopeSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type dsseEnvelope struct {
	Payload     string              `json:"payload"`
	PayloadType string              `json:"payloadType"`
	Signatures  []envelopeSignature `json:"signatures"`
}

func credentialSubject(binding SetBinding) (string, string) {
	digest := strings.TrimPrefix(binding.SetHandleHash, "sha256:")
	return "worksflow-credential-set/" + digest, digest
}

func canonicalStatement(predicateType string, binding SetBinding, predicate any) ([]byte, error) {
	if err := ValidateBinding(binding); err != nil {
		return nil, err
	}
	name, digest := credentialSubject(binding)
	// Maps are intentional: encoding/json orders their keys by UTF-8 bytes,
	// producing the repository's canonical JSON form independent of struct
	// declaration order.
	statement := map[string]any{
		"_type":         templateauthority.InTotoStatementV1,
		"predicate":     predicate,
		"predicateType": predicateType,
		"subject": []any{map[string]any{
			"digest": map[string]string{"sha256": digest},
			"name":   name,
		}},
	}
	encoded, err := json.Marshal(statement)
	if err != nil {
		return nil, fmt.Errorf("%w: encode in-toto statement: %v", ErrInvalid, err)
	}
	return encoded, nil
}

// CanonicalIssuancePayload builds bytes accepted by qualificationreceipt's
// credential-set v1 verifier. SetID stays in the ledger/Fixture identity and is
// intentionally absent from the already-frozen predicate schema.
func CanonicalIssuancePayload(binding SetBinding) ([]byte, error) {
	predicate := issuancePredicate{
		Audience: binding.Audience, ExpiresAt: binding.ExpiresAt, FixtureID: binding.FixtureID,
		IssuedAt: binding.IssuedAt, Issuer: binding.Issuer,
		MemberBindingsDigest: binding.MemberBindingsDigest, MemberCount: binding.MemberCount,
		Members: cloneMembers(binding.Members), RunID: binding.RunID, SchemaVersion: IssuanceSchemaV1,
		SetHandleHash: binding.SetHandleHash, Status: "issued",
	}
	return canonicalStatement(IssuancePredicateTypeV1, binding, predicate)
}

func CanonicalRevocationPayload(binding SetBinding, revokedAt string) ([]byte, error) {
	issuedAt, err := parseCanonicalTime(binding.IssuedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid binding issue time", ErrInvalid)
	}
	expiresAt, _ := parseCanonicalTime(binding.ExpiresAt)
	revoked, err := parseCanonicalTime(revokedAt)
	if err != nil || !revoked.After(issuedAt) || !revoked.Before(expiresAt) {
		return nil, fmt.Errorf("%w: revocation must be after issuance and before expiry", ErrInvalid)
	}
	predicate := revocationPredicate{
		Audience: binding.Audience, ExpiresAt: binding.ExpiresAt, FixtureID: binding.FixtureID,
		IssuedAt: binding.IssuedAt, Issuer: binding.Issuer,
		MemberBindingsDigest: binding.MemberBindingsDigest, MemberCount: binding.MemberCount,
		Members: cloneMembers(binding.Members), RevokedAt: revokedAt, RunID: binding.RunID,
		SchemaVersion: RevocationSchemaV1, SetHandleHash: binding.SetHandleHash, Status: "revoked",
	}
	return canonicalStatement(RevocationPredicateTypeV1, binding, predicate)
}

func signingOperationID(operationID, suffix string) string {
	return "credential-sign/" + operationID + "/" + suffix
}

func buildSignRequest(operationID string, payload []byte) SignRequest {
	return SignRequest{
		OperationID:   signingOperationID(operationID, "attestation"),
		PAE:           append([]byte(nil), templateauthority.DSSEPAE(InTotoPayloadType, payload)...),
		PayloadDigest: sha256Digest(payload), PayloadType: InTotoPayloadType,
	}
}

func signAttestation(ctx context.Context, signer Signer, request SignRequest, payload []byte, inspect bool) (Attestation, error) {
	if len(payload) == 0 || sha256Digest(payload) != request.PayloadDigest ||
		!bytesEqual(templateauthority.DSSEPAE(request.PayloadType, payload), request.PAE) {
		return Attestation{}, fmt.Errorf("%w: signing request PAE is invalid", ErrInvalid)
	}
	var (
		observation SignObservation
		err         error
	)
	if inspect {
		observation, err = signer.Inspect(ctx, request.OperationID)
	} else {
		observation, err = signer.Sign(ctx, request)
	}
	if err != nil {
		return Attestation{}, ErrOutcomeUnknown
	}
	if observation.OperationID != request.OperationID || !validStableID(observation.KeyID) || len(observation.Signature) < 1 || len(observation.Signature) > 16<<10 {
		return Attestation{}, fmt.Errorf("%w: signer observation does not close over the reserved operation", ErrSignerRejected)
	}
	envelope := dsseEnvelope{
		Payload:     base64.StdEncoding.EncodeToString(payload),
		PayloadType: request.PayloadType,
		Signatures:  []envelopeSignature{{KeyID: observation.KeyID, Sig: base64.StdEncoding.EncodeToString(observation.Signature)}},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return Attestation{}, fmt.Errorf("%w: encode DSSE envelope: %v", ErrInvalid, err)
	}
	return Attestation{
		Envelope: encoded, EnvelopeDigest: sha256Digest(encoded), KeyID: observation.KeyID,
		Payload: append([]byte(nil), payload...), PayloadDigest: request.PayloadDigest,
	}, nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
