package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAuthReplayDerivationIsDeterministicAndDomainSeparated(t *testing.T) {
	service := &Service{replayKey: bytes.Repeat([]byte{0x42}, 32)}
	sessionID := uuid.New()
	token := service.deriveSessionValue("session-token", sessionID)
	if token != service.deriveSessionValue("session-token", sessionID) {
		t.Fatal("session replay derivation was not deterministic")
	}
	if token == service.deriveSessionValue("csrf-token", sessionID) || token == service.deriveSessionValue("session-token", uuid.New()) {
		t.Fatal("session replay derivation was not domain separated")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatalf("derived session token is malformed: len=%d err=%v", len(decoded), err)
	}
	first := service.sessionRequestFingerprint("sign-in", "owner@example.com", "secret-password")
	second := service.sessionRequestFingerprint("sign-in", "owner@example.com", "secret-password")
	if first != second || strings.Contains(first, "secret-password") || first == service.sessionRequestFingerprint("sign-in", "owner@example.com", "different-password") {
		t.Fatal("keyed authentication request fingerprint is unsafe or unstable")
	}
}

func TestAuthServiceRequiresServerOnlyReplayKey(t *testing.T) {
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 user=test dbname=test sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(database, nil, PasswordHasher{}, ServiceConfig{}); err == nil || !strings.Contains(err.Error(), "replay key") {
		t.Fatalf("NewService() error = %v, want replay key validation", err)
	}
}

func TestIdempotentRefreshConcurrentReplayAndSensitiveReceipt(t *testing.T) {
	service, database, cleanup := newIdempotentAuthIntegrationService(t)
	defer cleanup()
	oldToken, user := seedAuthSession(t, service, database)
	key := "concurrent-refresh-" + uuid.NewString()

	const requests = 8
	start := make(chan struct{})
	results := make([]IdempotentIssuedSession, requests)
	errorsByRequest := make([]error, requests)
	var wait sync.WaitGroup
	for index := 0; index < requests; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsByRequest[index] = service.RotateIdempotent(context.Background(), key, oldToken, "stable-csrf-token", "integration-agent", "127.0.0.1")
		}(index)
	}
	close(start)
	wait.Wait()

	nonReplay := 0
	for index, err := range errorsByRequest {
		if err != nil {
			t.Fatalf("concurrent refresh %d failed: %v", index, err)
		}
		if !results[index].Replayed {
			nonReplay++
		}
		if results[index].ID != results[0].ID || results[index].Token != results[0].Token || results[index].CSRFToken != results[0].CSRFToken || !results[index].IssuedAt.Equal(results[0].IssuedAt) {
			t.Fatalf("concurrent refresh returned inconsistent result: first=%+v current=%+v", results[0], results[index])
		}
	}
	if nonReplay != 1 {
		t.Fatalf("new refresh executions = %d, want 1", nonReplay)
	}

	replay, err := service.RotateIdempotent(context.Background(), key, oldToken, "stable-csrf-token", "integration-agent", "127.0.0.1")
	if err != nil || !replay.Replayed || replay.Token != results[0].Token || replay.CSRFToken != results[0].CSRFToken {
		t.Fatalf("completed refresh did not replay: result=%+v err=%v", replay, err)
	}
	if _, err := service.RotateIdempotent(context.Background(), key, oldToken, "stable-csrf-token", "different-agent", "127.0.0.1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same key with a different refresh request error = %v, want conflict", err)
	}
	replacementCookieReplay, err := service.RotateIdempotent(context.Background(), key, results[0].Token, "stable-csrf-token", "integration-agent", "127.0.0.1")
	if err != nil || !replacementCookieReplay.Replayed || replacementCookieReplay.ID != results[0].ID || replacementCookieReplay.Token != results[0].Token {
		t.Fatalf("replacement-cookie retry rotated again: result=%+v err=%v", replacementCookieReplay, err)
	}

	var sessionCount, activeCount int64
	if err := database.Model(&storage.AuthSessionModel{}).Where("user_id = ?", user.ID).Count(&sessionCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.AuthSessionModel{}).Where("user_id = ? AND revoked_at IS NULL", user.ID).Count(&activeCount).Error; err != nil {
		t.Fatal(err)
	}
	if sessionCount != 2 || activeCount != 1 {
		t.Fatalf("sessions after concurrent refresh: total=%d active=%d", sessionCount, activeCount)
	}
	if _, err := service.Authenticate(context.Background(), oldToken); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("old token remained active: %v", err)
	}
	if authenticated, err := service.Authenticate(context.Background(), results[0].Token); err != nil || authenticated.ID != results[0].ID {
		t.Fatalf("replacement token is unusable: session=%+v err=%v", authenticated, err)
	}

	oldDigest, _ := tokenDigest(oldToken)
	scope := service.sessionReceiptScope("refresh", base16(oldDigest))
	var receipt storage.IdempotencyRecordModel
	if err := database.Where("scope = ? AND idempotency_key = ?", scope, key).Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	persisted := strings.Join([]string{
		receipt.Scope, receipt.IdempotencyKey, receipt.RequestHash,
		string(receipt.ResponseHeaders), string(receipt.ResponseBody), pointerValue(receipt.ResourceType), pointerValue(receipt.ResourceID),
	}, "\n")
	for _, secret := range []string{oldToken, results[0].Token, results[0].CSRFToken, "stable-csrf-token", "Set-Cookie", "integration-agent"} {
		if strings.Contains(persisted, secret) {
			t.Fatalf("auth receipt persisted sensitive value %q: %s", secret, persisted)
		}
	}
	if len(receipt.ResponseBody) != 0 || receipt.ResourceID == nil || *receipt.ResourceID != results[0].ID {
		t.Fatalf("auth receipt contains an unsafe replay body or wrong resource: %+v", receipt)
	}
}

func TestIdempotentRefreshReceiptFailureRollsBackRotation(t *testing.T) {
	service, database, cleanup := newIdempotentAuthIntegrationService(t)
	defer cleanup()
	oldToken, user := seedAuthSession(t, service, database)
	key := "failed-refresh-" + uuid.NewString()
	service.receiptCompleteHook = func() error { return errors.New("injected receipt write failure") }

	if _, err := service.RotateIdempotent(context.Background(), key, oldToken, "stable-csrf-token", "integration-agent", "127.0.0.1"); !errors.Is(err, ErrIdempotencyUnavailable) {
		t.Fatalf("RotateIdempotent() error = %v, want unavailable", err)
	}
	if authenticated, err := service.Authenticate(context.Background(), oldToken); err != nil || authenticated.User.ID != user.ID.String() {
		t.Fatalf("old token was not recoverable after receipt failure: session=%+v err=%v", authenticated, err)
	}
	var sessionCount, receiptCount int64
	if err := database.Model(&storage.AuthSessionModel{}).Where("user_id = ?", user.ID).Count(&sessionCount).Error; err != nil {
		t.Fatal(err)
	}
	oldDigest, _ := tokenDigest(oldToken)
	scope := service.sessionReceiptScope("refresh", base16(oldDigest))
	if err := database.Model(&storage.IdempotencyRecordModel{}).Where("scope = ? AND idempotency_key = ?", scope, key).Count(&receiptCount).Error; err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 || receiptCount != 0 {
		t.Fatalf("receipt failure committed partial state: sessions=%d receipts=%d", sessionCount, receiptCount)
	}
}

func TestIdempotentSignUpAndSignInReplayWithoutPersistingCredentials(t *testing.T) {
	service, database, cleanup := newIdempotentAuthIntegrationService(t)
	defer cleanup()
	email := "receipt-" + uuid.NewString() + "@example.com"
	password := "correct horse battery staple"
	signUpKey := "sign-up-" + uuid.NewString()
	created, err := service.SignUpIdempotent(context.Background(), signUpKey, email, "Receipt Owner", password, "integration-agent", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.SignUpIdempotent(context.Background(), signUpKey, email, "Receipt Owner", password, "integration-agent", "127.0.0.1")
	if err != nil || !replayed.Replayed || replayed.Token != created.Token || replayed.CSRFToken != created.CSRFToken || replayed.ID != created.ID {
		t.Fatalf("sign-up replay mismatch: created=%+v replay=%+v err=%v", created, replayed, err)
	}
	if _, err := service.SignUpIdempotent(context.Background(), signUpKey, email, "Other Owner", password, "integration-agent", "127.0.0.1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed sign-up request error = %v, want conflict", err)
	}

	signInKey := "sign-in-" + uuid.NewString()
	signedIn, err := service.SignInIdempotent(context.Background(), signInKey, email, password, "integration-agent", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signedInReplay, err := service.SignInIdempotent(context.Background(), signInKey, email, password, "integration-agent", "127.0.0.1")
	if err != nil || !signedInReplay.Replayed || signedInReplay.Token != signedIn.Token || signedInReplay.CSRFToken != signedIn.CSRFToken {
		t.Fatalf("sign-in replay mismatch: created=%+v replay=%+v err=%v", signedIn, signedInReplay, err)
	}
	if _, err := service.SignInIdempotent(context.Background(), signInKey, email, password, "different-agent", "127.0.0.1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed sign-in request error = %v, want conflict", err)
	}

	var receipts []storage.IdempotencyRecordModel
	if err := database.Where("idempotency_key IN ?", []string{signUpKey, signInKey}).Find(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 {
		t.Fatalf("auth receipt count = %d, want 2", len(receipts))
	}
	for _, receipt := range receipts {
		persisted := receipt.Scope + "\n" + receipt.RequestHash + "\n" + string(receipt.ResponseHeaders) + "\n" + string(receipt.ResponseBody)
		for _, secret := range []string{email, password, created.Token, created.CSRFToken, signedIn.Token, signedIn.CSRFToken, "Set-Cookie"} {
			if strings.Contains(persisted, secret) {
				t.Fatalf("credential or cookie leaked into auth receipt: %q in %s", secret, persisted)
			}
		}
	}
}

func TestIdempotentSignUpAndSignInReceiptFailureRollsBackMutation(t *testing.T) {
	service, database, cleanup := newIdempotentAuthIntegrationService(t)
	defer cleanup()
	service.receiptCompleteHook = func() error { return errors.New("injected receipt write failure") }
	email := "receipt-" + uuid.NewString() + "@example.com"
	password := "correct horse battery staple"
	signUpKey := "sign-up-" + uuid.NewString()
	if _, err := service.SignUpIdempotent(context.Background(), signUpKey, email, "Receipt Owner", password, "integration-agent", "127.0.0.1"); !errors.Is(err, ErrIdempotencyUnavailable) {
		t.Fatalf("SignUpIdempotent() error = %v, want unavailable", err)
	}
	var users, sessions, receipts int64
	if err := database.Model(&storage.UserModel{}).Where("email = ?", email).Count(&users).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.AuthSessionModel{}).Count(&sessions).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.IdempotencyRecordModel{}).Where("idempotency_key = ?", signUpKey).Count(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if users != 0 || sessions != 0 || receipts != 0 {
		t.Fatalf("failed sign-up committed partial state: users=%d sessions=%d receipts=%d", users, sessions, receipts)
	}

	service.receiptCompleteHook = nil
	created, err := service.SignUpIdempotent(context.Background(), signUpKey, email, "Receipt Owner", password, "integration-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("sign-up was not recoverable with the same key: %v", err)
	}
	service.receiptCompleteHook = func() error { return errors.New("injected receipt write failure") }
	signInKey := "sign-in-" + uuid.NewString()
	if _, err := service.SignInIdempotent(context.Background(), signInKey, email, password, "integration-agent", "127.0.0.1"); !errors.Is(err, ErrIdempotencyUnavailable) {
		t.Fatalf("SignInIdempotent() error = %v, want unavailable", err)
	}
	if err := database.Model(&storage.AuthSessionModel{}).Where("user_id = ?", created.User.ID).Count(&sessions).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.IdempotencyRecordModel{}).Where("idempotency_key = ?", signInKey).Count(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || receipts != 0 {
		t.Fatalf("failed sign-in committed partial state: sessions=%d receipts=%d", sessions, receipts)
	}
}

func newIdempotentAuthIntegrationService(t *testing.T) (*Service, *gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	baseDatabase, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "auth_idempotency_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := baseDatabase.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	if err := baseDatabase.Exec(fmt.Sprintf(`
CREATE TABLE "%[1]s".users (
  id uuid PRIMARY KEY, email text NOT NULL UNIQUE, display_name text NOT NULL,
  password_hash text NOT NULL, avatar_url text, disabled_at timestamptz,
  created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE "%[1]s".auth_sessions (
  id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES "%[1]s".users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE, expires_at timestamptz NOT NULL,
  revoked_at timestamptz, last_seen_at timestamptz NOT NULL, user_agent text,
  ip_address inet, created_at timestamptz NOT NULL
);
CREATE TABLE "%[1]s".idempotency_records (
  scope text NOT NULL, idempotency_key text NOT NULL, request_hash text NOT NULL,
  response_status integer, response_headers jsonb, response_body bytea,
  resource_type text, resource_id text, locked_until timestamptz,
  expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL,
  completed_at timestamptz, PRIMARY KEY (scope, idempotency_key)
);
ALTER TABLE "%[1]s".idempotency_records
  ADD CONSTRAINT idempotency_auth_receipt_safe_check
  CHECK (
    resource_type NOT IN ('auth_session_sign_up_v1', 'auth_session_sign_in_v1', 'auth_session_refresh_v1')
    OR (
      scope ~ '^auth:(sign-up|sign-in|refresh):[0-9a-f]{64}$'
      AND request_hash ~ '^[0-9a-f]{64}$'
      AND resource_id IS NOT NULL
      AND resource_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
      AND response_status IS NOT NULL AND response_body IS NULL
      AND COALESCE(response_headers, '{}'::jsonb) = '{}'::jsonb AND completed_at IS NOT NULL
    )
  );
CREATE INDEX idempotency_auth_refresh_replay_idx
  ON "%[1]s".idempotency_records (idempotency_key, resource_id)
  WHERE resource_type = 'auth_session_refresh_v1' AND completed_at IS NOT NULL;`, schema)).Error; err != nil {
		_ = baseDatabase.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		t.Fatal(err)
	}
	parsedDSN, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedDSN.Query()
	query.Set("search_path", schema)
	parsedDSN.RawQuery = query.Encode()
	database, err := gorm.Open(postgres.New(postgres.Config{DSN: parsedDSN.String(), PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := NewPasswordHasher(PasswordParams{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(database, nil, hasher, ServiceConfig{
		TTL: time.Hour, IdempotencyTTL: time.Hour, ReplayKey: bytes.Repeat([]byte{0x5a}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		sqlDatabase, sqlErr := database.DB()
		if sqlErr == nil {
			_ = sqlDatabase.Close()
		}
		_ = baseDatabase.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		baseSQLDatabase, baseSQLErr := baseDatabase.DB()
		if baseSQLErr == nil {
			_ = baseSQLDatabase.Close()
		}
	}
	return service, database, cleanup
}

func seedAuthSession(t *testing.T, service *Service, database *gorm.DB) (string, storage.UserModel) {
	t.Helper()
	passwordHash, err := service.hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := storage.UserModel{
		ID: uuid.New(), Email: "receipt-" + uuid.NewString() + "@example.com", DisplayName: "Receipt Owner",
		PasswordHash: passwordHash, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	tokenMaterial := sha256.Sum256(user.ID[:])
	oldToken := base64.RawURLEncoding.EncodeToString(tokenMaterial[:])
	digest := sha256.Sum256([]byte(oldToken))
	session := storage.AuthSessionModel{
		ID: uuid.New(), UserID: user.ID, TokenHash: digest[:], ExpiresAt: now.Add(time.Hour),
		LastSeenAt: now, CreatedAt: now,
	}
	if err := database.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	return oldToken, user
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func base16(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = alphabet[item>>4]
		result[index*2+1] = alphabet[item&0x0f]
	}
	return string(result)
}
