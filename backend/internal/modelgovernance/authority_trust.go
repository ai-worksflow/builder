package modelgovernance

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type governanceTrustPolicyDocument struct {
	SchemaVersion string                          `json:"schemaVersion"`
	Signers       []governanceTrustSignerDocument `json:"signers"`
}

type governanceTrustSignerDocument struct {
	Identity  string `json:"identity"`
	KeyID     string `json:"keyId"`
	NotAfter  string `json:"notAfter"`
	NotBefore string `json:"notBefore"`
	PublicKey string `json:"publicKey"`
	Role      string `json:"role"`
}

type governanceRevocationAuthorityDocument struct {
	DigestRevocations []governanceTrustRevocationDocument       `json:"digestRevocations"`
	Epoch             uint64                                    `json:"epoch"`
	ExpiresAt         string                                    `json:"expiresAt"`
	IssuedAt          string                                    `json:"issuedAt"`
	SchemaVersion     string                                    `json:"schemaVersion"`
	SignerRevocations []governanceTrustSignerRevocationDocument `json:"signerRevocations"`
}

type governanceTrustRevocationDocument struct {
	Digest     string `json:"digest"`
	ReasonHash string `json:"reasonHash"`
	RevokedAt  string `json:"revokedAt"`
}

type governanceTrustSignerRevocationDocument struct {
	KeyID         string `json:"keyId"`
	PolicyHash    string `json:"policyHash"`
	PublicKeyHash string `json:"publicKeyHash"`
	ReasonHash    string `json:"reasonHash"`
	RevokedAt     string `json:"revokedAt"`
}

// CanonicalGovernanceTrustPolicyJSON commits every immutable signer-policy
// field. PolicyHash itself is omitted to avoid a recursive hash. Operational
// revocations are committed by CanonicalGovernanceRevocationAuthorityJSON.
func CanonicalGovernanceTrustPolicyJSON(policy GovernanceTrustPolicy) ([]byte, error) {
	if err := validateGovernanceTrustPolicyContents(policy); err != nil {
		return nil, err
	}
	document := governanceTrustPolicyDocument{
		SchemaVersion: GovernanceTrustPolicySchemaV1,
		Signers:       make([]governanceTrustSignerDocument, 0, len(policy.Signers)),
	}
	keyIDs := make([]string, 0, len(policy.Signers))
	for keyID := range policy.Signers {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		signer := policy.Signers[keyID]
		document.Signers = append(document.Signers, governanceTrustSignerDocument{
			Identity: signer.Identity, KeyID: keyID, NotAfter: formatGovernanceTime(signer.NotAfter),
			NotBefore: formatGovernanceTime(signer.NotBefore), PublicKey: base64.StdEncoding.EncodeToString(signer.PublicKey),
			Role: signer.Role,
		})
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal canonical trust policy: %v", ErrGovernanceUntrusted, err)
	}
	return encoded, nil
}

func GovernanceTrustPolicyHash(policy GovernanceTrustPolicy) (string, error) {
	encoded, err := CanonicalGovernanceTrustPolicyJSON(policy)
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

// ParseGovernanceTrustPolicy accepts only the canonical immutable public-key
// document and makes its exact content digest the PolicyHash used by subjects.
func ParseGovernanceTrustPolicy(encoded []byte, expectedHash string) (GovernanceTrustPolicy, error) {
	if len(encoded) == 0 || len(encoded) > maximumGovernancePayloadBytes || !validDigest(expectedHash) || sha256Digest(encoded) != expectedHash {
		return GovernanceTrustPolicy{}, fmt.Errorf("%w: trust policy bytes or hash are invalid", ErrGovernanceUntrusted)
	}
	var document governanceTrustPolicyDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return GovernanceTrustPolicy{}, fmt.Errorf("%w: decode trust policy: %v", ErrGovernanceUntrusted, err)
	}
	if document.SchemaVersion != GovernanceTrustPolicySchemaV1 || document.Signers == nil {
		return GovernanceTrustPolicy{}, fmt.Errorf("%w: trust policy schema or signer array is invalid", ErrGovernanceUntrusted)
	}
	policy := GovernanceTrustPolicy{PolicyHash: expectedHash, Signers: make(map[string]GovernanceSignerTrust, len(document.Signers))}
	priorKeyID := ""
	for index, wire := range document.Signers {
		if index > 0 && priorKeyID >= wire.KeyID {
			return GovernanceTrustPolicy{}, fmt.Errorf("%w: trust signers are not strictly sorted", ErrGovernanceUntrusted)
		}
		priorKeyID = wire.KeyID
		publicKey, err := decodeCanonicalGovernanceBase64(wire.PublicKey, ed25519.PublicKeySize, ed25519.PublicKeySize)
		if err != nil {
			return GovernanceTrustPolicy{}, fmt.Errorf("%w: signer %q public key is invalid", ErrGovernanceUntrusted, wire.KeyID)
		}
		notBefore, err := parseGovernanceTime(wire.NotBefore, "trust.signer.notBefore")
		if err != nil {
			return GovernanceTrustPolicy{}, fmt.Errorf("%w: %v", ErrGovernanceUntrusted, err)
		}
		notAfter, err := parseGovernanceTime(wire.NotAfter, "trust.signer.notAfter")
		if err != nil {
			return GovernanceTrustPolicy{}, fmt.Errorf("%w: %v", ErrGovernanceUntrusted, err)
		}
		policy.Signers[wire.KeyID] = GovernanceSignerTrust{
			Identity: wire.Identity, Role: wire.Role, PublicKey: ed25519.PublicKey(bytes.Clone(publicKey)),
			NotBefore: notBefore, NotAfter: notAfter,
		}
	}
	canonical, err := CanonicalGovernanceTrustPolicyJSON(policy)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return GovernanceTrustPolicy{}, fmt.Errorf("%w: trust policy is not canonical JSON", ErrGovernanceUntrusted)
	}
	if err := ValidateGovernanceTrustPolicy(policy); err != nil {
		return GovernanceTrustPolicy{}, err
	}
	return policy, nil
}

// CanonicalGovernanceRevocationAuthorityJSON commits the current cumulative
// revocation epoch without coupling it to immutable signed subjects.
func CanonicalGovernanceRevocationAuthorityJSON(authority GovernanceRevocationAuthority) ([]byte, error) {
	if err := validateGovernanceRevocationAuthorityContents(authority); err != nil {
		return nil, err
	}
	document := governanceRevocationAuthorityDocument{
		DigestRevocations: make([]governanceTrustRevocationDocument, len(authority.DigestRevocations)),
		Epoch:             authority.Epoch,
		ExpiresAt:         formatGovernanceTime(authority.ExpiresAt),
		IssuedAt:          formatGovernanceTime(authority.IssuedAt),
		SchemaVersion:     GovernanceRevocationSchemaV1,
		SignerRevocations: make([]governanceTrustSignerRevocationDocument, len(authority.SignerRevocations)),
	}
	for index, revocation := range authority.DigestRevocations {
		document.DigestRevocations[index] = governanceTrustRevocationDocument{
			Digest: revocation.Digest, ReasonHash: revocation.ReasonHash, RevokedAt: formatGovernanceTime(revocation.RevokedAt),
		}
	}
	for index, revocation := range authority.SignerRevocations {
		document.SignerRevocations[index] = governanceTrustSignerRevocationDocument{
			KeyID: revocation.KeyID, PolicyHash: revocation.PolicyHash, PublicKeyHash: revocation.PublicKeyHash,
			ReasonHash: revocation.ReasonHash, RevokedAt: formatGovernanceTime(revocation.RevokedAt),
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal canonical revocation authority: %v", ErrGovernanceUntrusted, err)
	}
	return encoded, nil
}

func GovernanceRevocationAuthorityHash(authority GovernanceRevocationAuthority) (string, error) {
	encoded, err := CanonicalGovernanceRevocationAuthorityJSON(authority)
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

func ParseGovernanceRevocationAuthority(encoded []byte, expectedHash string) (GovernanceRevocationAuthority, error) {
	if len(encoded) == 0 || len(encoded) > maximumGovernancePayloadBytes || !validDigest(expectedHash) || sha256Digest(encoded) != expectedHash {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: revocation authority bytes or hash are invalid", ErrGovernanceUntrusted)
	}
	var document governanceRevocationAuthorityDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: decode revocation authority: %v", ErrGovernanceUntrusted, err)
	}
	if document.SchemaVersion != GovernanceRevocationSchemaV1 || document.DigestRevocations == nil || document.SignerRevocations == nil {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: revocation authority schema or arrays are invalid", ErrGovernanceUntrusted)
	}
	issuedAt, err := parseGovernanceTime(document.IssuedAt, "revocationAuthority.issuedAt")
	if err != nil {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: %v", ErrGovernanceUntrusted, err)
	}
	expiresAt, err := parseGovernanceTime(document.ExpiresAt, "revocationAuthority.expiresAt")
	if err != nil {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: %v", ErrGovernanceUntrusted, err)
	}
	authority := GovernanceRevocationAuthority{
		AuthorityHash: expectedHash, Epoch: document.Epoch, IssuedAt: issuedAt, ExpiresAt: expiresAt,
		DigestRevocations: make([]GovernanceRevocation, len(document.DigestRevocations)),
		SignerRevocations: make([]GovernanceSignerRevocation, len(document.SignerRevocations)),
	}
	for index, wire := range document.DigestRevocations {
		revokedAt, parseErr := parseGovernanceTime(wire.RevokedAt, "revocationAuthority.digestRevocations.revokedAt")
		if parseErr != nil {
			return GovernanceRevocationAuthority{}, fmt.Errorf("%w: %v", ErrGovernanceUntrusted, parseErr)
		}
		authority.DigestRevocations[index] = GovernanceRevocation{Digest: wire.Digest, ReasonHash: wire.ReasonHash, RevokedAt: revokedAt}
	}
	for index, wire := range document.SignerRevocations {
		revokedAt, parseErr := parseGovernanceTime(wire.RevokedAt, "revocationAuthority.signerRevocations.revokedAt")
		if parseErr != nil {
			return GovernanceRevocationAuthority{}, fmt.Errorf("%w: %v", ErrGovernanceUntrusted, parseErr)
		}
		authority.SignerRevocations[index] = GovernanceSignerRevocation{
			PolicyHash: wire.PolicyHash, KeyID: wire.KeyID, PublicKeyHash: wire.PublicKeyHash,
			ReasonHash: wire.ReasonHash, RevokedAt: revokedAt,
		}
	}
	canonical, err := CanonicalGovernanceRevocationAuthorityJSON(authority)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: revocation authority is not canonical JSON", ErrGovernanceUntrusted)
	}
	if err := ValidateGovernanceRevocationAuthority(authority, issuedAt); err != nil {
		return GovernanceRevocationAuthority{}, err
	}
	return authority, nil
}

func canonicalGovernanceTime(value time.Time) bool {
	if value.IsZero() || value.Nanosecond()%int(time.Millisecond) != 0 {
		return false
	}
	parsed, err := time.Parse(governanceTimeLayout, formatGovernanceTime(value))
	return err == nil && value.UTC().Equal(parsed)
}

func normalizeGovernanceTrustedTime(value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, fmt.Errorf("%w: trusted time is zero", ErrRuntimeAuthority)
	}
	return value.UTC().Truncate(time.Millisecond), nil
}
