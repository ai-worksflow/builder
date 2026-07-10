package postgres

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestApplicationBuildManifestModelMapsLineageColumns(t *testing.T) {
	t.Parallel()

	parsed, err := schema.Parse(&ApplicationBuildManifestModel{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		field   string
		column  string
		notNull bool
	}{
		{field: "RootManifestID", column: "root_manifest_id", notNull: true},
		{field: "DerivedFromID", column: "derived_from_id", notNull: false},
		{field: "WorkspaceRevisionID", column: "workspace_revision_id", notNull: false},
		{field: "RootOrdinal", column: "root_ordinal", notNull: false},
		{field: "ManifestGroupKey", column: "manifest_group_key", notNull: false},
	}
	for _, test := range tests {
		field := parsed.LookUpField(test.field)
		if field == nil {
			t.Fatalf("model is missing %s", test.field)
		}
		if field.DBName != test.column {
			t.Errorf("%s maps to %q, want %q", test.field, field.DBName, test.column)
		}
		if field.NotNull != test.notNull {
			t.Errorf("%s NotNull = %v, want %v", test.field, field.NotNull, test.notNull)
		}
	}
}
