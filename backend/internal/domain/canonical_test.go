package domain

import (
	"errors"
	"testing"
)

func TestCanonicalJSONIsStableAcrossObjectOrder(t *testing.T) {
	first, err := CanonicalJSON([]byte(`{"z":1,"nested":{"b":2,"a":1},"a":[3,2,1]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalJSON([]byte(` { "a" : [3,2,1], "nested" : {"a":1,"b":2}, "z":1 } `))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical JSON differs:\n%s\n%s", first, second)
	}
	firstHash, _ := CanonicalHash(first)
	secondHash, _ := CanonicalHash(second)
	if firstHash != secondHash || !IsCanonicalHash(firstHash) {
		t.Fatalf("expected matching SHA-256 hashes, got %q and %q", firstHash, secondHash)
	}
}

func TestCanonicalJSONRejectsMultipleValues(t *testing.T) {
	_, err := CanonicalJSON([]byte(`{} {}`))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}
