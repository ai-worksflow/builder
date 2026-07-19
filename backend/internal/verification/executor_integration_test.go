package verification

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This integration test is opt-in because it needs a qualified Docker/Podman
// daemon, digest-addressable Node/PostgreSQL images, and a workspace path that
// is shared with that daemon. It exercises real containers; unit fakes remain
// responsible only for argument and failure-matrix coverage.
func TestDockerCandidateExecutorFullStackFixture(t *testing.T) {
	if os.Getenv("WORKSFLOW_TEST_VERIFICATION_DOCKER") != "1" {
		t.Skip("set WORKSFLOW_TEST_VERIFICATION_DOCKER=1 for the real container fixture")
	}
	daemon := os.Getenv("WORKSFLOW_TEST_VERIFICATION_DAEMON")
	root := os.Getenv("WORKSFLOW_TEST_VERIFICATION_ROOT")
	nodeImage := os.Getenv("WORKSFLOW_TEST_VERIFICATION_NODE_IMAGE")
	postgresImage := os.Getenv("WORKSFLOW_TEST_VERIFICATION_POSTGRES_IMAGE")
	if daemon == "" || root == "" || !imagePattern.MatchString(nodeImage) || !imagePattern.MatchString(postgresImage) {
		t.Fatal("real verification daemon, shared root, and digest-pinned Node/PostgreSQL images are required")
	}
	if os.Getuid() == 0 {
		t.Fatal("real verification fixture must run as a non-root worker")
	}
	runtimeUser := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	contents := newVerificationContentStoreFake()
	executor, err := NewDockerCandidateExecutor(DockerCandidateExecutorConfig{
		RuntimeBinary: "docker", DaemonHost: daemon, WorkspaceRoot: root,
		Memory: "512m", CPUs: "1.0", PIDs: 128, OutputLimit: 64 << 10,
		TempBytes: 256 << 20, User: runtimeUser,
	}, contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	attemptID, runID, planID, projectID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	fence := uint64(1)
	workspace := filepath.Join(root, attemptID, "1", "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Join(root, attemptID))
	migration := `#!/bin/sh
set -eu
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c 'CREATE TABLE IF NOT EXISTS verification_messages (id integer PRIMARY KEY, body text NOT NULL)'
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c "INSERT INTO verification_messages (id, body) VALUES (1, 'ready') ON CONFLICT (id) DO UPDATE SET body = EXCLUDED.body"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -tAc "SELECT body FROM verification_messages WHERE id = 1" | grep -qx ready
`
	if err := os.WriteFile(filepath.Join(workspace, "migrate.sh"), []byte(migration), 0o500); err != nil {
		t.Fatal(err)
	}
	policy := map[string]any{
		"postgres": map[string]any{
			"image": postgresImage, "database": "verification", "user": "verification", "runtimeUser": "70:70",
		},
		"services": []any{map[string]any{
			"id": "api", "image": nodeImage, "workingDirectory": ".",
			"argv":       []any{"node", "-e", `const http=require('http');http.createServer((req,res)=>{res.setHeader('content-type','application/json');res.end(JSON.stringify({status:req.url==='/health'?'ready':'contract-ok'}))}).listen(3000,'0.0.0.0')`},
			"healthArgv": []any{"node", "-e", `fetch('http://127.0.0.1:3000/health').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))`},
		}},
	}
	subject := CandidatePlanSubject{
		SessionID: uuid.NewString(), SessionVersion: 1,
		CandidateID: uuid.NewString(), CandidateSnapshotID: uuid.NewString(), CandidateVersion: 1,
		SessionEpoch: 1, WriterLeaseEpoch: 1, TreeStore: "content", TreeOwnerID: uuid.NewString(),
		TreeRef: "fixture-tree", TreeContentHash: verificationTestHash("container-tree-content"),
		TreeHash: verificationTestHash("container-tree"),
	}
	spec := CandidateExecutionSpec{
		RunID: runID, AttemptID: attemptID, AttemptFenceEpoch: fence,
		PlanID: planID, PlanHash: verificationTestHash("container-plan"),
		Content: PlanContent{
			SchemaVersion: PlanContentSchemaVersion, Scope: ScopeCandidate,
			ProjectID: projectID, Subject: subject,
			RuntimePolicy: PlanRuntimePolicy{Limits: map[string]any{}, NetworkPolicy: policy},
		},
	}
	if err := executor.Prepare(ctx, spec); err != nil {
		t.Fatal(err)
	}
	defer func() {
		collectCtx, collectCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer collectCancel()
		if err := executor.Collect(collectCtx, spec); err != nil {
			t.Errorf("collect full-stack fixture: %v", err)
		}
	}()

	runCheck := func(check PlanCheck) CheckExecutionOutcome {
		t.Helper()
		outcome, err := executor.Execute(ctx, CheckExecutionRequest{
			ProjectID: projectID, RunID: runID, AttemptID: attemptID,
			AttemptFenceEpoch: fence, AttemptCount: 1, Subject: subject,
			RuntimePolicy: spec.Content.RuntimePolicy, Check: check,
		})
		if err != nil || outcome.Status != CheckPassed || outcome.ExitCode == nil || *outcome.ExitCode != 0 {
			t.Fatalf("real check %s = %#v, %v", check.ID, outcome, err)
		}
		return outcome
	}
	migrationCheck := PlanCheck{
		ID: "migration-repeat", Kind: "migration", ServiceID: "api", Required: true,
		VerifierImageDigest: postgresImage, Argv: []string{"sh", "/workspace/migrate.sh"},
		WorkingDirectory: ".", TimeoutSeconds: 60,
	}
	runCheck(migrationCheck)
	runCheck(migrationCheck)
	contract := runCheck(PlanCheck{
		ID: "contract", Kind: "contract", ServiceID: "api", Required: true,
		VerifierImageDigest: nodeImage,
		Argv:                []string{"node", "-e", `fetch('http://api:3000/contract').then(async r=>{const v=await r.json();process.exit(r.ok&&v.status==='contract-ok'?0:1)}).catch(()=>process.exit(1))`},
		WorkingDirectory:    ".", TimeoutSeconds: 60,
	})
	stored, err := contents.Get(ctx, contract.Stdout.Ref, contract.Stdout.ContentHash)
	if err != nil || strings.Contains(string(stored.Payload), "verification:") {
		t.Fatalf("real contract log persistence = %s, %v", stored.Payload, err)
	}
}
