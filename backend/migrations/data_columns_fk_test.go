package migrations

import (
	"strings"
	"testing"
)

func TestDataColumnsMigrationCascadesWithItsTable(t *testing.T) {
	t.Parallel()
	contents, err := files.ReadFile("000006_data_columns_table_fk.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	if !strings.Contains(sql, "foreign key (table_id) references data_tables(id) on delete cascade") {
		t.Fatalf("data column table ownership is not enforced: %s", sql)
	}
}
