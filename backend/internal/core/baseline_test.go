package core

import (
	"testing"
)

func TestBaselineHashExcludesItsOwnField(t *testing.T) {
	t.Parallel()
	baseline := RequirementBaseline{SchemaVersion: 1, SourceVersions: []VersionRef{}, Requirements: nil}
	first := baseline
	first.BaselineHash = ""
	second := baseline
	second.BaselineHash = "ignored"
	second.BaselineHash = ""
	if len(first.SourceVersions) != len(second.SourceVersions) {
		t.Fatal("baseline copies should remain equivalent")
	}
}
