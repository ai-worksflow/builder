package templateoperator

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestDecodeAdmissionRequestRejectsNonStrictJSON(t *testing.T) {
	valid := []byte(`{"schemaVersion":"template-artifact-authority-admission/v1"}`)
	if _, err := DecodeAdmissionRequest(valid); err != nil {
		t.Fatalf("decode minimal valid admission request: %v", err)
	}

	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "duplicate top-level name",
			input: []byte(`{"schemaVersion":"template-artifact-authority-admission/v1","attemptId":"first","attemptId":"second"}`),
			want:  `duplicate JSON object key "attemptId"`,
		},
		{
			name:  "duplicate nested name",
			input: []byte(`{"schemaVersion":"template-artifact-authority-admission/v1","candidate":{"sbomDigest":"first","sbomDigest":"second"}}`),
			want:  `duplicate JSON object key "sbomDigest"`,
		},
		{
			name:  "unknown name",
			input: []byte(`{"schemaVersion":"template-artifact-authority-admission/v1","unexpected":true}`),
			want:  `unknown field "unexpected"`,
		},
		{
			name:  "trailing value",
			input: []byte(`{"schemaVersion":"template-artifact-authority-admission/v1"} []`),
			want:  "trailing JSON value",
		},
		{
			name:  "oversized",
			input: bytes.Repeat([]byte{' '}, maxAdmissionRequestBytes+1),
			want:  "must be between 1 and",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeAdmissionRequest(test.input)
			testRequireErrorContains(t, err, test.want)
		})
	}
}

func TestAdmissionRequestRejectsAuthorityControlledFieldInjection(t *testing.T) {
	fields := []struct {
		name  string
		field string
		value string
	}{
		{name: "evidence", field: "evidence", value: `[]`},
		{name: "status", field: "status", value: `"approved"`},
		{name: "policy", field: "policyHash", value: `"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`},
		{name: "trust root", field: "trustRootDigest", value: `"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`},
		{name: "signature", field: "signature", value: `{}`},
		{name: "receipt identity", field: "receiptId", value: `"attacker-controlled"`},
		{name: "decision", field: "decision", value: `"passed"`},
		{name: "creation time", field: "createdAt", value: `"2026-07-18T00:00:00Z"`},
		{name: "update time", field: "updatedAt", value: `"2026-07-18T00:00:00Z"`},
		{name: "evaluation time", field: "evaluatedAt", value: `"2026-07-18T00:00:00Z"`},
		{name: "verification time", field: "verifiedAt", value: `"2026-07-18T00:00:00Z"`},
		{name: "approval time", field: "approvedAt", value: `"2026-07-18T00:00:00Z"`},
		{name: "observation time", field: "observedAt", value: `"2026-07-18T00:00:00Z"`},
	}
	locations := []struct {
		name   string
		format string
	}{
		{name: "request", format: `{"schemaVersion":"template-artifact-authority-admission/v1","%s":%s}`},
		{name: "candidate", format: `{"schemaVersion":"template-artifact-authority-admission/v1","candidate":{"%s":%s}}`},
		{name: "bundle", format: `{"schemaVersion":"template-artifact-authority-admission/v1","bundle":{"%s":%s}}`},
	}

	for _, field := range fields {
		for _, location := range locations {
			t.Run(field.name+" through "+location.name, func(t *testing.T) {
				input := fmt.Sprintf(location.format, field.field, field.value)
				_, err := DecodeAdmissionRequest([]byte(input))
				testRequireErrorContains(t, err, `unknown field "`+field.field+`"`)
			})
		}
	}
}

func TestAdmissionRequestExposesOnlyCallerSafeFields(t *testing.T) {
	typeOfRequest := reflect.TypeOf(AdmissionRequest{})
	got := make([]string, 0, typeOfRequest.NumField())
	for index := 0; index < typeOfRequest.NumField(); index++ {
		field := typeOfRequest.Field(index)
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonName == "" || jsonName == "-" {
			t.Fatalf("AdmissionRequest field %s has unsafe JSON tag %q", field.Name, field.Tag.Get("json"))
		}
		got = append(got, jsonName)
	}
	slices.Sort(got)
	want := []string{
		"attemptId",
		"bundle",
		"candidate",
		"evaluatedBy",
		"releaseId",
		"requestedBy",
		"schemaVersion",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("AdmissionRequest JSON fields = %#v, want only %#v", got, want)
	}
}

func TestNewFailsClosed(t *testing.T) {
	config := testConfigWithExpectedCommitments(t)
	presentLookup := func(string) (string, bool) { return "Bearer test-token", true }

	t.Run("nil database", func(t *testing.T) {
		operator, err := New(nil, testCloneConfig(t, config), presentLookup)
		if operator != nil {
			t.Fatal("New returned an operator with a nil database")
		}
		testRequireErrorContains(t, err, "database is required")
	})

	t.Run("nil environment lookup", func(t *testing.T) {
		operator, err := New(new(gorm.DB), testCloneConfig(t, config), nil)
		if operator != nil {
			t.Fatal("New returned an operator with a nil environment lookup")
		}
		testRequireErrorContains(t, err, "environment lookup is required")
	})

	t.Run("missing reviewed hashes", func(t *testing.T) {
		input := testCloneConfig(t, config)
		input.Authority.ExpectedPolicyHash = ""
		operator, err := New(new(gorm.DB), input, presentLookup)
		if operator != nil {
			t.Fatal("New returned an operator without complete reviewed commitment pins")
		}
		testRequireErrorContains(t, err, "reviewed expected policy and trust-root digests are required")
	})

	t.Run("policy hash mismatch", func(t *testing.T) {
		input := testCloneConfig(t, config)
		input.Authority.ExpectedPolicyHash = testDifferentDigest(input.Authority.ExpectedPolicyHash)
		operator, err := New(new(gorm.DB), input, presentLookup)
		if operator != nil {
			t.Fatal("New returned an operator with a mismatched expected policy hash")
		}
		testRequireErrorContains(t, err, "commitments do not match")
	})

	t.Run("trust root mismatch", func(t *testing.T) {
		input := testCloneConfig(t, config)
		input.Authority.ExpectedTrustRootDigest = testDifferentDigest(input.Authority.ExpectedTrustRootDigest)
		operator, err := New(new(gorm.DB), input, presentLookup)
		if operator != nil {
			t.Fatal("New returned an operator with a mismatched expected trust-root digest")
		}
		testRequireErrorContains(t, err, "commitments do not match")
	})

	t.Run("missing registry credential", func(t *testing.T) {
		input := testCloneConfig(t, config)
		operator, err := New(new(gorm.DB), input, func(string) (string, bool) { return "", false })
		if operator != nil {
			t.Fatal("New returned an operator without its configured registry credential")
		}
		testRequireErrorContains(t, err, "registry credential environment variable REGISTRY_A_TOKEN is required")
	})
}

func testDifferentDigest(input string) string {
	last := byte('0')
	if input[len(input)-1] == last {
		last = '1'
	}
	return input[:len(input)-1] + string(last)
}
