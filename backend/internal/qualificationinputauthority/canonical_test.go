package qualificationinputauthority

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestIssueRequestCanonicalGoldenAndStrictDecode(t *testing.T) {
	request := IssueRequest{
		AuthorityID:                    "11111111-1111-4111-8111-111111111111",
		OperationID:                    "22222222-2222-4222-8222-222222222222",
		QualificationPlanAuthorityID:   "33333333-3333-4333-8333-333333333333",
		QualificationPolicyAuthorityID: "44444444-4444-4444-8444-444444444444",
		SchemaVersion:                  IssueRequestSchemaV1,
		WorkflowInputAuthorityID:       "55555555-5555-4555-8555-555555555555",
	}
	encoded, hash, err := EncodeIssueRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"authorityId":"11111111-1111-4111-8111-111111111111","operationId":"22222222-2222-4222-8222-222222222222","qualificationPlanAuthorityId":"33333333-3333-4333-8333-333333333333","qualificationPolicyAuthorityId":"44444444-4444-4444-8444-444444444444","schemaVersion":"worksflow-qualification-input-precommit-request/v1","workflowInputAuthorityId":"55555555-5555-4555-8555-555555555555"}`
	if string(encoded) != want {
		t.Fatalf("canonical request mismatch\n got: %s\nwant: %s", encoded, want)
	}
	const wantHash = "sha256:ec42821599eef2a5d7f3888e5a0df0bb60a12ac3c3e011862d3a6fd31ce9402a"
	if hash != wantHash {
		t.Fatalf("domain hash mismatch: got %s want %s", hash, wantHash)
	}
	decoded, err := DecodeIssueRequest(encoded, hash)
	if err != nil || decoded != request {
		t.Fatalf("round trip failed: decoded=%+v err=%v", decoded, err)
	}

	duplicate := bytes.Replace(
		encoded,
		[]byte(`{"authorityId":`),
		[]byte(`{"authorityId":"11111111-1111-4111-8111-111111111111","authorityId":`),
		1,
	)
	unknown := append([]byte(`{"aaa":"unexpected",`), encoded[1:]...)
	for name, malformed := range map[string][]byte{
		"duplicate":  duplicate,
		"unknown":    unknown,
		"whitespace": append(append([]byte(nil), encoded...), '\n'),
		"trailing":   append(append([]byte(nil), encoded...), []byte(`{}`)...),
		"bom":        append([]byte{0xef, 0xbb, 0xbf}, encoded...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeIssueRequest(malformed, hash); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected strict invalid result, got %v", err)
			}
		})
	}
}

func TestCanonicalRejectsMalformedUTF8AndSecretMaterial(t *testing.T) {
	request := IssueRequest{
		AuthorityID:                    "11111111-1111-4111-8111-111111111111",
		OperationID:                    "22222222-2222-4222-8222-222222222222",
		QualificationPlanAuthorityID:   "33333333-3333-4333-8333-333333333333",
		QualificationPolicyAuthorityID: "44444444-4444-4444-8444-444444444444",
		SchemaVersion:                  IssueRequestSchemaV1,
		WorkflowInputAuthorityID:       "55555555-5555-4555-8555-555555555555",
	}
	request.SchemaVersion = string([]byte{0xff})
	if _, _, err := EncodeIssueRequest(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected malformed UTF-8 rejection, got %v", err)
	}

	resolved := testResolvedAuthorities()
	credentialRequest := credentialRequestFromAuthoritySet(resolved)
	credentialRequest.CredentialProfile.Audience = "Bearer abcdefghijklmnop"
	credentialRequest.CredentialSet.Audience = credentialRequest.CredentialProfile.Audience
	if _, _, err := EncodeCredentialRequest(credentialRequest); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected secret scanner rejection, got %v", err)
	}
}

func TestForbiddenSecretStringUsesFrozenASCIIRegexClasses(t *testing.T) {
	for _, test := range []struct {
		value     string
		forbidden bool
	}{
		{value: "ésk-abcdefghijklmnop", forbidden: true},
		{value: "http://u\u2003:p@", forbidden: true},
		{value: "Authorization:\u2003x", forbidden: true},
		{value: "sk-abcdefghijklmno-", forbidden: false},
		{value: "sK-abcdefghijklmnop", forbidden: false},
		{value: "sk-abcdefſhijklmnop", forbidden: false},
		{value: "password\u2003=abcdefgh", forbidden: false},
		{value: "x\u2003/root/private", forbidden: false},
	} {
		if got := forbiddenSecretString(test.value); got != test.forbidden {
			t.Fatalf("forbiddenSecretString(%q)=%t, want %t", test.value, got, test.forbidden)
		}
	}
}
