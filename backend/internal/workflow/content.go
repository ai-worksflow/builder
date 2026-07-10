package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// InlineContentStore is a safe default for small aggregate payloads. Production
// deployments can inject Mongo/S3 while retaining the same hash verification.
type InlineContentStore struct{}

func (InlineContentStore) Put(_ context.Context, namespace, id string, content []byte) (string, string, string, error) {
	if namespace == "" || id == "" {
		return "", "", "", fmt.Errorf("content namespace and id are required")
	}
	digest := sha256.Sum256(content)
	return "inline", base64.RawURLEncoding.EncodeToString(content), hex.EncodeToString(digest[:]), nil
}

func (InlineContentStore) Get(_ context.Context, store, ref, expectedHash string) ([]byte, error) {
	if store != "inline" {
		return nil, fmt.Errorf("unsupported content store %q", store)
	}
	content, err := base64.RawURLEncoding.DecodeString(ref)
	if err != nil {
		return nil, fmt.Errorf("decode inline content: %w", err)
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != expectedHash {
		return nil, fmt.Errorf("content hash mismatch")
	}
	return content, nil
}
