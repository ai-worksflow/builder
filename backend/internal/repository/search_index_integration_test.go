package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type candidateSearchLiteralIndexFake struct {
	results        []ExactTreeLiteralIndexQueryResult
	queryErrs      []error
	buildErr       error
	queryCalls     int
	buildCalls     int
	builtProjectID string
	builtActorID   string
	builtTree      TreeManifest
}

func (index *candidateSearchLiteralIndexFake) BuildForActor(
	_ context.Context,
	projectID string,
	actorID string,
	tree TreeManifest,
) (ExactTreeLiteralIndexManifest, error) {
	index.buildCalls++
	index.builtProjectID = projectID
	index.builtActorID = actorID
	index.builtTree = tree
	if index.buildErr != nil {
		return ExactTreeLiteralIndexManifest{}, index.buildErr
	}
	return ExactTreeLiteralIndexManifest{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		TreeHash:      tree.TreeHash,
	}, nil
}

func TestCandidateSearchIndexFailureBoundary(t *testing.T) {
	projectID := uuid.NewString()
	tree, err := NewTree(nil)
	if err != nil {
		t.Fatal(err)
	}
	input := CandidateSearchInput{
		ProjectID: projectID, ActorID: uuid.NewString(), Query: "needle",
		CaseSensitive: true, MaxMatches: 10,
	}
	infrastructureErr := errors.New("database connection disappeared")

	for _, fixture := range []struct {
		name  string
		index *candidateSearchLiteralIndexFake
	}{
		{
			name:  "initial query infrastructure failure",
			index: &candidateSearchLiteralIndexFake{queryErrs: []error{infrastructureErr}},
		},
		{
			name: "build infrastructure failure",
			index: &candidateSearchLiteralIndexFake{
				queryErrs: []error{ErrExactTreeLiteralIndexNotReady}, buildErr: infrastructureErr,
			},
		},
		{
			name: "second query infrastructure failure",
			index: &candidateSearchLiteralIndexFake{queryErrs: []error{
				ErrExactTreeLiteralIndexNotReady, infrastructureErr,
			}},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			service := &CandidateBootstrapService{literalIndex: fixture.index}
			_, _, _, searchErr := service.candidateSearchFiles(
				context.Background(), input, tree,
			)
			if !errors.Is(searchErr, ErrCandidateSearchIndexUnavailable) ||
				errors.Is(searchErr, infrastructureErr) {
				t.Fatalf("index infrastructure error was not safely classified: %v", searchErr)
			}
		})
	}
}

func TestCandidateSearchIndexFailureBoundaryPreservesDomainAndCancellation(t *testing.T) {
	builderDenial := &ExactTreeSearchAdmissionDeniedError{
		Operation: ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Second,
	}
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		ErrInvalidExactTreeLiteralIndex,
		ErrExactTreeLiteralIndexConflict,
		ErrExactTreeLiteralIndexContract,
		ErrExactTreeLiteralBuildClaimLost,
		ErrExactTreeLiteralClaimRelease,
		ErrExactTreeLiteralProjectTreeQuota,
		ErrExactTreeLiteralProjectSourceBytesQuota,
		ErrExactTreeLiteralProjectActiveBuildQuota,
		ErrExactTreeSearchAdmissionInvalid,
		ErrExactTreeSearchAdmissionUnavailable,
	} {
		if classified := candidateSearchIndexError("query", err); classified != err ||
			errors.Is(classified, ErrCandidateSearchIndexUnavailable) {
			t.Fatalf("domain error %v was reclassified as %v", err, classified)
		}
	}
	if classified := candidateSearchIndexError("build", builderDenial); classified != builderDenial ||
		errors.Is(classified, ErrCandidateSearchIndexUnavailable) {
		t.Fatalf("valid first-builder denial was reclassified: %v", classified)
	}

	for _, malformed := range []error{
		ErrExactTreeSearchAdmissionDenied,
		&ExactTreeSearchAdmissionDeniedError{
			Operation: ExactTreeSearchAdmissionQuery, RetryAfter: time.Second,
		},
		&ExactTreeSearchAdmissionDeniedError{
			Operation: ExactTreeSearchAdmissionQuery, RetryAfter: -time.Second,
		},
		&ExactTreeSearchAdmissionDeniedError{
			Operation: "other", RetryAfter: time.Second,
		},
	} {
		classified := candidateSearchIndexError("build", malformed)
		if !errors.Is(classified, ErrCandidateSearchIndexUnavailable) ||
			errors.Is(classified, ErrExactTreeSearchAdmissionDenied) {
			t.Fatalf("malformed denial was not reduced to unavailable: %v", classified)
		}
	}
	queryDenial := &ExactTreeSearchAdmissionDeniedError{
		Operation: ExactTreeSearchAdmissionQuery, RetryAfter: time.Second,
	}
	if classified := candidateSearchIndexError("query", queryDenial); !errors.Is(
		classified, ErrCandidateSearchIndexUnavailable,
	) || errors.Is(classified, ErrExactTreeSearchAdmissionDenied) {
		t.Fatalf("index query admission denial was not rejected: %v", classified)
	}
}

func (index *candidateSearchLiteralIndexFake) QueryCandidateDocuments(
	_ context.Context,
	_ ExactTreeLiteralIndexQuery,
) (ExactTreeLiteralIndexQueryResult, error) {
	position := index.queryCalls
	index.queryCalls++
	if position < len(index.queryErrs) && index.queryErrs[position] != nil {
		return ExactTreeLiteralIndexQueryResult{}, index.queryErrs[position]
	}
	if position < len(index.results) {
		return index.results[position], nil
	}
	if len(index.results) != 0 {
		return index.results[len(index.results)-1], nil
	}
	return ExactTreeLiteralIndexQueryResult{}, ErrExactTreeLiteralIndexContract
}

func indexedCandidateDocument(file TreeFile) ExactTreeLiteralCandidateDocument {
	return ExactTreeLiteralCandidateDocument{
		Path: file.Path, Mode: file.Mode, ContentHash: file.ContentHash, ByteSize: file.ByteSize,
	}
}

func indexedCandidateResult(
	projectID string,
	tree TreeManifest,
	query string,
	documents ...ExactTreeLiteralCandidateDocument,
) ExactTreeLiteralIndexQueryResult {
	return ExactTreeLiteralIndexQueryResult{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		ProjectID:     projectID, TreeHash: tree.TreeHash, Query: query, CaseSensitive: true,
		Documents: documents,
	}
}

func TestCandidateBootstrapConstructorRequiresAdmissionWithLiteralIndex(t *testing.T) {
	trees, err := NewTreeStore(newFakeTreeContentStore())
	if err != nil {
		t.Fatal(err)
	}
	newService := func(options ...CandidateBootstrapOption) (*CandidateBootstrapService, error) {
		return NewCandidateBootstrapService(
			&gorm.DB{}, bootstrapContentFake{}, &bootstrapFileWriterFake{}, trees,
			&candidateSearchReaderFake{}, &candidateSearchAccessFake{},
			&bootstrapContractGateFake{}, time.Now, options...,
		)
	}
	if service, err := newService(); err != nil || service.literalIndex != nil ||
		service.searchAdmission != nil {
		t.Fatalf("non-indexed constructor unexpectedly required admission: service=%#v err=%v", service, err)
	}
	index := &candidateSearchLiteralIndexFake{}
	if service, err := newService(WithCandidateSearchLiteralIndex(index)); err == nil || service != nil {
		t.Fatalf("literal index without admission was accepted: service=%#v err=%v", service, err)
	}
	admission := &candidateSearchAdmissionFake{}
	service, err := newService(
		WithCandidateSearchLiteralIndex(index),
		WithExactTreeSearchAdmission(admission),
	)
	if err != nil || service.literalIndex != index || service.searchAdmission != admission {
		t.Fatalf("indexed constructor lost admission: service=%#v err=%v", service, err)
	}
	if service, err := newService(WithExactTreeSearchAdmission(nil)); err == nil || service != nil {
		t.Fatalf("nil search admission option was accepted: service=%#v err=%v", service, err)
	}
}

func TestCandidateSearchUsesReadyIndexButRechecksExactTreeAndBlobBytes(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	matchValue := []byte("needle in exact indexed bytes\n")
	otherValue := []byte("nothing here\n")
	match := TreeFile{
		Path: "a.txt", Mode: "100644", ContentHash: rawFileContentHash(matchValue),
		ByteSize: int64(len(matchValue)),
	}
	other := TreeFile{
		Path: "b.txt", Mode: "100644", ContentHash: rawFileContentHash(otherValue),
		ByteSize: int64(len(otherValue)),
	}
	tree, err := NewTree([]TreeFile{other, match})
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	index := &candidateSearchLiteralIndexFake{results: []ExactTreeLiteralIndexQueryResult{
		indexedCandidateResult(projectID, tree, "needle", indexedCandidateDocument(match)),
	}}
	files := &candidateSearchFileReaderFake{values: map[string][]byte{
		match.ContentHash: matchValue, other.ContentHash: otherValue,
	}}
	admission := &candidateSearchAdmissionFake{}
	service := &CandidateBootstrapService{
		candidates: &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}},
		files:      files, access: &candidateSearchAccessFake{}, literalIndex: index,
		searchAdmission: admission,
	}
	result, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "needle", CaseSensitive: true, MaxMatches: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.requests) != 1 || index.queryCalls != 1 || index.buildCalls != 0 || files.calls != 1 ||
		result.Stats.FilesScanned != 1 || result.Stats.BytesScanned != int64(len(matchValue)) ||
		len(result.Matches) != 1 || result.Matches[0].Path != match.Path || result.Truncated {
		t.Fatalf("indexed search result=%#v query=%d build=%d blob=%d", result, index.queryCalls, index.buildCalls, files.calls)
	}
}

func TestCandidateSearchBuildsOnlyForPureMissingIndexAndQueriesAgain(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	value := []byte("indexed needle")
	file := TreeFile{
		Path: "exact.txt", Mode: "100644", ContentHash: rawFileContentHash(value),
		ByteSize: int64(len(value)),
	}
	tree, _ := NewTree([]TreeFile{file})
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	index := &candidateSearchLiteralIndexFake{
		queryErrs: []error{ErrExactTreeLiteralIndexNotReady, nil},
		results: []ExactTreeLiteralIndexQueryResult{
			{}, indexedCandidateResult(projectID, tree, "needle", indexedCandidateDocument(file)),
		},
	}
	admission := &candidateSearchAdmissionFake{}
	service := &CandidateBootstrapService{
		candidates: &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}},
		files:      &candidateSearchFileReaderFake{values: map[string][]byte{file.ContentHash: value}},
		access:     &candidateSearchAccessFake{}, literalIndex: index, searchAdmission: admission,
	}
	result, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "needle", CaseSensitive: true,
	})
	if err != nil || len(result.Matches) != 1 || len(admission.requests) != 1 ||
		index.queryCalls != 2 || index.buildCalls != 1 ||
		index.builtProjectID != projectID || index.builtActorID != actorID ||
		index.builtTree.TreeHash != tree.TreeHash {
		t.Fatalf("missing-index recovery result=%#v err=%v query=%d build=%d", result, err, index.queryCalls, index.buildCalls)
	}

	joined := &candidateSearchLiteralIndexFake{queryErrs: []error{
		errors.Join(ErrExactTreeLiteralIndexNotReady, ErrExactTreeLiteralIndexConflict),
	}}
	service.literalIndex = joined
	if _, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "needle", CaseSensitive: true,
	}); !errors.Is(err, ErrExactTreeLiteralIndexConflict) || joined.buildCalls != 0 ||
		len(admission.requests) != 2 {
		t.Fatalf("ambiguous not-ready error=%v build=%d", err, joined.buildCalls)
	}
}

func TestCandidateSearchIndexFallbackAndForeignCandidateBoundaries(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	value := []byte("ab needle")
	file := TreeFile{
		Path: "src/exact.txt", Mode: "100644", ContentHash: rawFileContentHash(value),
		ByteSize: int64(len(value)),
	}
	tree, _ := NewTree([]TreeFile{file})
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	reader := &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}}
	files := &candidateSearchFileReaderFake{values: map[string][]byte{file.ContentHash: value}}
	index := &candidateSearchLiteralIndexFake{queryErrs: []error{ErrExactTreeLiteralQueryTooShort}}
	admission := &candidateSearchAdmissionFake{}
	service := &CandidateBootstrapService{
		candidates: reader, files: files, access: &candidateSearchAccessFake{},
		literalIndex: index, searchAdmission: admission,
	}
	result, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "ab", CaseSensitive: true,
	})
	if err != nil || len(result.Matches) != 1 || len(admission.requests) != 1 ||
		index.queryCalls != 1 || index.buildCalls != 0 {
		t.Fatalf("short fallback result=%#v err=%v index=%#v", result, err, index)
	}

	index.queryCalls = 0
	result, err = service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "needle", CaseSensitive: true, IncludeGlobs: []string{"src/*"},
	})
	if err != nil || len(result.Matches) != 1 || len(admission.requests) != 2 ||
		index.queryCalls != 0 {
		t.Fatalf("glob fallback result=%#v err=%v query=%d", result, err, index.queryCalls)
	}

	foreign := indexedCandidateResult(projectID, tree, "needle", ExactTreeLiteralCandidateDocument{
		Path: "foreign.txt", Mode: "100644", ContentHash: file.ContentHash, ByteSize: file.ByteSize,
	})
	service.literalIndex = &candidateSearchLiteralIndexFake{results: []ExactTreeLiteralIndexQueryResult{foreign}}
	before := files.calls
	if _, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "needle", CaseSensitive: true,
	}); !errors.Is(err, ErrExactTreeLiteralIndexContract) || files.calls != before ||
		len(admission.requests) != 3 {
		t.Fatalf("foreign index candidate error=%v blob calls=%d->%d", err, before, files.calls)
	}
}
