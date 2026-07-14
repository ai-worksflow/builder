INSERT INTO artifact_health (
  artifact_id,
  sync_status,
  delivery_status,
  finding_count,
  blocking_count,
  report,
  computed_at
)
SELECT
  artifacts.id,
  'current',
  'incomplete',
  0,
  0,
  '{}'::jsonb,
  now()
FROM artifacts
LEFT JOIN artifact_health
  ON artifact_health.artifact_id = artifacts.id
WHERE artifact_health.artifact_id IS NULL
ON CONFLICT (artifact_id) DO NOTHING;
