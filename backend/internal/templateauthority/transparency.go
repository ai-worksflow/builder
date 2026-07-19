package templateauthority

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	maxTransparencyBundleBytes = 16 << 20
	maxTransparencyLeafBytes   = 8 << 20
	maxTransparencyProofNodes  = 64
)

// TrustedTransparencyLog is a keyful server-side trust entry for one log. The
// map key in TransparencyTrustPolicy.Logs is the authoritative log ID.
type TrustedTransparencyLog struct {
	Keys map[string]TrustedSigner
}

// TransparencyTrustPolicy defines log/key allowlists and freshness bounds.
// MaxEntryAge is required; evidence outside [now-MaxEntryAge,
// now+MaxFutureSkew] is rejected.
type TransparencyTrustPolicy struct {
	Logs          map[string]TrustedTransparencyLog
	MaxEntryAge   time.Duration
	MaxFutureSkew time.Duration
}

// TransparencyExpectation binds the proof to bytes selected by the caller.
// Passing only a reference is intentionally unsupported.
type TransparencyExpectation struct {
	Leaf []byte
}

// TransparencyEntry contains the exact fields cryptographically bound by both
// the checkpoint signature and signed entry timestamp (SET).
type TransparencyEntry struct {
	LogID          string
	TreeSize       uint64
	RootHash       string
	LeafIndex      uint64
	LeafHash       string
	IntegratedTime int64
}

// VerifiedTransparency is returned only after leaf equality, RFC6962 inclusion,
// freshness, checkpoint signature, and SET signature all pass.
type VerifiedTransparency struct {
	Entry               TransparencyEntry
	IntegratedAt        time.Time
	CheckpointSignedAt  time.Time
	BundleDigest        string
	LogSignerIdentities []string
}

type trustedTransparencyLog struct {
	keys map[string]trustedSigner
}

// TransparencyVerifier is immutable and safe for concurrent use.
type TransparencyVerifier struct {
	logs          map[string]trustedTransparencyLog
	maxEntryAge   time.Duration
	maxFutureSkew time.Duration
	now           func() time.Time
}

// NewTransparencyVerifier constructs a verifier using time.Now as its trusted
// wall clock.
func NewTransparencyVerifier(policy TransparencyTrustPolicy) (*TransparencyVerifier, error) {
	return newTransparencyVerifier(policy, time.Now)
}

func newTransparencyVerifier(policy TransparencyTrustPolicy, now func() time.Time) (*TransparencyVerifier, error) {
	if len(policy.Logs) == 0 {
		return nil, errors.New("transparency trust policy must contain at least one log")
	}
	if policy.MaxEntryAge <= 0 {
		return nil, errors.New("transparency MaxEntryAge must be positive")
	}
	if policy.MaxFutureSkew < 0 {
		return nil, errors.New("transparency MaxFutureSkew must not be negative")
	}
	if policy.MaxFutureSkew > policy.MaxEntryAge {
		return nil, errors.New("transparency MaxFutureSkew must not exceed MaxEntryAge")
	}
	if now == nil {
		return nil, errors.New("transparency clock must not be nil")
	}
	verifier := &TransparencyVerifier{
		logs:          make(map[string]trustedTransparencyLog, len(policy.Logs)),
		maxEntryAge:   policy.MaxEntryAge,
		maxFutureSkew: policy.MaxFutureSkew,
		now:           now,
	}
	for logID, configuredLog := range policy.Logs {
		if !validPolicyToken(logID) {
			return nil, fmt.Errorf("invalid transparency log ID %q", logID)
		}
		if len(configuredLog.Keys) == 0 {
			return nil, fmt.Errorf("transparency log %q has no trusted keys", logID)
		}
		trustedLog := trustedTransparencyLog{keys: make(map[string]trustedSigner, len(configuredLog.Keys))}
		for keyID, configuredKey := range configuredLog.Keys {
			if !validPolicyToken(keyID) {
				return nil, fmt.Errorf("invalid key ID %q for transparency log %q", keyID, logID)
			}
			if !validPolicyToken(configuredKey.Identity) {
				return nil, fmt.Errorf("invalid identity for transparency log %q key %q", logID, keyID)
			}
			publicKey, err := copyPublicKey(configuredKey.Algorithm, configuredKey.PublicKey)
			if err != nil {
				return nil, fmt.Errorf("invalid transparency log %q key %q: %w", logID, keyID, err)
			}
			trustedLog.keys[keyID] = trustedSigner{
				algorithm: configuredKey.Algorithm,
				publicKey: publicKey,
				identity:  configuredKey.Identity,
			}
		}
		verifier.logs[logID] = trustedLog
	}
	return verifier, nil
}

type transparencyBundle struct {
	LogID                string                   `json:"logId"`
	TreeSize             uint64                   `json:"treeSize"`
	RootHash             string                   `json:"rootHash"`
	LeafIndex            uint64                   `json:"leafIndex"`
	IntegratedTime       int64                    `json:"integratedTime"`
	Leaf                 string                   `json:"leaf"`
	InclusionProof       []string                 `json:"inclusionProof"`
	Checkpoint           transparencyCheckpoint   `json:"checkpoint"`
	SignedEntryTimestamp transparencyLogSignature `json:"signedEntryTimestamp"`
}

type transparencyCheckpoint struct {
	SignedAt  int64  `json:"signedAt"`
	KeyID     string `json:"keyid"`
	Signature string `json:"signature"`
}

type transparencyLogSignature struct {
	KeyID     string `json:"keyid"`
	Signature string `json:"signature"`
}

type transparencySigningDocument struct {
	Kind           string `json:"kind"`
	LogID          string `json:"logId"`
	TreeSize       uint64 `json:"treeSize"`
	RootHash       string `json:"rootHash"`
	LeafIndex      uint64 `json:"leafIndex"`
	LeafHash       string `json:"leafHash"`
	IntegratedTime int64  `json:"integratedTime"`
	SignedAt       int64  `json:"signedAt,omitempty"`
}

// CheckpointSigningBytes returns the deterministic message signed by a log
// checkpoint key. It binds the full entry coordinates in addition to the tree
// head; SignedAt is part of the signature.
func CheckpointSigningBytes(entry TransparencyEntry, signedAt int64) []byte {
	encoded, _ := json.Marshal(transparencySigningDocument{
		Kind: "worksflow-transparency-checkpoint/v1", LogID: entry.LogID,
		TreeSize: entry.TreeSize, RootHash: entry.RootHash, LeafIndex: entry.LeafIndex,
		LeafHash: entry.LeafHash, IntegratedTime: entry.IntegratedTime, SignedAt: signedAt,
	})
	return encoded
}

// SignedEntryTimestampSigningBytes returns the deterministic message signed as
// the entry's SET. It binds log, tree, root, index, leaf, and integrated time.
func SignedEntryTimestampSigningBytes(entry TransparencyEntry) []byte {
	encoded, _ := json.Marshal(transparencySigningDocument{
		Kind: "worksflow-transparency-set/v1", LogID: entry.LogID,
		TreeSize: entry.TreeSize, RootHash: entry.RootHash, LeafIndex: entry.LeafIndex,
		LeafHash: entry.LeafHash, IntegratedTime: entry.IntegratedTime,
	})
	return encoded
}

// Verify parses and verifies a self-contained normalized transparency bundle.
// Native log formats must be adapted into this structure without weakening any
// of the required bindings.
func (v *TransparencyVerifier) Verify(bundleJSON []byte, expected TransparencyExpectation) (*VerifiedTransparency, error) {
	if v == nil {
		return nil, verificationError("invalid_verifier", "", "transparency verifier is nil")
	}
	if len(bundleJSON) == 0 || len(bundleJSON) > maxTransparencyBundleBytes {
		return nil, verificationError("invalid_transparency_bundle", "bundle", "size must be between 1 and %d bytes", maxTransparencyBundleBytes)
	}
	if len(expected.Leaf) == 0 || len(expected.Leaf) > maxTransparencyLeafBytes {
		return nil, verificationError("invalid_expected_leaf", "leaf", "expected leaf size must be between 1 and %d bytes", maxTransparencyLeafBytes)
	}
	var bundle transparencyBundle
	if err := decodeStrictJSON(bundleJSON, &bundle); err != nil {
		return nil, verificationError("invalid_transparency_bundle", "bundle", "%v", err)
	}
	trustedLog, logAllowed := v.logs[bundle.LogID]
	if !logAllowed {
		return nil, verificationError("untrusted_transparency_log", "logId", "log ID %q is not allowlisted", bundle.LogID)
	}
	if bundle.TreeSize == 0 || bundle.LeafIndex >= bundle.TreeSize {
		return nil, verificationError("invalid_tree_coordinates", "leafIndex", "leaf index %d is outside tree size %d", bundle.LeafIndex, bundle.TreeSize)
	}
	rootHash, err := parseCanonicalSHA256Digest(bundle.RootHash)
	if err != nil {
		return nil, verificationError("invalid_root_hash", "rootHash", "%v", err)
	}
	leaf, err := decodeCanonicalBase64(bundle.Leaf)
	if err != nil || len(leaf) == 0 || len(leaf) > maxTransparencyLeafBytes {
		return nil, verificationError("invalid_leaf_encoding", "leaf", "leaf must be strict standard base64 encoding of 1..%d bytes", maxTransparencyLeafBytes)
	}
	if len(leaf) != len(expected.Leaf) || subtle.ConstantTimeCompare(leaf, expected.Leaf) != 1 {
		return nil, verificationError("transparency_leaf_mismatch", "leaf", "bundle leaf is not the exact expected value")
	}
	leafHash := RFC6962LeafHash(leaf)
	proof, err := decodeInclusionProof(bundle.InclusionProof)
	if err != nil {
		return nil, err
	}
	if !VerifyRFC6962Inclusion(leafHash, bundle.LeafIndex, bundle.TreeSize, proof, rootHash) {
		return nil, verificationError("invalid_inclusion_proof", "inclusionProof", "proof is incomplete, excessive, or does not produce the signed root")
	}

	now := v.now().UTC()
	if now.IsZero() {
		return nil, verificationError("invalid_verification_time", "", "trusted clock returned the zero time")
	}
	integratedAt := time.Unix(bundle.IntegratedTime, 0).UTC()
	if bundle.IntegratedTime <= 0 || integratedAt.After(now.Add(v.maxFutureSkew)) || integratedAt.Before(now.Add(-v.maxEntryAge)) {
		return nil, verificationError("invalid_integrated_time", "integratedTime", "entry is outside the configured verification window")
	}
	checkpointAt := time.Unix(bundle.Checkpoint.SignedAt, 0).UTC()
	if bundle.Checkpoint.SignedAt <= 0 || checkpointAt.After(now.Add(v.maxFutureSkew)) || checkpointAt.Before(now.Add(-v.maxEntryAge)) {
		return nil, verificationError("invalid_checkpoint_time", "checkpoint.signedAt", "checkpoint is outside the configured verification window")
	}
	if checkpointAt.Add(v.maxFutureSkew).Before(integratedAt) {
		return nil, verificationError("checkpoint_precedes_entry", "checkpoint.signedAt", "checkpoint predates the integrated entry beyond allowed skew")
	}
	entry := TransparencyEntry{
		LogID: bundle.LogID, TreeSize: bundle.TreeSize, RootHash: bundle.RootHash,
		LeafIndex: bundle.LeafIndex, LeafHash: canonicalHash(leafHash), IntegratedTime: bundle.IntegratedTime,
	}

	checkpointIdentity, checkpointSignature, err := verifyLogSignature(
		trustedLog, bundle.Checkpoint.KeyID, bundle.Checkpoint.Signature,
		CheckpointSigningBytes(entry, bundle.Checkpoint.SignedAt), "checkpoint",
	)
	if err != nil {
		return nil, err
	}
	setIdentity, setSignature, err := verifyLogSignature(
		trustedLog, bundle.SignedEntryTimestamp.KeyID, bundle.SignedEntryTimestamp.Signature,
		SignedEntryTimestampSigningBytes(entry), "signedEntryTimestamp",
	)
	if err != nil {
		return nil, err
	}
	canonicalBundle := struct {
		LogID          string   `json:"logId"`
		TreeSize       uint64   `json:"treeSize"`
		RootHash       string   `json:"rootHash"`
		LeafIndex      uint64   `json:"leafIndex"`
		IntegratedTime int64    `json:"integratedTime"`
		Leaf           string   `json:"leaf"`
		InclusionProof []string `json:"inclusionProof"`
		Checkpoint     struct {
			SignedAt  int64  `json:"signedAt"`
			KeyID     string `json:"keyid"`
			Signature string `json:"signature"`
		} `json:"checkpoint"`
		SET struct {
			KeyID     string `json:"keyid"`
			Signature string `json:"signature"`
		} `json:"signedEntryTimestamp"`
	}{
		LogID: bundle.LogID, TreeSize: bundle.TreeSize, RootHash: bundle.RootHash,
		LeafIndex: bundle.LeafIndex, IntegratedTime: bundle.IntegratedTime,
		Leaf: base64.StdEncoding.EncodeToString(leaf), InclusionProof: canonicalProof(proof),
	}
	canonicalBundle.Checkpoint.SignedAt = bundle.Checkpoint.SignedAt
	canonicalBundle.Checkpoint.KeyID = bundle.Checkpoint.KeyID
	canonicalBundle.Checkpoint.Signature = base64.StdEncoding.EncodeToString(checkpointSignature)
	canonicalBundle.SET.KeyID = bundle.SignedEntryTimestamp.KeyID
	canonicalBundle.SET.Signature = base64.StdEncoding.EncodeToString(setSignature)
	canonicalJSON, marshalErr := json.Marshal(canonicalBundle)
	if marshalErr != nil {
		return nil, verificationError("canonicalization_failed", "bundle", "%v", marshalErr)
	}
	identities := []string{checkpointIdentity}
	if setIdentity != checkpointIdentity {
		identities = append(identities, setIdentity)
	}
	sort.Strings(identities)
	return &VerifiedTransparency{
		Entry: entry, IntegratedAt: integratedAt, CheckpointSignedAt: checkpointAt,
		BundleDigest: SHA256Digest(canonicalJSON), LogSignerIdentities: identities,
	}, nil
}

func verifyLogSignature(log trustedTransparencyLog, keyID, encodedSignature string, message []byte, field string) (string, []byte, error) {
	trustedKey, exists := log.keys[keyID]
	if !exists {
		return "", nil, verificationError("untrusted_transparency_key", field+".keyid", "key ID %q is not allowlisted for the selected log", keyID)
	}
	signature, err := decodeCanonicalBase64(encodedSignature)
	if err != nil || len(signature) == 0 {
		return "", nil, verificationError("invalid_transparency_signature_encoding", field+".signature", "signature must be non-empty strict standard base64")
	}
	if !verifyConfiguredSignature(trustedKey, message, signature) {
		return "", nil, verificationError("transparency_signature_verification_failed", field+".signature", "signature for key %q is invalid", keyID)
	}
	return trustedKey.identity, signature, nil
}

func decodeInclusionProof(encoded []string) ([][sha256.Size]byte, error) {
	if len(encoded) > maxTransparencyProofNodes {
		return nil, verificationError("invalid_inclusion_proof", "inclusionProof", "proof exceeds %d nodes", maxTransparencyProofNodes)
	}
	proof := make([][sha256.Size]byte, 0, len(encoded))
	for index, value := range encoded {
		hash, err := parseCanonicalSHA256Digest(value)
		if err != nil {
			return nil, verificationError("invalid_inclusion_proof_hash", fmt.Sprintf("inclusionProof[%d]", index), "%v", err)
		}
		proof = append(proof, hash)
	}
	return proof, nil
}

func canonicalProof(proof [][sha256.Size]byte) []string {
	result := make([]string, 0, len(proof))
	for _, hash := range proof {
		result = append(result, canonicalHash(hash))
	}
	return result
}

func parseCanonicalSHA256Digest(value string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	if !strings.HasPrefix(value, "sha256:") {
		return result, errors.New("digest must use the sha256: prefix")
	}
	raw := strings.TrimPrefix(value, "sha256:")
	if len(raw) != sha256.Size*2 || strings.ToLower(raw) != raw {
		return result, errors.New("digest must contain 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return result, errors.New("digest is not hexadecimal")
	}
	copy(result[:], decoded)
	return result, nil
}

func canonicalHash(value [sha256.Size]byte) string {
	return "sha256:" + hex.EncodeToString(value[:])
}

// RFC6962LeafHash implements SHA-256(0x00 || leaf).
func RFC6962LeafHash(leaf []byte) [sha256.Size]byte {
	buffer := make([]byte, 1+len(leaf))
	buffer[0] = 0
	copy(buffer[1:], leaf)
	return sha256.Sum256(buffer)
}

// RFC6962NodeHash implements SHA-256(0x01 || left || right).
func RFC6962NodeHash(left, right [sha256.Size]byte) [sha256.Size]byte {
	var buffer [1 + 2*sha256.Size]byte
	buffer[0] = 1
	copy(buffer[1:1+sha256.Size], left[:])
	copy(buffer[1+sha256.Size:], right[:])
	return sha256.Sum256(buffer[:])
}

// VerifyRFC6962Inclusion applies the RFC6962 audit-path algorithm. It rejects
// out-of-range coordinates and both truncated and overlong proofs.
func VerifyRFC6962Inclusion(leafHash [sha256.Size]byte, leafIndex, treeSize uint64, proof [][sha256.Size]byte, rootHash [sha256.Size]byte) bool {
	if treeSize == 0 || leafIndex >= treeSize || len(proof) > maxTransparencyProofNodes {
		return false
	}
	fn, sn := leafIndex, treeSize-1
	computed := leafHash
	for _, sibling := range proof {
		if sn == 0 {
			return false
		}
		if fn&1 == 1 || fn == sn {
			computed = RFC6962NodeHash(sibling, computed)
			for fn&1 == 0 && fn != 0 {
				fn >>= 1
				sn >>= 1
			}
		} else {
			computed = RFC6962NodeHash(computed, sibling)
		}
		fn >>= 1
		sn >>= 1
	}
	return sn == 0 && subtle.ConstantTimeCompare(computed[:], rootHash[:]) == 1
}
