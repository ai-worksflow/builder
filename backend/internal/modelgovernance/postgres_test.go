package modelgovernance

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresActivationRecordScanIsNullTotalAndValidated(t *testing.T) {
	record := postgresActivationTestRecord(t, 2, "postgres-scan")
	got, err := scanPostgresActivationRecord(postgresActivationStaticRow{record: record, nullIndex: -1})
	if err != nil || !sameActivationRecord(got, record) {
		t.Fatalf("scan exact activation = %+v, %v", got, err)
	}
	for _, index := range append([]int{24}, integerRange(0, 19)...) {
		_, err := scanPostgresActivationRecord(postgresActivationStaticRow{record: record, nullIndex: index})
		if !errors.Is(err, ErrActivationConflict) {
			t.Fatalf("NULL column %d error = %v, want activation conflict", index, err)
		}
	}

	corrupt := record
	corrupt.RequestHash = testDigest("different-request-hash")
	if _, err := scanPostgresActivationRecord(postgresActivationStaticRow{record: corrupt, nullIndex: -1}); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("corrupt stored request hash error = %v", err)
	}
}

func TestPostgresActivationCommitErrorIsAlwaysUnknown(t *testing.T) {
	acknowledgement := errors.New("commit acknowledgement lost")
	err := commitPostgresActivation(postgresActivationCommitterStub{err: acknowledgement})
	if !errors.Is(err, ErrActivationOutcomeUnknown) || !errors.Is(err, acknowledgement) {
		t.Fatalf("commit error = %v, want joined unknown outcome and driver cause", err)
	}
	if err := commitPostgresActivation(postgresActivationCommitterStub{}); err != nil {
		t.Fatalf("successful commit = %v", err)
	}
}

func TestPostgresActivationClassifiesKnownConflicts(t *testing.T) {
	for _, code := range []string{"40001", "40P01", "23505", "23514", "22023"} {
		err := classifyPostgresActivationAppendError(&pgconn.PgError{Code: code, Message: "known CAS or constraint conflict"})
		if !errors.Is(err, ErrActivationConflict) {
			t.Fatalf("SQLSTATE %s error = %v", code, err)
		}
	}
	unknown := errors.New("database unavailable before append")
	if err := classifyPostgresActivationAppendError(unknown); errors.Is(err, ErrActivationConflict) || !errors.Is(err, unknown) {
		t.Fatalf("unclassified error = %v", err)
	}
	revocation := classifyPostgresRevocationObservationError(&pgconn.PgError{Code: "40001", Message: "rollback"})
	if !errors.Is(revocation, ErrGovernanceUntrusted) {
		t.Fatalf("revocation rollback error = %v", revocation)
	}
}

func TestNewPostgresActivationStoreRejectsNilDatabase(t *testing.T) {
	if _, err := NewPostgresActivationStore(nil); !errors.Is(err, ErrGovernanceInvalid) {
		t.Fatalf("nil database error = %v", err)
	}
}

type postgresActivationStaticRow struct {
	record    ActivationRecord
	nullIndex int
}

func (row postgresActivationStaticRow) Scan(destinations ...any) error {
	if len(destinations) != 25 {
		return fmt.Errorf("destination count = %d", len(destinations))
	}
	values := []any{
		row.record.OperationID, row.record.RequestHash, row.record.AuthorityKind, row.record.Workload, row.record.ProfileID,
		row.record.ProfileContentHash, row.record.ReceiptDigest, row.record.ReceiptPayloadDigest,
		row.record.ActivationEnvelopeDigest, row.record.ActivationPayloadDigest,
		int64(row.record.PreviousGeneration), int64(row.record.Generation),
		row.record.PreviousFence, row.record.Fence, row.record.CorpusContentHash,
		row.record.ProviderRouteAuthorityHash, row.record.RunnerImmutableDigest,
		row.record.SourceTreeDigest, row.record.TrustPolicyHash,
		nullableString(row.record.GenesisEnvelopeDigest), nullableString(row.record.GenesisPayloadDigest),
		nullableString(row.record.InitialRevocationAuthorityID), nullableString(row.record.InitialRevocationAuthorityHash),
		nullablePositiveInt64(row.record.InitialRevocationAuthorityEpoch), row.record.ActivatedAt,
	}
	if row.nullIndex >= 0 && row.nullIndex < len(values) {
		values[row.nullIndex] = nil
	}
	for index, destination := range destinations {
		var err error
		switch target := destination.(type) {
		case *uuid.NullUUID:
			err = target.Scan(values[index])
		case *sql.NullString:
			err = target.Scan(values[index])
		case *sql.NullInt64:
			err = target.Scan(values[index])
		case *sql.NullTime:
			err = target.Scan(values[index])
		default:
			return fmt.Errorf("unsupported destination %T", destination)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func integerRange(first, end int) []int {
	values := make([]int, 0, end-first)
	for value := first; value < end; value++ {
		values = append(values, value)
	}
	return values
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositiveInt64(value uint64) any {
	if value == 0 {
		return nil
	}
	return int64(value)
}

type postgresActivationCommitterStub struct {
	err error
}

func (stub postgresActivationCommitterStub) Commit() error { return stub.err }

func postgresActivationTestRecord(t *testing.T, generation uint64, seed string) ActivationRecord {
	t.Helper()
	operationID := uuid.NewString()
	receiptDigest := testDigest(seed + "-receipt-envelope")
	previousFence := testDigest(seed + "-previous-fence")
	request := ActivationRequest{
		OperationID: operationID, ReceiptDigest: receiptDigest,
		ExpectedGeneration: generation - 1, ExpectedFence: previousFence,
	}
	requestHash, err := activationRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	return ActivationRecord{
		AuthorityKind: ActivationAuthorityKind,
		OperationID:   operationID, RequestHash: requestHash, Workload: "reference-app",
		ProfileID: uuid.NewString(), ProfileContentHash: testDigest(seed + "-profile"),
		ReceiptDigest: receiptDigest, ReceiptPayloadDigest: testDigest(seed + "-receipt-payload"),
		ActivationEnvelopeDigest: testDigest(seed + "-activation-envelope"),
		ActivationPayloadDigest:  testDigest(seed + "-activation-payload"),
		PreviousGeneration:       generation - 1, Generation: generation,
		PreviousFence: previousFence, Fence: testDigest(seed + "-fence"),
		CorpusContentHash:          testDigest(seed + "-corpus"),
		ProviderRouteAuthorityHash: testDigest(seed + "-route"),
		RunnerImmutableDigest:      testDigest(seed + "-runner"),
		SourceTreeDigest:           testDigest(seed + "-source"), TrustPolicyHash: testDigest("postgres-governance-policy"),
		ActivatedAt: time.Date(2026, 7, 19, 4, 5, 6, 789000000, time.UTC),
	}
}
