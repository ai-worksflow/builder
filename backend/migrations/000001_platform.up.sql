CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email text NOT NULL,
  display_name text NOT NULL,
  password_hash text NOT NULL,
  avatar_url text,
  disabled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT users_email_normalized CHECK (email = lower(trim(email)))
);

CREATE UNIQUE INDEX users_email_unique ON users (email);

CREATE TABLE auth_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  user_agent text,
  ip_address inet,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX auth_sessions_token_hash_unique ON auth_sessions (token_hash);
CREATE INDEX auth_sessions_user_active_idx ON auth_sessions (user_id, expires_at)
  WHERE revoked_at IS NULL;

CREATE TABLE projects (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  lifecycle text NOT NULL DEFAULT 'active',
  version bigint NOT NULL DEFAULT 1,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  CONSTRAINT projects_lifecycle_check CHECK (lifecycle IN ('active', 'archived'))
);

CREATE UNIQUE INDEX projects_slug_unique ON projects (slug) WHERE slug IS NOT NULL;
CREATE INDEX projects_updated_idx ON projects (updated_at DESC);

CREATE TABLE project_members (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL,
  invited_by uuid REFERENCES users(id),
  joined_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id),
  CONSTRAINT project_members_role_check
    CHECK (role IN ('owner', 'admin', 'editor', 'commenter', 'viewer'))
);

CREATE INDEX project_members_user_idx ON project_members (user_id, project_id);

CREATE TABLE project_invitations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  email text NOT NULL,
  role text NOT NULL,
  token_hash bytea NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  invited_by uuid NOT NULL REFERENCES users(id),
  accepted_by uuid REFERENCES users(id),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  accepted_at timestamptz,
  revoked_at timestamptz,
  CONSTRAINT project_invitations_role_check
    CHECK (role IN ('admin', 'editor', 'commenter', 'viewer')),
  CONSTRAINT project_invitations_status_check
    CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
  CONSTRAINT project_invitations_email_normalized CHECK (email = lower(trim(email)))
);

CREATE UNIQUE INDEX project_invitations_token_unique ON project_invitations (token_hash);
CREATE UNIQUE INDEX project_invitations_pending_email_unique
  ON project_invitations (project_id, email) WHERE status = 'pending';

CREATE TABLE artifacts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind text NOT NULL,
  artifact_key text NOT NULL,
  title text NOT NULL,
  lifecycle text NOT NULL DEFAULT 'active',
  version bigint NOT NULL DEFAULT 1,
  latest_draft_id uuid,
  latest_revision_id uuid,
  latest_approved_revision_id uuid,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  CONSTRAINT artifacts_kind_check CHECK (kind IN (
    'project_brief', 'product_requirements', 'decision_record',
    'glossary_policy', 'reference_source', 'change_request',
    'requirement_baseline', 'blueprint', 'page_spec', 'prototype',
    'prototype_flow', 'fixture_bundle', 'design_system', 'token_set',
    'component_registry', 'api_contract', 'data_contract',
    'permission_contract', 'workspace', 'test_report', 'quality_report'
  )),
  CONSTRAINT artifacts_lifecycle_check CHECK (lifecycle IN ('active', 'archived')),
  CONSTRAINT artifacts_key_nonempty CHECK (length(trim(artifact_key)) > 0)
);

CREATE UNIQUE INDEX artifacts_project_key_unique ON artifacts (project_id, artifact_key);
CREATE INDEX artifacts_project_kind_idx ON artifacts (project_id, kind, updated_at DESC);

CREATE TABLE artifact_revisions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  revision_number bigint NOT NULL,
  parent_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  schema_version integer NOT NULL,
  content_store text NOT NULL DEFAULT 'mongo',
  content_ref text NOT NULL,
  content_hash text NOT NULL,
  byte_size bigint NOT NULL DEFAULT 0,
  workflow_status text NOT NULL DEFAULT 'draft',
  change_source text NOT NULL,
  change_summary text NOT NULL DEFAULT '',
  source_manifest_id uuid,
  proposal_id uuid,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  approved_at timestamptz,
  superseded_at timestamptz,
  CONSTRAINT artifact_revisions_number_positive CHECK (revision_number > 0),
  CONSTRAINT artifact_revisions_content_hash_nonempty CHECK (length(content_hash) > 0),
  CONSTRAINT artifact_revisions_workflow_status_check CHECK (
    workflow_status IN ('draft', 'in_review', 'changes_requested', 'approved', 'superseded')
  ),
  CONSTRAINT artifact_revisions_change_source_check CHECK (
    change_source IN ('human', 'ai_proposal', 'import', 'merge', 'rollback', 'system')
  ),
  UNIQUE (artifact_id, revision_number),
  UNIQUE (artifact_id, content_hash)
);

CREATE INDEX artifact_revisions_artifact_created_idx
  ON artifact_revisions (artifact_id, revision_number DESC);
CREATE INDEX artifact_revisions_manifest_idx ON artifact_revisions (source_manifest_id)
  WHERE source_manifest_id IS NOT NULL;

CREATE TABLE artifact_drafts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  base_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  sequence bigint NOT NULL DEFAULT 1,
  etag text NOT NULL,
  schema_version integer NOT NULL,
  content_store text NOT NULL DEFAULT 'mongo',
  content_ref text NOT NULL,
  content_hash text NOT NULL,
  byte_size bigint NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'draft',
  created_by uuid NOT NULL REFERENCES users(id),
  updated_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT artifact_drafts_sequence_positive CHECK (sequence > 0),
  CONSTRAINT artifact_drafts_status_check
    CHECK (status IN ('draft', 'submitted', 'abandoned')),
  UNIQUE (artifact_id, id),
  UNIQUE (artifact_id, etag)
);

CREATE INDEX artifact_drafts_artifact_updated_idx ON artifact_drafts (artifact_id, updated_at DESC);

CREATE TABLE artifact_draft_sources (
  draft_id uuid NOT NULL REFERENCES artifact_drafts(id) ON DELETE CASCADE,
  source_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  source_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  source_content_hash text NOT NULL,
  source_anchor_id text,
  purpose text NOT NULL,
  required boolean NOT NULL DEFAULT true,
  added_by uuid NOT NULL REFERENCES users(id),
  added_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (draft_id, source_revision_id, purpose)
);

CREATE INDEX artifact_draft_sources_source_idx
  ON artifact_draft_sources (source_artifact_id, source_revision_id);

ALTER TABLE artifacts
  ADD CONSTRAINT artifacts_latest_draft_fk
    FOREIGN KEY (latest_draft_id) REFERENCES artifact_drafts(id) ON DELETE SET NULL,
  ADD CONSTRAINT artifacts_latest_revision_fk
    FOREIGN KEY (latest_revision_id) REFERENCES artifact_revisions(id) ON DELETE SET NULL,
  ADD CONSTRAINT artifacts_approved_revision_fk
    FOREIGN KEY (latest_approved_revision_id) REFERENCES artifact_revisions(id) ON DELETE SET NULL;

CREATE TABLE artifact_responsibilities (
  artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  responsibility text NOT NULL,
  reason text NOT NULL DEFAULT '',
  assigned_by uuid NOT NULL REFERENCES users(id),
  assigned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (artifact_id, user_id, responsibility),
  CONSTRAINT artifact_responsibility_check CHECK (
    responsibility IN ('owner', 'assignee', 'downstream_owner', 'reviewer', 'watcher')
  )
);

CREATE TABLE artifact_dependencies (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  source_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  source_content_hash text NOT NULL,
  target_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  target_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  relation text NOT NULL,
  required boolean NOT NULL DEFAULT true,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT artifact_dependencies_distinct CHECK (source_artifact_id <> target_artifact_id),
  UNIQUE (source_revision_id, target_artifact_id, target_revision_id, relation)
);

CREATE INDEX artifact_dependencies_source_idx
  ON artifact_dependencies (source_artifact_id, source_revision_id);
CREATE INDEX artifact_dependencies_target_idx
  ON artifact_dependencies (target_artifact_id, target_revision_id);

CREATE TABLE trace_links (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  source_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  source_anchor_id text,
  target_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  target_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  target_anchor_id text,
  relation text NOT NULL,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX trace_links_source_idx
  ON trace_links (project_id, source_artifact_id, source_revision_id, source_anchor_id);
CREATE INDEX trace_links_target_idx
  ON trace_links (project_id, target_artifact_id, target_revision_id, target_anchor_id);

CREATE TABLE artifact_health (
  artifact_id uuid PRIMARY KEY REFERENCES artifacts(id) ON DELETE CASCADE,
  sync_status text NOT NULL DEFAULT 'current',
  delivery_status text NOT NULL DEFAULT 'incomplete',
  finding_count integer NOT NULL DEFAULT 0,
  blocking_count integer NOT NULL DEFAULT 0,
  report jsonb NOT NULL DEFAULT '{}'::jsonb,
  computed_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT artifact_health_sync_check CHECK (sync_status IN ('current', 'needs_sync', 'blocked')),
  CONSTRAINT artifact_health_delivery_check CHECK (
    delivery_status IN ('incomplete', 'ready_for_prototype', 'ready_for_generation')
  )
);

CREATE TABLE review_requests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  content_hash text NOT NULL,
  status text NOT NULL DEFAULT 'open',
  policy jsonb NOT NULL DEFAULT '{}'::jsonb,
  requested_by uuid NOT NULL REFERENCES users(id),
  requested_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz,
  CONSTRAINT review_requests_status_check
    CHECK (status IN ('open', 'approved', 'changes_requested', 'withdrawn', 'stale'))
);

CREATE UNIQUE INDEX review_requests_open_revision_unique
  ON review_requests (revision_id) WHERE status = 'open';

CREATE TABLE review_decisions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  review_request_id uuid NOT NULL REFERENCES review_requests(id) ON DELETE CASCADE,
  reviewer_id uuid NOT NULL REFERENCES users(id),
  decision text NOT NULL,
  summary text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT review_decisions_check CHECK (decision IN ('approve', 'request_changes')),
  UNIQUE (review_request_id, reviewer_id)
);

CREATE TABLE comment_threads (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  revision_id uuid REFERENCES artifact_revisions(id) ON DELETE SET NULL,
  anchor jsonb NOT NULL DEFAULT '{}'::jsonb,
  severity text NOT NULL DEFAULT 'normal',
  assigned_to uuid REFERENCES users(id),
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_by uuid REFERENCES users(id),
  resolved_at timestamptz,
  outdated_at timestamptz,
  CONSTRAINT comment_threads_severity_check CHECK (severity IN ('normal', 'blocking'))
);

CREATE INDEX comment_threads_scope_idx
  ON comment_threads (artifact_id, revision_id, created_at DESC);

CREATE TABLE comment_messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  thread_id uuid NOT NULL REFERENCES comment_threads(id) ON DELETE CASCADE,
  parent_message_id uuid REFERENCES comment_messages(id) ON DELETE CASCADE,
  body text NOT NULL,
  mentions jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  edited_at timestamptz,
  deleted_at timestamptz,
  CONSTRAINT comment_messages_body_nonempty CHECK (length(trim(body)) > 0)
);

CREATE TABLE input_manifests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind text NOT NULL,
  schema_version integer NOT NULL,
  content_store text NOT NULL DEFAULT 'mongo',
  content_ref text NOT NULL,
  content_hash text NOT NULL,
  manifest_hash text NOT NULL,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, content_hash)
);

ALTER TABLE artifact_revisions
  ADD CONSTRAINT artifact_revisions_source_manifest_fk
    FOREIGN KEY (source_manifest_id) REFERENCES input_manifests(id) ON DELETE SET NULL;

CREATE TABLE output_proposals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  artifact_id uuid REFERENCES artifacts(id) ON DELETE CASCADE,
  kind text NOT NULL,
  input_manifest_id uuid NOT NULL REFERENCES input_manifests(id) ON DELETE RESTRICT,
  base_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  base_draft_id uuid REFERENCES artifact_drafts(id) ON DELETE SET NULL,
  base_content_hash text,
  status text NOT NULL DEFAULT 'open',
  version bigint NOT NULL DEFAULT 1,
  content_store text NOT NULL DEFAULT 'mongo',
  content_ref text NOT NULL,
  content_hash text NOT NULL,
  payload_hash text NOT NULL,
  operation_count integer NOT NULL DEFAULT 0,
  accepted_count integer NOT NULL DEFAULT 0,
  rejected_count integer NOT NULL DEFAULT 0,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  applied_by uuid REFERENCES users(id),
  applied_at timestamptz,
  CONSTRAINT output_proposals_status_check CHECK (
    status IN ('open', 'reviewing', 'ready', 'partially_applied', 'applied', 'rejected', 'stale', 'failed')
  )
);

CREATE INDEX output_proposals_artifact_idx ON output_proposals (artifact_id, created_at DESC);

ALTER TABLE artifact_revisions
  ADD CONSTRAINT artifact_revisions_proposal_fk
    FOREIGN KEY (proposal_id) REFERENCES output_proposals(id) ON DELETE SET NULL;

CREATE TABLE proposal_operation_decisions (
  proposal_id uuid NOT NULL REFERENCES output_proposals(id) ON DELETE CASCADE,
  operation_id text NOT NULL,
  decision text NOT NULL,
  reason text NOT NULL DEFAULT '',
  decided_by uuid NOT NULL REFERENCES users(id),
  decided_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (proposal_id, operation_id),
  CONSTRAINT proposal_operation_decision_check CHECK (decision IN ('accepted', 'rejected'))
);

CREATE TABLE blueprint_layouts (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  blueprint_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  lens text NOT NULL DEFAULT 'default',
  layout jsonb NOT NULL DEFAULT '{}'::jsonb,
  version bigint NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (blueprint_artifact_id, user_id, lens)
);

CREATE TABLE delivery_slices (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  slice_key text NOT NULL,
  title text NOT NULL,
  blueprint_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  page_spec_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  prototype_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  sync_status text NOT NULL DEFAULT 'current',
  workflow_status text NOT NULL DEFAULT 'pending',
  owner_id uuid REFERENCES users(id),
  blocker_reason text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, slice_key, blueprint_revision_id),
  CONSTRAINT delivery_slices_sync_check CHECK (sync_status IN ('current', 'needs_sync', 'blocked')),
  CONSTRAINT delivery_slices_workflow_check CHECK (
    workflow_status IN ('pending', 'ready', 'in_progress', 'waiting_review', 'completed', 'failed')
  )
);

CREATE TABLE impact_reports (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  from_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  to_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  status text NOT NULL DEFAULT 'open',
  report jsonb NOT NULL,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  CONSTRAINT impact_reports_status_check CHECK (status IN ('open', 'resolved', 'superseded')),
  UNIQUE (from_revision_id, to_revision_id)
);

CREATE INDEX impact_reports_project_idx ON impact_reports (project_id, created_at DESC);

CREATE TABLE workflow_definitions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
  workflow_key text NOT NULL,
  title text NOT NULL,
  description text NOT NULL DEFAULT '',
  lifecycle text NOT NULL DEFAULT 'active',
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT workflow_definitions_lifecycle_check CHECK (lifecycle IN ('active', 'archived'))
);

CREATE UNIQUE INDEX workflow_definitions_scope_key_unique
  ON workflow_definitions (COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid), workflow_key);

CREATE TABLE workflow_definition_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  definition_id uuid NOT NULL REFERENCES workflow_definitions(id) ON DELETE CASCADE,
  version integer NOT NULL,
  schema_version integer NOT NULL,
  content jsonb NOT NULL,
  content_hash text NOT NULL,
  validation_report jsonb NOT NULL DEFAULT '{}'::jsonb,
  published boolean NOT NULL DEFAULT false,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (definition_id, version),
  UNIQUE (definition_id, content_hash)
);

CREATE TABLE workflow_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  definition_version_id uuid NOT NULL REFERENCES workflow_definition_versions(id) ON DELETE RESTRICT,
  status text NOT NULL DEFAULT 'pending',
  input_manifest_id uuid REFERENCES input_manifests(id) ON DELETE RESTRICT,
  scope jsonb NOT NULL DEFAULT '{}'::jsonb,
  context jsonb NOT NULL DEFAULT '{}'::jsonb,
  event_cursor bigint NOT NULL DEFAULT 0,
  started_by uuid NOT NULL REFERENCES users(id),
  started_at timestamptz,
  completed_at timestamptz,
  cancelled_at timestamptz,
  failure jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT workflow_runs_status_check CHECK (
    status IN ('pending', 'running', 'waiting_input', 'waiting_review', 'completed', 'failed', 'cancelled', 'stale')
  )
);

CREATE INDEX workflow_runs_project_idx ON workflow_runs (project_id, created_at DESC);

CREATE TABLE workflow_node_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  node_key text NOT NULL,
  node_type text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  attempt integer NOT NULL DEFAULT 0,
  input_manifest_id uuid REFERENCES input_manifests(id) ON DELETE RESTRICT,
  output_proposal_id uuid REFERENCES output_proposals(id) ON DELETE RESTRICT,
  output_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  lease_owner text,
  lease_expires_at timestamptz,
  available_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  failure jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT workflow_node_runs_status_check CHECK (
    status IN ('pending', 'ready', 'running', 'waiting_input', 'waiting_review', 'completed', 'failed', 'cancelled', 'stale')
  ),
  UNIQUE (run_id, node_key)
);

CREATE INDEX workflow_node_runs_lease_idx
  ON workflow_node_runs (status, available_at, lease_expires_at)
  WHERE status IN ('ready', 'running');

CREATE TABLE workflow_run_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  sequence bigint NOT NULL,
  event_type text NOT NULL,
  node_key text,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  actor_id uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, sequence)
);

CREATE INDEX workflow_run_events_cursor_idx ON workflow_run_events (run_id, sequence);

CREATE TABLE application_build_manifests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  workflow_run_id uuid REFERENCES workflow_runs(id) ON DELETE SET NULL,
  schema_version integer NOT NULL,
  content_store text NOT NULL DEFAULT 'mongo',
  content_ref text NOT NULL,
  content_hash text NOT NULL,
  manifest_hash text NOT NULL,
  status text NOT NULL DEFAULT 'frozen',
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  invalidated_at timestamptz,
  invalidation_reason text,
  CONSTRAINT build_manifests_status_check CHECK (status IN ('frozen', 'consumed', 'invalidated')),
  UNIQUE (project_id, content_hash)
);

CREATE TABLE implementation_proposals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  base_workspace_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  status text NOT NULL DEFAULT 'open',
  version bigint NOT NULL DEFAULT 1,
  content_store text NOT NULL DEFAULT 'mongo',
  content_ref text NOT NULL,
  content_hash text NOT NULL,
  payload_hash text NOT NULL,
  operation_count integer NOT NULL DEFAULT 0,
  accepted_count integer NOT NULL DEFAULT 0,
  rejected_count integer NOT NULL DEFAULT 0,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  applied_by uuid REFERENCES users(id),
  applied_at timestamptz,
  CONSTRAINT implementation_proposals_status_check CHECK (
    status IN ('open', 'reviewing', 'ready', 'partially_applied', 'applied', 'rejected', 'stale', 'failed')
  )
);

CREATE TABLE implementation_operation_decisions (
  proposal_id uuid NOT NULL REFERENCES implementation_proposals(id) ON DELETE CASCADE,
  operation_id text NOT NULL,
  decision text NOT NULL,
  reason text NOT NULL DEFAULT '',
  decided_by uuid NOT NULL REFERENCES users(id),
  decided_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (proposal_id, operation_id),
  CONSTRAINT implementation_operation_decision_check CHECK (decision IN ('accepted', 'rejected'))
);

ALTER TABLE artifact_revisions
  ADD COLUMN implementation_proposal_id uuid,
  ADD CONSTRAINT artifact_revisions_implementation_proposal_fk
    FOREIGN KEY (implementation_proposal_id) REFERENCES implementation_proposals(id) ON DELETE SET NULL;

CREATE TABLE notifications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind text NOT NULL,
  title text NOT NULL,
  body text NOT NULL,
  resource_type text NOT NULL,
  resource_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  read_at timestamptz
);

CREATE INDEX notifications_user_unread_idx ON notifications (user_id, created_at DESC)
  WHERE read_at IS NULL;

CREATE TABLE idempotency_records (
  scope text NOT NULL,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  response_status integer,
  response_headers jsonb,
  response_body bytea,
  resource_type text,
  resource_id text,
  locked_until timestamptz,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (scope, idempotency_key)
);

CREATE INDEX idempotency_records_expiry_idx ON idempotency_records (expires_at);

CREATE TABLE audit_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid REFERENCES projects(id) ON DELETE SET NULL,
  actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
  request_id text,
  action text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_project_idx ON audit_events (project_id, created_at DESC);
CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id, created_at DESC);

CREATE TABLE outbox_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  event_type text NOT NULL,
  subject text NOT NULL,
  payload jsonb NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}'::jsonb,
  attempts integer NOT NULL DEFAULT 0,
  available_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX outbox_events_pending_idx ON outbox_events (available_at, created_at)
  WHERE published_at IS NULL;

CREATE FUNCTION reject_artifact_revision_mutation()
RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'artifact revisions cannot be deleted';
  END IF;
  IF NEW.artifact_id IS DISTINCT FROM OLD.artifact_id
    OR NEW.revision_number IS DISTINCT FROM OLD.revision_number
    OR NEW.parent_revision_id IS DISTINCT FROM OLD.parent_revision_id
    OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
    OR NEW.content_store IS DISTINCT FROM OLD.content_store
    OR NEW.content_ref IS DISTINCT FROM OLD.content_ref
    OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
    OR NEW.byte_size IS DISTINCT FROM OLD.byte_size
    OR NEW.change_source IS DISTINCT FROM OLD.change_source
    OR NEW.change_summary IS DISTINCT FROM OLD.change_summary
    OR NEW.source_manifest_id IS DISTINCT FROM OLD.source_manifest_id
    OR NEW.proposal_id IS DISTINCT FROM OLD.proposal_id
    OR NEW.implementation_proposal_id IS DISTINCT FROM OLD.implementation_proposal_id
    OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'artifact revision content is immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER artifact_revisions_immutable_update
BEFORE UPDATE OR DELETE ON artifact_revisions
FOR EACH ROW EXECUTE FUNCTION reject_artifact_revision_mutation();
