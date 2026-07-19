package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func gatewayEditorLeaseInput(profile ProfileIdentity, owner string) GatewayEditorLeaseInput {
	return GatewayEditorLeaseInput{
		ProjectID: testProject, SessionID: testSession, ProfileID: profile.ID,
		ProfileContentHash: profile.ContentHash, CapabilityHash: profile.CapabilityHash,
		OwnerBindingID: owner,
	}
}

func TestRedisGatewayEditorLeaseConcurrentAcquireHasExactlyOneOwnerAndHashOnlyKey(t *testing.T) {
	client, rootPrefix := lspRedisCanary(t)
	prefix := rootPrefix + "editor-lease-concurrent:"
	store, err := NewRedisGatewayEditorLeaseStore(client, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if store.Contract() != (GatewayEditorLeaseContract{
		TTL: GatewayEditorLeaseTTL, HeartbeatInterval: GatewayEditorHeartbeatInterval,
	}) {
		t.Fatalf("production lease contract = %#v", store.Contract())
	}
	profile := lspTestProfile("typescript")
	const callers = 32
	var group sync.WaitGroup
	results := make(chan GatewayEditorLeaseInput, callers)
	for index := 0; index < callers; index++ {
		input := gatewayEditorLeaseInput(profile, uuid.NewString())
		group.Add(1)
		go func() {
			defer group.Done()
			acquired, acquireErr := store.AcquireGatewayEditorLease(context.Background(), input)
			if acquireErr == nil && acquired {
				results <- input
			}
		}()
	}
	group.Wait()
	close(results)
	owners := make([]GatewayEditorLeaseInput, 0, 1)
	for owner := range results {
		owners = append(owners, owner)
	}
	if len(owners) != 1 {
		t.Fatalf("concurrent owners = %d, want exactly one", len(owners))
	}
	keys, err := client.Keys(context.Background(), prefix+"*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("editor lease keys = %#v, %v", keys, err)
	}
	keyPrefix := prefix
	suffix := strings.TrimPrefix(keys[0], keyPrefix)
	decoded, decodeErr := hex.DecodeString(suffix)
	if !strings.HasPrefix(keys[0], keyPrefix) || decodeErr != nil || len(decoded) != sha256.Size {
		t.Fatalf("editor key is not one hash-only suffix: %q", keys[0])
	}
	storedOwner, err := client.Get(context.Background(), keys[0]).Result()
	if err != nil || storedOwner != gatewayEditorLeaseOwner(owners[0].OwnerBindingID) {
		t.Fatalf("stored owner is not the compare-owner digest: %q, %v", storedOwner, err)
	}
	for _, raw := range []string{
		testProject, testSession, profile.ID, profile.ContentHash,
		profile.CapabilityHash, owners[0].OwnerBindingID,
	} {
		if strings.Contains(keys[0], raw) || strings.Contains(storedOwner, raw) {
			t.Fatalf("Redis lease exposed raw authority identity %q", raw)
		}
	}
}

func TestRedisGatewayEditorLeaseTTLTakeoverFencesLateOldOwner(t *testing.T) {
	client, rootPrefix := lspRedisCanary(t)
	store, err := newRedisGatewayEditorLeaseStore(
		client, rootPrefix+"editor-lease-ttl:", GatewayEditorLeaseContract{
			TTL: 150 * time.Millisecond, HeartbeatInterval: 50 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile := lspTestProfile("typescript")
	oldOwner := gatewayEditorLeaseInput(profile, gatewayBindingID)
	newOwner := gatewayEditorLeaseInput(profile, gatewayServerMessageID)
	if acquired, err := store.AcquireGatewayEditorLease(context.Background(), oldOwner); err != nil || !acquired {
		t.Fatalf("old acquire = %v, %v", acquired, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		acquired, acquireErr := store.AcquireGatewayEditorLease(context.Background(), newOwner)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		if acquired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new owner did not take over after exact Redis TTL")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if renewed, err := store.RenewGatewayEditorLease(context.Background(), oldOwner); err != nil || renewed {
		t.Fatalf("late old renew = %v, %v", renewed, err)
	}
	if released, err := store.ReleaseGatewayEditorLease(context.Background(), oldOwner); err != nil || released {
		t.Fatalf("late old release = %v, %v", released, err)
	}
	if renewed, err := store.RenewGatewayEditorLease(context.Background(), newOwner); err != nil || !renewed {
		t.Fatalf("replacement owner was damaged by old owner = %v, %v", renewed, err)
	}
	if released, err := store.ReleaseGatewayEditorLease(context.Background(), newOwner); err != nil || !released {
		t.Fatalf("new release = %v, %v", released, err)
	}
	if renewed, err := store.RenewGatewayEditorLease(context.Background(), oldOwner); err != nil || renewed {
		t.Fatalf("old renew revived a missing key = %v, %v", renewed, err)
	}
}

func TestRedisGatewayEditorLeaseFailsClosedDuringOutage(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", MaxRetries: -1,
		DialTimeout: 25 * time.Millisecond, ReadTimeout: 25 * time.Millisecond,
		WriteTimeout: 25 * time.Millisecond, PoolTimeout: 25 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisGatewayEditorLeaseStore(client, "")
	if err != nil {
		t.Fatal(err)
	}
	input := gatewayEditorLeaseInput(lspTestProfile("typescript"), gatewayBindingID)
	operations := []func() (bool, error){
		func() (bool, error) { return store.AcquireGatewayEditorLease(context.Background(), input) },
		func() (bool, error) { return store.RenewGatewayEditorLease(context.Background(), input) },
		func() (bool, error) { return store.ReleaseGatewayEditorLease(context.Background(), input) },
	}
	for index, operation := range operations {
		if result, operationErr := operation(); result || !errors.Is(operationErr, ErrGatewaySecurityUnavailable) {
			t.Fatalf("outage operation %d = %v, %v", index, result, operationErr)
		}
	}
}
