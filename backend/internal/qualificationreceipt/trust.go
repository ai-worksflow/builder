package qualificationreceipt

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type trustPolicyDocument struct {
	SchemaVersion        string                        `json:"schemaVersion"`
	MinimumSignatures    int                           `json:"minimumSignatures"`
	MaxReceiptAgeSeconds int64                         `json:"maxReceiptAgeSeconds"`
	MaxFutureSkewSeconds int64                         `json:"maxFutureSkewSeconds"`
	Signers              []trustSignerDocument         `json:"signers"`
	CredentialIssuers    []credentialIssuerDocument    `json:"credentialIssuers"`
	EncryptionRecipients []encryptionRecipientDocument `json:"encryptionRecipients"`
	EncryptionAuthority  encryptionAuthorityDocument   `json:"encryptionAuthority"`
	FaultAuthority       encryptionAuthorityDocument   `json:"faultAuthority"`
	FaultLedgerAttestor  encryptionAuthorityDocument   `json:"faultLedgerAttestor"`
}

type trustSignerDocument struct {
	KeyID        string `json:"keyId"`
	Algorithm    string `json:"algorithm"`
	Identity     string `json:"identity"`
	Role         string `json:"role"`
	NotBefore    string `json:"notBefore"`
	NotAfter     string `json:"notAfter"`
	RevokedAt    string `json:"revokedAt,omitempty"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

type credentialIssuerDocument struct {
	Issuer            string                  `json:"issuer"`
	MinimumSignatures int                     `json:"minimumSignatures"`
	AllowedIdentities []string                `json:"allowedIdentities"`
	Keys              []credentialKeyDocument `json:"keys"`
}

type credentialKeyDocument struct {
	KeyID        string `json:"keyId"`
	Algorithm    string `json:"algorithm"`
	Identity     string `json:"identity"`
	NotBefore    string `json:"notBefore"`
	NotAfter     string `json:"notAfter"`
	RevokedAt    string `json:"revokedAt,omitempty"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

type encryptionRecipientDocument struct {
	KeyResource string `json:"keyResource"`
	KeyVersion  string `json:"keyVersion"`
}

type encryptionAuthorityDocument struct {
	MinimumSignatures int                     `json:"minimumSignatures"`
	AllowedIdentities []string                `json:"allowedIdentities"`
	Keys              []credentialKeyDocument `json:"keys"`
}

func LoadTrustPolicy(filePath string) (TrustPolicy, error) {
	encoded, err := readBoundedRegularFile(filePath, maxIndexBytes, true)
	if err != nil {
		return TrustPolicy{}, fmt.Errorf("read qualification trust policy: %w", err)
	}
	if err := requireExactShape(encoded, trustPolicyShape()); err != nil {
		return TrustPolicy{}, fmt.Errorf("validate qualification trust policy shape: %w", err)
	}
	var document trustPolicyDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return TrustPolicy{}, fmt.Errorf("decode qualification trust policy: %w", err)
	}
	if document.SchemaVersion != TrustPolicySchemaV2 || document.MaxReceiptAgeSeconds < 1 ||
		document.MaxReceiptAgeSeconds > int64((365*24*time.Hour)/time.Second) || document.MaxFutureSkewSeconds < 0 ||
		document.MaxFutureSkewSeconds > int64((5*time.Minute)/time.Second) {
		return TrustPolicy{}, errors.New("qualification trust policy schema or freshness limits are invalid")
	}
	policy := TrustPolicy{
		Digest: sha256Digest(encoded), Signers: map[string]SignerTrust{}, MinimumSignatures: document.MinimumSignatures,
		MaxReceiptAge:        time.Duration(document.MaxReceiptAgeSeconds) * time.Second,
		MaxFutureSkew:        time.Duration(document.MaxFutureSkewSeconds) * time.Second,
		CredentialIssuers:    map[string]CredentialIssuerTrust{},
		EncryptionRecipients: make([]EncryptionRecipient, 0, len(document.EncryptionRecipients)),
	}
	for index, signer := range document.Signers {
		if index > 0 && document.Signers[index-1].KeyID >= signer.KeyID {
			return TrustPolicy{}, errors.New("qualification trust policy signers must be uniquely sorted by keyId")
		}
		algorithm, publicKey, err := parsePublicKey(signer.Algorithm, signer.PublicKeyPEM)
		if err != nil {
			return TrustPolicy{}, fmt.Errorf("parse qualification signer %q: %w", signer.KeyID, err)
		}
		notBefore, err := parseCanonicalTime(signer.NotBefore, "trust.signer.notBefore")
		if err != nil {
			return TrustPolicy{}, err
		}
		notAfter, err := parseCanonicalTime(signer.NotAfter, "trust.signer.notAfter")
		if err != nil {
			return TrustPolicy{}, err
		}
		var revokedAt *time.Time
		if signer.RevokedAt != "" {
			parsed, err := parseCanonicalTime(signer.RevokedAt, "trust.signer.revokedAt")
			if err != nil {
				return TrustPolicy{}, err
			}
			revokedAt = &parsed
		}
		policy.Signers[signer.KeyID] = SignerTrust{
			Algorithm: algorithm, PublicKey: publicKey, Identity: signer.Identity, Role: signer.Role,
			NotBefore: notBefore, NotAfter: notAfter, RevokedAt: revokedAt,
		}
	}
	for index, issuer := range document.CredentialIssuers {
		if index > 0 && document.CredentialIssuers[index-1].Issuer >= issuer.Issuer {
			return TrustPolicy{}, errors.New("credential issuers must be uniquely sorted by issuer")
		}
		if !sort.StringsAreSorted(issuer.AllowedIdentities) || hasDuplicateStrings(issuer.AllowedIdentities) {
			return TrustPolicy{}, fmt.Errorf("credential issuer %q identities must be sorted and unique", issuer.Issuer)
		}
		keys := make(map[string]templateauthority.TrustedSigner, len(issuer.Keys))
		keyValidity := make(map[string]AuthorityKeyValidity, len(issuer.Keys))
		for keyIndex, key := range issuer.Keys {
			if keyIndex > 0 && issuer.Keys[keyIndex-1].KeyID >= key.KeyID {
				return TrustPolicy{}, fmt.Errorf("credential issuer %q keys must be uniquely sorted", issuer.Issuer)
			}
			algorithm, publicKey, err := parsePublicKey(key.Algorithm, key.PublicKeyPEM)
			if err != nil {
				return TrustPolicy{}, fmt.Errorf("parse credential issuer %q key %q: %w", issuer.Issuer, key.KeyID, err)
			}
			keys[key.KeyID] = templateauthority.TrustedSigner{Algorithm: algorithm, PublicKey: publicKey, Identity: key.Identity}
			validity, err := parseAuthorityKeyValidity(key)
			if err != nil {
				return TrustPolicy{}, fmt.Errorf("credential issuer %q key %q validity: %w", issuer.Issuer, key.KeyID, err)
			}
			keyValidity[key.KeyID] = validity
		}
		policy.CredentialIssuers[issuer.Issuer] = CredentialIssuerTrust{
			Issuer: issuer.Issuer, Keys: keys, KeyValidity: keyValidity, MinimumSignatures: issuer.MinimumSignatures,
			AllowedIdentities: append([]string(nil), issuer.AllowedIdentities...),
		}
	}
	for index, recipient := range document.EncryptionRecipients {
		if !validCanonicalString(recipient.KeyResource, 2048) || !validCanonicalString(recipient.KeyVersion, 256) ||
			(index > 0 && (document.EncryptionRecipients[index-1].KeyResource > recipient.KeyResource ||
				(document.EncryptionRecipients[index-1].KeyResource == recipient.KeyResource && document.EncryptionRecipients[index-1].KeyVersion >= recipient.KeyVersion))) {
			return TrustPolicy{}, errors.New("encryption recipients must be canonical, uniquely sorted key resource/version pairs")
		}
		policy.EncryptionRecipients = append(policy.EncryptionRecipients, EncryptionRecipient{
			KeyResource: recipient.KeyResource, KeyVersion: recipient.KeyVersion,
		})
	}
	if document.EncryptionAuthority.MinimumSignatures < 1 ||
		document.EncryptionAuthority.MinimumSignatures > len(document.EncryptionAuthority.Keys) ||
		!sort.StringsAreSorted(document.EncryptionAuthority.AllowedIdentities) ||
		hasDuplicateStrings(document.EncryptionAuthority.AllowedIdentities) || len(document.EncryptionAuthority.AllowedIdentities) == 0 {
		return TrustPolicy{}, errors.New("encryption authority threshold and identities are invalid")
	}
	encryptionKeys := make(map[string]templateauthority.TrustedSigner, len(document.EncryptionAuthority.Keys))
	encryptionValidity := make(map[string]AuthorityKeyValidity, len(document.EncryptionAuthority.Keys))
	for index, key := range document.EncryptionAuthority.Keys {
		if index > 0 && document.EncryptionAuthority.Keys[index-1].KeyID >= key.KeyID {
			return TrustPolicy{}, errors.New("encryption authority keys must be uniquely sorted")
		}
		algorithm, publicKey, err := parsePublicKey(key.Algorithm, key.PublicKeyPEM)
		if err != nil {
			return TrustPolicy{}, fmt.Errorf("parse encryption authority key %q: %w", key.KeyID, err)
		}
		encryptionKeys[key.KeyID] = templateauthority.TrustedSigner{
			Algorithm: algorithm, PublicKey: publicKey, Identity: key.Identity,
		}
		validity, err := parseAuthorityKeyValidity(key)
		if err != nil {
			return TrustPolicy{}, fmt.Errorf("encryption authority key %q validity: %w", key.KeyID, err)
		}
		encryptionValidity[key.KeyID] = validity
	}
	policy.EncryptionAuthority = EncryptionAuthorityTrust{
		Keys: encryptionKeys, KeyValidity: encryptionValidity, MinimumSignatures: document.EncryptionAuthority.MinimumSignatures,
		AllowedIdentities: append([]string(nil), document.EncryptionAuthority.AllowedIdentities...),
	}
	faultKeys, faultValidity, err := parseIndependentAuthorityDocument("fault authority", document.FaultAuthority)
	if err != nil {
		return TrustPolicy{}, err
	}
	policy.FaultAuthority = FaultAuthorityTrust{
		Keys: faultKeys, KeyValidity: faultValidity, MinimumSignatures: document.FaultAuthority.MinimumSignatures,
		AllowedIdentities: append([]string(nil), document.FaultAuthority.AllowedIdentities...),
	}
	attestorKeys, attestorValidity, err := parseIndependentAuthorityDocument("fault ledger attestor", document.FaultLedgerAttestor)
	if err != nil {
		return TrustPolicy{}, err
	}
	policy.FaultLedgerAttestor = FaultLedgerAttestorTrust{
		Keys: attestorKeys, KeyValidity: attestorValidity, MinimumSignatures: document.FaultLedgerAttestor.MinimumSignatures,
		AllowedIdentities: append([]string(nil), document.FaultLedgerAttestor.AllowedIdentities...),
	}
	return policy, nil
}

func parseIndependentAuthorityDocument(
	label string,
	document encryptionAuthorityDocument,
) (map[string]templateauthority.TrustedSigner, map[string]AuthorityKeyValidity, error) {
	if document.MinimumSignatures < 1 || document.MinimumSignatures > len(document.Keys) ||
		!sort.StringsAreSorted(document.AllowedIdentities) || hasDuplicateStrings(document.AllowedIdentities) ||
		len(document.AllowedIdentities) == 0 {
		return nil, nil, fmt.Errorf("%s threshold and identities are invalid", label)
	}
	keys := make(map[string]templateauthority.TrustedSigner, len(document.Keys))
	validity := make(map[string]AuthorityKeyValidity, len(document.Keys))
	for index, key := range document.Keys {
		if index > 0 && document.Keys[index-1].KeyID >= key.KeyID {
			return nil, nil, fmt.Errorf("%s keys must be uniquely sorted", label)
		}
		algorithm, publicKey, err := parsePublicKey(key.Algorithm, key.PublicKeyPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s key %q: %w", label, key.KeyID, err)
		}
		keys[key.KeyID] = templateauthority.TrustedSigner{
			Algorithm: algorithm, PublicKey: publicKey, Identity: key.Identity,
		}
		keyValidity, err := parseAuthorityKeyValidity(key)
		if err != nil {
			return nil, nil, fmt.Errorf("%s key %q validity: %w", label, key.KeyID, err)
		}
		validity[key.KeyID] = keyValidity
	}
	return keys, validity, nil
}

func parseAuthorityKeyValidity(key credentialKeyDocument) (AuthorityKeyValidity, error) {
	notBefore, err := parseCanonicalTime(key.NotBefore, "authorityKey.notBefore")
	if err != nil {
		return AuthorityKeyValidity{}, err
	}
	notAfter, err := parseCanonicalTime(key.NotAfter, "authorityKey.notAfter")
	if err != nil || !notAfter.After(notBefore) {
		return AuthorityKeyValidity{}, errors.New("authority key validity window is invalid")
	}
	var revokedAt *time.Time
	if key.RevokedAt != "" {
		parsed, err := parseCanonicalTime(key.RevokedAt, "authorityKey.revokedAt")
		if err != nil {
			return AuthorityKeyValidity{}, err
		}
		revokedAt = &parsed
	}
	return AuthorityKeyValidity{NotBefore: notBefore, NotAfter: notAfter, RevokedAt: revokedAt}, nil
}

func parsePublicKey(rawAlgorithm, encodedPEM string) (templateauthority.SignatureAlgorithm, any, error) {
	algorithm := templateauthority.SignatureAlgorithm(rawAlgorithm)
	if algorithm != templateauthority.AlgorithmEd25519 && algorithm != templateauthority.AlgorithmECDSASHA256 {
		return "", nil, errors.New("unsupported signature algorithm")
	}
	block, rest := pem.Decode([]byte(encodedPEM))
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 || len(block.Bytes) == 0 {
		return "", nil, errors.New("public key must be one canonical PKIX PUBLIC KEY PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", nil, err
	}
	switch algorithm {
	case templateauthority.AlgorithmEd25519:
		publicKey, ok := parsed.(ed25519.PublicKey)
		if !ok || len(publicKey) != ed25519.PublicKeySize {
			return "", nil, errors.New("algorithm and Ed25519 public key do not match")
		}
		return algorithm, publicKey, nil
	case templateauthority.AlgorithmECDSASHA256:
		publicKey, ok := parsed.(*ecdsa.PublicKey)
		if !ok || publicKey.Curve != elliptic.P256() {
			return "", nil, errors.New("ECDSA qualification keys must use P-256")
		}
		return algorithm, publicKey, nil
	default:
		return "", nil, errors.New("unsupported signature algorithm")
	}
}

func hasDuplicateStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return true
		}
	}
	return false
}
