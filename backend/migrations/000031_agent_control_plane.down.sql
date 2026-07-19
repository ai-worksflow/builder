DROP TRIGGER IF EXISTS agent_attempt_event_chain_guard ON agent_attempt_events;
DROP TRIGGER IF EXISTS agent_attempt_parent_event_chain_guard ON agent_attempts;
DROP FUNCTION IF EXISTS validate_agent_attempt_event_chain();

DROP TRIGGER IF EXISTS agent_attempt_event_immutable ON agent_attempt_events;
DROP FUNCTION IF EXISTS prevent_agent_attempt_event_mutation();
DROP TRIGGER IF EXISTS agent_attempt_mutation_guard ON agent_attempts;
DROP FUNCTION IF EXISTS prevent_agent_attempt_mutation();
DROP TRIGGER IF EXISTS agent_attempt_event_advance ON agent_attempt_events;
DROP FUNCTION IF EXISTS advance_agent_attempt_from_event();
DROP TRIGGER IF EXISTS agent_attempt_event_guard ON agent_attempt_events;
DROP FUNCTION IF EXISTS validate_agent_attempt_event();
DROP TABLE IF EXISTS agent_attempt_events;
DROP FUNCTION IF EXISTS agent_attempt_transition_is_valid(text, text);

DROP TRIGGER IF EXISTS agent_attempt_insert_guard ON agent_attempts;
DROP FUNCTION IF EXISTS validate_agent_attempt_insert();
DROP TABLE IF EXISTS agent_attempts;
DROP FUNCTION IF EXISTS agent_attempt_evidence_is_valid(jsonb);
DROP FUNCTION IF EXISTS agent_executor_identity_is_valid(jsonb);

DROP TRIGGER IF EXISTS agent_task_capsule_immutable ON agent_task_capsules;
DROP TRIGGER IF EXISTS agent_task_capsule_insert_guard ON agent_task_capsules;
DROP FUNCTION IF EXISTS validate_agent_task_capsule_insert();
DROP TABLE IF EXISTS agent_task_capsules;

DROP TRIGGER IF EXISTS agent_context_pack_immutable ON agent_context_packs;
DROP FUNCTION IF EXISTS prevent_agent_immutable_row_mutation();
DROP TRIGGER IF EXISTS agent_context_pack_insert_guard ON agent_context_packs;
DROP FUNCTION IF EXISTS validate_agent_context_pack_insert();
DROP TABLE IF EXISTS agent_context_packs;

DROP FUNCTION IF EXISTS agent_allowed_tools_are_valid(jsonb);
DROP FUNCTION IF EXISTS agent_task_budgets_are_valid(jsonb);
DROP FUNCTION IF EXISTS agent_network_policy_is_valid(jsonb);
DROP FUNCTION IF EXISTS agent_exact_references_are_valid(jsonb);
DROP FUNCTION IF EXISTS agent_context_items_are_valid(jsonb);
DROP FUNCTION IF EXISTS agent_exact_reference_is_valid(jsonb);
DROP FUNCTION IF EXISTS agent_blob_reference_is_valid(jsonb);
DROP FUNCTION IF EXISTS agent_path_sets_are_disjoint(jsonb, jsonb);
DROP FUNCTION IF EXISTS agent_path_array_is_valid(jsonb, integer, integer);
DROP FUNCTION IF EXISTS agent_json_string_array_is_valid(jsonb, integer, integer, integer);
