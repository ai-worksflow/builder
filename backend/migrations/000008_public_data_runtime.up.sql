ALTER TABLE deployments
  ADD CONSTRAINT deployments_project_id_id_unique UNIQUE (project_id, id);

ALTER TABLE deployment_versions
  ADD CONSTRAINT deployment_versions_deployment_id_id_unique UNIQUE (deployment_id, id);

CREATE TABLE data_public_table_policies (
  project_id uuid NOT NULL,
  table_id uuid NOT NULL,
  allow_read boolean NOT NULL DEFAULT false,
  allow_create boolean NOT NULL DEFAULT false,
  allow_update boolean NOT NULL DEFAULT false,
  allow_delete boolean NOT NULL DEFAULT false,
  readable_fields jsonb NOT NULL DEFAULT '[]'::jsonb,
  writable_fields jsonb NOT NULL DEFAULT '[]'::jsonb,
  version bigint NOT NULL DEFAULT 1,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, table_id),
  CONSTRAINT data_public_policies_table_fk
    FOREIGN KEY (project_id, table_id) REFERENCES data_tables(project_id, id) ON DELETE CASCADE,
  CONSTRAINT data_public_policies_readable_array_check
    CHECK (jsonb_typeof(readable_fields) = 'array'),
  CONSTRAINT data_public_policies_writable_array_check
    CHECK (jsonb_typeof(writable_fields) = 'array'),
  CONSTRAINT data_public_policies_read_fields_disabled_check
    CHECK (allow_read OR readable_fields = '[]'::jsonb),
  CONSTRAINT data_public_policies_write_fields_disabled_check
    CHECK (allow_create OR allow_update OR writable_fields = '[]'::jsonb),
  CONSTRAINT data_public_policies_version_positive CHECK (version > 0)
);

CREATE INDEX data_public_table_policies_project_idx
  ON data_public_table_policies (project_id, updated_at DESC, table_id);

CREATE TABLE data_public_capabilities (
  id uuid PRIMARY KEY,
  project_id uuid NOT NULL,
  deployment_id uuid NOT NULL,
  deployment_version_id uuid NOT NULL,
  token_digest bytea NOT NULL,
  allowed_origins jsonb NOT NULL,
  status text NOT NULL,
  expires_at timestamptz NOT NULL,
  activated_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_public_capabilities_deployment_fk
    FOREIGN KEY (project_id, deployment_id) REFERENCES deployments(project_id, id) ON DELETE CASCADE,
  CONSTRAINT data_public_capabilities_version_fk
    FOREIGN KEY (deployment_id, deployment_version_id)
    REFERENCES deployment_versions(deployment_id, id) ON DELETE CASCADE,
  CONSTRAINT data_public_capabilities_digest_check CHECK (octet_length(token_digest) = 32),
  CONSTRAINT data_public_capabilities_origins_array_check CHECK (jsonb_typeof(allowed_origins) = 'array'),
  CONSTRAINT data_public_capabilities_origins_nonempty_check CHECK (jsonb_array_length(allowed_origins) BETWEEN 1 AND 16),
  CONSTRAINT data_public_capabilities_status_check CHECK (status IN ('pending', 'active', 'revoked')),
  CONSTRAINT data_public_capabilities_lifecycle_check CHECK (
    (status = 'pending' AND activated_at IS NULL AND revoked_at IS NULL)
    OR (status = 'active' AND activated_at IS NOT NULL AND revoked_at IS NULL)
    OR (status = 'revoked' AND revoked_at IS NOT NULL)
  ),
  CONSTRAINT data_public_capabilities_expiry_check CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX data_public_capabilities_one_active_idx
  ON data_public_capabilities (deployment_id)
  WHERE status = 'active';

CREATE INDEX data_public_capabilities_pending_idx
  ON data_public_capabilities (deployment_id, created_at DESC)
  WHERE status = 'pending';

CREATE INDEX data_public_capabilities_expiry_idx
  ON data_public_capabilities (expires_at)
  WHERE status IN ('pending', 'active');

COMMENT ON COLUMN data_public_capabilities.token_digest IS
  'SHA-256 digest of a 256-bit opaque deployment capability. Plaintext is returned once and never persisted.';
COMMENT ON TABLE data_public_table_policies IS
  'Default-deny anonymous CRUD and field allowlists for published application data access.';
COMMENT ON TABLE data_public_capabilities IS
  'Revocable deployment-version capabilities. Pending tokens do not authorize access until delivery activation.';
