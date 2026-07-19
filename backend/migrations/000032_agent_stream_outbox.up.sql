CREATE TABLE agent_stream_outbox (
  attempt_id uuid NOT NULL,
  event_sequence bigint NOT NULL CHECK (event_sequence > 0),
  available_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  claim_token uuid,
  claimed_until timestamptz,
  delivery_attempts integer NOT NULL DEFAULT 0 CHECK (delivery_attempts >= 0),
  delivered_at timestamptz,
  stream_sequence bigint CHECK (stream_sequence IS NULL OR stream_sequence > 0),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  PRIMARY KEY (attempt_id, event_sequence),
  CONSTRAINT agent_stream_outbox_event_fk
    FOREIGN KEY (attempt_id, event_sequence)
    REFERENCES agent_attempt_events(attempt_id, sequence)
    ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT agent_stream_outbox_claim_shape CHECK (
    (claim_token IS NULL AND claimed_until IS NULL)
    OR (claim_token IS NOT NULL AND claimed_until IS NOT NULL)
  ),
  CONSTRAINT agent_stream_outbox_delivery_shape CHECK (
    (delivered_at IS NULL AND stream_sequence IS NULL)
    OR (delivered_at IS NOT NULL AND stream_sequence IS NOT NULL
        AND claim_token IS NULL AND claimed_until IS NULL AND last_error IS NULL)
  ),
  CONSTRAINT agent_stream_outbox_error_bound CHECK (
    last_error IS NULL OR length(last_error) BETWEEN 1 AND 2000
  )
);

CREATE INDEX agent_stream_outbox_pending_idx
  ON agent_stream_outbox (available_at, attempt_id, event_sequence)
  WHERE delivered_at IS NULL;

CREATE OR REPLACE FUNCTION enqueue_agent_attempt_stream_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO agent_stream_outbox (attempt_id, event_sequence)
  VALUES (NEW.attempt_id, NEW.sequence)
  ON CONFLICT (attempt_id, event_sequence) DO NOTHING;
  RETURN NULL;
END;
$$;

CREATE TRIGGER agent_attempt_event_stream_enqueue
AFTER INSERT ON agent_attempt_events
FOR EACH ROW EXECUTE FUNCTION enqueue_agent_attempt_stream_event();

-- Backfill defensively so an interrupted rolling deployment cannot leave
-- already-committed immutable events outside the retained Session stream.
INSERT INTO agent_stream_outbox (attempt_id, event_sequence, created_at, updated_at)
SELECT attempt_id, sequence, created_at, statement_timestamp()
FROM agent_attempt_events
ON CONFLICT (attempt_id, event_sequence) DO NOTHING;
