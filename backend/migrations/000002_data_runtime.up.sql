CREATE TABLE data_project_states (
  project_id uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  revision bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_project_states_revision_nonnegative CHECK (revision >= 0)
);

CREATE TABLE data_tables (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_tables_name_check CHECK (name ~ '^[a-z][a-z0-9_]{0,62}$'),
  UNIQUE (project_id, name),
  UNIQUE (project_id, id)
);

CREATE INDEX data_tables_project_order_idx ON data_tables (project_id, created_at, id);

CREATE TABLE data_columns (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  table_id uuid NOT NULL,
  name text NOT NULL,
  data_type text NOT NULL,
  required boolean NOT NULL DEFAULT false,
  default_value jsonb,
  position integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_columns_name_check CHECK (name ~ '^[a-z][a-z0-9_]{0,62}$'),
  CONSTRAINT data_columns_type_check CHECK (data_type IN ('text', 'number', 'boolean', 'date', 'json')),
  CONSTRAINT data_columns_position_nonnegative CHECK (position >= 0),
  UNIQUE (table_id, name),
  UNIQUE (table_id, position)
);

CREATE INDEX data_columns_table_order_idx ON data_columns (table_id, position, id);

CREATE TABLE data_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  table_id uuid NOT NULL REFERENCES data_tables(id) ON DELETE CASCADE,
  values jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_records_values_object_check CHECK (jsonb_typeof(values) = 'object'),
  CONSTRAINT data_records_project_table_fk
    FOREIGN KEY (project_id, table_id) REFERENCES data_tables(project_id, id) ON DELETE CASCADE,
  UNIQUE (table_id, id)
);

CREATE INDEX data_records_table_order_idx ON data_records (project_id, table_id, created_at, id);

CREATE TABLE data_metadata_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind text NOT NULL,
  unique_key text,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_metadata_kind_check CHECK (kind IN ('auth-users', 'storage-objects', 'server-functions')),
  CONSTRAINT data_metadata_payload_object_check CHECK (jsonb_typeof(payload) = 'object')
);

CREATE UNIQUE INDEX data_metadata_unique_key_idx
  ON data_metadata_items (project_id, kind, unique_key)
  WHERE unique_key IS NOT NULL;
CREATE INDEX data_metadata_project_kind_idx
  ON data_metadata_items (project_id, kind, created_at, id);

CREATE TABLE data_environment_variables (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name text NOT NULL,
  scope text NOT NULL,
  kind text NOT NULL,
  encrypted_value bytea,
  plain_value text,
  value_bytes integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_environment_name_check CHECK (name ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  CONSTRAINT data_environment_scope_check CHECK (scope IN ('development', 'preview', 'production')),
  CONSTRAINT data_environment_kind_check CHECK (kind IN ('plain', 'secret')),
  CONSTRAINT data_environment_value_bytes_check CHECK (value_bytes > 0 AND value_bytes <= 16000),
  CONSTRAINT data_environment_value_storage_check CHECK (
    (kind = 'secret' AND encrypted_value IS NOT NULL AND octet_length(encrypted_value) > 32 AND plain_value IS NULL)
    OR (kind = 'plain' AND encrypted_value IS NULL AND plain_value IS NOT NULL)
  ),
  UNIQUE (project_id, scope, name)
);

CREATE INDEX data_environment_project_idx
  ON data_environment_variables (project_id, scope, name);

CREATE TABLE data_migration_previews (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL,
  base_revision bigint NOT NULL,
  plan jsonb NOT NULL,
  changes jsonb NOT NULL,
  result_tables jsonb NOT NULL,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  CONSTRAINT data_migration_previews_token_hash_check CHECK (octet_length(token_hash) = 32),
  CONSTRAINT data_migration_previews_revision_nonnegative CHECK (base_revision >= 0),
  CONSTRAINT data_migration_previews_plan_object_check CHECK (jsonb_typeof(plan) = 'object'),
  CONSTRAINT data_migration_previews_changes_array_check CHECK (jsonb_typeof(changes) = 'array'),
  CONSTRAINT data_migration_previews_result_array_check CHECK (jsonb_typeof(result_tables) = 'array'),
  CONSTRAINT data_migration_previews_expiry_check CHECK (expires_at > created_at),
  UNIQUE (token_hash)
);

CREATE INDEX data_migration_previews_pending_idx
  ON data_migration_previews (project_id, created_at)
  WHERE consumed_at IS NULL;
CREATE INDEX data_migration_previews_expiry_idx
  ON data_migration_previews (expires_at)
  WHERE consumed_at IS NULL;

CREATE TABLE data_migrations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  preview_id uuid NOT NULL REFERENCES data_migration_previews(id) ON DELETE RESTRICT,
  changes jsonb NOT NULL,
  applied_by uuid NOT NULL REFERENCES users(id),
  applied_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_migrations_changes_array_check CHECK (jsonb_typeof(changes) = 'array'),
  UNIQUE (preview_id)
);

CREATE INDEX data_migrations_project_idx ON data_migrations (project_id, applied_at DESC);

CREATE TABLE data_connections (
  project_id uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  provider text NOT NULL,
  endpoint text NOT NULL,
  status text NOT NULL,
  http_status integer NOT NULL,
  latency_ms bigint NOT NULL,
  schema_tables jsonb NOT NULL DEFAULT '[]'::jsonb,
  connected_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_connections_provider_check CHECK (provider = 'supabase'),
  CONSTRAINT data_connections_status_check CHECK (status = 'connected'),
  CONSTRAINT data_connections_endpoint_check CHECK (endpoint ~ '^https://'),
  CONSTRAINT data_connections_http_status_check CHECK (http_status BETWEEN 200 AND 299),
  CONSTRAINT data_connections_latency_check CHECK (latency_ms >= 0 AND latency_ms <= 600000),
  CONSTRAINT data_connections_schema_array_check CHECK (jsonb_typeof(schema_tables) = 'array')
);

COMMENT ON COLUMN data_environment_variables.encrypted_value IS
  'AES-256-GCM envelope for secret variables; no service or HTTP operation can decrypt it.';
COMMENT ON COLUMN data_environment_variables.plain_value IS
  'Only kind=plain values used by the authenticated publish pipeline; never returned by data metadata routes.';
COMMENT ON COLUMN data_migration_previews.token_hash IS
  'SHA-256 confirmation token digest; plaintext confirmation tokens are not stored in migration state.';
COMMENT ON TABLE data_connections IS
  'Connection metadata only. Supabase API keys are discarded after the probe and are never persisted.';
