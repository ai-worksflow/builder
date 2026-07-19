DROP TRIGGER IF EXISTS sandbox_runtime_process_event_chain_guard ON sandbox_runtime_process_events;
DROP TRIGGER IF EXISTS sandbox_runtime_process_parent_event_chain_guard ON sandbox_runtime_processes;
DROP FUNCTION IF EXISTS validate_sandbox_runtime_process_event_chain();

DROP TRIGGER IF EXISTS sandbox_runtime_process_event_immutable ON sandbox_runtime_process_events;
DROP FUNCTION IF EXISTS prevent_sandbox_runtime_process_event_mutation();
DROP TRIGGER IF EXISTS sandbox_runtime_process_mutation_guard ON sandbox_runtime_processes;
DROP FUNCTION IF EXISTS prevent_sandbox_runtime_process_mutation();
DROP TRIGGER IF EXISTS sandbox_runtime_process_event_advance ON sandbox_runtime_process_events;
DROP FUNCTION IF EXISTS advance_sandbox_runtime_process_from_event();
DROP TRIGGER IF EXISTS sandbox_runtime_process_event_guard ON sandbox_runtime_process_events;
DROP FUNCTION IF EXISTS validate_sandbox_runtime_process_event();
DROP TABLE IF EXISTS sandbox_runtime_process_events;

DROP TRIGGER IF EXISTS sandbox_runtime_process_insert_guard ON sandbox_runtime_processes;
DROP FUNCTION IF EXISTS validate_sandbox_runtime_process_insert();
DROP TABLE IF EXISTS sandbox_runtime_processes;
DROP FUNCTION IF EXISTS sandbox_process_argv_is_valid(jsonb);
