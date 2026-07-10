package dataruntime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeMetadataRejectsSecretsAndExecutableFunctionFields(t *testing.T) {
	t.Parallel()

	_, _, err := NormalizeMetadataPatch(MetadataAuthUsers, map[string]json.RawMessage{
		"email":    json.RawMessage(`"Owner@Example.com"`),
		"metadata": json.RawMessage(`{"apiKey":"never"}`),
	}, nil)
	if err == nil {
		t.Fatal("expected sensitive metadata to be rejected")
	}
	if runtimeErr, ok := AsRuntimeError(err); !ok || runtimeErr.Code != CodeInvalidRequest {
		t.Fatalf("unexpected error: %v", err)
	}

	_, _, err = NormalizeMetadataPatch(MetadataServerFunctions, map[string]json.RawMessage{
		"name": json.RawMessage(`"sync_user"`),
		"code": json.RawMessage(`"process.exit()"`),
	}, nil)
	if err == nil {
		t.Fatal("expected executable function content to be rejected")
	}
	_, _, err = NormalizeMetadataPatch(MetadataStorageObjects, map[string]json.RawMessage{
		"bucket":   json.RawMessage(`"assets"`),
		"path":     json.RawMessage(`"safe.txt"`),
		"metadata": json.RawMessage(`{"__proto__":{"polluted":true}}`),
	}, nil)
	if err == nil {
		t.Fatal("expected prototype metadata key to be rejected")
	}
}

func TestNormalizeMetadataMergesPatchAndNormalizesUniqueKey(t *testing.T) {
	t.Parallel()

	payload, unique, err := NormalizeMetadataPatch(MetadataAuthUsers, map[string]json.RawMessage{
		"email":       json.RawMessage(`"Owner@Example.COM"`),
		"displayName": json.RawMessage(`"Owner"`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unique != "owner@example.com" || !bytes.Contains(payload, []byte(`"status":"active"`)) {
		t.Fatalf("payload=%s unique=%q", payload, unique)
	}
	updated, _, err := NormalizeMetadataPatch(MetadataAuthUsers, map[string]json.RawMessage{
		"displayName": json.RawMessage(`null`),
	}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(updated, []byte("displayName")) {
		t.Fatalf("null PATCH did not remove optional field: %s", updated)
	}
}

func TestMigrationValidationAndPlanningAreStrict(t *testing.T) {
	t.Parallel()

	bad := []MigrationOperation{{
		Type: MigrationRenameTable, TableID: "not-a-uuid", Name: "renamed",
	}}
	if err := ValidateMigrationOperations(bad); err == nil {
		t.Fatal("expected invalid pinned resource id to fail")
	}

	tableID, columnID := uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	current := []Table{{
		ID: tableID, Name: "users", RecordCount: 2, CreatedAt: now, UpdatedAt: now,
		Columns: []Column{{ID: columnID, Name: "name", Type: ColumnText, CreatedAt: now}},
	}}
	required := []MigrationOperation{{
		Type: MigrationAddColumn, TableID: tableID,
		Column: &ColumnInput{Name: "email", Type: ColumnText, Required: true},
	}}
	if err := ValidateMigrationOperations(required); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := compileMigrationPlan(current, required, now); err == nil {
		t.Fatal("expected required column without default to conflict with existing records")
	}

	withDefault := json.RawMessage(`"unknown@example.com"`)
	operations := []MigrationOperation{
		{Type: MigrationAddColumn, TableID: tableID, Column: &ColumnInput{Name: "email", Type: ColumnText, Required: true, DefaultValue: withDefault}},
		{Type: MigrationDropColumn, TableID: tableID, ColumnID: columnID},
	}
	if err := ValidateMigrationOperations(operations); err != nil {
		t.Fatal(err)
	}
	plan, tables, changes, err := compileMigrationPlan(current, operations, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 2 || len(tables) != 1 || len(tables[0].Columns) != 1 || tables[0].Columns[0].Name != "email" {
		t.Fatalf("unexpected plan: %+v tables=%+v", plan, tables)
	}
	if !changes[1].Destructive || changes[0].Destructive {
		t.Fatalf("destructive flags are wrong: %+v", changes)
	}
}

func TestAESGCMSealerNeverReturnsPlaintextAndHasNoReadAPI(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x42}, 32)
	sealer, err := NewAESGCMSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("super-secret-value")
	first, err := sealer.Seal(plaintext, "project:variable")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealer.Seal(plaintext, "project:variable")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, plaintext) || bytes.Equal(first, second) {
		t.Fatal("ciphertext leaked plaintext or reused a nonce")
	}
	var onlySeal ValueSealer = sealer
	if _, err := onlySeal.Seal([]byte("x"), "aad"); err != nil {
		t.Fatal(err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(key)
	parsed, err := ParseEncryptionKey(encoded)
	if err != nil || !bytes.Equal(parsed, key) {
		t.Fatalf("parse key: %v", err)
	}
	if _, err := ParseEncryptionKey("short"); err == nil {
		t.Fatal("expected short key to fail")
	}
}

func TestConfirmationErrorsRetainDistinctCodes(t *testing.T) {
	t.Parallel()

	required := NewError(CodeConfirmationRequired, 409, "required")
	expired := NewError(CodeConfirmationExpired, 409, "expired")
	if errors.Is(required, expired) || required.Code == expired.Code {
		t.Fatal("confirmation failure modes must remain distinguishable")
	}
}
