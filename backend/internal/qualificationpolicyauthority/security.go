package qualificationpolicyauthority

import (
	"regexp"
	"strings"
)

// Policy authorities are durable control-plane evidence, never a deployment
// configuration or secret store. Closed structs remove generic extension
// points; these patterns provide a second fail-closed layer for opaque IDs and
// other strings whose values cannot be completely enumerated.
var (
	privateKeyPattern    = regexp.MustCompile(`(?i)-----BEGIN[ \t]+(?:[A-Z0-9]+[ \t]+)*PRIVATE KEY-----`)
	providerTokenPattern = regexp.MustCompile(
		`(?i)\b(?:sk-[a-z0-9_-]{16,}|(?:gh[pousr]|github_pat)_[a-z0-9_-]{16,}|AKIA[0-9A-Z]{16})\b`,
	)
	jwtPattern           = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\b`)
	credentialURLPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@:"]+:[^\s/@"]+@`)
	forbiddenURLPattern  = regexp.MustCompile(
		`(?i)\b(?:https?|file|ftp|postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|redis|amqp|nats)://`,
	)
	credentialAssignmentPattern = regexp.MustCompile(
		`(?i)\b(?:api[_-]?key|client[_-]?secret|auth[_-]?token|password|passwd|private[_-]?key)\b\s*[:=]\s*["'][^"'\r\n]{8,}["']`,
	)
	headerValuePattern = regexp.MustCompile(
		`(?im)(?:^|\n)[ \t]*(?:authorization|proxy-authorization|cookie|set-cookie)[ \t]*:[ \t]*\S+`,
	)
	environmentAssignmentPattern = regexp.MustCompile(
		`(?im)(?:^|\n)[ \t]*(?:export[ \t]+)?[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|PRIVATE_KEY|DATABASE_URL)[A-Z0-9_]*[ \t]*=[ \t]*\S+`,
	)
	absoluteHostPathPattern = regexp.MustCompile(
		`(?i)(?:^|[\s"'=(:])(?:/(?:home|users|root|tmp|var|etc|run|proc|sys|dev|opt|srv|mnt|media|data|workspace|workspaces)(?:[/\\]|$)|[a-z]:[/\\]|\\\\[a-z0-9._-]+[/\\])`,
	)
	privateDSNPattern = regexp.MustCompile(
		`(?i)\b(?:host|hostname|server)\s*=\s*[^\s;]+(?:\s+|;)+(?:user|username|uid)\s*=\s*[^\s;]+(?:\s+|;)+(?:password|passwd|pwd)\s*=`,
	)
)

func validateSecretFree(field string, encoded []byte) error {
	if forbiddenSecretString(string(encoded)) {
		return invalid(field, "contains forbidden secret, credential, URL, DSN, header, environment value, or host path material")
	}
	return nil
}

func forbiddenSecretString(value string) bool {
	return privateKeyPattern.MatchString(value) || providerTokenPattern.MatchString(value) ||
		jwtPattern.MatchString(value) || credentialURLPattern.MatchString(value) || forbiddenURLPattern.MatchString(value) ||
		credentialAssignmentPattern.MatchString(value) || headerValuePattern.MatchString(value) ||
		environmentAssignmentPattern.MatchString(value) || absoluteHostPathPattern.MatchString(value) ||
		privateDSNPattern.MatchString(value) || containsInlineBearer(value)
}

func containsInlineBearer(value string) bool {
	lower := strings.ToLower(value)
	index := strings.Index(lower, "bearer ")
	if index < 0 {
		return false
	}
	remainder := value[index+len("bearer "):]
	return len(strings.TrimSpace(remainder)) >= 16
}
