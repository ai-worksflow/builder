package agent

import (
	"errors"
	"testing"

	"github.com/worksflow/builder/backend/internal/constructor"
)

func TestPlanningObligationsDerivesOnlyExecutableMustGraph(t *testing.T) {
	contract := constructor.ContractContent{
		AcceptanceCriteria: []constructor.AcceptanceCriterion{
			{ID: "AC-2", Statement: "Second"}, {ID: "AC-1", Statement: "First"},
		},
		Oracles: []constructor.Oracle{
			{ID: "oracle-browser", AcceptanceCriterionIDs: []string{"AC-2"}, Kind: "browser"},
			{ID: "oracle-contract", AcceptanceCriterionIDs: []string{"AC-1"}, Kind: "contract", CommandID: "test-contract"},
		},
		Obligations: []constructor.Obligation{
			{ID: "OBL-2", Level: "must", Status: constructor.StatusReady, OracleIDs: []string{"oracle-browser"}},
			{ID: "OBL-1", Level: "must", Status: constructor.StatusReady, OracleIDs: []string{"oracle-contract"}},
			{ID: "OBL-SHOULD", Level: "should", Status: constructor.StatusReady, OracleIDs: []string{"oracle-contract"}},
		},
	}
	obligations, criteria, commands, err := planningObligations(contract)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(obligations, []string{"OBL-1", "OBL-2"}) ||
		!equalJSON(criteria, []string{"AC-1", "AC-2"}) ||
		!equalJSON(commands, []string{"oracle:oracle-browser", "test-contract"}) {
		t.Fatalf("unexpected executable graph: obligations=%v criteria=%v commands=%v", obligations, criteria, commands)
	}
}

func TestPlanningObligationsFailsClosedOnMissingOracleOrCriterion(t *testing.T) {
	contract := constructor.ContractContent{
		AcceptanceCriteria: []constructor.AcceptanceCriterion{{ID: "AC-1", Statement: "First"}},
		Obligations: []constructor.Obligation{{
			ID: "OBL-1", Level: "must", Status: constructor.StatusReady, OracleIDs: []string{"missing"},
		}},
	}
	if _, _, _, err := planningObligations(contract); !errors.Is(err, ErrPlanningDrift) {
		t.Fatalf("missing Oracle error = %v", err)
	}

	contract.Oracles = []constructor.Oracle{{
		ID: "missing", AcceptanceCriterionIDs: []string{"AC-UNKNOWN"}, CommandID: "test-contract",
	}}
	if _, _, _, err := planningObligations(contract); !errors.Is(err, ErrPlanningDrift) {
		t.Fatalf("missing acceptance criterion error = %v", err)
	}
}
