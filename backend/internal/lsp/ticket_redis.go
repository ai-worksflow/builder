package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultTicketGrantPrefix = "worksflow:sandbox:lsp-ticket:"
	maxTicketGrantBytes      = 64 << 10
	maxTicketGrantTTL        = 30 * time.Second
)

var consumeTicketGrantScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then
  return false
end
redis.call('DEL', KEYS[1])
return value
`)

// RedisTicketGrantStore persists only short-lived, one-shot LSP ticket grants.
// Bearer secrets are never included in Redis keys or values; only their
// SHA-256 digest is used as the lookup key.
type RedisTicketGrantStore struct {
	client redis.UniversalClient
	prefix string
	now    func() time.Time
}

func NewRedisTicketGrantStore(
	client redis.UniversalClient,
	prefix string,
	now func() time.Time,
) (*RedisTicketGrantStore, error) {
	if client == nil || now == nil {
		return nil, fmt.Errorf("%w: Redis client and clock are required", ErrTicketUnavailable)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = defaultTicketGrantPrefix
	}
	if len(prefix) > 200 || strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, fmt.Errorf("%w: Redis prefix is invalid", ErrTicketInvalid)
	}
	return &RedisTicketGrantStore{client: client, prefix: prefix, now: now}, nil
}

func (store *RedisTicketGrantStore) Put(
	ctx context.Context,
	secret string,
	grant TicketGrant,
	ttl time.Duration,
) error {
	if store == nil || ctx == nil || !validTicketSecret(secret) || ttl <= 0 || ttl > maxTicketGrantTTL {
		return ErrTicketInvalid
	}
	now := store.now().UTC()
	if now.IsZero() || validateTicketGrant(grant, now) != nil {
		return ErrTicketInvalid
	}
	remaining := grant.ExpiresAt.Sub(now)
	if remaining <= 0 || remaining > ttl+time.Second {
		return ErrTicketInvalid
	}
	if remaining < ttl {
		ttl = remaining
	}
	encoded, err := json.Marshal(grant)
	if err != nil || len(encoded) == 0 || len(encoded) > maxTicketGrantBytes {
		return ErrTicketInvalid
	}
	stored, err := store.client.SetNX(ctx, store.key(secret), encoded, ttl).Result()
	if err != nil {
		return fmt.Errorf("%w: store grant: %v", ErrTicketUnavailable, err)
	}
	if !stored {
		return fmt.Errorf("%w: ticket secret collision", ErrTicketUnavailable)
	}
	return nil
}

func (store *RedisTicketGrantStore) Consume(
	ctx context.Context,
	secret string,
) (TicketGrant, error) {
	if store == nil || ctx == nil || !validTicketSecret(secret) {
		return TicketGrant{}, ErrTicketInvalid
	}
	value, err := consumeTicketGrantScript.Run(ctx, store.client, []string{store.key(secret)}).Result()
	if errors.Is(err, redis.Nil) {
		return TicketGrant{}, ErrTicketConsumed
	}
	if err != nil {
		return TicketGrant{}, fmt.Errorf("%w: consume grant: %v", ErrTicketUnavailable, err)
	}
	if value == nil || value == false {
		return TicketGrant{}, ErrTicketConsumed
	}
	encoded, ok := value.(string)
	if !ok {
		return TicketGrant{}, fmt.Errorf("%w: Redis grant has an invalid representation", ErrTicketUnavailable)
	}
	return decodePersistedTicketGrant([]byte(encoded), store.now().UTC())
}

func decodePersistedTicketGrant(encoded []byte, observedAt time.Time) (TicketGrant, error) {
	if observedAt.IsZero() || len(encoded) == 0 || len(encoded) > maxTicketGrantBytes {
		return TicketGrant{}, fmt.Errorf("%w: stored grant is outside its bounds", ErrTicketUnavailable)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var grant TicketGrant
	if err := decoder.Decode(&grant); err != nil {
		return TicketGrant{}, fmt.Errorf("%w: stored grant failed JSON validation", ErrTicketUnavailable)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return TicketGrant{}, fmt.Errorf("%w: stored grant contains trailing JSON", ErrTicketUnavailable)
	}
	reencoded, err := json.Marshal(grant)
	if err != nil || !bytes.Equal(encoded, reencoded) {
		return TicketGrant{}, fmt.Errorf("%w: stored grant is not canonical", ErrTicketUnavailable)
	}
	if validateTicketGrant(grant, time.Time{}) != nil {
		return TicketGrant{}, fmt.Errorf("%w: stored grant failed semantic validation", ErrTicketUnavailable)
	}
	if !grant.ExpiresAt.After(observedAt) {
		return TicketGrant{}, ErrTicketConsumed
	}
	return grant, nil
}

func (store *RedisTicketGrantStore) key(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return store.prefix + hex.EncodeToString(digest[:])
}

var _ TicketGrantStore = (*RedisTicketGrantStore)(nil)
