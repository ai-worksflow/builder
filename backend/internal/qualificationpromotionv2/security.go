package qualificationpromotionv2

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Promotion documents are durable authority records, not a secret store.
// Scan decoded string values so JSON punctuation cannot hide transport,
// credential, environment, key, or host-path material.
var (
	privateKeyPattern            = regexp.MustCompile(`(?i)-----BEGIN[ \t]+(?:[A-Z0-9]+[ \t]+)*PRIVATE KEY-----`)
	providerTokenPattern         = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|(?:gh[pousr]|github_pat)_[a-z0-9_-]{16,}|AKIA[0-9A-Z]{16})\b`)
	jwtPattern                   = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\b`)
	credentialURLPattern         = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@`)
	credentialAssignmentPattern  = regexp.MustCompile(`(?i)\b(?:api[_-]?key|client[_-]?secret|auth[_-]?token|password|passwd|private[_-]?key|database[_-]?url)\b\s*[:=]\s*["']?\S{8,}`)
	headerValuePattern           = regexp.MustCompile(`(?i)(?:^|\n)[ \t]*(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key)[ \t]*:[ \t]*\S+`)
	bearerPattern                = regexp.MustCompile(`(?i)\bbearer[ \t]+[a-z0-9._~+/-]{8,}=*`)
	environmentAssignmentPattern = regexp.MustCompile(`(?i)(?:^|\n)[ \t]*(?:export[ \t]+)?[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|PRIVATE_KEY|DATABASE_URL)[A-Z0-9_]*[ \t]*=[ \t]*\S+`)
	absoluteHostPathPattern      = regexp.MustCompile(`(?i)(?:^|[\s"'=(:])(?:file://|/(?:home|users|root|tmp|var|etc|run|proc|sys|dev|opt|srv|mnt|media|data|workspace|workspaces)(?:[/\\]|$)|[a-z]:[/\\]|\\\\[a-z0-9._-]+[/\\])`)
)

func validateSecretFree(name string, encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalid(name, "cannot scan malformed JSON")
	}
	stack := []any{value}
	for len(stack) > 0 {
		value = stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch typed := value.(type) {
		case string:
			if forbiddenSecretString(typed) {
				return invalid(name, "contains forbidden secret, credential, header, environment value, private key, or host path material")
			}
		case []any:
			stack = append(stack, typed...)
		case map[string]any:
			for key, child := range typed {
				normalized := normalizeSecurityName(key)
				if forbiddenSecurityName(normalized) {
					return invalid(name, "contains forbidden secret-bearing field")
				}
				stack = append(stack, child)
			}
		}
	}
	return nil
}

func forbiddenSecretString(value string) bool {
	return privateKeyPattern.MatchString(value) || providerTokenPattern.MatchString(value) || jwtPattern.MatchString(value) ||
		credentialURLPattern.MatchString(value) || credentialAssignmentPattern.MatchString(value) ||
		headerValuePattern.MatchString(value) || bearerPattern.MatchString(value) ||
		environmentAssignmentPattern.MatchString(value) || absoluteHostPathPattern.MatchString(value)
}

func normalizeSecurityName(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func forbiddenSecurityName(value string) bool {
	for _, suffix := range []string{
		"accesstoken", "apikey", "authorizationheader", "authtoken", "bearertoken", "clientsecret",
		"cookie", "cookieheader", "credentials", "credentialvalue", "environmentvalue", "envvalue", "idtoken",
		"password", "passwd", "passphrase", "plaintextsecret", "privatekey", "privatekeypem", "proxyauthorization",
		"rawsecret", "rawtoken", "refreshtoken", "requestheaders", "responseheaders", "secretvalue", "sessioncookie",
		"sessiontoken", "setcookie", "storagestate",
	} {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}
