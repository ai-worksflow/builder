package templateauthority

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestTransparencyVerifierAcceptsSignedRFC6962Proof(t *testing.T) {
	now := time.Date(2026, time.July, 18, 2, 0, 0, 0, time.UTC)
	checkpointKey := newDSSETestKey(t, AlgorithmEd25519, "checkpoint@log.example.test")
	setKey := newDSSETestKey(t, AlgorithmECDSASHA256, "set@log.example.test")
	leaves := [][]byte{[]byte("entry-zero"), []byte("entry-one"), []byte("entry-two"), []byte("entry-three"), []byte("entry-four")}
	bundle := testTransparencyBundle(t, "log.example.test/prod", leaves, 3, now.Add(-5*time.Minute), now.Add(-4*time.Minute), "checkpoint", checkpointKey, "set", setKey)
	verifier := testTransparencyVerifier(t, now, "log.example.test/prod", map[string]dsseTestKey{
		"checkpoint": checkpointKey,
		"set":        setKey,
	})

	verified, err := verifier.Verify(bundle, TransparencyExpectation{Leaf: leaves[3]})
	if err != nil {
		t.Fatalf("verify transparency proof: %v", err)
	}
	if verified.Entry.LeafIndex != 3 || verified.Entry.TreeSize != uint64(len(leaves)) {
		t.Fatalf("verified entry = %#v", verified.Entry)
	}
	if verified.Entry.LeafHash != canonicalHash(RFC6962LeafHash(leaves[3])) {
		t.Fatalf("leaf hash = %q", verified.Entry.LeafHash)
	}
	if verified.BundleDigest == "" || verified.BundleDigest[:len("sha256:")] != "sha256:" {
		t.Fatalf("bundle digest = %q", verified.BundleDigest)
	}
	if !slices.Equal(verified.LogSignerIdentities, []string{"checkpoint@log.example.test", "set@log.example.test"}) {
		t.Fatalf("log signer identities = %#v", verified.LogSignerIdentities)
	}
}

func TestTransparencyVerifierRejectsTampering(t *testing.T) {
	now := time.Date(2026, time.July, 18, 2, 0, 0, 0, time.UTC)
	checkpointKey := newDSSETestKey(t, AlgorithmEd25519, "checkpoint@log.example.test")
	setKey := newDSSETestKey(t, AlgorithmECDSASHA256, "set@log.example.test")
	leaves := [][]byte{[]byte("entry-zero"), []byte("entry-one"), []byte("entry-two"), []byte("entry-three")}
	valid := testTransparencyBundle(t, "log.example.test/prod", leaves, 2, now.Add(-5*time.Minute), now.Add(-4*time.Minute), "checkpoint", checkpointKey, "set", setKey)
	verifier := testTransparencyVerifier(t, now, "log.example.test/prod", map[string]dsseTestKey{
		"checkpoint": checkpointKey,
		"set":        setKey,
	})

	tests := []struct {
		name         string
		code         string
		expectedLeaf []byte
		edit         func(t *testing.T, bundle *transparencyBundle)
	}{
		{
			name: "leaf", code: "transparency_leaf_mismatch", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) {
				bundle.Leaf = base64.StdEncoding.EncodeToString([]byte("attacker entry"))
			},
		},
		{
			name: "root", code: "invalid_inclusion_proof", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) {
				root, err := parseCanonicalSHA256Digest(bundle.RootHash)
				if err != nil {
					t.Fatal(err)
				}
				root[0] ^= 1
				bundle.RootHash = canonicalHash(root)
			},
		},
		{
			name: "proof node", code: "invalid_inclusion_proof", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) {
				hash, err := parseCanonicalSHA256Digest(bundle.InclusionProof[0])
				if err != nil {
					t.Fatal(err)
				}
				hash[0] ^= 1
				bundle.InclusionProof[0] = canonicalHash(hash)
			},
		},
		{
			name: "truncated proof", code: "invalid_inclusion_proof", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) {
				bundle.InclusionProof = bundle.InclusionProof[:len(bundle.InclusionProof)-1]
			},
		},
		{
			name: "excess proof", code: "invalid_inclusion_proof", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) {
				bundle.InclusionProof = append(bundle.InclusionProof, bundle.InclusionProof[0])
			},
		},
		{
			name: "unknown log", code: "untrusted_transparency_log", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) { bundle.LogID = "log.example.test/attacker" },
		},
		{
			name: "unknown checkpoint key", code: "untrusted_transparency_key", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) { bundle.Checkpoint.KeyID = "unknown" },
		},
		{
			name: "checkpoint signature", code: "transparency_signature_verification_failed", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) {
				bundle.Checkpoint.Signature = tamperBase64Signature(t, bundle.Checkpoint.Signature)
			},
		},
		{
			name: "SET signature", code: "transparency_signature_verification_failed", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) {
				bundle.SignedEntryTimestamp.Signature = tamperBase64Signature(t, bundle.SignedEntryTimestamp.Signature)
			},
		},
		{
			name: "integrated time binding", code: "transparency_signature_verification_failed", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) { bundle.IntegratedTime++ },
		},
		{
			name: "checkpoint time binding", code: "transparency_signature_verification_failed", expectedLeaf: leaves[2],
			edit: func(t *testing.T, bundle *transparencyBundle) { bundle.Checkpoint.SignedAt++ },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var bundle transparencyBundle
			if err := json.Unmarshal(valid, &bundle); err != nil {
				t.Fatal(err)
			}
			test.edit(t, &bundle)
			encoded, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			_, err = verifier.Verify(encoded, TransparencyExpectation{Leaf: test.expectedLeaf})
			assertVerificationCode(t, err, test.code)
		})
	}
}

func TestTransparencyVerifierRejectsClockDriftWithValidSignatures(t *testing.T) {
	now := time.Date(2026, time.July, 18, 2, 0, 0, 0, time.UTC)
	key := newDSSETestKey(t, AlgorithmEd25519, "log@example.test")
	leaves := [][]byte{[]byte("entry")}
	verifier := testTransparencyVerifier(t, now, "log.example.test/prod", map[string]dsseTestKey{"log-key": key})

	tests := []struct {
		name       string
		integrated time.Time
		checkpoint time.Time
		code       string
	}{
		{name: "future entry", integrated: now.Add(3 * time.Minute), checkpoint: now, code: "invalid_integrated_time"},
		{name: "stale entry", integrated: now.Add(-25 * time.Hour), checkpoint: now, code: "invalid_integrated_time"},
		{name: "future checkpoint", integrated: now, checkpoint: now.Add(3 * time.Minute), code: "invalid_checkpoint_time"},
		{name: "stale checkpoint", integrated: now.Add(-time.Hour), checkpoint: now.Add(-25 * time.Hour), code: "invalid_checkpoint_time"},
		{name: "checkpoint predates entry", integrated: now, checkpoint: now.Add(-10 * time.Minute), code: "checkpoint_precedes_entry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := testTransparencyBundle(t, "log.example.test/prod", leaves, 0, test.integrated, test.checkpoint, "log-key", key, "log-key", key)
			_, err := verifier.Verify(bundle, TransparencyExpectation{Leaf: leaves[0]})
			assertVerificationCode(t, err, test.code)
		})
	}
}

func TestRFC6962InclusionProofAllLeafPositions(t *testing.T) {
	for treeSize := 1; treeSize <= 17; treeSize++ {
		leaves := make([][]byte, 0, treeSize)
		for index := 0; index < treeSize; index++ {
			leaves = append(leaves, []byte{byte(treeSize), byte(index)})
		}
		hashes := hashLeaves(leaves)
		root := testMerkleRoot(hashes)
		for index := range leaves {
			proof := testMerkleProof(hashes, index)
			if !VerifyRFC6962Inclusion(hashes[index], uint64(index), uint64(treeSize), proof, root) {
				t.Fatalf("valid proof rejected for tree size %d index %d", treeSize, index)
			}
			if len(proof) > 0 && VerifyRFC6962Inclusion(hashes[index], uint64(index), uint64(treeSize), proof[:len(proof)-1], root) {
				t.Fatalf("truncated proof accepted for tree size %d index %d", treeSize, index)
			}
			excess := append(append([][sha256.Size]byte(nil), proof...), root)
			if VerifyRFC6962Inclusion(hashes[index], uint64(index), uint64(treeSize), excess, root) {
				t.Fatalf("excess proof accepted for tree size %d index %d", treeSize, index)
			}
		}
	}
}

func testTransparencyVerifier(t *testing.T, now time.Time, logID string, keys map[string]dsseTestKey) *TransparencyVerifier {
	t.Helper()
	configuredKeys := make(map[string]TrustedSigner, len(keys))
	for keyID, key := range keys {
		configuredKeys[keyID] = TrustedSigner{Algorithm: key.algorithm, PublicKey: key.public, Identity: key.identity}
	}
	verifier, err := newTransparencyVerifier(TransparencyTrustPolicy{
		Logs:        map[string]TrustedTransparencyLog{logID: {Keys: configuredKeys}},
		MaxEntryAge: 24 * time.Hour, MaxFutureSkew: 2 * time.Minute,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func testTransparencyBundle(
	t *testing.T,
	logID string,
	leaves [][]byte,
	leafIndex int,
	integratedAt time.Time,
	checkpointAt time.Time,
	checkpointKeyID string,
	checkpointKey dsseTestKey,
	setKeyID string,
	setKey dsseTestKey,
) []byte {
	t.Helper()
	hashes := hashLeaves(leaves)
	root := testMerkleRoot(hashes)
	proof := testMerkleProof(hashes, leafIndex)
	entry := TransparencyEntry{
		LogID: logID, TreeSize: uint64(len(leaves)), RootHash: canonicalHash(root),
		LeafIndex: uint64(leafIndex), LeafHash: canonicalHash(hashes[leafIndex]),
		IntegratedTime: integratedAt.UTC().Unix(),
	}
	bundle := transparencyBundle{
		LogID: logID, TreeSize: entry.TreeSize, RootHash: entry.RootHash,
		LeafIndex: entry.LeafIndex, IntegratedTime: entry.IntegratedTime,
		Leaf: base64.StdEncoding.EncodeToString(leaves[leafIndex]), InclusionProof: canonicalProof(proof),
		Checkpoint:           transparencyCheckpoint{SignedAt: checkpointAt.UTC().Unix(), KeyID: checkpointKeyID},
		SignedEntryTimestamp: transparencyLogSignature{KeyID: setKeyID},
	}
	bundle.Checkpoint.Signature = base64.StdEncoding.EncodeToString(checkpointKey.sign(t, CheckpointSigningBytes(entry, bundle.Checkpoint.SignedAt)))
	bundle.SignedEntryTimestamp.Signature = base64.StdEncoding.EncodeToString(setKey.sign(t, SignedEntryTimestampSigningBytes(entry)))
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func tamperBase64Signature(t *testing.T, encoded string) string {
	t.Helper()
	signature, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 1
	return base64.StdEncoding.EncodeToString(signature)
}

func hashLeaves(leaves [][]byte) [][sha256.Size]byte {
	result := make([][sha256.Size]byte, 0, len(leaves))
	for _, leaf := range leaves {
		result = append(result, RFC6962LeafHash(leaf))
	}
	return result
}

func testMerkleRoot(leaves [][sha256.Size]byte) [sha256.Size]byte {
	if len(leaves) == 1 {
		return leaves[0]
	}
	split := largestPowerOfTwoBelow(len(leaves))
	return RFC6962NodeHash(testMerkleRoot(leaves[:split]), testMerkleRoot(leaves[split:]))
}

func testMerkleProof(leaves [][sha256.Size]byte, index int) [][sha256.Size]byte {
	if len(leaves) == 1 {
		return nil
	}
	split := largestPowerOfTwoBelow(len(leaves))
	if index < split {
		proof := testMerkleProof(leaves[:split], index)
		return append(proof, testMerkleRoot(leaves[split:]))
	}
	proof := testMerkleProof(leaves[split:], index-split)
	return append(proof, testMerkleRoot(leaves[:split]))
}

func largestPowerOfTwoBelow(value int) int {
	result := 1
	for result<<1 < value {
		result <<= 1
	}
	return result
}
