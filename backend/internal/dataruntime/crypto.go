package dataruntime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ValueSealer deliberately exposes no Open/Decrypt operation. Data runtime
// HTTP and service layers therefore cannot accidentally turn stored variables
// into a secret-reading API.
type ValueSealer interface {
	Seal(plaintext []byte, associatedData string) ([]byte, error)
}

func ParseEncryptionKey(value string) ([]byte, error) {
	normalized := strings.TrimSpace(value)
	if decoded, err := hex.DecodeString(normalized); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(normalized); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("data runtime encryption key must encode exactly 32 bytes")
}

type AESGCMSealer struct {
	aead cipher.AEAD
	rand io.Reader
}

func NewAESGCMSealer(key []byte) (*AESGCMSealer, error) {
	if len(key) != 32 {
		return nil, errors.New("data runtime encryption key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(append([]byte(nil), key...))
	if err != nil {
		return nil, fmt.Errorf("create data runtime cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create data runtime GCM: %w", err)
	}
	return &AESGCMSealer{aead: aead, rand: rand.Reader}, nil
}

func (s *AESGCMSealer) Seal(plaintext []byte, associatedData string) ([]byte, error) {
	if s == nil || s.aead == nil {
		return nil, errors.New("data runtime sealer is not configured")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(s.rand, nonce); err != nil {
		return nil, fmt.Errorf("create data runtime nonce: %w", err)
	}
	// Envelope: version (uint16), nonce length (uint16), nonce, ciphertext/tag.
	result := make([]byte, 4, 4+len(nonce)+len(plaintext)+s.aead.Overhead())
	binary.BigEndian.PutUint16(result[0:2], 1)
	binary.BigEndian.PutUint16(result[2:4], uint16(len(nonce)))
	result = append(result, nonce...)
	result = s.aead.Seal(result, nonce, plaintext, []byte(associatedData))
	return result, nil
}
