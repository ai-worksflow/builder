package modelgovernance

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFrozenCorpusCanonicalRoundTripAndProfileClosure(t *testing.T) {
	profile := validModelProfile()
	corpus := validFrozenCorpus(t, profile)
	encoded, err := CanonicalFrozenCorpusJSON(corpus)
	if err != nil {
		t.Fatalf("canonicalize FrozenCorpus: %v", err)
	}
	digest, err := FrozenCorpusHash(corpus)
	if err != nil {
		t.Fatalf("hash FrozenCorpus: %v", err)
	}
	if digest != "sha256:b6249eb75ac018fdd88ff276a11f87d5e7324b438d679b3c42622e59487421d1" {
		t.Fatalf("canonical FrozenCorpus vector drifted: %s", digest)
	}
	parsed, err := ParseFrozenCorpusForProfile(encoded, digest, profile)
	if err != nil {
		t.Fatalf("parse linked FrozenCorpus: %v", err)
	}
	second, err := CanonicalFrozenCorpusJSON(parsed)
	if err != nil || !bytes.Equal(second, encoded) {
		t.Fatalf("FrozenCorpus canonical representation drifted: %v", err)
	}

	wrong := "sha256:" + strings.Repeat("f", 64)
	if wrong == digest {
		wrong = "sha256:" + strings.Repeat("e", 64)
	}
	if _, err := ParseFrozenCorpus(encoded, wrong); err == nil || !strings.Contains(err.Error(), "hash drift") {
		t.Fatalf("FrozenCorpus hash drift was accepted: %v", err)
	}
}

func TestParseFrozenCorpusRejectsNonStrictOrNonCanonicalJSON(t *testing.T) {
	corpus := validFrozenCorpus(t, validModelProfile())
	encoded := mustCanonicalCorpus(t, corpus)
	digest := sha256Digest(encoded)
	schema := `"schemaVersion":"` + FrozenCorpusSchemaVersion + `"`
	canonicalPrefix := "{" + schema + `,"id":"33333333-3333-4333-8333-333333333333"`
	reorderedPrefix := `{"id":"33333333-3333-4333-8333-333333333333",` + schema
	profileJSON, err := json.Marshal(corpus.Profile)
	if err != nil {
		t.Fatalf("marshal profile test value: %v", err)
	}
	casesJSON, err := json.Marshal(corpus.Cases)
	if err != nil {
		t.Fatalf("marshal cases test value: %v", err)
	}
	nullProfile := []byte(strings.Replace(string(encoded), `"profile":`+string(profileJSON), `"profile":null`, 1))
	nullCases := []byte(strings.Replace(string(encoded), `"cases":`+string(casesJSON), `"cases":null`, 1))
	if bytes.Equal(nullProfile, encoded) || bytes.Equal(nullCases, encoded) {
		t.Fatal("null test fixture replacement did not apply")
	}

	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "unknown", payload: appendBeforeClosing(encoded, `,"secret":"forbidden"`)},
		{name: "nested unknown", payload: []byte(strings.Replace(string(encoded), `"repetitions":3`, `"repetitions":3,"pass":true`, 1))},
		{name: "nested duplicate", payload: []byte(strings.Replace(string(encoded), `"repetitions":3`, `"repetitions":3,"repetitions":3`, 1))},
		{name: "null object", payload: nullProfile},
		{name: "null cases", payload: nullCases},
		{name: "null boolean", payload: []byte(strings.Replace(string(encoded), `"dirty":false`, `"dirty":null`, 1))},
		{name: "duplicate", payload: []byte(strings.Replace(string(encoded), "{", "{"+schema+",", 1))},
		{name: "trailing", payload: append(append([]byte(nil), encoded...), []byte(`null`)...)},
		{name: "bom", payload: append([]byte{0xef, 0xbb, 0xbf}, encoded...)},
		{name: "pretty whitespace", payload: append(encoded, '\n')},
		{name: "different field order", payload: []byte(strings.Replace(string(encoded), canonicalPrefix, reorderedPrefix, 1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseFrozenCorpus(test.payload, digest); err == nil {
				t.Fatal("non-strict/non-canonical FrozenCorpus was accepted")
			}
		})
	}
}

func TestValidateFrozenCorpusRejectsIncompleteOrAmbiguousBindings(t *testing.T) {
	profile := validModelProfile()
	tests := []struct {
		name   string
		mutate func(*FrozenCorpus)
	}{
		{name: "old schema", mutate: func(value *FrozenCorpus) { value.SchemaVersion = "worksflow-frozen-model-conformance-corpus/v0" }},
		{name: "invalid corpus id", mutate: func(value *FrozenCorpus) { value.ID = "corpus-latest" }},
		{name: "profile alias", mutate: func(value *FrozenCorpus) { value.Profile.ID = "profile-latest" }},
		{name: "profile hash missing", mutate: func(value *FrozenCorpus) { value.Profile.ContentHash = "" }},
		{name: "profile workload noncanonical", mutate: func(value *FrozenCorpus) { value.Profile.Workload = "Constructor" }},
		{name: "threshold not immutable", mutate: func(value *FrozenCorpus) { value.ThresholdPolicyHash = "thresholds:latest" }},
		{name: "harness not immutable", mutate: func(value *FrozenCorpus) { value.HarnessHash = "main" }},
		{name: "verifier not immutable", mutate: func(value *FrozenCorpus) { value.VerifierHash = "sha256:ABC" }},
		{name: "null cases", mutate: func(value *FrozenCorpus) { value.Cases = nil }},
		{name: "empty cases", mutate: func(value *FrozenCorpus) { value.Cases = []CorpusCase{} }},
		{name: "unsorted cases", mutate: func(value *FrozenCorpus) {
			first := value.Cases[0]
			second := first
			first.ID = "case-zulu"
			first.Input.ArtifactID = "input-zulu"
			first.HiddenOracle.ArtifactID = "oracle-zulu"
			second.ID = "case-alpha"
			second.Input.ArtifactID = "input-alpha"
			second.HiddenOracle.ArtifactID = "oracle-alpha"
			value.Cases = []CorpusCase{first, second}
		}},
		{name: "duplicate case", mutate: func(value *FrozenCorpus) { value.Cases = append(value.Cases, value.Cases[0]) }},
		{name: "input alias", mutate: func(value *FrozenCorpus) { value.Cases[0].Input.ArtifactID = "Input Latest" }},
		{name: "input media parameters", mutate: func(value *FrozenCorpus) { value.Cases[0].Input.MediaType = "application/json; charset=utf-8" }},
		{name: "input hash drift", mutate: func(value *FrozenCorpus) { value.Cases[0].Input.ContentHash = "sha256:ABC" }},
		{name: "oracle plaintext media", mutate: func(value *FrozenCorpus) { value.Cases[0].HiddenOracle.MediaType = "application/json" }},
		{name: "oracle equals input", mutate: func(value *FrozenCorpus) { value.Cases[0].HiddenOracle.ArtifactID = value.Cases[0].Input.ArtifactID }},
		{name: "oracle exposes same content", mutate: func(value *FrozenCorpus) {
			value.Cases[0].HiddenOracle.PlaintextCommitmentHash = value.Cases[0].HiddenOracle.CiphertextHash
		}},
		{name: "input equals same case oracle plaintext", mutate: func(value *FrozenCorpus) {
			value.Cases[0].Input.ContentHash = value.Cases[0].HiddenOracle.PlaintextCommitmentHash
		}},
		{name: "input equals same case oracle ciphertext", mutate: func(value *FrozenCorpus) {
			value.Cases[0].Input.ContentHash = value.Cases[0].HiddenOracle.CiphertextHash
		}},
		{name: "input equals another case oracle plaintext", mutate: func(value *FrozenCorpus) {
			second := value.Cases[0]
			second.ID = "case-secondary"
			second.Input.ArtifactID = "input-secondary"
			second.Input.ContentHash = testDigest("input-secondary")
			second.HiddenOracle.ArtifactID = "oracle-secondary"
			second.HiddenOracle.CiphertextHash = testDigest("ciphertext-secondary")
			second.HiddenOracle.PlaintextCommitmentHash = value.Cases[0].Input.ContentHash
			second.HiddenOracle.KeyPolicyHash = testDigest("oracle-key-policy-secondary")
			value.Cases = append(value.Cases, second)
		}},
		{name: "input equals another case oracle ciphertext", mutate: func(value *FrozenCorpus) {
			second := value.Cases[0]
			second.ID = "case-secondary"
			second.Input.ArtifactID = "input-secondary"
			second.Input.ContentHash = testDigest("input-secondary")
			second.HiddenOracle.ArtifactID = "oracle-secondary"
			second.HiddenOracle.CiphertextHash = value.Cases[0].Input.ContentHash
			second.HiddenOracle.PlaintextCommitmentHash = testDigest("oracle-plaintext-secondary")
			second.HiddenOracle.KeyPolicyHash = testDigest("oracle-key-policy-secondary")
			value.Cases = append(value.Cases, second)
		}},
		{name: "build contract alias", mutate: func(value *FrozenCorpus) { value.Cases[0].BuildContract.ID = "latest" }},
		{name: "build contract hash missing", mutate: func(value *FrozenCorpus) { value.Cases[0].BuildContract.ContractHash = "" }},
		{name: "template release alias", mutate: func(value *FrozenCorpus) { value.Cases[0].TemplateRelease.ID = "latest" }},
		{name: "template approval missing", mutate: func(value *FrozenCorpus) { value.Cases[0].TemplateRelease.ApprovalReceiptDigest = "" }},
		{name: "base branch", mutate: func(value *FrozenCorpus) { value.Cases[0].BaseTree.Commit = "main" }},
		{name: "wrong tree schema", mutate: func(value *FrozenCorpus) { value.Cases[0].BaseTree.TreeDigestSchema = "git-tree/v1" }},
		{name: "dirty tree", mutate: func(value *FrozenCorpus) { value.Cases[0].BaseTree.Dirty = true }},
		{name: "zero repetitions", mutate: func(value *FrozenCorpus) { value.Cases[0].Repetitions = 0 }},
		{name: "unbounded repetitions", mutate: func(value *FrozenCorpus) { value.Cases[0].Repetitions = 101 }},
		{name: "duplicate artifact across cases", mutate: func(value *FrozenCorpus) {
			second := value.Cases[0]
			second.ID = "case-second"
			second.HiddenOracle.ArtifactID = "oracle-second"
			value.Cases = append(value.Cases, second)
		}},
		{name: "conflicting build identity", mutate: func(value *FrozenCorpus) {
			second := value.Cases[0]
			second.ID = "case-second"
			second.Input.ArtifactID = "input-second"
			second.HiddenOracle.ArtifactID = "oracle-second"
			second.BuildContract.ContentHash = testDigest("different-build")
			value.Cases = append(value.Cases, second)
		}},
		{name: "conflicting template identity", mutate: func(value *FrozenCorpus) {
			second := value.Cases[0]
			second.ID = "case-second"
			second.Input.ArtifactID = "input-second"
			second.HiddenOracle.ArtifactID = "oracle-second"
			second.TemplateRelease.ContentHash = testDigest("different-template")
			value.Cases = append(value.Cases, second)
		}},
		{name: "conflicting base commit", mutate: func(value *FrozenCorpus) {
			second := value.Cases[0]
			second.ID = "case-second"
			second.Input.ArtifactID = "input-second"
			second.HiddenOracle.ArtifactID = "oracle-second"
			second.BaseTree.TreeDigest = testDigest("different-tree")
			value.Cases = append(value.Cases, second)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corpus := validFrozenCorpus(t, profile)
			test.mutate(&corpus)
			if err := ValidateFrozenCorpus(corpus); err == nil {
				t.Fatal("invalid FrozenCorpus was accepted")
			}
		})
	}
}

func TestFrozenCorpusProfileBindingRejectsIdentityWorkloadAndHashDrift(t *testing.T) {
	profile := validModelProfile()
	tests := []struct {
		name   string
		mutate func(*FrozenCorpus)
	}{
		{name: "id", mutate: func(value *FrozenCorpus) { value.Profile.ID = "22222222-2222-4222-8222-222222222222" }},
		{name: "workload", mutate: func(value *FrozenCorpus) { value.Profile.Workload = "constructor-planning" }},
		{name: "content hash", mutate: func(value *FrozenCorpus) { value.Profile.ContentHash = testDigest("different-profile") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corpus := validFrozenCorpus(t, profile)
			test.mutate(&corpus)
			if err := ValidateFrozenCorpusForProfile(corpus, profile); err == nil {
				t.Fatal("drifted profile binding was accepted")
			}
		})
	}
}

func validFrozenCorpus(t *testing.T, profile ModelProfile) FrozenCorpus {
	t.Helper()
	profileHash, err := ModelProfileHash(profile)
	if err != nil {
		t.Fatalf("hash profile fixture: %v", err)
	}
	return FrozenCorpus{
		SchemaVersion: FrozenCorpusSchemaVersion,
		ID:            "33333333-3333-4333-8333-333333333333",
		Profile: CorpusProfileBinding{
			ID: profile.ID, ContentHash: profileHash, Workload: profile.Workload,
		},
		ThresholdPolicyHash: testDigest("threshold-policy"),
		HarnessHash:         testDigest("harness"),
		VerifierHash:        testDigest("verifier"),
		Cases: []CorpusCase{
			{
				ID: "case-primary",
				Input: InputArtifactBinding{
					ArtifactID: "input-primary", MediaType: "application/json", ContentHash: testDigest("input"),
				},
				HiddenOracle: HiddenOracleBinding{
					ArtifactID: "oracle-primary", MediaType: "application/octet-stream", CiphertextHash: testDigest("ciphertext"),
					PlaintextCommitmentHash: testDigest("oracle-plaintext"), KeyPolicyHash: testDigest("oracle-key-policy"),
				},
				BuildContract: BuildContractBinding{
					ID: "44444444-4444-4444-8444-444444444444", ContentHash: testDigest("build-content"), ContractHash: testDigest("build-contract"),
				},
				TemplateRelease: TemplateReleaseBinding{
					ID: "55555555-5555-4555-8555-555555555555", ContentHash: testDigest("template-content"), ApprovalReceiptDigest: testDigest("template-approval"),
				},
				BaseTree: BaseTreeBinding{
					Commit: strings.Repeat("a", 40), TreeDigestSchema: SourceTreeDigestSchemaV1, TreeDigest: testDigest("base-tree"), Dirty: false,
				},
				Repetitions: 3,
			},
		},
	}
}

func mustCanonicalCorpus(t *testing.T, corpus FrozenCorpus) []byte {
	t.Helper()
	encoded, err := CanonicalFrozenCorpusJSON(corpus)
	if err != nil {
		t.Fatalf("canonicalize corpus: %v", err)
	}
	return encoded
}
