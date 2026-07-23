package qualificationinputauthority

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewPostgresStoreRequiresThreeIndependentAffinityVerifiedRoles(t *testing.T) {
	harness := newInputPostgresHarness(t)
	input := openInputPostgresTestDatabase(t, harness, postgresTestRoleInput)
	source := openInputPostgresTestDatabase(t, harness, postgresTestRoleSource)
	credential := openInputPostgresTestDatabase(t, harness, postgresTestRoleCredential)
	valid := PostgresStoreConfig{
		InputPrecommit:     PostgresRoleDatabase{Database: input, SessionAffinityMode: PostgresSessionAffinityDirect},
		SourceVerifier:     PostgresRoleDatabase{Database: source, SessionAffinityMode: PostgresSessionAffinitySessionPool},
		CredentialResolver: PostgresRoleDatabase{Database: credential, SessionAffinityMode: PostgresSessionAffinityDirect},
	}
	store, err := NewPostgresStore(valid)
	if err != nil || store.maxTransactionRetries != defaultPostgresTransactionRetries {
		t.Fatalf("NewPostgresStore(valid) = %#v, %v", store, err)
	}
	if _, resolver := any(store).(AuthorityResolver); resolver {
		t.Fatal("PostgresStore must not expose preflight upstream resolution")
	}
	for name, mutate := range map[string]func(*PostgresStoreConfig){
		"missing input":      func(config *PostgresStoreConfig) { config.InputPrecommit.Database = nil },
		"missing source":     func(config *PostgresStoreConfig) { config.SourceVerifier.Database = nil },
		"missing credential": func(config *PostgresStoreConfig) { config.CredentialResolver.Database = nil },
		"unverified input": func(config *PostgresStoreConfig) {
			config.InputPrecommit.SessionAffinityMode = PostgresSessionAffinityUnverified
		},
		"transaction pooled source": func(config *PostgresStoreConfig) {
			config.SourceVerifier.SessionAffinityMode = PostgresSessionAffinityTransactionPool
		},
		"transaction pooled credential": func(config *PostgresStoreConfig) {
			config.CredentialResolver.SessionAffinityMode = PostgresSessionAffinityTransactionPool
		},
		"shared input/source": func(config *PostgresStoreConfig) { config.SourceVerifier.Database = input },
		"shared input/credential": func(config *PostgresStoreConfig) {
			config.CredentialResolver.Database = input
		},
		"shared source/credential": func(config *PostgresStoreConfig) {
			config.CredentialResolver.Database = source
		},
		"negative retry": func(config *PostgresStoreConfig) { config.MaxTransactionRetries = -1 },
		"excessive retry": func(config *PostgresStoreConfig) {
			config.MaxTransactionRetries = maximumPostgresTransactionRetries + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if _, err := NewPostgresStore(config); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewPostgresStore() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestPostgresStoreRoutesClosedKindsAndStrictlyRoundTripsEveryCapability(t *testing.T) {
	harness := newInputPostgresHarness(t)
	store := newInputPostgresTestStore(t, harness, 1)
	ctx := context.Background()

	source, err := store.admitSourceReceipt(ctx, verifiedSourceGrant{
		proof: harness.record.Document.SourceProof, requestBytes: harness.record.SourceRequestBytes,
	})
	if err != nil || !sameReceiptAdmission(source, harness.sourceAdmission) {
		t.Fatalf("admitSourceReceipt() = %#v, %v", source, err)
	}
	credential, err := store.admitCredentialReceipt(ctx, verifiedCredentialGrant{
		proof: harness.record.Document.CredentialProof, requestBytes: harness.record.CredentialRequestBytes,
	})
	if err != nil || !sameReceiptAdmission(credential, harness.credentialAdmission) {
		t.Fatalf("admitCredentialReceipt() = %#v, %v", credential, err)
	}
	for name, test := range map[string]struct {
		kind string
		hash string
		want ReceiptAdmissionRecord
	}{
		"source request":     {ReceiptKindSource, source.Document.RequestHash, source},
		"credential request": {ReceiptKindCredential, credential.Document.RequestHash, credential},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := store.resolveReceiptAdmissionForRequest(ctx, test.kind, test.hash)
			if err != nil || !sameReceiptAdmission(got, test.want) {
				t.Fatalf("resolveReceiptAdmissionForRequest() = %#v, %v", got, err)
			}
			got, err = store.resolveReceiptAdmission(ctx, test.kind, test.want.AdmissionHash)
			if err != nil || !sameReceiptAdmission(got, test.want) {
				t.Fatalf("resolveReceiptAdmission() = %#v, %v", got, err)
			}
		})
	}

	issued, err := store.Issue(ctx, harness.record)
	if err != nil || issued.Idempotent || !sameImmutableRecord(issued, harness.record) {
		t.Fatalf("Issue() = %#v, %v", issued, err)
	}
	inspected, err := store.InspectOperation(ctx, harness.record.Command.OperationID)
	if err != nil || !sameImmutableRecord(inspected, harness.record) {
		t.Fatalf("InspectOperation() = %#v, %v", inspected, err)
	}
	resolved, err := store.ResolveAuthority(ctx, harness.record.Command.AuthorityID)
	if err != nil || !sameImmutableRecord(resolved, harness.record) {
		t.Fatalf("ResolveAuthority() = %#v, %v", resolved, err)
	}
	clock, err := NewPostgresClock(store.inputDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now, err := clock.Now(ctx)
	if err != nil || !now.Equal(testIssuedAt) {
		t.Fatalf("clock.Now() = %s, %v", now, err)
	}

	snapshot := harness.snapshot()
	if snapshot.roleWrites[postgresTestRoleSource] != 1 || snapshot.roleWrites[postgresTestRoleCredential] != 1 ||
		snapshot.roleWrites[postgresTestRoleInput] != 1 {
		t.Fatalf("write routing = %#v", snapshot.roleWrites)
	}
	if snapshot.begins != 3 || snapshot.commits != 3 || snapshot.rollbacks != 0 || snapshot.unlocks != 3 ||
		snapshot.isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Fatalf("write protocol = %#v", snapshot)
	}
	if snapshot.wrongRoleQueries != 0 {
		t.Fatalf("capability queries crossed role pools: %#v", snapshot)
	}
	if len(snapshot.lockIdentities) != 3 ||
		!strings.HasPrefix(snapshot.lockIdentities[0], postgresSourceAdmissionLockNamespace) ||
		!strings.HasPrefix(snapshot.lockIdentities[1], postgresCredentialAdmissionLockNamespace) ||
		!strings.HasPrefix(snapshot.lockIdentities[2], postgresInputPrecommitSessionLockNamespace) {
		t.Fatalf("session lock identities = %#v", snapshot.lockIdentities)
	}
}

func TestPostgresStoreMarksTransactionVisibleOperationReplayIdempotent(t *testing.T) {
	harness := newInputPostgresHarness(t)
	harness.priorOperationExists = true
	store := newInputPostgresTestStore(t, harness, 1)
	record, err := store.Issue(context.Background(), harness.record)
	if err != nil || !record.Idempotent || !sameImmutableRecord(record, harness.record) {
		t.Fatalf("Issue(replay) = %#v, %v", record, err)
	}
}

func TestPostgresStoreRetriesOnlyUnequivocalAbortsAndBoundsAttempts(t *testing.T) {
	t.Run("primary posture abort then success", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.primaryErrors = []error{&pgconn.PgError{Code: "40001", Message: "posture serialization abort"}}
		store := newInputPostgresTestStore(t, harness, 1)
		if _, err := store.Issue(context.Background(), harness.record); err != nil {
			t.Fatal(err)
		}
		if snapshot := harness.snapshot(); snapshot.begins != 2 || snapshot.rollbacks != 1 ||
			snapshot.commits != 1 || snapshot.unlocks != 2 {
			t.Fatalf("primary-check retry accounting = %#v", snapshot)
		}
	})

	t.Run("query abort then success", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.writeErrors = []error{&pgconn.PgError{Code: "40001", Message: "serialization abort"}}
		store := newInputPostgresTestStore(t, harness, 1)
		if _, err := store.Issue(context.Background(), harness.record); err != nil {
			t.Fatal(err)
		}
		snapshot := harness.snapshot()
		if snapshot.begins != 2 || snapshot.rollbacks != 1 || snapshot.commits != 1 || snapshot.unlocks != 2 {
			t.Fatalf("retry accounting = %#v", snapshot)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.writeErrors = []error{
			&pgconn.PgError{Code: "40001", Message: "serialization abort"},
			&pgconn.PgError{Code: "40P01", Message: "deadlock abort"},
		}
		store := newInputPostgresTestStore(t, harness, 1)
		if _, err := store.Issue(context.Background(), harness.record); !errors.Is(err, ErrRetryable) ||
			errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("Issue() error = %v, want only ErrRetryable", err)
		}
		if snapshot := harness.snapshot(); snapshot.begins != 2 || snapshot.unlocks != 2 {
			t.Fatalf("bounded retry accounting = %#v", snapshot)
		}
	})

	for name, attemptError := range map[string]error{
		"transport": errors.New("connection disappeared"),
		"joined abort and transport": errors.Join(
			&pgconn.PgError{Code: "40001", Message: "serialization abort"}, errors.New("transport"),
		),
	} {
		t.Run(name, func(t *testing.T) {
			harness := newInputPostgresHarness(t)
			harness.writeErrors = []error{attemptError}
			store := newInputPostgresTestStore(t, harness, 3)
			if _, err := store.Issue(context.Background(), harness.record); !errors.Is(err, ErrOutcomeUnknown) ||
				errors.Is(err, ErrRetryable) {
				t.Fatalf("Issue() error = %v", err)
			}
			if snapshot := harness.snapshot(); snapshot.begins != 1 || snapshot.unlocks != 1 {
				t.Fatalf("ambiguous attempt was retried: %#v", snapshot)
			}
		})
	}

	t.Run("primary posture transport error is classified", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.primaryErrors = []error{errors.New("postgres endpoint and credential detail")}
		store := newInputPostgresTestStore(t, harness, 3)
		_, err := store.Issue(context.Background(), harness.record)
		if !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "endpoint") {
			t.Fatalf("Issue() error = %v, want sanitized ErrOutcomeUnknown", err)
		}
		if snapshot := harness.snapshot(); snapshot.begins != 1 || snapshot.rollbacks != 1 || snapshot.unlocks != 1 {
			t.Fatalf("ambiguous primary check was retried: %#v", snapshot)
		}
	})
}

func TestPostgresStoreNeverRetriesUnknownCommitAndPoisonsUnknownLockState(t *testing.T) {
	t.Run("commit unknown", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.commitErrors = []error{errors.New("commit acknowledgement lost")}
		store := newInputPostgresTestStore(t, harness, 3)
		if _, err := store.Issue(context.Background(), harness.record); !errors.Is(err, ErrStoreOutcomeUnknown) ||
			errors.Is(err, ErrRetryable) {
			t.Fatalf("Issue() error = %v", err)
		}
		if snapshot := harness.snapshot(); snapshot.begins != 1 || snapshot.commits != 1 ||
			snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("unknown commit was retried, unlocked, or pooled: %#v", snapshot)
		}
	})

	t.Run("known aborted commit retries on a cleanly released session", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.commitErrors = []error{&pgconn.PgError{Code: "40001", Message: "commit serialization abort"}}
		store := newInputPostgresTestStore(t, harness, 1)
		if _, err := store.Issue(context.Background(), harness.record); err != nil {
			t.Fatal(err)
		}
		if snapshot := harness.snapshot(); snapshot.begins != 2 || snapshot.commits != 2 ||
			snapshot.unlocks != 2 || snapshot.closes != 0 {
			t.Fatalf("definitely aborted commit did not use the bounded retry path: %#v", snapshot)
		}
	})

	t.Run("begin result lost", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.beginErrors = []error{errors.New("begin acknowledgement lost")}
		store := newInputPostgresTestStore(t, harness, 3)
		if _, err := store.Issue(context.Background(), harness.record); !errors.Is(err, ErrOutcomeUnknown) ||
			errors.Is(err, ErrRetryable) {
			t.Fatalf("Issue() error = %v", err)
		}
		if snapshot := harness.snapshot(); snapshot.begins != 1 || snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("unknown transaction state was unlocked or pooled: %#v", snapshot)
		}
	})

	t.Run("rollback result lost", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.writeErrors = []error{&pgconn.PgError{Code: "WIP02", Message: "immutable conflict"}}
		harness.rollbackErrors = []error{errors.New("rollback acknowledgement lost")}
		store := newInputPostgresTestStore(t, harness, 3)
		if _, err := store.Issue(context.Background(), harness.record); !errors.Is(err, ErrStoreOutcomeUnknown) {
			t.Fatalf("Issue() error = %v, want ErrStoreOutcomeUnknown", err)
		}
		if snapshot := harness.snapshot(); snapshot.begins != 1 || snapshot.rollbacks != 1 ||
			snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("unknown rollback state was unlocked or pooled: %#v", snapshot)
		}
	})

	for name, mutate := range map[string]func(*inputPostgresHarness){
		"acquire result lost": func(harness *inputPostgresHarness) {
			harness.acquireErrors = []error{errors.New("lock result lost")}
		},
		"unlock result lost": func(harness *inputPostgresHarness) {
			harness.unlockErrors = []error{errors.New("unlock result lost")}
		},
		"unlock false": func(harness *inputPostgresHarness) { harness.unlockResults = []bool{false} },
	} {
		t.Run(name, func(t *testing.T) {
			harness := newInputPostgresHarness(t)
			mutate(harness)
			store := newInputPostgresTestStore(t, harness, 3)
			_, err := store.Issue(context.Background(), harness.record)
			if name == "acquire result lost" {
				if !errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("Issue() error = %v", err)
				}
			} else if !errors.Is(err, ErrStoreOutcomeUnknown) {
				t.Fatalf("Issue() error = %v", err)
			}
			snapshot := harness.snapshot()
			if snapshot.closes == 0 || snapshot.acquires != 1 || snapshot.unlocks > 1 || snapshot.begins > 1 {
				t.Fatalf("unknown lock state was pooled or retried: %#v", snapshot)
			}
		})
	}
}

func TestPostgresStoreFailsClosedOnReplicaMissingAndCorruptRows(t *testing.T) {
	t.Run("replica reads and writes", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.primaryReadWrite = false
		store := newInputPostgresTestStore(t, harness, 1)
		if _, err := store.Issue(context.Background(), harness.record); !errors.Is(err, ErrNotReady) {
			t.Fatalf("Issue() error = %v", err)
		}
		if _, err := store.InspectOperation(context.Background(), harness.record.Command.OperationID); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("InspectOperation() error = %v", err)
		}
		clock, _ := NewPostgresClock(store.inputDatabase)
		if _, err := clock.Now(context.Background()); !errors.Is(err, ErrNotReady) {
			t.Fatalf("clock.Now() error = %v", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.readMissing = true
		store := newInputPostgresTestStore(t, harness, 1)
		if _, err := store.InspectOperation(context.Background(), harness.record.Command.OperationID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("InspectOperation() error = %v", err)
		}
		if _, err := store.resolveReceiptAdmissionForRequest(
			context.Background(), ReceiptKindSource, harness.sourceAdmission.Document.RequestHash,
		); !errors.Is(err, ErrNotFound) {
			t.Fatalf("resolveReceiptAdmissionForRequest() error = %v", err)
		}
	})

	for name, corrupt := range map[string]func(*inputPostgresHarness){
		"retained canonical bytes": func(harness *inputPostgresHarness) {
			harness.authorityValues[3] = []byte(`{"schemaVersion":"wrong"}`)
		},
		"retained source request bytes": func(harness *inputPostgresHarness) {
			harness.authorityValues[6] = []byte(`{"schemaVersion":"wrong"}`)
		},
		"retained credential request bytes": func(harness *inputPostgresHarness) {
			harness.authorityValues[9] = []byte(`{"schemaVersion":"wrong"}`)
		},
		"retained authority bytes": func(harness *inputPostgresHarness) {
			harness.authorityValues[12] = []byte(`{"schemaVersion":"wrong"}`)
		},
		"JSONB projection": func(harness *inputPostgresHarness) {
			harness.authorityValues[4] = []byte(`{"unexpected":true}`)
		},
		"component JSONB projection": func(harness *inputPostgresHarness) {
			harness.authorityValues[7] = []byte(`{"unexpected":true}`)
		},
		"nested JSONB projection": func(harness *inputPostgresHarness) {
			harness.authorityValues[21] = []byte(`{"unexpected":true}`)
		},
		"scalar projection": func(harness *inputPostgresHarness) {
			harness.authorityValues[15] = testDigest("drifted-input-hash")
		},
		"proof scalar projection": func(harness *inputPostgresHarness) {
			harness.authorityValues[30] = testDigest("drifted-source-receipt")
		},
		"noncanonical UUID scalar": func(harness *inputPostgresHarness) {
			harness.authorityValues[0] = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		},
		"missing issued-at scalar": func(harness *inputPostgresHarness) {
			harness.authorityValues[36] = nil
		},
		"sub-millisecond issued-at scalar": func(harness *inputPostgresHarness) {
			harness.authorityValues[36] = testIssuedAt.Add(time.Microsecond)
		},
		"admission retained request bytes": func(harness *inputPostgresHarness) {
			harness.sourceValues[1] = []byte(`{"schemaVersion":"wrong"}`)
		},
		"admission request JSONB projection": func(harness *inputPostgresHarness) {
			harness.sourceValues[2] = []byte(`{"unexpected":true}`)
		},
		"admission retained document bytes": func(harness *inputPostgresHarness) {
			harness.sourceValues[4] = []byte(`{"schemaVersion":"wrong"}`)
		},
		"admission JSONB projection": func(harness *inputPostgresHarness) {
			harness.sourceValues[5] = []byte(`{"schemaVersion":"wrong"}`)
		},
		"admission scalar projection": func(harness *inputPostgresHarness) {
			harness.sourceValues[8] = testDigest("drifted-source-receipt")
		},
		"admission missing time": func(harness *inputPostgresHarness) {
			harness.sourceValues[9] = nil
		},
		"admission sub-millisecond time": func(harness *inputPostgresHarness) {
			harness.sourceValues[9] = testIssuedAt.Add(time.Microsecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newInputPostgresHarness(t)
			corrupt(harness)
			store := newInputPostgresTestStore(t, harness, 1)
			if strings.Contains(name, "admission") {
				_, err := store.resolveReceiptAdmissionForRequest(
					context.Background(), ReceiptKindSource, harness.sourceAdmission.Document.RequestHash,
				)
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("corrupt admission error = %v", err)
				}
			} else if _, err := store.InspectOperation(
				context.Background(), harness.record.Command.OperationID,
			); !errors.Is(err, ErrConflict) {
				t.Fatalf("corrupt authority error = %v", err)
			}
		})
	}

	t.Run("credential admission uses its own strict decoder", func(t *testing.T) {
		harness := newInputPostgresHarness(t)
		harness.credentialValues[2] = []byte(`{"schemaVersion":"wrong"}`)
		store := newInputPostgresTestStore(t, harness, 1)
		_, err := store.resolveReceiptAdmissionForRequest(
			context.Background(), ReceiptKindCredential, harness.credentialAdmission.Document.RequestHash,
		)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("corrupt credential admission error = %v", err)
		}
	})
}

func TestPostgresErrorClassificationAndKindAwareRecovery(t *testing.T) {
	for code, want := range map[string]error{
		"WIP01": ErrInvalid,
		"WIP02": ErrConflict,
		"WIP03": ErrStale,
		"23505": ErrConflict,
	} {
		if got := classifyPostgresWriteError(&pgconn.PgError{Code: code}); !errors.Is(got, want) {
			t.Errorf("SQLSTATE %s classified %v, want %v", code, got, want)
		}
	}
	serialization := &pgconn.PgError{Code: "40001"}
	if !isDefinitePostgresRetryable(fmt.Errorf("query: %w", serialization)) ||
		isDefinitePostgresRetryable(errors.Join(serialization, errors.New("transport"))) {
		t.Fatal("definite retry classification is not conservative")
	}
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"no row":              {sql.ErrNoRows, ErrNotFound},
		"exactness violation": {&pgconn.PgError{Code: "WIP02"}, ErrConflict},
		"check violation":     {&pgconn.PgError{Code: "23514"}, ErrConflict},
		"permission":          {&pgconn.PgError{Code: "42501"}, ErrOutcomeUnknown},
		"transport":           {errors.New("connection disappeared"), ErrOutcomeUnknown},
		"canceled":            {context.Canceled, context.Canceled},
		"deadline":            {context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		t.Run("inspect "+name, func(t *testing.T) {
			if got := classifyPostgresInspectError(test.err); !errors.Is(got, test.want) {
				t.Fatalf("classifyPostgresInspectError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}

	harness := newInputPostgresHarness(t)
	store := newInputPostgresTestStore(t, harness, 1)
	if _, err := store.resolveReceiptAdmission(
		context.Background(), ReceiptKindCredential, harness.sourceAdmission.AdmissionHash,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-kind admission recovery error = %v, want ErrNotFound", err)
	}
	snapshot := harness.snapshot()
	if snapshot.roleReads[postgresTestRoleSource] != 0 || snapshot.roleReads[postgresTestRoleCredential] != 1 {
		t.Fatalf("kind-aware recovery guessed across DSNs: %#v", snapshot.roleReads)
	}
}

func TestServicePreservesPostgresRetryableAdmissionAndIssue(t *testing.T) {
	for name, store := range map[string]Store{
		"issue":     &inputRetryStore{MemoryStore: NewMemoryStore(), issue: true},
		"admission": &inputRetryStore{MemoryStore: NewMemoryStore(), admission: true},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newServiceHarness(t)
			retrying := store.(*inputRetryStore)
			if err := retrying.InstallAuthorities(harness.resolved); err != nil {
				t.Fatal(err)
			}
			harness.service.store = retrying
			if _, err := harness.service.Issue(context.Background(), harness.command); !errors.Is(err, ErrRetryable) ||
				errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("Issue() error = %v, want only ErrRetryable", err)
			}
		})
	}
}

type inputRetryStore struct {
	*MemoryStore
	admission bool
	issue     bool
}

func (store *inputRetryStore) admitSourceReceipt(
	ctx context.Context,
	grant verifiedSourceGrant,
) (ReceiptAdmissionRecord, error) {
	if store.admission {
		return ReceiptAdmissionRecord{}, ErrRetryable
	}
	return store.MemoryStore.admitSourceReceipt(ctx, grant)
}

func (store *inputRetryStore) Issue(ctx context.Context, record Record) (Record, error) {
	if store.issue {
		return Record{}, ErrRetryable
	}
	return store.MemoryStore.Issue(ctx, record)
}

const (
	postgresTestRoleInput      = "input"
	postgresTestRoleSource     = "source"
	postgresTestRoleCredential = "credential"
)

type inputPostgresHarness struct {
	mu sync.Mutex

	record              Record
	sourceAdmission     ReceiptAdmissionRecord
	credentialAdmission ReceiptAdmissionRecord
	authorityValues     []driver.Value
	sourceValues        []driver.Value
	credentialValues    []driver.Value

	primaryReadWrite     bool
	priorOperationExists bool
	readMissing          bool

	acquireErrors  []error
	beginErrors    []error
	primaryErrors  []error
	writeErrors    []error
	commitErrors   []error
	rollbackErrors []error
	unlockErrors   []error
	unlockResults  []bool

	roleWrites       map[string]int
	roleReads        map[string]int
	lockIdentities   []string
	wrongRoleQueries int
	acquires         int
	begins           int
	commits          int
	rollbacks        int
	unlocks          int
	closes           int
	isolation        driver.IsolationLevel
}

type inputPostgresSnapshot struct {
	roleWrites       map[string]int
	roleReads        map[string]int
	lockIdentities   []string
	wrongRoleQueries int
	acquires         int
	begins           int
	commits          int
	rollbacks        int
	unlocks          int
	closes           int
	isolation        driver.IsolationLevel
}

func newInputPostgresHarness(t *testing.T) *inputPostgresHarness {
	t.Helper()
	service := newServiceHarness(t)
	record, err := service.service.Issue(context.Background(), service.command)
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.store.resolveReceiptAdmission(
		context.Background(), ReceiptKindSource, record.Document.SourceProof.AdmissionHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := service.store.resolveReceiptAdmission(
		context.Background(), ReceiptKindCredential, record.Document.CredentialProof.AdmissionHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	return &inputPostgresHarness{
		record:              record,
		sourceAdmission:     source,
		credentialAdmission: credential,
		authorityValues:     inputAuthorityDriverValues(record),
		sourceValues:        inputAdmissionDriverValues(source),
		credentialValues:    inputAdmissionDriverValues(credential),
		primaryReadWrite:    true,
		roleWrites:          make(map[string]int),
		roleReads:           make(map[string]int),
	}
}

func inputAuthorityDriverValues(record Record) []driver.Value {
	return []driver.Value{
		record.Command.AuthorityID.String(), record.Command.OperationID.String(),
		record.RequestHash, record.RequestBytes, record.RequestBytes,
		record.SourceRequestHash, record.SourceRequestBytes, record.SourceRequestBytes,
		record.CredentialRequestHash, record.CredentialRequestBytes, record.CredentialRequestBytes,
		record.AuthorityHash, record.DocumentBytes, record.DocumentBytes,
		record.Document.WorkflowInput.AuthorityID, record.Document.WorkflowInput.AuthorityHash,
		record.Document.WorkflowInput.InputHash, record.Document.Policy.AuthorityID,
		record.Document.Policy.AuthorityHash, record.Document.Policy.PlanInputProfileHash,
		record.Document.Policy.SourcePolicyDigest, mustCanonicalInputPostgres(record.Document.Policy.CredentialProfile),
		record.Document.Plan.AuthorityID, record.Document.Plan.AuthorityHash,
		record.Document.Plan.InputAuthorityID, record.Document.Plan.InputHash,
		mustCanonicalInputPostgres(record.Document.Plan.Source),
		mustCanonicalInputPostgres(record.Document.Plan.CredentialSet),
		record.Document.SourceProof.AuthorityID, record.Document.SourceProof.ExecutableDigest,
		record.Document.SourceProof.ReceiptHash, record.Document.SourceProof.AdmissionHash,
		record.Document.CredentialProof.AuthorityID, record.Document.CredentialProof.ExecutableDigest,
		record.Document.CredentialProof.ReceiptHash, record.Document.CredentialProof.AdmissionHash,
		record.IssuedAt,
	}
}

func inputAdmissionDriverValues(record ReceiptAdmissionRecord) []driver.Value {
	return []driver.Value{
		record.Document.RequestHash, record.RequestBytes, record.RequestBytes,
		record.AdmissionHash, record.DocumentBytes, record.DocumentBytes,
		record.Document.AuthorityID, record.Document.ExecutableDigest, record.Document.ReceiptHash,
		testIssuedAt,
	}
}

func mustCanonicalInputPostgres(value any) []byte {
	encoded, err := CanonicalJSON(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func (harness *inputPostgresHarness) snapshot() inputPostgresSnapshot {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	roleWrites := make(map[string]int, len(harness.roleWrites))
	for role, count := range harness.roleWrites {
		roleWrites[role] = count
	}
	roleReads := make(map[string]int, len(harness.roleReads))
	for role, count := range harness.roleReads {
		roleReads[role] = count
	}
	return inputPostgresSnapshot{
		roleWrites:       roleWrites,
		roleReads:        roleReads,
		lockIdentities:   append([]string(nil), harness.lockIdentities...),
		wrongRoleQueries: harness.wrongRoleQueries,
		acquires:         harness.acquires, begins: harness.begins, commits: harness.commits,
		rollbacks: harness.rollbacks, unlocks: harness.unlocks, closes: harness.closes,
		isolation: harness.isolation,
	}
}

func newInputPostgresTestStore(t *testing.T, harness *inputPostgresHarness, retries int) *PostgresStore {
	t.Helper()
	store, err := NewPostgresStore(PostgresStoreConfig{
		InputPrecommit: PostgresRoleDatabase{
			Database:            openInputPostgresTestDatabase(t, harness, postgresTestRoleInput),
			SessionAffinityMode: PostgresSessionAffinityDirect,
		},
		SourceVerifier: PostgresRoleDatabase{
			Database:            openInputPostgresTestDatabase(t, harness, postgresTestRoleSource),
			SessionAffinityMode: PostgresSessionAffinityDirect,
		},
		CredentialResolver: PostgresRoleDatabase{
			Database:            openInputPostgresTestDatabase(t, harness, postgresTestRoleCredential),
			SessionAffinityMode: PostgresSessionAffinityDirect,
		},
		MaxTransactionRetries: retries,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func shiftInputPostgresError(values *[]error) error {
	if len(*values) == 0 {
		return nil
	}
	value := (*values)[0]
	*values = (*values)[1:]
	return value
}

var (
	inputPostgresDriverOnce  sync.Once
	inputPostgresConnections sync.Map
	inputPostgresSequence    atomic.Uint64
)

type inputPostgresConnectionFixture struct {
	harness *inputPostgresHarness
	role    string
}

func openInputPostgresTestDatabase(t *testing.T, harness *inputPostgresHarness, role string) *sql.DB {
	t.Helper()
	inputPostgresDriverOnce.Do(func() { sql.Register("qualification-input-precommit-test", inputPostgresDriver{}) })
	name := fmt.Sprintf("harness-%d-%s", inputPostgresSequence.Add(1), role)
	inputPostgresConnections.Store(name, inputPostgresConnectionFixture{harness: harness, role: role})
	database, err := sql.Open("qualification-input-precommit-test", name)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() {
		_ = database.Close()
		inputPostgresConnections.Delete(name)
	})
	return database
}

type inputPostgresDriver struct{}

func (inputPostgresDriver) Open(name string) (driver.Conn, error) {
	value, found := inputPostgresConnections.Load(name)
	if !found {
		return nil, errors.New("unknown Qualification Input PostgreSQL harness")
	}
	fixture := value.(inputPostgresConnectionFixture)
	return &inputPostgresConnection{harness: fixture.harness, role: fixture.role}, nil
}

type inputPostgresConnection struct {
	harness *inputPostgresHarness
	role    string
}

func (*inputPostgresConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}

func (connection *inputPostgresConnection) Close() error {
	connection.harness.mu.Lock()
	connection.harness.closes++
	connection.harness.mu.Unlock()
	return nil
}

func (connection *inputPostgresConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *inputPostgresConnection) BeginTx(
	_ context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.begins++
	connection.harness.isolation = options.Isolation
	if err := shiftInputPostgresError(&connection.harness.beginErrors); err != nil {
		return nil, err
	}
	return &inputPostgresTransaction{harness: connection.harness}, nil
}

func (connection *inputPostgresConnection) ExecContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Result, error) {
	if !strings.Contains(query, "pg_advisory_lock") || strings.Contains(query, "pg_advisory_unlock") {
		return nil, errors.New("unexpected Exec query")
	}
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.acquires++
	if len(arguments) == 1 {
		connection.harness.lockIdentities = append(connection.harness.lockIdentities, fmt.Sprint(arguments[0].Value))
	}
	if err := shiftInputPostgresError(&connection.harness.acquireErrors); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (connection *inputPostgresConnection) QueryContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	harness := connection.harness
	harness.mu.Lock()
	defer harness.mu.Unlock()

	if strings.Contains(query, "pg_advisory_unlock") {
		harness.unlocks++
		if err := shiftInputPostgresError(&harness.unlockErrors); err != nil {
			return nil, err
		}
		unlocked := true
		if len(harness.unlockResults) > 0 {
			unlocked = harness.unlockResults[0]
			harness.unlockResults = harness.unlockResults[1:]
		}
		return inputPostgresRows([]driver.Value{unlocked}), nil
	}
	if query == postgresPrimaryReadWriteQuery {
		if err := shiftInputPostgresError(&harness.primaryErrors); err != nil {
			return nil, err
		}
		return inputPostgresRows([]driver.Value{harness.primaryReadWrite}), nil
	}
	if query == postgresClockQuery {
		var value driver.Value
		if harness.primaryReadWrite {
			value = testIssuedAt
		}
		return inputPostgresRows([]driver.Value{harness.primaryReadWrite, value}), nil
	}

	role, write, recognized := inputPostgresQueryRole(query)
	if !recognized {
		return nil, fmt.Errorf("unexpected Query: %s", query)
	}
	if connection.role != role {
		harness.wrongRoleQueries++
		return nil, &pgconn.PgError{Code: "42501", Message: "wrong role"}
	}
	if write {
		harness.roleWrites[role]++
		if err := shiftInputPostgresError(&harness.writeErrors); err != nil {
			return nil, err
		}
	} else {
		harness.roleReads[role]++
	}

	var values []driver.Value
	switch {
	case strings.Contains(query, "admit_qualification_input_source_receipt_v1"):
		values = harness.sourceValues
	case strings.Contains(query, "admit_qualification_input_credential_receipt_v1"):
		values = harness.credentialValues
	case strings.Contains(query, "issue_qualification_input_precommit_v1"):
		values = harness.authorityValues
	case query == postgresInspectOperationInTransactionQuery:
		if !harness.priorOperationExists {
			return inputPostgresEmptyRows(len(harness.authorityValues)), nil
		}
		values = harness.authorityValues
	case strings.Contains(query, "inspect_qualification_input_precommit_operation_v1"),
		strings.Contains(query, "resolve_qualification_input_precommit_authority_v1"):
		values = inputPostgresReadValues(harness.primaryReadWrite, harness.readMissing, harness.authorityValues)
	case strings.Contains(query, "source_receipt"):
		values = inputPostgresReadValues(harness.primaryReadWrite, harness.readMissing, harness.sourceValues)
	case strings.Contains(query, "credential_receipt"):
		crossKindHash := len(arguments) == 1 &&
			fmt.Sprint(arguments[0].Value) == harness.sourceAdmission.AdmissionHash
		if !harness.readMissing && strings.Contains(query, "resolve_") && crossKindHash {
			// The cross-kind test asks the credential DSN for a source hash.
			values = inputPostgresReadValues(harness.primaryReadWrite, true, harness.credentialValues)
		} else {
			values = inputPostgresReadValues(harness.primaryReadWrite, harness.readMissing, harness.credentialValues)
		}
	default:
		return nil, errors.New("recognized query has no result fixture")
	}
	return inputPostgresRows(append([]driver.Value(nil), values...)), nil
}

func inputPostgresQueryRole(query string) (string, bool, bool) {
	switch {
	case strings.Contains(query, "admit_qualification_input_source_receipt_v1"):
		return postgresTestRoleSource, true, true
	case strings.Contains(query, "admit_qualification_input_credential_receipt_v1"):
		return postgresTestRoleCredential, true, true
	case strings.Contains(query, "issue_qualification_input_precommit_v1"):
		return postgresTestRoleInput, true, true
	case query == postgresInspectOperationInTransactionQuery:
		return postgresTestRoleInput, false, true
	case strings.Contains(query, "inspect_qualification_input_precommit_operation_v1"),
		strings.Contains(query, "resolve_qualification_input_precommit_authority_v1"):
		return postgresTestRoleInput, false, true
	case strings.Contains(query, "source_receipt"):
		return postgresTestRoleSource, false, true
	case strings.Contains(query, "credential_receipt"):
		return postgresTestRoleCredential, false, true
	default:
		return "", false, false
	}
}

func inputPostgresReadValues(primary, missing bool, values []driver.Value) []driver.Value {
	result := make([]driver.Value, 1, len(values)+1)
	result[0] = primary
	if missing || !primary {
		return append(result, make([]driver.Value, len(values))...)
	}
	return append(result, values...)
}

type inputPostgresTransaction struct {
	harness *inputPostgresHarness
}

func (transaction *inputPostgresTransaction) Commit() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.commits++
	return shiftInputPostgresError(&transaction.harness.commitErrors)
}

func (transaction *inputPostgresTransaction) Rollback() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.rollbacks++
	return shiftInputPostgresError(&transaction.harness.rollbackErrors)
}

type inputPostgresRowSet struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func inputPostgresRows(values []driver.Value) driver.Rows {
	columns := make([]string, len(values))
	for index := range columns {
		columns[index] = fmt.Sprintf("column_%d", index)
	}
	return &inputPostgresRowSet{columns: columns, values: [][]driver.Value{values}}
}

func inputPostgresEmptyRows(columns int) driver.Rows {
	names := make([]string, columns)
	for index := range names {
		names[index] = fmt.Sprintf("column_%d", index)
	}
	return &inputPostgresRowSet{columns: names}
}

func (rows *inputPostgresRowSet) Columns() []string { return rows.columns }
func (*inputPostgresRowSet) Close() error           { return nil }

func (rows *inputPostgresRowSet) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

var _ driver.Driver = inputPostgresDriver{}
var _ driver.ConnBeginTx = (*inputPostgresConnection)(nil)
var _ driver.ExecerContext = (*inputPostgresConnection)(nil)
var _ driver.QueryerContext = (*inputPostgresConnection)(nil)
