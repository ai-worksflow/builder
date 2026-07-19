DROP FUNCTION IF EXISTS transition_sandbox_session(uuid, bigint, bigint, text, uuid, text, uuid);
DROP FUNCTION IF EXISTS attach_sandbox_session_checkpoint(uuid, bigint, bigint, uuid, uuid);
DROP FUNCTION IF EXISTS sync_sandbox_session_candidate(uuid, bigint, bigint, uuid);

DROP TRIGGER IF EXISTS sandbox_session_event_chain_guard ON sandbox_session_transition_events;
DROP TRIGGER IF EXISTS sandbox_session_parent_event_chain_guard ON sandbox_sessions;
DROP FUNCTION IF EXISTS validate_sandbox_session_event_chain();

DROP TRIGGER IF EXISTS sandbox_session_transition_event_immutable ON sandbox_session_transition_events;
DROP FUNCTION IF EXISTS prevent_sandbox_session_event_mutation();
DROP TRIGGER IF EXISTS sandbox_session_mutation_guard ON sandbox_sessions;
DROP FUNCTION IF EXISTS prevent_sandbox_session_mutation();
DROP TRIGGER IF EXISTS sandbox_session_transition_advance ON sandbox_session_transition_events;
DROP FUNCTION IF EXISTS advance_sandbox_session_from_event();
DROP TRIGGER IF EXISTS sandbox_session_transition_event_guard ON sandbox_session_transition_events;
DROP FUNCTION IF EXISTS validate_sandbox_session_transition_event();
DROP TABLE IF EXISTS sandbox_session_transition_events;

DROP TRIGGER IF EXISTS sandbox_session_configuration_complete ON sandbox_sessions;
DROP FUNCTION IF EXISTS validate_sandbox_session_configuration_complete();
DROP TRIGGER IF EXISTS sandbox_session_port_immutable ON sandbox_session_ports;
DROP TRIGGER IF EXISTS sandbox_session_service_immutable ON sandbox_session_services;
DROP TRIGGER IF EXISTS sandbox_session_template_immutable ON sandbox_session_template_releases;
DROP FUNCTION IF EXISTS prevent_sandbox_session_configuration_mutation();
DROP TRIGGER IF EXISTS sandbox_session_port_insert_guard ON sandbox_session_ports;
DROP TRIGGER IF EXISTS sandbox_session_service_insert_guard ON sandbox_session_services;
DROP TRIGGER IF EXISTS sandbox_session_template_insert_guard ON sandbox_session_template_releases;
DROP FUNCTION IF EXISTS validate_sandbox_session_configuration_insert();
DROP TABLE IF EXISTS sandbox_session_ports;
DROP TABLE IF EXISTS sandbox_session_services;
DROP TABLE IF EXISTS sandbox_session_template_releases;

DROP TRIGGER IF EXISTS sandbox_session_insert_guard ON sandbox_sessions;
DROP FUNCTION IF EXISTS validate_sandbox_session_insert();
DROP FUNCTION IF EXISTS sandbox_checkpoint_is_exact(
  uuid, uuid, uuid, bigint, bigint, bigint, bigint, text, uuid, text, text, text
);
DROP TABLE IF EXISTS sandbox_sessions;
DROP FUNCTION IF EXISTS sandbox_profiles_are_valid(jsonb);
