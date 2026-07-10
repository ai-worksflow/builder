package content

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCanonicalJSONAndHashAreStable(t *testing.T) {
	t.Parallel()

	first, err := canonicalJSON(json.RawMessage(`{"b":2,"a":{"d":4,"c":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalJSON(json.RawMessage(` { "a": { "c": 3, "d": 4 }, "b": 2 } `))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical values differ: %s != %s", first, second)
	}
	if contentHash(first) != contentHash(second) {
		t.Fatal("equivalent JSON must have the same hash")
	}
}

func TestCanonicalJSONRejectsInvalidPayload(t *testing.T) {
	t.Parallel()

	if _, err := canonicalJSON([]byte(`{"broken"`)); err == nil {
		t.Fatal("expected malformed JSON to fail")
	}
	if !errors.Is(ErrContentTooLarge, ErrContentTooLarge) {
		t.Fatal("sentinel errors must remain comparable")
	}
}
