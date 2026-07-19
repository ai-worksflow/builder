package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
)

const (
	CandidateSearchSchemaVersion = "repository-candidate-search/v1"

	// Candidate search is deliberately a bounded exact-snapshot scan. These
	// ceilings keep it safe without pretending to be a repository-scale index.
	MaxCandidateSearchQueryBytes         = 256
	MaxCandidateSearchIncludeGlobs       = 16
	MaxCandidateSearchGlobBytes          = 256
	MaxCandidateSearchFiles              = 2_000
	MaxCandidateSearchBytes        int64 = 8 << 20
	MaxCandidateSearchMatches            = 500
	MaxCandidateSearchPreviewBytes       = 320

	defaultCandidateSearchMatches = 100
	searchCancellationStride      = 64 << 10
)

var (
	ErrInvalidCandidateSearch          = errors.New("invalid repository Candidate search")
	ErrCandidateSearchDrift            = errors.New("repository Candidate search head changed")
	ErrCandidateSearchIndexUnavailable = errors.New("repository Candidate search index is unavailable")
)

// CandidateReadAuthorizer separates read access from edit access. Bootstrap
// and mutations require edit; Candidate reads and exact-head search require
// view. The authenticated actor always comes from the transport/session.
type CandidateReadAuthorizer interface {
	MutationAuthorizer
	RequireProjectView(context.Context, string, string) error
}

type CandidateSearchFileReader interface {
	Resolve(context.Context, string, string, int64) (FileBlobPointer, []byte, error)
}

type CandidateSearchInput struct {
	ProjectID              string   `json:"projectId"`
	CandidateID            string   `json:"candidateId"`
	ExpectedHeadGeneration uint64   `json:"expectedHeadGeneration"`
	ExpectedRootHash       string   `json:"expectedRootHash"`
	Query                  string   `json:"query"`
	CaseSensitive          bool     `json:"caseSensitive"`
	IncludeGlobs           []string `json:"includeGlobs,omitempty"`
	MaxMatches             int      `json:"maxMatches,omitempty"`
	ActorID                string   `json:"-"`
}

type CandidateSearchHead struct {
	CandidateID string `json:"candidateId"`
	Generation  uint64 `json:"generation"`
	RootHash    string `json:"rootHash"`
}

type CandidateSearchLimits struct {
	MaxQueryBytes   int   `json:"maxQueryBytes"`
	MaxIncludeGlobs int   `json:"maxIncludeGlobs"`
	MaxGlobBytes    int   `json:"maxGlobBytes"`
	MaxFiles        int   `json:"maxFiles"`
	MaxBytes        int64 `json:"maxBytes"`
	MaxMatches      int   `json:"maxMatches"`
	MaxPreviewBytes int   `json:"maxPreviewBytes"`
}

type CandidateSearchStats struct {
	FilesScanned       int   `json:"filesScanned"`
	BytesScanned       int64 `json:"bytesScanned"`
	BinaryFilesSkipped int   `json:"binaryFilesSkipped"`
}

type CandidateSearchMatch struct {
	Path             string `json:"path"`
	Line             int    `json:"line"`
	Column           int    `json:"column"`
	Preview          string `json:"preview"`
	PreviewTruncated bool   `json:"previewTruncated"`
	ContentHash      string `json:"contentHash"`
}

type CandidateSearchResult struct {
	SchemaVersion string                 `json:"schemaVersion"`
	ProjectID     string                 `json:"projectId"`
	Head          CandidateSearchHead    `json:"head"`
	Query         string                 `json:"query"`
	CaseSensitive bool                   `json:"caseSensitive"`
	IncludeGlobs  []string               `json:"includeGlobs"`
	Truncated     bool                   `json:"truncated"`
	Limits        CandidateSearchLimits  `json:"limits"`
	Stats         CandidateSearchStats   `json:"stats"`
	Matches       []CandidateSearchMatch `json:"matches"`
}

// SearchCandidate performs literal search over one explicitly fenced
// Candidate generation/root. A durable exact-tree index may reduce the files
// considered, but every candidate must still match the opening canonical tree
// and is re-resolved from tenant-scoped FileBlob authority before matching.
// The closing head recheck makes concurrent drift fail closed.
func (service *CandidateBootstrapService) SearchCandidate(
	ctx context.Context,
	input CandidateSearchInput,
) (CandidateSearchResult, error) {
	if ctx == nil {
		return CandidateSearchResult{}, ErrInvalidCandidateSearch
	}
	input, err := normalizeCandidateSearchInput(input)
	if err != nil {
		return CandidateSearchResult{}, err
	}
	if err := service.access.RequireProjectView(ctx, input.ProjectID, input.ActorID); err != nil {
		return CandidateSearchResult{}, fmt.Errorf("authorize repository Candidate search: %w", err)
	}
	if err := service.admitCandidateSearch(ctx, input); err != nil {
		return CandidateSearchResult{}, err
	}
	record, err := service.loadSearchCandidate(ctx, input.ProjectID, input.CandidateID)
	if err != nil {
		return CandidateSearchResult{}, err
	}
	if !candidateSearchHeadMatches(record.Candidate, input) {
		return CandidateSearchResult{}, ErrCandidateSearchDrift
	}

	result := newCandidateSearchResult(input, record.Candidate)
	files, indexTruncated, indexed, err := service.candidateSearchFiles(
		ctx, input, record.Candidate.CurrentTree,
	)
	if err != nil {
		return CandidateSearchResult{}, err
	}
	result.Truncated = indexTruncated
	needle := []byte(input.Query)
	queryBytes := len(needle)
	if !input.CaseSensitive {
		needle, err = foldSearchASCII(ctx, needle)
		if err != nil {
			return CandidateSearchResult{}, err
		}
	}

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return CandidateSearchResult{}, err
		}
		if !indexed && !candidateSearchPathIncluded(file.Path, input.IncludeGlobs) {
			continue
		}
		if result.Stats.FilesScanned >= MaxCandidateSearchFiles ||
			result.Stats.BytesScanned+file.ByteSize > MaxCandidateSearchBytes {
			result.Truncated = true
			break
		}

		pointer, value, resolveErr := service.files.Resolve(ctx, input.ProjectID, file.ContentHash, file.ByteSize)
		if resolveErr != nil {
			return CandidateSearchResult{}, fmt.Errorf("resolve exact search file %s: %w", file.Path, resolveErr)
		}
		if err := validateCatalogPointer(pointer, input.ProjectID, file.ContentHash, file.ByteSize); err != nil ||
			int64(len(value)) != file.ByteSize || rawFileContentHash(value) != file.ContentHash {
			return CandidateSearchResult{}, fmt.Errorf("%w: resolved search file %s drifted from the exact tree", ErrFileBlobCatalogContract, file.Path)
		}
		result.Stats.FilesScanned++
		result.Stats.BytesScanned += int64(len(value))
		if !isCandidateSearchText(value) {
			if indexed {
				return CandidateSearchResult{}, fmt.Errorf(
					"%w: indexed candidate file %s is no longer classified as text",
					ErrExactTreeLiteralIndexConflict, file.Path,
				)
			}
			result.Stats.BinaryFilesSkipped++
			continue
		}

		searchValue := value
		if !input.CaseSensitive {
			searchValue, err = foldSearchASCII(ctx, value)
			if err != nil {
				return CandidateSearchResult{}, err
			}
		}
		positioner := candidateSearchPositioner{
			value: value, line: 1, column: 1, previewLineStart: -1,
		}
		for offset := 0; offset <= len(searchValue)-len(needle); {
			if err := ctx.Err(); err != nil {
				return CandidateSearchResult{}, err
			}
			relative := bytes.Index(searchValue[offset:], needle)
			if relative < 0 {
				break
			}
			matchOffset := offset + relative
			line, column, positionErr := positioner.position(ctx, matchOffset)
			if positionErr != nil {
				return CandidateSearchResult{}, positionErr
			}
			preview, previewTruncated := positioner.preview(matchOffset, queryBytes)
			result.Matches = append(result.Matches, CandidateSearchMatch{
				Path: file.Path, Line: line, Column: column,
				Preview: preview, PreviewTruncated: previewTruncated,
				ContentHash: file.ContentHash,
			})
			if len(result.Matches) >= result.Limits.MaxMatches {
				result.Truncated = true
				break
			}
			// Advance one byte so overlapping literal matches remain visible.
			offset = matchOffset + 1
		}
		if result.Truncated && len(result.Matches) >= result.Limits.MaxMatches {
			break
		}
	}

	current, err := service.loadSearchCandidate(ctx, input.ProjectID, input.CandidateID)
	if err != nil {
		if errors.Is(err, ErrCandidateNotFound) {
			return CandidateSearchResult{}, ErrCandidateSearchDrift
		}
		return CandidateSearchResult{}, err
	}
	if current.Candidate.Version != record.Candidate.Version ||
		current.Candidate.CurrentTree.TreeHash != record.Candidate.CurrentTree.TreeHash {
		return CandidateSearchResult{}, ErrCandidateSearchDrift
	}
	return result, nil
}

func (service *CandidateBootstrapService) admitCandidateSearch(
	ctx context.Context,
	input CandidateSearchInput,
) error {
	if service.searchAdmission == nil {
		// A production-constructed indexed service cannot reach this state. Keep
		// legacy non-indexed services available while failing closed if a service
		// assembled outside the constructor violates the indexed-search contract.
		if service.literalIndex != nil {
			return ErrExactTreeSearchAdmissionUnavailable
		}
		return nil
	}
	err := service.searchAdmission.Admit(ctx, ExactTreeSearchAdmissionRequest{
		ProjectID: input.ProjectID,
		ActorID:   input.ActorID,
		Operation: ExactTreeSearchAdmissionQuery,
	})
	if err == nil || errors.Is(err, ErrExactTreeSearchAdmissionUnavailable) {
		return err
	}
	var denial *ExactTreeSearchAdmissionDeniedError
	if errors.As(err, &denial) && denial != nil &&
		errors.Is(err, ErrExactTreeSearchAdmissionDenied) &&
		denial.Operation == ExactTreeSearchAdmissionQuery && denial.RetryAfter > 0 &&
		denial.RetryAfter <= maximumExactTreeSearchAdmissionRetry {
		return err
	}
	return errors.Join(ErrExactTreeSearchAdmissionUnavailable, err)
}

func (service *CandidateBootstrapService) candidateSearchFiles(
	ctx context.Context,
	input CandidateSearchInput,
	tree TreeManifest,
) ([]TreeFile, bool, bool, error) {
	if service.literalIndex == nil || len(input.IncludeGlobs) != 0 {
		return tree.Files, false, false, nil
	}
	query := ExactTreeLiteralIndexQuery{
		ProjectID: input.ProjectID, TreeHash: tree.TreeHash, Query: input.Query,
		CaseSensitive: input.CaseSensitive, MaxDocuments: input.MaxMatches,
	}
	indexed, err := service.literalIndex.QueryCandidateDocuments(ctx, query)
	if err == ErrExactTreeLiteralQueryTooShort {
		return tree.Files, false, false, nil
	}
	if err == ErrExactTreeLiteralIndexNotReady {
		if _, buildErr := service.literalIndex.BuildForActor(
			ctx, input.ProjectID, input.ActorID, tree,
		); buildErr != nil {
			return nil, false, false, fmt.Errorf(
				"build exact-tree Candidate search index: %w",
				candidateSearchIndexError("build", buildErr),
			)
		}
		indexed, err = service.literalIndex.QueryCandidateDocuments(ctx, query)
	}
	if err != nil {
		return nil, false, false, fmt.Errorf(
			"query exact-tree Candidate search index: %w",
			candidateSearchIndexError("query", err),
		)
	}
	if indexed.ProjectID != input.ProjectID || indexed.TreeHash != tree.TreeHash ||
		indexed.Query != input.Query || indexed.CaseSensitive != input.CaseSensitive {
		return nil, false, false, ErrExactTreeLiteralIndexContract
	}

	authoritative := make(map[string]TreeFile, len(tree.Files))
	for _, file := range tree.Files {
		authoritative[file.Path] = file
	}
	files := make([]TreeFile, len(indexed.Documents))
	for position, candidate := range indexed.Documents {
		file, exists := authoritative[candidate.Path]
		if !exists || file.Mode != candidate.Mode || file.ContentHash != candidate.ContentHash ||
			file.ByteSize != candidate.ByteSize ||
			(position > 0 && indexed.Documents[position-1].Path >= candidate.Path) {
			return nil, false, false, fmt.Errorf(
				"%w: index candidate %q is not an exact opening-tree member",
				ErrExactTreeLiteralIndexContract, candidate.Path,
			)
		}
		files[position] = file
	}
	return files, indexed.Truncated, true, nil
}

// candidateSearchIndexError is the fail-closed boundary between the optional
// derived index and the repository search API. Known domain and cancellation
// errors keep their identity; every other Build/Query failure is reduced to a
// stable unavailable classification so infrastructure details cannot be
// mistaken for an application error by transports.
func candidateSearchIndexError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidExactTreeLiteralIndex) ||
		errors.Is(err, ErrExactTreeLiteralIndexConflict) ||
		errors.Is(err, ErrExactTreeLiteralIndexContract) ||
		errors.Is(err, ErrExactTreeLiteralBuildClaimLost) ||
		errors.Is(err, ErrExactTreeLiteralClaimRelease) ||
		errors.Is(err, ErrExactTreeLiteralProjectTreeQuota) ||
		errors.Is(err, ErrExactTreeLiteralProjectSourceBytesQuota) ||
		errors.Is(err, ErrExactTreeLiteralProjectActiveBuildQuota) ||
		errors.Is(err, ErrExactTreeSearchAdmissionInvalid) ||
		errors.Is(err, ErrExactTreeSearchAdmissionUnavailable) ||
		validCandidateSearchIndexAdmissionDenial(operation, err) {
		return err
	}
	// Deliberately do not wrap the unknown cause: a nested application sentinel
	// from a broken index implementation must not override the 503 boundary.
	return fmt.Errorf("%w: %s failed: %v", ErrCandidateSearchIndexUnavailable, operation, err)
}

func validCandidateSearchIndexAdmissionDenial(operation string, err error) bool {
	if !errors.Is(err, ErrExactTreeSearchAdmissionDenied) ||
		errors.Is(err, ErrExactTreeSearchAdmissionUnavailable) ||
		errors.Is(err, ErrExactTreeSearchAdmissionInvalid) ||
		operation != "build" {
		return false
	}
	denial, valid := exactTreeSearchAdmissionDenialInSingleErrorChain(err)
	return valid && denial.Operation == ExactTreeSearchAdmissionFirstBuilder &&
		denial.RetryAfter > 0 && denial.RetryAfter <= maximumExactTreeSearchAdmissionRetry
}

func exactTreeSearchAdmissionDenialInSingleErrorChain(
	err error,
) (*ExactTreeSearchAdmissionDeniedError, bool) {
	for current := err; current != nil; {
		if _, joined := current.(interface{ Unwrap() []error }); joined {
			return nil, false
		}
		if denial, ok := current.(*ExactTreeSearchAdmissionDeniedError); ok {
			return denial, denial != nil
		}
		wrapped, ok := current.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		current = wrapped.Unwrap()
	}
	return nil, false
}

func (service *CandidateBootstrapService) loadSearchCandidate(
	ctx context.Context,
	projectID, candidateID string,
) (CandidateMutationRecord, error) {
	record, err := service.candidates.LoadMutationCandidate(ctx, projectID, candidateID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CandidateMutationRecord{}, ErrCandidateNotFound
	}
	if err != nil {
		return CandidateMutationRecord{}, fmt.Errorf("load repository Candidate for search: %w", err)
	}
	if err := record.Candidate.Validate(); err != nil ||
		record.Candidate.ProjectID != projectID || record.Candidate.ID != candidateID {
		return CandidateMutationRecord{}, fmt.Errorf("%w: Candidate search reader returned a different or invalid aggregate", ErrInvalidCandidate)
	}
	return record, nil
}

func normalizeCandidateSearchInput(input CandidateSearchInput) (CandidateSearchInput, error) {
	if input.ProjectID != strings.TrimSpace(input.ProjectID) ||
		input.CandidateID != strings.TrimSpace(input.CandidateID) ||
		input.ActorID != strings.TrimSpace(input.ActorID) ||
		!validUUID(input.ProjectID) || !validUUID(input.CandidateID) || !validUUID(input.ActorID) ||
		input.ExpectedHeadGeneration == 0 || !isCanonicalSHA256(input.ExpectedRootHash) ||
		input.Query == "" || len([]byte(input.Query)) > MaxCandidateSearchQueryBytes || !utf8.ValidString(input.Query) {
		return CandidateSearchInput{}, ErrInvalidCandidateSearch
	}
	for _, character := range input.Query {
		if unicode.IsControl(character) {
			return CandidateSearchInput{}, ErrInvalidCandidateSearch
		}
	}
	if !input.CaseSensitive && !isASCII(input.Query) {
		return CandidateSearchInput{}, fmt.Errorf("%w: case-insensitive query must be ASCII", ErrInvalidCandidateSearch)
	}
	if input.MaxMatches == 0 {
		input.MaxMatches = defaultCandidateSearchMatches
	}
	if input.MaxMatches < 1 || input.MaxMatches > MaxCandidateSearchMatches ||
		len(input.IncludeGlobs) > MaxCandidateSearchIncludeGlobs {
		return CandidateSearchInput{}, ErrInvalidCandidateSearch
	}
	input.IncludeGlobs = append([]string(nil), input.IncludeGlobs...)
	for _, pattern := range input.IncludeGlobs {
		if !validCandidateSearchGlob(pattern) {
			return CandidateSearchInput{}, ErrInvalidCandidateSearch
		}
	}
	return input, nil
}

func validCandidateSearchGlob(pattern string) bool {
	if pattern == "" || pattern != strings.TrimSpace(pattern) || len([]byte(pattern)) > MaxCandidateSearchGlobBytes ||
		!utf8.ValidString(pattern) || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, `\`) ||
		strings.ContainsRune(pattern, '\x00') {
		return false
	}
	for _, character := range pattern {
		if unicode.IsControl(character) {
			return false
		}
	}
	_, err := path.Match(pattern, "candidate-search-validation")
	return err == nil
}

func candidateSearchHeadMatches(candidate CandidateWorkspace, input CandidateSearchInput) bool {
	return candidate.ProjectID == input.ProjectID && candidate.ID == input.CandidateID &&
		candidate.Version == input.ExpectedHeadGeneration &&
		candidate.CurrentTree.TreeHash == input.ExpectedRootHash
}

func newCandidateSearchResult(input CandidateSearchInput, candidate CandidateWorkspace) CandidateSearchResult {
	return CandidateSearchResult{
		SchemaVersion: CandidateSearchSchemaVersion,
		ProjectID:     input.ProjectID,
		Head: CandidateSearchHead{
			CandidateID: candidate.ID, Generation: candidate.Version, RootHash: candidate.CurrentTree.TreeHash,
		},
		Query: input.Query, CaseSensitive: input.CaseSensitive,
		IncludeGlobs: append([]string(nil), input.IncludeGlobs...),
		Limits: CandidateSearchLimits{
			MaxQueryBytes: MaxCandidateSearchQueryBytes, MaxIncludeGlobs: MaxCandidateSearchIncludeGlobs,
			MaxGlobBytes: MaxCandidateSearchGlobBytes, MaxFiles: MaxCandidateSearchFiles,
			MaxBytes: MaxCandidateSearchBytes, MaxMatches: input.MaxMatches,
			MaxPreviewBytes: MaxCandidateSearchPreviewBytes,
		},
		Matches: make([]CandidateSearchMatch, 0),
	}
}

func candidateSearchPathIncluded(candidatePath string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		matched, _ := path.Match(pattern, candidatePath)
		if matched {
			return true
		}
	}
	return false
}

func isCandidateSearchText(value []byte) bool {
	if !utf8.Valid(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || (character < 0x20 && character != '\n' && character != '\r' && character != '\t' && character != '\f') ||
			character == 0x7f {
			return false
		}
	}
	return true
}

func isASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func foldSearchASCII(ctx context.Context, value []byte) ([]byte, error) {
	folded := append([]byte(nil), value...)
	for index, character := range folded {
		if index%searchCancellationStride == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if character >= 'A' && character <= 'Z' {
			folded[index] = character + ('a' - 'A')
		}
	}
	return folded, nil
}

type candidateSearchPositioner struct {
	value            []byte
	offset           int
	line             int
	column           int
	lineStart        int
	previewLineStart int
	previewLineEnd   int
	nextContextCheck int
}

func (positioner *candidateSearchPositioner) position(ctx context.Context, target int) (int, int, error) {
	for positioner.offset < target {
		if positioner.offset >= positioner.nextContextCheck {
			if err := ctx.Err(); err != nil {
				return 0, 0, err
			}
			positioner.nextContextCheck = positioner.offset + searchCancellationStride
		}
		character, size := utf8.DecodeRune(positioner.value[positioner.offset:])
		if size == 0 || character == utf8.RuneError && size == 1 {
			return 0, 0, ErrFileBlobIntegrity
		}
		positioner.offset += size
		if character == '\n' {
			positioner.line++
			positioner.column = 1
			positioner.lineStart = positioner.offset
			positioner.previewLineStart = -1
		} else {
			positioner.column++
		}
	}
	if positioner.offset != target {
		return 0, 0, ErrFileBlobIntegrity
	}
	return positioner.line, positioner.column, nil
}

func (positioner *candidateSearchPositioner) preview(matchOffset, matchBytes int) (string, bool) {
	if positioner.previewLineStart != positioner.lineStart {
		positioner.previewLineStart = positioner.lineStart
		positioner.previewLineEnd = len(positioner.value)
		if relative := bytes.IndexByte(positioner.value[positioner.lineStart:], '\n'); relative >= 0 {
			positioner.previewLineEnd = positioner.lineStart + relative
		}
	}
	return candidateSearchPreview(
		positioner.value,
		positioner.lineStart,
		positioner.previewLineEnd,
		matchOffset,
		matchBytes,
	)
}

func candidateSearchPreview(value []byte, lineStart, lineEnd, matchOffset, matchBytes int) (string, bool) {
	if lineEnd > lineStart && value[lineEnd-1] == '\r' {
		lineEnd--
	}
	if lineEnd-lineStart <= MaxCandidateSearchPreviewBytes {
		return string(value[lineStart:lineEnd]), false
	}

	availableContext := MaxCandidateSearchPreviewBytes - matchBytes
	if availableContext < 0 {
		availableContext = 0
	}
	start := matchOffset - availableContext/2
	if start < lineStart {
		start = lineStart
	}
	end := start + MaxCandidateSearchPreviewBytes
	if end < matchOffset+matchBytes {
		end = matchOffset + matchBytes
		start = end - MaxCandidateSearchPreviewBytes
	}
	if end > lineEnd {
		end = lineEnd
		start = end - MaxCandidateSearchPreviewBytes
		if start < lineStart {
			start = lineStart
		}
	}
	for start < end && !utf8.RuneStart(value[start]) {
		start++
	}
	for end > start && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return string(value[start:end]), true
}
