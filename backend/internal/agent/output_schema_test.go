package agent

import "testing"

func TestQualifiedOutputSchemaHasStableCanonicalDigest(t *testing.T) {
	first, firstHash, err := QualifiedOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := QualifiedOutputSchema()
	if err != nil || len(first) == 0 || firstHash != secondHash || !sha256Pattern.MatchString(firstHash) {
		t.Fatalf("qualified schema hash: first=%q second=%q err=%v", firstHash, secondHash, err)
	}
	first[0] ^= 0xff
	if string(first) == string(second) {
		t.Fatal("QualifiedOutputSchema returned mutable shared bytes")
	}
}
