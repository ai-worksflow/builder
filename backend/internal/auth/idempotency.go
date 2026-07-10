package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrIdempotencyConflict    = errors.New("authentication idempotency key was used for a different request")
	ErrIdempotencyInProgress  = errors.New("authentication request is already in progress")
	ErrIdempotencyUnavailable = errors.New("authentication replay protection is unavailable")
)

var validSessionIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:~-]{1,128}$`)

const (
	sessionReceiptSignUp  = "auth_session_sign_up_v1"
	sessionReceiptSignIn  = "auth_session_sign_in_v1"
	sessionReceiptRefresh = "auth_session_refresh_v1"
)

// IdempotentIssuedSession contains the values needed to reconstruct the HTTP
// response without storing a raw session token, CSRF token, or Set-Cookie
// header. Session tokens and initial CSRF tokens are derived from the opaque
// session ID with a server-only key; refresh keeps the already validated CSRF
// value so a partially delivered Set-Cookie response remains retryable.
type IdempotentIssuedSession struct {
	IssuedSession
	CSRFToken string
	IssuedAt  time.Time
	Replayed  bool
}

type idempotentSessionCreator func(*gorm.DB, uuid.UUID, string, time.Time) (storage.UserModel, storage.AuthSessionModel, error)

// SignUpIdempotent commits the user, session, and non-sensitive replay receipt
// in one database transaction. A receipt failure therefore leaves no partial
// account or unusable issued token behind.
func (s *Service) SignUpIdempotent(
	ctx context.Context,
	idempotencyKey, email, displayName, password, userAgent, ipAddress string,
) (IdempotentIssuedSession, error) {
	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return IdempotentIssuedSession{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 120 {
		return IdempotentIssuedSession{}, errors.New("display name must contain 1 to 120 characters")
	}
	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return IdempotentIssuedSession{}, err
	}
	userAgent = normalizeUserAgent(userAgent)
	scope := s.sessionReceiptScope("sign-up", normalizedEmail)
	requestHash := s.sessionRequestFingerprint("sign-up", normalizedEmail, displayName, password, userAgent)
	return s.issueIdempotentSession(ctx, scope, idempotencyKey, requestHash, sessionReceiptSignUp, 201,
		func(transaction *gorm.DB, sessionID uuid.UUID, token string, now time.Time) (storage.UserModel, storage.AuthSessionModel, error) {
			user := storage.UserModel{
				ID: uuid.New(), Email: normalizedEmail, DisplayName: displayName,
				PasswordHash: passwordHash, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&user).Error; err != nil {
				if isUniqueViolation(err) {
					return storage.UserModel{}, storage.AuthSessionModel{}, ErrEmailExists
				}
				return storage.UserModel{}, storage.AuthSessionModel{}, err
			}
			session := s.derivedSessionModel(sessionID, user.ID, token, userAgent, ipAddress, now)
			if err := transaction.Create(&session).Error; err != nil {
				return storage.UserModel{}, storage.AuthSessionModel{}, err
			}
			return user, session, nil
		})
}

// SignInIdempotent prevents a response loss from minting an unbounded series
// of sessions. Repeating the same credentials and key reconstructs one session.
func (s *Service) SignInIdempotent(
	ctx context.Context,
	idempotencyKey, email, password, userAgent, ipAddress string,
) (IdempotentIssuedSession, error) {
	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return IdempotentIssuedSession{}, ErrInvalidCredentials
	}
	var verifiedUser storage.UserModel
	err = s.database.WithContext(ctx).Where("email = ?", normalizedEmail).Take(&verifiedUser).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return IdempotentIssuedSession{}, ErrInvalidCredentials
	}
	if err != nil {
		return IdempotentIssuedSession{}, fmt.Errorf("load user: %w", err)
	}
	verified, err := s.hasher.Verify(password, verifiedUser.PasswordHash)
	if err != nil || !verified {
		return IdempotentIssuedSession{}, ErrInvalidCredentials
	}
	if verifiedUser.DisabledAt != nil {
		return IdempotentIssuedSession{}, ErrUserDisabled
	}
	userAgent = normalizeUserAgent(userAgent)
	scope := s.sessionReceiptScope("sign-in", normalizedEmail)
	requestHash := s.sessionRequestFingerprint("sign-in", normalizedEmail, password, userAgent)
	return s.issueIdempotentSession(ctx, scope, idempotencyKey, requestHash, sessionReceiptSignIn, 200,
		func(transaction *gorm.DB, sessionID uuid.UUID, token string, now time.Time) (storage.UserModel, storage.AuthSessionModel, error) {
			var user storage.UserModel
			err := transaction.Where("id = ? AND password_hash = ? AND disabled_at IS NULL", verifiedUser.ID, verifiedUser.PasswordHash).Take(&user).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return storage.UserModel{}, storage.AuthSessionModel{}, ErrInvalidCredentials
			}
			if err != nil {
				return storage.UserModel{}, storage.AuthSessionModel{}, err
			}
			session := s.derivedSessionModel(sessionID, user.ID, token, userAgent, ipAddress, now)
			if err := transaction.Create(&session).Error; err != nil {
				return storage.UserModel{}, storage.AuthSessionModel{}, err
			}
			return user, session, nil
		})
}

// RotateIdempotent atomically claims the old session, rotates it, and completes
// a replay receipt. Replays can use the now-revoked old token because the
// receipt is looked up before the old session is required to still be active.
func (s *Service) RotateIdempotent(
	ctx context.Context,
	idempotencyKey, token, csrfToken, userAgent, ipAddress string,
) (IdempotentIssuedSession, error) {
	oldDigest, err := tokenDigest(token)
	if err != nil {
		return IdempotentIssuedSession{}, ErrSessionExpired
	}
	csrfToken = strings.TrimSpace(csrfToken)
	if csrfToken == "" {
		return IdempotentIssuedSession{}, ErrInvalidCredentials
	}
	userAgent = normalizeUserAgent(userAgent)
	requestHash := s.sessionRequestFingerprint("refresh", csrfToken, userAgent)
	if replay, found, err := s.replayRefreshReplacement(ctx, idempotencyKey, oldDigest, requestHash); err != nil {
		return IdempotentIssuedSession{}, err
	} else if found {
		replay.CSRFToken = csrfToken
		return replay, nil
	}
	scope := s.sessionReceiptScope("refresh", hex.EncodeToString(oldDigest))
	result, err := s.issueIdempotentSession(ctx, scope, idempotencyKey, requestHash, sessionReceiptRefresh, 200,
		func(transaction *gorm.DB, sessionID uuid.UUID, replacementToken string, now time.Time) (storage.UserModel, storage.AuthSessionModel, error) {
			var oldSession storage.AuthSessionModel
			err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", oldDigest, now).
				Take(&oldSession).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return storage.UserModel{}, storage.AuthSessionModel{}, ErrSessionExpired
			}
			if err != nil {
				return storage.UserModel{}, storage.AuthSessionModel{}, err
			}
			var user storage.UserModel
			err = transaction.Where("id = ? AND disabled_at IS NULL", oldSession.UserID).Take(&user).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return storage.UserModel{}, storage.AuthSessionModel{}, ErrUserDisabled
			}
			if err != nil {
				return storage.UserModel{}, storage.AuthSessionModel{}, err
			}
			replacement := s.derivedSessionModel(sessionID, oldSession.UserID, replacementToken, userAgent, ipAddress, now)
			update := transaction.Model(&storage.AuthSessionModel{}).
				Where("id = ? AND revoked_at IS NULL", oldSession.ID).
				Update("revoked_at", now)
			if update.Error != nil {
				return storage.UserModel{}, storage.AuthSessionModel{}, update.Error
			}
			if update.RowsAffected != 1 {
				return storage.UserModel{}, storage.AuthSessionModel{}, ErrSessionExpired
			}
			if err := transaction.Create(&replacement).Error; err != nil {
				return storage.UserModel{}, storage.AuthSessionModel{}, err
			}
			return user, replacement, nil
		})
	if err != nil {
		return IdempotentIssuedSession{}, err
	}
	result.CSRFToken = csrfToken
	if s.cache != nil {
		_ = s.cache.Del(ctx, s.config.CachePrefix+hex.EncodeToString(oldDigest)).Err()
	}
	return result, nil
}

// replayRefreshReplacement handles the browser edge case where PostgreSQL
// committed the first rotation and the user agent installed Set-Cookie, but the
// fetch caller lost the response. A retry then carries the replacement token,
// not the revoked token used to scope the original receipt. The active token is
// first bound to its session ID before any receipt can be replayed.
func (s *Service) replayRefreshReplacement(
	ctx context.Context,
	idempotencyKey string,
	tokenHash []byte,
	requestHash string,
) (IdempotentIssuedSession, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validSessionIdempotencyKey.MatchString(idempotencyKey) {
		return IdempotentIssuedSession{}, false, ErrIdempotencyUnavailable
	}
	now := s.config.Now().UTC().Truncate(time.Microsecond)
	var result IdempotentIssuedSession
	found := false
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var session storage.AuthSessionModel
		err := transaction.Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", tokenHash, now).Take(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var receipt storage.IdempotencyRecordModel
		err = transaction.Where(
			"idempotency_key = ? AND resource_type = ? AND resource_id = ? AND completed_at IS NOT NULL AND expires_at > ?",
			idempotencyKey, sessionReceiptRefresh, session.ID.String(), now,
		).Take(&receipt).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if receipt.RequestHash != requestHash {
			return ErrIdempotencyConflict
		}
		result, err = s.replaySessionReceipt(transaction, receipt, sessionReceiptRefresh, requestHash, now)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrUserDisabled) || errors.Is(err, ErrIdempotencyUnavailable) {
			return IdempotentIssuedSession{}, found, err
		}
		return IdempotentIssuedSession{}, found, fmt.Errorf("%w: replay replacement session: %v", ErrIdempotencyUnavailable, err)
	}
	return result, found, nil
}

func (s *Service) issueIdempotentSession(
	ctx context.Context,
	scope, idempotencyKey, requestHash, receiptType string,
	status int,
	create idempotentSessionCreator,
) (IdempotentIssuedSession, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validSessionIdempotencyKey.MatchString(idempotencyKey) || len(requestHash) != sha256.Size*2 {
		return IdempotentIssuedSession{}, ErrIdempotencyUnavailable
	}
	now := s.config.Now().UTC().Truncate(time.Microsecond)
	var result IdempotentIssuedSession
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		acquired, receipt, err := s.claimSessionReceipt(transaction, scope, idempotencyKey, requestHash, now)
		if err != nil {
			return err
		}
		if !acquired {
			result, err = s.replaySessionReceipt(transaction, receipt, receiptType, requestHash, now)
			return err
		}

		sessionID := uuid.New()
		token := s.deriveSessionValue("session-token", sessionID)
		user, session, err := create(transaction, sessionID, token, now)
		if err != nil {
			return err
		}
		if s.receiptCompleteHook != nil {
			if err := s.receiptCompleteHook(); err != nil {
				return fmt.Errorf("%w: %v", ErrIdempotencyUnavailable, err)
			}
		}
		resourceType, resourceID := receiptType, session.ID.String()
		completedAt := now
		expiresAt := now.Add(s.config.IdempotencyTTL)
		if session.ExpiresAt.Before(expiresAt) {
			expiresAt = session.ExpiresAt
		}
		headers, _ := json.Marshal(map[string][]string{})
		completion := transaction.Model(&storage.IdempotencyRecordModel{}).
			Where("scope = ? AND idempotency_key = ? AND request_hash = ? AND completed_at IS NULL", scope, idempotencyKey, requestHash).
			Updates(map[string]any{
				"response_status": status, "response_headers": gorm.Expr("?::jsonb", string(headers)), "response_body": nil,
				"resource_type": resourceType, "resource_id": resourceID,
				"locked_until": nil, "completed_at": completedAt, "expires_at": expiresAt,
			})
		if completion.Error != nil || completion.RowsAffected != 1 {
			if completion.Error != nil {
				return fmt.Errorf("%w: complete auth receipt: %v", ErrIdempotencyUnavailable, completion.Error)
			}
			return fmt.Errorf("%w: auth receipt claim was lost", ErrIdempotencyUnavailable)
		}
		result = s.idempotentIssuedSession(user, session, token, false)
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrEmailExists),
			errors.Is(err, ErrSessionExpired), errors.Is(err, ErrUserDisabled),
			errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrIdempotencyInProgress),
			errors.Is(err, ErrIdempotencyUnavailable):
			return IdempotentIssuedSession{}, err
		default:
			return IdempotentIssuedSession{}, fmt.Errorf("%w: %v", ErrIdempotencyUnavailable, err)
		}
	}
	digest := sha256.Sum256([]byte(result.Token))
	s.cacheSession(ctx, s.config.CachePrefix+hex.EncodeToString(digest[:]), storage.AuthSessionModel{
		ID: uuid.MustParse(result.ID), UserID: uuid.MustParse(result.User.ID),
		TokenHash: digest[:], ExpiresAt: result.ExpiresAt, CreatedAt: result.IssuedAt,
	})
	return result, nil
}

func (s *Service) claimSessionReceipt(
	transaction *gorm.DB,
	scope, key, requestHash string,
	now time.Time,
) (bool, storage.IdempotencyRecordModel, error) {
	lockedUntil := now.Add(s.config.IdempotencyTTL)
	created := storage.IdempotencyRecordModel{
		Scope: scope, IdempotencyKey: key, RequestHash: requestHash,
		LockedUntil: &lockedUntil, ExpiresAt: now.Add(s.config.IdempotencyTTL), CreatedAt: now,
	}
	insert := transaction.Clauses(clause.OnConflict{DoNothing: true}).Create(&created)
	if insert.Error != nil {
		return false, storage.IdempotencyRecordModel{}, fmt.Errorf("%w: claim auth receipt: %v", ErrIdempotencyUnavailable, insert.Error)
	}
	if insert.RowsAffected == 1 {
		return true, created, nil
	}

	var current storage.IdempotencyRecordModel
	err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("scope = ? AND idempotency_key = ?", scope, key).Take(&current).Error
	if err != nil {
		return false, storage.IdempotencyRecordModel{}, fmt.Errorf("%w: load auth receipt: %v", ErrIdempotencyUnavailable, err)
	}
	if !current.ExpiresAt.After(now) {
		if err := transaction.Delete(&current).Error; err != nil {
			return false, storage.IdempotencyRecordModel{}, fmt.Errorf("%w: delete expired auth receipt: %v", ErrIdempotencyUnavailable, err)
		}
		if err := transaction.Create(&created).Error; err != nil {
			return false, storage.IdempotencyRecordModel{}, fmt.Errorf("%w: replace expired auth receipt: %v", ErrIdempotencyUnavailable, err)
		}
		return true, created, nil
	}
	if current.RequestHash != requestHash {
		return false, current, ErrIdempotencyConflict
	}
	if current.CompletedAt == nil || current.ResponseStatus == nil || current.ResourceType == nil || current.ResourceID == nil {
		return false, current, ErrIdempotencyInProgress
	}
	return false, current, nil
}

func (s *Service) replaySessionReceipt(
	transaction *gorm.DB,
	receipt storage.IdempotencyRecordModel,
	receiptType, requestHash string,
	now time.Time,
) (IdempotentIssuedSession, error) {
	if receipt.RequestHash != requestHash || receipt.ResourceType == nil || *receipt.ResourceType != receiptType || receipt.ResourceID == nil {
		return IdempotentIssuedSession{}, ErrIdempotencyConflict
	}
	sessionID, err := uuid.Parse(*receipt.ResourceID)
	if err != nil {
		return IdempotentIssuedSession{}, fmt.Errorf("%w: invalid auth receipt resource", ErrIdempotencyUnavailable)
	}
	var session storage.AuthSessionModel
	err = transaction.Where("id = ? AND revoked_at IS NULL AND expires_at > ?", sessionID, now).Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return IdempotentIssuedSession{}, ErrSessionExpired
	}
	if err != nil {
		return IdempotentIssuedSession{}, fmt.Errorf("%w: load replay session: %v", ErrIdempotencyUnavailable, err)
	}
	token := s.deriveSessionValue("session-token", session.ID)
	digest := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(session.TokenHash, digest[:]) != 1 {
		return IdempotentIssuedSession{}, fmt.Errorf("%w: auth receipt token binding failed", ErrIdempotencyUnavailable)
	}
	var user storage.UserModel
	err = transaction.Where("id = ? AND disabled_at IS NULL", session.UserID).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return IdempotentIssuedSession{}, ErrUserDisabled
	}
	if err != nil {
		return IdempotentIssuedSession{}, fmt.Errorf("%w: load replay user: %v", ErrIdempotencyUnavailable, err)
	}
	return s.idempotentIssuedSession(user, session, token, true), nil
}

func (s *Service) idempotentIssuedSession(
	user storage.UserModel,
	session storage.AuthSessionModel,
	token string,
	replayed bool,
) IdempotentIssuedSession {
	return IdempotentIssuedSession{
		IssuedSession: IssuedSession{
			Session: Session{ID: session.ID.String(), User: userFromModel(user), ExpiresAt: session.ExpiresAt},
			Token:   token,
		},
		CSRFToken: s.deriveSessionValue("csrf-token", session.ID),
		IssuedAt:  session.CreatedAt,
		Replayed:  replayed,
	}
}

func (s *Service) derivedSessionModel(
	sessionID, userID uuid.UUID,
	token, userAgent, _ string,
	now time.Time,
) storage.AuthSessionModel {
	digest := sha256.Sum256([]byte(token))
	expiresAt := now.Add(s.config.TTL)
	var userAgentPointer *string
	if userAgent != "" {
		userAgentPointer = &userAgent
	}
	return storage.AuthSessionModel{
		ID: sessionID, UserID: userID, TokenHash: digest[:], ExpiresAt: expiresAt,
		LastSeenAt: now, UserAgent: userAgentPointer, CreatedAt: now,
	}
}

func (s *Service) sessionReceiptScope(operation, subject string) string {
	return "auth:" + operation + ":" + s.keyedDigest("scope", subject)
}

func (s *Service) sessionRequestFingerprint(parts ...string) string {
	return s.keyedDigest("request", parts...)
}

func (s *Service) keyedDigest(label string, parts ...string) string {
	digest := hmac.New(sha256.New, s.replayKey)
	_, _ = digest.Write([]byte("worksflow/auth/idempotency/v1\x00" + label + "\x00"))
	var length [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (s *Service) deriveSessionValue(label string, sessionID uuid.UUID) string {
	digest := hmac.New(sha256.New, s.replayKey)
	_, _ = digest.Write([]byte("worksflow/auth/session-value/v1\x00" + label + "\x00"))
	_, _ = digest.Write(sessionID[:])
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func normalizeUserAgent(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}
