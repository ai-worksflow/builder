package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const exactTreeLiteralIndexReadyStatus = "ready"

type exactTreeLiteralIndexManifestRow struct {
	ProjectID        uuid.UUID  `gorm:"column:project_id;primaryKey"`
	TreeHash         string     `gorm:"column:tree_hash;primaryKey"`
	SchemaVersion    string     `gorm:"column:schema_version"`
	Status           string     `gorm:"column:status"`
	FileCount        int        `gorm:"column:file_count"`
	TextFileCount    int        `gorm:"column:text_file_count"`
	SkippedFileCount int        `gorm:"column:skipped_file_count"`
	TotalBytes       int64      `gorm:"column:total_bytes"`
	TreeCommitment   string     `gorm:"column:tree_commitment"`
	IndexCommitment  string     `gorm:"column:index_commitment"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	ReadyAt          *time.Time `gorm:"column:ready_at"`
}

func (exactTreeLiteralIndexManifestRow) TableName() string {
	return "repository_exact_tree_literal_index_manifests"
}

type exactTreeLiteralIndexBlobRow struct {
	ProjectID   uuid.UUID      `gorm:"column:project_id;primaryKey"`
	ContentHash string         `gorm:"column:content_hash;primaryKey"`
	ByteSize    int64          `gorm:"column:byte_size"`
	IsText      bool           `gorm:"column:is_text"`
	Body        sql.NullString `gorm:"column:body"`
}

func (exactTreeLiteralIndexBlobRow) TableName() string {
	return "repository_exact_tree_literal_index_blobs"
}

type exactTreeLiteralIndexMemberRow struct {
	ProjectID   uuid.UUID `gorm:"column:project_id;primaryKey"`
	TreeHash    string    `gorm:"column:tree_hash;primaryKey"`
	Path        string    `gorm:"column:path;primaryKey"`
	Mode        string    `gorm:"column:mode"`
	ContentHash string    `gorm:"column:content_hash"`
	ByteSize    int64     `gorm:"column:byte_size"`
	Indexed     bool      `gorm:"column:indexed"`
}

func (exactTreeLiteralIndexMemberRow) TableName() string {
	return "repository_exact_tree_literal_index_members"
}

type exactTreeLiteralIndexVerificationRow struct {
	Path        string         `gorm:"column:path"`
	Mode        string         `gorm:"column:mode"`
	ContentHash string         `gorm:"column:content_hash"`
	ByteSize    int64          `gorm:"column:byte_size"`
	Indexed     bool           `gorm:"column:indexed"`
	IsText      bool           `gorm:"column:is_text"`
	Body        sql.NullString `gorm:"column:body"`
}

type exactTreeLiteralIndexClaimAcquireRow struct {
	Decision            string     `gorm:"column:decision"`
	OwnerToken          *uuid.UUID `gorm:"column:current_owner_token"`
	Attempt             *int64     `gorm:"column:current_attempt"`
	ReservedSourceBytes *int64     `gorm:"column:current_reserved_source_bytes"`
	LeaseExpiresAt      *time.Time `gorm:"column:current_lease_expires_at"`
}

type exactTreeLiteralIndexClaimRenewRow struct {
	Renewed        bool       `gorm:"column:renewed"`
	LeaseExpiresAt *time.Time `gorm:"column:current_lease_expires_at"`
}

type GORMExactTreeLiteralIndexStore struct {
	database *gorm.DB
}

func NewGORMExactTreeLiteralIndexStore(database *gorm.DB) (*GORMExactTreeLiteralIndexStore, error) {
	if database == nil {
		return nil, errors.New("repository exact-tree literal index database is required")
	}
	return &GORMExactTreeLiteralIndexStore{database: database}, nil
}

func (store *GORMExactTreeLiteralIndexStore) AcquireExactTreeLiteralIndexBuildClaim(
	ctx context.Context,
	request ExactTreeLiteralIndexBuildClaimRequest,
) (ExactTreeLiteralIndexBuildClaimResult, error) {
	if ctx == nil || request.ProjectID != strings.TrimSpace(request.ProjectID) ||
		!validUUID(request.ProjectID) || !isCanonicalSHA256(request.TreeHash) ||
		!validUUID(request.OwnerToken) || request.SourceBytes < 0 || request.SourceBytes > MaxTreeBytes ||
		!validExactTreeLiteralIndexClaimLease(request.Lease) ||
		!validExactTreeLiteralIndexProjectQuota(ExactTreeLiteralIndexProjectQuota{
			MaxTrees: request.MaxProjectTrees, MaxSourceBytes: request.MaxProjectSourceBytes,
			MaxActiveBuilds: request.MaxProjectActiveBuilds,
		}) {
		return ExactTreeLiteralIndexBuildClaimResult{}, ErrInvalidExactTreeLiteralIndex
	}
	projectID, _ := uuid.Parse(request.ProjectID)
	ownerToken, _ := uuid.Parse(request.OwnerToken)
	var outcome ExactTreeLiteralIndexBuildClaimResult
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var acquiredAdvisory bool
		lock := transaction.Raw(
			"SELECT pg_try_advisory_xact_lock(hashtextextended(CAST(? AS text), 0)) AS acquired",
			exactTreeLiteralIndexAdvisoryKey(request.ProjectID, request.TreeHash),
		).Scan(&acquiredAdvisory)
		if lock.Error != nil {
			return fmt.Errorf("try exact-tree literal index build coordination lock: %w", lock.Error)
		}
		if !acquiredAdvisory {
			outcome.Disposition = ExactTreeLiteralBuildClaimWaiting
			return nil
		}

		manifest, found, err := loadVerifiedReadyExactTreeLiteralIndex(
			transaction, projectID, request.TreeHash,
		)
		if err != nil {
			return err
		}
		if found {
			outcome = ExactTreeLiteralIndexBuildClaimResult{
				Disposition: ExactTreeLiteralBuildClaimReady,
				Manifest:    manifest,
			}
			return nil
		}

		var row exactTreeLiteralIndexClaimAcquireRow
		result := transaction.Raw(`
SELECT
  decision, current_owner_token, current_attempt,
  current_reserved_source_bytes, current_lease_expires_at
FROM acquire_repository_exact_tree_literal_index_build_claim(?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, request.TreeHash, ownerToken, request.SourceBytes,
			int(request.Lease.Milliseconds()), request.MaxProjectTrees,
			request.MaxProjectSourceBytes, request.MaxProjectActiveBuilds,
		).Scan(&row)
		if result.Error != nil {
			return fmt.Errorf("acquire exact-tree literal index build claim row: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return exactTreeLiteralIndexContract("claim acquire function returned no decision", nil)
		}
		switch row.Decision {
		case "quota_trees":
			return ErrExactTreeLiteralProjectTreeQuota
		case "quota_source_bytes":
			return ErrExactTreeLiteralProjectSourceBytesQuota
		case "quota_active_builds":
			return ErrExactTreeLiteralProjectActiveBuildQuota
		case "acquired", "waiting":
		default:
			return exactTreeLiteralIndexContract("claim acquire function returned an unknown decision", nil)
		}
		if row.OwnerToken == nil || *row.OwnerToken == uuid.Nil || row.Attempt == nil || *row.Attempt <= 0 ||
			row.ReservedSourceBytes == nil || *row.ReservedSourceBytes != request.SourceBytes ||
			row.LeaseExpiresAt == nil || row.LeaseExpiresAt.IsZero() {
			return exactTreeLiteralIndexContract("claim acquire function returned invalid claim facts", nil)
		}
		outcome.Claim = ExactTreeLiteralIndexBuildClaim{
			ProjectID: request.ProjectID, TreeHash: request.TreeHash,
			OwnerToken: (*row.OwnerToken).String(), Attempt: *row.Attempt,
			ReservedSourceBytes: *row.ReservedSourceBytes,
			LeaseExpiresAt:      row.LeaseExpiresAt.UTC(),
		}
		if row.Decision == "acquired" {
			if outcome.Claim.OwnerToken != request.OwnerToken {
				return exactTreeLiteralIndexContract("claim acquire function returned a foreign owner", nil)
			}
			outcome.Disposition = ExactTreeLiteralBuildClaimAcquired
		} else {
			outcome.Disposition = ExactTreeLiteralBuildClaimWaiting
		}
		return nil
	})
	if err != nil {
		return ExactTreeLiteralIndexBuildClaimResult{}, err
	}
	return outcome, nil
}

func (store *GORMExactTreeLiteralIndexStore) RenewExactTreeLiteralIndexBuildClaim(
	ctx context.Context,
	claim ExactTreeLiteralIndexBuildClaim,
	lease time.Duration,
) (ExactTreeLiteralIndexBuildClaim, error) {
	if ctx == nil || validateExactTreeLiteralIndexClaimIdentity(claim) != nil ||
		!validExactTreeLiteralIndexClaimLease(lease) {
		return ExactTreeLiteralIndexBuildClaim{}, ErrInvalidExactTreeLiteralIndex
	}
	projectID, _ := uuid.Parse(claim.ProjectID)
	ownerToken, _ := uuid.Parse(claim.OwnerToken)
	var row exactTreeLiteralIndexClaimRenewRow
	result := store.database.WithContext(ctx).Raw(`
SELECT renewed, current_lease_expires_at
FROM renew_repository_exact_tree_literal_index_build_claim(?, ?, ?, ?, ?)`,
		projectID, claim.TreeHash, ownerToken, claim.Attempt, int(lease.Milliseconds()),
	).Scan(&row)
	if result.Error != nil {
		return ExactTreeLiteralIndexBuildClaim{}, fmt.Errorf("renew exact-tree literal index build claim row: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ExactTreeLiteralIndexBuildClaim{}, exactTreeLiteralIndexContract(
			"claim renewal function returned no decision", nil,
		)
	}
	if !row.Renewed || row.LeaseExpiresAt == nil || row.LeaseExpiresAt.IsZero() {
		return ExactTreeLiteralIndexBuildClaim{}, ErrExactTreeLiteralBuildClaimLost
	}
	claim.LeaseExpiresAt = row.LeaseExpiresAt.UTC()
	return claim, nil
}

func (store *GORMExactTreeLiteralIndexStore) ReleaseExactTreeLiteralIndexBuildClaim(
	ctx context.Context,
	claim ExactTreeLiteralIndexBuildClaim,
) error {
	if ctx == nil || validateExactTreeLiteralIndexClaimIdentity(claim) != nil {
		return ErrInvalidExactTreeLiteralIndex
	}
	projectID, _ := uuid.Parse(claim.ProjectID)
	ownerToken, _ := uuid.Parse(claim.OwnerToken)
	return store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if result := transaction.Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(CAST(? AS text), 0))",
			exactTreeLiteralIndexAdvisoryKey(claim.ProjectID, claim.TreeHash),
		); result.Error != nil {
			return fmt.Errorf("lock exact-tree literal index claim release: %w", result.Error)
		}
		var released bool
		result := transaction.Raw(`
SELECT release_repository_exact_tree_literal_index_build_claim(?, ?, ?, ?) AS released`,
			projectID, claim.TreeHash, ownerToken, claim.Attempt,
		).Scan(&released)
		if result.Error != nil {
			return fmt.Errorf("release exact-tree literal index build claim row: %w", result.Error)
		}
		if result.RowsAffected != 1 || !released {
			return ErrExactTreeLiteralBuildClaimLost
		}
		return nil
	})
}

func (store *GORMExactTreeLiteralIndexStore) PublishExactTreeLiteralIndex(
	ctx context.Context,
	build ExactTreeLiteralIndexBuild,
) (ExactTreeLiteralIndexManifest, error) {
	if ctx == nil {
		return ExactTreeLiteralIndexManifest{}, ErrInvalidExactTreeLiteralIndex
	}
	if err := validateExactTreeLiteralIndexBuild(build); err != nil {
		return ExactTreeLiteralIndexManifest{}, err
	}
	projectID, _ := uuid.Parse(build.ProjectID)
	var published ExactTreeLiteralIndexManifest
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		// hashtextextended is stable across sessions and PostgreSQL processes. The
		// full key is still checked by the primary key, so a 64-bit collision only
		// serializes unrelated builds; it cannot merge their state.
		lockKey := exactTreeLiteralIndexAdvisoryKey(build.ProjectID, build.TreeHash)
		if result := transaction.Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(CAST(? AS text), 0))", lockKey,
		); result.Error != nil {
			return fmt.Errorf("lock exact-tree literal index publication: %w", result.Error)
		}
		if err := verifyActiveExactTreeLiteralIndexBuildClaim(transaction, build); err != nil {
			return err
		}

		existing, found, err := loadExactTreeLiteralIndexManifestForUpdate(
			transaction, projectID, build.TreeHash,
		)
		if err != nil {
			return err
		}
		if found {
			if existing.Status != exactTreeLiteralIndexReadyStatus {
				return exactTreeLiteralIndexConflict(
					"an earlier publication left a non-ready manifest", nil,
				)
			}
			if err := verifyPersistedExactTreeLiteralIndex(transaction, existing, build); err != nil {
				return err
			}
			published, err = exactTreeLiteralIndexManifestFromRow(existing, true)
			return err
		}

		if err := publishExactTreeLiteralIndexBlobs(transaction, projectID, build.Files); err != nil {
			return err
		}
		insertManifest := `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id, tree_hash, schema_version, status,
  file_count, text_file_count, skipped_file_count, total_bytes,
  tree_commitment, index_commitment
) VALUES (?, ?, ?, 'building', ?, ?, ?, ?, ?, ?)`
		if result := transaction.Exec(
			insertManifest,
			projectID, build.TreeHash, build.SchemaVersion,
			build.FileCount, build.TextFileCount, build.SkippedFileCount, build.TotalBytes,
			build.TreeCommitment, build.IndexCommitment,
		); result.Error != nil {
			return fmt.Errorf("insert exact-tree literal index manifest: %w", result.Error)
		}

		members := make([]exactTreeLiteralIndexMemberRow, len(build.Files))
		for index, file := range build.Files {
			members[index] = exactTreeLiteralIndexMemberRow{
				ProjectID: projectID, TreeHash: build.TreeHash, Path: file.Path,
				Mode: file.Mode, ContentHash: file.ContentHash,
				ByteSize: file.ByteSize, Indexed: file.Text,
			}
		}
		if len(members) > 0 {
			if result := transaction.CreateInBatches(&members, 250); result.Error != nil {
				return fmt.Errorf("insert exact-tree literal index members: %w", result.Error)
			}
		}
		if err := verifyActiveExactTreeLiteralIndexBuildClaim(transaction, build); err != nil {
			return err
		}
		ready := transaction.Exec(`
UPDATE repository_exact_tree_literal_index_manifests
SET status = 'ready', ready_at = statement_timestamp()
WHERE project_id = ? AND tree_hash = ? AND status = 'building'`, projectID, build.TreeHash)
		if ready.Error != nil {
			return fmt.Errorf("mark exact-tree literal index ready: %w", ready.Error)
		}
		if ready.RowsAffected != 1 {
			return exactTreeLiteralIndexConflict("manifest did not make one building-to-ready transition", nil)
		}
		row, found, err := loadExactTreeLiteralIndexManifestForUpdate(
			transaction, projectID, build.TreeHash,
		)
		if err != nil {
			return err
		}
		if !found {
			return exactTreeLiteralIndexContract("ready manifest disappeared inside publication transaction", nil)
		}
		if err := verifyPersistedExactTreeLiteralIndex(transaction, row, build); err != nil {
			return err
		}
		if err := renewActiveExactTreeLiteralIndexBuildClaim(transaction, build); err != nil {
			return err
		}
		published, err = exactTreeLiteralIndexManifestFromRow(row, false)
		return err
	})
	if err != nil {
		return ExactTreeLiteralIndexManifest{}, err
	}
	return published, nil
}

func (store *GORMExactTreeLiteralIndexStore) QueryExactTreeLiteralIndex(
	ctx context.Context,
	query ExactTreeLiteralIndexStoreQuery,
) (ExactTreeLiteralIndexStoreQueryResult, error) {
	if ctx == nil {
		return ExactTreeLiteralIndexStoreQueryResult{}, ErrInvalidExactTreeLiteralIndex
	}
	normalized, err := normalizeExactTreeLiteralIndexQuery(ExactTreeLiteralIndexQuery{
		ProjectID: query.ProjectID, TreeHash: query.TreeHash, Query: query.Query,
		CaseSensitive: query.CaseSensitive, MaxDocuments: query.MaxDocuments,
	})
	if err != nil {
		return ExactTreeLiteralIndexStoreQueryResult{}, err
	}
	projectID, _ := uuid.Parse(normalized.ProjectID)
	var outcome ExactTreeLiteralIndexStoreQueryResult
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		// Publication and retention use the matching exclusive tenant/tree lock.
		// Acquiring the shared lock as the transaction's first database operation
		// makes the manifest, member and blob lookup one indivisible read with
		// respect to either writer, without changing the query planner's indexed
		// lookup below.
		if result := transaction.Exec(
			"SELECT pg_advisory_xact_lock_shared(hashtextextended(CAST(? AS text), 0))",
			exactTreeLiteralIndexAdvisoryKey(normalized.ProjectID, normalized.TreeHash),
		); result.Error != nil {
			return fmt.Errorf("lock exact-tree literal index query: %w", result.Error)
		}

		var manifestRow exactTreeLiteralIndexManifestRow
		err := transaction.Where(
			"project_id = ? AND tree_hash = ?",
			projectID, normalized.TreeHash,
		).Take(&manifestRow).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrExactTreeLiteralIndexNotReady
		}
		if err != nil {
			return fmt.Errorf("load exact-tree literal index manifest: %w", err)
		}
		if manifestRow.Status != exactTreeLiteralIndexReadyStatus {
			return exactTreeLiteralIndexConflict(
				"persisted exact-tree literal index manifest is not ready", nil,
			)
		}
		manifest, err := exactTreeLiteralIndexManifestFromRow(manifestRow, true)
		if err != nil {
			return err
		}

		// The manifest and member rows are immutable once ready. Recompute both
		// commitments from complete ordered metadata on every lookup. Bodies remain
		// out of this audit, but an equal-count member swap or partial restore cannot
		// pass merely by preserving aggregate counts.
		var members []exactTreeLiteralIndexMemberRow
		membersResult := transaction.Raw(`
SELECT project_id, tree_hash, path, mode, content_hash, byte_size, indexed
FROM repository_exact_tree_literal_index_members
WHERE project_id = ? AND tree_hash = ?
ORDER BY path COLLATE "C"`, projectID, normalized.TreeHash).Scan(&members)
		if membersResult.Error != nil {
			return fmt.Errorf(
				"load exact-tree literal index member commitment: %w", membersResult.Error,
			)
		}
		if err := verifyExactTreeLiteralIndexMemberCommitment(manifest, members); err != nil {
			return err
		}

		pattern := "%" + escapeExactTreeLiteralLike(normalized.Query) + "%"
		predicate := `(blob.body COLLATE "C") LIKE (CAST(? AS text) COLLATE "C") ESCAPE '!'`
		if !normalized.CaseSensitive {
			pattern = "%" + escapeExactTreeLiteralLike(foldExactTreeLiteralASCII(normalized.Query)) + "%"
			predicate = `translate(blob.body COLLATE "C", 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz') LIKE (CAST(? AS text) COLLATE "C") ESCAPE '!'`
		}
		lookupSQL := `
SELECT
  member.path, member.mode, member.content_hash, member.byte_size
FROM repository_exact_tree_literal_index_members AS member
JOIN repository_exact_tree_literal_index_blobs AS blob
  ON blob.project_id = member.project_id
 AND blob.content_hash = member.content_hash
 AND blob.byte_size = member.byte_size
 AND blob.is_text = member.indexed
WHERE member.project_id = ?
  AND member.tree_hash = ?
  AND member.indexed
  AND blob.is_text
  AND ` + predicate + `
ORDER BY member.path COLLATE "C"
LIMIT ?`
		documents := make([]ExactTreeLiteralCandidateDocument, 0, normalized.MaxDocuments+1)
		result := transaction.Raw(
			lookupSQL, projectID, normalized.TreeHash, pattern, normalized.MaxDocuments+1,
		).Scan(&documents)
		if result.Error != nil {
			return fmt.Errorf("query exact-tree literal index: %w", result.Error)
		}
		more := len(documents) > normalized.MaxDocuments
		if more {
			documents = documents[:normalized.MaxDocuments]
		}
		outcome = ExactTreeLiteralIndexStoreQueryResult{
			Manifest: manifest, Documents: documents, More: more,
		}
		return nil
	})
	if err != nil {
		return ExactTreeLiteralIndexStoreQueryResult{}, err
	}
	return outcome, nil
}

func publishExactTreeLiteralIndexBlobs(
	transaction *gorm.DB,
	projectID uuid.UUID,
	files []ExactTreeLiteralIndexBuildFile,
) error {
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if _, exists := seen[file.ContentHash]; exists {
			continue
		}
		seen[file.ContentHash] = struct{}{}
		body := sql.NullString{}
		if file.Text {
			body = sql.NullString{String: string(file.Body), Valid: true}
		}
		row := exactTreeLiteralIndexBlobRow{
			ProjectID: projectID, ContentHash: file.ContentHash,
			ByteSize: file.ByteSize, IsText: file.Text, Body: body,
		}
		created := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "content_hash"}},
			DoNothing: true,
		}).Create(&row)
		if created.Error != nil {
			return fmt.Errorf("insert exact-tree literal index blob: %w", created.Error)
		}
		var persisted exactTreeLiteralIndexBlobRow
		// ON CONFLICT and the database immutability trigger make the persisted
		// bytes stable. A row lock adds no integrity here and would unnecessarily
		// require the ordinary application role to hold blob UPDATE authority.
		if err := transaction.Where(
			"project_id = ? AND content_hash = ?", projectID, file.ContentHash,
		).Take(&persisted).Error; err != nil {
			return fmt.Errorf("load exact-tree literal index blob: %w", err)
		}
		if persisted.ByteSize != file.ByteSize || persisted.IsText != file.Text ||
			persisted.Body.Valid != file.Text ||
			(file.Text && persisted.Body.String != string(file.Body)) {
			return exactTreeLiteralIndexConflict(
				"deduplicated blob differs from freshly verified source bytes", nil,
			)
		}
	}
	return nil
}

func loadExactTreeLiteralIndexManifestForUpdate(
	database *gorm.DB,
	projectID uuid.UUID,
	treeHash string,
) (exactTreeLiteralIndexManifestRow, bool, error) {
	var row exactTreeLiteralIndexManifestRow
	err := database.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"project_id = ? AND tree_hash = ?", projectID, treeHash,
	).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return exactTreeLiteralIndexManifestRow{}, false, nil
	}
	if err != nil {
		return exactTreeLiteralIndexManifestRow{}, false, fmt.Errorf(
			"load exact-tree literal index manifest: %w", err,
		)
	}
	return row, true, nil
}

func verifyPersistedExactTreeLiteralIndex(
	database *gorm.DB,
	manifest exactTreeLiteralIndexManifestRow,
	build ExactTreeLiteralIndexBuild,
) error {
	ready, err := exactTreeLiteralIndexManifestFromRow(manifest, true)
	if err != nil {
		return exactTreeLiteralIndexConflict("persisted manifest is not a valid ready publication", err)
	}
	if err := validateExactTreeLiteralIndexManifest(ready, build); err != nil {
		return exactTreeLiteralIndexConflict("manifest commitment differs from freshly verified build", err)
	}
	var rows []exactTreeLiteralIndexVerificationRow
	result := database.Raw(`
SELECT
  member.path, member.mode, member.content_hash, member.byte_size, member.indexed,
  blob.is_text, blob.body
FROM repository_exact_tree_literal_index_members AS member
JOIN repository_exact_tree_literal_index_blobs AS blob
  ON blob.project_id = member.project_id
 AND blob.content_hash = member.content_hash
WHERE member.project_id = ? AND member.tree_hash = ?
ORDER BY member.path COLLATE "C"`, manifest.ProjectID, manifest.TreeHash).Scan(&rows)
	if result.Error != nil {
		return fmt.Errorf("verify exact-tree literal index members: %w", result.Error)
	}
	if len(rows) != len(build.Files) {
		return exactTreeLiteralIndexConflict("ready manifest has an incomplete member set", nil)
	}
	for index, row := range rows {
		file := build.Files[index]
		if row.Path != file.Path || row.Mode != file.Mode || row.ContentHash != file.ContentHash ||
			row.ByteSize != file.ByteSize || row.Indexed != file.Text || row.IsText != file.Text ||
			row.Body.Valid != file.Text || (file.Text && row.Body.String != string(file.Body)) {
			return exactTreeLiteralIndexConflict(
				fmt.Sprintf("ready member %d differs from freshly verified build", index), nil,
			)
		}
	}
	return nil
}

func verifyExactTreeLiteralIndexMemberCommitment(
	manifest ExactTreeLiteralIndexManifest,
	members []exactTreeLiteralIndexMemberRow,
) error {
	build := ExactTreeLiteralIndexBuild{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		ProjectID:     manifest.ProjectID,
		TreeHash:      manifest.TreeHash,
		FileCount:     len(members),
		Files:         make([]ExactTreeLiteralIndexBuildFile, len(members)),
	}
	treeFiles := make([]TreeFile, len(members))
	for index, member := range members {
		if member.ProjectID.String() != manifest.ProjectID || member.TreeHash != manifest.TreeHash {
			return exactTreeLiteralIndexConflict(
				"member query escaped the exact tenant/tree fence", nil,
			)
		}
		file, err := normalizeTreeFile(TreeFile{
			Path: member.Path, Mode: member.Mode,
			ContentHash: member.ContentHash, ByteSize: member.ByteSize,
		})
		if err != nil || file.Path != member.Path || file.Mode != member.Mode ||
			file.ContentHash != member.ContentHash || file.ByteSize != member.ByteSize ||
			(index > 0 && member.Path <= members[index-1].Path) {
			return exactTreeLiteralIndexConflict(
				"ready member set is not canonical and strictly path ordered", err,
			)
		}
		treeFiles[index] = file
		build.Files[index] = ExactTreeLiteralIndexBuildFile{
			Path: member.Path, Mode: member.Mode, ContentHash: member.ContentHash,
			ByteSize: member.ByteSize, Text: member.Indexed,
		}
		build.TotalBytes += member.ByteSize
		if member.Indexed {
			build.TextFileCount++
		} else {
			build.SkippedFileCount++
		}
	}
	canonical, err := NewTree(treeFiles)
	if err != nil || canonical.TreeHash != manifest.TreeHash {
		return exactTreeLiteralIndexConflict("member set does not reproduce the exact tree hash", err)
	}
	build.TreeCommitment, build.IndexCommitment, err = exactTreeLiteralIndexCommitments(build)
	if err != nil {
		return exactTreeLiteralIndexContract("recompute ready member commitments", err)
	}
	if manifest.FileCount != build.FileCount || manifest.TextFileCount != build.TextFileCount ||
		manifest.SkippedFileCount != build.SkippedFileCount || manifest.TotalBytes != build.TotalBytes ||
		manifest.TreeCommitment != build.TreeCommitment || manifest.IndexCommitment != build.IndexCommitment {
		return exactTreeLiteralIndexConflict(
			"ready manifest commitments do not describe the complete member set", nil,
		)
	}
	return nil
}

func exactTreeLiteralIndexManifestFromRow(
	row exactTreeLiteralIndexManifestRow,
	reused bool,
) (ExactTreeLiteralIndexManifest, error) {
	if row.ProjectID == uuid.Nil || row.SchemaVersion != ExactTreeLiteralIndexSchemaVersion ||
		!isCanonicalSHA256(row.TreeHash) || row.Status != exactTreeLiteralIndexReadyStatus ||
		row.FileCount < 0 || row.FileCount > MaxTreeFiles || row.TextFileCount < 0 ||
		row.SkippedFileCount < 0 || row.TextFileCount+row.SkippedFileCount != row.FileCount ||
		row.TotalBytes < 0 || row.TotalBytes > MaxTreeBytes ||
		!isCanonicalSHA256(row.TreeCommitment) || !isCanonicalSHA256(row.IndexCommitment) ||
		row.CreatedAt.IsZero() || row.ReadyAt == nil || row.ReadyAt.IsZero() || row.ReadyAt.Before(row.CreatedAt) {
		return ExactTreeLiteralIndexManifest{}, exactTreeLiteralIndexContract(
			"persisted ready manifest contains invalid scalar facts", nil,
		)
	}
	return ExactTreeLiteralIndexManifest{
		SchemaVersion: row.SchemaVersion, ProjectID: row.ProjectID.String(), TreeHash: row.TreeHash,
		FileCount: row.FileCount, TextFileCount: row.TextFileCount,
		SkippedFileCount: row.SkippedFileCount, TotalBytes: row.TotalBytes,
		TreeCommitment: row.TreeCommitment, IndexCommitment: row.IndexCommitment,
		ReadyAt: row.ReadyAt.UTC(), Reused: reused,
	}, nil
}

func escapeExactTreeLiteralLike(value string) string {
	value = strings.ReplaceAll(value, "!", "!!")
	value = strings.ReplaceAll(value, "%", "!%")
	return strings.ReplaceAll(value, "_", "!_")
}

func foldExactTreeLiteralASCII(value string) string {
	buffer := []byte(value)
	for index, character := range buffer {
		if character >= 'A' && character <= 'Z' {
			buffer[index] = character + ('a' - 'A')
		}
	}
	return string(buffer)
}

func exactTreeLiteralIndexAdvisoryKey(projectID, treeHash string) string {
	return projectID + "|" + treeHash
}

func validExactTreeLiteralIndexClaimLease(lease time.Duration) bool {
	return lease >= 50*time.Millisecond && lease <= 5*time.Minute &&
		lease == time.Duration(lease.Milliseconds())*time.Millisecond
}

func validateExactTreeLiteralIndexClaimIdentity(claim ExactTreeLiteralIndexBuildClaim) error {
	if claim.ProjectID != strings.TrimSpace(claim.ProjectID) || !validUUID(claim.ProjectID) ||
		!isCanonicalSHA256(claim.TreeHash) || !validUUID(claim.OwnerToken) || claim.Attempt <= 0 ||
		claim.ReservedSourceBytes < 0 || claim.ReservedSourceBytes > MaxTreeBytes {
		return ErrInvalidExactTreeLiteralIndex
	}
	return nil
}

func verifyActiveExactTreeLiteralIndexBuildClaim(
	database *gorm.DB,
	build ExactTreeLiteralIndexBuild,
) error {
	projectID, _ := uuid.Parse(build.ProjectID)
	ownerToken, _ := uuid.Parse(build.ClaimOwnerToken)
	var active bool
	result := database.Raw(`
SELECT EXISTS (
  SELECT 1
  FROM repository_exact_tree_literal_index_build_claims
  WHERE project_id = ?
    AND tree_hash = ?
    AND owner_token = ?
    AND attempt = ?
    AND reserved_source_bytes = ?
    AND lease_expires_at > clock_timestamp()
) AS active`, projectID, build.TreeHash, ownerToken, build.ClaimAttempt, build.TotalBytes).Scan(&active)
	if result.Error != nil {
		return fmt.Errorf("verify exact-tree literal index build claim: %w", result.Error)
	}
	if !active {
		return ErrExactTreeLiteralBuildClaimLost
	}
	return nil
}

func renewActiveExactTreeLiteralIndexBuildClaim(
	database *gorm.DB,
	build ExactTreeLiteralIndexBuild,
) error {
	projectID, _ := uuid.Parse(build.ProjectID)
	ownerToken, _ := uuid.Parse(build.ClaimOwnerToken)
	var row exactTreeLiteralIndexClaimRenewRow
	result := database.Raw(`
SELECT renewed, current_lease_expires_at
FROM renew_repository_exact_tree_literal_index_build_claim(?, ?, ?, ?, ?)`,
		projectID, build.TreeHash, ownerToken, build.ClaimAttempt,
		int(build.ClaimLease.Milliseconds()),
	).Scan(&row)
	if result.Error != nil {
		return fmt.Errorf("renew exact-tree literal index build claim before commit: %w", result.Error)
	}
	if result.RowsAffected != 1 || !row.Renewed || row.LeaseExpiresAt == nil || row.LeaseExpiresAt.IsZero() {
		return ErrExactTreeLiteralBuildClaimLost
	}
	return nil
}

func loadVerifiedReadyExactTreeLiteralIndex(
	database *gorm.DB,
	projectID uuid.UUID,
	treeHash string,
) (ExactTreeLiteralIndexManifest, bool, error) {
	var row exactTreeLiteralIndexManifestRow
	err := database.Where(
		"project_id = ? AND tree_hash = ?", projectID, treeHash,
	).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ExactTreeLiteralIndexManifest{}, false, nil
	}
	if err != nil {
		return ExactTreeLiteralIndexManifest{}, false, fmt.Errorf(
			"load exact-tree literal index readiness for build claim: %w", err,
		)
	}
	if row.Status != exactTreeLiteralIndexReadyStatus {
		return ExactTreeLiteralIndexManifest{}, false, exactTreeLiteralIndexConflict(
			"persisted exact-tree literal index manifest is not ready", nil,
		)
	}
	manifest, err := exactTreeLiteralIndexManifestFromRow(row, true)
	if err != nil {
		return ExactTreeLiteralIndexManifest{}, false, err
	}
	var members []exactTreeLiteralIndexMemberRow
	result := database.Raw(`
SELECT project_id, tree_hash, path, mode, content_hash, byte_size, indexed
FROM repository_exact_tree_literal_index_members
WHERE project_id = ? AND tree_hash = ?
ORDER BY path COLLATE "C"`, projectID, treeHash).Scan(&members)
	if result.Error != nil {
		return ExactTreeLiteralIndexManifest{}, false, fmt.Errorf(
			"load ready exact-tree literal index members for build claim: %w", result.Error,
		)
	}
	if err := verifyExactTreeLiteralIndexMemberCommitment(manifest, members); err != nil {
		return ExactTreeLiteralIndexManifest{}, false, err
	}
	return manifest, true, nil
}

func exactTreeLiteralIndexConflict(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrExactTreeLiteralIndexConflict, message)
	}
	return errors.Join(
		ErrExactTreeLiteralIndexConflict,
		fmt.Errorf("%s: %w", message, cause),
	)
}

func exactTreeLiteralIndexContract(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrExactTreeLiteralIndexContract, message)
	}
	return errors.Join(
		ErrExactTreeLiteralIndexContract,
		fmt.Errorf("%s: %w", message, cause),
	)
}
