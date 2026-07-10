package core

import "testing"

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
