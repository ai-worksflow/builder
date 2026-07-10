package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrEmailExists        = errors.New("email is already registered")
	ErrSessionExpired     = errors.New("session is missing, expired, or revoked")
	ErrUserDisabled       = errors.New("user is disabled")
)

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	AvatarURL   *string   `json:"avatarUrl,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Session struct {
	ID        string    `json:"id"`
	User      User      `json:"user"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type IssuedSession struct {
	Session
	Token string `json:"token"`
}

type ServiceConfig struct {
	TTL            time.Duration
	CachePrefix    string
	IdempotencyTTL time.Duration
	ReplayKey      []byte
	Now            func() time.Time
}

type Service struct {
	database            *gorm.DB
	cache               redis.UniversalClient
	hasher              PasswordHasher
	config              ServiceConfig
	replayKey           []byte
	receiptCompleteHook func() error
}

type cachedSession struct {
	SessionID string    `json:"sessionId"`
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func NewService(database *gorm.DB, cache redis.UniversalClient, hasher PasswordHasher, config ServiceConfig) (*Service, error) {
	if database == nil {
		return nil, errors.New("auth database is required")
	}
	if config.TTL <= 0 {
		config.TTL = 7 * 24 * time.Hour
	}
	if config.CachePrefix == "" {
		config.CachePrefix = "worksflow:session:"
	}
	if config.IdempotencyTTL <= 0 {
		config.IdempotencyTTL = 24 * time.Hour
	}
	if len(config.ReplayKey) != 32 {
		return nil, errors.New("auth replay key must contain exactly 32 bytes")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	replayKey := append([]byte(nil), config.ReplayKey...)
	config.ReplayKey = nil
	return &Service{
		database: database, cache: cache, hasher: hasher, config: config,
		replayKey: replayKey,
	}, nil
}

func (s *Service) SignUp(ctx context.Context, email, displayName, password, userAgent, ipAddress string) (IssuedSession, error) {
	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return IssuedSession{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 120 {
		return IssuedSession{}, errors.New("display name must contain 1 to 120 characters")
	}
	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return IssuedSession{}, err
	}
	now := s.config.Now().UTC()
	model := storage.UserModel{
		ID: uuid.New(), Email: normalizedEmail, DisplayName: displayName,
		PasswordHash: passwordHash, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.database.WithContext(ctx).Create(&model).Error; err != nil {
		if isUniqueViolation(err) {
			return IssuedSession{}, ErrEmailExists
		}
		return IssuedSession{}, fmt.Errorf("create user: %w", err)
	}
	return s.issue(ctx, model, userAgent, ipAddress)
}

func (s *Service) SignIn(ctx context.Context, email, password, userAgent, ipAddress string) (IssuedSession, error) {
	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return IssuedSession{}, ErrInvalidCredentials
	}
	var model storage.UserModel
	err = s.database.WithContext(ctx).Where("email = ?", normalizedEmail).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return IssuedSession{}, ErrInvalidCredentials
	}
	if err != nil {
		return IssuedSession{}, fmt.Errorf("load user: %w", err)
	}
	verified, err := s.hasher.Verify(password, model.PasswordHash)
	if err != nil || !verified {
		return IssuedSession{}, ErrInvalidCredentials
	}
	if model.DisabledAt != nil {
		return IssuedSession{}, ErrUserDisabled
	}
	return s.issue(ctx, model, userAgent, ipAddress)
}

func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	digest, err := tokenDigest(token)
	if err != nil {
		return Session{}, ErrSessionExpired
	}
	cacheKey := s.config.CachePrefix + hex.EncodeToString(digest)
	// PostgreSQL remains the revocation authority. A Redis hit must never let a
	// revoked session through when cache invalidation was delayed or failed.
	now := s.config.Now().UTC()
	var sessionModel storage.AuthSessionModel
	err = s.database.WithContext(ctx).
		Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", digest, now).
		Take(&sessionModel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Session{}, ErrSessionExpired
	}
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	session, err := s.loadSessionUser(ctx, sessionModel.ID.String(), sessionModel.UserID.String(), sessionModel.ExpiresAt)
	if err != nil {
		return Session{}, err
	}
	s.cacheSession(ctx, cacheKey, sessionModel)
	return session, nil
}

func (s *Service) SignOut(ctx context.Context, token string) error {
	digest, err := tokenDigest(token)
	if err != nil {
		return nil
	}
	now := s.config.Now().UTC()
	if err := s.database.WithContext(ctx).Model(&storage.AuthSessionModel{}).
		Where("token_hash = ? AND revoked_at IS NULL", digest).
		Update("revoked_at", now).Error; err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if s.cache != nil {
		_ = s.cache.Del(ctx, s.config.CachePrefix+hex.EncodeToString(digest)).Err()
	}
	return nil
}

// Rotate replaces a live opaque token atomically. Concurrent refresh attempts
// cannot both succeed because the old session row is locked and revoked in the
// same transaction that inserts the replacement.
func (s *Service) Rotate(ctx context.Context, token, userAgent, ipAddress string) (IssuedSession, error) {
	oldDigest, err := tokenDigest(token)
	if err != nil {
		return IssuedSession{}, ErrSessionExpired
	}
	now := s.config.Now().UTC()
	var oldSession storage.AuthSessionModel
	var user storage.UserModel
	var replacement storage.AuthSessionModel
	var replacementToken string
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", oldDigest, now).
			Take(&oldSession).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSessionExpired
			}
			return err
		}
		if err := transaction.Where("id = ? AND disabled_at IS NULL", oldSession.UserID).Take(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserDisabled
			}
			return err
		}
		replacementToken, replacement, err = s.newSession(oldSession.UserID, userAgent, ipAddress, now)
		if err != nil {
			return err
		}
		if result := transaction.Model(&storage.AuthSessionModel{}).
			Where("id = ? AND revoked_at IS NULL", oldSession.ID).
			Update("revoked_at", now); result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return ErrSessionExpired
		}
		return transaction.Create(&replacement).Error
	})
	if err != nil {
		return IssuedSession{}, err
	}
	if s.cache != nil {
		_ = s.cache.Del(ctx, s.config.CachePrefix+hex.EncodeToString(oldDigest)).Err()
	}
	replacementDigest := sha256.Sum256([]byte(replacementToken))
	s.cacheSession(ctx, s.config.CachePrefix+hex.EncodeToString(replacementDigest[:]), replacement)
	return IssuedSession{
		Session: Session{ID: replacement.ID.String(), User: userFromModel(user), ExpiresAt: replacement.ExpiresAt},
		Token:   replacementToken,
	}, nil
}

func (s *Service) issue(ctx context.Context, user storage.UserModel, userAgent, ipAddress string) (IssuedSession, error) {
	now := s.config.Now().UTC()
	token, model, err := s.newSession(user.ID, userAgent, ipAddress, now)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := s.database.WithContext(ctx).Create(&model).Error; err != nil {
		return IssuedSession{}, fmt.Errorf("create session: %w", err)
	}
	digest := sha256.Sum256([]byte(token))
	s.cacheSession(ctx, s.config.CachePrefix+hex.EncodeToString(digest[:]), model)
	return IssuedSession{
		Session: Session{ID: model.ID.String(), User: userFromModel(user), ExpiresAt: model.ExpiresAt},
		Token:   token,
	}, nil
}

func (s *Service) newSession(userID uuid.UUID, userAgent, _ string, now time.Time) (string, storage.AuthSessionModel, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", storage.AuthSessionModel{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	digest := sha256.Sum256([]byte(token))
	expiresAt := now.Add(s.config.TTL)
	userAgent = strings.TrimSpace(userAgent)
	if len(userAgent) > 1024 {
		userAgent = userAgent[:1024]
	}
	var userAgentPointer *string
	if userAgent != "" {
		userAgentPointer = &userAgent
	}
	// IP is intentionally not persisted here until the driver-specific inet scanner
	// is configured; request audit metadata still records a redacted network value.
	model := storage.AuthSessionModel{
		ID: uuid.New(), UserID: userID, TokenHash: digest[:], ExpiresAt: expiresAt,
		LastSeenAt: now, UserAgent: userAgentPointer, CreatedAt: now,
	}
	return token, model, nil
}

func (s *Service) loadSessionUser(ctx context.Context, sessionID, userID string, expiresAt time.Time) (Session, error) {
	parsedUserID, err := uuid.Parse(userID)
	if err != nil || !expiresAt.After(s.config.Now().UTC()) {
		return Session{}, ErrSessionExpired
	}
	var user storage.UserModel
	err = s.database.WithContext(ctx).Where("id = ? AND disabled_at IS NULL", parsedUserID).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Session{}, ErrSessionExpired
	}
	if err != nil {
		return Session{}, fmt.Errorf("load session user: %w", err)
	}
	return Session{ID: sessionID, User: userFromModel(user), ExpiresAt: expiresAt}, nil
}

func (s *Service) cacheSession(ctx context.Context, key string, model storage.AuthSessionModel) {
	if s.cache == nil {
		return
	}
	value, err := json.Marshal(cachedSession{
		SessionID: model.ID.String(), UserID: model.UserID.String(), ExpiresAt: model.ExpiresAt,
	})
	if err != nil {
		return
	}
	ttl := model.ExpiresAt.Sub(s.config.Now().UTC())
	if ttl > 0 {
		_ = s.cache.Set(ctx, key, value, ttl).Err()
	}
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 254 || strings.Count(value, "@") != 1 {
		return "", errors.New("a valid email is required")
	}
	parts := strings.SplitN(value, "@", 2)
	if parts[0] == "" || !strings.Contains(parts[1], ".") {
		return "", errors.New("a valid email is required")
	}
	return value, nil
}

func tokenDigest(token string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(token))
	if err != nil || len(decoded) != 32 {
		return nil, ErrSessionExpired
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return digest[:], nil
}

func userFromModel(model storage.UserModel) User {
	return User{
		ID: model.ID.String(), Email: model.Email, DisplayName: model.DisplayName,
		AvatarURL: model.AvatarURL, CreatedAt: model.CreatedAt,
	}
}

func isUniqueViolation(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint")
}
