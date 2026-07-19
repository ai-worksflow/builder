package templateauthority

import (
	"context"
	"encoding/json"
	"testing"
)

type sbomFixture struct {
	image          imageFixture
	request        ServiceSBOMRequest
	referrer       ExactReference
	document       manifestDocument
	config         []byte
	statement      map[string]any
	statementBytes []byte
	predicate      map[string]any
}

func newSBOMFixture(t *testing.T, serviceID string) sbomFixture {
	t.Helper()
	image := newImageFixture(t)
	predicate := map[string]any{
		"spdxVersion":       "SPDX-2.3",
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              serviceID + " SBOM",
		"documentNamespace": "https://sbom.example.com/" + serviceID + "/v1",
		"creationInfo": map[string]any{
			"created":  "2026-07-18T00:00:00Z",
			"creators": []string{"Tool: worksflow-template-authority"},
		},
		"packages": []any{map[string]any{
			"SPDXID":           "SPDXRef-Package-Root",
			"name":             serviceID,
			"downloadLocation": "https://packages.example.com/" + serviceID,
			"licenseConcluded": "Apache-2.0",
		}},
	}
	statement := map[string]any{
		"_type": InTotoStatementV1,
		"subject": []any{map[string]any{
			"name":   image.reference,
			"digest": map[string]any{"sha256": image.referenceKey.Digest[len("sha256:"):]},
		}},
		"predicateType": PredicateTypeSPDX,
		"predicate":     predicate,
	}
	fixture := sbomFixture{image: image, config: []byte(`{}`), statement: statement, predicate: predicate}
	fixture.rebuildReferrer(t)
	fixture.request = ServiceSBOMRequest{
		ServiceID:         serviceID,
		ImageReference:    image.reference,
		ReferrerReference: fixture.referrer.String(),
	}
	return fixture
}

func (fixture *sbomFixture) rebuildReferrer(t *testing.T) {
	t.Helper()
	fixture.statementBytes = mustJSON(t, fixture.statement)
	fixture.document = manifestDocument{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIImageManifest,
		ArtifactType:  MediaTypeInTotoStatement,
		Config:        descriptorFor(MediaTypeOCIEmptyConfig, fixture.config),
		Layers:        []Descriptor{descriptorFor(MediaTypeInTotoStatement, fixture.statementBytes)},
		Subject: &Descriptor{
			MediaType: MediaTypeOCIImageManifest,
			Digest:    fixture.image.referenceKey.Digest,
			Size:      int64(len(fixture.image.client.manifests[fixture.image.reference])),
		},
	}
	manifest := mustJSON(t, fixture.document)
	fixture.referrer = ExactReference{Host: testRegistry, Repository: testRepository, Digest: sha256Digest(manifest)}
	fixture.image.client.manifests[fixture.referrer.String()] = manifest
	fixture.image.client.blobs[fixture.document.Config.Digest] = append([]byte(nil), fixture.config...)
	fixture.image.client.blobs[fixture.document.Layers[0].Digest] = append([]byte(nil), fixture.statementBytes...)
	fixture.request.ReferrerReference = fixture.referrer.String()
}

func newTestSBOMVerifier(t *testing.T, client RegistryClient, limits Limits) *SBOMVerifier {
	t.Helper()
	oci := newTestOCIVerifier(t, client, limits)
	verifier, err := NewSBOMVerifier(oci)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func TestSBOMVerifierVerifiesExactReferrerAndSPDXBytes(t *testing.T) {
	fixture := newSBOMFixture(t, "api")
	verified, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if verified.ServiceID != "api" || verified.ImageReference != fixture.image.referenceKey || verified.ReferrerReference != fixture.referrer {
		t.Fatalf("unexpected verified identity: %#v", verified)
	}
	if verified.ReferrerManifest.Digest != fixture.referrer.Digest || verified.Statement.Digest != fixture.document.Layers[0].Digest {
		t.Fatalf("unexpected referrer descriptors: %#v", verified)
	}
	if verified.StatementDigest != sha256Digest(fixture.statementBytes) || verified.PredicateDigest != sha256Digest(mustJSON(t, fixture.predicate)) {
		t.Fatalf("unexpected statement hashes: %#v", verified)
	}
	if verified.SPDXVersion != "SPDX-2.3" || verified.DocumentNamespace != "https://sbom.example.com/api/v1" || verified.CanonicalEvidenceHash == "" {
		t.Fatalf("unexpected SPDX projection: %#v", verified)
	}
}

func TestSBOMAggregateSortsByServiceAndProducesDeterministicHash(t *testing.T) {
	fixture := newSBOMFixture(t, "shared")
	api := fixture.request
	api.ServiceID = "api"
	web := fixture.request
	web.ServiceID = "web"
	verifier := newTestSBOMVerifier(t, fixture.image.client, Limits{})
	first, err := verifier.VerifyAggregate(context.Background(), []ServiceSBOMRequest{web, api})
	if err != nil {
		t.Fatal(err)
	}
	second, err := verifier.VerifyAggregate(context.Background(), []ServiceSBOMRequest{api, web})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Services) != 2 || first.Services[0].ServiceID != "api" || first.Services[1].ServiceID != "web" {
		t.Fatalf("services are not canonically sorted: %#v", first.Services)
	}
	if first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("aggregate digest is not deterministic: %q != %q", first.Digest, second.Digest)
	}
	_, err = verifier.VerifyAggregate(context.Background(), []ServiceSBOMRequest{api, api})
	requireCode(t, err, CodeInvalidSBOM)
}

func TestSBOMVerifierRejectsReferrerAndStatementSubjectMismatch(t *testing.T) {
	t.Run("referrer", func(t *testing.T) {
		fixture := newSBOMFixture(t, "api")
		fixture.document.Subject.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		manifest := mustJSON(t, fixture.document)
		fixture.referrer.Digest = sha256Digest(manifest)
		fixture.request.ReferrerReference = fixture.referrer.String()
		fixture.image.client.manifests[fixture.referrer.String()] = manifest
		_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
		requireCode(t, err, CodeInvalidSBOM)
	})
	t.Run("in-toto", func(t *testing.T) {
		fixture := newSBOMFixture(t, "api")
		subjects := fixture.statement["subject"].([]any)
		subject := subjects[0].(map[string]any)
		subject["digest"] = map[string]any{"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
		fixture.rebuildReferrer(t)
		_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
		requireCode(t, err, CodeInvalidSBOM)
	})
}

func TestSBOMVerifierRejectsPredicateAndSPDXFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sbomFixture)
	}{
		{name: "predicate type", mutate: func(fixture *sbomFixture) { fixture.statement["predicateType"] = "https://cyclonedx.org/bom" }},
		{name: "SPDX version", mutate: func(fixture *sbomFixture) { fixture.predicate["spdxVersion"] = "SPDX-2.2" }},
		{name: "missing packages", mutate: func(fixture *sbomFixture) { fixture.predicate["packages"] = []any{} }},
		{name: "NONE", mutate: func(fixture *sbomFixture) {
			packages := fixture.predicate["packages"].([]any)
			packages[0].(map[string]any)["licenseConcluded"] = "NONE"
		}},
		{name: "NOASSERTION", mutate: func(fixture *sbomFixture) {
			packages := fixture.predicate["packages"].([]any)
			packages[0].(map[string]any)["downloadLocation"] = "NOASSERTION"
		}},
		{name: "invalid namespace", mutate: func(fixture *sbomFixture) { fixture.predicate["documentNamespace"] = "relative" }},
		{name: "invalid package ID", mutate: func(fixture *sbomFixture) {
			packages := fixture.predicate["packages"].([]any)
			packages[0].(map[string]any)["SPDXID"] = "Package"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSBOMFixture(t, "api")
			test.mutate(&fixture)
			fixture.rebuildReferrer(t)
			_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
			requireCode(t, err, CodeInvalidSBOM)
		})
	}
}

func TestSBOMVerifierRehashesReferrerManifestConfigAndLayer(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*sbomFixture)
	}{
		{name: "manifest", tamper: func(fixture *sbomFixture) {
			fixture.image.client.manifests[fixture.referrer.String()] = append(fixture.image.client.manifests[fixture.referrer.String()], ' ')
		}},
		{name: "config", tamper: func(fixture *sbomFixture) {
			fixture.image.client.blobs[fixture.document.Config.Digest] = []byte(`{"not":"empty"}`)
		}},
		{name: "layer", tamper: func(fixture *sbomFixture) {
			fixture.image.client.blobs[fixture.document.Layers[0].Digest] = []byte(`{"tampered":true}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSBOMFixture(t, "api")
			test.tamper(&fixture)
			_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
			requireCode(t, err, CodeIntegrityMismatch)
		})
	}
}

func TestSBOMVerifierRejectsInvalidReferrerMediaConfigAndRepository(t *testing.T) {
	t.Run("artifact media", func(t *testing.T) {
		fixture := newSBOMFixture(t, "api")
		fixture.document.ArtifactType = "application/example"
		manifest := mustJSON(t, fixture.document)
		fixture.referrer.Digest = sha256Digest(manifest)
		fixture.request.ReferrerReference = fixture.referrer.String()
		fixture.image.client.manifests[fixture.referrer.String()] = manifest
		_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
		requireCode(t, err, CodeUnsupportedMediaType)
	})
	t.Run("nonempty config", func(t *testing.T) {
		fixture := newSBOMFixture(t, "api")
		fixture.config = []byte(`{"created":"now"}`)
		fixture.rebuildReferrer(t)
		_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
		requireCode(t, err, CodeInvalidSBOM)
	})
	t.Run("different repository", func(t *testing.T) {
		fixture := newSBOMFixture(t, "api")
		other := fixture.referrer
		other.Repository = "worksflow/other"
		fixture.request.ReferrerReference = other.String()
		verifier, err := NewOCIVerifier(fixture.image.client, RegistryPolicy{Repositories: []RepositoryRule{
			{Host: testRegistry, Repository: testRepository},
			{Host: testRegistry, Repository: "worksflow/other"},
		}}, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		sbom, err := NewSBOMVerifier(verifier)
		if err != nil {
			t.Fatal(err)
		}
		_, err = sbom.VerifyService(context.Background(), fixture.request)
		requireCode(t, err, CodePolicyDenied)
	})
}

func TestSBOMVerifierRejectsMalformedPredicateJSON(t *testing.T) {
	fixture := newSBOMFixture(t, "api")
	rawStatement := []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"image","digest":{"sha256":"` + fixture.image.referenceKey.Digest[len("sha256:"):] + `"}}],"predicateType":"https://spdx.dev/Document","predicate":"not-an-object"}`)
	fixture.statementBytes = rawStatement
	fixture.document.Layers[0] = descriptorFor(MediaTypeInTotoStatement, rawStatement)
	manifest := mustJSON(t, fixture.document)
	fixture.referrer.Digest = sha256Digest(manifest)
	fixture.request.ReferrerReference = fixture.referrer.String()
	fixture.image.client.manifests[fixture.referrer.String()] = manifest
	fixture.image.client.blobs[fixture.document.Layers[0].Digest] = rawStatement
	_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
	requireCode(t, err, CodeInvalidSBOM)
}

func TestSBOMVerifierRequiresExactReferrerAndPresentStatementBlob(t *testing.T) {
	t.Run("tag", func(t *testing.T) {
		fixture := newSBOMFixture(t, "api")
		fixture.request.ReferrerReference = testRegistry + "/" + testRepository + ":sbom"
		_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
		requireCode(t, err, CodeInvalidReference)
	})
	t.Run("missing blob", func(t *testing.T) {
		fixture := newSBOMFixture(t, "api")
		delete(fixture.image.client.blobs, fixture.document.Layers[0].Digest)
		_, err := newTestSBOMVerifier(t, fixture.image.client, Limits{}).VerifyService(context.Background(), fixture.request)
		requireCode(t, err, CodeRegistryFetchFailed)
	})
}

func TestValidateInTotoSPDXRejectsNonObjectPredicate(t *testing.T) {
	statement := map[string]any{
		"_type":         InTotoStatementV1,
		"subject":       []any{map[string]any{"name": "image", "digest": map[string]any{"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		"predicateType": PredicateTypeSPDX,
		"predicate":     []any{"not", "an", "object"},
	}
	_, _, err := validateInTotoSPDXStatement(mustJSON(t, statement), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	requireCode(t, err, CodeInvalidSBOM)
}

func TestSPDXSentinelTraversalIsDeterministic(t *testing.T) {
	value := map[string]any{"z": "NONE", "a": map[string]any{"license": "NOASSERTION"}}
	field, sentinel, found := forbiddenSPDXSentinel(value, "predicate")
	if !found || field != "predicate.a.license" || sentinel != "NOASSERTION" {
		content, _ := json.Marshal(value)
		t.Fatalf("unexpected sentinel result for %s: %q %q %t", content, field, sentinel, found)
	}
}
