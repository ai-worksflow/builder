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

const defaultPreviewGrantPrefix = "worksflow:sandbox:preview-grant:"

type RedisPreviewGrantStore struct {
	client redis.UniversalClient
	prefix string
	now    func() time.Time
}

func NewRedisPreviewGrantStore(
	client redis.UniversalClient,
	prefix string,
	now func() time.Time,
) (*RedisPreviewGrantStore, error) {
	if client == nil || now == nil {
		return nil, ErrPreviewGrantUnavailable
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = defaultPreviewGrantPrefix
	}
	if len(prefix) > 200 || strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, ErrPreviewGrantInvalid
	}
	return &RedisPreviewGrantStore{client: client, prefix: prefix, now: now}, nil
}

func (store *RedisPreviewGrantStore) Put(
	ctx context.Context,
	secret string,
	grant PreviewGrant,
	ttl time.Duration,
) error {
	if store == nil || ctx == nil || !validPreviewSecret(secret) || ttl <= 0 || ttl > time.Hour {
		return ErrPreviewGrantInvalid
	}
	now := store.now().UTC()
	if err := validatePreviewGrant(grant, now); err != nil || grant.ExpiresAt.Sub(now) > ttl+time.Second {
		return ErrPreviewGrantInvalid
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return fmt.Errorf("%w: encode grant: %v", ErrPreviewGrantUnavailable, err)
	}
	stored, err := store.client.SetNX(ctx, store.key(secret), encoded, ttl).Result()
	if err != nil {
		return fmt.Errorf("%w: store grant: %v", ErrPreviewGrantUnavailable, err)
	}
	if !stored {
		return fmt.Errorf("%w: preview capability collision", ErrPreviewGrantUnavailable)
	}
	return nil
}

func (store *RedisPreviewGrantStore) Get(
	ctx context.Context,
	secret string,
) (PreviewGrant, error) {
	if store == nil || ctx == nil || !validPreviewSecret(secret) {
		return PreviewGrant{}, ErrPreviewGrantInvalid
	}
	encoded, err := store.client.Get(ctx, store.key(secret)).Bytes()
	if errors.Is(err, redis.Nil) {
		return PreviewGrant{}, ErrPreviewGrantExpired
	}
	if err != nil {
		return PreviewGrant{}, fmt.Errorf("%w: read grant: %v", ErrPreviewGrantUnavailable, err)
	}
	var grant PreviewGrant
	if err := json.Unmarshal(encoded, &grant); err != nil || validatePreviewGrant(grant, store.now().UTC()) != nil {
		return PreviewGrant{}, fmt.Errorf("%w: stored grant failed validation", ErrPreviewGrantUnavailable)
	}
	return grant, nil
}

func (store *RedisPreviewGrantStore) key(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return store.prefix + hex.EncodeToString(digest[:])
}

var _ PreviewGrantStore = (*RedisPreviewGrantStore)(nil)
