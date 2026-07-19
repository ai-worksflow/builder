package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultConnectionTicketPrefix = "worksflow:sandbox:connection-ticket:"

var consumeConnectionTicketScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then
  return false
end
redis.call('DEL', KEYS[1])
return value
`)

type RedisConnectionTicketStore struct {
	client redis.UniversalClient
	prefix string
	now    func() time.Time
}

func NewRedisConnectionTicketStore(
	client redis.UniversalClient,
	prefix string,
	now func() time.Time,
) (*RedisConnectionTicketStore, error) {
	if client == nil || now == nil {
		return nil, fmt.Errorf("%w: Redis client and clock are required", ErrConnectionTicketUnavailable)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = defaultConnectionTicketPrefix
	}
	if len(prefix) > 200 || strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, fmt.Errorf("%w: Redis prefix is invalid", ErrConnectionTicketInvalid)
	}
	return &RedisConnectionTicketStore{client: client, prefix: prefix, now: now}, nil
}

func (store *RedisConnectionTicketStore) Put(
	ctx context.Context,
	secret string,
	grant ConnectionTicketGrant,
	ttl time.Duration,
) error {
	if store == nil || ctx == nil || !validConnectionTicketSecret(secret) || ttl <= 0 || ttl > 2*time.Minute {
		return ErrConnectionTicketInvalid
	}
	now := store.now().UTC()
	if err := validateConnectionTicketGrant(grant, now); err != nil || grant.ExpiresAt.Sub(now) > ttl+time.Second {
		return ErrConnectionTicketInvalid
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return fmt.Errorf("%w: encode grant: %v", ErrConnectionTicketUnavailable, err)
	}
	stored, err := store.client.SetNX(ctx, store.key(secret), encoded, ttl).Result()
	if err != nil {
		return fmt.Errorf("%w: store grant: %v", ErrConnectionTicketUnavailable, err)
	}
	if !stored {
		return fmt.Errorf("%w: ticket secret collision", ErrConnectionTicketUnavailable)
	}
	return nil
}

func (store *RedisConnectionTicketStore) Consume(
	ctx context.Context,
	secret string,
) (ConnectionTicketGrant, error) {
	if store == nil || ctx == nil || !validConnectionTicketSecret(secret) {
		return ConnectionTicketGrant{}, ErrConnectionTicketInvalid
	}
	value, err := consumeConnectionTicketScript.Run(ctx, store.client, []string{store.key(secret)}).Result()
	if errors.Is(err, redis.Nil) || value == nil || value == false {
		return ConnectionTicketGrant{}, ErrConnectionTicketConsumed
	}
	if err != nil {
		return ConnectionTicketGrant{}, fmt.Errorf("%w: consume grant: %v", ErrConnectionTicketUnavailable, err)
	}
	encoded, ok := value.(string)
	if !ok || encoded == "" {
		return ConnectionTicketGrant{}, fmt.Errorf("%w: Redis grant has an invalid representation", ErrConnectionTicketUnavailable)
	}
	var grant ConnectionTicketGrant
	decoderErr := json.Unmarshal([]byte(encoded), &grant)
	if decoderErr != nil || validateConnectionTicketGrant(grant, store.now().UTC()) != nil {
		return ConnectionTicketGrant{}, fmt.Errorf("%w: stored grant failed validation", ErrConnectionTicketUnavailable)
	}
	return grant, nil
}

func (store *RedisConnectionTicketStore) key(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return store.prefix + hex.EncodeToString(digest[:])
}

var _ ConnectionTicketStore = (*RedisConnectionTicketStore)(nil)
