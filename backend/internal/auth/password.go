package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

type PasswordParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultPasswordParams() PasswordParams {
	return PasswordParams{
		Memory: 64 * 1024, Iterations: 3, Parallelism: 2,
		SaltLength: 16, KeyLength: 32,
	}
}

type PasswordHasher struct {
	params PasswordParams
}

func NewPasswordHasher(params PasswordParams) (PasswordHasher, error) {
	if params.Memory < 8*1024 || params.Iterations < 1 || params.Parallelism < 1 ||
		params.SaltLength < 16 || params.KeyLength < 16 {
		return PasswordHasher{}, errors.New("unsafe Argon2id parameters")
	}
	return PasswordHasher{params: params}, nil
}

func (h PasswordHasher) Hash(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(
		[]byte(password), salt, h.params.Iterations, h.params.Memory,
		h.params.Parallelism, h.params.KeyLength,
	)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.Memory,
		h.params.Iterations,
		h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func (h PasswordHasher) Verify(password, encoded string) (bool, error) {
	params, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey(
		[]byte(password), salt, params.Iterations, params.Memory,
		params.Parallelism, uint32(len(expected)),
	)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parsePasswordHash(encoded string) (PasswordParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return PasswordParams{}, nil, nil, errors.New("invalid password hash format")
	}
	version, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v="))
	if err != nil || version != argon2.Version {
		return PasswordParams{}, nil, nil, errors.New("unsupported Argon2 version")
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return PasswordParams{}, nil, nil, errors.New("invalid Argon2 parameters")
	}
	if memory > 256*1024 || iterations > 10 || parallelism > 16 ||
		memory < 8*1024 || iterations < 1 || parallelism < 1 {
		return PasswordParams{}, nil, nil, errors.New("Argon2 parameters are outside safe verification limits")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return PasswordParams{}, nil, nil, errors.New("invalid password salt")
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return PasswordParams{}, nil, nil, errors.New("invalid password key")
	}
	return PasswordParams{
		Memory: memory, Iterations: iterations, Parallelism: parallelism,
		SaltLength: uint32(len(salt)), KeyLength: uint32(len(key)),
	}, salt, key, nil
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must contain at least 8 characters")
	}
	if len(password) > 1024 {
		return errors.New("password is too long")
	}
	return nil
}
