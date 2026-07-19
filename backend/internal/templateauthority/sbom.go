package templateauthority

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	PredicateTypeSPDX    = "https://spdx.dev/Document"
	maxAggregateServices = 128
)

var (
	serviceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	spdxIDPattern    = regexp.MustCompile(`^SPDXRef-[A-Za-z0-9.-]+$`)
)

// ServiceSBOMRequest identifies an image and its SBOM referrer by exact digest.
// The referrer must live in the same registry repository as the image.
type ServiceSBOMRequest struct {
	ServiceID         string `json:"serviceId"`
	ImageReference    string `json:"imageReference"`
	ReferrerReference string `json:"referrerReference"`
}

// VerifiedServiceSBOM is canonical evidence that both an image and its in-toto
// SPDX referrer were verified from registry bytes.
type VerifiedServiceSBOM struct {
	ServiceID             string               `json:"serviceId"`
	ImageReference        ExactReference       `json:"imageReference"`
	ImageManifest         VerifiedDescriptor   `json:"imageManifest"`
	ImageConfig           VerifiedDescriptor   `json:"imageConfig"`
	ImageLayers           []VerifiedDescriptor `json:"imageLayers"`
	ReferrerReference     ExactReference       `json:"referrerReference"`
	ReferrerManifest      VerifiedDescriptor   `json:"referrerManifest"`
	ReferrerConfig        VerifiedDescriptor   `json:"referrerConfig"`
	Statement             VerifiedDescriptor   `json:"statement"`
	StatementDigest       string               `json:"statementDigest"`
	PredicateDigest       string               `json:"predicateDigest"`
	SPDXVersion           string               `json:"spdxVersion"`
	DocumentNamespace     string               `json:"documentNamespace"`
	CanonicalEvidenceHash string               `json:"canonicalEvidenceHash"`
}

// VerifiedSBOMAggregate contains service evidence sorted by ServiceID and a
// deterministic sha256 hash of that ordered evidence document.
type VerifiedSBOMAggregate struct {
	SchemaVersion string                `json:"schemaVersion"`
	Services      []VerifiedServiceSBOM `json:"services"`
	Digest        string                `json:"digest"`
}

// SBOMVerifier reuses an OCIVerifier, so image and referrer reads share exactly
// the same origin, redirect, size, count, and timeout policies.
type SBOMVerifier struct {
	oci *OCIVerifier
}

func NewSBOMVerifier(oci *OCIVerifier) (*SBOMVerifier, error) {
	if oci == nil {
		return nil, verificationFailure(CodeInvalidConfiguration, "configure SBOM verifier", "oci", "OCI verifier is required", nil)
	}
	return &SBOMVerifier{oci: oci}, nil
}

// VerifyService re-verifies the target image instead of trusting a caller-built
// VerifiedImage value, then verifies the exact SBOM referrer and statement.
func (verifier *SBOMVerifier) VerifyService(ctx context.Context, request ServiceSBOMRequest) (VerifiedServiceSBOM, error) {
	if verifier == nil || verifier.oci == nil {
		return VerifiedServiceSBOM{}, verificationFailure(CodeInvalidConfiguration, "verify service SBOM", "verifier", "SBOM verifier is required", nil)
	}
	if ctx == nil {
		return VerifiedServiceSBOM{}, verificationFailure(CodeInvalidConfiguration, "verify service SBOM", "context", "context is required", nil)
	}
	if !serviceIDPattern.MatchString(request.ServiceID) || len(request.ServiceID) > 128 {
		return VerifiedServiceSBOM{}, verificationFailure(CodeInvalidSBOM, "verify service SBOM", "serviceId", "must be a canonical service identifier", nil)
	}
	imageReference, err := verifier.oci.ParseExactReference(request.ImageReference)
	if err != nil {
		return VerifiedServiceSBOM{}, err
	}
	referrerReference, err := verifier.oci.ParseExactReference(request.ReferrerReference)
	if err != nil {
		return VerifiedServiceSBOM{}, err
	}
	if imageReference.Host != referrerReference.Host || imageReference.Repository != referrerReference.Repository {
		return VerifiedServiceSBOM{}, verificationFailure(CodePolicyDenied, "verify service SBOM", "referrerReference", "referrer must use the image registry and repository", nil)
	}

	image, err := verifier.oci.VerifyImage(ctx, request.ImageReference)
	if err != nil {
		return VerifiedServiceSBOM{}, err
	}
	referrer, err := verifier.oci.verify(ctx, referrerReference, sbomManifestPolicy(), captureLayers)
	if err != nil {
		return VerifiedServiceSBOM{}, err
	}
	if referrer.document.Subject == nil {
		return VerifiedServiceSBOM{}, verificationFailure(CodeInvalidSBOM, "verify referrer", "subject", "subject is required", nil)
	}
	if err := validateDescriptor(*referrer.document.Subject, "subject", map[string]struct{}{MediaTypeOCIImageManifest: {}}); err != nil {
		return VerifiedServiceSBOM{}, err
	}
	if referrer.document.Subject.Digest != image.Reference.Digest {
		return VerifiedServiceSBOM{}, verificationFailure(CodeInvalidSBOM, "verify referrer", "subject.digest", "must equal the target image digest", nil)
	}
	if referrer.document.Subject.Size != image.Manifest.Size {
		return VerifiedServiceSBOM{}, verificationFailure(CodeInvalidSBOM, "verify referrer", "subject.size", "must equal the target image manifest size", nil)
	}
	if err := validateEmptyOCIConfig(referrer.configData); err != nil {
		return VerifiedServiceSBOM{}, err
	}
	if len(referrer.layerData) != 1 || len(referrer.layers) != 1 {
		return VerifiedServiceSBOM{}, verificationFailure(CodeInvalidSBOM, "verify referrer", "layers", "exactly one in-toto statement layer is required", nil)
	}

	statement, predicate, err := validateInTotoSPDXStatement(referrer.layerData[0], image.Reference.Digest)
	if err != nil {
		return VerifiedServiceSBOM{}, err
	}
	statementDigest := sha256Digest(referrer.layerData[0])
	predicateDigest := sha256Digest(statement.Predicate)
	result := VerifiedServiceSBOM{
		ServiceID:         request.ServiceID,
		ImageReference:    image.Reference,
		ImageManifest:     image.Manifest,
		ImageConfig:       image.Config,
		ImageLayers:       append([]VerifiedDescriptor(nil), image.Layers...),
		ReferrerReference: referrerReference,
		ReferrerManifest:  referrer.manifest,
		ReferrerConfig:    referrer.config,
		Statement:         referrer.layers[0],
		StatementDigest:   statementDigest,
		PredicateDigest:   predicateDigest,
		SPDXVersion:       predicate.SPDXVersion,
		DocumentNamespace: predicate.DocumentNamespace,
	}
	result.CanonicalEvidenceHash, err = serviceEvidenceHash(result)
	if err != nil {
		return VerifiedServiceSBOM{}, err
	}
	return result, nil
}

// VerifyAggregate verifies every service independently, rejects duplicate
// service IDs, sorts evidence by service ID, and hashes the resulting canonical
// evidence document.
func (verifier *SBOMVerifier) VerifyAggregate(ctx context.Context, requests []ServiceSBOMRequest) (VerifiedSBOMAggregate, error) {
	if verifier == nil || verifier.oci == nil {
		return VerifiedSBOMAggregate{}, verificationFailure(CodeInvalidConfiguration, "verify aggregate", "verifier", "SBOM verifier is required", nil)
	}
	if ctx == nil {
		return VerifiedSBOMAggregate{}, verificationFailure(CodeInvalidConfiguration, "verify aggregate", "context", "context is required", nil)
	}
	if len(requests) == 0 {
		return VerifiedSBOMAggregate{}, verificationFailure(CodeInvalidSBOM, "verify aggregate", "services", "at least one service SBOM is required", nil)
	}
	if len(requests) > maxAggregateServices {
		return VerifiedSBOMAggregate{}, verificationFailure(CodeLimitExceeded, "verify aggregate", "services", "service count limit exceeded", nil)
	}
	serviceIDs := make([]string, 0, len(requests))
	for _, request := range requests {
		serviceIDs = append(serviceIDs, request.ServiceID)
	}
	if _, unique := sortedUnique(serviceIDs); !unique {
		return VerifiedSBOMAggregate{}, verificationFailure(CodeInvalidSBOM, "verify aggregate", "services", "service IDs must be unique", nil)
	}
	services := make([]VerifiedServiceSBOM, 0, len(requests))
	for index, request := range requests {
		service, err := verifier.VerifyService(ctx, request)
		if err != nil {
			code, ok := ErrorCodeOf(err)
			if !ok {
				code = CodeInvalidSBOM
			}
			return VerifiedSBOMAggregate{}, verificationFailure(code, "verify aggregate", fmt.Sprintf("services[%d]", index), "service SBOM verification failed", err)
		}
		services = append(services, service)
	}
	sort.Slice(services, func(left, right int) bool {
		return services[left].ServiceID < services[right].ServiceID
	})
	digest, err := aggregateEvidenceHash(services)
	if err != nil {
		return VerifiedSBOMAggregate{}, err
	}
	return VerifiedSBOMAggregate{SchemaVersion: "worksflow.template-sbom-aggregate/v1", Services: services, Digest: digest}, nil
}

func sbomManifestPolicy() descriptorMediaPolicy {
	return descriptorMediaPolicy{
		manifestMediaType: MediaTypeOCIImageManifest,
		artifactType:      MediaTypeInTotoStatement,
		configMediaTypes:  map[string]struct{}{MediaTypeOCIEmptyConfig: {}},
		layerMediaTypes:   map[string]struct{}{MediaTypeInTotoStatement: {}},
		requireLayers:     1,
		requireSubject:    true,
	}
}

type spdxDocument struct {
	SPDXVersion       string           `json:"spdxVersion"`
	DataLicense       string           `json:"dataLicense"`
	SPDXID            string           `json:"SPDXID"`
	Name              string           `json:"name"`
	DocumentNamespace string           `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo `json:"creationInfo"`
	Packages          []spdxPackage    `json:"packages"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	SPDXID string `json:"SPDXID"`
	Name   string `json:"name"`
}

func validateEmptyOCIConfig(data []byte) error {
	var value map[string]json.RawMessage
	if err := decodeSingleJSON(data, &value); err != nil || value == nil || len(value) != 0 {
		return verificationFailure(CodeInvalidSBOM, "validate referrer config", "config", "must be one empty JSON object", err)
	}
	return nil
}

func validateInTotoSPDXStatement(data []byte, targetDigest string) (inTotoStatement, spdxDocument, error) {
	var statement inTotoStatement
	if err := decodeSingleJSON(data, &statement); err != nil {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "decode statement", "statement", "must be one valid in-toto JSON statement", err)
	}
	if statement.Type != InTotoStatementV1 {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "validate statement", "_type", "must be in-toto Statement v1", nil)
	}
	if statement.PredicateType != PredicateTypeSPDX {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "validate statement", "predicateType", "only SPDX JSON predicates are supported", nil)
	}
	if len(statement.Subject) != 1 {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "validate statement", "subject", "must contain exactly one target image", nil)
	}
	if strings.TrimSpace(statement.Subject[0].Name) == "" {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "validate statement", "subject[0].name", "must not be empty", nil)
	}
	if len(statement.Subject[0].Digest) != 1 || statement.Subject[0].Digest["sha256"] != strings.TrimPrefix(targetDigest, "sha256:") {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "validate statement", "subject[0].digest.sha256", "must equal the target image digest", nil)
	}
	if len(statement.Predicate) == 0 || string(statement.Predicate) == "null" {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "validate statement", "predicate", "SPDX predicate is required", nil)
	}
	var predicate spdxDocument
	if err := decodeSingleJSON(statement.Predicate, &predicate); err != nil {
		return inTotoStatement{}, spdxDocument{}, verificationFailure(CodeInvalidSBOM, "decode SPDX", "predicate", "must be one valid SPDX JSON document", err)
	}
	if err := validateSPDXDocument(statement.Predicate, predicate); err != nil {
		return inTotoStatement{}, spdxDocument{}, err
	}
	return statement, predicate, nil
}

func validateSPDXDocument(raw json.RawMessage, document spdxDocument) error {
	if document.SPDXVersion != "SPDX-2.3" {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.spdxVersion", "only SPDX-2.3 JSON is supported", nil)
	}
	if document.DataLicense != "CC0-1.0" {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.dataLicense", "must equal CC0-1.0", nil)
	}
	if document.SPDXID != "SPDXRef-DOCUMENT" {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.SPDXID", "must equal SPDXRef-DOCUMENT", nil)
	}
	if strings.TrimSpace(document.Name) == "" {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.name", "must not be empty", nil)
	}
	parsedNamespace, err := url.Parse(document.DocumentNamespace)
	if err != nil || !parsedNamespace.IsAbs() || parsedNamespace.Host == "" || (parsedNamespace.Scheme != "https" && parsedNamespace.Scheme != "http") {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.documentNamespace", "must be an absolute HTTP(S) URI", err)
	}
	if _, err := time.Parse(time.RFC3339, document.CreationInfo.Created); err != nil {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.creationInfo.created", "must be an RFC3339 timestamp", err)
	}
	if len(document.CreationInfo.Creators) == 0 {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.creationInfo.creators", "must contain at least one creator", nil)
	}
	for index, creator := range document.CreationInfo.Creators {
		if !strings.HasPrefix(creator, "Person: ") && !strings.HasPrefix(creator, "Organization: ") && !strings.HasPrefix(creator, "Tool: ") {
			return verificationFailure(CodeInvalidSBOM, "validate SPDX", fmt.Sprintf("predicate.creationInfo.creators[%d]", index), "must use an SPDX creator prefix", nil)
		}
	}
	if len(document.Packages) == 0 {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate.packages", "must contain at least one package", nil)
	}
	seenPackageIDs := map[string]struct{}{}
	for index, pkg := range document.Packages {
		if !spdxIDPattern.MatchString(pkg.SPDXID) || pkg.SPDXID == "SPDXRef-DOCUMENT" {
			return verificationFailure(CodeInvalidSBOM, "validate SPDX", fmt.Sprintf("predicate.packages[%d].SPDXID", index), "must be a package SPDX identifier", nil)
		}
		if _, exists := seenPackageIDs[pkg.SPDXID]; exists {
			return verificationFailure(CodeInvalidSBOM, "validate SPDX", fmt.Sprintf("predicate.packages[%d].SPDXID", index), "must be unique", nil)
		}
		seenPackageIDs[pkg.SPDXID] = struct{}{}
		if strings.TrimSpace(pkg.Name) == "" {
			return verificationFailure(CodeInvalidSBOM, "validate SPDX", fmt.Sprintf("predicate.packages[%d].name", index), "must not be empty", nil)
		}
	}
	var arbitrary any
	if err := decodeSingleJSON(raw, &arbitrary); err != nil {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", "predicate", "must be valid JSON", err)
	}
	if field, value, found := forbiddenSPDXSentinel(arbitrary, "predicate"); found {
		return verificationFailure(CodeInvalidSBOM, "validate SPDX", field, value+" is forbidden in admission SBOMs", nil)
	}
	return nil
}

func forbiddenSPDXSentinel(value any, field string) (string, string, bool) {
	switch typed := value.(type) {
	case string:
		normalized := strings.ToUpper(strings.TrimSpace(typed))
		if normalized == "NONE" || normalized == "NOASSERTION" {
			return field, normalized, true
		}
	case []any:
		for index, item := range typed {
			if nestedField, sentinel, found := forbiddenSPDXSentinel(item, fmt.Sprintf("%s[%d]", field, index)); found {
				return nestedField, sentinel, true
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if nestedField, sentinel, found := forbiddenSPDXSentinel(typed[key], field+"."+key); found {
				return nestedField, sentinel, true
			}
		}
	}
	return "", "", false
}

type canonicalServiceEvidence struct {
	ServiceID         string               `json:"serviceId"`
	ImageDigest       string               `json:"imageDigest"`
	ImageManifest     VerifiedDescriptor   `json:"imageManifest"`
	ImageConfig       VerifiedDescriptor   `json:"imageConfig"`
	ImageLayers       []VerifiedDescriptor `json:"imageLayers"`
	ReferrerDigest    string               `json:"referrerDigest"`
	ReferrerManifest  VerifiedDescriptor   `json:"referrerManifest"`
	ReferrerConfig    VerifiedDescriptor   `json:"referrerConfig"`
	Statement         VerifiedDescriptor   `json:"statement"`
	StatementDigest   string               `json:"statementDigest"`
	PredicateDigest   string               `json:"predicateDigest"`
	SPDXVersion       string               `json:"spdxVersion"`
	DocumentNamespace string               `json:"documentNamespace"`
}

func canonicalizeServiceEvidence(service VerifiedServiceSBOM) canonicalServiceEvidence {
	return canonicalServiceEvidence{
		ServiceID:         service.ServiceID,
		ImageDigest:       service.ImageReference.Digest,
		ImageManifest:     service.ImageManifest,
		ImageConfig:       service.ImageConfig,
		ImageLayers:       append([]VerifiedDescriptor(nil), service.ImageLayers...),
		ReferrerDigest:    service.ReferrerReference.Digest,
		ReferrerManifest:  service.ReferrerManifest,
		ReferrerConfig:    service.ReferrerConfig,
		Statement:         service.Statement,
		StatementDigest:   service.StatementDigest,
		PredicateDigest:   service.PredicateDigest,
		SPDXVersion:       service.SPDXVersion,
		DocumentNamespace: service.DocumentNamespace,
	}
}

func serviceEvidenceHash(service VerifiedServiceSBOM) (string, error) {
	content, err := json.Marshal(canonicalizeServiceEvidence(service))
	if err != nil {
		return "", verificationFailure(CodeInvalidSBOM, "hash service evidence", "service", "canonical evidence serialization failed", err)
	}
	return sha256Digest(content), nil
}

func aggregateEvidenceHash(services []VerifiedServiceSBOM) (string, error) {
	document := struct {
		SchemaVersion string                     `json:"schemaVersion"`
		Services      []canonicalServiceEvidence `json:"services"`
	}{SchemaVersion: "worksflow.template-sbom-aggregate/v1", Services: make([]canonicalServiceEvidence, 0, len(services))}
	for _, service := range services {
		document.Services = append(document.Services, canonicalizeServiceEvidence(service))
	}
	content, err := json.Marshal(document)
	if err != nil {
		return "", verificationFailure(CodeInvalidSBOM, "hash aggregate", "services", "canonical evidence serialization failed", err)
	}
	return sha256Digest(content), nil
}
