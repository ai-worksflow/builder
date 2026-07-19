package sandbox

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/templates"
	"github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sandboxStorePostgresFixture struct {
	context        context.Context
	database       *sql.DB
	store          *Store
	actorID        uuid.UUID
	projectID      uuid.UUID
	otherProjectID uuid.UUID
	candidateID    uuid.UUID
	webRelease     repository.ExactReference
	apiRelease     repository.ExactReference
	candidate      repository.CandidateWorkspace
}

type sandboxStoreCandidateProjection struct {
	Version          int64
	JournalSequence  int64
	SessionEpoch     int64
	WriterLeaseEpoch int64
	TreeStore        string
	TreeOwnerID      uuid.UUID
	TreeRef          string
	TreeContentHash  string
	TreeHash         string
	TreeFileCount    int
	TreeByteSize     int64
}

func TestSandboxStorePostgresCanary(t *testing.T) {
	fixture := openSandboxStorePostgresFixture(t)
	sessionID := uuid.New()
	input := fixture.sessionInput(sessionID)

	invalid := input
	invalid.ID = uuid.NewString()
	invalid.Services = append([]AllowedService(nil), input.Services...)
	invalid.Services[0].TemplateRelease = repository.ExactReference{
		ID: uuid.NewString(), ContentHash: sandboxStoreDigest("unselected-web-release"),
	}
	if _, err := fixture.store.Create(fixture.context, invalid, time.Now().UTC()); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("mismatched BuildContract role/release error = %v, want ErrInvalidSession", err)
	}

	created, err := fixture.store.Create(fixture.context, input, time.Now().UTC())
	if err != nil {
		t.Fatalf("create SandboxSession: %v", err)
	}
	createdView := created.Snapshot()
	assertSandboxStoreInitialProjection(t, fixture, createdView, sessionID)

	loaded, err := fixture.store.Get(fixture.context, fixture.projectID.String(), sessionID.String())
	if err != nil {
		t.Fatalf("get SandboxSession: %v", err)
	}
	if !reflect.DeepEqual(createdView, loaded.Snapshot()) {
		t.Fatalf("create/read projection differs:\ncreated=%#v\nloaded=%#v", createdView, loaded.Snapshot())
	}

	if _, err := fixture.store.Get(
		fixture.context, fixture.otherProjectID.String(), sessionID.String(),
	); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("cross-project Get error = %v, want ErrSessionNotFound", err)
	}
	if _, err := fixture.store.SyncCandidate(
		fixture.context, fixture.otherProjectID.String(), sessionID.String(), 1, 1, fixture.actorID.String(),
	); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("cross-project mutation error = %v, want ErrSessionNotFound", err)
	}

	view := transitionSandboxStoreSession(
		t, fixture, sessionID, 1, 1, StateStarting, "runner allocated", 2, 1,
	)
	if _, err := fixture.store.Transition(
		fixture.context, fixture.projectID.String(), sessionID.String(), 1, 1,
		StateReady, fixture.actorID.String(), "stale version", "",
	); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version error = %v, want ErrVersionConflict", err)
	}
	if _, err := fixture.store.Transition(
		fixture.context, fixture.projectID.String(), sessionID.String(), 2, 2,
		StateReady, fixture.actorID.String(), "stale epoch", "",
	); !errors.Is(err, ErrEpochFenced) {
		t.Fatalf("stale epoch error = %v, want ErrEpochFenced", err)
	}
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateReady, "runtime healthy", 3, 1,
	)

	dirtyCandidate := appendSandboxStoreCandidateEdit(t, fixture, "first-edit")
	synced, err := fixture.store.SyncCandidate(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		view.Version, view.SessionEpoch, fixture.actorID.String(),
	)
	if err != nil {
		t.Fatalf("sync dirty Candidate: %v", err)
	}
	view = synced.Snapshot()
	if view.Version != 4 || view.Candidate.Version != uint64(dirtyCandidate.Version) ||
		view.Candidate.JournalSequence != uint64(dirtyCandidate.JournalSequence) ||
		view.Candidate.TreeHash != dirtyCandidate.TreeHash || !view.Candidate.Dirty {
		t.Fatalf("dirty Candidate sync lost exact projection: %#v", view)
	}
	if _, err := fixture.store.Transition(
		fixture.context, fixture.projectID.String(), sessionID.String(), view.Version, view.SessionEpoch,
		StateSuspending, fixture.actorID.String(), "idle hibernate", "",
	); !errors.Is(err, ErrCheckpointRequired) {
		t.Fatalf("dirty suspend without checkpoint error = %v, want ErrCheckpointRequired", err)
	}

	checkpointV3 := insertSandboxStoreCheckpoint(t, fixture, "pre-suspend")
	attached, err := fixture.store.AttachCheckpoint(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		view.Version, view.SessionEpoch, fixture.actorID.String(), checkpointV3.String(),
	)
	if err != nil {
		t.Fatalf("attach exact pre-suspend checkpoint: %v", err)
	}
	view = attached.Snapshot()
	if view.Version != 5 || view.LatestCheckpoint == nil ||
		view.LatestCheckpoint.ID != checkpointV3.String() ||
		view.LatestCheckpoint.CandidateVersion != view.Candidate.Version ||
		view.LatestCheckpoint.JournalSequence != view.Candidate.JournalSequence ||
		view.LatestCheckpoint.SessionEpoch != view.Candidate.SessionEpoch ||
		view.LatestCheckpoint.WriterLeaseEpoch != view.Candidate.WriterLeaseEpoch ||
		view.LatestCheckpoint.TreeHash != view.Candidate.TreeHash {
		t.Fatalf("attached checkpoint is not exact: %#v", view)
	}

	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateSuspending, "idle hibernate", 6, 1,
	)
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateSuspended, "runtime suspended", 7, 1,
	)
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateResuming, "resume requested", 8, 2,
	)
	if view.Candidate.Version != 4 || view.Candidate.SessionEpoch != 2 ||
		view.Candidate.WriterLeaseEpoch != 2 || view.Candidate.TreeHash != dirtyCandidate.TreeHash ||
		!view.Candidate.Dirty {
		t.Fatalf("resume did not rotate only the Candidate fences: %#v", view.Candidate)
	}
	if _, err := fixture.store.Transition(
		fixture.context, fixture.projectID.String(), sessionID.String(), view.Version, view.SessionEpoch,
		StateReady, fixture.actorID.String(), "runtime healthy", "",
	); !errors.Is(err, ErrCheckpointRequired) {
		t.Fatalf("dirty resume without fresh exact checkpoint error = %v, want ErrCheckpointRequired", err)
	}

	acquireSandboxStoreCandidateLease(t, fixture, 4, 5, 2, 3)
	resumedCandidate := readSandboxStoreCandidate(t, fixture)
	synced, err = fixture.store.SyncCandidate(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		view.Version, view.SessionEpoch, fixture.actorID.String(),
	)
	if err != nil {
		t.Fatalf("sync post-resume Candidate fence: %v", err)
	}
	view = synced.Snapshot()
	if view.Version != 9 || view.State != StateResuming ||
		view.Candidate.Version != uint64(resumedCandidate.Version) ||
		view.Candidate.SessionEpoch != uint64(resumedCandidate.SessionEpoch) ||
		view.Candidate.WriterLeaseEpoch != uint64(resumedCandidate.WriterLeaseEpoch) {
		t.Fatalf("post-resume Candidate sync lost fences: %#v", view)
	}

	checkpointV5 := insertSandboxStoreCheckpoint(t, fixture, "post-resume")
	attached, err = fixture.store.AttachCheckpoint(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		view.Version, view.SessionEpoch, fixture.actorID.String(), checkpointV5.String(),
	)
	if err != nil {
		t.Fatalf("attach post-resume checkpoint: %v", err)
	}
	view = attached.Snapshot()
	if view.Version != 10 || view.LatestCheckpoint == nil ||
		view.LatestCheckpoint.ID != checkpointV5.String() ||
		view.LatestCheckpoint.CandidateVersion != 5 ||
		view.LatestCheckpoint.SessionEpoch != 2 ||
		view.LatestCheckpoint.WriterLeaseEpoch != 3 {
		t.Fatalf("post-resume checkpoint lost exact fences: %#v", view.LatestCheckpoint)
	}
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateReady, "resumed runtime healthy", 11, 2,
	)

	final, err := fixture.store.Get(fixture.context, fixture.projectID.String(), sessionID.String())
	if err != nil {
		t.Fatalf("read final SandboxSession: %v", err)
	}
	if !reflect.DeepEqual(view, final.Snapshot()) {
		t.Fatalf("final Get differs from transition result:\ntransition=%#v\nget=%#v", view, final.Snapshot())
	}
	assertSandboxStoreEventProjection(t, fixture, sessionID, view)
}

func TestSandboxStoreAbandonCandidateAtomicallyFencesAndCompletesPostgres(t *testing.T) {
	fixture := openSandboxStorePostgresFixture(t)
	sessionID := uuid.New()
	created, err := fixture.store.Create(
		fixture.context, fixture.sessionInput(sessionID), time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	view := created.Snapshot()
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateStarting, "runner allocated", 2, 1,
	)
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateReady, "runtime healthy", 3, 1,
	)
	dirty := appendSandboxStoreCandidateEdit(t, fixture, "abandon-dirty")
	synced, err := fixture.store.SyncCandidate(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		view.Version, view.SessionEpoch, fixture.actorID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	view = synced.Snapshot()
	if !view.Candidate.Dirty || view.Candidate.Version != uint64(dirty.Version) {
		t.Fatalf("dirty Candidate was not projected before abandon: %#v", view)
	}
	if _, err := fixture.store.AbandonCandidate(
		fixture.context, fixture.projectID.String(), sessionID.String(), fixture.candidateID.String(),
		view.Version, view.SessionEpoch, view.Candidate.Version, view.Candidate.WriterLeaseEpoch,
		fixture.actorID.String(), "", "discard dirty experiment",
	); !errors.Is(err, ErrCheckpointRequired) {
		t.Fatalf("dirty abandon without exact checkpoint error=%v, want ErrCheckpointRequired", err)
	}

	checkpointID := insertSandboxStoreCheckpoint(t, fixture, "abandon recovery point")
	attached, err := fixture.store.AttachCheckpoint(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		view.Version, view.SessionEpoch, fixture.actorID.String(), checkpointID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	view = attached.Snapshot()
	transitioning, err := fixture.store.AbandonCandidate(
		fixture.context, fixture.projectID.String(), sessionID.String(), fixture.candidateID.String(),
		view.Version, view.SessionEpoch, view.Candidate.Version, view.Candidate.WriterLeaseEpoch,
		fixture.actorID.String(), checkpointID.String(), "discard dirty experiment",
	)
	if err != nil {
		t.Fatalf("atomically abandon Candidate and fence Session: %v", err)
	}
	abandoned := transitioning.Snapshot()
	if abandoned.State != StateTerminating || abandoned.Candidate.Status != repository.CandidateAbandoned ||
		abandoned.Version != view.Version+1 || abandoned.Candidate.Version != view.Candidate.Version+1 ||
		abandoned.Candidate.WriterLeaseEpoch != view.Candidate.WriterLeaseEpoch+1 ||
		abandoned.Candidate.TreeHash != view.Candidate.TreeHash {
		t.Fatalf("abandonment did not preserve exact terminal projection: before=%#v after=%#v", view, abandoned)
	}
	lease, err := fixture.store.ClaimDueDeadline(
		fixture.context, "candidate-abandon-recovery", time.Minute,
	)
	if err != nil || lease == nil || lease.SessionID != sessionID.String() ||
		lease.ProjectID != fixture.projectID.String() || lease.Action != DeadlineAbandonCleanup {
		t.Fatalf("terminating abandoned Session was not durably claimable for cleanup: lease=%#v err=%v", lease, err)
	}

	terminated, err := fixture.store.CompleteCandidateAbandon(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		abandoned.Version, abandoned.SessionEpoch, SandboxLifecycleWorkerActorID,
	)
	if err != nil {
		t.Fatalf("complete abandoned runtime reconciliation: %v", err)
	}
	terminal := terminated.Snapshot()
	if terminal.State != StateTerminated || terminal.Candidate.Status != repository.CandidateAbandoned ||
		terminal.Version != abandoned.Version+1 || terminal.Candidate.Version != abandoned.Candidate.Version {
		t.Fatalf("abandon completion changed Candidate or missed terminal state: %#v", terminal)
	}
	if err := fixture.store.CompleteDeadline(fixture.context, *lease); err != nil {
		t.Fatalf("complete abandoned cleanup lease: %v", err)
	}

	var controlEvents, specialEvents int
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT
  (SELECT count(*) FROM candidate_workspace_control_events
   WHERE candidate_id = $1 AND event_kind = 'candidate.abandoned'),
  (SELECT count(*) FROM sandbox_session_transition_events
   WHERE session_id = $2 AND event_kind IN ('candidate.abandoned', 'candidate.abandon_completed'))
`, fixture.candidateID, sessionID).Scan(&controlEvents, &specialEvents); err != nil {
		t.Fatal(err)
	}
	if controlEvents != 1 || specialEvents != 2 {
		t.Fatalf("abandon saga was not append-only and exact: controls=%d sessionEvents=%d", controlEvents, specialEvents)
	}
}

func TestCandidateJournalLosesRaceToSandboxSuspendAtDatabaseBoundary(t *testing.T) {
	fixture := openSandboxStorePostgresFixture(t)
	sessionID := uuid.New()
	if _, err := fixture.store.Create(fixture.context, fixture.sessionInput(sessionID), time.Now().UTC()); err != nil {
		t.Fatalf("create SandboxSession: %v", err)
	}
	view := transitionSandboxStoreSession(
		t, fixture, sessionID, 1, 1, StateStarting, "runner allocated", 2, 1,
	)
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch, StateReady, "runtime healthy", 3, 1,
	)
	before := readSandboxStoreCandidate(t, fixture)

	// Hold the lifecycle transaction open after it has changed the Session to
	// suspending. The competing journal append must wait on the Session row,
	// then fail against the committed state instead of landing behind it.
	lifecycle, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Rollback()
	var nextVersion, nextEpoch, candidateVersion int64
	var nextState string
	if err := lifecycle.QueryRowContext(fixture.context, `
SELECT session_version, session_state, session_epoch, candidate_version
FROM transition_sandbox_session($1, $2, $3, 'suspending', $4, 'idle deadline elapsed', NULL)
`, sessionID, view.Version, view.SessionEpoch, fixture.actorID).Scan(
		&nextVersion, &nextState, &nextEpoch, &candidateVersion,
	); err != nil {
		t.Fatalf("begin transactional suspend: %v", err)
	}
	if nextVersion != 4 || nextState != "suspending" || nextEpoch != 1 || candidateVersion != before.Version {
		t.Fatalf("unexpected uncommitted lifecycle projection: version=%d state=%s epoch=%d candidate=%d",
			nextVersion, nextState, nextEpoch, candidateVersion)
	}

	appendResult := make(chan error, 1)
	go func() {
		appendResult <- execSandboxStoreCandidateEdit(
			fixture.context, fixture.database, fixture, before, "suspend-race",
		)
	}()
	select {
	case appendErr := <-appendResult:
		t.Fatalf("Candidate append did not wait for lifecycle row lock: %v", appendErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err := lifecycle.Commit(); err != nil {
		t.Fatalf("commit suspend decision: %v", err)
	}
	select {
	case appendErr := <-appendResult:
		var postgresError *pgconn.PgError
		if !errors.As(appendErr, &postgresError) || postgresError.Code != "40001" {
			t.Fatalf("Candidate append after suspend error = %v, want SQLSTATE 40001", appendErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Candidate append remained blocked after lifecycle commit")
	}
	after := readSandboxStoreCandidate(t, fixture)
	if after != before {
		t.Fatalf("fenced Candidate append changed durable state: before=%#v after=%#v", before, after)
	}
	loaded, err := fixture.store.Get(fixture.context, fixture.projectID.String(), sessionID.String())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot().State != StateSuspending || loaded.Snapshot().Candidate.Version != uint64(before.Version) {
		t.Fatalf("suspend did not retain the exact Candidate projection: %#v", loaded.Snapshot())
	}
}

func TestTerminalSandboxSessionDoesNotPoisonSuccessorCandidateWrites(t *testing.T) {
	for _, terminal := range []State{StateFailed, StateTerminated} {
		terminal := terminal
		t.Run(string(terminal), func(t *testing.T) {
			fixture := openSandboxStorePostgresFixture(t)
			oldSessionID := uuid.New()
			if _, err := fixture.store.Create(
				fixture.context, fixture.sessionInput(oldSessionID), time.Now().UTC(),
			); err != nil {
				t.Fatalf("create old SandboxSession: %v", err)
			}
			old := transitionSandboxStoreSession(
				t, fixture, oldSessionID, 1, 1, StateStarting, "runner allocated", 2, 1,
			)
			old = transitionSandboxStoreSession(
				t, fixture, oldSessionID, old.Version, old.SessionEpoch,
				StateReady, "runtime healthy", 3, 1,
			)
			old = transitionSandboxStoreSession(
				t, fixture, oldSessionID, old.Version, old.SessionEpoch,
				StateFailed, "runtime failed", 4, 1,
			)
			if terminal == StateTerminated {
				old = transitionSandboxStoreSession(
					t, fixture, oldSessionID, old.Version, old.SessionEpoch,
					StateTerminating, "clean failed runtime", 5, 1,
				)
				_ = transitionSandboxStoreSession(
					t, fixture, oldSessionID, old.Version, old.SessionEpoch,
					StateTerminated, "runtime terminated", 6, 1,
				)
			}

			successorSessionID := uuid.New()
			if _, err := fixture.store.Create(
				fixture.context, fixture.sessionInput(successorSessionID), time.Now().UTC(),
			); err != nil {
				t.Fatalf("create successor SandboxSession: %v", err)
			}
			successor := transitionSandboxStoreSession(
				t, fixture, successorSessionID, 1, 1, StateStarting, "runner allocated", 2, 1,
			)
			_ = transitionSandboxStoreSession(
				t, fixture, successorSessionID, successor.Version, successor.SessionEpoch,
				StateReady, "runtime healthy", 3, 1,
			)

			before := readSandboxStoreCandidate(t, fixture)
			if err := execSandboxStoreCandidateEdit(
				fixture.context, fixture.database, fixture, before, "successor-"+string(terminal),
			); err != nil {
				t.Fatalf("successor Candidate edit was poisoned by historical %s Session: %v", terminal, err)
			}
			after := readSandboxStoreCandidate(t, fixture)
			if after.Version != before.Version+1 || after.JournalSequence != before.JournalSequence+1 {
				t.Fatalf("successor Candidate edit did not commit exactly once: before=%#v after=%#v", before, after)
			}
		})
	}
}

func openSandboxStorePostgresFixture(t *testing.T) *sandboxStorePostgresFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	schema := "sandbox_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", sandboxStoreDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrations.Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up in temporary schema: %v", err)
	}
	gormDatabase, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(gormDatabase)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &sandboxStorePostgresFixture{
		context: ctx, database: database, store: store,
		actorID: uuid.New(), projectID: uuid.New(), otherProjectID: uuid.New(), candidateID: uuid.New(),
	}
	fixture.seed(t)
	return fixture
}

func (fixture *sandboxStorePostgresFixture) seed(t *testing.T) {
	t.Helper()
	createdAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	workspaceArtifactID, workspaceRevisionID := uuid.New(), uuid.New()
	workspaceHash := sandboxStoreDigest("workspace-" + uuid.NewString())
	manifestID := uuid.New()
	manifestHash := strings.TrimPrefix(sandboxStoreDigest("manifest-"+uuid.NewString()), "sha256:")
	contractID := uuid.New()
	contractHash := strings.TrimPrefix(sandboxStoreDigest("contract-"+uuid.NewString()), "sha256:")
	fullStackID := uuid.New()
	fullStackHash := sandboxStoreDigest("full-stack-" + uuid.NewString())
	snapshotID := uuid.New()
	webReleaseID, apiReleaseID := uuid.New(), uuid.New()
	templateReviewerID := uuid.New()
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES
  ($1, $2, 'Sandbox store actor', 'not-used'),
  ($3, $4, 'Sandbox store template reviewer', 'not-used')
`, fixture.actorID, "sandbox-store-"+uuid.NewString()+"@example.com",
		templateReviewerID, "sandbox-template-reviewer-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatalf("insert SandboxStore Template Writer identities: %v", err)
	}
	templateDatabase, err := gorm.Open(postgres.New(postgres.Config{Conn: fixture.database}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open SandboxStore Template Writer database: %v", err)
	}
	templateWriter, err := templates.NewWriter(templateDatabase, &sandboxStoreArtifactAuthority{})
	if err != nil {
		t.Fatalf("create SandboxStore Template Writer: %v", err)
	}
	admitRelease := func(id uuid.UUID, role string) repository.ExactReference {
		t.Helper()
		serviceID := role + "-core"
		if role == "web" {
			serviceID = "web-ui"
		}
		candidate := sandboxStoreTemplateCandidate(serviceID, role)
		registration, admitErr := templateWriter.Admit(fixture.context, templates.AdmitInput{
			AttemptID: uuid.NewString(), ReleaseID: id.String(), Candidate: candidate,
			Bundle:      sandboxStoreArtifactBundle(candidate),
			RequestedBy: fixture.actorID.String(), EvaluatedBy: templateReviewerID.String(),
		})
		if admitErr != nil {
			var registryErr *templates.RegistryError
			if errors.As(admitErr, &registryErr) && registryErr.Cause != nil {
				t.Fatalf("admit %s TemplateRelease through Artifact Authority: %v: %v", role, admitErr, registryErr.Cause)
			}
			t.Fatalf("admit %s TemplateRelease through Artifact Authority: %v", role, admitErr)
		}
		if registration.AuthorityReceipt == nil || registration.Release == nil {
			t.Fatalf("admit %s TemplateRelease returned incomplete authority lineage: %#v", role, registration)
		}
		view := registration.Release.Release.Snapshot()
		return repository.ExactReference{ID: view.ID, ContentHash: view.ContentHash}
	}
	fixture.webRelease = admitRelease(webReleaseID, "web")
	fixture.apiRelease = admitRelease(apiReleaseID, "api")

	baseTree, err := repository.NewTree([]repository.TreeFile{{
		Path: "README.md", Mode: "100644",
		ContentHash: sandboxStoreDigest("base-file-" + uuid.NewString()), ByteSize: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	baseTreeContentHash := sandboxStoreDigest("base-tree-object-" + uuid.NewString())
	baseTreeRef := "blob://sandbox-store/" + strings.TrimPrefix(baseTreeContentHash, "sha256:")

	transaction, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(fixture.context, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("disable prerequisite-only seed triggers: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Sandbox store canary', $3), ($2, 'Sandbox store other tenant', $3)
`, fixture.projectID, fixture.otherProjectID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO artifacts (id, project_id, kind, artifact_key, title, created_by)
VALUES ($1, $2, 'workspace', 'SANDBOX-STORE-WORKSPACE', 'Sandbox store workspace', $3)
`, workspaceArtifactID, fixture.projectID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO artifact_revisions (
  id, artifact_id, revision_number, schema_version, content_ref, content_hash,
  workflow_status, change_source, change_summary, created_by, created_at, approved_at
) VALUES ($1, $2, 1, 1, $3, $4, 'approved', 'system', 'sandbox store canary', $5, $6, $6)
`, workspaceRevisionID, workspaceArtifactID, "sandbox-store-workspace-"+workspaceRevisionID.String(),
		workspaceHash, fixture.actorID, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO application_build_manifests (
  id, project_id, root_manifest_id, workspace_revision_id, schema_version,
  content_ref, content_hash, manifest_hash, status, created_by, created_at
) VALUES ($1, $2, $1, $3, 1, $4, $5, $6, 'frozen', $7, $8)
`, manifestID, fixture.projectID, workspaceRevisionID,
		"sandbox-store-manifest-"+manifestID.String(), sandboxStoreDigest("manifest-content-"+uuid.NewString()),
		manifestHash, fixture.actorID, createdAt); err != nil {
		t.Fatal(err)
	}

	fullStackDocument, err := json.Marshal(map[string]any{
		"id": fullStackID.String(), "schemaVersion": "full-stack-template/v1",
		"templateId": "sandbox-store-stack", "version": "1.0.0", "contentHash": fullStackHash,
		"components": []any{
			map[string]any{"role": "web", "mountPath": "apps/web", "release": map[string]any{"id": fixture.webRelease.ID, "contentHash": fixture.webRelease.ContentHash}},
			map[string]any{"role": "api", "mountPath": "apps/api", "release": map[string]any{"id": fixture.apiRelease.ID, "contentHash": fixture.apiRelease.ContentHash}},
		},
		"layout":    map[string]any{"contractTruthSource": "openapi"},
		"createdBy": fixture.actorID.String(), "createdAt": createdAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO full_stack_template_releases (
  id, schema_version, template_id, release_version, document, content_hash, created_by, created_at
) VALUES ($1, 'full-stack-template/v1', 'sandbox-store-stack', '1.0.0', $2::jsonb, $3, $4, $5)
`, fullStackID, string(fullStackDocument), fullStackHash, fixture.actorID, createdAt); err != nil {
		t.Fatal(err)
	}
	for _, component := range []struct {
		role, mount string
		release     repository.ExactReference
	}{{"web", "apps/web", fixture.webRelease}, {"api", "apps/api", fixture.apiRelease}} {
		if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO full_stack_template_components (
  full_stack_template_id, full_stack_content_hash, role, mount_path,
  template_release_id, template_release_content_hash
) VALUES ($1, $2, $3, $4, $5, $6)
`, fullStackID, fullStackHash, component.role, component.mount,
			component.release.ID, component.release.ContentHash); err != nil {
			t.Fatalf("insert FullStackTemplate %s component: %v", component.role, err)
		}
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO application_build_contracts (
  id, project_id, build_manifest_id, build_manifest_hash,
  full_stack_template_id, full_stack_template_hash,
  schema_version, compiler_version, compiler_hash,
  content_ref, content_hash, contract_hash, status,
  must_count, must_ready_count, obligation_count, source_count, template_release_count,
  blocking_count, conflict_count, version, created_by, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6,
  'application-build-contract/v2', 'sandbox-store-canary', $7,
  $8, $9, $10, 'ready', 1, 1, 1, 1, 2, 0, 0, 1, $11, $12
)
`, contractID, fixture.projectID, manifestID, manifestHash, fullStackID, fullStackHash,
		sandboxStoreDigest("compiler-"+uuid.NewString()), "sandbox-store-contract-"+contractID.String(),
		sandboxStoreDigest("contract-content-"+uuid.NewString()), contractHash, fixture.actorID, createdAt); err != nil {
		t.Fatal(err)
	}
	for ordinal, selected := range []struct {
		role    string
		release repository.ExactReference
	}{{"api", fixture.apiRelease}, {"web", fixture.webRelease}} {
		if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES ($1, $2, $3, $4, $5)
`, contractID, ordinal, selected.role, selected.release.ID, selected.release.ContentHash); err != nil {
			t.Fatalf("insert BuildContract %s release: %v", selected.role, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit SandboxStore prerequisites: %v", err)
	}

	snapshotCreatedAt := createdAt.Add(time.Second)
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO repository_snapshots (
  id, schema_version, project_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, created_by, created_at
) VALUES (
  $1, 'repository-snapshot/v1', $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, 'blob', $1, $12, $13, $14, 1, 4, $15, $16
)
`, snapshotID, fixture.projectID, manifestID, manifestHash, contractID, contractHash,
		fullStackID, fullStackHash, workspaceArtifactID, workspaceRevisionID, workspaceHash,
		baseTreeRef, baseTreeContentHash, baseTree.TreeHash, fixture.actorID, snapshotCreatedAt); err != nil {
		t.Fatalf("insert RepositorySnapshot: %v", err)
	}
	candidateCreatedAt := snapshotCreatedAt.Add(time.Second)
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO candidate_workspaces (
  id, schema_version, project_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  base_tree_store, base_tree_owner_id, base_tree_ref, base_tree_content_hash, base_tree_hash,
  current_tree_store, current_tree_owner_id, current_tree_ref, current_tree_content_hash, current_tree_hash,
  current_tree_file_count, current_tree_byte_size,
  status, dirty, conflicted, stale, rebase_required,
  session_epoch, version, journal_sequence, writer_lease_epoch,
  created_by, created_at, updated_at
) VALUES (
  $1, 'candidate-workspace/v1', $2, $3, $4, $5, $6, $7, $8, $9,
  $10, $11, $12,
  'blob', $3, $13, $14, $15,
  'blob', $3, $13, $14, $15, 1, 4,
  'active', false, false, false, false, 1, 1, 0, 0, $16, $17, $17
)
`, fixture.candidateID, fixture.projectID, snapshotID, manifestID, manifestHash, contractID, contractHash,
		fullStackID, fullStackHash, workspaceArtifactID, workspaceRevisionID, workspaceHash,
		baseTreeRef, baseTreeContentHash, baseTree.TreeHash, fixture.actorID, candidateCreatedAt); err != nil {
		t.Fatalf("insert CandidateWorkspace: %v", err)
	}
	acquireSandboxStoreCandidateLease(t, fixture, 1, 2, 1, 1)

	var created, updated, leaseExpiresAt time.Time
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT created_at, updated_at, writer_lease_expires_at
FROM candidate_workspaces WHERE id = $1
`, fixture.candidateID).Scan(&created, &updated, &leaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	fixture.candidate = repository.CandidateWorkspace{
		SchemaVersion: repository.CandidateSchemaVersion,
		ID:            fixture.candidateID.String(), ProjectID: fixture.projectID.String(),
		RepositorySnapshotID: snapshotID.String(),
		BuildManifest:        repository.ExactReference{ID: manifestID.String(), ContentHash: manifestHash},
		BuildContract:        repository.ExactReference{ID: contractID.String(), ContentHash: contractHash},
		FullStackTemplate:    repository.ExactReference{ID: fullStackID.String(), ContentHash: fullStackHash},
		BaseWorkspaceRevision: &repository.ExactRevisionReference{
			ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionID.String(), ContentHash: workspaceHash,
		},
		BaseTreeHash: baseTree.TreeHash, CurrentTree: baseTree,
		Status:       repository.CandidateActive,
		SessionEpoch: 1, Version: 2, WriterLeaseEpoch: 1,
		Lease:     &repository.WriterLease{OwnerID: fixture.actorID.String(), Epoch: 1, ExpiresAt: leaseExpiresAt.UTC()},
		CreatedBy: fixture.actorID.String(), CreatedAt: created.UTC(), UpdatedAt: updated.UTC(),
	}
	if err := fixture.candidate.Validate(); err != nil {
		t.Fatalf("seeded Candidate domain projection is invalid: %v", err)
	}
}

func (fixture *sandboxStorePostgresFixture) sessionInput(id uuid.UUID) NewSessionInput {
	return NewSessionInput{
		ID: id.String(), ActorID: fixture.actorID.String(), Candidate: fixture.candidate,
		RunnerImageDigest: sandboxStoreDigest("runner-image"),
		Quota: Quota{
			CPUMillis: 2000, MemoryBytes: 2 << 30, WorkspaceBytes: 8 << 30,
			PIDLimit: 1024, PreviewPortLimit: 2,
		},
		TTL: TTLPolicy{IdleHibernateAfter: 15 * time.Minute, MaxRuntime: 2 * time.Hour},
		Services: []AllowedService{
			{ID: "web-ui", Kind: "web", Profiles: []string{"preview", "dev"}, TemplateRelease: fixture.webRelease},
			{ID: "api-core", Kind: "api", Profiles: []string{"test", "dev"}, TemplateRelease: fixture.apiRelease},
		},
		Ports: []AllowedPort{
			{Name: "web-http", ServiceID: "web-ui", Number: 3000, Protocol: "http"},
			{Name: "api-http", ServiceID: "api-core", Number: 8080, Protocol: "http"},
		},
	}
}

func assertSandboxStoreInitialProjection(
	t *testing.T,
	fixture *sandboxStorePostgresFixture,
	view SessionView,
	sessionID uuid.UUID,
) {
	t.Helper()
	if view.ID != sessionID.String() || view.ProjectID != fixture.projectID.String() ||
		view.ActorID != fixture.actorID.String() || view.State != StateProvisioning ||
		view.Version != 1 || view.SessionEpoch != 1 || view.Candidate.Version != 2 ||
		view.Candidate.WriterLeaseEpoch != 1 || view.Candidate.Dirty ||
		view.RunnerImageDigest != sandboxStoreDigest("runner-image") {
		t.Fatalf("created SandboxSession lost identity, state, or fence facts: %#v", view)
	}
	if view.Quota != (Quota{
		CPUMillis: 2000, MemoryBytes: 2 << 30, WorkspaceBytes: 8 << 30,
		PIDLimit: 1024, PreviewPortLimit: 2,
	}) || view.TTL.Policy != (TTLPolicy{IdleHibernateAfter: 15 * time.Minute, MaxRuntime: 2 * time.Hour}) ||
		!view.TTL.IdleDeadline.Equal(view.CreatedAt.Add(15*time.Minute)) ||
		!view.TTL.ExpiresAt.Equal(view.CreatedAt.Add(2*time.Hour)) {
		t.Fatalf("created SandboxSession lost immutable quota/TTL: %#v", view)
	}
	wantServices := []AllowedService{
		{ID: "api-core", Kind: "api", Profiles: []string{"dev", "test"}, TemplateRelease: fixture.apiRelease},
		{ID: "web-ui", Kind: "web", Profiles: []string{"dev", "preview"}, TemplateRelease: fixture.webRelease},
	}
	wantPorts := []AllowedPort{
		{Name: "api-http", ServiceID: "api-core", Number: 8080, Protocol: "http"},
		{Name: "web-http", ServiceID: "web-ui", Number: 3000, Protocol: "http"},
	}
	if !reflect.DeepEqual(view.AllowedServices, wantServices) || !reflect.DeepEqual(view.AllowedPorts, wantPorts) {
		t.Fatalf("configuration was not canonically hydrated: services=%#v ports=%#v", view.AllowedServices, view.AllowedPorts)
	}
	wantReleases := []repository.ExactReference{fixture.apiRelease, fixture.webRelease}
	if fixture.webRelease.ID < fixture.apiRelease.ID {
		wantReleases[0], wantReleases[1] = wantReleases[1], wantReleases[0]
	}
	if !reflect.DeepEqual(view.TemplateReleases, wantReleases) {
		t.Fatalf("TemplateRelease projection = %#v, want %#v", view.TemplateReleases, wantReleases)
	}
}

func transitionSandboxStoreSession(
	t *testing.T,
	fixture *sandboxStorePostgresFixture,
	sessionID uuid.UUID,
	expectedVersion, expectedEpoch uint64,
	target State,
	reason string,
	wantVersion, wantEpoch uint64,
) SessionView {
	t.Helper()
	next, err := fixture.store.Transition(
		fixture.context, fixture.projectID.String(), sessionID.String(),
		expectedVersion, expectedEpoch, target, fixture.actorID.String(), reason, "",
	)
	if err != nil {
		t.Fatalf("transition SandboxSession to %s: %v", target, err)
	}
	view := next.Snapshot()
	if view.State != target || view.Version != wantVersion || view.SessionEpoch != wantEpoch ||
		view.Candidate.SessionEpoch != wantEpoch {
		t.Fatalf("unexpected %s transition projection: %#v", target, view)
	}
	return view
}

func appendSandboxStoreCandidateEdit(
	t *testing.T,
	fixture *sandboxStorePostgresFixture,
	identity string,
) sandboxStoreCandidateProjection {
	t.Helper()
	before := readSandboxStoreCandidate(t, fixture)
	if err := execSandboxStoreCandidateEdit(fixture.context, fixture.database, fixture, before, identity); err != nil {
		t.Fatalf("append Candidate edit: %v", err)
	}
	return readSandboxStoreCandidate(t, fixture)
}

type sandboxStoreExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func execSandboxStoreCandidateEdit(
	ctx context.Context,
	executor sandboxStoreExecer,
	fixture *sandboxStorePostgresFixture,
	before sandboxStoreCandidateProjection,
	identity string,
) error {
	afterContentHash := sandboxStoreDigest("candidate-content-" + identity)
	afterTreeHash := sandboxStoreDigest("candidate-tree-" + identity)
	afterRef := "blob://sandbox-store/candidates/" + fixture.candidateID.String() + "/" + strings.TrimPrefix(afterContentHash, "sha256:")
	_, err := executor.ExecContext(ctx, `
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path, content_hash, byte_size, file_mode,
  before_tree_store, before_tree_owner_id, before_tree_ref, before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref, after_tree_content_hash, after_tree_hash,
  after_tree_file_count, after_tree_byte_size
) VALUES (
  $1, $2::bigint, $3::bigint, $3::bigint + 1, $4, $5, $6, 'user',
  $7, 'file.upsert', 'apps/web/page.tsx', $8, 256, '100644',
  $9, $10, $11, $12, $13,
  'blob', $1, $14, $15, $16, 2, 260
)
`, fixture.candidateID, before.JournalSequence+1, before.Version,
		before.SessionEpoch, before.WriterLeaseEpoch, fixture.actorID, "sandbox-store-"+identity,
		sandboxStoreDigest("candidate-file-"+identity),
		before.TreeStore, before.TreeOwnerID, before.TreeRef, before.TreeContentHash, before.TreeHash,
		afterRef, afterContentHash, afterTreeHash)
	return err
}

func readSandboxStoreCandidate(t *testing.T, fixture *sandboxStorePostgresFixture) sandboxStoreCandidateProjection {
	t.Helper()
	var candidate sandboxStoreCandidateProjection
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT version, journal_sequence, session_epoch, writer_lease_epoch,
       current_tree_store, current_tree_owner_id, current_tree_ref,
       current_tree_content_hash, current_tree_hash,
       current_tree_file_count, current_tree_byte_size
FROM candidate_workspaces
WHERE project_id = $1 AND id = $2
`, fixture.projectID, fixture.candidateID).Scan(
		&candidate.Version, &candidate.JournalSequence, &candidate.SessionEpoch, &candidate.WriterLeaseEpoch,
		&candidate.TreeStore, &candidate.TreeOwnerID, &candidate.TreeRef,
		&candidate.TreeContentHash, &candidate.TreeHash,
		&candidate.TreeFileCount, &candidate.TreeByteSize,
	); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func acquireSandboxStoreCandidateLease(
	t *testing.T,
	fixture *sandboxStorePostgresFixture,
	expectedVersion, wantVersion, wantSessionEpoch, wantWriterEpoch int64,
) {
	t.Helper()
	var version, sessionEpoch, writerEpoch int64
	var expiresAt time.Time
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT candidate_version, session_epoch, writer_lease_epoch, writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1, $2, $3, 300)
`, fixture.candidateID, expectedVersion, fixture.actorID).Scan(
		&version, &sessionEpoch, &writerEpoch, &expiresAt,
	); err != nil {
		t.Fatalf("acquire Candidate lease: %v", err)
	}
	if version != wantVersion || sessionEpoch != wantSessionEpoch || writerEpoch != wantWriterEpoch ||
		!expiresAt.After(time.Now()) {
		t.Fatalf("unexpected Candidate lease: version=%d session=%d writer=%d expires=%s",
			version, sessionEpoch, writerEpoch, expiresAt)
	}
}

func insertSandboxStoreCheckpoint(
	t *testing.T,
	fixture *sandboxStorePostgresFixture,
	reason string,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO candidate_snapshots (
  id, schema_version, candidate_id, project_id,
  candidate_version, journal_sequence, session_epoch, writer_lease_epoch,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, reason, created_by
)
SELECT $1, 'candidate-snapshot/v1', candidate.id, candidate.project_id,
       candidate.version, candidate.journal_sequence, candidate.session_epoch, candidate.writer_lease_epoch,
       candidate.current_tree_store, candidate.current_tree_owner_id,
       candidate.current_tree_ref, candidate.current_tree_content_hash, candidate.current_tree_hash,
       candidate.current_tree_file_count, candidate.current_tree_byte_size, $2, $3
FROM candidate_workspaces AS candidate
WHERE candidate.project_id = $4 AND candidate.id = $5
`, id, reason, fixture.actorID, fixture.projectID, fixture.candidateID); err != nil {
		t.Fatalf("insert exact CandidateSnapshot %s: %v", reason, err)
	}
	return id
}

func assertSandboxStoreEventProjection(
	t *testing.T,
	fixture *sandboxStorePostgresFixture,
	sessionID uuid.UUID,
	view SessionView,
) {
	t.Helper()
	var count, minimum, maximum, invalid int64
	var eventKinds string
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT count(*), min(sequence), max(sequence),
       count(*) FILTER (
         WHERE sequence <> session_version_from
            OR session_version_to <> session_version_from + 1
            OR candidate_session_epoch_from <> session_epoch_from
            OR candidate_session_epoch_to <> session_epoch_to
       ),
       string_agg(event_kind, ',' ORDER BY sequence)
FROM sandbox_session_transition_events
WHERE session_id = $1
`, sessionID).Scan(&count, &minimum, &maximum, &invalid, &eventKinds); err != nil {
		t.Fatal(err)
	}
	wantKinds := strings.Join([]string{
		"lifecycle.started", "lifecycle.ready", "candidate.synced", "checkpoint.attached",
		"lifecycle.suspend_requested", "lifecycle.suspended", "lifecycle.resume_requested",
		"candidate.synced", "checkpoint.attached", "lifecycle.ready",
	}, ",")
	if count != int64(view.Version-1) || minimum != 1 || maximum != count || invalid != 0 || eventKinds != wantKinds {
		t.Fatalf("invalid append-only event projection: count=%d min=%d max=%d invalid=%d kinds=%q version=%d",
			count, minimum, maximum, invalid, eventKinds, view.Version)
	}
	var brokenLinks int64
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT count(*)
FROM (
  SELECT sequence, session_version_from, state_from, session_epoch_from,
         candidate_version_from, candidate_journal_sequence_from,
         candidate_session_epoch_from, candidate_writer_lease_epoch_from,
         lag(session_version_to) OVER (ORDER BY sequence) AS previous_version,
         lag(state_to) OVER (ORDER BY sequence) AS previous_state,
         lag(session_epoch_to) OVER (ORDER BY sequence) AS previous_epoch,
         lag(candidate_version_to) OVER (ORDER BY sequence) AS previous_candidate_version,
         lag(candidate_journal_sequence_to) OVER (ORDER BY sequence) AS previous_journal,
         lag(candidate_session_epoch_to) OVER (ORDER BY sequence) AS previous_candidate_epoch,
         lag(candidate_writer_lease_epoch_to) OVER (ORDER BY sequence) AS previous_writer_epoch
  FROM sandbox_session_transition_events
  WHERE session_id = $1
) AS chain
WHERE sequence > 1
  AND ROW(
    session_version_from, state_from, session_epoch_from,
    candidate_version_from, candidate_journal_sequence_from,
    candidate_session_epoch_from, candidate_writer_lease_epoch_from
  ) IS DISTINCT FROM ROW(
    previous_version, previous_state, previous_epoch,
    previous_candidate_version, previous_journal,
    previous_candidate_epoch, previous_writer_epoch
  )
`, sessionID).Scan(&brokenLinks); err != nil {
		t.Fatal(err)
	}
	if brokenLinks != 0 {
		t.Fatalf("SandboxSession event chain has %d broken projection links", brokenLinks)
	}
}

func sandboxStoreDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type sandboxStoreArtifactAuthority struct{}

func (*sandboxStoreArtifactAuthority) Readiness(context.Context) error { return nil }

func (*sandboxStoreArtifactAuthority) Verify(
	_ context.Context,
	request templates.ArtifactAuthorityVerifyRequest,
) (templates.ArtifactAuthorityReceipt, error) {
	templateID := request.Candidate.Manifest.TemplateID
	verifiedAt := time.Now().UTC().Add(-time.Millisecond).Truncate(time.Microsecond)
	integratedAt := verifiedAt.Add(-time.Minute)
	artifactDigest := sandboxStoreDigest("authority-artifact-" + templateID)
	signatureDigest := sandboxStoreDigest("authority-signature-" + templateID)
	logEntry := "entry-" + strings.ReplaceAll(templateID, "-", ".")
	evidence := make([]templates.GateEvidence, 0, len(templates.RequiredAdmissionGates()))
	for _, gate := range templates.RequiredAdmissionGates() {
		evidence = append(evidence, templates.GateEvidence{
			Gate: gate, Outcome: templates.EvidencePassed, SubjectHash: request.SubjectHash,
			Digest:       sandboxStoreDigest("authority-evidence-" + templateID + "-" + string(gate)),
			Reference:    "urn:sandbox-store:evidence:" + templateID + ":" + string(gate),
			Producer:     "sandbox-store-artifact-authority/v1",
			InvocationID: "sandbox-store-" + templateID + "-" + string(gate), ObservedAt: verifiedAt,
		})
	}
	signer := "https://github.com/ai-worksflow/templates/.github/workflows/admit.yml@refs/heads/main"
	service := request.Bundle.ServiceSBOMs[0]
	return templates.NewArtifactAuthorityReceipt(templates.NewArtifactAuthorityReceiptInput{
		ID: uuid.NewString(), SubjectHash: request.SubjectHash,
		SourceTreeHash: request.Candidate.Source.TreeHash, ArtifactDigest: artifactDigest,
		SBOMDigest: request.Candidate.SBOMDigest, SignatureBundleDigest: signatureDigest,
		PolicyHash: sandboxStoreDigest("authority-policy-" + templateID),
		Authority: templates.ArtifactAuthorityIdentity{
			ID: "sandbox-store-artifact-authority.test", Version: "1.0.0-test",
		},
		VerifierImageDigest: sandboxStoreDigest("authority-verifier-" + templateID),
		TrustRootDigest:     sandboxStoreDigest("authority-trust-root-" + templateID),
		TransparencyLog: templates.ArtifactTransparencyLog{
			ID: "rekor.sandbox-store.test", EntryUUID: logEntry, LogIndex: 0, IntegratedAt: integratedAt,
		},
		VerificationReference: request.Bundle.VerificationReference,
		ArtifactDescriptor: templates.ArtifactDescriptor{
			Reference: request.Bundle.ArtifactReference,
			MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: artifactDigest, SizeBytes: 100,
			Config: templates.ArtifactBlobDescriptor{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Digest:    sandboxStoreDigest("authority-config-" + templateID), SizeBytes: 20,
			},
			Layers: []templates.ArtifactBlobDescriptor{{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    sandboxStoreDigest("authority-layer-" + templateID), SizeBytes: 30,
			}},
			TotalBytes: 150,
		},
		SBOMDescriptor: templates.ArtifactSBOMDescriptor{
			SchemaVersion: "worksflow.template-sbom-aggregate/v1",
			Digest:        request.Candidate.SBOMDigest, ServiceCount: 1,
			Services: []templates.ArtifactSBOMServiceDescriptor{{
				ServiceID: service.ServiceID, ImageReference: service.ImageReference,
				ImageDigest:       strings.TrimPrefix(service.ImageReference[strings.LastIndex(service.ImageReference, "@"):], "@"),
				ReferrerReference: service.ReferrerReference,
				ReferrerDigest:    strings.TrimPrefix(service.ReferrerReference[strings.LastIndex(service.ReferrerReference, "@"):], "@"),
				StatementDigest:   sandboxStoreDigest("authority-statement-" + templateID),
				PredicateDigest:   sandboxStoreDigest("authority-predicate-" + templateID), SPDXVersion: "SPDX-2.3",
				DocumentNamespace: "https://spdx.test/" + templateID,
				EvidenceHash:      sandboxStoreDigest("authority-sbom-evidence-" + templateID),
			}},
		},
		Proof: templates.ArtifactAuthorityProof{
			PayloadType: "application/vnd.in-toto+json", PredicateType: "https://slsa.dev/provenance/v1",
			PayloadDigest:         sandboxStoreDigest("authority-payload-" + templateID),
			SignatureBundleDigest: signatureDigest, SignerIdentities: []string{signer},
			TransparencyBundleDigest: sandboxStoreDigest("authority-transparency-" + templateID),
			LogID:                    "rekor.sandbox-store.test", EntryUUID: logEntry, LogIndex: 0, TreeSize: 2,
			RootHash: sandboxStoreDigest("authority-root-" + templateID), IntegratedAt: integratedAt,
			CheckpointSignedAt: verifiedAt,
		},
		Evidence: evidence,
		Signature: templates.SignatureEnvelope{
			Format: "dsse", SubjectHash: request.SubjectHash, BundleDigest: signatureDigest,
			Signer: signer, TransparencyLogRef: "urn:rekor:sandbox-store:" + logEntry, SignedAt: verifiedAt,
		},
		VerifiedAt: verifiedAt, RecordedBy: request.RecordedBy, CreatedAt: verifiedAt,
	})
}

func sandboxStoreTemplateCandidate(serviceID, kind string) templates.AdmissionCandidate {
	templateID := "sandbox-" + kind
	portName := kind + "-http"
	return templates.AdmissionCandidate{
		Source: templates.TemplateSource{
			Repository: "https://github.com/ai-worksflow/templates.git", Branch: templateID,
			Commit: strings.Repeat("a", 40), TreeHash: sandboxStoreDigest("template-tree-" + templateID),
		},
		Manifest: templates.TemplateManifest{
			SchemaVersion: templates.TemplateManifestSchemaVersion,
			TemplateID:    templateID, DisplayName: templateID, Version: "1.0.0",
			Services: []templates.TemplateService{{ID: serviceID, Kind: kind, RootPath: "."}},
			Toolchains: []templates.Toolchain{{
				Name: "runtime", Version: "22.0.0",
				Image: "ghcr.io/worksflow/runtime@" + sandboxStoreDigest("template-runtime-"+templateID),
			}},
			Commands: map[string]templates.Command{
				"dev":       {WorkingDirectory: ".", Argv: []string{"node", "server.js"}},
				"preview":   {WorkingDirectory: ".", Argv: []string{"node", "preview.js"}},
				"install":   {WorkingDirectory: ".", Argv: []string{"pnpm", "install", "--frozen-lockfile"}},
				"lint":      {WorkingDirectory: ".", Argv: []string{"pnpm", "lint"}},
				"typecheck": {WorkingDirectory: ".", Argv: []string{"pnpm", "typecheck"}},
				"test":      {WorkingDirectory: ".", Argv: []string{"pnpm", "test"}},
				"build":     {WorkingDirectory: ".", Argv: []string{"pnpm", "build"}},
				"start":     {WorkingDirectory: ".", Argv: []string{"pnpm", "start"}},
			},
			Ports: []templates.Port{{
				Name: portName, ServiceID: serviceID, Number: 3000, Protocol: "http", Exposure: "preview",
			}},
			HealthChecks: []templates.HealthCheck{{
				ID: kind + "-health", ServiceID: serviceID, PortName: portName, Path: "/health",
			}},
			BuildOutputs:   []templates.BuildOutput{{ServiceID: serviceID, Path: "output"}},
			ExtensionPaths: []string{"src"}, ProtectedPaths: []string{"templates.lock.json"},
			EnvironmentSchema: []templates.EnvironmentVariable{{
				Name: "PORT", Required: true, Description: "service port",
			}},
			Lockfiles: []templates.Lockfile{{
				Path: "pnpm-lock.yaml", Digest: sandboxStoreDigest("template-lock-" + templateID),
				Registry: "https://registry.npmjs.org",
			}},
			ProfileDigest: sandboxStoreDigest("template-profile-" + templateID),
		},
		SBOMDigest:        sandboxStoreDigest("template-sbom-" + templateID),
		LicenseExpression: "Apache-2.0", LicenseDigest: sandboxStoreDigest("template-license-" + templateID),
	}
}

func sandboxStoreArtifactBundle(candidate templates.AdmissionCandidate) templates.ArtifactAdmissionBundle {
	templateID := candidate.Manifest.TemplateID
	artifactDigest := sandboxStoreDigest("authority-artifact-" + templateID)
	imageDigest := sandboxStoreDigest("authority-service-image-" + templateID)
	referrerDigest := sandboxStoreDigest("authority-service-sbom-" + templateID)
	return templates.ArtifactAdmissionBundle{
		ArtifactReference: "ghcr.io/worksflow/templates/" + templateID + "@" + artifactDigest,
		ServiceSBOMs: []templates.ArtifactServiceSBOMReference{{
			ServiceID:         candidate.Manifest.Services[0].ID,
			ImageReference:    "ghcr.io/worksflow/templates/" + templateID + "-service@" + imageDigest,
			ReferrerReference: "ghcr.io/worksflow/templates/" + templateID + "-sbom@" + referrerDigest,
		}},
		DSSEEnvelope:          []byte(`{"payloadType":"application/vnd.in-toto+json","payload":"test","signatures":[]}`),
		TransparencyBundle:    []byte(`{"kind":"rekorInclusionProof","test":true}`),
		VerificationReference: "urn:sandbox-store:artifact-authority:" + templateID,
	}
}

func sandboxStoreDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if parsed, err := url.Parse(strings.TrimSpace(dsn)); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
