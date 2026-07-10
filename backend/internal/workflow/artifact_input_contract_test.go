package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestArtifactInputBaseLineageAndApprovalPolicy(t *testing.T) {
	ref := platformRef("unapproved-project-brief")
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), uuid.NewString(), "workflow_start", "", &ref,
		[]domain.ManifestSource{{Ref: ref, Purpose: "project_brief"}},
		json.RawMessage(`{"entry":"project_brief"}`), "workflow-input/v1",
		uuid.NewString(), time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	refs := artifactInputRefs(manifest)
	if len(refs) != 1 || !refs[0].Equal(ref) {
		t.Fatalf("base and matching source were not deduplicated as one exact input: %+v", refs)
	}

	entryConfig := domain.ArtifactInputNodeConfig{
		AllowedTypes:    []domain.ArtifactType{domain.ArtifactDocument},
		RequireApproved: false, MinimumArtifacts: 1,
	}
	artifactType, err := validateArtifactInputRevision(
		entryConfig, manifest.ProjectID, manifest.ProjectID, "in_review", "project_brief",
	)
	if err != nil || artifactType != domain.ArtifactDocument {
		t.Fatalf("unapproved Project Brief entry was rejected: type=%q err=%v", artifactType, err)
	}

	formalConfig := entryConfig
	formalConfig.RequireApproved = true
	if _, err := validateArtifactInputRevision(
		formalConfig, manifest.ProjectID, manifest.ProjectID, "in_review", "project_brief",
	); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("custom requireApproved input accepted an unapproved revision: %v", err)
	}

	accepted := []map[string]any{{
		"ref": ref, "artifactType": domain.ArtifactDocument,
		"kind": "project_brief", "status": "in_review",
	}}
	output, err := artifactInputOutput(manifest, refs, accepted)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := artifactRefsFromNodeOutput(output)
	if err != nil || len(lineage) != 1 || !lineage[0].Equal(ref) {
		t.Fatalf("validated ArtifactInput output lost exact revision lineage: refs=%+v err=%v output=%s", lineage, err, output)
	}
}
