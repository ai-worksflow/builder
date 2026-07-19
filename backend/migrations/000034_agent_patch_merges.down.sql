DROP TRIGGER IF EXISTS agent_patch_merge_application_immutable ON agent_patch_merge_applications;
DROP TRIGGER IF EXISTS agent_patch_merge_plan_immutable ON agent_patch_merge_plans;
DROP FUNCTION IF EXISTS prevent_agent_patch_merge_mutation();

DROP TRIGGER IF EXISTS agent_patch_merge_application_insert_guard ON agent_patch_merge_applications;
DROP FUNCTION IF EXISTS validate_agent_patch_merge_application_insert();
DROP TABLE IF EXISTS agent_patch_merge_applications;

DROP TRIGGER IF EXISTS agent_patch_merge_plan_insert_guard ON agent_patch_merge_plans;
DROP FUNCTION IF EXISTS validate_agent_patch_merge_plan_insert();
DROP INDEX IF EXISTS agent_patch_merge_candidate_idx;
DROP INDEX IF EXISTS agent_patch_merge_attempt_idx;
DROP TABLE IF EXISTS agent_patch_merge_plans;

ALTER TABLE agent_attempts DROP CONSTRAINT IF EXISTS agent_attempt_merge_lineage_unique;
DROP FUNCTION IF EXISTS agent_patch_merge_conflicts_are_valid(jsonb);
DROP FUNCTION IF EXISTS agent_patch_merge_operations_are_valid(jsonb);
