package content

import (
	"encoding/json"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/mongo"
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

func TestIsMissingIndexDropTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "collection missing", err: mongo.CommandError{Code: 26}, want: true},
		{name: "index missing", err: mongo.CommandError{Code: 27}, want: true},
		{name: "unauthorized", err: mongo.CommandError{Code: 13}},
		{name: "non-command error", err: errors.New("connection closed")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := isMissingIndexDropTarget(test.err); got != test.want {
				t.Fatalf("isMissingIndexDropTarget() = %v, want %v", got, test.want)
			}
		})
	}
}
