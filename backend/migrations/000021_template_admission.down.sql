DROP TRIGGER IF EXISTS full_stack_template_release_immutable ON full_stack_template_releases;
DROP FUNCTION IF EXISTS prevent_full_stack_template_mutation();
DROP TRIGGER IF EXISTS full_stack_template_complete ON full_stack_template_releases;
DROP FUNCTION IF EXISTS validate_full_stack_template_complete();
DROP TRIGGER IF EXISTS full_stack_template_component_guard ON full_stack_template_components;
DROP FUNCTION IF EXISTS validate_full_stack_template_component();
DROP TABLE IF EXISTS full_stack_template_components;
DROP TABLE IF EXISTS full_stack_template_releases;

DROP TRIGGER IF EXISTS template_release_policy_guard ON template_release_policies;
DROP FUNCTION IF EXISTS validate_template_release_policy();
DROP TABLE IF EXISTS template_release_policies;

ALTER TABLE template_admission_attempts
  DROP CONSTRAINT IF EXISTS template_admission_approved_release_fk;
DROP TRIGGER IF EXISTS template_release_immutable ON template_releases;
DROP FUNCTION IF EXISTS prevent_template_release_mutation();
DROP TRIGGER IF EXISTS template_release_exact_admission ON template_releases;
DROP FUNCTION IF EXISTS validate_template_release_insert();
DROP TABLE IF EXISTS template_releases;

DROP TRIGGER IF EXISTS template_admission_attempt_guard ON template_admission_attempts;
DROP FUNCTION IF EXISTS validate_template_admission_attempt();
DROP TABLE IF EXISTS template_admission_attempts;

ALTER TABLE artifacts DROP CONSTRAINT artifacts_kind_check;
ALTER TABLE artifacts ADD CONSTRAINT artifacts_kind_check CHECK (kind IN (
  'project_brief', 'product_requirements', 'decision_record',
  'glossary_policy', 'reference_source', 'change_request',
  'requirement_baseline', 'blueprint', 'page_spec', 'prototype',
  'prototype_flow', 'fixture_bundle', 'design_system', 'token_set',
  'component_registry', 'api_contract', 'data_contract',
  'permission_contract', 'workspace', 'test_report', 'quality_report'
));
