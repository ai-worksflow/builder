package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/templates"
)

type templateGitRunnerFake struct {
	trees    map[string][]byte
	blobs    map[string][]byte
	commands [][]string
}

func (runner *templateGitRunnerFake) Run(
	_ context.Context,
	_ int64,
	arguments ...string,
) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string(nil), arguments...))
	for index, argument := range arguments {
		switch argument {
		case "rev-parse":
			return []byte(strings.TrimSuffix(arguments[index+2], "^{commit}") + "\n"), nil
		case "ls-tree":
			return append([]byte(nil), runner.trees[arguments[len(arguments)-1]]...), nil
		case "cat-file":
			return append([]byte(nil), runner.blobs[arguments[len(arguments)-1]]...), nil
		}
	}
	return nil, nil
}

func TestGitTemplateSourceMaterializerPinsTreeMountsAndLock(t *testing.T) {
	webCommit := strings.Repeat("1", 40)
	apiCommit := strings.Repeat("2", 40)
	webObject := strings.Repeat("a", 40)
	apiObject := strings.Repeat("b", 40)
	webTree := []byte("100644 blob " + webObject + "\tpackage.json\x00")
	apiTree := []byte("100755 blob " + apiObject + "\tstart.sh\x00")
	runner := &templateGitRunnerFake{
		trees: map[string][]byte{webCommit: webTree, apiCommit: apiTree},
		blobs: map[string][]byte{
			webObject: []byte(`{"scripts":{"dev":"vite"}}`),
			apiObject: []byte("#!/bin/sh\nexec python -m app\n"),
		},
	}
	materializer, err := newGitTemplateSourceMaterializer(runner, GitTemplateSourceOptions{
		CacheRoot: t.TempDir(), AllowedHosts: []string{"github.com"}, FetchTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := TemplateSourceRequest{
		FullStackTemplate: ExactReference{ID: uuid.NewString(), ContentHash: digestForSource("stack")},
		BuildContract:     ExactReference{ID: uuid.NewString(), ContentHash: digestForSource("contract")},
		Components: []TemplateSourceComponent{
			templateSourceComponentWithAuthority(TemplateSourceComponent{
				Role: "web", MountPath: "apps/web", ReleaseID: uuid.NewString(),
				ReleaseContentHash: digestForSource("web-release"), ReleaseSubjectHash: digestForSource("web-subject"),
				Repository: "https://github.com/ai-worksflow/templates.git", Branch: "react-shadcn-template",
				Commit: webCommit, TreeHash: hashSourceBytes(webTree),
			}),
			templateSourceComponentWithAuthority(TemplateSourceComponent{
				Role: "api", MountPath: "services/api", ReleaseID: uuid.NewString(),
				ReleaseContentHash: digestForSource("api-release"), ReleaseSubjectHash: digestForSource("api-subject"),
				Repository: "https://github.com/ai-worksflow/templates.git", Branch: "python-fastapi-template",
				Commit: apiCommit, TreeHash: hashSourceBytes(apiTree),
			}),
		},
	}

	files, err := materializer.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("materialize exact TemplateReleases: %v", err)
	}
	if len(files) != 3 || files[0].Path != "apps/web/package.json" || files[0].Mode != "100644" ||
		files[1].Path != "services/api/start.sh" || files[1].Mode != "100755" ||
		files[2].Path != "templates.lock.json" ||
		!strings.Contains(string(files[2].Content), request.BuildContract.ID) ||
		!strings.Contains(string(files[2].Content), webCommit) {
		t.Fatalf("materialized source lost exact mounts, modes, or lock: %#v", files)
	}
	fetches := countTemplateGitCommand(runner.commands, "fetch")
	if fetches != 2 {
		t.Fatalf("first materialization fetch count = %d, want 2", fetches)
	}
	if _, err := materializer.Materialize(context.Background(), request); err != nil {
		t.Fatalf("reuse verified exact source cache: %v", err)
	}
	if countTemplateGitCommand(runner.commands, "fetch") != fetches {
		t.Fatal("verified source cache unexpectedly fetched mutable upstream state")
	}

	drifted := request
	drifted.Components = append([]TemplateSourceComponent(nil), request.Components...)
	drifted.Components[0].TreeHash = digestForSource("wrong-tree")
	if _, err := materializer.Materialize(context.Background(), drifted); !errors.Is(err, ErrTemplateSourceDrift) {
		t.Fatalf("tree digest drift error = %v, want ErrTemplateSourceDrift", err)
	}
}

func TestGitTemplateSourceMaterializerVerifiesSingleAuthoritySource(t *testing.T) {
	commit := strings.Repeat("3", 40)
	objectID := strings.Repeat("c", 40)
	tree := []byte("100644 blob " + objectID + "\ttemplate.json\x00")
	runner := &templateGitRunnerFake{
		trees: map[string][]byte{commit: tree},
		blobs: map[string][]byte{objectID: []byte(`{"schemaVersion":"template-manifest/v1"}`)},
	}
	materializer, err := newGitTemplateSourceMaterializer(runner, GitTemplateSourceOptions{
		CacheRoot: t.TempDir(), AllowedHosts: []string{"github.com"}, FetchTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := materializer.Readiness(context.Background()); err != nil {
		t.Fatalf("exact source readiness: %v", err)
	}
	source := templates.TemplateSource{
		Repository: "https://github.com/ai-worksflow/templates.git", Branch: "api-template",
		Commit: commit, TreeHash: hashSourceBytes(tree),
	}
	if err := materializer.VerifySource(context.Background(), source); err != nil {
		t.Fatalf("verify exact authority source: %v", err)
	}
	if countTemplateGitCommand(runner.commands, "fetch") != 1 ||
		countTemplateGitCommand(runner.commands, "ls-tree") != 1 ||
		countTemplateGitCommand(runner.commands, "cat-file") != 1 {
		t.Fatalf("source verifier did not fetch and read exact tree bytes: %#v", runner.commands)
	}
	source.TreeHash = digestForSource("drifted")
	if err := materializer.VerifySource(context.Background(), source); !errors.Is(err, ErrTemplateSourceDrift) {
		t.Fatalf("source tree drift error = %v, want ErrTemplateSourceDrift", err)
	}
	unsafe := source
	unsafe.Repository = "https://127.0.0.1/templates.git"
	if err := materializer.VerifySource(context.Background(), unsafe); !errors.Is(err, ErrTemplateSourceInvalid) {
		t.Fatalf("unsafe authority source error = %v, want ErrTemplateSourceInvalid", err)
	}
}

func TestTemplateSourceMaterializerRejectsUnsafeRepositoryAndGitEntries(t *testing.T) {
	runner := &templateGitRunnerFake{}
	materializer, err := newGitTemplateSourceMaterializer(runner, GitTemplateSourceOptions{
		CacheRoot: t.TempDir(), AllowedHosts: []string{"github.com"}, FetchTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := validTemplateSourceRequestForTest()
	request.Components[0].Repository = "https://127.0.0.1/templates.git"
	if _, err := materializer.Materialize(context.Background(), request); !errors.Is(err, ErrTemplateSourceInvalid) {
		t.Fatalf("private/unlisted source host error = %v", err)
	}
	for name, raw := range map[string][]byte{
		"symlink":     []byte("120000 blob " + strings.Repeat("a", 40) + "\tlink\x00"),
		"submodule":   []byte("160000 commit " + strings.Repeat("a", 40) + "\tvendor\x00"),
		"environment": []byte("100644 blob " + strings.Repeat("a", 40) + "\t.env.local\x00"),
		"not framed":  []byte("100644 blob " + strings.Repeat("a", 40) + "\tfile"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitTreeEntries(raw); err == nil {
				t.Fatal("unsafe Git entry was accepted")
			}
		})
	}
	if _, err := validateTemplateSourceFiles([]TemplateSourceFile{
		{Path: "src/App.tsx", Mode: "100644", Content: []byte("a")},
		{Path: "src/app.tsx", Mode: "100644", Content: []byte("b")},
	}); err == nil {
		t.Fatal("case-insensitive source collision was accepted")
	}
}

func validTemplateSourceRequestForTest() TemplateSourceRequest {
	component := func(role, mount, suffix string) TemplateSourceComponent {
		return templateSourceComponentWithAuthority(TemplateSourceComponent{
			Role: role, MountPath: mount, ReleaseID: uuid.NewString(),
			ReleaseContentHash: digestForSource(role + "-release"), ReleaseSubjectHash: digestForSource(role + "-subject"),
			Repository: "https://github.com/ai-worksflow/templates.git", Branch: role,
			Commit: strings.Repeat(suffix, 40), TreeHash: digestForSource(role + "-tree"),
		})
	}
	return TemplateSourceRequest{
		FullStackTemplate: ExactReference{ID: uuid.NewString(), ContentHash: digestForSource("stack")},
		BuildContract:     ExactReference{ID: uuid.NewString(), ContentHash: digestForSource("contract")},
		Components: []TemplateSourceComponent{
			component("web", "apps/web", "1"), component("api", "services/api", "2"),
		},
	}
}

func templateSourceComponentWithAuthority(component TemplateSourceComponent) TemplateSourceComponent {
	component.SBOMDigest = digestForSource(component.Role + "-sbom")
	component.SignatureBundleDigest = digestForSource(component.Role + "-signature")
	component.AuthorityReceiptID = uuid.NewString()
	component.AuthorityReceiptContentHash = digestForSource(component.Role + "-authority-receipt")
	component.AuthorityPolicyHash = digestForSource(component.Role + "-authority-policy")
	return component
}

func countTemplateGitCommand(commands [][]string, expected string) int {
	count := 0
	for _, command := range commands {
		for _, argument := range command {
			if argument == expected {
				count++
				break
			}
		}
	}
	return count
}

func hashSourceBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func digestForSource(value string) string { return hashSourceBytes([]byte(value)) }

func TestGitTemplateSourceMaterializerRejectsSymlinkCache(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	if _, err := newGitTemplateSourceMaterializer(&templateGitRunnerFake{}, GitTemplateSourceOptions{
		CacheRoot: filepath.Clean(root), AllowedHosts: []string{"github.com"}, FetchTimeout: time.Minute,
	}); err == nil {
		t.Fatal("symlink source cache was accepted")
	}
}
