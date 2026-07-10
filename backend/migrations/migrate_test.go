package migrations

import (
	"strings"
	"testing"
)

func TestMigrationFilesAreOrderedAndPaired(t *testing.T) {
	t.Parallel()

	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("expected at least one migration")
	}
	previous := ""
	for _, name := range names {
		if previous != "" && name <= previous {
			t.Fatalf("migrations are not strictly ordered: %q then %q", previous, name)
		}
		down := strings.TrimSuffix(name, ".up.sql") + ".down.sql"
		if _, err := files.ReadFile(down); err != nil {
			t.Fatalf("migration %s has no matching down file: %v", name, err)
		}
		previous = name
	}
}
