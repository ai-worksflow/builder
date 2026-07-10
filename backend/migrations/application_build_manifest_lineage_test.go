package migrations

import (
	"strings"
	"testing"
)

func TestApplicationBuildManifestLineageMigrationPinsRootParentAndWorkspace(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000012_application_build_manifest_lineage.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000012_application_build_manifest_lineage.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upSQL := strings.ToLower(string(up))
	for _, required := range []string{
		"add column root_manifest_id uuid",
		"add column derived_from_id uuid",
		"add column workspace_revision_id uuid",
		"add column root_ordinal integer",
		"add column manifest_group_key text",
		"set root_manifest_id = id",
		"alter column root_manifest_id set not null",
		"unique (id, project_id)",
		"unique (id, root_manifest_id, project_id)",
		"foreign key (root_manifest_id, project_id)",
		"references application_build_manifests (id, project_id)",
		"foreign key (derived_from_id, root_manifest_id, project_id)",
		"references application_build_manifests (id, root_manifest_id, project_id)",
		"foreign key (workspace_revision_id)",
		"references artifact_revisions (id)",
		"root_manifest_id = id and derived_from_id is null",
		"root_manifest_id <> id",
		"derived_from_id is not null",
		"derived_from_id <> id",
		"application_build_manifests_root_history_idx",
		"create unique index application_build_manifests_derived_from_unique",
		"create unique index application_build_manifests_root_workspace_unique",
		"on application_build_manifests (root_manifest_id, workspace_revision_id)",
		"where workspace_revision_id is not null",
		"application_build_manifests_root_ordinal_nonnegative",
		"application_build_manifests_workflow_group_shape_check",
		"foreign key (workflow_run_id, project_id)",
		"references workflow_runs (id, project_id)",
		"manifest_group_key = 'legacy'",
		"row_number() over",
		"create unique index application_build_manifests_run_root_ordinal_unique",
		"on application_build_manifests (project_id, workflow_run_id, manifest_group_key, root_ordinal)",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("build manifest lineage migration is missing %q", required)
		}
	}

	if got := strings.Count(upSQL, "on delete restrict"); got != 4 {
		t.Fatalf("all lineage inputs and workflow ownership must be immutable; got %d RESTRICT foreign keys", got)
	}
	backfill := strings.Index(upSQL, "set root_manifest_id = id")
	notNull := strings.Index(upSQL, "alter column root_manifest_id set not null")
	if backfill < 0 || notNull < 0 || backfill > notNull {
		t.Fatal("legacy manifests must be backfilled as roots before root_manifest_id becomes NOT NULL")
	}

	downSQL := strings.ToLower(string(down))
	for _, required := range []string{
		"drop index if exists application_build_manifests_root_workspace_unique",
		"drop index if exists application_build_manifests_run_root_ordinal_unique",
		"drop index if exists application_build_manifests_derived_from_unique",
		"drop index if exists application_build_manifests_root_history_idx",
		"drop constraint if exists application_build_manifests_lineage_shape_check",
		"drop constraint if exists application_build_manifests_workspace_revision_fk",
		"drop constraint if exists application_build_manifests_workflow_run_project_fk",
		"drop constraint if exists application_build_manifests_derived_from_fk",
		"drop constraint if exists application_build_manifests_root_fk",
		"drop column if exists workspace_revision_id",
		"drop column if exists root_ordinal",
		"drop column if exists manifest_group_key",
		"drop column if exists derived_from_id",
		"drop column if exists root_manifest_id",
	} {
		if !strings.Contains(downSQL, required) {
			t.Errorf("build manifest lineage rollback is missing %q", required)
		}
	}
}
