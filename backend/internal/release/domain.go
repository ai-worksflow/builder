package release

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/verification"
)

const BundleSchemaVersion = "release-bundle/v1"

var (
	ErrInvalidBundle          = errors.New("invalid release bundle")
	ErrBundleNotFound         = errors.New("release bundle was not found")
	ErrBundleConflict         = errors.New("release bundle conflicts with committed content")
	ErrBundleIntegrity        = errors.New("release bundle persistence integrity failure")
	ErrPreviewRunConflict     = errors.New("exact release bundle already has an active or unresolved preview run")
	ErrProductionHeadConflict = errors.New("production environment head changed or already has an active deployment")
)

type NewBundleInput struct {
	ID        string
	Receipt   verification.CanonicalReceipt
	CreatedBy string
	CreatedAt time.Time
}

// Bundle is the immutable handoff between Canonical quality and deployment.
// The Receipt is referenced, not embedded, while every release-critical exact
// lineage reference is repeated in the hash payload for direct verification.
type Bundle struct {
	SchemaVersion       string                                  `json:"schemaVersion"`
	ID                  string                                  `json:"id"`
	ProjectID           string                                  `json:"projectId"`
	Workspace           verification.CanonicalPlanSubject       `json:"workspace"`
	CanonicalReceipt    repository.ExactReference               `json:"canonicalReceipt"`
	BuildManifest       repository.ExactReference               `json:"buildManifest"`
	BuildContract       repository.ExactReference               `json:"buildContract"`
	FullStackTemplate   repository.ExactReference               `json:"fullStackTemplate"`
	VerificationProfile verification.ProfileReference           `json:"verificationProfile"`
	ReleaseArtifacts    []verification.CanonicalReleaseArtifact `json:"releaseArtifacts"`
	BundleHash          string                                  `json:"bundleHash"`
	CreatedBy           string                                  `json:"createdBy"`
	CreatedAt           time.Time                               `json:"createdAt"`
}

func NewBundle(input NewBundleInput) (Bundle, error) {
	if _, err := uuid.Parse(input.ID); err != nil {
		return Bundle{}, invalid("bundle identity")
	}
	if _, err := uuid.Parse(input.CreatedBy); err != nil || input.CreatedAt.IsZero() {
		return Bundle{}, invalid("bundle actor or time")
	}
	receipt, err := verification.ParseCanonicalReceipt(input.Receipt)
	if err != nil {
		return Bundle{}, invalid("Canonical Receipt")
	}
	receiptReference, err := receipt.PassedReference()
	if err != nil {
		return Bundle{}, invalid("passing Canonical Receipt")
	}
	artifacts := append([]verification.CanonicalReleaseArtifact(nil), receipt.ReleaseArtifacts...)
	sort.Slice(artifacts, func(left, right int) bool { return artifacts[left].ID < artifacts[right].ID })
	if !completeReleaseArtifactSet(artifacts) {
		return Bundle{}, invalid("release artifacts")
	}
	bundle := Bundle{
		SchemaVersion: BundleSchemaVersion, ID: input.ID, ProjectID: receipt.ProjectID,
		Workspace: receipt.Subject, CanonicalReceipt: receiptReference,
		BuildManifest: receipt.BuildManifest, BuildContract: receipt.BuildContract,
		FullStackTemplate: receipt.FullStackTemplate, VerificationProfile: receipt.Profile,
		ReleaseArtifacts: artifacts, CreatedBy: input.CreatedBy,
		CreatedAt: input.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(bundleHashPayload(bundle))
	if err != nil {
		return Bundle{}, invalid("bundle content")
	}
	bundle.BundleHash = "sha256:" + hash
	return bundle, nil
}

func ParseBundle(value Bundle) (Bundle, error) {
	if value.SchemaVersion != BundleSchemaVersion || !exactHash(value.BundleHash) {
		return Bundle{}, invalid("bundle envelope")
	}
	// NewBundle needs the complete signed Receipt. ParseBundle validates the
	// Bundle independently; the store additionally re-loads that exact Receipt.
	if _, err := uuid.Parse(value.ID); err != nil {
		return Bundle{}, invalid("bundle identity")
	}
	if _, err := uuid.Parse(value.ProjectID); err != nil {
		return Bundle{}, invalid("bundle project")
	}
	if _, err := uuid.Parse(value.CreatedBy); err != nil || value.CreatedAt.IsZero() {
		return Bundle{}, invalid("bundle actor or time")
	}
	if _, err := uuid.Parse(value.Workspace.WorkspaceArtifactID); err != nil {
		return Bundle{}, invalid("workspace artifact")
	}
	if _, err := uuid.Parse(value.Workspace.WorkspaceRevisionID); err != nil || !exactHash(value.Workspace.WorkspaceContentHash) {
		return Bundle{}, invalid("workspace revision")
	}
	for name, reference := range map[string]repository.ExactReference{
		"Canonical Receipt": value.CanonicalReceipt, "BuildManifest": value.BuildManifest,
		"BuildContract": value.BuildContract, "FullStackTemplate": value.FullStackTemplate,
	} {
		if _, err := uuid.Parse(reference.ID); err != nil || !exactHash(reference.ContentHash) {
			return Bundle{}, invalid(name)
		}
	}
	if strings.TrimSpace(value.VerificationProfile.ID) == "" || value.VerificationProfile.Version == 0 ||
		!exactHash(value.VerificationProfile.ContentHash) || len(value.ReleaseArtifacts) == 0 {
		return Bundle{}, invalid("profile or release artifacts")
	}
	artifacts := append([]verification.CanonicalReleaseArtifact(nil), value.ReleaseArtifacts...)
	sort.Slice(artifacts, func(left, right int) bool { return artifacts[left].ID < artifacts[right].ID })
	for index, artifact := range artifacts {
		if artifact.ID == "" || artifact.Kind == "" || artifact.Store == "" || artifact.Ref == "" ||
			artifact.MediaType == "" || !exactHash(artifact.ContentHash) || artifact.ByteSize < 0 ||
			(index > 0 && artifacts[index-1].ID == artifact.ID) {
			return Bundle{}, invalid("release artifact")
		}
	}
	if !sameArtifacts(artifacts, value.ReleaseArtifacts) {
		return Bundle{}, invalid("release artifact order")
	}
	if !completeReleaseArtifactSet(artifacts) {
		return Bundle{}, invalid("incomplete release artifact set")
	}
	hash, err := domain.CanonicalHash(bundleHashPayload(value))
	if err != nil || value.BundleHash != "sha256:"+hash {
		return Bundle{}, invalid("bundle hash")
	}
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	return value, nil
}

func completeReleaseArtifactSet(artifacts []verification.CanonicalReleaseArtifact) bool {
	required := map[string]bool{
		"migration":                 false,
		"runtime-config-schema":     false,
		"health-readiness-contract": false,
		"sbom":                      false,
		"vulnerability-report":      false,
		"provenance":                false,
		"signature":                 false,
	}
	deployable := false
	for _, artifact := range artifacts {
		if _, exists := required[artifact.Kind]; exists {
			required[artifact.Kind] = true
		}
		switch artifact.Kind {
		case "web-static", "oci-image", "service-image":
			deployable = true
		}
	}
	if !deployable {
		return false
	}
	for _, present := range required {
		if !present {
			return false
		}
	}
	return true
}

func bundleHashPayload(value Bundle) any {
	copy := value
	copy.BundleHash = ""
	return copy
}

func sameArtifacts(left, right []verification.CanonicalReleaseArtifact) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func exactHash(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && domain.IsCanonicalHash(value)
}

func invalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidBundle, detail)
}
