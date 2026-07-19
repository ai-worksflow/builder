DROP TRIGGER IF EXISTS sandbox_terminal_event_chain_guard ON sandbox_terminal_events;
DROP TRIGGER IF EXISTS sandbox_terminal_parent_event_chain_guard ON sandbox_terminals;
DROP FUNCTION IF EXISTS validate_sandbox_terminal_event_chain();

DROP TRIGGER IF EXISTS sandbox_terminal_event_immutable ON sandbox_terminal_events;
DROP FUNCTION IF EXISTS prevent_sandbox_terminal_event_mutation();
DROP TRIGGER IF EXISTS sandbox_terminal_mutation_guard ON sandbox_terminals;
DROP FUNCTION IF EXISTS prevent_sandbox_terminal_mutation();
DROP TRIGGER IF EXISTS sandbox_terminal_event_advance ON sandbox_terminal_events;
DROP FUNCTION IF EXISTS advance_sandbox_terminal_from_event();
DROP TRIGGER IF EXISTS sandbox_terminal_event_guard ON sandbox_terminal_events;
DROP FUNCTION IF EXISTS validate_sandbox_terminal_event();
DROP TABLE IF EXISTS sandbox_terminal_events;

DROP TRIGGER IF EXISTS sandbox_terminal_insert_guard ON sandbox_terminals;
DROP FUNCTION IF EXISTS validate_sandbox_terminal_insert();
DROP TABLE IF EXISTS sandbox_terminals;
