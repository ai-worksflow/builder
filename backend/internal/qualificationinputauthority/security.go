package qualificationinputauthority

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

var (
	privateKeyPattern            = regexp.MustCompile(`-----begin[ \t]+(?:[a-z0-9]+[ \t]+)*private key-----`)
	providerTokenPattern         = regexp.MustCompile(`\b(?:sk-[a-z0-9_-]{16,}|(?:gh[pousr]|github_pat)_[a-z0-9_-]{16,}|akia[0-9a-z]{16})\b`)
	jwtPattern                   = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\b`)
	credentialURLPattern         = regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@`)
	credentialAssignmentPattern  = regexp.MustCompile(`\b(?:api[_-]?key|client[_-]?secret|auth[_-]?token|password|passwd|private[_-]?key|database[_-]?url)\b\s*[:=]\s*["']?\S{8,}`)
	headerValuePattern           = regexp.MustCompile(`(?:^|\n)[ \t]*(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key)[ \t]*:[ \t]*\S+`)
	bearerPattern                = regexp.MustCompile(`\bbearer[ \t]+[a-z0-9._~+/-]{8,}=*`)
	environmentAssignmentPattern = regexp.MustCompile(`(?:^|\n)[ \t]*(?:export[ \t]+)?[a-z0-9_]*(?:secret|token|password|passwd|api_key|private_key|database_url)[a-z0-9_]*[ \t]*=[ \t]*\S+`)
	absoluteHostPathPattern      = regexp.MustCompile(`(?:^|[\s"'=(:])(?:file://|/(?:home|users|root|tmp|var|etc|run|proc|sys|dev|opt|srv|mnt|media|data|workspace|workspaces)(?:[/\\]|$)|[a-z]:[/\\]|\\\\[a-z0-9._-]+[/\\])`)
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
				return invalid(name, "contains forbidden secret, credential, environment, header, URL, or host-path material")
			}
		case []any:
			stack = append(stack, typed...)
		case map[string]any:
			for key, child := range typed {
				if forbiddenSecurityName(normalizeSecurityName(key)) {
					return invalid(name, "contains forbidden secret-bearing field")
				}
				stack = append(stack, child)
			}
		}
	}
	return nil
}

func forbiddenSecretString(value string) bool {
	folded := asciiLower(value)
	return privateKeyPattern.MatchString(folded) || providerTokenPattern.MatchString(folded) || jwtPattern.MatchString(value) ||
		credentialURLPattern.MatchString(folded) || credentialAssignmentPattern.MatchString(folded) ||
		headerValuePattern.MatchString(folded) || bearerPattern.MatchString(folded) ||
		environmentAssignmentPattern.MatchString(folded) || absoluteHostPathPattern.MatchString(folded)
}

func asciiLower(value string) string {
	var folded []byte
	for index := 0; index < len(value); index++ {
		if value[index] < 'A' || value[index] > 'Z' {
			continue
		}
		if folded == nil {
			folded = []byte(value)
		}
		folded[index] += 'a' - 'A'
	}
	if folded == nil {
		return value
	}
	return string(folded)
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
