DROP TABLE IF EXISTS data_public_capabilities;
DROP TABLE IF EXISTS data_public_table_policies;

ALTER TABLE deployment_versions
  DROP CONSTRAINT IF EXISTS deployment_versions_deployment_id_id_unique;

ALTER TABLE deployments
  DROP CONSTRAINT IF EXISTS deployments_project_id_id_unique;
