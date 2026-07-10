package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

var (
	ErrIdempotencyConflict   = errors.New("idempotency key was used for a different request")
	ErrIdempotencyInProgress = errors.New("idempotent request is already in progress")
)

type IdempotencyConfig struct {
	TTL              time.Duration
	LockTTL          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int
	Now              func() time.Time
}

type StoredResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
}

type ClaimResult struct {
	Acquired bool
	Replay   *StoredResponse
}

// IdempotencyRepository owns the database claim used by mutating HTTP routes.
// A row is locked while its state is inspected so only one expired lease can be
// recovered. Completed responses are immutable until the record expires.
type IdempotencyRepository struct {
	database *gorm.DB
	config   IdempotencyConfig
}

func NewIdempotencyRepository(database *gorm.DB, config IdempotencyConfig) (*IdempotencyRepository, error) {
	if database == nil {
		return nil, errors.New("idempotency database is required")
	}
	if config.TTL <= 0 {
		config.TTL = 24 * time.Hour
	}
	if config.LockTTL <= 0 {
		config.LockTTL = 2 * time.Minute
	}
	if config.MaxRequestBytes <= 0 {
		config.MaxRequestBytes = 4 << 20
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = 8 << 20
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &IdempotencyRepository{database: database, config: config}, nil
}

func (r *IdempotencyRepository) Claim(ctx context.Context, scope, key, requestHash string) (ClaimResult, error) {
	if strings.TrimSpace(scope) == "" || !validIdempotencyKey.MatchString(key) || len(requestHash) != sha256.Size*2 {
		return ClaimResult{}, errors.New("invalid idempotency claim")
	}
	now := r.config.Now().UTC()
	lockedUntil := now.Add(r.config.LockTTL)
	created := storage.IdempotencyRecordModel{
		Scope: scope, IdempotencyKey: key, RequestHash: requestHash,
		LockedUntil: &lockedUntil, ExpiresAt: now.Add(r.config.TTL), CreatedAt: now,
	}
	result := ClaimResult{}
	err := r.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		insert := transaction.Clauses(clause.OnConflict{DoNothing: true}).Create(&created)
		if insert.Error != nil {
			return fmt.Errorf("claim idempotency key: %w", insert.Error)
		}
		if insert.RowsAffected == 1 {
			result.Acquired = true
			return nil
		}

		var current storage.IdempotencyRecordModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope = ? AND idempotency_key = ?", scope, key).Take(&current).Error; err != nil {
			return fmt.Errorf("load idempotency claim: %w", err)
		}
		if !current.ExpiresAt.After(now) {
			if err := transaction.Delete(&current).Error; err != nil {
				return fmt.Errorf("delete expired idempotency claim: %w", err)
			}
			if err := transaction.Create(&created).Error; err != nil {
				return fmt.Errorf("replace expired idempotency claim: %w", err)
			}
			result.Acquired = true
			return nil
		}
		if current.RequestHash != requestHash {
			return ErrIdempotencyConflict
		}
		if current.CompletedAt != nil && current.ResponseStatus != nil {
			headers := http.Header{}
			if len(current.ResponseHeaders) > 0 {
				if err := json.Unmarshal(current.ResponseHeaders, &headers); err != nil {
					return fmt.Errorf("decode idempotency response headers: %w", err)
				}
			}
			result.Replay = &StoredResponse{Status: *current.ResponseStatus, Headers: headers, Body: append([]byte(nil), current.ResponseBody...)}
			return nil
		}
		if current.LockedUntil != nil && current.LockedUntil.After(now) {
			return ErrIdempotencyInProgress
		}
		if err := transaction.Model(&storage.IdempotencyRecordModel{}).
			Where("scope = ? AND idempotency_key = ? AND completed_at IS NULL", scope, key).
			Updates(map[string]any{"locked_until": lockedUntil, "expires_at": now.Add(r.config.TTL)}).Error; err != nil {
			return fmt.Errorf("recover idempotency claim: %w", err)
		}
		result.Acquired = true
		return nil
	})
	return result, err
}

func (r *IdempotencyRepository) Complete(ctx context.Context, scope, key, requestHash string, response StoredResponse) error {
	now := r.config.Now().UTC()
	headers, err := json.Marshal(safeReplayHeaders(response.Headers))
	if err != nil {
		return fmt.Errorf("encode idempotency response headers: %w", err)
	}
	updated := r.database.WithContext(ctx).Model(&storage.IdempotencyRecordModel{}).
		Where("scope = ? AND idempotency_key = ? AND request_hash = ? AND completed_at IS NULL", scope, key, requestHash).
		Updates(map[string]any{
			"response_status": response.Status, "response_headers": headers,
			"response_body": response.Body, "locked_until": nil,
			"completed_at": now, "expires_at": now.Add(r.config.TTL),
		})
	if updated.Error != nil {
		return fmt.Errorf("complete idempotency claim: %w", updated.Error)
	}
	if updated.RowsAffected != 1 {
		return errors.New("idempotency claim was lost before completion")
	}
	return nil
}

func (r *IdempotencyRepository) Release(ctx context.Context, scope, key, requestHash string) error {
	return r.database.WithContext(ctx).
		Where("scope = ? AND idempotency_key = ? AND request_hash = ? AND completed_at IS NULL", scope, key, requestHash).
		Delete(&storage.IdempotencyRecordModel{}).Error
}

// PersistIdempotency must run after authentication and CaptureIdempotencyKey.
// Requests without a key pass through. The scope includes the authenticated
// user and canonical route so the same client key can safely be reused elsewhere.
func PersistIdempotency(repository *IdempotencyRepository) gin.HandlerFunc {
	return func(ginContext *gin.Context) {
		key := IdempotencyKey(ginContext)
		if key == "" {
			ginContext.Next()
			return
		}
		identity, ok := GetIdentity(ginContext)
		if !ok || identity.Session.User.ID == "" {
			problem.Write(ginContext, problem.New(http.StatusUnauthorized, "authentication_required", "Authentication required", "An authenticated user is required for idempotent mutations."))
			return
		}
		body, err := readRequestBody(ginContext, repository.config.MaxRequestBytes)
		if err != nil {
			problem.Write(ginContext, problem.New(http.StatusRequestEntityTooLarge, "request_too_large", "Request is too large", "The request body exceeds the idempotency limit."))
			return
		}
		scope := idempotencyScope(ginContext, identity.Session.User.ID)
		requestHash := hashIdempotentRequest(ginContext.Request, identity.Session.User.ID, body)
		claim, err := repository.Claim(ginContext.Request.Context(), scope, key, requestHash)
		if err != nil {
			switch {
			case errors.Is(err, ErrIdempotencyConflict):
				problem.Write(ginContext, problem.New(http.StatusConflict, "idempotency_key_conflict", "Idempotency key conflict", "This idempotency key was already used for a different request."))
			case errors.Is(err, ErrIdempotencyInProgress):
				ginContext.Header("Retry-After", "1")
				problem.Write(ginContext, problem.New(http.StatusConflict, "idempotency_request_in_progress", "Request is in progress", "A request with this idempotency key is still being processed."))
			default:
				problem.Write(ginContext, problem.New(http.StatusServiceUnavailable, "idempotency_unavailable", "Request protection unavailable", "The request could not be safely claimed. Retry later with the same key."))
			}
			return
		}
		if claim.Replay != nil {
			writeStoredResponse(ginContext, *claim.Replay)
			return
		}
		if !claim.Acquired {
			problem.Write(ginContext, problem.New(http.StatusServiceUnavailable, "idempotency_unavailable", "Request protection unavailable", "The request could not be safely claimed."))
			return
		}

		capture := newCaptureWriter(ginContext.Writer, repository.config.MaxResponseBytes)
		ginContext.Writer = capture
		ginContext.Next()
		status := ginContext.Writer.Status()
		if status >= http.StatusInternalServerError {
			_ = repository.Release(context.WithoutCancel(ginContext.Request.Context()), scope, key, requestHash)
			return
		}
		stored := StoredResponse{Status: status, Headers: capture.Header().Clone(), Body: capture.Bytes()}
		if capture.Overflowed() {
			stored = replayUnavailableResponse()
		}
		_ = repository.Complete(context.WithoutCancel(ginContext.Request.Context()), scope, key, requestHash, stored)
	}
}

func idempotencyScope(ginContext *gin.Context, userID string) string {
	path := ginContext.FullPath()
	if path == "" {
		path = ginContext.Request.URL.Path
	}
	projectHeader := strings.TrimSpace(ginContext.GetHeader("X-Worksflow-Project-Id"))
	if projectHeader != "" {
		digest := sha256.Sum256([]byte(projectHeader))
		path += ":project:" + hex.EncodeToString(digest[:8])
	}
	return userID + ":" + ginContext.Request.Method + ":" + path
}

func hashIdempotentRequest(request *http.Request, userID string, body []byte) string {
	queryKeys := make([]string, 0, len(request.URL.Query()))
	for key := range request.URL.Query() {
		queryKeys = append(queryKeys, key)
	}
	sort.Strings(queryKeys)
	canonicalQuery := make([]string, 0, len(queryKeys))
	for _, key := range queryKeys {
		values := append([]string(nil), request.URL.Query()[key]...)
		sort.Strings(values)
		canonicalQuery = append(canonicalQuery, key+"="+strings.Join(values, ","))
	}
	payload := strings.Join([]string{
		request.Method, request.URL.Path, strings.Join(canonicalQuery, "&"), userID,
		strings.TrimSpace(request.Header.Get("Content-Type")),
		strings.TrimSpace(request.Header.Get("If-Match")),
		strings.TrimSpace(request.Header.Get("X-Worksflow-Project-Id")),
	}, "\n")
	digest := sha256.New()
	_, _ = digest.Write([]byte(payload))
	_, _ = digest.Write([]byte{'\n'})
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil))
}

func readRequestBody(ginContext *gin.Context, limit int64) ([]byte, error) {
	if ginContext.Request.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(ginContext.Request.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("request exceeds limit")
	}
	ginContext.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func safeReplayHeaders(source http.Header) http.Header {
	result := http.Header{}
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Location", "Retry-After", "Content-Language"} {
		for _, value := range source.Values(name) {
			result.Add(name, value)
		}
	}
	return result
}

func writeStoredResponse(ginContext *gin.Context, response StoredResponse) {
	for name, values := range safeReplayHeaders(response.Headers) {
		for _, value := range values {
			ginContext.Writer.Header().Add(name, value)
		}
	}
	ginContext.Header("Idempotency-Replayed", "true")
	ginContext.Status(response.Status)
	_, _ = ginContext.Writer.Write(response.Body)
}

func replayUnavailableResponse() StoredResponse {
	body := []byte(`{"type":"about:blank","title":"Stored response unavailable","status":409,"code":"idempotency_response_unavailable","detail":"The original operation completed, but its response exceeded the replay limit. Refresh the affected resource."}`)
	return StoredResponse{Status: http.StatusConflict, Headers: http.Header{"Content-Type": []string{"application/problem+json"}, "Cache-Control": []string{"no-store"}}, Body: body}
}

type captureWriter struct {
	gin.ResponseWriter
	body     bytes.Buffer
	limit    int
	overflow bool
}

func newCaptureWriter(writer gin.ResponseWriter, limit int) *captureWriter {
	return &captureWriter{ResponseWriter: writer, limit: limit}
}

func (w *captureWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *captureWriter) WriteString(value string) (int, error) {
	w.capture([]byte(value))
	return w.ResponseWriter.WriteString(value)
}

func (w *captureWriter) capture(data []byte) {
	if w.overflow || len(data) == 0 {
		return
	}
	remaining := w.limit - w.body.Len()
	if len(data) > remaining {
		if remaining > 0 {
			_, _ = w.body.Write(data[:remaining])
		}
		w.overflow = true
		return
	}
	_, _ = w.body.Write(data)
}

func (w *captureWriter) Bytes() []byte { return append([]byte(nil), w.body.Bytes()...) }

func (w *captureWriter) Overflowed() bool { return w.overflow }
