package modelgovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	maxDocumentBytes = 4 << 20
	maxJSONDepth     = 64
	maxJSONNodes     = 100_000
)

// CanonicalModelProfileJSON validates and serializes a profile into the one
// accepted v1 wire representation.
func CanonicalModelProfileJSON(profile ModelProfile) ([]byte, error) {
	if err := ValidateModelProfile(profile); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical ModelProfile: %w", err)
	}
	return encoded, nil
}

func ModelProfileHash(profile ModelProfile) (string, error) {
	encoded, err := CanonicalModelProfileJSON(profile)
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

// ParseModelProfile accepts canonical bytes only and requires the caller's
// immutable content-hash fence. Pretty-printed or differently ordered JSON is
// intentionally rejected, even when it would decode to the same Go value.
func ParseModelProfile(encoded []byte, expectedHash string) (ModelProfile, error) {
	var profile ModelProfile
	if err := decodeStrictJSON(encoded, &profile); err != nil {
		return ModelProfile{}, fmt.Errorf("decode ModelProfile: %w", err)
	}
	canonical, err := CanonicalModelProfileJSON(profile)
	if err != nil {
		return ModelProfile{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return ModelProfile{}, errors.New("ModelProfile JSON is not in canonical wire form")
	}
	if err := requireExpectedHash(expectedHash, canonical, "ModelProfile"); err != nil {
		return ModelProfile{}, err
	}
	return profile, nil
}

func CanonicalFrozenCorpusJSON(corpus FrozenCorpus) ([]byte, error) {
	if err := ValidateFrozenCorpus(corpus); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(corpus)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical FrozenCorpus: %w", err)
	}
	return encoded, nil
}

func FrozenCorpusHash(corpus FrozenCorpus) (string, error) {
	encoded, err := CanonicalFrozenCorpusJSON(corpus)
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

func ParseFrozenCorpus(encoded []byte, expectedHash string) (FrozenCorpus, error) {
	var corpus FrozenCorpus
	if err := decodeStrictJSON(encoded, &corpus); err != nil {
		return FrozenCorpus{}, fmt.Errorf("decode FrozenCorpus: %w", err)
	}
	canonical, err := CanonicalFrozenCorpusJSON(corpus)
	if err != nil {
		return FrozenCorpus{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return FrozenCorpus{}, errors.New("FrozenCorpus JSON is not in canonical wire form")
	}
	if err := requireExpectedHash(expectedHash, canonical, "FrozenCorpus"); err != nil {
		return FrozenCorpus{}, err
	}
	return corpus, nil
}

func requireExpectedHash(expectedHash string, canonical []byte, document string) error {
	if !validDigest(expectedHash) {
		return fmt.Errorf("expected %s hash must be a canonical sha256 digest", document)
	}
	actual := sha256Digest(canonical)
	if actual != expectedHash {
		return fmt.Errorf("%s hash drift: expected %s, got %s", document, expectedHash, actual)
	}
	return nil
}

func sha256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func decodeStrictJSON(input []byte, target any) error {
	if len(input) == 0 {
		return errors.New("JSON document is empty")
	}
	if len(input) > maxDocumentBytes {
		return fmt.Errorf("JSON document exceeds %d bytes", maxDocumentBytes)
	}
	if !utf8.Valid(input) || bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) || bytes.ContainsRune(input, utf8.RuneError) {
		return errors.New("JSON must use valid BOM-free UTF-8 without replacement characters")
	}
	if err := rejectDuplicateJSONNames(input); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func rejectDuplicateJSONNames(input []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	nodes := 0
	if err := scanJSONValue(decoder, 0, &nodes); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder, depth int, nodes *int) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxJSONDepth)
	}
	*nodes = *nodes + 1
	if *nodes > maxJSONNodes {
		return fmt.Errorf("JSON document exceeds %d values", maxJSONNodes)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON object name %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("JSON document has trailing content")
	}
	return err
}
