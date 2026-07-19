package agent

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
)

//go:embed schema/output.schema.json
var qualifiedOutputSchema []byte

func QualifiedOutputSchema() ([]byte, string, error) {
	if len(qualifiedOutputSchema) == 0 || !json.Valid(qualifiedOutputSchema) {
		return nil, "", errors.New("embedded Agent output schema is invalid")
	}
	digest := sha256.Sum256(qualifiedOutputSchema)
	return append([]byte(nil), qualifiedOutputSchema...), "sha256:" + hex.EncodeToString(digest[:]), nil
}
