package constructor

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/worksflow/builder/backend/internal/contracts"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestCompilerProducesReadyDeterministicContract(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	first, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content.Status != StatusReady {
		t.Fatalf("status = %s, gaps = %#v conflicts = %#v", first.Content.Status, first.Content.Gaps, first.Content.Conflicts)
	}
	if len(first.Content.Obligations) != 1 || first.Content.Obligations[0].Status != StatusReady || len(first.Content.Obligations[0].OracleIDs) != 1 {
		t.Fatalf("obligations = %#v", first.Content.Obligations)
	}
	if len(first.Content.ContractBindings) != 3 {
		t.Fatalf("bindings = %#v", first.Content.ContractBindings)
	}
	wantBindingSources := map[string]string{
		"api": contracts.KindAPI, "data": contracts.KindData, "permission": contracts.KindPermission,
	}
	for _, binding := range first.Content.ContractBindings {
		wantKind, exists := wantBindingSources[binding.Kind]
		if !exists {
			t.Fatalf("unexpected binding kind %q", binding.Kind)
		}
		wantRef := compileInputSourceRef(t, input, wantKind)
		if binding.SourceRevision != wantRef {
			t.Fatalf("binding %s source = %#v, want exact %s source %#v", binding.ID, binding.SourceRevision, wantKind, wantRef)
		}
	}
	const expectedContractHash = "ed3cf0e4fe288b248f97be032d7d9c7f14912e1697d78fc121b78eabc91ee9be"
	if !domain.IsCanonicalHash(first.ContentHash) || first.ContractHash != first.ContentHash {
		t.Fatalf("content hash = %q, contract hash = %q", first.ContentHash, first.ContractHash)
	}
	if first.ContractHash != expectedContractHash {
		t.Fatalf("contractHash = %q, want deterministic %q", first.ContractHash, expectedContractHash)
	}

	shuffled := input
	shuffled.Sources = append([]PinnedBuildSource{}, input.Sources...)
	shuffled.TemplateReleaseRefs = append([]TemplateReleaseRef{}, input.TemplateReleaseRefs...)
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled.Sources), func(i, j int) { shuffled.Sources[i], shuffled.Sources[j] = shuffled.Sources[j], shuffled.Sources[i] })
	rand.New(rand.NewSource(8)).Shuffle(len(shuffled.TemplateReleaseRefs), func(i, j int) {
		shuffled.TemplateReleaseRefs[i], shuffled.TemplateReleaseRefs[j] = shuffled.TemplateReleaseRefs[j], shuffled.TemplateReleaseRefs[i]
	})
	second, err := (Compiler{}).Compile(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if second.ContentHash != first.ContentHash {
		t.Fatalf("hash changed after input reordering: %s != %s", first.ContentHash, second.ContentHash)
	}
	if second.ContractHash != first.ContractHash {
		t.Fatalf("contractHash changed after input reordering: %s != %s", first.ContractHash, second.ContractHash)
	}
	firstJSON, _ := domain.CanonicalJSON(first.Content)
	secondJSON, _ := domain.CanonicalJSON(second.Content)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("canonical contract changed after input reordering\n%s\n%s", firstJSON, secondJSON)
	}
}

func TestCompilerBlocksLegacyAIRuntimeSchema(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	replaceCompileInputSource(t, &input, contracts.KindAIRuntime, `{
      "schemaVersion":"ai-runtime-contract/v1",
      "providerPolicy":{"policyId":"legacy","modelClass":"reasoning","fallbackAllowed":false},
      "conversation":{"persistence":"required","messageRoles":["user","assistant"]},
      "run":{"idempotencyRequired":true,"statusValues":["queued","running","completed","failed","cancelled"]},
      "streaming":{"transport":"sse","eventTypes":["run.started","output.delta","run.completed","run.failed"],"resumeCursor":true},
      "cancellation":{"supported":true,"terminalStatus":"cancelled"},
      "retry":{"reasonRequired":true,"maxAttempts":3,"supersedeOnModelChange":true},
      "limits":{"maxInputBytes":1000,"maxOutputBytes":1000,"timeoutSeconds":30},
      "retention":{"messageDays":30,"runDays":30,"redactionRequired":true}
    }`)
	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "contract_schema_version_obsolete", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksLegacyAPIAndDataSchemas(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, kind, payload string
	}{
		{
			name: "API", kind: contracts.KindAPI,
			payload: `{"schemaVersion":"api-contract/v1","openapi":"3.1.0","info":{"title":"Legacy","version":"1"},"paths":{"/messages":{"get":{"operationId":"listMessages","responses":{"200":{"description":"ok"}}}}}}`,
		},
		{
			name: "Data", kind: contracts.KindData,
			payload: `{"schemaVersion":"data-contract/v1","entities":[{"id":"Message","tableName":"messages","fields":[{"id":"id","name":"id","type":"uuid","nullable":false},{"id":"projectId","name":"project_id","type":"uuid","nullable":false}],"primaryKey":["id"],"indexes":[],"tenantScope":{"mode":"project","fieldId":"projectId"}}],"migrationPolicy":{"tool":"goose","directory":"migrations","applyCommandId":"migrate","rollbackPolicy":"forward-only"}}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := readyCompileInput(t)
			replaceCompileInputSource(t, &input, test.kind, test.payload)
			compiled, err := (Compiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "contract_schema_version_obsolete", "") {
				t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
			}
		})
	}
}

func TestCompilerBlocksBlueprintWithoutSemanticPage(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	replaceCompileInputSource(t, &input, "blueprint", `{
      "semantic":{
        "nodes":[{"id":"feature-messages","key":"messages-feature","type":"feature","title":"Messages feature"}],
        "edges":[]
      }
    }`)

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "blueprint_page_missing", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksPageSpecBoundToNonPageBlueprintNode(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	replaceCompileInputSource(t, &input, "page_spec", `{
      "blueprintPageNodeId":"feature-messages","title":"Messages","route":"/messages","userGoal":"Use messages",
      "acceptanceCriterionIds":["AC-MESSAGES"],"requiredRoles":["message-reader"],
      "states":[
        {"id":"state-ready","key":"ready","title":"Ready","required":true},
        {"id":"state-loading","key":"loading","title":"Loading","required":true},
        {"id":"state-empty","key":"empty","title":"Empty","required":true},
        {"id":"state-error","key":"error","title":"Error","required":true}
      ],
      "dataBindings":[
        {"id":"messages-api","name":"Messages API","source":"api","operationId":"listMessages"},
        {"id":"messages-data","name":"Messages","source":"database","entityId":"Message"}
      ],
      "interactions":[]
    }`)

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "page_spec_blueprint_page_unknown", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksPageSpecBlueprintSemanticDrift(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	replaceCompileInputSource(t, &input, "page_spec", `{
      "blueprintPageNodeId":"page-messages","title":"Messages","route":"/inbox","userGoal":"Use messages",
      "acceptanceCriterionIds":["AC-MESSAGES"],"requiredRoles":["message-reader"],
      "states":[
        {"id":"state-ready","key":"ready","title":"Ready","required":true},
        {"id":"state-loading","key":"loading","title":"Loading","required":true},
        {"id":"state-empty","key":"empty","title":"Empty","required":true},
        {"id":"state-error","key":"error","title":"Error","required":true}
      ],
      "dataBindings":[
        {"id":"messages-api","name":"Messages API","source":"api","operationId":"listMessages"},
        {"id":"messages-data","name":"Messages","source":"database","entityId":"Message"}
      ],
      "interactions":[]
    }`)

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "page_spec_blueprint_page_conflict", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksPrototypePageSpecRevisionDrift(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	mutateCompileInputSource(t, &input, "prototype", func(content map[string]any) {
		content["pageSpecRevision"].(map[string]any)["contentHash"] = strings.Repeat("b", 64)
	})

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "prototype_page_spec_revision_mismatch", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksPrototypePageStateDrift(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	mutateCompileInputSource(t, &input, "prototype", func(content map[string]any) {
		states := content["states"].([]any)
		states[0].(map[string]any)["title"] = "Ready but drifted"
	})

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "prototype_page_state_conflict", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksMissingPrototypeFrameCoverage(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	mutateCompileInputSource(t, &input, "prototype", func(content map[string]any) {
		frames := content["frames"].([]any)
		content["frames"] = frames[:len(frames)-1]
	})

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "prototype_page_state_frame_missing", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksLegacyPrototypeWithoutCanonicalFormat(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	replaceCompileInputSource(t, &input, "prototype", `{"states":[],"layers":{},"traceLinks":[]}`)

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked ||
		!containsGap(compiled.Content.Gaps, "prototype_page_spec_revision_invalid", "") ||
		!containsGap(compiled.Content.Gaps, "prototype_breakpoints_invalid", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBindsDeliverySliceToPageSpecBlueprintPage(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	input.DeliverySliceID = "page-other"
	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "delivery_slice_page_mismatch", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksBlueprintMissingMustRequirementCoverage(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	mutateCompileInputSource(t, &input, "requirement_baseline", func(content map[string]any) {
		requirements := content["requirements"].([]any)
		content["requirements"] = append(requirements,
			map[string]any{
				"type": "requirement", "requirementId": "REQ-SECOND", "statement": "Cover the second Must requirement.",
				"priority": "must", "acceptanceCriterionIds": []string{"AC-SECOND"},
			},
			map[string]any{
				"type": "acceptanceCriterion", "acceptanceCriterionId": "AC-SECOND", "statement": "Second criterion is covered.",
			},
		)
	})

	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked ||
		!containsGapMessage(compiled.Content.Gaps, "blueprint_requirement_trace_invalid", "REQ-SECOND") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksPageSpecAcceptanceAuthorityDrift(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		acceptance string
		configure  func(*testing.T, *CompileInput)
	}{
		{
			name: "incomplete",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, "requirement_baseline", func(content map[string]any) {
					requirements := content["requirements"].([]any)
					requirement := requirements[0].(map[string]any)
					requirement["acceptanceCriterionIds"] = []string{"AC-MESSAGES", "AC-SECOND"}
					content["requirements"] = append(requirements, map[string]any{
						"type": "acceptanceCriterion", "acceptanceCriterionId": "AC-SECOND", "statement": "Second criterion is covered.",
					})
				})
			},
			acceptance: "AC-SECOND",
		},
		{
			name: "unauthorized",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, "requirement_baseline", func(content map[string]any) {
					requirements := content["requirements"].([]any)
					content["requirements"] = append(requirements,
						map[string]any{
							"type": "requirement", "requirementId": "REQ-OTHER", "statement": "Other page requirement.",
							"priority": "should", "acceptanceCriterionIds": []string{"AC-OTHER"},
						},
						map[string]any{
							"type": "acceptanceCriterion", "acceptanceCriterionId": "AC-OTHER", "statement": "Other page criterion.",
						},
					)
				})
				mutatePageSpecAndSyncPrototype(t, input, func(content map[string]any) {
					content["acceptanceCriterionIds"] = []string{"AC-OTHER"}
				})
			},
			acceptance: "AC-OTHER",
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := readyCompileInput(t)
			testCase.configure(t, &input)
			compiled, err := (Compiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Content.Status != StatusBlocked ||
				!containsGapMessage(compiled.Content.Gaps, "page_spec_semantic_trace_invalid", testCase.acceptance) {
				t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
			}
		})
	}
}

func TestCompilerBlocksBrokenPageAPIPermissionAuthority(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		code string
		kind string
	}{
		{name: "page_api_calls_missing", code: "page_spec_api_ownership_invalid", kind: "calls"},
		{name: "api_permission_requires_missing", code: "blueprint.api_permission", kind: "requires"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := readyCompileInput(t)
			mutateCompileInputSource(t, &input, "blueprint", func(content map[string]any) {
				semantic := content["semantic"].(map[string]any)
				edges := semantic["edges"].([]any)
				filtered := make([]any, 0, len(edges)-1)
				for _, raw := range edges {
					edge := raw.(map[string]any)
					if edge["kind"] != testCase.kind {
						filtered = append(filtered, edge)
					}
				}
				semantic["edges"] = filtered
			})
			compiled, err := (Compiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, testCase.code, "") {
				t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
			}
		})
	}
}

func TestCompilerBlocksPageSpecRequiredRoleDrift(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	mutatePageSpecAndSyncPrototype(t, &input, func(content map[string]any) {
		content["requiredRoles"] = []string{}
	})
	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "page_spec_semantic_trace_invalid", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerBlocksPrototypeSemanticAuthorityDrift(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		configure func(*testing.T, *CompileInput)
	}{
		{
			name: "required_binding_missing",
			configure: func(t *testing.T, input *CompileInput) {
				mutatePageSpecAndSyncPrototype(t, input, func(content map[string]any) {
					bindings := content["dataBindings"].([]any)
					bindings[0].(map[string]any)["required"] = true
				})
			},
		},
		{
			name: "fixture_missing",
			configure: func(t *testing.T, input *CompileInput) {
				mutatePageSpecAndSyncPrototype(t, input, func(content map[string]any) {
					states := content["states"].([]any)
					states[0].(map[string]any)["fixtureIds"] = []string{"fixture-ready"}
				})
			},
		},
		{
			name: "interaction_missing",
			configure: func(t *testing.T, input *CompileInput) {
				mutatePageSpecAndSyncPrototype(t, input, func(content map[string]any) {
					content["interactions"] = []any{map[string]any{
						"id": "interaction-open", "trigger": "click", "outcome": "Open message",
					}}
				})
			},
		},
		{
			name: "trace_unauthorized",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, "prototype", func(content map[string]any) {
					layers := content["layers"].(map[string]any)
					layers["layer-root"].(map[string]any)["requirementIds"] = []string{"REQ-BOGUS"}
				})
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := readyCompileInput(t)
			testCase.configure(t, &input)
			compiled, err := (Compiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "prototype_semantic_trace_invalid", "") {
				t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
			}
		})
	}
}

func TestCompilerRequiresExactBlueprintAPIContractOperation(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		payload string
		codes   []string
	}{
		{
			name: "method",
			payload: `{
          "schemaVersion":"api-contract/v2","openapi":"3.1.0","info":{"title":"Messages API","version":"1"},
          "paths":{"/messages":{"post":{"operationId":"listMessages","responses":{"200":{"description":"ok"}}}}}
        }`,
			codes: []string{"blueprint_api_operation_conflict"},
		},
		{
			name: "path",
			payload: `{
          "schemaVersion":"api-contract/v2","openapi":"3.1.0","info":{"title":"Messages API","version":"1"},
          "paths":{"/inbox":{"get":{"operationId":"listMessages","responses":{"200":{"description":"ok"}}}}}
        }`,
			codes: []string{"blueprint_api_operation_conflict"},
		},
		{
			name: "operation_id",
			payload: `{
          "schemaVersion":"api-contract/v2","openapi":"3.1.0","info":{"title":"Messages API","version":"1"},
          "paths":{"/messages":{"get":{"operationId":"fetchMessages","responses":{"200":{"description":"ok"}}}}}
        }`,
			codes: []string{"blueprint_api_operation_missing", "api_contract_operation_outside_delivery_slice"},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := readyCompileInput(t)
			replaceCompileInputSource(t, &input, contracts.KindAPI, testCase.payload)
			compiled, err := (Compiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Content.Status != StatusBlocked {
				t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
			}
			for _, code := range testCase.codes {
				if !containsGap(compiled.Content.Gaps, code, "") {
					t.Fatalf("missing %s: gaps = %#v", code, compiled.Content.Gaps)
				}
			}
		})
	}
}

func TestCompilerBlocksAPIContractOperationOutsideDeliverySlice(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	replaceCompileInputSource(t, &input, contracts.KindAPI, `{
      "schemaVersion":"api-contract/v2","openapi":"3.1.0","info":{"title":"Messages API","version":"1"},
      "paths":{
        "/messages":{"get":{"operationId":"listMessages","responses":{"200":{"description":"ok"}}}},
        "/admin/messages":{"delete":{"operationId":"deleteMessages","responses":{"204":{"description":"deleted"}}}}
      }
    }`)
	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked ||
		!containsGap(compiled.Content.Gaps, "api_contract_operation_outside_delivery_slice", "") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestCompilerObligationIDsDoNotCollideAfterReadableNormalization(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	replaceCompileInputSource(t, &input, "requirement_baseline", `{
      "sourceVersions":[{"artifactId":"requirements","revisionId":"requirements-r1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
      "requirements":[
        {"type":"requirement","requirementId":"REQ-COLLISION","statement":"Keep distinct acceptance anchors.","priority":"must","acceptanceCriterionIds":["AC/A","AC-A"]},
        {"type":"acceptanceCriterion","acceptanceCriterionId":"AC/A","statement":"Slash acceptance remains distinct."},
        {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-A","statement":"Dash acceptance remains distinct."}
      ]
    }`)
	mutateCompileInputSource(t, &input, "blueprint", func(content map[string]any) {
		semantic := content["semantic"].(map[string]any)
		for _, node := range semantic["nodes"].([]any) {
			candidate := node.(map[string]any)
			if candidate["id"] == "page-messages" {
				candidate["requirementIds"] = []string{"REQ-COLLISION"}
			}
		}
	})
	replaceCompileInputSource(t, &input, "page_spec", `{
      "blueprintPageNodeId":"page-messages","title":"Messages","route":"/messages","userGoal":"Use messages",
      "acceptanceCriterionIds":["AC/A","AC-A"],"requiredRoles":["message-reader"],
      "states":[
        {"id":"state-ready","key":"ready","title":"Ready","required":true},
        {"id":"state-loading","key":"loading","title":"Loading","required":true},
        {"id":"state-empty","key":"empty","title":"Empty","required":true},
        {"id":"state-error","key":"error","title":"Error","required":true}
      ],
      "dataBindings":[
        {"id":"messages-api","name":"Messages API","source":"api","operationId":"listMessages"},
        {"id":"messages-data","name":"Messages","source":"database","entityId":"Message"}
      ],
      "interactions":[]
    }`)
	mutateCompileInputSource(t, &input, "prototype", func(content map[string]any) {
		layers := content["layers"].(map[string]any)
		root := layers["layer-root"].(map[string]any)
		root["requirementIds"] = []string{"REQ-COLLISION"}
		root["acceptanceCriterionIds"] = []string{"AC/A", "AC-A"}
	})
	replaceCompileInputSource(t, &input, contracts.KindVerification, `{
      "schemaVersion":"verification-contract/v1","oracles":[
        {"id":"verify-slash","acceptanceCriterionIds":["AC/A"],"kind":"contract","target":"GET /messages?shape=slash","commandId":"test-slash","blocking":true},
        {"id":"verify-dash","acceptanceCriterionIds":["AC-A"],"kind":"contract","target":"GET /messages?shape=dash","commandId":"test-dash","blocking":true}
      ]
    }`)

	first, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content.Status != StatusReady || len(first.Content.Obligations) != 2 {
		t.Fatalf("status=%s obligations=%#v gaps=%#v", first.Content.Status, first.Content.Obligations, first.Content.Gaps)
	}
	ids := map[string]string{}
	for _, obligation := range first.Content.Obligations {
		ids[obligation.SourceAnchorID] = obligation.ID
		if !strings.HasPrefix(obligation.ID, "OBL-AC-A-") {
			t.Fatalf("obligation %q lost its readable prefix", obligation.ID)
		}
		if obligation.ID != obligationID(obligation.SourceAnchorID) {
			t.Fatalf("obligation %q is not stable for %q", obligation.ID, obligation.SourceAnchorID)
		}
	}
	if ids["AC/A"] == "" || ids["AC-A"] == "" || ids["AC/A"] == ids["AC-A"] {
		t.Fatalf("normalized acceptance anchors collided: %#v", ids)
	}

	second, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if second.ContractHash != first.ContractHash {
		t.Fatalf("recompilation changed contractHash: %s != %s", first.ContractHash, second.ContractHash)
	}
	for _, obligation := range second.Content.Obligations {
		if ids[obligation.SourceAnchorID] != obligation.ID {
			t.Fatalf("recompilation changed obligation ID for %q: %q != %q", obligation.SourceAnchorID, ids[obligation.SourceAnchorID], obligation.ID)
		}
	}
}

func TestCompilerRequiresEveryMachineContract(t *testing.T) {
	t.Parallel()

	for _, missingKind := range []string{
		contracts.KindAPI, contracts.KindData, contracts.KindPermission,
		contracts.KindAIRuntime, contracts.KindDeployment, contracts.KindVerification,
	} {
		missingKind := missingKind
		t.Run(missingKind, func(t *testing.T) {
			t.Parallel()
			input := readyCompileInput(t)
			filtered := []PinnedBuildSource{}
			for _, source := range input.Sources {
				if source.Ref.Kind != missingKind {
					filtered = append(filtered, source)
				}
			}
			input.Sources = filtered
			compiled, err := (Compiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "required_contract_missing", missingKind) {
				t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
			}
		})
	}
}

func TestCompilerRejectsProseAndSourceHashDrift(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	for index := range input.Sources {
		if input.Sources[index].Ref.Kind == contracts.KindAPI {
			input.Sources[index].Content = json.RawMessage(`{"blocks":[],"summary":"pretend API"}`)
			break
		}
	}
	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "source_content_hash_mismatch", "") || !containsGap(compiled.Content.Gaps, "contract_schema_invalid", "") {
		t.Fatalf("gaps = %#v", compiled.Content.Gaps)
	}
}

func TestCompilerBlocksMustAcceptanceWithoutOracle(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	for index := range input.Sources {
		if input.Sources[index].Ref.Kind == contracts.KindVerification {
			input.Sources[index] = pinnedSource(t, contracts.KindVerification, `{
          "schemaVersion":"verification-contract/v1",
          "oracles":[{"id":"other","acceptanceCriterionIds":["AC-OTHER"],"kind":"health","target":"health","blocking":true}]
        }`)
			break
		}
	}
	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, "must_oracle_missing", "AC-MESSAGES") {
		t.Fatalf("gaps = %#v", compiled.Content.Gaps)
	}
	if len(compiled.Content.Obligations) != 1 || compiled.Content.Obligations[0].Status != StatusBlocked || compiled.Content.Obligations[0].BlockingReasonID == "" {
		t.Fatalf("obligation = %#v", compiled.Content.Obligations)
	}
	if !gapIDExists(compiled.Content.Gaps, compiled.Content.Obligations[0].BlockingReasonID) {
		t.Fatalf("blocking reason %q does not identify a persisted gap: %#v", compiled.Content.Obligations[0].BlockingReasonID, compiled.Content.Gaps)
	}
}

func TestCompilerHashPinsTemplateRelease(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	first, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	input.TemplateReleaseRefs[0].ReleaseHash = testHash(t, map[string]any{"release": "web-v2"})
	for index := range input.TemplateRuntime.Components {
		if input.TemplateRuntime.Components[index].Role == input.TemplateReleaseRefs[0].Role {
			input.TemplateRuntime.Components[index].ReleaseHash = input.TemplateReleaseRefs[0].ReleaseHash
		}
	}
	second, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash == second.ContentHash {
		t.Fatal("template release hash did not change BuildContract hash")
	}
}

func TestCompilerTemplateDeploymentRuntimeClosureFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		code      string
		configure func(*testing.T, *CompileInput)
	}{
		{
			name: "runtime facts missing", code: "template_runtime_facts_missing",
			configure: func(_ *testing.T, input *CompileInput) { input.TemplateRuntime = nil },
		},
		{
			name: "stack identity drift", code: "template_runtime_identity_mismatch",
			configure: func(_ *testing.T, input *CompileInput) {
				input.TemplateRuntime.FullStackTemplateHash = strings.Repeat("f", 64)
			},
		},
		{
			name: "component missing", code: "template_runtime_component_set_mismatch",
			configure: func(_ *testing.T, input *CompileInput) {
				input.TemplateRuntime.Components = input.TemplateRuntime.Components[:1]
			},
		},
		{
			name: "release identity drift", code: "template_runtime_component_set_mismatch",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "api").ReleaseHash = strings.Repeat("e", 64)
			},
		},
		{
			name: "layout invalid", code: "template_runtime_layout_invalid",
			configure: func(_ *testing.T, input *CompileInput) { input.TemplateRuntime.Layout.OpenAPIPath = "../openapi.yaml" },
		},
		{
			name: "manifest version unsupported", code: "template_runtime_manifest_version",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "web").ManifestSchemaVersion = "template-manifest/v2"
			},
		},
		{
			name: "mount path invalid", code: "template_runtime_mount_invalid",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "web").MountPath = "../apps/web"
			},
		},
		{
			name: "service path invalid", code: "template_runtime_service_invalid",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "web").Services[0].RootPath = "../src"
			},
		},
		{
			name: "component service ambiguous", code: "template_runtime_component_role_ambiguous",
			configure: func(t *testing.T, input *CompileInput) {
				component := templateRuntimeComponent(t, input, "web")
				component.Services = append(component.Services, TemplateRuntimeService{ID: "sidecar", Role: "web", RootPath: "sidecar"})
			},
		},
		{
			name: "multiple outputs unrepresentable", code: "template_runtime_v1_cardinality_unrepresentable",
			configure: func(t *testing.T, input *CompileInput) {
				component := templateRuntimeComponent(t, input, "web")
				component.BuildOutputs = append(component.BuildOutputs, TemplateRuntimeBuildOutput{ServiceID: "web", Path: "assets"})
			},
		},
		{
			name: "https unrepresentable", code: "template_runtime_protocol_unrepresentable",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "web").Ports[0].Protocol = "https"
			},
		},
		{
			name: "port fact invalid", code: "template_runtime_port_invalid",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "web").Ports[0].Number = 0
			},
		},
		{
			name: "health fact invalid", code: "template_runtime_health_invalid",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "web").HealthChecks[0].Path = "/health?probe=true"
			},
		},
		{
			name: "build output fact invalid", code: "template_runtime_build_output_invalid",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "web").BuildOutputs[0].Path = "../dist"
			},
		},
		{
			name: "migration fact invalid", code: "template_runtime_migration_invalid",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "api").Migration.CommandName = "undeclared-migrate"
			},
		},
		{
			name: "environment scope ambiguous", code: "template_runtime_environment_scope_ambiguous",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "api").EnvironmentVariables = []TemplateRuntimeEnvironmentVariable{{
					Name: "DATABASE_URL", Required: true, Secret: true, Scope: "web-build",
				}}
			},
		},
		{
			name: "service id collision", code: "template_runtime_service_collision",
			configure: func(t *testing.T, input *CompileInput) {
				component := templateRuntimeComponent(t, input, "api")
				component.Services[0].ID = "web"
				component.Ports[0].ServiceID = "web"
				component.HealthChecks[0].ServiceID = "web"
				component.BuildOutputs[0].ServiceID = "web"
				component.Migration.ServiceID = "web"
			},
		},
		{
			name: "port name collision", code: "template_runtime_port_collision",
			configure: func(t *testing.T, input *CompileInput) {
				component := templateRuntimeComponent(t, input, "api")
				component.Ports[0].Name = "web"
				component.HealthChecks[0].PortName = "web"
			},
		},
		{
			name: "port number collision", code: "template_runtime_port_collision",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "api").Ports[0].Number = 3000
			},
		},
		{
			name: "health id collision", code: "template_runtime_health_collision",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "api").HealthChecks[0].ID = "web-health"
			},
		},
		{
			name: "environment collision", code: "template_runtime_environment_collision",
			configure: func(t *testing.T, input *CompileInput) {
				for _, role := range []string{"web", "api"} {
					component := templateRuntimeComponent(t, input, role)
					component.EnvironmentVariables = append(component.EnvironmentVariables, TemplateRuntimeEnvironmentVariable{
						Name: "PORT", Required: true, Scope: templateEnvironmentScope(role),
					})
				}
			},
		},
		{
			name: "multiple migrations unrepresentable", code: "template_runtime_migration_unrepresentable",
			configure: func(t *testing.T, input *CompileInput) {
				component := templateRuntimeComponent(t, input, "web")
				component.Commands = append(component.Commands, "web-migrate")
				component.Migration = &TemplateRuntimeMigration{ServiceID: "web", CommandName: "web-migrate"}
			},
		},
		{
			name: "deployment service set drift", code: "template_deployment_service_set_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["services"] = append(content["services"].([]any), map[string]any{
						"id": "worker", "role": "worker", "sourceRoot": "workers/jobs", "portName": "api",
						"healthCheckId": "api-health", "buildOutput": "workers/jobs/bin",
					})
				})
			},
		},
		{
			name: "deployment service port link drift", code: "template_deployment_service_port_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					service := content["services"].([]any)[0].(map[string]any)
					service["portName"], service["healthCheckId"] = "api", "api-health"
				})
			},
		},
		{
			name: "deployment service health link drift", code: "template_deployment_service_health_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["healthChecks"] = append(content["healthChecks"].([]any), map[string]any{
						"id": "web-alt-health", "portName": "web", "method": "HEAD", "path": "/", "expectedStatuses": []any{float64(204)},
					})
					content["services"].([]any)[0].(map[string]any)["healthCheckId"] = "web-alt-health"
				})
			},
		},
		{
			name: "deployment port set drift", code: "template_deployment_port_set_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["ports"] = append(content["ports"].([]any), map[string]any{
						"name": "metrics", "containerPort": float64(9090), "protocol": "http",
					})
				})
			},
		},
		{
			name: "deployment health set drift", code: "template_deployment_health_set_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["healthChecks"] = append(content["healthChecks"].([]any), map[string]any{
						"id": "web-extra-health", "portName": "web", "method": "GET", "path": "/healthz", "expectedStatuses": []any{float64(200)},
					})
				})
			},
		},
		{
			name: "source root drift", code: "template_deployment_service_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["services"].([]any)[0].(map[string]any)["sourceRoot"] = "apps/other"
				})
			},
		},
		{
			name: "port number drift", code: "template_deployment_port_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["ports"].([]any)[0].(map[string]any)["containerPort"] = float64(3001)
				})
			},
		},
		{
			name: "health path drift", code: "template_deployment_health_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["healthChecks"].([]any)[0].(map[string]any)["path"] = "/healthz"
				})
			},
		},
		{
			name: "build output drift", code: "template_deployment_build_output_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["services"].([]any)[0].(map[string]any)["buildOutput"] = "apps/web/build"
				})
			},
		},
		{
			name: "migration command drift", code: "template_deployment_migration_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["migration"].(map[string]any)["commandId"] = "other-migrate"
				})
			},
		},
		{
			name: "migration disabled", code: "template_deployment_migration_drift",
			configure: func(t *testing.T, input *CompileInput) {
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["migration"].(map[string]any)["required"] = false
				})
			},
		},
		{
			name: "required environment missing", code: "template_deployment_environment_missing",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "api").EnvironmentVariables = []TemplateRuntimeEnvironmentVariable{{
					Name: "DATABASE_URL", Required: true, Secret: true, Scope: "api-runtime",
				}}
			},
		},
		{
			name: "environment weakened", code: "template_deployment_environment_drift",
			configure: func(t *testing.T, input *CompileInput) {
				templateRuntimeComponent(t, input, "api").EnvironmentVariables = []TemplateRuntimeEnvironmentVariable{{
					Name: "DATABASE_URL", Required: true, Secret: true, Scope: "api-runtime",
				}}
				mutateCompileInputSource(t, input, contracts.KindDeployment, func(content map[string]any) {
					content["environmentVariables"] = []any{map[string]any{
						"name": "DATABASE_URL", "required": false, "secret": true, "scope": "api-runtime",
					}}
				})
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := readyCompileInput(t)
			test.configure(t, &input)
			compiled, err := (Compiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Content.Status != StatusBlocked || !containsGap(compiled.Content.Gaps, test.code, "") {
				t.Fatalf("status = %s, want gap %q in %#v", compiled.Content.Status, test.code, compiled.Content.Gaps)
			}
		})
	}
}

func TestCompilerTemplateDeploymentRuntimeAllowsExactSubsetAndExtraBusinessEnvironment(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	templateRuntimeComponent(t, &input, "api").EnvironmentVariables = []TemplateRuntimeEnvironmentVariable{{
		Name: "DATABASE_URL", Required: true, Secret: true, Scope: "api-runtime",
	}}
	mutateCompileInputSource(t, &input, contracts.KindDeployment, func(content map[string]any) {
		content["environmentVariables"] = []any{
			map[string]any{"name": "DATABASE_URL", "required": true, "secret": true, "scope": "api-runtime"},
			map[string]any{"name": "BUSINESS_FEATURE_FLAG", "required": false, "secret": false, "scope": "api-runtime"},
		}
	})
	first, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content.Status != StatusReady {
		t.Fatalf("status = %s, gaps = %#v", first.Content.Status, first.Content.Gaps)
	}
	input.TemplateRuntime.Components[0], input.TemplateRuntime.Components[1] = input.TemplateRuntime.Components[1], input.TemplateRuntime.Components[0]
	second, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if second.ContentHash != first.ContentHash || second.Content.Status != StatusReady {
		t.Fatalf("runtime fact reordering changed ready output: first=%#v second=%#v", first, second)
	}
}

func TestCompilerTemplateRuntimeDuplicateIdentityDiagnosticsAreDeterministic(t *testing.T) {
	t.Parallel()

	input := readyCompileInput(t)
	duplicate := input.TemplateRuntime.Components[0]
	duplicate.ManifestSchemaVersion = "template-manifest/invalid"
	input.TemplateRuntime.Components = append(input.TemplateRuntime.Components, duplicate)

	first, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content.Status != StatusBlocked || !containsGap(first.Content.Gaps, "template_runtime_component_set_mismatch", "") {
		t.Fatalf("status = %s, want duplicate component gap in %#v", first.Content.Status, first.Content.Gaps)
	}

	reordered := input
	reordered.TemplateRuntime = &TemplateRuntimeFacts{
		FullStackTemplateID:   input.TemplateRuntime.FullStackTemplateID,
		FullStackTemplateHash: input.TemplateRuntime.FullStackTemplateHash,
		Layout:                input.TemplateRuntime.Layout,
		Components:            append([]TemplateRuntimeComponent(nil), input.TemplateRuntime.Components...),
	}
	reordered.TemplateRuntime.Components[0], reordered.TemplateRuntime.Components[2] =
		reordered.TemplateRuntime.Components[2], reordered.TemplateRuntime.Components[0]
	second, err := (Compiler{}).Compile(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if second.ContentHash != first.ContentHash {
		t.Fatalf("duplicate component reordering changed blocked hash: %s != %s", first.ContentHash, second.ContentHash)
	}
	firstJSON, _ := domain.CanonicalJSON(first.Content)
	secondJSON, _ := domain.CanonicalJSON(second.Content)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("duplicate component reordering changed blocked contract\n%s\n%s", firstJSON, secondJSON)
	}
}

func templateRuntimeComponent(t *testing.T, input *CompileInput, role string) *TemplateRuntimeComponent {
	t.Helper()
	if input.TemplateRuntime == nil {
		t.Fatal("template runtime fixture is nil")
	}
	for index := range input.TemplateRuntime.Components {
		if input.TemplateRuntime.Components[index].Role == role {
			return &input.TemplateRuntime.Components[index]
		}
	}
	t.Fatalf("template runtime component %q was not found", role)
	return nil
}

func readyCompileInput(t *testing.T) CompileInput {
	t.Helper()
	baseline := canonicalRequirementBaselineSource(t, `{
      "sourceVersions":[{"artifactId":"requirements","revisionId":"requirements-r1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
      "requirements":[
        {"type":"requirement","requirementId":"REQ-MESSAGES","statement":"Persist project messages.","priority":"must","acceptanceCriterionIds":["AC-MESSAGES"]},
        {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-MESSAGES","statement":"Messages persist and can be listed after a new session."}
      ]
    }`)
	blueprint := pinnedSource(t, "blueprint", `{
      "semantic":{
        "nodes":[
          {"id":"feature-messages","key":"messages-feature","type":"feature","title":"Messages feature"},
          {"id":"page-messages","key":"messages","type":"page","title":"Messages","route":"/messages","userGoal":"Use messages","requirementIds":["REQ-MESSAGES"]},
          {"id":"listMessages","key":"list-messages","type":"apiOperation","title":"List messages","method":"GET","path":"/messages"},
          {"id":"permission-message-reader","key":"message-reader","type":"permission","title":"Message reader","roles":["message-reader"]}
        ],
        "edges":[
          {"id":"contains-messages","sourceNodeId":"feature-messages","targetNodeId":"page-messages","kind":"contains","required":true},
          {"id":"calls-list-messages","sourceNodeId":"page-messages","targetNodeId":"listMessages","kind":"calls","required":true},
          {"id":"requires-message-reader","sourceNodeId":"listMessages","targetNodeId":"permission-message-reader","kind":"requires","required":true}
        ]
      }
    }`)
	pageSpec := pinnedSource(t, "page_spec", `{
      "blueprintPageNodeId":"page-messages","title":"Messages","route":"/messages","userGoal":"Use messages",
      "acceptanceCriterionIds":["AC-MESSAGES"],"requiredRoles":["message-reader"],
      "states":[
        {"id":"state-ready","key":"ready","title":"Ready","required":true},
        {"id":"state-loading","key":"loading","title":"Loading","required":true},
        {"id":"state-empty","key":"empty","title":"Empty","required":true},
        {"id":"state-error","key":"error","title":"Error","required":true}
      ],
      "dataBindings":[
        {"id":"messages-api","name":"Messages API","source":"api","operationId":"listMessages"},
        {"id":"messages-data","name":"Messages","source":"database","entityId":"Message"}
      ],
      "interactions":[]
    }`)
	prototype := readyPrototypeSource(t, pageSpec)
	input := CompileInput{
		ProjectID: "project-1", DeliverySliceID: "page-messages",
		BuildManifest: BuildManifestRef{ID: "manifest-1", ContentHash: testHash(t, map[string]any{"manifest": 1})},
		Sources: []PinnedBuildSource{
			baseline,
			blueprint,
			pageSpec,
			prototype,
			pinnedSource(t, contracts.KindAPI, testAPIContract),
			pinnedSource(t, contracts.KindData, testDataContract),
			pinnedSource(t, contracts.KindPermission, testPermissionContract),
			pinnedSource(t, contracts.KindAIRuntime, testAIRuntimeContract),
			pinnedSource(t, contracts.KindDeployment, testDeploymentContract),
			pinnedSource(t, contracts.KindVerification, testVerificationContract),
		},
		FullStackTemplate: FullStackTemplateRef{
			ID: "fullstack-golden", ContentHash: testHash(t, map[string]any{"template": "golden"}), Certification: "approved", PolicyStatus: "active",
		},
		TemplateReleaseRefs: []TemplateReleaseRef{
			{ID: "web-release", ReleaseHash: testHash(t, map[string]any{"release": "web"}), Role: "web", Certification: "approved", PolicyStatus: "active"},
			{ID: "api-release", ReleaseHash: testHash(t, map[string]any{"release": "api"}), Role: "api", Certification: "approved", PolicyStatus: "active"},
		},
	}
	input.TemplateRuntime = compatibleTemplateRuntime(input.FullStackTemplate, input.TemplateReleaseRefs)
	return input
}

func compatibleTemplateRuntime(template FullStackTemplateRef, releases []TemplateReleaseRef) *TemplateRuntimeFacts {
	releaseByRole := map[string]TemplateReleaseRef{}
	for _, release := range releases {
		releaseByRole[release.Role] = release
	}
	web, api := releaseByRole["web"], releaseByRole["api"]
	return &TemplateRuntimeFacts{
		FullStackTemplateID: template.ID, FullStackTemplateHash: template.ContentHash,
		Layout: TemplateRuntimeLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "generated/client", DeploymentPath: "deploy/stack.yaml",
			TestPath: "tests", DatabaseEngine: "postgresql",
		},
		Components: []TemplateRuntimeComponent{
			{
				Role: "web", MountPath: "apps/web", ReleaseID: web.ID, ReleaseHash: web.ReleaseHash,
				ManifestSchemaVersion: "template-manifest/v1",
				Services:              []TemplateRuntimeService{{ID: "web", Role: "web", RootPath: "."}},
				Commands:              []string{"build-web"},
				Ports:                 []TemplateRuntimePort{{Name: "web", ServiceID: "web", Number: 3000, Protocol: "http"}},
				HealthChecks:          []TemplateRuntimeHealthCheck{{ID: "web-health", ServiceID: "web", PortName: "web", Path: "/"}},
				BuildOutputs:          []TemplateRuntimeBuildOutput{{ServiceID: "web", Path: "dist"}},
				EnvironmentVariables:  []TemplateRuntimeEnvironmentVariable{},
			},
			{
				Role: "api", MountPath: "services/api", ReleaseID: api.ID, ReleaseHash: api.ReleaseHash,
				ManifestSchemaVersion: "template-manifest/v1",
				Services:              []TemplateRuntimeService{{ID: "api", Role: "api", RootPath: "."}},
				Commands:              []string{"build-api", "migrate"},
				Ports:                 []TemplateRuntimePort{{Name: "api", ServiceID: "api", Number: 8080, Protocol: "http"}},
				HealthChecks:          []TemplateRuntimeHealthCheck{{ID: "api-health", ServiceID: "api", PortName: "api", Path: "/health"}},
				Migration:             &TemplateRuntimeMigration{ServiceID: "api", CommandName: "migrate"},
				BuildOutputs:          []TemplateRuntimeBuildOutput{{ServiceID: "api", Path: "bin"}},
				EnvironmentVariables:  []TemplateRuntimeEnvironmentVariable{},
			},
		},
	}
}

func readyPrototypeSource(t *testing.T, pageSpec PinnedBuildSource) PinnedBuildSource {
	t.Helper()
	states := []map[string]any{
		{"id": "state-ready", "key": "ready", "title": "Ready", "required": true, "fixtureIds": []string{}},
		{"id": "state-loading", "key": "loading", "title": "Loading", "required": true, "fixtureIds": []string{}},
		{"id": "state-empty", "key": "empty", "title": "Empty", "required": true, "fixtureIds": []string{}},
		{"id": "state-error", "key": "error", "title": "Error", "required": true, "fixtureIds": []string{}},
	}
	breakpoints := []map[string]any{
		{"id": "breakpoint-desktop", "name": "desktop", "minWidth": 1024, "viewportWidth": 1440, "viewportHeight": 900},
		{"id": "breakpoint-tablet", "name": "tablet", "minWidth": 768, "maxWidth": 1023, "viewportWidth": 768, "viewportHeight": 1024},
		{"id": "breakpoint-mobile", "name": "mobile", "minWidth": 0, "maxWidth": 767, "viewportWidth": 390, "viewportHeight": 844},
	}
	frames := make([]map[string]any, 0, len(states)*len(breakpoints))
	for _, state := range states {
		for _, breakpoint := range breakpoints {
			stateID := state["id"].(string)
			breakpointID := breakpoint["id"].(string)
			frames = append(frames, map[string]any{
				"id": stateID + "-" + breakpointID, "stateId": stateID,
				"breakpointId": breakpointID, "rootLayerId": "layer-root",
				"title": state["title"].(string) + " · " + breakpoint["name"].(string),
			})
		}
	}
	payload, err := json.Marshal(map[string]any{
		"pageSpecRevision": map[string]any{
			"artifactId": pageSpec.Ref.ArtifactID, "revisionId": pageSpec.Ref.RevisionID,
			"contentHash": pageSpec.Ref.ContentHash,
		},
		"exploratory": false,
		"states":      states,
		"breakpoints": breakpoints,
		"layers": map[string]any{
			"layer-root": map[string]any{
				"id": "layer-root", "childIds": []string{}, "kind": "frame", "name": "Messages",
				"layout": map[string]any{"x": 0, "y": 0, "width": 1440, "height": 900},
				"style":  map[string]any{}, "properties": map[string]any{},
				"requirementIds":         []string{"REQ-MESSAGES"},
				"acceptanceCriterionIds": []string{"AC-MESSAGES"}, "fieldMetadata": map[string]any{},
			},
		},
		"frames": frames, "overrides": []any{}, "interactions": []any{}, "fixtures": []any{},
		"tokenBindings": []any{}, "componentBindings": []any{}, "assets": []any{}, "traceLinks": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pinnedSource(t, "prototype", string(payload))
}

func pinnedSource(t *testing.T, kind, payload string) PinnedBuildSource {
	t.Helper()
	content := json.RawMessage(payload)
	return PinnedBuildSource{
		Ref: ExactRevisionRef{
			Kind: kind, Purpose: kind, Required: true, ArtifactID: "artifact-" + kind,
			RevisionID: "revision-" + kind, ContentHash: testHash(t, content), ApprovalStatus: "approved",
		},
		Content: content,
	}
}

func canonicalRequirementBaselineSource(t *testing.T, payload string) PinnedBuildSource {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal([]byte(payload), &content); err != nil {
		t.Fatal(err)
	}
	canonicalizeRequirementBaseline(t, content)
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return pinnedSource(t, "requirement_baseline", string(encoded))
}

func canonicalizeRequirementBaseline(t *testing.T, content map[string]any) {
	t.Helper()
	content["baselineHash"] = ""
	content["baselineHash"] = testHash(t, content)
}

func replaceCompileInputSource(t *testing.T, input *CompileInput, kind, payload string) {
	t.Helper()
	for index := range input.Sources {
		if input.Sources[index].Ref.Kind == kind {
			if kind == "requirement_baseline" {
				input.Sources[index] = canonicalRequirementBaselineSource(t, payload)
			} else {
				input.Sources[index] = pinnedSource(t, kind, payload)
			}
			if kind == "page_spec" {
				replacePrototypeSourceForPageSpec(t, input, input.Sources[index])
			}
			return
		}
	}
	t.Fatalf("compile input source %q was not found", kind)
}

func replacePrototypeSourceForPageSpec(t *testing.T, input *CompileInput, pageSpec PinnedBuildSource) {
	t.Helper()
	for index := range input.Sources {
		if input.Sources[index].Ref.Kind == "prototype" {
			input.Sources[index] = readyPrototypeSource(t, pageSpec)
			return
		}
	}
	t.Fatal("compile input prototype source was not found")
}

func mutateCompileInputSource(t *testing.T, input *CompileInput, kind string, mutate func(map[string]any)) {
	t.Helper()
	for index := range input.Sources {
		if input.Sources[index].Ref.Kind != kind {
			continue
		}
		var content map[string]any
		if err := json.Unmarshal(input.Sources[index].Content, &content); err != nil {
			t.Fatal(err)
		}
		mutate(content)
		if kind == "requirement_baseline" {
			canonicalizeRequirementBaseline(t, content)
		}
		payload, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		input.Sources[index] = pinnedSource(t, kind, string(payload))
		return
	}
	t.Fatalf("compile input source %q was not found", kind)
}

func mutatePageSpecAndSyncPrototype(t *testing.T, input *CompileInput, mutate func(map[string]any)) {
	t.Helper()
	mutateCompileInputSource(t, input, "page_spec", mutate)
	pageSpecRef := compileInputSourceRef(t, *input, "page_spec")
	mutateCompileInputSource(t, input, "prototype", func(content map[string]any) {
		content["pageSpecRevision"] = map[string]any{
			"artifactId": pageSpecRef.ArtifactID, "revisionId": pageSpecRef.RevisionID,
			"contentHash": pageSpecRef.ContentHash,
		}
	})
}

func compileInputSourceRef(t *testing.T, input CompileInput, kind string) ExactRevisionRef {
	t.Helper()
	for _, source := range input.Sources {
		if source.Ref.Kind == kind {
			return source.Ref
		}
	}
	t.Fatalf("compile input source %q was not found", kind)
	return ExactRevisionRef{}
}

func testHash(t *testing.T, value any) string {
	t.Helper()
	hash, err := domain.CanonicalHash(value)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func containsGap(gaps []BuildGap, code, source string) bool {
	for _, gap := range gaps {
		if gap.Code == code && (source == "" || gap.SourceID == source) {
			return true
		}
	}
	return false
}

func containsGapMessage(gaps []BuildGap, code, messagePart string) bool {
	for _, gap := range gaps {
		if gap.Code == code && strings.Contains(gap.Message, messagePart) {
			return true
		}
	}
	return false
}

func gapIDExists(gaps []BuildGap, id string) bool {
	for _, gap := range gaps {
		if gap.ID == id {
			return true
		}
	}
	return false
}

const testAPIContract = `{
  "schemaVersion":"api-contract/v2","openapi":"3.1.0","info":{"title":"Messages API","version":"1"},
  "paths":{"/messages":{"get":{"operationId":"listMessages","responses":{"200":{"description":"ok"}}}}},
  "components":{"schemas":{"ProviderGenerateRequest":{"type":"object"},"ProviderEvent":{"type":"object"}}}
}`

const testDataContract = `{
  "schemaVersion":"data-contract/v2","entities":[{"id":"Message","tableName":"messages","fields":[
    {"id":"id","name":"id","type":"uuid","nullable":false},{"id":"projectId","name":"project_id","type":"uuid","nullable":false}
  ],"primaryKey":["id"],"indexes":[],"foreignKeys":[],"tenantScope":{"mode":"project","fieldId":"projectId"}}],
  "migrationPolicy":{"tool":"goose","directory":"migrations","applyCommandId":"migrate","rollbackPolicy":"forward-only"}
}`

const testPermissionContract = `{
  "schemaVersion":"permission-contract/v1","identity":{"subjectClaim":"sub","authentication":"session"},
  "tenant":{"mode":"project","claim":"project_id"},"roles":[{"id":"message-reader"}],
  "policies":[{"id":"read","roles":["message-reader"],"resource":"messages","actions":["read"],"tenantScoped":true,"effect":"allow"}]
}`

const testAIRuntimeContract = `{
  "schemaVersion":"ai-runtime-contract/v2","applicationProfile":"messages/v1",
  "providerPolicy":{"policyId":"default","modelClass":"reasoning","fallbackAllowed":false,"profilePinned":true},
  "providerPort":{"portId":"generated-ai","protocol":"worksflow-generated-ai/v1","contractKind":"api_contract","requestSchemaRef":"#/components/schemas/ProviderGenerateRequest","eventSchemaRef":"#/components/schemas/ProviderEvent","streamingRequired":true,"cancellationPropagation":true,"usageRequired":true},
  "gateway":{"plane":"generated-application","endpointEnvironmentVariable":"AI_GATEWAY_URL","capabilityEnvironmentVariable":"AI_GATEWAY_CAPABILITY_FILE","capabilityMode":"file","providerCredentials":"gateway-only","providerKeyExposure":"forbidden","tenantScoped":true,"auditRequired":true},
  "conversation":{"persistence":"required","tenantScoped":true,"messageRoles":["user","assistant"]},
  "run":{"persistent":true,"idempotencyRequired":true,"statusValues":["queued","running","completed","failed","cancelled"]},
  "streaming":{"transport":"sse","eventTypes":["run.started","output.delta","run.completed","run.failed","run.cancelled"],"eventSchemaRef":"run-event-schema.json","durable":true,"resumeCursor":true,"cursorField":"sequence","resumeHeader":"Last-Event-ID"},
  "cancellation":{"supported":true,"idempotent":true,"terminalStatus":"cancelled"},
  "retry":{"reasonRequired":true,"maxAttempts":3,"createsNewAttempt":true,"supersedeOnModelChange":true},
  "rateLimit":{"scope":"tenant-actor","requests":60,"windowSeconds":60,"burst":10,"retryAfterRequired":true,"failClosed":true},
  "limits":{"maxInputBytes":1000,"maxOutputBytes":1000,"timeoutSeconds":30},
  "retention":{"messageDays":30,"runDays":30,"eventDays":14,"redactionRequired":true},
  "audit":{"requiredFields":["tenantId","projectId","actorId","conversationId","runId","providerPolicyId","requestId","outcome","usage"],"promptContent":"redacted","responseContent":"redacted","retentionDays":30}
}`

const testDeploymentContract = `{
  "schemaVersion":"deployment-contract/v1","services":[
    {"id":"web","role":"web","sourceRoot":"apps/web","portName":"web","healthCheckId":"web-health","buildOutput":"apps/web/dist"},
    {"id":"api","role":"api","sourceRoot":"services/api","portName":"api","healthCheckId":"api-health","buildOutput":"services/api/bin"}
  ],"ports":[{"name":"web","containerPort":3000,"protocol":"http"},{"name":"api","containerPort":8080,"protocol":"http"}],
  "environmentVariables":[],"healthChecks":[
    {"id":"web-health","portName":"web","method":"GET","path":"/","expectedStatuses":[200]},
    {"id":"api-health","portName":"api","method":"GET","path":"/health","expectedStatuses":[200]}
  ],"migration":{"required":true,"commandId":"migrate","beforeTraffic":true},
  "releasePolicy":{"strategy":"rolling","rollbackRequired":true,"immutableImages":true}
}`

const testVerificationContract = `{
  "schemaVersion":"verification-contract/v1","oracles":[
    {"id":"verify-messages","acceptanceCriterionIds":["AC-MESSAGES"],"kind":"contract","target":"GET /messages","commandId":"test-contract","blocking":true}
  ]
}`
