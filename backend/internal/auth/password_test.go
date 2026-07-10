package auth

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	t.Parallel()
	hasher, err := NewPasswordHasher(PasswordParams{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	matched, err := hasher.Verify("correct horse battery staple", encoded)
	if err != nil || !matched {
		t.Fatalf("expected password to match: matched=%v err=%v", matched, err)
	}
	matched, err = hasher.Verify("incorrect password", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("incorrect password must not match")
	}
}

func TestPasswordValidation(t *testing.T) {
	t.Parallel()
	hasher, err := NewPasswordHasher(DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hasher.Hash("short"); err == nil {
		t.Fatal("expected short password to fail")
	}
	if _, _, _, err := parsePasswordHash("not-a-password-hash"); err == nil {
		t.Fatal("expected malformed password hash to fail")
	}
}
