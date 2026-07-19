-- Migration 58 replaces delivery state-machine guards with a stricter
-- resolution-backed edge. Restoring the older guards would silently remove
-- the permanent GET-only rule even when no Case has yet been written. This
-- authority boundary is therefore intentionally non-downgradable.
DO $$
BEGIN
  RAISE EXCEPTION 'cannot downgrade governed release delivery reconciliation; preserve immutable Cases and GET-only authority'
    USING ERRCODE = '55000';
END;
$$;
