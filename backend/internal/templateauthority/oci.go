// Package templateauthority verifies the bytes behind exact OCI references
// before they may be used as Template Registry admission evidence.
package templateauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	MediaTypeOCIImageManifest = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIImageIndex    = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIImageConfig   = "application/vnd.oci.image.config.v1+json"
	MediaTypeOCIEmptyConfig   = "application/vnd.oci.empty.v1+json"
	MediaTypeOCILayer         = "application/vnd.oci.image.layer.v1.tar"
	MediaTypeOCILayerGzip     = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaTypeOCILayerZstd     = "application/vnd.oci.image.layer.v1.tar+zstd"
	MediaTypeInTotoStatement  = "application/vnd.in-toto+json"
)

const (
	defaultMaxManifestBytes int64 = 4 << 20
	defaultMaxBlobBytes     int64 = 256 << 20
	defaultMaxTotalBytes    int64 = 1 << 30
	defaultMaxBlobs               = 128
	defaultMaxRedirects           = 4
	defaultTimeout                = 2 * time.Minute
)

var (
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hostPattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
)

// ErrorCode is a stable, machine-readable verification failure class. Callers
// should branch on this value, not on an error string.
type ErrorCode string

const (
	CodeInvalidConfiguration ErrorCode = "invalid_configuration"
	CodeInvalidReference     ErrorCode = "invalid_reference"
	CodePolicyDenied         ErrorCode = "policy_denied"
	CodeUnsupportedMediaType ErrorCode = "unsupported_media_type"
	CodeInvalidManifest      ErrorCode = "invalid_manifest"
	CodeRegistryFetchFailed  ErrorCode = "registry_fetch_failed"
	CodeIntegrityMismatch    ErrorCode = "integrity_mismatch"
	CodeLimitExceeded        ErrorCode = "limit_exceeded"
	CodeTimeout              ErrorCode = "timeout"
	CodeInvalidSBOM          ErrorCode = "invalid_sbom"
)

// ErrorCodeOf extracts a stable verification code from an error chain.
func ErrorCodeOf(err error) (ErrorCode, bool) {
	var verificationError *VerificationError
	if !errors.As(err, &verificationError) {
		return "", false
	}
	return verificationError.Code, true
}

// ExactReference is a canonical registry/repository@sha256 reference returned
// by OCIVerifier after syntax and policy validation.
type ExactReference struct {
	Host       string `json:"host"`
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
}

func (reference ExactReference) String() string {
	return reference.Host + "/" + reference.Repository + "@" + reference.Digest
}

// RegistryRead is the stream returned by RegistryClient. ServingHost is
// mandatory and is the final host that served Body. RedirectHosts must contain
// every redirect target, in order. A client that follows redirects without
// reporting them violates this security contract.
type RegistryRead struct {
	Body          io.ReadCloser
	ServingHost   string
	RedirectHosts []string
}

// RegistryClient is deliberately transport-agnostic and injectable. Its
// implementation owns authentication and HTTP transport, must honor ctx, and
// must expose redirects through RegistryRead rather than hiding them.
type RegistryClient interface {
	FetchManifest(ctx context.Context, reference ExactReference) (RegistryRead, error)
	FetchBlob(ctx context.Context, repository ExactReference, descriptor Descriptor) (RegistryRead, error)
}

// RepositoryRule is an exact origin allowlist entry. Prefix and wildcard
// repository matching are intentionally unsupported.
type RepositoryRule struct {
	Host       string
	Repository string
}

// RegistryPolicy defines exact origins and separately approved redirect hosts.
// The origin host of a request is always accepted as a serving host.
type RegistryPolicy struct {
	Repositories  []RepositoryRule
	RedirectHosts []string
}

// Limits bounds all data read by one manifest verification. Total bytes include
// the raw manifest, config, and all layers. Zero values select safe defaults.
type Limits struct {
	MaxManifestBytes int64
	MaxBlobBytes     int64
	MaxTotalBytes    int64
	MaxBlobs         int
	MaxRedirects     int
	Timeout          time.Duration
}

// Descriptor is the subset of an OCI descriptor that contributes to byte-level
// identity. Digest is always a lowercase sha256 digest after verification.
type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// VerifiedDescriptor records the descriptor after its bytes, size, and digest
// have all been independently verified.
type VerifiedDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// VerifiedImage is a canonical byte-verified OCI image manifest. Layers retain
// manifest order; consumers must not sort them.
type VerifiedImage struct {
	Reference  ExactReference       `json:"reference"`
	Manifest   VerifiedDescriptor   `json:"manifest"`
	Config     VerifiedDescriptor   `json:"config"`
	Layers     []VerifiedDescriptor `json:"layers"`
	TotalBytes int64                `json:"totalBytes"`
}

type normalizedPolicy struct {
	repositories  map[string]struct{}
	redirectHosts map[string]struct{}
}

// OCIVerifier performs exact-reference policy checks and byte-level OCI
// verification. It does not claim that a RegistryClient is production-safe;
// that client's redirect/auth/TLS implementation must be qualified separately.
type OCIVerifier struct {
	client RegistryClient
	policy normalizedPolicy
	limits Limits
}

// NewOCIVerifier constructs a fail-closed verifier.
func NewOCIVerifier(client RegistryClient, policy RegistryPolicy, limits Limits) (*OCIVerifier, error) {
	if client == nil {
		return nil, verificationFailure(CodeInvalidConfiguration, "configure", "client", "registry client is required", nil)
	}
	normalized, err := normalizePolicy(policy)
	if err != nil {
		return nil, err
	}
	limits, err = normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	return &OCIVerifier{client: client, policy: normalized, limits: limits}, nil
}

// ParseExactReference validates both syntax and the exact origin allowlist.
func (verifier *OCIVerifier) ParseExactReference(raw string) (ExactReference, error) {
	if verifier == nil {
		return ExactReference{}, verificationFailure(CodeInvalidConfiguration, "parse reference", "verifier", "verifier is required", nil)
	}
	if raw != strings.TrimSpace(raw) || raw == "" || strings.ContainsAny(raw, "?#\\") || strings.Contains(raw, "://") {
		return ExactReference{}, verificationFailure(CodeInvalidReference, "parse reference", "reference", "must be a canonical registry/repository@sha256 reference", nil)
	}
	if strings.Count(raw, "@") != 1 {
		return ExactReference{}, verificationFailure(CodeInvalidReference, "parse reference", "reference", "tag and ambiguous references are forbidden", nil)
	}
	name, digest, _ := strings.Cut(raw, "@")
	if !digestPattern.MatchString(digest) {
		return ExactReference{}, verificationFailure(CodeInvalidReference, "parse reference", "digest", "must be an exact lowercase sha256 digest", nil)
	}
	host, repository, found := strings.Cut(name, "/")
	if !found || normalizeHost(host) != host || !repositoryPattern.MatchString(repository) {
		return ExactReference{}, verificationFailure(CodeInvalidReference, "parse reference", "reference", "host and repository must be canonical lowercase values", nil)
	}
	reference := ExactReference{Host: host, Repository: repository, Digest: digest}
	if _, ok := verifier.policy.repositories[repositoryKey(host, repository)]; !ok {
		return ExactReference{}, verificationFailure(CodePolicyDenied, "parse reference", "reference", "registry host/repository is not allowlisted", nil)
	}
	return reference, nil
}

// VerifyImage accepts only a digest-pinned OCI image manifest v1 and verifies
// the raw manifest, config, and every ordered layer byte stream.
func (verifier *OCIVerifier) VerifyImage(ctx context.Context, rawReference string) (VerifiedImage, error) {
	if verifier == nil {
		return VerifiedImage{}, verificationFailure(CodeInvalidConfiguration, "verify image", "verifier", "verifier is required", nil)
	}
	if ctx == nil {
		return VerifiedImage{}, verificationFailure(CodeInvalidConfiguration, "verify image", "context", "context is required", nil)
	}
	reference, err := verifier.ParseExactReference(rawReference)
	if err != nil {
		return VerifiedImage{}, err
	}
	result, err := verifier.verify(ctx, reference, imageManifestPolicy(), captureNone)
	if err != nil {
		return VerifiedImage{}, err
	}
	return VerifiedImage{
		Reference:  reference,
		Manifest:   result.manifest,
		Config:     result.config,
		Layers:     append([]VerifiedDescriptor(nil), result.layers...),
		TotalBytes: result.totalBytes,
	}, nil
}

type manifestDocument struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	ArtifactType  string       `json:"artifactType,omitempty"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
	Subject       *Descriptor  `json:"subject,omitempty"`
}

type descriptorMediaPolicy struct {
	manifestMediaType string
	artifactType      string
	configMediaTypes  map[string]struct{}
	layerMediaTypes   map[string]struct{}
	requireLayers     int
	requireSubject    bool
}

type captureMode int

const (
	captureNone captureMode = iota
	captureLayers
)

type verifiedManifest struct {
	document   manifestDocument
	manifest   VerifiedDescriptor
	config     VerifiedDescriptor
	layers     []VerifiedDescriptor
	configData []byte
	layerData  [][]byte
	totalBytes int64
}

func imageManifestPolicy() descriptorMediaPolicy {
	return descriptorMediaPolicy{
		manifestMediaType: MediaTypeOCIImageManifest,
		configMediaTypes:  map[string]struct{}{MediaTypeOCIImageConfig: {}},
		layerMediaTypes: map[string]struct{}{
			MediaTypeOCILayer: {}, MediaTypeOCILayerGzip: {}, MediaTypeOCILayerZstd: {},
		},
		requireLayers: -1,
	}
}

func (verifier *OCIVerifier) verify(ctx context.Context, reference ExactReference, mediaPolicy descriptorMediaPolicy, capture captureMode) (verifiedManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, verifier.limits.Timeout)
	defer cancel()

	manifestRead, err := verifier.client.FetchManifest(ctx, reference)
	if err != nil {
		return verifiedManifest{}, verifier.fetchFailure(ctx, "fetch manifest", "manifest", err)
	}
	if err := verifier.validateRead(reference, manifestRead, "manifest"); err != nil {
		closeQuietly(manifestRead.Body)
		return verifiedManifest{}, err
	}
	manifestBytes, err := readBounded(ctx, manifestRead.Body, verifier.limits.MaxManifestBytes)
	closeErr := manifestRead.Body.Close()
	if err != nil {
		return verifiedManifest{}, verifier.readFailure(ctx, "read manifest", "manifest", err)
	}
	if closeErr != nil {
		return verifiedManifest{}, verifier.fetchFailure(ctx, "close manifest", "manifest", closeErr)
	}
	if int64(len(manifestBytes)) > verifier.limits.MaxTotalBytes {
		return verifiedManifest{}, verificationFailure(CodeLimitExceeded, "verify manifest", "manifest", "total byte limit exceeded", nil)
	}
	manifestDigest := sha256Digest(manifestBytes)
	if manifestDigest != reference.Digest {
		return verifiedManifest{}, verificationFailure(CodeIntegrityMismatch, "verify manifest", "manifest.digest", "raw manifest digest does not match exact reference", nil)
	}

	var document manifestDocument
	if err := decodeSingleJSON(manifestBytes, &document); err != nil {
		return verifiedManifest{}, verificationFailure(CodeInvalidManifest, "decode manifest", "manifest", "must be one valid JSON document", err)
	}
	if document.SchemaVersion != 2 {
		return verifiedManifest{}, verificationFailure(CodeInvalidManifest, "validate manifest", "schemaVersion", "must equal 2", nil)
	}
	if document.MediaType != mediaPolicy.manifestMediaType {
		detail := "only OCI image manifest v1 is supported"
		if document.MediaType == MediaTypeOCIImageIndex {
			detail = "OCI image indexes are not supported"
		}
		return verifiedManifest{}, verificationFailure(CodeUnsupportedMediaType, "validate manifest", "mediaType", detail, nil)
	}
	if mediaPolicy.artifactType == "" {
		if document.ArtifactType != "" {
			return verifiedManifest{}, verificationFailure(CodeUnsupportedMediaType, "validate manifest", "artifactType", "artifact manifests are not image manifests", nil)
		}
	} else if document.ArtifactType != mediaPolicy.artifactType {
		return verifiedManifest{}, verificationFailure(CodeUnsupportedMediaType, "validate manifest", "artifactType", "artifact type is not supported", nil)
	}
	if mediaPolicy.requireSubject && document.Subject == nil {
		return verifiedManifest{}, verificationFailure(CodeInvalidManifest, "validate manifest", "subject", "subject descriptor is required", nil)
	}
	if !mediaPolicy.requireSubject && document.Subject != nil {
		return verifiedManifest{}, verificationFailure(CodeInvalidManifest, "validate manifest", "subject", "image manifests must not be referrer manifests", nil)
	}
	if mediaPolicy.requireLayers >= 0 && len(document.Layers) != mediaPolicy.requireLayers {
		return verifiedManifest{}, verificationFailure(CodeInvalidManifest, "validate manifest", "layers", fmt.Sprintf("must contain exactly %d layer(s)", mediaPolicy.requireLayers), nil)
	}
	if len(document.Layers)+1 > verifier.limits.MaxBlobs {
		return verifiedManifest{}, verificationFailure(CodeLimitExceeded, "validate manifest", "layers", "blob count limit exceeded", nil)
	}
	if err := validateDescriptor(document.Config, "config", mediaPolicy.configMediaTypes); err != nil {
		return verifiedManifest{}, err
	}
	for index, layer := range document.Layers {
		if err := validateDescriptor(layer, fmt.Sprintf("layers[%d]", index), mediaPolicy.layerMediaTypes); err != nil {
			return verifiedManifest{}, err
		}
	}

	totalBytes := int64(len(manifestBytes))
	descriptors := make([]Descriptor, 0, 1+len(document.Layers))
	descriptors = append(descriptors, document.Config)
	descriptors = append(descriptors, document.Layers...)
	for index, descriptor := range descriptors {
		if descriptor.Size > verifier.limits.MaxBlobBytes {
			return verifiedManifest{}, verificationFailure(CodeLimitExceeded, "validate descriptor", descriptorField(index), "declared blob size exceeds per-blob limit", nil)
		}
		if descriptor.Size > verifier.limits.MaxTotalBytes-totalBytes {
			return verifiedManifest{}, verificationFailure(CodeLimitExceeded, "validate descriptor", descriptorField(index), "declared blobs exceed total byte limit", nil)
		}
		totalBytes += descriptor.Size
	}

	result := verifiedManifest{
		document:   document,
		manifest:   VerifiedDescriptor{MediaType: document.MediaType, Digest: manifestDigest, Size: int64(len(manifestBytes))},
		layers:     make([]VerifiedDescriptor, 0, len(document.Layers)),
		totalBytes: int64(len(manifestBytes)),
	}
	if capture == captureLayers {
		result.layerData = make([][]byte, 0, len(document.Layers))
	}
	for index, descriptor := range descriptors {
		blobRead, fetchErr := verifier.client.FetchBlob(ctx, reference, descriptor)
		if fetchErr != nil {
			return verifiedManifest{}, verifier.fetchFailure(ctx, "fetch blob", descriptorField(index), fetchErr)
		}
		if validateErr := verifier.validateRead(reference, blobRead, descriptorField(index)); validateErr != nil {
			closeQuietly(blobRead.Body)
			return verifiedManifest{}, validateErr
		}
		captureBytes := capture == captureLayers
		verified, data, readErr := verifyBlobStream(ctx, blobRead.Body, descriptor, verifier.limits.MaxBlobBytes, captureBytes)
		closeErr := blobRead.Body.Close()
		if readErr != nil {
			return verifiedManifest{}, verifier.readFailure(ctx, "verify blob", descriptorField(index), readErr)
		}
		if closeErr != nil {
			return verifiedManifest{}, verifier.fetchFailure(ctx, "close blob", descriptorField(index), closeErr)
		}
		result.totalBytes += verified.Size
		if result.totalBytes > verifier.limits.MaxTotalBytes {
			return verifiedManifest{}, verificationFailure(CodeLimitExceeded, "verify blob", descriptorField(index), "total byte limit exceeded", nil)
		}
		if index == 0 {
			result.config = verified
			if capture == captureLayers {
				result.configData = data
			}
			continue
		}
		result.layers = append(result.layers, verified)
		if capture == captureLayers {
			result.layerData = append(result.layerData, data)
		}
	}
	return result, nil
}

func (verifier *OCIVerifier) validateRead(reference ExactReference, read RegistryRead, field string) error {
	if read.Body == nil {
		return verificationFailure(CodeRegistryFetchFailed, "validate registry response", field, "registry returned no body", nil)
	}
	servingHost := normalizeHost(read.ServingHost)
	if servingHost == "" || servingHost != read.ServingHost {
		return verificationFailure(CodePolicyDenied, "validate registry response", field+".servingHost", "serving host is missing or non-canonical", nil)
	}
	if len(read.RedirectHosts) > verifier.limits.MaxRedirects {
		return verificationFailure(CodeLimitExceeded, "validate registry response", field+".redirectHosts", "redirect count limit exceeded", nil)
	}
	for index, host := range read.RedirectHosts {
		if normalizeHost(host) != host || !verifier.hostMayServe(reference.Host, host) {
			return verificationFailure(CodePolicyDenied, "validate registry response", fmt.Sprintf("%s.redirectHosts[%d]", field, index), "redirect host is not allowlisted", nil)
		}
	}
	if len(read.RedirectHosts) == 0 {
		if servingHost != reference.Host {
			return verificationFailure(CodePolicyDenied, "validate registry response", field+".servingHost", "unreported redirect is forbidden", nil)
		}
	} else if read.RedirectHosts[len(read.RedirectHosts)-1] != servingHost {
		return verificationFailure(CodePolicyDenied, "validate registry response", field+".servingHost", "serving host must equal the final reported redirect host", nil)
	}
	if !verifier.hostMayServe(reference.Host, servingHost) {
		return verificationFailure(CodePolicyDenied, "validate registry response", field+".servingHost", "serving host is not allowlisted", nil)
	}
	return nil
}

func (verifier *OCIVerifier) hostMayServe(origin, host string) bool {
	if host == origin {
		return true
	}
	_, ok := verifier.policy.redirectHosts[host]
	return ok
}

func (verifier *OCIVerifier) fetchFailure(ctx context.Context, operation, field string, cause error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
		return verificationFailure(CodeTimeout, operation, field, "verification deadline exceeded", cause)
	}
	return verificationFailure(CodeRegistryFetchFailed, operation, field, "registry read failed", cause)
}

func (verifier *OCIVerifier) readFailure(ctx context.Context, operation, field string, cause error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
		return verificationFailure(CodeTimeout, operation, field, "verification deadline exceeded", cause)
	}
	var limitError *streamLimitError
	if errors.As(cause, &limitError) {
		return verificationFailure(CodeLimitExceeded, operation, field, limitError.Error(), cause)
	}
	var mismatch *streamMismatchError
	if errors.As(cause, &mismatch) {
		return verificationFailure(CodeIntegrityMismatch, operation, field, mismatch.Error(), cause)
	}
	return verificationFailure(CodeRegistryFetchFailed, operation, field, "registry stream failed", cause)
}

func normalizePolicy(policy RegistryPolicy) (normalizedPolicy, error) {
	normalized := normalizedPolicy{repositories: map[string]struct{}{}, redirectHosts: map[string]struct{}{}}
	if len(policy.Repositories) == 0 {
		return normalizedPolicy{}, verificationFailure(CodeInvalidConfiguration, "configure", "policy.repositories", "at least one exact repository is required", nil)
	}
	for index, rule := range policy.Repositories {
		host := normalizeHost(rule.Host)
		if host == "" || host != rule.Host {
			return normalizedPolicy{}, verificationFailure(CodeInvalidConfiguration, "configure", fmt.Sprintf("policy.repositories[%d].host", index), "must be a canonical lowercase DNS host", nil)
		}
		if !repositoryPattern.MatchString(rule.Repository) {
			return normalizedPolicy{}, verificationFailure(CodeInvalidConfiguration, "configure", fmt.Sprintf("policy.repositories[%d].repository", index), "must be an exact canonical lowercase repository", nil)
		}
		normalized.repositories[repositoryKey(host, rule.Repository)] = struct{}{}
	}
	for index, rawHost := range policy.RedirectHosts {
		host := normalizeHost(rawHost)
		if host == "" || host != rawHost {
			return normalizedPolicy{}, verificationFailure(CodeInvalidConfiguration, "configure", fmt.Sprintf("policy.redirectHosts[%d]", index), "must be a canonical lowercase DNS host", nil)
		}
		normalized.redirectHosts[host] = struct{}{}
	}
	return normalized, nil
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxManifestBytes == 0 {
		limits.MaxManifestBytes = defaultMaxManifestBytes
	}
	if limits.MaxBlobBytes == 0 {
		limits.MaxBlobBytes = defaultMaxBlobBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaultMaxTotalBytes
	}
	if limits.MaxBlobs == 0 {
		limits.MaxBlobs = defaultMaxBlobs
	}
	if limits.MaxRedirects == 0 {
		limits.MaxRedirects = defaultMaxRedirects
	}
	if limits.Timeout == 0 {
		limits.Timeout = defaultTimeout
	}
	if limits.MaxManifestBytes < 1 || limits.MaxBlobBytes < 1 || limits.MaxTotalBytes < 1 || limits.MaxBlobs < 1 || limits.MaxRedirects < 1 || limits.Timeout < time.Millisecond {
		return Limits{}, verificationFailure(CodeInvalidConfiguration, "configure", "limits", "all limits must be positive and timeout must be at least one millisecond", nil)
	}
	if limits.MaxManifestBytes > limits.MaxTotalBytes || limits.MaxBlobBytes > limits.MaxTotalBytes {
		return Limits{}, verificationFailure(CodeInvalidConfiguration, "configure", "limits", "manifest and per-blob limits must not exceed the total byte limit", nil)
	}
	return limits, nil
}

func normalizeHost(raw string) string {
	if raw == "" || len(raw) > 253 || strings.HasSuffix(raw, ".") || !hostPattern.MatchString(raw) || !strings.Contains(raw, ".") || strings.ToLower(raw) != raw || net.ParseIP(raw) != nil {
		return ""
	}
	for _, label := range strings.Split(raw, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
	}
	return raw
}

func repositoryKey(host, repository string) string { return host + "/" + repository }

func validateDescriptor(descriptor Descriptor, field string, allowedMediaTypes map[string]struct{}) error {
	if _, ok := allowedMediaTypes[descriptor.MediaType]; !ok {
		return verificationFailure(CodeUnsupportedMediaType, "validate descriptor", field+".mediaType", "descriptor media type is not supported", nil)
	}
	if !digestPattern.MatchString(descriptor.Digest) {
		return verificationFailure(CodeInvalidManifest, "validate descriptor", field+".digest", "must be a lowercase sha256 digest", nil)
	}
	if descriptor.Size < 0 {
		return verificationFailure(CodeInvalidManifest, "validate descriptor", field+".size", "must be non-negative", nil)
	}
	return nil
}

func descriptorField(index int) string {
	if index == 0 {
		return "config"
	}
	return fmt.Sprintf("layers[%d]", index-1)
}

func readBounded(ctx context.Context, reader io.Reader, maximum int64) ([]byte, error) {
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: reader}, N: maximum + 1}
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, &streamLimitError{detail: "stream exceeds byte limit"}
	}
	return content, nil
}

func verifyBlobStream(ctx context.Context, reader io.Reader, descriptor Descriptor, maximum int64, capture bool) (VerifiedDescriptor, []byte, error) {
	digester := sha256.New()
	var destination io.Writer = digester
	var content bytes.Buffer
	if capture {
		destination = io.MultiWriter(digester, &content)
	}
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: reader}, N: maximum + 1}
	written, err := io.CopyBuffer(destination, limited, make([]byte, 32*1024))
	if err != nil {
		return VerifiedDescriptor{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return VerifiedDescriptor{}, nil, err
	}
	if written > maximum {
		return VerifiedDescriptor{}, nil, &streamLimitError{detail: "blob exceeds per-blob byte limit"}
	}
	if written != descriptor.Size {
		return VerifiedDescriptor{}, nil, &streamMismatchError{detail: "actual blob size does not match descriptor"}
	}
	digest := "sha256:" + hex.EncodeToString(digester.Sum(nil))
	if digest != descriptor.Digest {
		return VerifiedDescriptor{}, nil, &streamMismatchError{detail: "actual blob digest does not match descriptor"}
	}
	var data []byte
	if capture {
		data = content.Bytes()
	}
	return VerifiedDescriptor{MediaType: descriptor.MediaType, Digest: digest, Size: written}, data, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

type streamLimitError struct{ detail string }

func (e *streamLimitError) Error() string { return e.detail }

type streamMismatchError struct{ detail string }

func (e *streamMismatchError) Error() string { return e.detail }

func decodeSingleJSON(data []byte, destination any) error {
	if err := rejectDuplicateJSONNames(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func verificationFailure(code ErrorCode, operation, field, detail string, cause error) error {
	return &VerificationError{Code: code, Operation: operation, Field: field, Detail: detail, Cause: cause}
}

func closeQuietly(closer io.Closer) {
	if closer != nil {
		_ = closer.Close()
	}
}

func sortedUnique(values []string) ([]string, bool) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, false
		}
	}
	return result, true
}
