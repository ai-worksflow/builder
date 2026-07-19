DROP TRIGGER IF EXISTS agent_attempt_event_stream_enqueue ON agent_attempt_events;
DROP FUNCTION IF EXISTS enqueue_agent_attempt_stream_event();
DROP TABLE IF EXISTS agent_stream_outbox;

