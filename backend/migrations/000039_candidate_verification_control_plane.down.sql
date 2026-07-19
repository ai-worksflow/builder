DROP TRIGGER IF EXISTS candidate_verification_attempt_transition_guard ON candidate_verification_attempts;
DROP FUNCTION IF EXISTS guard_candidate_verification_attempt_transition();
DROP TRIGGER IF EXISTS candidate_verification_attempt_insert_guard ON candidate_verification_attempts;
DROP FUNCTION IF EXISTS validate_candidate_verification_attempt_insert();
DROP TABLE IF EXISTS candidate_verification_attempts;

DROP TRIGGER IF EXISTS candidate_verification_run_transition_guard ON candidate_verification_runs;
DROP FUNCTION IF EXISTS guard_candidate_verification_run_transition();
DROP TRIGGER IF EXISTS candidate_verification_run_insert_guard ON candidate_verification_runs;
DROP FUNCTION IF EXISTS validate_candidate_verification_run_insert();
DROP TABLE IF EXISTS candidate_verification_runs;

DROP TRIGGER IF EXISTS candidate_verification_plan_immutable ON candidate_verification_plans;
DROP FUNCTION IF EXISTS prevent_candidate_verification_plan_mutation();
DROP TRIGGER IF EXISTS candidate_verification_plan_insert_guard ON candidate_verification_plans;
DROP FUNCTION IF EXISTS validate_candidate_verification_plan_insert();
DROP TABLE IF EXISTS candidate_verification_plans;

DROP FUNCTION IF EXISTS verification_obligations_are_valid(jsonb);
DROP FUNCTION IF EXISTS verification_template_releases_are_valid(jsonb);

DROP TRIGGER IF EXISTS verification_profile_policy_guard ON verification_profile_policies;
DROP FUNCTION IF EXISTS guard_verification_profile_policy();
DROP TABLE IF EXISTS verification_profile_policies;

DROP TRIGGER IF EXISTS verification_profile_version_immutable ON verification_profile_versions;
DROP FUNCTION IF EXISTS prevent_verification_profile_version_mutation();
DROP TRIGGER IF EXISTS verification_profile_version_insert_guard ON verification_profile_versions;
DROP FUNCTION IF EXISTS validate_verification_profile_version_insert();
DROP TABLE IF EXISTS verification_profile_versions;

DROP FUNCTION IF EXISTS verification_profile_images_are_valid(jsonb);
DROP FUNCTION IF EXISTS verification_string_array_is_subset(jsonb, jsonb);
DROP FUNCTION IF EXISTS verification_json_string_array_is_valid(jsonb, integer, integer, integer);
DROP FUNCTION IF EXISTS verification_normalize_sha256(text);
