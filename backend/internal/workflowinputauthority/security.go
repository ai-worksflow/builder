package workflowinputauthority

import (
	"regexp"
	"sort"
	"strings"
)

// Retained Workflow Input materials are durable qualification evidence, not a
// secret store. The scanner is deliberately structural: safe declarations
// such as {"name":"DATABASE_URL","secret":true} remain representable, while
// credential values, environment values, transport headers, key material and
// host paths fail closed before an authority can be compiled or replayed.
var forbiddenRetainedNames = map[string]struct{}{
	"accesstoken": {}, "apikey": {}, "authorizationheader": {}, "authtoken": {}, "bearertoken": {},
	"clientsecret": {}, "cookie": {}, "cookieheader": {}, "cookies": {}, "credential": {}, "credentials": {},
	"credentialvalue": {}, "csrftoken": {}, "encryptedsecret": {}, "environmentvalue": {}, "environmentvalues": {},
	"envvalue": {}, "envvalues": {}, "idtoken": {}, "password": {}, "passwd": {}, "passphrase": {},
	"plaintextsecret": {}, "privatekey": {}, "privatekeypem": {}, "proxyauthorization": {}, "rawsecret": {},
	"rawtoken": {}, "refreshtoken": {}, "requestheaders": {}, "responseheaders": {}, "secretvalue": {},
	"secretvalues": {}, "sessioncookie": {}, "sessiontoken": {}, "setcookie": {}, "storagestate": {},
}

var (
	retainedPrivateKeyPattern    = regexp.MustCompile(`(?i)-----BEGIN[ \t]+(?:[A-Z0-9]+[ \t]+)*PRIVATE KEY-----`)
	retainedProviderTokenPattern = regexp.MustCompile(
		`(?i)\b(?:sk-[a-z0-9_-]{16,}|(?:gh[pousr]|github_pat)_[a-z0-9_-]{16,}|AKIA[0-9A-Z]{16})\b`,
	)
	retainedJWTPattern                  = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\b`)
	retainedCredentialURLPattern        = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@`)
	retainedCredentialAssignmentPattern = regexp.MustCompile(
		`(?i)\b(?:api[_-]?key|client[_-]?secret|auth[_-]?token|password|passwd|private[_-]?key)\b\s*[:=]\s*["'][^"'\r\n]{8,}["']`,
	)
	retainedHeaderValuePattern = regexp.MustCompile(
		`(?im)(?:^|\n)[ \t]*(?:authorization|proxy-authorization|cookie|set-cookie)[ \t]*:[ \t]*\S+`,
	)
	retainedEnvironmentAssignmentPattern = regexp.MustCompile(
		`(?im)(?:^|\n)[ \t]*(?:export[ \t]+)?[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|PRIVATE_KEY|DATABASE_URL)[A-Z0-9_]*[ \t]*=[ \t]*\S+`,
	)
	retainedAbsoluteHostPathPattern = regexp.MustCompile(
		`(?i)(?:^|[\s"'=(:])(?:file://|/(?:home|users|root|tmp|var|etc|run|proc|sys|dev|opt|srv|mnt|media|data|workspace|workspaces)(?:[/\\]|$)|[a-z]:[/\\]|\\\\[a-z0-9._-]+[/\\])`,
	)
)

type retainedSafetyFrame struct {
	value                any
	environmentContainer bool
	environmentMember    bool
	publishDeclaration   bool
	schemaPropertyNames  bool
	sensitiveDeclaration bool
}

func validateRetainedMaterialSafety(name string, raw []byte, maximum int) error {
	value, err := decodeRetainedGeneric(name, raw, maximum)
	if err != nil {
		return err
	}
	stack := []retainedSafetyFrame{{value: value}}
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch typed := frame.value.(type) {
		case string:
			if forbiddenRetainedString(typed) {
				return invalid("materials."+name, "contains forbidden secret, credential, header, environment value, or host path material")
			}
		case []any:
			for index := len(typed) - 1; index >= 0; index-- {
				stack = append(stack, retainedSafetyFrame{
					value: typed[index], environmentMember: frame.environmentContainer,
					sensitiveDeclaration: frame.sensitiveDeclaration,
				})
			}
		case map[string]any:
			names := make([]string, 0, len(typed))
			for key := range typed {
				names = append(names, key)
			}
			sort.Sort(sort.Reverse(sort.StringSlice(names)))
			for _, key := range names {
				child := typed[key]
				normalized := normalizeRetainedName(key)
				sensitive := retainedSensitiveName(normalized)
				if sensitive && !frame.schemaPropertyNames && !safeRetainedDeclaration(normalized, child) {
					return invalid("materials."+name, "contains a forbidden secret, credential, token, cookie, or header field")
				}
				if (frame.environmentContainer || frame.environmentMember) && retainedEnvironmentValueName(normalized) {
					return invalid("materials."+name, "contains a forbidden environment value field")
				}
				if frame.environmentContainer && retainedEnvironmentScalar(child) {
					return invalid("materials."+name, "contains a forbidden environment value")
				}
				if frame.sensitiveDeclaration && retainedSchemaValueName(normalized) {
					return invalid("materials."+name, "contains a value for a sensitive schema declaration")
				}
				environment := retainedEnvironmentContainerName(normalized)
				if environment && retainedEnvironmentScalar(child) &&
					!(frame.publishDeclaration && normalized == "environment") {
					return invalid("materials."+name, "contains a forbidden environment value")
				}
				stack = append(stack, retainedSafetyFrame{
					value: child, environmentContainer: environment,
					environmentMember:    frame.environmentMember,
					publishDeclaration:   normalized == "publish" && safeRetainedPublishDeclaration(child),
					schemaPropertyNames:  normalized == "properties",
					sensitiveDeclaration: frame.sensitiveDeclaration || frame.schemaPropertyNames && sensitive,
				})
			}
		}
	}
	return nil
}

func safeRetainedPublishDeclaration(value any) bool {
	config, ok := value.(map[string]any)
	if !ok || len(config) != 3 {
		return false
	}
	environment, environmentOK := config["environment"].(string)
	requiredRole, roleOK := config["requiredRole"].(string)
	_, rollbackOK := config["allowRollback"].(bool)
	return environmentOK && (environment == "preview" || environment == "production") &&
		roleOK && requiredRole == strings.TrimSpace(requiredRole) && len(requiredRole) >= 1 && len(requiredRole) <= 256 &&
		rollbackOK
}

func normalizeRetainedName(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func retainedSensitiveName(value string) bool {
	if value == "secret" || value == "token" || value == "authorization" || value == "headers" {
		return true
	}
	if _, forbidden := forbiddenRetainedNames[value]; forbidden {
		return true
	}
	// Provider- and framework-qualified field names are at least as sensitive
	// as their suffix (for example openaiApiKey, databasePassword,
	// providerCredentials, httpHeaders, or runtimeSessionToken). Treating only
	// an exact spelling as sensitive would make the durable evidence scanner
	// depend on every vendor prefix that may ever exist.
	for _, suffix := range []string{
		"accesstoken", "apikey", "authorization", "authtoken", "bearertoken",
		"clientsecret", "cookie", "credentials", "credential", "headers",
		"idtoken", "password", "passwd", "passphrase", "privatekey",
		"refreshtoken", "secret", "sessiontoken", "token",
	} {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func safeRetainedDeclaration(name string, value any) bool {
	if name == "secret" {
		_, isBoolean := value.(bool)
		return isBoolean
	}
	// These closed values describe gateway ownership, never credential bytes.
	if strings.HasSuffix(name, "credentials") {
		policy, isString := value.(string)
		return isString && (policy == "gateway-only" || policy == "forbidden" || policy == "none")
	}
	return false
}

func retainedEnvironmentContainerName(value string) bool {
	for _, suffix := range []string{"environmentvariables", "environment", "env"} {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func retainedEnvironmentValueName(value string) bool {
	switch value {
	case "currentvalue", "default", "encryptedvalue", "plaintextvalue", "plainvalue", "value", "values":
		return true
	default:
		return false
	}
}

func retainedSchemaValueName(value string) bool {
	switch value {
	case "const", "default", "example", "examples", "enum":
		return true
	default:
		return false
	}
}

func retainedEnvironmentScalar(value any) bool {
	switch value.(type) {
	case nil, map[string]any, []any:
		return false
	default:
		return true
	}
}

func forbiddenRetainedString(value string) bool {
	return retainedPrivateKeyPattern.MatchString(value) || retainedProviderTokenPattern.MatchString(value) ||
		retainedJWTPattern.MatchString(value) || retainedCredentialURLPattern.MatchString(value) ||
		retainedCredentialAssignmentPattern.MatchString(value) || retainedHeaderValuePattern.MatchString(value) ||
		retainedEnvironmentAssignmentPattern.MatchString(value) || retainedAbsoluteHostPathPattern.MatchString(value)
}
