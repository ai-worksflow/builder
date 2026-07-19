package templateauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

const (
	testRegistry   = "registry.example.com"
	testRepository = "worksflow/templates"
)

type fakeRegistryClient struct {
	mu             sync.Mutex
	manifests      map[string][]byte
	blobs          map[string][]byte
	reads          map[string]RegistryRead
	failures       map[string]error
	fetchOrder     []string
	waitForContext map[string]bool
}

func newFakeRegistryClient() *fakeRegistryClient {
	return &fakeRegistryClient{
		manifests:      map[string][]byte{},
		blobs:          map[string][]byte{},
		reads:          map[string]RegistryRead{},
		failures:       map[string]error{},
		waitForContext: map[string]bool{},
	}
}

func (client *fakeRegistryClient) FetchManifest(ctx context.Context, reference ExactReference) (RegistryRead, error) {
	key := "manifest:" + reference.String()
	client.mu.Lock()
	client.fetchOrder = append(client.fetchOrder, key)
	wait := client.waitForContext[key]
	failure := client.failures[key]
	data, exists := client.manifests[reference.String()]
	configured, hasConfigured := client.reads[key]
	client.mu.Unlock()
	if wait {
		<-ctx.Done()
		return RegistryRead{}, ctx.Err()
	}
	if failure != nil {
		return RegistryRead{}, failure
	}
	if !exists {
		return RegistryRead{}, errors.New("manifest not found")
	}
	return fakeRead(reference.Host, data, configured, hasConfigured), nil
}

func (client *fakeRegistryClient) FetchBlob(ctx context.Context, repository ExactReference, descriptor Descriptor) (RegistryRead, error) {
	key := "blob:" + descriptor.Digest
	client.mu.Lock()
	client.fetchOrder = append(client.fetchOrder, key)
	wait := client.waitForContext[key]
	failure := client.failures[key]
	data, exists := client.blobs[descriptor.Digest]
	configured, hasConfigured := client.reads[key]
	client.mu.Unlock()
	if wait {
		<-ctx.Done()
		return RegistryRead{}, ctx.Err()
	}
	if failure != nil {
		return RegistryRead{}, failure
	}
	if !exists {
		return RegistryRead{}, errors.New("blob not found")
	}
	return fakeRead(repository.Host, data, configured, hasConfigured), nil
}

func fakeRead(origin string, data []byte, configured RegistryRead, hasConfigured bool) RegistryRead {
	if !hasConfigured {
		return RegistryRead{Body: io.NopCloser(bytes.NewReader(data)), ServingHost: origin}
	}
	configured.Body = io.NopCloser(bytes.NewReader(data))
	configured.RedirectHosts = append([]string(nil), configured.RedirectHosts...)
	return configured
}

type imageFixture struct {
	client       *fakeRegistryClient
	reference    string
	referenceKey ExactReference
	document     manifestDocument
	config       []byte
	layers       [][]byte
}

func newImageFixture(t *testing.T, layerContents ...string) imageFixture {
	t.Helper()
	if len(layerContents) == 0 {
		layerContents = []string{"first-layer", "second-layer"}
	}
	client := newFakeRegistryClient()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	document := manifestDocument{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIImageManifest,
		Config:        descriptorFor(MediaTypeOCIImageConfig, config),
	}
	layers := make([][]byte, 0, len(layerContents))
	for index, content := range layerContents {
		data := []byte(content)
		mediaType := MediaTypeOCILayer
		if index == 1 {
			mediaType = MediaTypeOCILayerGzip
		}
		document.Layers = append(document.Layers, descriptorFor(mediaType, data))
		layers = append(layers, data)
	}
	manifest := mustJSON(t, document)
	referenceKey := ExactReference{Host: testRegistry, Repository: testRepository, Digest: sha256Digest(manifest)}
	client.manifests[referenceKey.String()] = manifest
	client.blobs[document.Config.Digest] = config
	for index, descriptor := range document.Layers {
		client.blobs[descriptor.Digest] = layers[index]
	}
	return imageFixture{client: client, reference: referenceKey.String(), referenceKey: referenceKey, document: document, config: config, layers: layers}
}

func descriptorFor(mediaType string, data []byte) Descriptor {
	return Descriptor{MediaType: mediaType, Digest: sha256Digest(data), Size: int64(len(data))}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func newTestOCIVerifier(t *testing.T, client RegistryClient, limits Limits) *OCIVerifier {
	t.Helper()
	verifier, err := NewOCIVerifier(client, RegistryPolicy{
		Repositories:  []RepositoryRule{{Host: testRegistry, Repository: testRepository}},
		RedirectHosts: []string{"cdn.example.com"},
	}, limits)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func requireCode(t *testing.T, err error, expected ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", expected)
	}
	actual, ok := ErrorCodeOf(err)
	if !ok || actual != expected {
		t.Fatalf("got error %v with code %q, want %q", err, actual, expected)
	}
}

func TestOCIVerifierVerifiesExactManifestAndOrderedBlobs(t *testing.T) {
	fixture := newImageFixture(t)
	verifier := newTestOCIVerifier(t, fixture.client, Limits{})

	verified, err := verifier.VerifyImage(context.Background(), fixture.reference)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Reference != fixture.referenceKey || verified.Manifest.Digest != fixture.referenceKey.Digest {
		t.Fatalf("unexpected verified identity: %#v", verified)
	}
	if verified.Config.Digest != fixture.document.Config.Digest || len(verified.Layers) != len(fixture.document.Layers) {
		t.Fatalf("unexpected verified descriptors: %#v", verified)
	}
	for index, layer := range verified.Layers {
		if layer.Digest != fixture.document.Layers[index].Digest || layer.MediaType != fixture.document.Layers[index].MediaType {
			t.Fatalf("layer order changed at %d: %#v", index, verified.Layers)
		}
	}
	wantOrder := []string{
		"manifest:" + fixture.reference,
		"blob:" + fixture.document.Config.Digest,
		"blob:" + fixture.document.Layers[0].Digest,
		"blob:" + fixture.document.Layers[1].Digest,
	}
	if !reflect.DeepEqual(fixture.client.fetchOrder, wantOrder) {
		t.Fatalf("fetch order = %#v, want %#v", fixture.client.fetchOrder, wantOrder)
	}
	wantBytes := int64(len(fixture.client.manifests[fixture.reference]) + len(fixture.config) + len(fixture.layers[0]) + len(fixture.layers[1]))
	if verified.TotalBytes != wantBytes {
		t.Fatalf("total bytes = %d, want %d", verified.TotalBytes, wantBytes)
	}
}

func TestOCIVerifierRejectsTagsHostsRepositoriesAndNonCanonicalReferences(t *testing.T) {
	fixture := newImageFixture(t)
	verifier := newTestOCIVerifier(t, fixture.client, Limits{})
	tests := []struct {
		name      string
		reference string
		code      ErrorCode
	}{
		{name: "tag", reference: testRegistry + "/" + testRepository + ":latest", code: CodeInvalidReference},
		{name: "tag before digest", reference: testRegistry + "/" + testRepository + ":latest@" + fixture.referenceKey.Digest, code: CodeInvalidReference},
		{name: "host", reference: "evil.example.com/" + testRepository + "@" + fixture.referenceKey.Digest, code: CodePolicyDenied},
		{name: "repository", reference: testRegistry + "/other/templates@" + fixture.referenceKey.Digest, code: CodePolicyDenied},
		{name: "scheme", reference: "https://" + fixture.reference, code: CodeInvalidReference},
		{name: "uppercase digest", reference: testRegistry + "/" + testRepository + "@sha256:ABCDEF", code: CodeInvalidReference},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := verifier.VerifyImage(context.Background(), test.reference)
			requireCode(t, err, test.code)
		})
	}
}

func TestOCIVerifierRejectsIndexAndUnknownMediaTypes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifestDocument)
	}{
		{name: "index", mutate: func(document *manifestDocument) { document.MediaType = MediaTypeOCIImageIndex }},
		{name: "manifest", mutate: func(document *manifestDocument) { document.MediaType = "application/example" }},
		{name: "config", mutate: func(document *manifestDocument) { document.Config.MediaType = "application/example" }},
		{name: "layer", mutate: func(document *manifestDocument) { document.Layers[0].MediaType = "application/example" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImageFixture(t)
			test.mutate(&fixture.document)
			manifest := mustJSON(t, fixture.document)
			fixture.referenceKey.Digest = sha256Digest(manifest)
			fixture.reference = fixture.referenceKey.String()
			fixture.client.manifests = map[string][]byte{fixture.reference: manifest}
			_, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference)
			requireCode(t, err, CodeUnsupportedMediaType)
		})
	}
}

func TestOCIVerifierDetectsManifestConfigAndLayerTampering(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		fixture := newImageFixture(t)
		fixture.client.manifests[fixture.reference] = append(fixture.client.manifests[fixture.reference], ' ')
		_, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodeIntegrityMismatch)
	})
	for _, target := range []string{"config", "layer"} {
		t.Run(target, func(t *testing.T) {
			fixture := newImageFixture(t)
			digest := fixture.document.Config.Digest
			if target == "layer" {
				digest = fixture.document.Layers[0].Digest
			}
			fixture.client.blobs[digest] = []byte("tampered bytes")
			_, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference)
			requireCode(t, err, CodeIntegrityMismatch)
		})
	}
}

func TestOCIVerifierRejectsSizeLimitsCountsAndMissingBlob(t *testing.T) {
	t.Run("declared size mismatch", func(t *testing.T) {
		fixture := newImageFixture(t)
		fixture.document.Layers[0].Size++
		manifest := mustJSON(t, fixture.document)
		fixture.referenceKey.Digest = sha256Digest(manifest)
		fixture.reference = fixture.referenceKey.String()
		fixture.client.manifests = map[string][]byte{fixture.reference: manifest}
		_, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodeIntegrityMismatch)
	})
	t.Run("per blob", func(t *testing.T) {
		fixture := newImageFixture(t)
		limits := Limits{MaxManifestBytes: 4096, MaxBlobBytes: 8, MaxTotalBytes: 8192}
		_, err := newTestOCIVerifier(t, fixture.client, limits).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodeLimitExceeded)
	})
	t.Run("total", func(t *testing.T) {
		fixture := newImageFixture(t)
		manifestSize := int64(len(fixture.client.manifests[fixture.reference]))
		limits := Limits{MaxManifestBytes: manifestSize, MaxBlobBytes: 64, MaxTotalBytes: manifestSize + int64(len(fixture.config))}
		_, err := newTestOCIVerifier(t, fixture.client, limits).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodeLimitExceeded)
	})
	t.Run("count", func(t *testing.T) {
		fixture := newImageFixture(t)
		_, err := newTestOCIVerifier(t, fixture.client, Limits{MaxBlobs: 2}).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodeLimitExceeded)
	})
	t.Run("missing", func(t *testing.T) {
		fixture := newImageFixture(t)
		delete(fixture.client.blobs, fixture.document.Layers[0].Digest)
		_, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodeRegistryFetchFailed)
	})
}

func TestOCIVerifierValidatesEveryRedirectHostAndFinalServingHost(t *testing.T) {
	t.Run("allowlisted", func(t *testing.T) {
		fixture := newImageFixture(t)
		fixture.client.reads["blob:"+fixture.document.Config.Digest] = RegistryRead{
			ServingHost: "cdn.example.com", RedirectHosts: []string{"cdn.example.com"},
		}
		if _, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("escape", func(t *testing.T) {
		fixture := newImageFixture(t)
		fixture.client.reads["manifest:"+fixture.reference] = RegistryRead{
			ServingHost: "evil.example.com", RedirectHosts: []string{"evil.example.com"},
		}
		_, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodePolicyDenied)
	})
	t.Run("unreported", func(t *testing.T) {
		fixture := newImageFixture(t)
		fixture.client.reads["blob:"+fixture.document.Config.Digest] = RegistryRead{ServingHost: "cdn.example.com"}
		_, err := newTestOCIVerifier(t, fixture.client, Limits{}).VerifyImage(context.Background(), fixture.reference)
		requireCode(t, err, CodePolicyDenied)
	})
}

func TestOCIVerifierClassifiesTimeout(t *testing.T) {
	fixture := newImageFixture(t)
	fixture.client.waitForContext["manifest:"+fixture.reference] = true
	verifier := newTestOCIVerifier(t, fixture.client, Limits{Timeout: 5 * time.Millisecond})
	_, err := verifier.VerifyImage(context.Background(), fixture.reference)
	requireCode(t, err, CodeTimeout)
}
