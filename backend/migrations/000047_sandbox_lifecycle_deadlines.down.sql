DROP TABLE IF EXISTS sandbox_lifecycle_deadline_leases;
DROP TRIGGER IF EXISTS sandbox_session_activity_projection ON sandbox_sessions;
DROP FUNCTION IF EXISTS sync_sandbox_session_activity_from_projection();
DROP TABLE IF EXISTS sandbox_session_activity;

-- The disabled lifecycle service principal is deliberately retained because
-- immutable checkpoint and SandboxSession audit rows may reference it.
