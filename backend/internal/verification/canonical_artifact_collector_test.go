package verification

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestCanonicalArtifactCollectorReadsOnlyExactTrustedManifestOutput(t *testing.T) {
	compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
	if err != nil {
		t.Fatal(err)
	}
	spec := CanonicalExecutionSpec{
		RunID: uuid.NewString(), AttemptID: uuid.NewString(), AttemptFenceEpoch: 1,
		PlanID: uuid.NewString(), PlanHash: compiled.PlanHash, Content: compiled.Content,
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": CanonicalReleaseArtifactManifestSchemaVersion,
		"artifacts": []CanonicalReleaseArtifact{{
			ID: "api-image", Kind: "oci-image", Store: "oci",
			Ref: "registry.example/api@" + hashFixture("collector-api"), ContentHash: hashFixture("collector-api"),
			MediaType: "application/vnd.oci.image.manifest.v1+json", ByteSize: 4096,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": "verification-check-log/v1", "stream": "stdout",
		"checkId": "release-artifacts", "value": string(manifest),
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := newVerificationContentStoreFake()
	reference, err := contents.PutPending(
		context.Background(), compiled.Content.ProjectID, verificationCheckLogAggregate, spec.AttemptID, 1, payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := contents.Finalize(context.Background(), reference.ID); err != nil {
		t.Fatal(err)
	}
	collector, err := NewContentCanonicalArtifactCollector(contents)
	if err != nil {
		t.Fatal(err)
	}
	checks := []CheckResult{{
		ID: "release-artifacts", Kind: "release-manifest", Required: true,
		Status: CheckPassed, AttemptID: spec.AttemptID,
		Stdout: &BlobReference{
			Store: "content", OwnerID: spec.AttemptID, Ref: reference.ID,
			ContentHash: reference.ContentHash, ByteSize: reference.ByteSize,
		},
	}}
	artifacts, err := collector.CollectReleaseArtifacts(context.Background(), spec, checks)
	if err != nil || len(artifacts) != 1 || artifacts[0].ID != "api-image" {
		t.Fatalf("collected artifacts = %#v, %v", artifacts, err)
	}
	checks[0].Stdout.ContentHash = hashFixture("wrong-log")
	if _, err := collector.CollectReleaseArtifacts(context.Background(), spec, checks); err == nil {
		t.Fatal("drifted release manifest log was accepted")
	}
}
