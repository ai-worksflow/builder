package workflow

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestQualityCompletionMaterialSQLUsesNarrowPlanCapabilityAndSerializableAdmission(t *testing.T) {
	if strings.Count(postgresQualityCompletionMaterialPlanQuery, "?") != 3 ||
		!strings.Contains(postgresQualityCompletionMaterialPlanQuery,
			"resolve_workflow_v3_quality_completion_material_plan_v1") ||
		!strings.Contains(postgresQualityCompletionMaterialPlanQuery, "CAST(? AS uuid)") {
		t.Fatalf("unexpected Quality completion material-plan contract: %s", postgresQualityCompletionMaterialPlanQuery)
	}
	for _, privateRelation := range []string{
		"qualification_policy_authorities", "qualification_policy_review_defaults",
		"canonical_review_approval_receipts", "workflow_v3_quality_completion_materials",
	} {
		if strings.Contains(postgresQualityCompletionMaterialPlanQuery, privateRelation) {
			t.Fatalf("material prefetch directly reads private relation %s", privateRelation)
		}
	}
	if strings.Count(postgresQualityCompletionMaterialAdmissionQuery, "?") != 8 ||
		!strings.Contains(postgresQualityCompletionMaterialAdmissionQuery,
			"admit_workflow_v3_quality_completion_materials_v1") ||
		!strings.Contains(postgresQualityCompletionMaterialAdmissionQuery, "CAST(? AS jsonb)") {
		t.Fatalf("unexpected Quality completion material-admission contract: %s", postgresQualityCompletionMaterialAdmissionQuery)
	}
	if options := qualityCompletionSerializableOptions(nil); len(options) != 0 {
		t.Fatalf("ordinary workflow transaction options = %#v, want default isolation", options)
	}
	options := qualityCompletionSerializableOptions(&QualityCompletionPrecommitMutation{})
	if len(options) != 1 || options[0] == nil || options[0].Isolation != sql.LevelSerializable || options[0].ReadOnly {
		t.Fatalf("Quality completion transaction options = %#v, want read-write SERIALIZABLE", options)
	}
}

func TestDecodeQualityCompletionMaterialPlanRequiresExactDeterministicClosure(t *testing.T) {
	plan := validQualityCompletionMaterialPlan()
	raw := mustQualityCompletionMaterialPlanJSON(t, plan)
	decoded, err := decodeQualityCompletionMaterialPlan(raw)
	if err != nil {
		t.Fatalf("decode exact plan: %v", err)
	}
	if len(decoded.InputManifests) != 2 || len(decoded.Revisions) != 2 || len(decoded.ReviewReceipts) != 1 {
		t.Fatalf("decoded plan lost members: %+v", decoded)
	}

	tests := []struct {
		name   string
		mutate func(*qualityCompletionMaterialPlan)
	}{
		{name: "nil receipts", mutate: func(value *qualityCompletionMaterialPlan) { value.ReviewReceipts = nil }},
		{name: "unordered manifests", mutate: func(value *qualityCompletionMaterialPlan) {
			value.InputManifests[0], value.InputManifests[1] = value.InputManifests[1], value.InputManifests[0]
		}},
		{name: "duplicate revision", mutate: func(value *qualityCompletionMaterialPlan) {
			value.Revisions[1].RevisionID = value.Revisions[0].RevisionID
		}},
		{name: "unordered receipts", mutate: func(value *qualityCompletionMaterialPlan) {
			value.ReviewReceipts = append(value.ReviewReceipts,
				qualityCompletionReviewReceiptPlan{
					ReviewRequestID: "11111111-1111-4111-8111-111111111111",
					RawBytesHex:     hex.EncodeToString([]byte(`{"receipt":2}`)),
				})
		}},
		{name: "widened content reference", mutate: func(value *qualityCompletionMaterialPlan) {
			value.BuildManifest.ContentStore = " " + value.BuildManifest.ContentStore
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validQualityCompletionMaterialPlan()
			test.mutate(&candidate)
			_, err := decodeQualityCompletionMaterialPlan(mustQualityCompletionMaterialPlanJSON(t, candidate))
			if !errors.Is(err, ErrCASConflict) || !errors.Is(err, ErrQualityCompletionPrecommitCorrupt) {
				t.Fatalf("invalid plan error = %v", err)
			}
		})
	}

	widened := append([]byte(nil), raw...)
	widened[len(widened)-1] = ','
	widened = append(widened, []byte(`"extra":true}`)...)
	if _, err := decodeQualityCompletionMaterialPlan(widened); !errors.Is(err, ErrQualityCompletionPrecommitCorrupt) {
		t.Fatalf("widened material plan error = %v", err)
	}
}

func TestQualityCompletionMaterialHexAndContentPrefetchAreBoundedAndCopied(t *testing.T) {
	raw := []byte(`{"value":true}`)
	decoded, err := decodeQualityCompletionMaterialPlanHex("test", hex.EncodeToString(raw), len(raw))
	if err != nil || string(decoded) != string(raw) {
		t.Fatalf("decode bounded raw material = %q, %v", decoded, err)
	}
	if _, err := decodeQualityCompletionMaterialPlanHex("test", strings.ToUpper(hex.EncodeToString(raw)), len(raw)); !errors.Is(err, ErrQualityCompletionPrecommitCorrupt) {
		t.Fatalf("uppercase raw hexadecimal error = %v", err)
	}
	if _, err := decodeQualityCompletionMaterialPlanHex("test", hex.EncodeToString(raw), len(raw)-1); !errors.Is(err, ErrQualityCompletionPrecommitCorrupt) {
		t.Fatalf("oversized raw material error = %v", err)
	}

	content := &qualityCompletionMaterialContentStoreFake{raw: raw}
	store := &GORMStore{content: content}
	material, err := store.getQualityCompletionMaterial(context.Background(), "test",
		qualityCompletionMaterialContentPlan{
			ContentStore: "memory", ContentRef: "ref", ContentHash: "sha256:" + strings.Repeat("a", 64),
		}, len(raw))
	if err != nil {
		t.Fatalf("prefetch content material: %v", err)
	}
	material[0] = 'X'
	if raw[0] == 'X' || content.calls != 1 {
		t.Fatalf("prefetch retained an alias or skipped ContentStore.Get: raw=%q calls=%d", raw, content.calls)
	}
}

func TestQualityCompletionMaterialPlanBuildsExactAdmissionBundle(t *testing.T) {
	plan := validQualityCompletionMaterialPlan()
	nodeInput, err := hex.DecodeString(plan.NodeInputRawBytesHex)
	if err != nil {
		t.Fatal(err)
	}
	content := &qualityCompletionMaterialMapStore{values: map[string][]byte{
		"build-manifest": []byte(`{"kind":"build-manifest"}`),
		"build-contract": []byte(`{"kind":"build-contract"}`),
		"predecessor":    []byte(`{"kind":"predecessor"}`),
		"run":            []byte(`{"kind":"run"}`),
		"source":         []byte(`{"kind":"source"}`),
		"workspace":      []byte(`{"kind":"workspace"}`),
	}}
	store := &GORMStore{content: content}
	admission, err := store.prefetchQualityCompletionMaterialPlan(
		context.Background(), &QualityCompletionPrecommitMutation{GateInputCanonical: nodeInput}, plan,
	)
	if err != nil {
		t.Fatalf("build exact admission bundle: %v", err)
	}
	wantCalls := []string{"build-manifest", "build-contract", "predecessor", "run", "source", "workspace"}
	if strings.Join(content.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("ContentStore prefetch order = %v, want %v", content.calls, wantCalls)
	}
	if !json.Valid(admission.definitionRaw) || !json.Valid(admission.runScopeRaw) ||
		string(admission.nodeInputRaw) != string(nodeInput) ||
		string(admission.buildManifestRaw) != string(content.values["build-manifest"]) ||
		string(admission.buildContractRaw) != string(content.values["build-contract"]) {
		t.Fatalf("admission root materials differ: %+v", admission)
	}
	var bundle qualityCompletionMaterialBundle
	if err := json.Unmarshal(admission.bundle, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.InputManifests) != 2 || len(bundle.Revisions) != 2 || len(bundle.ReviewReceipts) != 1 ||
		bundle.InputManifests[0].Role != "predecessor" || bundle.InputManifests[1].Role != "run" ||
		bundle.Revisions[0].Purpose != "governed-source" || bundle.Revisions[1].Purpose != "workspace-target" ||
		bundle.ReviewReceipts[0].ReviewRequestID != plan.ReviewReceipts[0].ReviewRequestID {
		t.Fatalf("admission bundle lost deterministic plan closure: %+v", bundle)
	}
	if bundle.InputManifests[0].RawBytesHex != hex.EncodeToString(content.values["predecessor"]) ||
		bundle.Revisions[1].RawBytesHex != hex.EncodeToString(content.values["workspace"]) ||
		bundle.ReviewReceipts[0].RawBytesHex != plan.ReviewReceipts[0].RawBytesHex {
		t.Fatalf("admission bundle raw materials differ: %+v", bundle)
	}
	content.values["build-manifest"][0] = 'X'
	if admission.buildManifestRaw[0] == 'X' {
		t.Fatal("admission retained an alias to ContentStore bytes")
	}
}

type qualityCompletionMaterialContentStoreFake struct {
	raw   []byte
	calls int
}

type qualityCompletionMaterialMapStore struct {
	values map[string][]byte
	calls  []string
}

func (*qualityCompletionMaterialMapStore) Put(context.Context, string, string, []byte) (string, string, string, error) {
	return "", "", "", errors.New("unexpected Put")
}

func (store *qualityCompletionMaterialMapStore) Get(_ context.Context, contentStore, contentRef, _ string) ([]byte, error) {
	if contentStore != "memory" {
		return nil, errors.New("unexpected content store")
	}
	value, exists := store.values[contentRef]
	if !exists {
		return nil, errors.New("unexpected content ref")
	}
	store.calls = append(store.calls, contentRef)
	return value, nil
}

func (*qualityCompletionMaterialContentStoreFake) Put(context.Context, string, string, []byte) (string, string, string, error) {
	return "", "", "", errors.New("unexpected Put")
}

func (store *qualityCompletionMaterialContentStoreFake) Get(_ context.Context, contentStore, contentRef, contentHash string) ([]byte, error) {
	store.calls++
	if contentStore != "memory" || contentRef != "ref" || contentHash != "sha256:"+strings.Repeat("a", 64) {
		return nil, errors.New("unexpected content reference")
	}
	return store.raw, nil
}

func validQualityCompletionMaterialPlan() qualityCompletionMaterialPlan {
	return qualityCompletionMaterialPlan{
		DefinitionRawBytesHex: hex.EncodeToString([]byte(`{"nodes":[]}`)),
		RunScopeRawBytesHex:   hex.EncodeToString([]byte(`{"scope":"all"}`)),
		NodeInputRawBytesHex:  hex.EncodeToString([]byte(`{"bindings":[],"hash":"sha256:` + strings.Repeat("a", 64) + `"}`)),
		BuildManifest: qualityCompletionMaterialContentPlan{
			ContentStore: "memory", ContentRef: "build-manifest", ContentHash: "sha256:" + strings.Repeat("1", 64),
		},
		BuildContract: qualityCompletionMaterialContentPlan{
			ContentStore: "memory", ContentRef: "build-contract", ContentHash: "sha256:" + strings.Repeat("2", 64),
		},
		InputManifests: []qualityCompletionManifestMaterialPlan{
			{
				ManifestID: "11111111-1111-4111-8111-111111111111", Role: "predecessor",
				ContentStore: "memory", ContentRef: "predecessor", ContentHash: "sha256:" + strings.Repeat("3", 64),
			},
			{
				ManifestID: "22222222-2222-4222-8222-222222222222", Role: "run",
				ContentStore: "memory", ContentRef: "run", ContentHash: "sha256:" + strings.Repeat("4", 64),
			},
		},
		Revisions: []qualityCompletionRevisionMaterialPlan{
			{
				Purpose: "governed-source", RevisionID: "33333333-3333-4333-8333-333333333333",
				ContentStore: "memory", ContentRef: "source", ContentHash: "sha256:" + strings.Repeat("5", 64),
			},
			{
				Purpose: "workspace-target", RevisionID: "44444444-4444-4444-8444-444444444444",
				ContentStore: "memory", ContentRef: "workspace", ContentHash: "sha256:" + strings.Repeat("6", 64),
			},
		},
		ReviewReceipts: []qualityCompletionReviewReceiptPlan{
			{
				ReviewRequestID: "55555555-5555-4555-8555-555555555555",
				RawBytesHex:     hex.EncodeToString([]byte(`{"receipt":1}`)),
			},
		},
	}
}

func mustQualityCompletionMaterialPlanJSON(t *testing.T, plan qualityCompletionMaterialPlan) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
