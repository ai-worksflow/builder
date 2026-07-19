DROP TRIGGER IF EXISTS golden_fault_results_append_only ON golden_fault_consume_results;
DROP TRIGGER IF EXISTS golden_fault_results_validate ON golden_fault_consume_results;
DROP TRIGGER IF EXISTS golden_fault_reservations_append_only ON golden_fault_consume_reservations;
DROP TABLE IF EXISTS golden_fault_consume_results;
DROP TABLE IF EXISTS golden_fault_consume_reservations;
DROP FUNCTION IF EXISTS reject_golden_fault_ledger_mutation();
DROP FUNCTION IF EXISTS validate_golden_fault_consume_result();
