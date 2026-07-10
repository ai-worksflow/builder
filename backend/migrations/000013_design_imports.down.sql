DROP TRIGGER IF EXISTS design_import_snapshot_immutable ON design_imports;
DROP FUNCTION IF EXISTS prevent_design_import_snapshot_mutation();
DROP TRIGGER IF EXISTS design_import_state_transition ON design_imports;
DROP FUNCTION IF EXISTS validate_design_import_state_transition();
DROP FUNCTION IF EXISTS design_import_stage_rank(text);
DROP TRIGGER IF EXISTS design_import_tenant_refs ON design_imports;
DROP FUNCTION IF EXISTS validate_design_import_tenant_refs();
DROP TABLE IF EXISTS design_imports;
