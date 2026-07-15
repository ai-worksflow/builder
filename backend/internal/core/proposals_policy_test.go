package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOnlyExactManifestBaseMayBeAnUnapprovedSource(t *testing.T) {
	anchor := "brief"
	base := VersionRef{
		ArtifactID:  "project-brief",
		RevisionID:  "project-brief-r1",
		ContentHash: "sha256:project-brief-r1",
		AnchorID:    &anchor,
	}
	if !manifestSourceIsExactBase(&base, base) {
		t.Fatal("the exact base revision must be admissible as the editable manifest source")
	}
	if manifestSourceIsExactBase(nil, base) {
		t.Fatal("an unapproved formal source without a base revision must stay blocked")
	}
	for name, source := range map[string]VersionRef{
		"artifact": {ArtifactID: "other", RevisionID: base.RevisionID, ContentHash: base.ContentHash, AnchorID: base.AnchorID},
		"revision": {ArtifactID: base.ArtifactID, RevisionID: "other", ContentHash: base.ContentHash, AnchorID: base.AnchorID},
		"hash":     {ArtifactID: base.ArtifactID, RevisionID: base.RevisionID, ContentHash: "sha256:other", AnchorID: base.AnchorID},
		"anchor":   {ArtifactID: base.ArtifactID, RevisionID: base.RevisionID, ContentHash: base.ContentHash},
	} {
		t.Run(name, func(t *testing.T) {
			if manifestSourceIsExactBase(&base, source) {
				t.Fatalf("mismatched %s source bypassed the formal approval policy", name)
			}
		})
	}
}

func TestProposalPatchedContentMustPassArtifactValidationBeforeApply(t *testing.T) {
	t.Parallel()
	if err := validateProposalPatchedContent("prototype", canonicalPrototypeValidationPayload()); err != nil {
		t.Fatalf("canonical Prototype patch was rejected: %v", err)
	}

	var incomplete map[string]any
	if err := json.Unmarshal(canonicalPrototypeValidationPayload(), &incomplete); err != nil {
		t.Fatal(err)
	}
	delete(incomplete, "tokenBindings")
	payload, err := json.Marshal(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	err = validateProposalPatchedContent("prototype", payload)
	if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "prototype.array_contract") ||
		!strings.Contains(err.Error(), "$.tokenBindings") {
		t.Fatalf("invalid Prototype patch did not fail closed with validation evidence: %v", err)
	}
}
