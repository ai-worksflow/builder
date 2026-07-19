package platform

import (
	"net/url"
	"testing"
)

func TestPostgresDSNWithSchemaUsesTheSeparateCanonicalSchema(t *testing.T) {
	raw := "postgres://api:secret@postgres:5432/worksflow?sslmode=verify-full"
	scoped, err := postgresDSNWithSchema(raw, "worksflow_app")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(scoped)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("search_path") != "worksflow_app" ||
		parsed.Query().Get("sslmode") != "verify-full" ||
		parsed.User.Username() != "api" {
		t.Fatalf("scoped PostgreSQL connection identity is incomplete")
	}
}
