package workflowinputauthority

import "testing"

func TestRetainedMaterialSafetyAllowsDeclarationsWithoutValues(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"environmentVariables":[{"name":"DATABASE_URL","required":true,"scope":"api-runtime","secret":true}],"gateway":{"providerCredentials":"gateway-only"},"publish":{"allowRollback":true,"environment":"production","requiredRole":"owner"},"schema":{"properties":{"authorization":{"type":"string"}}}}`)
	if err := validateRetainedMaterialSafety("safe", raw, len(raw)); err != nil {
		t.Fatalf("safe non-secret declarations were rejected: %v", err)
	}
}

func TestRetainedMaterialSafetyRejectsForbiddenContent(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"credential field":            `{"accessToken":"opaque-value"}`,
		"provider api key field":      `{"openaiApiKey":"opaque-value"}`,
		"provider credential field":   `{"providerCredentials":"opaque-value"}`,
		"qualified password field":    `{"databasePassword":"opaque-value"}`,
		"qualified headers field":     `{"httpHeaders":{"x-request-id":"value"}}`,
		"secret field":                `{"secret":"opaque-value"}`,
		"transport headers":           `{"headers":{"x-request-id":"value"}}`,
		"environment map value":       `{"env":{"PORT":"3000"}}`,
		"qualified environment map":   `{"runtimeEnv":{"PORT":"3000"}}`,
		"free environment scalar":     `{"environment":"production"}`,
		"widened publish declaration": `{"publish":{"allowRollback":true,"environment":"production","requiredRole":"owner","value":"secret"}}`,
		"unsafe publish environment":  `{"publish":{"allowRollback":true,"environment":"prod-secret","requiredRole":"owner"}}`,
		"environment declared value":  `{"environmentVariables":[{"name":"PORT","value":"3000"}]}`,
		"sensitive schema default":    `{"properties":{"authorization":{"type":"string","default":"Bearer value"}}}`,
		"private key":                 `{"notes":"-----BEGIN PRIVATE KEY-----\nmaterial"}`,
		"provider token":              `{"notes":"sk-1234567890abcdefghijkl"}`,
		"jwt":                         `{"notes":"eyJabcdefghijk.abcdefghijkl.abcdefghijkl"}`,
		"credential URL":              `{"notes":"postgres://user:password@example.invalid/db"}`,
		"authorization header":        `{"notes":"Authorization: Bearer opaque-value"}`,
		"environment assignment":      `{"notes":"DATABASE_URL=postgres://database.invalid/app"}`,
		"absolute POSIX host path":    `{"notes":"read /home/runner/.config"}`,
		"absolute Windows path":       `{"notes":"read C:\\\\Users\\\\runner\\\\key"}`,
	}
	for name, document := range tests {
		name, document := name, document
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw := []byte(document)
			if err := validateRetainedMaterialSafety("unsafe", raw, len(raw)); err == nil {
				t.Fatal("forbidden retained material was accepted")
			}
		})
	}
}

func TestCompileRejectsSecretBearingRunScope(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	candidate.Materials.RunScope = []byte(`{"accessToken":"opaque-value"}`)
	candidate.Input.Run.ScopeRawBytesHash = RawSHA256(candidate.Materials.RunScope)
	candidate.Input.Run.ScopeRawBytesSize = int64(len(candidate.Materials.RunScope))
	if _, err := Compile(candidate); err == nil {
		t.Fatal("authority compiled with a secret-bearing retained run scope")
	}
}
