DROP TRIGGER IF EXISTS agent_patch_undo_application_immutable ON agent_patch_undo_applications;
DROP TRIGGER IF EXISTS agent_patch_undo_plan_immutable ON agent_patch_undo_plans;
DROP FUNCTION IF EXISTS prevent_agent_patch_undo_mutation();

DROP TRIGGER IF EXISTS agent_patch_undo_application_insert_guard ON agent_patch_undo_applications;
DROP FUNCTION IF EXISTS validate_agent_patch_undo_application_insert();
DROP TABLE IF EXISTS agent_patch_undo_applications;

DROP TRIGGER IF EXISTS agent_patch_undo_plan_insert_guard ON agent_patch_undo_plans;
DROP FUNCTION IF EXISTS validate_agent_patch_undo_plan_insert();
DROP INDEX IF EXISTS agent_patch_undo_candidate_idx;
DROP INDEX IF EXISTS agent_patch_undo_merge_idx;
DROP TABLE IF EXISTS agent_patch_undo_plans;
