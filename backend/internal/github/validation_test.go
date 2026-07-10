package github

import (
	"strings"
	"testing"
)

func TestWorkspaceValidationRejectsTraversalSecretsAndCredentials(t *testing.T) {
	t.Parallel()
	for _, file := range []WorkspaceFile{
		{Path: "../secret.txt", Content: "safe"},
		{Path: ".env.production", Content: "safe"},
		{Path: "src/config.ts", Content: `const github_token = "github_pat_abcdefghijklmnopqrstuvwxyz"`},
	} {
		if _, err := validateFiles([]WorkspaceFile{file}); err == nil {
			t.Fatalf("unsafe file was accepted: %+v", file)
		}
	}
	files, err := validateFiles([]WorkspaceFile{{Path: "src/app.ts", Content: "export const ready = true\n"}})
	if err != nil || len(files) != 1 {
		t.Fatalf("safe workspace rejected: files=%+v error=%v", files, err)
	}
}

func TestGitBlobSHAUsesCanonicalGitObjectFormat(t *testing.T) {
	t.Parallel()
	if got := gitBlobSHA("test\n"); got != "9daeafb9864cf43055ae93beb0afd6c7d144bfa4" {
		t.Fatalf("blob SHA = %s", got)
	}
	if strings.Contains(gitBlobSHA("different"), " ") {
		t.Fatal("blob SHA is not hexadecimal")
	}
}
