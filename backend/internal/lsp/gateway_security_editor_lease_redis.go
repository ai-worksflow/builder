package lsp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

const defaultGatewayEditorLeasePrefix = "worksflow:sandbox:lsp-editor-lease:"

type RedisGatewayEditorLeaseStore struct {
	client   redis.UniversalClient
	prefix   string
	contract GatewayEditorLeaseContract
}

// NewRedisGatewayEditorLeaseStore exposes one fixed production contract. The
// duration-taking constructor is private so production wiring cannot silently
// weaken the 30s lease / 10s heartbeat safety boundary.
func NewRedisGatewayEditorLeaseStore(
	client redis.UniversalClient,
	prefix string,
) (*RedisGatewayEditorLeaseStore, error) {
	return newRedisGatewayEditorLeaseStore(client, prefix, GatewayEditorLeaseContract{
		TTL: GatewayEditorLeaseTTL, HeartbeatInterval: GatewayEditorHeartbeatInterval,
	})
}

func newRedisGatewayEditorLeaseStore(
	client redis.UniversalClient,
	prefix string,
	contract GatewayEditorLeaseContract,
) (*RedisGatewayEditorLeaseStore, error) {
	if client == nil || validateGatewayEditorLeaseContract(contract) != nil {
		return nil, ErrGatewaySecurityUnavailable
	}
	if prefix == "" {
		prefix = defaultGatewayEditorLeasePrefix
	}
	if strings.TrimSpace(prefix) != prefix || len(prefix) > 200 ||
		strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, ErrGatewaySecurityUnavailable
	}
	return &RedisGatewayEditorLeaseStore{client: client, prefix: prefix, contract: contract}, nil
}

func (store *RedisGatewayEditorLeaseStore) Contract() GatewayEditorLeaseContract {
	if store == nil {
		return GatewayEditorLeaseContract{}
	}
	return store.contract
}

var gatewayEditorLeaseAcquireScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == false then
  local created = redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2])
  if created then
    return 1
  end
  current = redis.call('GET', KEYS[1])
end
if current == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0
`)

var gatewayEditorLeaseRenewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0
`)

var gatewayEditorLeaseReleaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

func (store *RedisGatewayEditorLeaseStore) AcquireGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (bool, error) {
	return store.runEditorLeaseScript(ctx, gatewayEditorLeaseAcquireScript, input, true)
}

func (store *RedisGatewayEditorLeaseStore) RenewGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (bool, error) {
	return store.runEditorLeaseScript(ctx, gatewayEditorLeaseRenewScript, input, true)
}

func (store *RedisGatewayEditorLeaseStore) ReleaseGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (bool, error) {
	return store.runEditorLeaseScript(ctx, gatewayEditorLeaseReleaseScript, input, false)
}

func (store *RedisGatewayEditorLeaseStore) runEditorLeaseScript(
	ctx context.Context,
	script *redis.Script,
	input GatewayEditorLeaseInput,
	withTTL bool,
) (bool, error) {
	if store == nil || store.client == nil || ctx == nil || script == nil ||
		validateGatewayEditorLeaseInput(input) != nil || validateGatewayEditorLeaseContract(store.contract) != nil {
		return false, ErrGatewaySecurityUnavailable
	}
	arguments := []any{gatewayEditorLeaseOwner(input.OwnerBindingID)}
	if withTTL {
		arguments = append(arguments, store.contract.TTL.Milliseconds())
	}
	result, err := script.Run(ctx, store.client, []string{store.editorLeaseKey(input)}, arguments...).Result()
	if err != nil {
		return false, errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	value, err := gatewayRateInteger(result)
	if err != nil || value != 0 && value != 1 {
		return false, ErrGatewaySecurityUnavailable
	}
	return value == 1, nil
}

// The Redis key is only prefix + one digest. It commits project, session, and
// the exact profile hashes, but deliberately excludes actor and owner.
func (store *RedisGatewayEditorLeaseStore) editorLeaseKey(input GatewayEditorLeaseInput) string {
	digest := sha256.New()
	for _, value := range []string{
		"sandbox-lsp-editor-lease/v1", input.ProjectID, input.SessionID,
		input.ProfileID, input.ProfileContentHash, input.CapabilityHash,
	} {
		_, _ = digest.Write([]byte(strconv.Itoa(len(value))))
		_, _ = digest.Write([]byte{':'})
		_, _ = digest.Write([]byte(value))
	}
	// A lease script touches exactly one key, so no Redis Cluster hash tag is
	// needed. Let the commitment digest distribute independent sessions across
	// slots instead of concentrating every tenant on one cluster shard.
	return fmt.Sprintf("%s%x", store.prefix, digest.Sum(nil))
}

// Redis also receives no raw binding owner. Lua compares this stable digest;
// an old owner therefore cannot reveal, delete, or revive a replacement owner.
func gatewayEditorLeaseOwner(bindingID string) string {
	digest := sha256.Sum256([]byte("sandbox-lsp-editor-owner/v1\x00" + bindingID))
	return fmt.Sprintf("%x", digest[:])
}

var _ GatewayEditorLeaseStore = (*RedisGatewayEditorLeaseStore)(nil)
