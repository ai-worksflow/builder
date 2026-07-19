package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	ExactTreeLiteralIndexSchemaVersion = "repository-exact-tree-literal-index/v1"

	MaxExactTreeLiteralQueryBytes               = 256
	MinExactTreeLiteralQueryRunes               = 3
	MaxExactTreeLiteralCandidateDocuments       = 500
	MaxExactTreeLiteralCandidateBytes     int64 = 8 << 20

	defaultExactTreeLiteralCandidateDocuments = 100

	exactTreeLiteralIndexBuildClaimLease     = 30 * time.Second
	exactTreeLiteralIndexBuildClaimHeartbeat = 10 * time.Second
	exactTreeLiteralIndexBuildClaimPoll      = 100 * time.Millisecond
	exactTreeLiteralIndexClaimReleaseTimeout = 5 * time.Second

	DefaultExactTreeLiteralIndexProjectTrees        = 16
	DefaultExactTreeLiteralIndexProjectSourceBytes  = int64(256 << 20)
	DefaultExactTreeLiteralIndexProjectActiveBuilds = 2

	maxExactTreeLiteralIndexProjectTrees       = 10_000
	maxExactTreeLiteralIndexProjectSourceBytes = int64(1 << 40)
)

var (
	ErrInvalidExactTreeLiteralIndex     = errors.New("invalid repository exact-tree literal index")
	ErrExactTreeLiteralIndexConflict    = errors.New("repository exact-tree literal index conflicts with persisted state")
	ErrExactTreeLiteralIndexNotReady    = errors.New("repository exact-tree literal index is not ready")
	ErrExactTreeLiteralQueryTooShort    = errors.New("repository exact-tree literal query is too short for the bounded trigram index")
	ErrExactTreeLiteralIndexContract    = errors.New("repository exact-tree literal index store violated its contract")
	ErrExactTreeLiteralBuildClaimLost   = errors.New("repository exact-tree literal index build claim was lost")
	ErrExactTreeLiteralClaimRelease     = errors.New("repository exact-tree literal index build claim release failed")
	ErrExactTreeLiteralProjectTreeQuota = errors.New(
		"repository exact-tree literal index project tree quota exceeded",
	)
	ErrExactTreeLiteralProjectSourceBytesQuota = errors.New(
		"repository exact-tree literal index project source-byte quota exceeded",
	)
	ErrExactTreeLiteralProjectActiveBuildQuota = errors.New(
		"repository exact-tree literal index project active-build quota exceeded",
	)
)

// ExactTreeLiteralBlobResolver is the tenant-scoped source boundary used by
// the builder. FileBlobService satisfies this interface. An index build never
// accepts file bodies, content-store references, or hashes from a caller.
type ExactTreeLiteralBlobResolver interface {
	Resolve(context.Context, string, string, int64) (FileBlobPointer, []byte, error)
}

// ExactTreeLiteralIndexBuildFile is one canonical tree member after the
// builder has resolved and verified its exact source bytes. Body is retained
// only for text files; binary and invalid UTF-8 files remain manifest members
// with Text=false and no derived body.
type ExactTreeLiteralIndexBuildFile struct {
	Path        string
	Mode        string
	ContentHash string
	ByteSize    int64
	Text        bool
	Body        []byte
}

// ExactTreeLiteralIndexBuild is the complete immutable publication passed to
// the durable store. TreeCommitment commits the canonical tree membership;
// IndexCommitment additionally commits the text/skipped classification.
type ExactTreeLiteralIndexBuild struct {
	SchemaVersion    string
	ProjectID        string
	TreeHash         string
	ClaimOwnerToken  string
	ClaimAttempt     int64
	ClaimLease       time.Duration
	FileCount        int
	TextFileCount    int
	SkippedFileCount int
	TotalBytes       int64
	TreeCommitment   string
	IndexCommitment  string
	Files            []ExactTreeLiteralIndexBuildFile
}

type ExactTreeLiteralIndexManifest struct {
	SchemaVersion    string    `json:"schemaVersion"`
	ProjectID        string    `json:"projectId"`
	TreeHash         string    `json:"treeHash"`
	FileCount        int       `json:"fileCount"`
	TextFileCount    int       `json:"textFileCount"`
	SkippedFileCount int       `json:"skippedFileCount"`
	TotalBytes       int64     `json:"totalBytes"`
	TreeCommitment   string    `json:"treeCommitment"`
	IndexCommitment  string    `json:"indexCommitment"`
	ReadyAt          time.Time `json:"readyAt"`
	Reused           bool      `json:"reused"`
}

type ExactTreeLiteralIndexStoreQuery struct {
	ProjectID     string
	TreeHash      string
	Query         string
	CaseSensitive bool
	MaxDocuments  int
}

type ExactTreeLiteralCandidateDocument struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	ContentHash string `json:"contentHash"`
	ByteSize    int64  `json:"byteSize"`
}

type ExactTreeLiteralIndexStoreQueryResult struct {
	Manifest  ExactTreeLiteralIndexManifest
	Documents []ExactTreeLiteralCandidateDocument
	More      bool
}

type ExactTreeLiteralBuildClaimDisposition string

const (
	ExactTreeLiteralBuildClaimAcquired ExactTreeLiteralBuildClaimDisposition = "acquired"
	ExactTreeLiteralBuildClaimWaiting  ExactTreeLiteralBuildClaimDisposition = "waiting"
	ExactTreeLiteralBuildClaimReady    ExactTreeLiteralBuildClaimDisposition = "ready"
)

type ExactTreeLiteralIndexBuildClaim struct {
	ProjectID           string
	TreeHash            string
	OwnerToken          string
	Attempt             int64
	ReservedSourceBytes int64
	LeaseExpiresAt      time.Time
}

type ExactTreeLiteralIndexBuildClaimRequest struct {
	ProjectID              string
	TreeHash               string
	OwnerToken             string
	SourceBytes            int64
	MaxProjectTrees        int
	MaxProjectSourceBytes  int64
	MaxProjectActiveBuilds int
	Lease                  time.Duration
}

type ExactTreeLiteralIndexBuildClaimResult struct {
	Disposition ExactTreeLiteralBuildClaimDisposition
	Claim       ExactTreeLiteralIndexBuildClaim
	Manifest    ExactTreeLiteralIndexManifest
}

// ExactTreeLiteralIndexStore publishes complete builds atomically and returns
// only bounded candidate documents for one exact immutable tree. It is a
// derived accelerator, never a source-of-truth replacement.
type ExactTreeLiteralIndexStore interface {
	AcquireExactTreeLiteralIndexBuildClaim(context.Context, ExactTreeLiteralIndexBuildClaimRequest) (ExactTreeLiteralIndexBuildClaimResult, error)
	RenewExactTreeLiteralIndexBuildClaim(context.Context, ExactTreeLiteralIndexBuildClaim, time.Duration) (ExactTreeLiteralIndexBuildClaim, error)
	ReleaseExactTreeLiteralIndexBuildClaim(context.Context, ExactTreeLiteralIndexBuildClaim) error
	PublishExactTreeLiteralIndex(context.Context, ExactTreeLiteralIndexBuild) (ExactTreeLiteralIndexManifest, error)
	QueryExactTreeLiteralIndex(context.Context, ExactTreeLiteralIndexStoreQuery) (ExactTreeLiteralIndexStoreQueryResult, error)
}

type ExactTreeLiteralIndexQuery struct {
	ProjectID     string `json:"projectId"`
	TreeHash      string `json:"treeHash"`
	Query         string `json:"query"`
	CaseSensitive bool   `json:"caseSensitive"`
	MaxDocuments  int    `json:"maxDocuments,omitempty"`
}

type ExactTreeLiteralIndexLimits struct {
	MinQueryRunes     int   `json:"minQueryRunes"`
	MaxQueryBytes     int   `json:"maxQueryBytes"`
	MaxDocuments      int   `json:"maxDocuments"`
	MaxCandidateBytes int64 `json:"maxCandidateBytes"`
}

// ExactTreeLiteralIndexQueryResult deliberately contains no match positions
// or previews. The caller must recheck its opening/closing Candidate head and
// locate the literal in freshly resolved authoritative source bytes.
type ExactTreeLiteralIndexQueryResult struct {
	SchemaVersion   string                              `json:"schemaVersion"`
	ProjectID       string                              `json:"projectId"`
	TreeHash        string                              `json:"treeHash"`
	IndexCommitment string                              `json:"indexCommitment"`
	Query           string                              `json:"query"`
	CaseSensitive   bool                                `json:"caseSensitive"`
	Limits          ExactTreeLiteralIndexLimits         `json:"limits"`
	CandidateBytes  int64                               `json:"candidateBytes"`
	Truncated       bool                                `json:"truncated"`
	Documents       []ExactTreeLiteralCandidateDocument `json:"documents"`
}

type ExactTreeLiteralIndexService struct {
	store                 ExactTreeLiteralIndexStore
	blobs                 ExactTreeLiteralBlobResolver
	firstBuilderAdmission ExactTreeSearchAdmission
	claimLease            time.Duration
	claimHeartbeat        time.Duration
	claimPoll             time.Duration
	releaseTimeout        time.Duration
	newClaimOwner         func() string
	projectQuota          ExactTreeLiteralIndexProjectQuota
}

// ExactTreeLiteralIndexProjectQuota bounds logical source retained by ready
// manifests plus source reserved by live build claims. It is deliberately
// independent from physical blob deduplication so it can be reserved before
// any source bytes are resolved.
type ExactTreeLiteralIndexProjectQuota struct {
	MaxTrees        int
	MaxSourceBytes  int64
	MaxActiveBuilds int
}

// ExactTreeLiteralIndexAdmissionConfig is the production construction
// boundary. A service built through NewAdmittedExactTreeLiteralIndexService
// cannot use the actor-less legacy Build method and admits only the process
// that actually owns a newly acquired durable build claim.
type ExactTreeLiteralIndexAdmissionConfig struct {
	ProjectQuota          ExactTreeLiteralIndexProjectQuota
	FirstBuilderAdmission ExactTreeSearchAdmission
}

func NewExactTreeLiteralIndexService(
	store ExactTreeLiteralIndexStore,
	blobs ExactTreeLiteralBlobResolver,
	configs ...ExactTreeLiteralIndexProjectQuota,
) (*ExactTreeLiteralIndexService, error) {
	if store == nil || blobs == nil {
		return nil, errors.New("repository exact-tree literal index store and blob resolver are required")
	}
	quota, err := normalizeExactTreeLiteralIndexProjectQuota(configs)
	if err != nil {
		return nil, err
	}
	return &ExactTreeLiteralIndexService{
		store: store, blobs: blobs,
		claimLease:     exactTreeLiteralIndexBuildClaimLease,
		claimHeartbeat: exactTreeLiteralIndexBuildClaimHeartbeat,
		claimPoll:      exactTreeLiteralIndexBuildClaimPoll,
		releaseTimeout: exactTreeLiteralIndexClaimReleaseTimeout,
		newClaimOwner:  uuid.NewString,
		projectQuota:   quota,
	}, nil
}

func NewAdmittedExactTreeLiteralIndexService(
	store ExactTreeLiteralIndexStore,
	blobs ExactTreeLiteralBlobResolver,
	config ExactTreeLiteralIndexAdmissionConfig,
) (*ExactTreeLiteralIndexService, error) {
	if config.FirstBuilderAdmission == nil {
		return nil, ErrExactTreeSearchAdmissionInvalid
	}
	service, err := NewExactTreeLiteralIndexService(store, blobs, config.ProjectQuota)
	if err != nil {
		return nil, err
	}
	service.firstBuilderAdmission = config.FirstBuilderAdmission
	return service, nil
}

func normalizeExactTreeLiteralIndexProjectQuota(
	configs []ExactTreeLiteralIndexProjectQuota,
) (ExactTreeLiteralIndexProjectQuota, error) {
	if len(configs) > 1 {
		return ExactTreeLiteralIndexProjectQuota{}, ErrInvalidExactTreeLiteralIndex
	}
	quota := ExactTreeLiteralIndexProjectQuota{
		MaxTrees:        DefaultExactTreeLiteralIndexProjectTrees,
		MaxSourceBytes:  DefaultExactTreeLiteralIndexProjectSourceBytes,
		MaxActiveBuilds: DefaultExactTreeLiteralIndexProjectActiveBuilds,
	}
	if len(configs) == 1 {
		configured := configs[0]
		if configured.MaxTrees != 0 {
			quota.MaxTrees = configured.MaxTrees
		}
		if configured.MaxSourceBytes != 0 {
			quota.MaxSourceBytes = configured.MaxSourceBytes
		}
		if configured.MaxActiveBuilds != 0 {
			quota.MaxActiveBuilds = configured.MaxActiveBuilds
		}
	}
	if !validExactTreeLiteralIndexProjectQuota(quota) {
		return ExactTreeLiteralIndexProjectQuota{}, ErrInvalidExactTreeLiteralIndex
	}
	return quota, nil
}

func validExactTreeLiteralIndexProjectQuota(quota ExactTreeLiteralIndexProjectQuota) bool {
	return quota.MaxTrees >= 1 && quota.MaxTrees <= maxExactTreeLiteralIndexProjectTrees &&
		quota.MaxSourceBytes >= 1 && quota.MaxSourceBytes <= maxExactTreeLiteralIndexProjectSourceBytes &&
		quota.MaxActiveBuilds >= 1 && quota.MaxActiveBuilds <= quota.MaxTrees
}

// Build acquires the durable tenant/tree single-builder claim before resolving
// any FileBlob. Concurrent waiters poll with no held connection and reuse the
// owner's immutable ready publication.
func (service *ExactTreeLiteralIndexService) Build(
	ctx context.Context,
	projectID string,
	tree TreeManifest,
) (ExactTreeLiteralIndexManifest, error) {
	if service == nil {
		return ExactTreeLiteralIndexManifest{}, ErrInvalidExactTreeLiteralIndex
	}
	if service.firstBuilderAdmission != nil {
		return ExactTreeLiteralIndexManifest{}, ErrExactTreeSearchAdmissionInvalid
	}
	return service.build(ctx, projectID, "", tree)
}

// BuildForActor is the production entry point. Actor identity is used only by
// first-builder admission and is never persisted in the immutable index.
func (service *ExactTreeLiteralIndexService) BuildForActor(
	ctx context.Context,
	projectID, actorID string,
	tree TreeManifest,
) (ExactTreeLiteralIndexManifest, error) {
	if service == nil || service.firstBuilderAdmission == nil {
		return ExactTreeLiteralIndexManifest{}, ErrExactTreeSearchAdmissionUnavailable
	}
	if ctx == nil || !canonicalExactTreeSearchAdmissionUUID(projectID) ||
		!canonicalExactTreeSearchAdmissionUUID(actorID) {
		return ExactTreeLiteralIndexManifest{}, ErrExactTreeSearchAdmissionInvalid
	}
	return service.build(ctx, projectID, actorID, tree)
}

func (service *ExactTreeLiteralIndexService) build(
	ctx context.Context,
	projectID, actorID string,
	tree TreeManifest,
) (ExactTreeLiteralIndexManifest, error) {
	if ctx == nil || projectID != strings.TrimSpace(projectID) || !validUUID(projectID) {
		return ExactTreeLiteralIndexManifest{}, ErrInvalidExactTreeLiteralIndex
	}
	canonical, err := ParseTree(tree)
	if err != nil {
		return ExactTreeLiteralIndexManifest{}, errors.Join(ErrInvalidExactTreeLiteralIndex, err)
	}
	return service.buildWithDurableClaim(ctx, projectID, actorID, canonical)
}

func (service *ExactTreeLiteralIndexService) resolveClaimedBuild(
	ctx context.Context,
	projectID string,
	canonical TreeManifest,
	claim ExactTreeLiteralIndexBuildClaim,
) (ExactTreeLiteralIndexBuild, error) {
	build := ExactTreeLiteralIndexBuild{
		SchemaVersion:   ExactTreeLiteralIndexSchemaVersion,
		ProjectID:       projectID,
		TreeHash:        canonical.TreeHash,
		ClaimOwnerToken: claim.OwnerToken,
		ClaimAttempt:    claim.Attempt,
		ClaimLease:      service.claimLease,
		FileCount:       len(canonical.Files),
		Files:           make([]ExactTreeLiteralIndexBuildFile, 0, len(canonical.Files)),
	}
	for _, file := range canonical.Files {
		if err := ctx.Err(); err != nil {
			return ExactTreeLiteralIndexBuild{}, err
		}
		pointer, value, resolveErr := service.blobs.Resolve(
			ctx, projectID, file.ContentHash, file.ByteSize,
		)
		if resolveErr != nil {
			return ExactTreeLiteralIndexBuild{}, fmt.Errorf(
				"resolve exact-tree index file %s: %w", file.Path, resolveErr,
			)
		}
		if pointerErr := validateCatalogPointer(
			pointer, projectID, file.ContentHash, file.ByteSize,
		); pointerErr != nil || int64(len(value)) != file.ByteSize ||
			rawFileContentHash(value) != file.ContentHash {
			return ExactTreeLiteralIndexBuild{}, errors.Join(
				ErrExactTreeLiteralIndexContract,
				fmt.Errorf("resolved file %s differs from the exact canonical tree", file.Path),
				pointerErr,
			)
		}
		indexed := ExactTreeLiteralIndexBuildFile{
			Path: file.Path, Mode: file.Mode, ContentHash: file.ContentHash,
			ByteSize: file.ByteSize, Text: isCandidateSearchText(value),
		}
		if indexed.Text {
			indexed.Body = append([]byte(nil), value...)
			build.TextFileCount++
		} else {
			build.SkippedFileCount++
		}
		build.TotalBytes += file.ByteSize
		build.Files = append(build.Files, indexed)
	}
	treeCommitment, indexCommitment, err := exactTreeLiteralIndexCommitments(build)
	if err != nil {
		return ExactTreeLiteralIndexBuild{}, err
	}
	build.TreeCommitment, build.IndexCommitment = treeCommitment, indexCommitment
	return build, nil
}

func (service *ExactTreeLiteralIndexService) QueryCandidateDocuments(
	ctx context.Context,
	input ExactTreeLiteralIndexQuery,
) (ExactTreeLiteralIndexQueryResult, error) {
	if ctx == nil {
		return ExactTreeLiteralIndexQueryResult{}, ErrInvalidExactTreeLiteralIndex
	}
	normalized, err := normalizeExactTreeLiteralIndexQuery(input)
	if err != nil {
		return ExactTreeLiteralIndexQueryResult{}, err
	}
	stored, err := service.store.QueryExactTreeLiteralIndex(ctx, ExactTreeLiteralIndexStoreQuery(normalized))
	if err != nil {
		return ExactTreeLiteralIndexQueryResult{}, err
	}
	if stored.Manifest.SchemaVersion != ExactTreeLiteralIndexSchemaVersion ||
		stored.Manifest.ProjectID != normalized.ProjectID || stored.Manifest.TreeHash != normalized.TreeHash ||
		!isCanonicalSHA256(stored.Manifest.IndexCommitment) || stored.Manifest.ReadyAt.IsZero() {
		return ExactTreeLiteralIndexQueryResult{}, ErrExactTreeLiteralIndexContract
	}
	result := ExactTreeLiteralIndexQueryResult{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		ProjectID:     normalized.ProjectID, TreeHash: normalized.TreeHash,
		IndexCommitment: stored.Manifest.IndexCommitment,
		Query:           normalized.Query, CaseSensitive: normalized.CaseSensitive,
		Limits: ExactTreeLiteralIndexLimits{
			MinQueryRunes:     MinExactTreeLiteralQueryRunes,
			MaxQueryBytes:     MaxExactTreeLiteralQueryBytes,
			MaxDocuments:      normalized.MaxDocuments,
			MaxCandidateBytes: MaxExactTreeLiteralCandidateBytes,
		},
		Documents: make([]ExactTreeLiteralCandidateDocument, 0, len(stored.Documents)),
	}
	previousPath := ""
	for index, document := range stored.Documents {
		if err := ctx.Err(); err != nil {
			return ExactTreeLiteralIndexQueryResult{}, err
		}
		file, fileErr := normalizeTreeFile(TreeFile{
			Path: document.Path, Mode: document.Mode,
			ContentHash: document.ContentHash, ByteSize: document.ByteSize,
		})
		if fileErr != nil || file.Path != document.Path || file.Mode != document.Mode ||
			file.ContentHash != document.ContentHash || file.ByteSize != document.ByteSize ||
			(index > 0 && document.Path <= previousPath) {
			return ExactTreeLiteralIndexQueryResult{}, errors.Join(
				ErrExactTreeLiteralIndexContract, fileErr,
			)
		}
		if len(result.Documents) >= normalized.MaxDocuments ||
			result.CandidateBytes+document.ByteSize > MaxExactTreeLiteralCandidateBytes {
			result.Truncated = true
			break
		}
		result.Documents = append(result.Documents, document)
		result.CandidateBytes += document.ByteSize
		previousPath = document.Path
	}
	if stored.More || len(stored.Documents) > len(result.Documents) {
		result.Truncated = true
	}
	return result, nil
}

func normalizeExactTreeLiteralIndexQuery(
	input ExactTreeLiteralIndexQuery,
) (ExactTreeLiteralIndexQuery, error) {
	if input.ProjectID != strings.TrimSpace(input.ProjectID) || !validUUID(input.ProjectID) ||
		input.TreeHash != strings.TrimSpace(input.TreeHash) || !isCanonicalSHA256(input.TreeHash) ||
		input.Query == "" ||
		!utf8.ValidString(input.Query) || len([]byte(input.Query)) > MaxExactTreeLiteralQueryBytes {
		return ExactTreeLiteralIndexQuery{}, ErrInvalidExactTreeLiteralIndex
	}
	for _, character := range input.Query {
		if unicode.IsControl(character) {
			return ExactTreeLiteralIndexQuery{}, ErrInvalidExactTreeLiteralIndex
		}
	}
	if utf8.RuneCountInString(input.Query) < MinExactTreeLiteralQueryRunes ||
		!hasExactTreeLiteralTrigram(input.Query) {
		return ExactTreeLiteralIndexQuery{}, ErrExactTreeLiteralQueryTooShort
	}
	// The indexed translate(body) expression folds only ASCII, matching
	// Candidate search's current deterministic case-insensitive contract.
	if !input.CaseSensitive && !isASCII(input.Query) {
		return ExactTreeLiteralIndexQuery{}, ErrInvalidExactTreeLiteralIndex
	}
	if input.MaxDocuments == 0 {
		input.MaxDocuments = defaultExactTreeLiteralCandidateDocuments
	}
	if input.MaxDocuments < 1 || input.MaxDocuments > MaxExactTreeLiteralCandidateDocuments {
		return ExactTreeLiteralIndexQuery{}, ErrInvalidExactTreeLiteralIndex
	}
	return input, nil
}

func hasExactTreeLiteralTrigram(value string) bool {
	consecutive := 0
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			consecutive++
			if consecutive >= MinExactTreeLiteralQueryRunes {
				return true
			}
		} else {
			consecutive = 0
		}
	}
	return false
}

type exactTreeLiteralTreeCommitment struct {
	SchemaVersion string     `json:"schemaVersion"`
	ProjectID     string     `json:"projectId"`
	TreeHash      string     `json:"treeHash"`
	Files         []TreeFile `json:"files"`
}

type exactTreeLiteralIndexCommitmentFile struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	ContentHash string `json:"contentHash"`
	ByteSize    int64  `json:"byteSize"`
	Text        bool   `json:"text"`
}

type exactTreeLiteralIndexCommitment struct {
	SchemaVersion    string                                `json:"schemaVersion"`
	ProjectID        string                                `json:"projectId"`
	TreeHash         string                                `json:"treeHash"`
	TreeCommitment   string                                `json:"treeCommitment"`
	FileCount        int                                   `json:"fileCount"`
	TextFileCount    int                                   `json:"textFileCount"`
	SkippedFileCount int                                   `json:"skippedFileCount"`
	TotalBytes       int64                                 `json:"totalBytes"`
	Files            []exactTreeLiteralIndexCommitmentFile `json:"files"`
}

func exactTreeLiteralIndexCommitments(build ExactTreeLiteralIndexBuild) (string, string, error) {
	treeFiles := make([]TreeFile, len(build.Files))
	indexFiles := make([]exactTreeLiteralIndexCommitmentFile, len(build.Files))
	for index, file := range build.Files {
		treeFiles[index] = TreeFile{
			Path: file.Path, Mode: file.Mode, ContentHash: file.ContentHash, ByteSize: file.ByteSize,
		}
		indexFiles[index] = exactTreeLiteralIndexCommitmentFile{
			Path: file.Path, Mode: file.Mode, ContentHash: file.ContentHash,
			ByteSize: file.ByteSize, Text: file.Text,
		}
	}
	treeDigest, err := domain.CanonicalHash(exactTreeLiteralTreeCommitment{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		ProjectID:     build.ProjectID, TreeHash: build.TreeHash, Files: treeFiles,
	})
	if err != nil {
		return "", "", fmt.Errorf("hash exact-tree literal index tree commitment: %w", err)
	}
	treeCommitment := "sha256:" + treeDigest
	indexDigest, err := domain.CanonicalHash(exactTreeLiteralIndexCommitment{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		ProjectID:     build.ProjectID, TreeHash: build.TreeHash,
		TreeCommitment: treeCommitment,
		FileCount:      build.FileCount, TextFileCount: build.TextFileCount,
		SkippedFileCount: build.SkippedFileCount, TotalBytes: build.TotalBytes,
		Files: indexFiles,
	})
	if err != nil {
		return "", "", fmt.Errorf("hash exact-tree literal index commitment: %w", err)
	}
	return treeCommitment, "sha256:" + indexDigest, nil
}

func validateExactTreeLiteralIndexBuild(build ExactTreeLiteralIndexBuild) error {
	if build.SchemaVersion != ExactTreeLiteralIndexSchemaVersion ||
		build.ProjectID != strings.TrimSpace(build.ProjectID) || !validUUID(build.ProjectID) ||
		!isCanonicalSHA256(build.TreeHash) || !validUUID(build.ClaimOwnerToken) ||
		build.ClaimAttempt <= 0 || !validExactTreeLiteralIndexClaimLease(build.ClaimLease) ||
		build.FileCount != len(build.Files) ||
		build.FileCount < 0 || build.FileCount > MaxTreeFiles ||
		build.TextFileCount < 0 || build.SkippedFileCount < 0 ||
		build.TextFileCount+build.SkippedFileCount != build.FileCount ||
		build.TotalBytes < 0 || build.TotalBytes > MaxTreeBytes {
		return ErrInvalidExactTreeLiteralIndex
	}
	treeFiles := make([]TreeFile, 0, len(build.Files))
	textFiles := 0
	totalBytes := int64(0)
	for _, file := range build.Files {
		normalized, err := normalizeTreeFile(TreeFile{
			Path: file.Path, Mode: file.Mode, ContentHash: file.ContentHash, ByteSize: file.ByteSize,
		})
		if err != nil || normalized.Path != file.Path || normalized.Mode != file.Mode ||
			normalized.ContentHash != file.ContentHash || normalized.ByteSize != file.ByteSize {
			return errors.Join(ErrInvalidExactTreeLiteralIndex, err)
		}
		if file.Text {
			if !isCandidateSearchText(file.Body) || int64(len(file.Body)) != file.ByteSize ||
				rawFileContentHash(file.Body) != file.ContentHash {
				return ErrInvalidExactTreeLiteralIndex
			}
			textFiles++
		} else if file.Body != nil {
			return ErrInvalidExactTreeLiteralIndex
		}
		treeFiles = append(treeFiles, normalized)
		totalBytes += file.ByteSize
	}
	canonical, err := NewTree(treeFiles)
	if err != nil || canonical.TreeHash != build.TreeHash {
		return errors.Join(ErrInvalidExactTreeLiteralIndex, err)
	}
	for index := range canonical.Files {
		if canonical.Files[index] != treeFiles[index] {
			return ErrInvalidExactTreeLiteralIndex
		}
	}
	if textFiles != build.TextFileCount || totalBytes != build.TotalBytes {
		return ErrInvalidExactTreeLiteralIndex
	}
	treeCommitment, indexCommitment, err := exactTreeLiteralIndexCommitments(build)
	if err != nil || treeCommitment != build.TreeCommitment || indexCommitment != build.IndexCommitment {
		return errors.Join(ErrInvalidExactTreeLiteralIndex, err)
	}
	return nil
}

func validateExactTreeLiteralIndexManifest(
	manifest ExactTreeLiteralIndexManifest,
	build ExactTreeLiteralIndexBuild,
) error {
	if manifest.SchemaVersion != build.SchemaVersion || manifest.ProjectID != build.ProjectID ||
		manifest.TreeHash != build.TreeHash || manifest.FileCount != build.FileCount ||
		manifest.TextFileCount != build.TextFileCount || manifest.SkippedFileCount != build.SkippedFileCount ||
		manifest.TotalBytes != build.TotalBytes || manifest.TreeCommitment != build.TreeCommitment ||
		manifest.IndexCommitment != build.IndexCommitment || manifest.ReadyAt.IsZero() {
		return ErrExactTreeLiteralIndexContract
	}
	return nil
}
