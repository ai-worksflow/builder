package github

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/redis/go-redis/v9"
)

type Credential struct {
	Token     string    `json:"token"`
	User      User      `json:"user"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type CredentialStore interface {
	Get(context.Context, string, string) (Credential, error)
	Set(context.Context, string, string, Credential, time.Duration) error
	Delete(context.Context, string, string) error
}

var ErrCredentialNotFound = errors.New("GitHub credential was not found")

type RedisCredentialStore struct {
	redis  redis.UniversalClient
	aead   cipher.AEAD
	prefix string
}

func NewRedisCredentialStore(client redis.UniversalClient, encryptionKey []byte, prefix string) (*RedisCredentialStore, error) {
	if client == nil || len(encryptionKey) != 32 {
		return nil, errors.New("GitHub Redis store and 32-byte encryption key are required")
	}
	block, err := aes.NewCipher(append([]byte(nil), encryptionKey...))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		prefix = "worksflow:github:credential:"
	}
	return &RedisCredentialStore{redis: client, aead: aead, prefix: prefix}, nil
}

func (s *RedisCredentialStore) key(userID, projectID string) string {
	return s.prefix + userID + ":" + projectID
}
func credentialAAD(userID, projectID string) []byte { return []byte(userID + "\x00" + projectID) }

func (s *RedisCredentialStore) Get(ctx context.Context, userID, projectID string) (Credential, error) {
	encoded, err := s.redis.Get(ctx, s.key(userID, projectID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Credential{}, ErrCredentialNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("load GitHub credential: %w", err)
	}
	if len(encoded) <= s.aead.NonceSize() {
		return Credential{}, errors.New("stored GitHub credential is malformed")
	}
	nonce := encoded[:s.aead.NonceSize()]
	plaintext, err := s.aead.Open(nil, nonce, encoded[s.aead.NonceSize():], credentialAAD(userID, projectID))
	if err != nil {
		return Credential{}, errors.New("stored GitHub credential could not be authenticated")
	}
	var credential Credential
	if err := json.Unmarshal(plaintext, &credential); err != nil {
		return Credential{}, errors.New("stored GitHub credential is malformed")
	}
	if credential.Token == "" || credential.User.ID < 1 || !credential.ExpiresAt.After(time.Now().UTC()) {
		_ = s.Delete(context.WithoutCancel(ctx), userID, projectID)
		return Credential{}, ErrCredentialNotFound
	}
	return credential, nil
}

func (s *RedisCredentialStore) Set(ctx context.Context, userID, projectID string, credential Credential, ttl time.Duration) error {
	if ttl <= 0 || credential.Token == "" {
		return errors.New("positive GitHub credential TTL and token are required")
	}
	plaintext, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	envelope := append([]byte(nil), nonce...)
	envelope = s.aead.Seal(envelope, nonce, plaintext, credentialAAD(userID, projectID))
	if err := s.redis.Set(ctx, s.key(userID, projectID), envelope, ttl).Err(); err != nil {
		return fmt.Errorf("store GitHub credential: %w", err)
	}
	return nil
}

func (s *RedisCredentialStore) Delete(ctx context.Context, userID, projectID string) error {
	if err := s.redis.Del(ctx, s.key(userID, projectID)).Err(); err != nil {
		return fmt.Errorf("delete GitHub credential: %w", err)
	}
	return nil
}
