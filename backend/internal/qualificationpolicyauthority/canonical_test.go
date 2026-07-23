package qualificationpolicyauthority

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanonicalV1GoldenDomainHashes(t *testing.T) {
	record, err := compileRecord(validIssueCommand(), validResolvedPolicy(), 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"revision":  "sha256:413798171ebfa0cebfd79175201767b78b91abefbd037edb48760b374408cacd",
		"plan":      "sha256:bf054957c6514c4420cb199c5ab513327bb9c64b408c8d15d59cafd7d475ed87",
		"promotion": "sha256:b5af84b93efd856c3e147354fdcd1a41b3502d95f5f56820f1ab6f504396fe11",
		"authority": "sha256:c1455eed95e713fe23c1cd9a331a7080baeddd25676a292f5e9e611f92cc4a65",
	}
	got := map[string]string{
		"revision":  record.RevisionPolicyHash,
		"plan":      record.PlanInputProfileHash,
		"promotion": record.PromotionPolicyHash,
		"authority": record.AuthorityHash,
	}
	for name, wantHash := range want {
		if got[name] != wantHash {
			t.Errorf("%s golden hash = %q; want %q", name, got[name], wantHash)
		}
	}
	if _, exists := reflect.TypeOf(AuthorityDocument{}).FieldByName("AuthorityHash"); exists {
		t.Fatal("root authority hash recursively entered its own document type")
	}
	if !bytes.Contains(record.DocumentBytes, []byte(`"policySourceId":"reviewed-release-2026-07-19"`)) {
		t.Fatal("opaque reviewed source provenance is not retained in root bytes")
	}
	if reflect.TypeOf(AuthorityDocument{}).NumField() != 16 {
		t.Fatalf("AuthorityDocument field closure drifted: %d", reflect.TypeOf(AuthorityDocument{}).NumField())
	}
}

func TestStrictDecodersRejectUnknownDuplicateMissingAndNonCanonicalJSON(t *testing.T) {
	policy := validResolvedPolicy().RevisionPolicy
	encoded, hash, err := EncodeRevisionPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodeRevisionPolicy(encoded, hash); err != nil || !reflect.DeepEqual(decoded, policy) {
		t.Fatalf("exact revision decode = policy:%+v error:%v", decoded, err)
	}

	unknown := append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"unknown":true}`)...)
	duplicate := bytes.Replace(
		encoded,
		[]byte(`"schemaVersion":"`+RevisionPolicySchemaV1+`",`),
		[]byte(`"schemaVersion":"`+RevisionPolicySchemaV1+`","schemaVersion":"`+RevisionPolicySchemaV1+`",`),
		1,
	)
	missing := bytes.Replace(encoded, []byte(`"canonicalReviewRequired":false,`), nil, 1)
	nonCanonical := append([]byte(" "), encoded...)
	invalidUTF8 := append(append([]byte(nil), encoded...), 0xff)
	for name, candidate := range map[string][]byte{
		"unknown":       unknown,
		"duplicate":     duplicate,
		"missing":       missing,
		"non-canonical": nonCanonical,
		"invalid UTF-8": invalidUTF8,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRevisionPolicy(candidate, hash); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeRevisionPolicy() error = %v", err)
			}
		})
	}
	if _, err := DecodeRevisionPolicy(encoded, testDigest("wrong-domain-hash")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong expected hash error = %v", err)
	}

	record, err := compileRecord(validIssueCommand(), validResolvedPolicy(), 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	rootUnknown := append(append([]byte(nil), record.DocumentBytes[:len(record.DocumentBytes)-1]...), []byte(`,"zzUnknown":true}`)...)
	if _, err := DecodeAuthorityDocument(rootUnknown, record.AuthorityHash); !errors.Is(err, ErrInvalid) {
		t.Fatalf("widened root error = %v", err)
	}
}

func TestPlanAndPromotionDecodersAreClosedAtEveryNestedObject(t *testing.T) {
	profile := validResolvedPolicy().PlanInputProfile
	profileBytes, profileHash, err := EncodePlanInputProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodePlanInputProfile(profileBytes, profileHash); err != nil || !reflect.DeepEqual(decoded, profile) {
		t.Fatalf("exact plan profile decode = profile:%+v error:%v", decoded, err)
	}
	widenedProfile := bytes.Replace(
		profileBytes,
		[]byte(`"maximumArtifacts":512`),
		[]byte(`"maximumArtifacts":512,"unsafeDefault":true`),
		1,
	)
	missingProfileMember := bytes.Replace(profileBytes, []byte(`"requireTrace":true,`), nil, 1)
	floatProfile := bytes.Replace(profileBytes, []byte(`"maximumArtifacts":512`), []byte(`"maximumArtifacts":512.0`), 1)
	for name, candidate := range map[string][]byte{
		"nested unknown": widenedProfile,
		"missing bool":   missingProfileMember,
		"float":          floatProfile,
	} {
		t.Run("plan "+name, func(t *testing.T) {
			if _, err := DecodePlanInputProfile(candidate, profileHash); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodePlanInputProfile() error = %v", err)
			}
		})
	}

	promotion := validResolvedPolicy().PromotionPolicy
	promotionBytes, promotionHash, err := EncodePromotionPolicy(promotion)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodePromotionPolicy(promotionBytes, promotionHash); err != nil || !reflect.DeepEqual(decoded, promotion) {
		t.Fatalf("exact promotion decode = policy:%+v error:%v", decoded, err)
	}
	widenedPromotion := bytes.Replace(
		promotionBytes,
		[]byte(`"authorityHash":"`+promotion.IndependentRequirements[0].AuthorityHash+`"`),
		[]byte(`"authorityHash":"`+promotion.IndependentRequirements[0].AuthorityHash+`","required":true`),
		1,
	)
	missingPromotionMember := bytes.Replace(
		promotionBytes,
		[]byte(`"independentRequirements":`),
		[]byte(`"ignoredRequirements":`),
		1,
	)
	for name, candidate := range map[string][]byte{
		"nested unknown": widenedPromotion,
		"missing member": missingPromotionMember,
	} {
		t.Run("promotion "+name, func(t *testing.T) {
			if _, err := DecodePromotionPolicy(candidate, promotionHash); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodePromotionPolicy() error = %v", err)
			}
		})
	}
}

func TestPolicySourceProvenanceChangesAuthorityHash(t *testing.T) {
	command := validIssueCommand()
	first, err := compileRecord(command, validResolvedPolicy(), 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	command.PolicySourceID = "reviewed-release-2026-07-20"
	second, err := compileRecord(command, validResolvedPolicy(), 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	if first.AuthorityHash == second.AuthorityHash || bytes.Equal(first.DocumentBytes, second.DocumentBytes) ||
		first.RevisionPolicyHash != second.RevisionPolicyHash || first.PlanInputProfileHash != second.PlanInputProfileHash ||
		first.PromotionPolicyHash != second.PromotionPolicyHash {
		t.Fatalf("source provenance did not affect only root authority: first=%s second=%s", first.AuthorityHash, second.AuthorityHash)
	}

	tampered := cloneRecord(first)
	tampered.Command.PolicySourceID = command.PolicySourceID
	if err := ValidateRecord(tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("coherent metadata-only source tamper error = %v", err)
	}
}

func TestValidateRecordDetectsCanonicalAndProjectionTampering(t *testing.T) {
	record, err := compileRecord(validIssueCommand(), validResolvedPolicy(), 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Record){
		"root bytes": func(value *Record) {
			value.DocumentBytes = append([]byte(nil), value.DocumentBytes...)
			value.DocumentBytes[len(value.DocumentBytes)-1] = ']'
		},
		"component bytes": func(value *Record) {
			value.RevisionPolicyBytes = append([]byte(nil), value.RevisionPolicyBytes...)
			value.RevisionPolicyBytes[0] = '['
		},
		"typed component": func(value *Record) {
			value.PlanInputProfile.Artifacts[0].ID = "mutated-artifact"
		},
		"root hash": func(value *Record) {
			value.AuthorityHash = testDigest("tampered-root")
		},
		"database time": func(value *Record) {
			value.IssuedAt = value.IssuedAt.Add(time.Millisecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneRecord(record)
			mutate(&candidate)
			if err := ValidateRecord(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateRecord() error = %v", err)
			}
		})
	}
}

func TestCanonicalJSONRejectsMalformedGoUTF8AndDomainConfusion(t *testing.T) {
	malformed := struct {
		Value string `json:"value"`
	}{Value: string([]byte{0xff})}
	if _, err := CanonicalJSON(malformed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed Go UTF-8 error = %v", err)
	}
	canonical := []byte(`{"schemaVersion":"example/v1"}`)
	first := DomainHash(RevisionPolicyHashDomainV1, canonical)
	second := DomainHash(PlanInputProfileHashDomainV1, canonical)
	if first == second || !strings.HasPrefix(first, "sha256:") || len(first) != 71 {
		t.Fatalf("domain hashes are confused: %q %q", first, second)
	}
}
