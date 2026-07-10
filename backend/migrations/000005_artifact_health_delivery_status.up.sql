ALTER TABLE artifact_health
  DROP CONSTRAINT artifact_health_delivery_check;

ALTER TABLE artifact_health
  ADD CONSTRAINT artifact_health_delivery_check CHECK (
    delivery_status IN (
      'incomplete',
      'ready_for_prototype',
      'ready_for_generation',
      'complete',
      'blocked'
    )
  );
