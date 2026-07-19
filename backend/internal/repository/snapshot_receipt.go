package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	RepositorySnapshotReceiptSchemaVersion        = "repository-snapshot-receipt/v1"
	RepositorySnapshotReceiptSubjectSchemaVersion = "repository-snapshot-receipt-subject/v1"
	RepositorySnapshotTreeCommitmentSchemaVersion = "repository-snapshot-tree-commitment/v1"
)

var (
	ErrInvalidRepositorySnapshotSelection = errors.New("invalid repository snapshot selection")
	ErrRepositorySnapshotNotFound         = errors.New("repository snapshot not found")
	ErrRepositorySnapshotPending          = errors.New("repository snapshot receipt is pending")
	ErrRepositorySnapshotDrift            = errors.New("repository snapshot exact content changed")
	ErrRepositorySnapshotIntegrity        = errors.New("repository snapshot integrity check failed")
)

type RepositorySnapshotTreeCommitment struct {
	SchemaVersion     string `json:"schemaVersion"`
	TreeHash          string `json:"treeHash"`
	ContentObjectHash string `json:"contentObjectHash"`
	FileCount         int    `json:"fileCount"`
	ByteSize          int64  `json:"byteSize"`
}

type RepositorySnapshotTemplateReleaseRef struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	SubjectHash string `json:"subjectHash"`
}

type RepositorySnapshotTemplateSource struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	TreeHash   string `json:"treeHash"`
}

type RepositorySnapshotAuthorityReceiptRef struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	PolicyHash  string `json:"policyHash"`
}

type RepositorySnapshotTemplateEvidence struct {
	Role                  string                                `json:"role"`
	MountPath             string                                `json:"mountPath"`
	Release               RepositorySnapshotTemplateReleaseRef  `json:"release"`
	Source                RepositorySnapshotTemplateSource      `json:"source"`
	SBOMDigest            string                                `json:"sbomDigest"`
	SignatureBundleDigest string                                `json:"signatureBundleDigest"`
	AuthorityReceipt      RepositorySnapshotAuthorityReceiptRef `json:"authorityReceipt"`
}

// RepositorySnapshotReceiptSubject is deliberately compact. TreeHash commits
// every semantic path/mode/content hash, while ContentObjectHash commits the
// canonical stored TreeManifest. This avoids duplicating a potentially large
// Candidate tree in idempotency responses while still binding all source and
// Artifact Authority evidence needed to reproduce it.
type RepositorySnapshotReceiptSubject struct {
	SchemaVersion         string                               `json:"schemaVersion"`
	ID                    string                               `json:"id"`
	ProjectID             string                               `json:"projectId"`
	BuildManifest         ExactReference                       `json:"buildManifest"`
	BuildContract         ExactReference                       `json:"buildContract"`
	FullStackTemplate     ExactReference                       `json:"fullStackTemplate"`
	BaseWorkspaceRevision *ExactRevisionReference              `json:"baseWorkspaceRevision,omitempty"`
	Tree                  RepositorySnapshotTreeCommitment     `json:"tree"`
	TemplateReleases      []RepositorySnapshotTemplateEvidence `json:"templateReleases"`
	CreatedBy             string                               `json:"createdBy"`
	CreatedAt             time.Time                            `json:"createdAt"`
}

// RepositorySnapshotReceipt is a deterministic, content-addressed projection
// of immutable PostgreSQL lineage, the exact tree object, and immutable
// Template Artifact Authority evidence. The final qualification Receipt signs
// this content hash; this projection is not itself an approval signature.
type RepositorySnapshotReceipt struct {
	SchemaVersion string                           `json:"schemaVersion"`
	ContentHash   string                           `json:"contentHash"`
	Snapshot      RepositorySnapshotReceiptSubject `json:"snapshot"`
}

func NewRepositorySnapshotReceipt(
	snapshot RepositorySnapshot,
	pointer TreeBlobPointer,
	components []TemplateSourceComponent,
) (RepositorySnapshotReceipt, error) {
	if err := snapshot.Validate(); err != nil {
		return RepositorySnapshotReceipt{}, err
	}
	if err := pointer.validate(); err != nil || pointer.OwnerID != snapshot.ID ||
		pointer.TreeHash != snapshot.Tree.TreeHash || pointer.FileCount != len(snapshot.Tree.Files) ||
		pointer.ByteSize != treeByteSize(snapshot.Tree) {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	templateEvidence, err := repositorySnapshotTemplateEvidence(components)
	if err != nil {
		return RepositorySnapshotReceipt{}, err
	}
	subject := RepositorySnapshotReceiptSubject{
		SchemaVersion: RepositorySnapshotReceiptSubjectSchemaVersion,
		ID:            snapshot.ID, ProjectID: snapshot.ProjectID,
		BuildManifest: snapshot.BuildManifest, BuildContract: snapshot.BuildContract,
		FullStackTemplate:     snapshot.FullStackTemplate,
		BaseWorkspaceRevision: cloneRevisionReference(snapshot.BaseWorkspaceRevision),
		Tree: RepositorySnapshotTreeCommitment{
			SchemaVersion: RepositorySnapshotTreeCommitmentSchemaVersion,
			TreeHash:      pointer.TreeHash, ContentObjectHash: pointer.ContentObjectHash,
			FileCount: pointer.FileCount, ByteSize: pointer.ByteSize,
		},
		TemplateReleases: templateEvidence,
		CreatedBy:        snapshot.CreatedBy, CreatedAt: snapshot.CreatedAt.UTC(),
	}
	contentHash, err := repositorySnapshotReceiptContentHash(subject)
	if err != nil {
		return RepositorySnapshotReceipt{}, err
	}
	return RepositorySnapshotReceipt{
		SchemaVersion: RepositorySnapshotReceiptSchemaVersion,
		ContentHash:   contentHash,
		Snapshot:      subject,
	}, nil
}

func (receipt RepositorySnapshotReceipt) Validate() error {
	if receipt.SchemaVersion != RepositorySnapshotReceiptSchemaVersion || !isCanonicalSHA256(receipt.ContentHash) {
		return fmt.Errorf("%w: receipt schema or content hash", ErrRepositorySnapshotIntegrity)
	}
	expected, err := repositorySnapshotReceiptContentHash(receipt.Snapshot)
	if err != nil {
		return errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	if receipt.ContentHash != expected {
		return fmt.Errorf("%w: receipt content hash mismatch", ErrRepositorySnapshotIntegrity)
	}
	return nil
}

func repositorySnapshotReceiptContentHash(subject RepositorySnapshotReceiptSubject) (string, error) {
	if err := validateRepositorySnapshotReceiptSubject(subject); err != nil {
		return "", err
	}
	hash, err := domain.CanonicalHash(subject)
	if err != nil {
		return "", fmt.Errorf("hash repository snapshot receipt: %w", err)
	}
	return "sha256:" + hash, nil
}

func validateRepositorySnapshotReceiptSubject(subject RepositorySnapshotReceiptSubject) error {
	if subject.SchemaVersion != RepositorySnapshotReceiptSubjectSchemaVersion ||
		!validUUID(subject.ID) || !validUUID(subject.ProjectID) || !validUUID(subject.CreatedBy) ||
		subject.CreatedAt.IsZero() || subject.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("%w: receipt subject identity", ErrRepositorySnapshotIntegrity)
	}
	for _, reference := range []ExactReference{subject.BuildManifest, subject.BuildContract, subject.FullStackTemplate} {
		if err := validateExact(reference); err != nil {
			return errors.Join(ErrRepositorySnapshotIntegrity, err)
		}
	}
	if subject.BaseWorkspaceRevision != nil {
		base := subject.BaseWorkspaceRevision
		if !validUUID(base.ArtifactID) || !validUUID(base.RevisionID) || !isCanonicalSHA256(base.ContentHash) {
			return fmt.Errorf("%w: receipt base WorkspaceRevision", ErrRepositorySnapshotIntegrity)
		}
	}
	tree := subject.Tree
	if tree.SchemaVersion != RepositorySnapshotTreeCommitmentSchemaVersion ||
		!isCanonicalSHA256(tree.TreeHash) || !isCanonicalSHA256(tree.ContentObjectHash) ||
		tree.FileCount < 0 || tree.FileCount > MaxTreeFiles || tree.ByteSize < 0 || tree.ByteSize > MaxTreeBytes {
		return fmt.Errorf("%w: receipt tree commitment", ErrRepositorySnapshotIntegrity)
	}
	components := make([]TemplateSourceComponent, 0, len(subject.TemplateReleases))
	for _, evidence := range subject.TemplateReleases {
		components = append(components, templateSourceComponentFromEvidence(evidence))
	}
	canonical, err := repositorySnapshotTemplateEvidence(components)
	if err != nil || len(canonical) != len(subject.TemplateReleases) {
		return errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	for index := range canonical {
		if canonical[index] != subject.TemplateReleases[index] {
			return fmt.Errorf("%w: receipt TemplateRelease evidence is noncanonical", ErrRepositorySnapshotIntegrity)
		}
	}
	return nil
}

func repositorySnapshotTemplateEvidence(
	components []TemplateSourceComponent,
) ([]RepositorySnapshotTemplateEvidence, error) {
	if len(components) < 2 || len(components) > 8 {
		return nil, fmt.Errorf("%w: receipt requires exact web/api TemplateRelease evidence", ErrRepositorySnapshotIntegrity)
	}
	copyComponents := append([]TemplateSourceComponent(nil), components...)
	sort.Slice(copyComponents, func(i, j int) bool { return copyComponents[i].Role < copyComponents[j].Role })
	result := make([]RepositorySnapshotTemplateEvidence, 0, len(copyComponents))
	roles := make(map[string]bool, len(copyComponents))
	for _, component := range copyComponents {
		parsed, parseErr := url.Parse(component.Repository)
		mountPath, mountErr := NormalizePath(component.MountPath)
		if roles[component.Role] || (component.Role != "api" && component.Role != "web" && component.Role != "worker") ||
			mountErr != nil || mountPath != component.MountPath || !validUUID(component.ReleaseID) ||
			!isCanonicalSHA256(component.ReleaseContentHash) || !isCanonicalSHA256(component.ReleaseSubjectHash) ||
			parseErr != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" ||
			parsed.Fragment != "" || parsed.Port() != "" || !strings.HasSuffix(parsed.Path, ".git") ||
			component.Branch == "" || component.Branch != strings.TrimSpace(component.Branch) ||
			strings.ContainsAny(component.Branch, "\r\n\x00") || !gitObjectPattern.MatchString(component.Commit) ||
			!isCanonicalSHA256(component.TreeHash) || !isCanonicalSHA256(component.SBOMDigest) ||
			!isCanonicalSHA256(component.SignatureBundleDigest) || !validUUID(component.AuthorityReceiptID) ||
			!isCanonicalSHA256(component.AuthorityReceiptContentHash) || !isCanonicalSHA256(component.AuthorityPolicyHash) {
			return nil, fmt.Errorf("%w: invalid exact TemplateRelease evidence", ErrRepositorySnapshotIntegrity)
		}
		roles[component.Role] = true
		result = append(result, RepositorySnapshotTemplateEvidence{
			Role: component.Role, MountPath: component.MountPath,
			Release: RepositorySnapshotTemplateReleaseRef{
				ID: component.ReleaseID, ContentHash: component.ReleaseContentHash,
				SubjectHash: component.ReleaseSubjectHash,
			},
			Source: RepositorySnapshotTemplateSource{
				Repository: component.Repository, Branch: component.Branch,
				Commit: component.Commit, TreeHash: component.TreeHash,
			},
			SBOMDigest: component.SBOMDigest, SignatureBundleDigest: component.SignatureBundleDigest,
			AuthorityReceipt: RepositorySnapshotAuthorityReceiptRef{
				ID: component.AuthorityReceiptID, ContentHash: component.AuthorityReceiptContentHash,
				PolicyHash: component.AuthorityPolicyHash,
			},
		})
	}
	if !roles["web"] || !roles["api"] {
		return nil, fmt.Errorf("%w: receipt requires web and api TemplateReleases", ErrRepositorySnapshotIntegrity)
	}
	return result, nil
}

func templateSourceComponentFromEvidence(evidence RepositorySnapshotTemplateEvidence) TemplateSourceComponent {
	return TemplateSourceComponent{
		Role: evidence.Role, MountPath: evidence.MountPath,
		ReleaseID: evidence.Release.ID, ReleaseContentHash: evidence.Release.ContentHash,
		ReleaseSubjectHash: evidence.Release.SubjectHash,
		Repository:         evidence.Source.Repository, Branch: evidence.Source.Branch,
		Commit: evidence.Source.Commit, TreeHash: evidence.Source.TreeHash,
		SBOMDigest: evidence.SBOMDigest, SignatureBundleDigest: evidence.SignatureBundleDigest,
		AuthorityReceiptID:          evidence.AuthorityReceipt.ID,
		AuthorityReceiptContentHash: evidence.AuthorityReceipt.ContentHash,
		AuthorityPolicyHash:         evidence.AuthorityReceipt.PolicyHash,
	}
}

// GetSnapshot resolves one exact immutable RepositorySnapshot. Authorization
// happens before database access, and callers must supply the content hash
// learned from Candidate bootstrap rather than treating an opaque UUID as the
// complete identity.
func (service *CandidateBootstrapService) GetSnapshot(
	ctx context.Context,
	projectID, snapshotID, expectedContentHash, actorID string,
) (RepositorySnapshotReceipt, error) {
	projectID = strings.TrimSpace(projectID)
	snapshotID = strings.TrimSpace(snapshotID)
	expectedContentHash = strings.TrimSpace(expectedContentHash)
	actorID = strings.TrimSpace(actorID)
	if ctx == nil || !validUUID(projectID) || !validUUID(snapshotID) ||
		!isCanonicalSHA256(expectedContentHash) || !validUUID(actorID) {
		return RepositorySnapshotReceipt{}, ErrInvalidRepositorySnapshotSelection
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return RepositorySnapshotReceipt{}, fmt.Errorf("authorize repository snapshot read: %w", err)
	}
	recorded, err := service.loadRecordedRepositorySnapshotReceipt(
		ctx, projectID, snapshotID, expectedContentHash,
	)
	if err != nil {
		return RepositorySnapshotReceipt{}, err
	}
	recomputed, err := service.loadRepositorySnapshotReceipt(ctx, projectID, snapshotID, nil, true)
	if err != nil {
		return RepositorySnapshotReceipt{}, err
	}
	equal, err := sameRepositorySnapshotReceipt(recorded, recomputed)
	if err != nil || !equal {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	return recorded, nil
}

type recordedRepositorySnapshotReceiptRow struct {
	ContentHash string          `gorm:"column:content_hash"`
	Document    json.RawMessage `gorm:"column:document"`
}

func (service *CandidateBootstrapService) recordRepositorySnapshotReceipt(
	ctx context.Context,
	receipt RepositorySnapshotReceipt,
) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	document, err := domain.CanonicalJSON(receipt)
	if err != nil {
		return fmt.Errorf("encode RepositorySnapshot receipt: %w", err)
	}
	recordedAt := service.now().UTC()
	if recordedAt.IsZero() {
		return fmt.Errorf("%w: RepositorySnapshot receipt timestamp", ErrRepositorySnapshotIntegrity)
	}
	result := service.database.WithContext(ctx).Exec(`
INSERT INTO repository_snapshot_receipts (
  snapshot_id, project_id, schema_version, content_hash, document, recorded_at
) VALUES (?, ?, ?, ?, ?::jsonb, ?)
ON CONFLICT (snapshot_id) DO NOTHING
`, receipt.Snapshot.ID, receipt.Snapshot.ProjectID, receipt.SchemaVersion,
		receipt.ContentHash, document, recordedAt)
	if result.Error != nil {
		return fmt.Errorf("record RepositorySnapshot receipt: %w", result.Error)
	}
	recorded, err := service.loadRecordedRepositorySnapshotReceipt(
		ctx, receipt.Snapshot.ProjectID, receipt.Snapshot.ID, receipt.ContentHash,
	)
	if err != nil {
		return err
	}
	equal, err := sameRepositorySnapshotReceipt(receipt, recorded)
	if err != nil || !equal {
		return errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	return nil
}

func (service *CandidateBootstrapService) loadRecordedRepositorySnapshotReceipt(
	ctx context.Context,
	projectID, snapshotID, expectedContentHash string,
) (RepositorySnapshotReceipt, error) {
	var row recordedRepositorySnapshotReceiptRow
	result := service.database.WithContext(ctx).Raw(`
SELECT content_hash, document
FROM repository_snapshot_receipts
WHERE project_id = ? AND snapshot_id = ?
`, projectID, snapshotID).Scan(&row)
	if result.Error != nil {
		return RepositorySnapshotReceipt{}, fmt.Errorf("load recorded RepositorySnapshot receipt: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		var snapshotCount int64
		if err := service.database.WithContext(ctx).Raw(`
SELECT count(*)
FROM repository_snapshots
WHERE project_id = ? AND id = ?
`, projectID, snapshotID).Scan(&snapshotCount).Error; err != nil {
			return RepositorySnapshotReceipt{}, fmt.Errorf("inspect pending RepositorySnapshot receipt: %w", err)
		}
		if snapshotCount == 1 {
			return RepositorySnapshotReceipt{}, ErrRepositorySnapshotPending
		}
		return RepositorySnapshotReceipt{}, ErrRepositorySnapshotNotFound
	}
	if row.ContentHash != expectedContentHash {
		return RepositorySnapshotReceipt{}, ErrRepositorySnapshotDrift
	}
	var receipt RepositorySnapshotReceipt
	if err := json.Unmarshal(row.Document, &receipt); err != nil {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	if err := receipt.Validate(); err != nil || receipt.ContentHash != row.ContentHash ||
		receipt.Snapshot.ID != snapshotID || receipt.Snapshot.ProjectID != projectID {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	rawCanonical, rawErr := domain.CanonicalJSON(row.Document)
	receiptCanonical, receiptErr := domain.CanonicalJSON(receipt)
	if rawErr != nil || receiptErr != nil || !bytes.Equal(rawCanonical, receiptCanonical) {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, rawErr, receiptErr)
	}
	return receipt, nil
}

func sameRepositorySnapshotReceipt(left, right RepositorySnapshotReceipt) (bool, error) {
	leftCanonical, err := domain.CanonicalJSON(left)
	if err != nil {
		return false, err
	}
	rightCanonical, err := domain.CanonicalJSON(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftCanonical, rightCanonical), nil
}

type repositorySnapshotRow struct {
	ID                    string         `gorm:"column:id"`
	SchemaVersion         string         `gorm:"column:schema_version"`
	ProjectID             string         `gorm:"column:project_id"`
	BuildManifestID       string         `gorm:"column:build_manifest_id"`
	BuildManifestHash     string         `gorm:"column:build_manifest_hash"`
	BuildContractID       string         `gorm:"column:build_contract_id"`
	BuildContractHash     string         `gorm:"column:build_contract_hash"`
	FullStackTemplateID   string         `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash string         `gorm:"column:full_stack_template_hash"`
	BaseArtifactID        sql.NullString `gorm:"column:base_workspace_artifact_id"`
	BaseRevisionID        sql.NullString `gorm:"column:base_workspace_revision_id"`
	BaseContentHash       sql.NullString `gorm:"column:base_workspace_content_hash"`
	TreeStore             string         `gorm:"column:tree_store"`
	TreeOwnerID           string         `gorm:"column:tree_owner_id"`
	TreeRef               string         `gorm:"column:tree_ref"`
	TreeContentHash       string         `gorm:"column:tree_content_hash"`
	TreeHash              string         `gorm:"column:tree_hash"`
	TreeFileCount         int            `gorm:"column:tree_file_count"`
	TreeByteSize          int64          `gorm:"column:tree_byte_size"`
	CreatedBy             string         `gorm:"column:created_by"`
	CreatedAt             time.Time      `gorm:"column:created_at"`
}

func (service *CandidateBootstrapService) loadRepositorySnapshotReceipt(
	ctx context.Context,
	projectID, snapshotID string,
	expectedPointer *TreeBlobPointer,
	verifyFiles bool,
) (RepositorySnapshotReceipt, error) {
	var row repositorySnapshotRow
	result := service.database.WithContext(ctx).Raw(`
SELECT id::text AS id, schema_version, project_id::text AS project_id,
       build_manifest_id::text AS build_manifest_id, build_manifest_hash,
       build_contract_id::text AS build_contract_id, build_contract_hash,
       full_stack_template_id::text AS full_stack_template_id, full_stack_template_hash,
       base_workspace_artifact_id::text AS base_workspace_artifact_id,
       base_workspace_revision_id::text AS base_workspace_revision_id,
       base_workspace_content_hash,
       tree_store, tree_owner_id::text AS tree_owner_id, tree_ref,
       tree_content_hash, tree_hash, tree_file_count, tree_byte_size,
       created_by::text AS created_by, created_at
FROM repository_snapshots
WHERE project_id = ? AND id = ?
`, projectID, snapshotID).Scan(&row)
	if result.Error != nil {
		return RepositorySnapshotReceipt{}, fmt.Errorf("load repository snapshot: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return RepositorySnapshotReceipt{}, ErrRepositorySnapshotNotFound
	}
	if row.ID != snapshotID || row.ProjectID != projectID || row.SchemaVersion != RepositorySnapshotSchemaVersion ||
		row.BaseArtifactID.Valid != row.BaseRevisionID.Valid || row.BaseArtifactID.Valid != row.BaseContentHash.Valid {
		return RepositorySnapshotReceipt{}, fmt.Errorf("%w: persisted snapshot projection", ErrRepositorySnapshotIntegrity)
	}
	pointer := TreeBlobPointer{
		Store: row.TreeStore, Ref: row.TreeRef, OwnerID: row.TreeOwnerID,
		TreeHash: row.TreeHash, FileCount: row.TreeFileCount, ByteSize: row.TreeByteSize,
		ContentObjectHash: row.TreeContentHash,
	}
	if err := pointer.validate(); err != nil || pointer.OwnerID != snapshotID {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	if expectedPointer != nil && pointer != *expectedPointer {
		return RepositorySnapshotReceipt{}, fmt.Errorf("%w: snapshot tree pointer changed", ErrRepositorySnapshotIntegrity)
	}
	tree, err := service.trees.Get(ctx, projectID, snapshotID, pointer)
	if err != nil {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	tree, err = ParseTree(tree)
	if err != nil || tree.TreeHash != pointer.TreeHash || len(tree.Files) != pointer.FileCount || treeByteSize(tree) != pointer.ByteSize {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	if verifyFiles {
		for _, file := range tree.Files {
			resolved, value, resolveErr := service.files.Resolve(ctx, projectID, file.ContentHash, file.ByteSize)
			if resolveErr != nil {
				return RepositorySnapshotReceipt{}, errors.Join(
					ErrRepositorySnapshotIntegrity,
					fmt.Errorf("resolve repository snapshot file %s: %w", file.Path, resolveErr),
				)
			}
			if resolved.ContentHash != file.ContentHash || resolved.ByteSize != file.ByteSize || int64(len(value)) != file.ByteSize {
				return RepositorySnapshotReceipt{}, fmt.Errorf(
					"%w: resolved repository snapshot file %s differs from its tree entry",
					ErrRepositorySnapshotIntegrity, file.Path,
				)
			}
		}
	}
	snapshot := RepositorySnapshot{
		ID: row.ID, ProjectID: row.ProjectID,
		BuildManifest:     ExactReference{ID: row.BuildManifestID, ContentHash: row.BuildManifestHash},
		BuildContract:     ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash},
		FullStackTemplate: ExactReference{ID: row.FullStackTemplateID, ContentHash: row.FullStackTemplateHash},
		Tree:              tree, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.UTC(),
	}
	if row.BaseArtifactID.Valid {
		snapshot.BaseWorkspaceRevision = &ExactRevisionReference{
			ArtifactID: row.BaseArtifactID.String, RevisionID: row.BaseRevisionID.String,
			ContentHash: row.BaseContentHash.String,
		}
	}
	components, err := service.loadRepositorySnapshotTemplateComponents(ctx, snapshot)
	if err != nil {
		return RepositorySnapshotReceipt{}, err
	}
	receipt, err := NewRepositorySnapshotReceipt(snapshot, pointer, components)
	if err != nil {
		return RepositorySnapshotReceipt{}, errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	return receipt, nil
}

func (service *CandidateBootstrapService) settleRepositorySnapshotContent(
	ctx context.Context,
	projectID string,
	pointer TreeBlobPointer,
	tree TreeManifest,
) error {
	canonical, err := ParseTree(tree)
	if err != nil || canonical.TreeHash != pointer.TreeHash || len(canonical.Files) != pointer.FileCount ||
		treeByteSize(canonical) != pointer.ByteSize {
		return errors.Join(ErrRepositorySnapshotIntegrity, err)
	}
	for _, file := range canonical.Files {
		if err := service.files.Settle(ctx, projectID, file.ContentHash, file.ByteSize); err != nil {
			return fmt.Errorf("settle repository snapshot file %s: %w", file.Path, err)
		}
	}
	if err := service.trees.Finalize(ctx, projectID, pointer.OwnerID, pointer); err != nil {
		return fmt.Errorf("settle repository snapshot tree: %w", err)
	}
	return nil
}

func (service *CandidateBootstrapService) loadRepositorySnapshotTemplateComponents(
	ctx context.Context,
	snapshot RepositorySnapshot,
) ([]TemplateSourceComponent, error) {
	var components []TemplateSourceComponent
	result := service.database.WithContext(ctx).Raw(`
SELECT component.role, component.mount_path,
       release.id::text AS release_id,
       release.content_hash AS release_content_hash,
       release.subject_hash AS release_subject_hash,
       release.source_repository, release.source_branch, release.source_commit,
       release.tree_hash, receipt.sbom_digest, receipt.signature_bundle_digest,
       receipt.id::text AS authority_receipt_id,
       receipt.content_hash AS authority_receipt_content_hash,
       receipt.policy_hash AS authority_policy_hash
FROM application_build_contract_template_releases AS selected
JOIN full_stack_template_components AS component
  ON component.full_stack_template_id = ?
 AND component.full_stack_content_hash = ?
 AND component.role = selected.role
 AND component.template_release_id = selected.template_release_id
 AND component.template_release_content_hash = selected.template_release_content_hash
JOIN template_releases AS release
  ON release.id = component.template_release_id
 AND release.content_hash = component.template_release_content_hash
JOIN template_artifact_authority_receipts AS receipt
  ON receipt.id = release.authority_receipt_id
 AND receipt.content_hash = release.authority_receipt_content_hash
 AND receipt.policy_hash = release.authority_policy_hash
 AND receipt.subject_hash = release.subject_hash
 AND receipt.source_tree_hash = release.tree_hash
 AND receipt.sbom_digest = release.sbom_digest
 AND receipt.signature_bundle_digest = release.signature ->> 'bundleDigest'
WHERE selected.contract_id = ?
ORDER BY component.role
`, snapshot.FullStackTemplate.ID, snapshot.FullStackTemplate.ContentHash, snapshot.BuildContract.ID).Scan(&components)
	if result.Error != nil {
		return nil, fmt.Errorf("load RepositorySnapshot TemplateRelease evidence: %w", result.Error)
	}
	if _, err := repositorySnapshotTemplateEvidence(components); err != nil {
		return nil, err
	}
	return components, nil
}

func repositorySnapshotMatchesCandidate(snapshot RepositorySnapshotReceiptSubject, candidate CandidateWorkspace) bool {
	if snapshot.ID != candidate.RepositorySnapshotID || snapshot.ProjectID != candidate.ProjectID ||
		snapshot.BuildManifest != candidate.BuildManifest || snapshot.BuildContract != candidate.BuildContract ||
		snapshot.FullStackTemplate != candidate.FullStackTemplate || snapshot.Tree.TreeHash != candidate.BaseTreeHash ||
		snapshot.CreatedBy != candidate.CreatedBy || !snapshot.CreatedAt.Equal(candidate.CreatedAt) {
		return false
	}
	left, right := snapshot.BaseWorkspaceRevision, candidate.BaseWorkspaceRevision
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
