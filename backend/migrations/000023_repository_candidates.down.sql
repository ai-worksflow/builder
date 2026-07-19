DROP TRIGGER IF EXISTS candidate_snapshot_immutable ON candidate_snapshots;
DROP FUNCTION IF EXISTS prevent_candidate_snapshot_mutation();
DROP TRIGGER IF EXISTS candidate_snapshot_exact_tree_guard ON candidate_snapshots;
DROP FUNCTION IF EXISTS validate_candidate_snapshot_exact_tree();
ALTER TABLE IF EXISTS candidate_workspace_control_events
  DROP CONSTRAINT IF EXISTS candidate_workspace_control_event_snapshot_fk;

DROP TRIGGER IF EXISTS candidate_workspace_journal_immutable ON candidate_workspace_journal;
DROP FUNCTION IF EXISTS prevent_candidate_workspace_journal_mutation();
DROP TRIGGER IF EXISTS candidate_workspace_control_event_immutable ON candidate_workspace_control_events;
DROP FUNCTION IF EXISTS prevent_candidate_workspace_control_event_mutation();
DROP TRIGGER IF EXISTS candidate_workspace_journal_chain_guard ON candidate_workspace_journal;
DROP TRIGGER IF EXISTS candidate_workspace_journal_parent_guard ON candidate_workspaces;
DROP FUNCTION IF EXISTS validate_candidate_workspace_journal_chain();
DROP FUNCTION IF EXISTS abandon_candidate_workspace(uuid, bigint, bigint, bigint, uuid, uuid, text);
DROP FUNCTION IF EXISTS freeze_candidate_workspace(uuid, bigint, bigint, bigint, uuid, uuid, text);
DROP FUNCTION IF EXISTS update_candidate_workspace_flags(uuid, bigint, bigint, bigint, uuid, boolean, boolean, boolean, text, text, text);
DROP FUNCTION IF EXISTS rotate_candidate_workspace_session(uuid, bigint, bigint, uuid);
DROP FUNCTION IF EXISTS acquire_candidate_workspace_lease(uuid, bigint, uuid, integer);
DROP TRIGGER IF EXISTS candidate_workspace_mutation_guard ON candidate_workspaces;
DROP FUNCTION IF EXISTS prevent_candidate_workspace_mutation();
DROP TRIGGER IF EXISTS candidate_workspace_control_event_advance ON candidate_workspace_control_events;
DROP FUNCTION IF EXISTS advance_candidate_workspace_from_control_event();
DROP TRIGGER IF EXISTS candidate_workspace_control_event_guard ON candidate_workspace_control_events;
DROP FUNCTION IF EXISTS validate_candidate_workspace_control_event();
DROP TABLE IF EXISTS candidate_workspace_control_events;
DROP TABLE IF EXISTS candidate_snapshots CASCADE;
DROP TRIGGER IF EXISTS candidate_workspace_journal_advance ON candidate_workspace_journal;
DROP FUNCTION IF EXISTS advance_candidate_workspace_from_journal();
DROP TRIGGER IF EXISTS candidate_workspace_journal_append_guard ON candidate_workspace_journal;
DROP FUNCTION IF EXISTS validate_candidate_workspace_journal_append();
DROP TABLE IF EXISTS candidate_workspace_journal;

DROP TRIGGER IF EXISTS candidate_workspace_insert_guard ON candidate_workspaces;
DROP FUNCTION IF EXISTS validate_candidate_workspace_insert();
DROP TABLE IF EXISTS candidate_workspaces CASCADE;

DROP TRIGGER IF EXISTS repository_snapshot_immutable ON repository_snapshots;
DROP FUNCTION IF EXISTS prevent_repository_snapshot_mutation();
DROP TRIGGER IF EXISTS repository_snapshot_reference_guard ON repository_snapshots;
DROP FUNCTION IF EXISTS validate_repository_snapshot_reference();
DROP TABLE IF EXISTS repository_snapshots CASCADE;

DROP FUNCTION IF EXISTS repository_path_matches_template_policy(uuid, text, text, text);
DROP FUNCTION IF EXISTS repository_path_is_safe(text);
