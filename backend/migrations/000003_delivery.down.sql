DROP TABLE IF EXISTS deployment_logs;
ALTER TABLE IF EXISTS deployments DROP CONSTRAINT IF EXISTS deployments_active_version_fk;
DROP TABLE IF EXISTS deployment_versions;
DROP TABLE IF EXISTS deployments;
DROP TABLE IF EXISTS quality_diagnostics;
DROP TABLE IF EXISTS quality_runs;
