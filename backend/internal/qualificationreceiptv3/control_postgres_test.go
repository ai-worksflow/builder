package qualificationreceiptv3

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type postgresControlValuesRow struct {
	values []any
	err    error
}

func (row postgresControlValuesRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != len(row.values) {
		return errors.New("unexpected PostgreSQL control scan width")
	}
	for index, destination := range destinations {
		target := reflect.ValueOf(destination)
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("PostgreSQL control scan destination is not a pointer")
		}
		value := row.values[index]
		if value == nil {
			target.Elem().SetZero()
			continue
		}
		reflected := reflect.ValueOf(value)
		if !reflected.Type().AssignableTo(target.Elem().Type()) {
			return errors.New("PostgreSQL control scan value type differs from destination")
		}
		target.Elem().Set(reflected)
	}
	return nil
}

func TestPostgresControlConstructorsRejectNilDatabase(t *testing.T) {
	if _, err := NewPostgresStore(nil); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("nil Store constructor error = %v", err)
	}
	if _, err := NewPostgresControlStore(nil); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("nil explicit Store constructor error = %v", err)
	}
	if _, err := NewPostgresExpectedResolver(nil); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("nil expected resolver constructor error = %v", err)
	}
}

func TestClassifyPostgresControlWriteError(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"not found":            {&pgconn.PgError{Code: "WQR01"}, ErrControlNotFound},
		"immutable conflict":   {&pgconn.PgError{Code: "WQR02"}, ErrControlConflict},
		"invalid input":        {&pgconn.PgError{Code: "WQR03"}, ErrControlInvalid},
		"not ready":            {&pgconn.PgError{Code: "WQR04"}, ErrControlNotReady},
		"serialization":        {&pgconn.PgError{Code: "40001"}, ErrControlConflict},
		"unique constraint":    {&pgconn.PgError{Code: "23505"}, ErrControlConflict},
		"check constraint":     {&pgconn.PgError{Code: "23514"}, ErrControlInvalid},
		"unknown transport":    {errors.New("connection dropped"), ErrControlStoreOutcomeUnknown},
		"missing returned row": {sql.ErrNoRows, ErrControlNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			if got := classifyPostgresControlWriteError("test", test.err); !errors.Is(got, test.want) {
				t.Fatalf("classification = %v, want %v", got, test.want)
			}
		})
	}
	permission := classifyPostgresControlWriteError("test", &pgconn.PgError{Code: "42501"})
	if errors.Is(permission, ErrControlNotFound) || errors.Is(permission, ErrControlConflict) ||
		errors.Is(permission, ErrControlInvalid) || errors.Is(permission, ErrControlNotReady) ||
		errors.Is(permission, ErrControlStoreOutcomeUnknown) {
		t.Fatalf("permission error was misclassified as a domain outcome: %v", permission)
	}
	if got := classifyPostgresControlWriteError("test", context.Canceled); !errors.Is(got, context.Canceled) ||
		errors.Is(got, ErrControlStoreOutcomeUnknown) {
		t.Fatalf("context cancellation classification = %v", got)
	}
}

func TestPostgresControlRequestScanRejectsProjectionCorruption(t *testing.T) {
	fixture := newControlFixture(t)
	lookup := ControlLookup{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		Kind:        RequestKindSnapshotSeal,
	}
	resolution, err := fixture.plan.ResolveControl(context.Background(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	records, err := recordsFromResolution(lookup, resolution)
	if err != nil {
		t.Fatal(err)
	}
	record := records[0]
	record.StartedAt = time.Date(2026, 7, 19, 12, 1, 0, 0, time.UTC)
	values := postgresControlRequestValues(record)
	stored, err := scanPostgresControlRequest(postgresControlValuesRow{values: values})
	if err != nil || !sameRequestRecord(stored, record, true) {
		t.Fatalf("valid request scan = %+v, %v", stored, err)
	}

	corrupt := append([]any(nil), values...)
	corrupt[12] = "different-snapshot"
	if _, err := scanPostgresControlRequest(postgresControlValuesRow{values: corrupt}); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("corrupt request scalar scan error = %v", err)
	}
	corrupt = append([]any(nil), values...)
	corrupt[2] = []byte(`{"schemaVersion":"wrong"}`)
	if _, err := scanPostgresControlRequest(postgresControlValuesRow{values: corrupt}); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("corrupt request JSONB scan error = %v", err)
	}
}

func TestPostgresControlObservationScanRejectsStoredHashCorruption(t *testing.T) {
	fixture := newControlFixture(t)
	start, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := start.Requests[0]
	observation := fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
	values := postgresControlObservationValues(t, observation)
	stored, err := scanPostgresControlObservation(postgresControlValuesRow{values: values}, request)
	if err != nil || !sameObservation(stored, observation, true) {
		t.Fatalf("valid observation scan = %+v, %v", stored, err)
	}

	corrupt := append([]any(nil), values...)
	corrupt[3] = testDigest("corrupt-observation-record")
	if _, err := scanPostgresControlObservation(postgresControlValuesRow{values: corrupt}, request); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("corrupt observation hash scan error = %v", err)
	}
}

func TestPostgresControlCompletionScanRehydratesGrantOnlyAfterClosure(t *testing.T) {
	fixture := newControlFixture(t)
	fixture.commitSnapshots(t)
	fixture.commitSigning(t)
	completed, err := fixture.service.Complete(context.Background(), CompletionCommand{
		AuthorityID:            uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		SnapshotOperationID:    uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	values := postgresControlCompletionValues(t, completed, fixture.receipt)
	stored, err := scanPostgresControlCompletion(postgresControlValuesRow{values: values})
	if err != nil || stored.verificationEnvelopeHash != stored.EnvelopeDigest || validateStoredCompletion(stored) != nil {
		t.Fatalf("valid completion scan = %+v, %v", stored, err)
	}

	corrupt := append([]any(nil), values...)
	corrupt[41] = testDigest("corrupt-completion-document")
	if result, err := scanPostgresControlCompletion(postgresControlValuesRow{values: corrupt}); !errors.Is(err, ErrControlConflict) || result.verificationEnvelopeHash != "" {
		t.Fatalf("corrupt completion scan = grant:%q error:%v", result.verificationEnvelopeHash, err)
	}
}

func postgresControlRequestValues(record RequestRecord) []any {
	request := record.Request
	return []any{
		record.RequestHash, record.RequestBytes, record.RequestBytes,
		record.Key.AuthorityID.String(), request.OrchestrationID, record.Key.OperationID.String(),
		string(record.Key.Kind), string(record.Key.Role), request.OperationalAuthorityID, request.AuthenticationKeyID,
		request.SignerIdentity, request.SignerKeyID, request.SnapshotID, request.SnapshotDigest, request.ReceiptID,
		request.PlanAuthorityHash, request.InputHash, request.ProjectionHash, request.EvidencePlanHash, request.TargetHash,
		request.TrustHash, request.TrustBindingsDigest, request.TrustPolicyDigest, int64(request.EvidenceHeadVersion),
		request.EvidenceLastEventID, request.EvidenceLastEventHash, request.EvidenceCommandDigest,
		request.EvidenceTrustDigest, request.EvidenceClosureDigest, request.ArtifactIndexDigest,
		record.PayloadHash, record.Payload, record.PAEHash, record.PAE, record.StartedAt,
	}
}

func postgresControlObservationValues(t *testing.T, record ObservationRecord) []any {
	t.Helper()
	projection := controlObservationProjection{
		AcknowledgementTokenHash: record.AckTokenHash, AuthenticationEnvelopeHash: record.AuthenticationEnvelopeHash,
		AuthenticationKeyID: record.AuthenticationKeyID, AuthenticationPayloadHash: record.AuthenticationPayloadHash,
		ClaimTokenHash: record.ClaimTokenHash, Generation: record.Generation,
		ObservedAt: record.ObservedAt.Format(canonicalTimeLayout), RecordedAt: record.RecordedAt.Format(canonicalTimeLayout),
		RequestHash: record.RequestHash, ResultHash: record.ResultHash, Sequence: record.Sequence,
		SignatureHash: record.SignatureHash, Status: record.Status,
	}
	projectionBytes, err := CanonicalJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	return []any{
		record.RequestHash, int64(record.Sequence), int64(record.Generation), record.RecordHash,
		projectionBytes, projectionBytes, string(record.Status), record.ObservedAt, record.RecordedAt,
		record.AuthenticationPayload.OperationalAuthorityID, record.AuthenticationKeyID,
		record.AuthenticationPayloadHash, record.AuthenticationPayloadBytes, record.AuthenticationPayloadBytes,
		record.AuthenticationEnvelopeHash, record.AuthenticationBytes, record.AuthenticationBytes,
		postgresControlNullString(record.ResultHash), postgresControlNullBytes(record.ResultBytes), postgresControlNullBytes(record.ResultBytes),
		postgresControlNullString(record.SignatureHash), postgresControlNullBytes(record.Signature),
		postgresControlNullString(record.ClaimTokenHash), postgresControlNullString(record.Claim.ClaimID),
		postgresControlNullBytes(record.ClaimBytes), postgresControlNullBytes(record.ClaimBytes),
		postgresControlNullString(record.AckTokenHash), postgresControlNullString(record.Acknowledgement.AcknowledgementID),
		postgresControlNullBytes(record.AckBytes), postgresControlNullBytes(record.AckBytes),
	}
}

func postgresControlCompletionValues(t *testing.T, record CompletionRecord, receipt Receipt) []any {
	t.Helper()
	var envelope postgresControlEnvelope
	if err := decodeStrictJSON(record.Envelope, &envelope); err != nil {
		t.Fatal(err)
	}
	signatures := make(map[string][]byte, 2)
	for _, signature := range envelope.Signatures {
		decoded, err := decodeStrictControlBase64(signature.Sig)
		if err != nil {
			t.Fatal(err)
		}
		signatures[signature.KeyID] = decoded
	}
	target := receipt.Target.PromotionTarget
	return []any{
		record.ReceiptID, record.AuthorityID.String(), receipt.Evidence.OrchestrationID,
		record.Operations.Snapshot, record.Operations.ReceiptSign,
		record.RequestHashes.SnapshotSeal, record.ObservationHashes.SnapshotSeal,
		record.RequestHashes.SnapshotVerify, record.ObservationHashes.SnapshotVerify,
		record.RequestHashes.RunnerSign, record.ObservationHashes.RunnerSign,
		record.RequestHashes.ApproverSign, record.ObservationHashes.ApproverSign,
		record.PlanAuthorityHash, record.EvidenceClosureDigest, receipt.ArtifactIndex.ContentDigest,
		record.SnapshotID, record.SnapshotDigest,
		receipt.Signers.Runner.Identity, receipt.Signers.Runner.KeyID,
		SHA256Digest(signatures[receipt.Signers.Runner.KeyID]), signatures[receipt.Signers.Runner.KeyID],
		receipt.Signers.Approver.Identity, receipt.Signers.Approver.KeyID,
		SHA256Digest(signatures[receipt.Signers.Approver.KeyID]), signatures[receipt.Signers.Approver.KeyID],
		target.ProjectID, target.WorkflowRunID, target.NodeKey, target.TargetRevision.ID,
		target.TargetRevision.ContentHash, target.Subject, target.StageGate,
		record.PayloadDigest, record.Payload, record.Payload,
		record.PAEDigest, record.PAE, record.EnvelopeDigest, record.Envelope, record.Envelope,
		record.DocumentHash, record.DocumentBytes, record.DocumentBytes, record.CompletedAt,
	}
}

func postgresControlNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func postgresControlNullBytes(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}
	return value
}
