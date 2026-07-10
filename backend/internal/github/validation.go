package github

import (
	"crypto/sha1" // #nosec G505 -- Git object IDs intentionally use SHA-1.
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	repositoryPartPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,98}[A-Za-z0-9_.])?$`)
	credentialPattern     = regexp.MustCompile(`(?i)\b(?:(?:gh[pousr]|github_pat)_[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9_-]{20,})\b`)
	assignedSecretPattern = regexp.MustCompile(`(?i)\b(?:api[_-]?key|client[_-]?secret|auth[_-]?token|github[_-]?token|password)\b\s*[:=]\s*["']([^"'\n]{12,})["']`)
)

func validateToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if len(token) < 8 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return "", invalid("token is not a valid personal access token")
	}
	return token, nil
}
func validateRepositoryPart(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if !repositoryPartPattern.MatchString(value) || strings.HasSuffix(value, ".") {
		return "", invalid(field + " contains unsupported characters")
	}
	return value, nil
}
func validateBranch(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	segments := strings.Split(value, "/")
	invalidBranch := value == "" || len(value) > 240 || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || strings.Contains(value, `\`) || strings.ContainsAny(value, " ~^:?*[")
	for _, segment := range segments {
		invalidBranch = invalidBranch || segment == "" || segment == "." || segment == ".." || strings.HasSuffix(segment, ".lock")
	}
	if invalidBranch {
		return "", invalid(field + " is not a safe Git branch name")
	}
	return value, nil
}
func validateWorkspacePath(value string) (string, error) {
	value = strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"), "./")
	cleaned := path.Clean(value)
	if value == "" || len(value) > 300 || cleaned != value || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") ||
		(len(value) > 2 && value[1] == ':' && value[2] == '/') {
		return "", invalid("files.path must be a safe relative path")
	}
	secretNames := map[string]bool{".npmrc": true, ".pypirc": true, "credentials.json": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true, "id_rsa": true}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		lower := strings.ToLower(segment)
		if segment == "" || segment == "." || segment == ".." || lower == ".git" || lower == ".next" || lower == "node_modules" ||
			strings.HasPrefix(lower, ".env") || strings.ContainsAny(segment, `<>:"|?*`) {
			return "", invalid("files.path identifies an unsafe, generated, or secret-bearing path")
		}
	}
	if secretNames[strings.ToLower(segments[len(segments)-1])] {
		return "", invalid("files.path identifies a secret-bearing file")
	}
	return value, nil
}
func containsCredential(value string) bool {
	if strings.Contains(value, "PRIVATE KEY-----") || credentialPattern.MatchString(value) {
		return true
	}
	for _, match := range assignedSecretPattern.FindAllStringSubmatch(value, -1) {
		candidate := strings.ToLower(match[1])
		if !strings.Contains(candidate, "placeholder") && !strings.Contains(candidate, "example") &&
			!strings.Contains(candidate, "changeme") && !strings.Contains(candidate, "process.env") &&
			!strings.Contains(candidate, "import.meta.env") {
			return true
		}
	}
	return false
}
func validateFiles(files []WorkspaceFile) ([]WorkspaceFile, error) {
	if len(files) == 0 || len(files) > MaxFileCount {
		return nil, invalid(fmt.Sprintf("files must contain between 1 and %d entries", MaxFileCount))
	}
	result := make([]WorkspaceFile, len(files))
	seen, total := map[string]bool{}, 0
	for index, file := range files {
		if !utf8.ValidString(file.Content) || len(file.Content) > MaxFileBytes || containsCredential(file.Content) {
			return nil, invalid(fmt.Sprintf("files[%d].content is too large, invalid UTF-8, or appears to contain a credential", index))
		}
		filePath, err := validateWorkspacePath(file.Path)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(filePath)
		if seen[key] {
			return nil, invalid("files contains duplicate paths")
		}
		seen[key] = true
		total += len([]byte(file.Content))
		result[index] = WorkspaceFile{Path: filePath, Content: file.Content}
	}
	if total > MaxTotalFileBytes {
		return nil, invalid(fmt.Sprintf("files exceeds %d total bytes", MaxTotalFileBytes))
	}
	return result, nil
}
func validatePreview(input PreviewInput) (PreviewInput, error) {
	var err error
	if input.Owner, err = validateRepositoryPart(input.Owner, "owner"); err != nil {
		return PreviewInput{}, err
	}
	if input.Repo, err = validateRepositoryPart(input.Repo, "repo"); err != nil {
		return PreviewInput{}, err
	}
	if input.Branch, err = validateBranch(input.Branch, "branch"); err != nil {
		return PreviewInput{}, err
	}
	input.Files, err = validateFiles(input.Files)
	return input, err
}
func gitBlobSHA(content string) string {
	value := []byte(content)
	hash := sha1.New() // #nosec G401 -- required by the Git object format.
	_, _ = fmt.Fprintf(hash, "blob %d\x00", len(value))
	_, _ = hash.Write(value)
	return hex.EncodeToString(hash.Sum(nil))
}
