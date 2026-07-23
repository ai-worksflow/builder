package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisCredentialStoreScopesEncryptedCredentialToUser(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store, err := NewRedisCredentialStore(client, []byte("0123456789abcdef0123456789abcdef"), "test:github:")
	if err != nil {
		t.Fatal(err)
	}
	credential := Credential{
		Token: "github_pat_secret-value", User: User{ID: 7, Login: "noir"},
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := store.Set(context.Background(), "user-1", "project-a", credential, time.Hour); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "user-1", "project-b")
	if err != nil || stored.Token != credential.Token || stored.User.Login != "noir" {
		t.Fatalf("credential=%+v error=%v", stored, err)
	}
	raw, err := client.Get(context.Background(), "test:github:user-1").Bytes()
	if err != nil || strings.Contains(string(raw), credential.Token) {
		t.Fatalf("credential was not stored as an encrypted user secret: %v", err)
	}
	if err := store.Delete(context.Background(), "user-1", "project-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "user-1", "project-a"); err != ErrCredentialNotFound {
		t.Fatalf("deleted user credential remained available: %v", err)
	}
}
