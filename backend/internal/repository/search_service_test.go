package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type candidateSearchAccessFake struct {
	viewCalls int
	editCalls int
	err       error
}

func (access *candidateSearchAccessFake) RequireProjectView(context.Context, string, string) error {
	access.viewCalls++
	return access.err
}

func (access *candidateSearchAccessFake) RequireProjectEdit(context.Context, string, string) error {
	access.editCalls++
	return access.err
}

type candidateSearchReaderFake struct {
	records []CandidateMutationRecord
	err     error
	calls   int
}

type candidateSearchAdmissionFake struct {
	requests []ExactTreeSearchAdmissionRequest
	err      error
}

func (admission *candidateSearchAdmissionFake) Admit(
	_ context.Context,
	request ExactTreeSearchAdmissionRequest,
) error {
	admission.requests = append(admission.requests, request)
	return admission.err
}

type candidateSearchTreeStoreFake struct {
	calls int
}

func (store *candidateSearchTreeStoreFake) Get(
	context.Context,
	string,
	string,
	TreeBlobPointer,
) (TreeManifest, error) {
	store.calls++
	return TreeManifest{}, errors.New("unexpected Candidate search tree read")
}

func (store *candidateSearchTreeStoreFake) PutPending(
	context.Context,
	string,
	string,
	TreeManifest,
) (TreeBlobPointer, error) {
	store.calls++
	return TreeBlobPointer{}, errors.New("unexpected Candidate search tree write")
}

func (store *candidateSearchTreeStoreFake) Finalize(
	context.Context,
	string,
	string,
	TreeBlobPointer,
) error {
	store.calls++
	return errors.New("unexpected Candidate search tree finalize")
}

func (store *candidateSearchTreeStoreFake) Abort(
	context.Context,
	string,
	string,
	TreeBlobPointer,
) error {
	store.calls++
	return errors.New("unexpected Candidate search tree abort")
}

func (reader *candidateSearchReaderFake) LoadMutationCandidate(
	context.Context,
	string,
	string,
) (CandidateMutationRecord, error) {
	reader.calls++
	if reader.err != nil {
		return CandidateMutationRecord{}, reader.err
	}
	if len(reader.records) == 0 {
		return CandidateMutationRecord{}, gorm.ErrRecordNotFound
	}
	index := reader.calls - 1
	if index >= len(reader.records) {
		index = len(reader.records) - 1
	}
	return reader.records[index], nil
}

func TestCandidateSearchAdmissionRunsAfterAuthorizationBeforeRepositoryIO(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	input := CandidateSearchInput{
		ProjectID: projectID, CandidateID: uuid.NewString(), ActorID: actorID,
		ExpectedHeadGeneration: 1, ExpectedRootHash: digestFixture("admission-root"),
		Query: "needle", CaseSensitive: true,
	}

	t.Run("invalid input reaches neither authorization nor admission", func(t *testing.T) {
		access := &candidateSearchAccessFake{}
		admission := &candidateSearchAdmissionFake{}
		reader := &candidateSearchReaderFake{}
		trees := &candidateSearchTreeStoreFake{}
		files := &candidateSearchFileReaderFake{}
		index := &candidateSearchLiteralIndexFake{}
		service := &CandidateBootstrapService{
			candidates: reader, trees: trees, files: files, access: access,
			literalIndex: index, searchAdmission: admission,
		}
		invalid := input
		invalid.Query = ""
		_, err := service.SearchCandidate(context.Background(), invalid)
		if !errors.Is(err, ErrInvalidCandidateSearch) || access.viewCalls != 0 ||
			len(admission.requests) != 0 || reader.calls != 0 || trees.calls != 0 ||
			files.calls != 0 || index.queryCalls != 0 || index.buildCalls != 0 {
			t.Fatalf(
				"invalid admission order err=%v view=%d admission=%d candidate=%d tree=%d blob=%d query=%d build=%d",
				err, access.viewCalls, len(admission.requests), reader.calls, trees.calls,
				files.calls, index.queryCalls, index.buildCalls,
			)
		}
	})

	t.Run("unauthorized does not consume admission", func(t *testing.T) {
		authorizationErr := errors.New("project view denied")
		access := &candidateSearchAccessFake{err: authorizationErr}
		admission := &candidateSearchAdmissionFake{}
		reader := &candidateSearchReaderFake{}
		trees := &candidateSearchTreeStoreFake{}
		files := &candidateSearchFileReaderFake{}
		index := &candidateSearchLiteralIndexFake{}
		service := &CandidateBootstrapService{
			candidates: reader, trees: trees, files: files, access: access,
			literalIndex: index, searchAdmission: admission,
		}
		_, err := service.SearchCandidate(context.Background(), input)
		if !errors.Is(err, authorizationErr) || access.viewCalls != 1 ||
			len(admission.requests) != 0 || reader.calls != 0 || trees.calls != 0 ||
			files.calls != 0 || index.queryCalls != 0 || index.buildCalls != 0 {
			t.Fatalf(
				"unauthorized admission order err=%v view=%d admission=%d candidate=%d tree=%d blob=%d query=%d build=%d",
				err, access.viewCalls, len(admission.requests), reader.calls, trees.calls,
				files.calls, index.queryCalls, index.buildCalls,
			)
		}
	})

	denial := &ExactTreeSearchAdmissionDeniedError{
		Operation: ExactTreeSearchAdmissionQuery, RetryAfter: time.Second,
	}
	maximumDenial := &ExactTreeSearchAdmissionDeniedError{
		Operation:  ExactTreeSearchAdmissionQuery,
		RetryAfter: maximumExactTreeSearchAdmissionRetry,
	}
	for _, test := range []struct {
		name            string
		admitErr        error
		exactErr        error
		wantCause       error
		wantUnavailable bool
	}{
		{name: "typed denial is preserved", admitErr: denial, exactErr: denial},
		{name: "maximum typed denial is preserved", admitErr: maximumDenial, exactErr: maximumDenial},
		{
			name: "infrastructure error is preserved", admitErr: ErrExactTreeSearchAdmissionUnavailable,
			exactErr: ErrExactTreeSearchAdmissionUnavailable, wantUnavailable: true,
		},
		{
			name: "other contract error fails closed", admitErr: ErrExactTreeSearchAdmissionInvalid,
			wantCause: ErrExactTreeSearchAdmissionInvalid, wantUnavailable: true,
		},
		{
			name: "bare denial sentinel fails closed", admitErr: ErrExactTreeSearchAdmissionDenied,
			wantCause: ErrExactTreeSearchAdmissionDenied, wantUnavailable: true,
		},
		{
			name: "wrong-operation denial fails closed",
			admitErr: &ExactTreeSearchAdmissionDeniedError{
				Operation: ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Second,
			},
			wantUnavailable: true,
		},
		{
			name: "zero-retry denial fails closed",
			admitErr: &ExactTreeSearchAdmissionDeniedError{
				Operation: ExactTreeSearchAdmissionQuery,
			},
			wantUnavailable: true,
		},
		{
			name: "negative-retry denial fails closed",
			admitErr: &ExactTreeSearchAdmissionDeniedError{
				Operation: ExactTreeSearchAdmissionQuery, RetryAfter: -time.Millisecond,
			},
			wantUnavailable: true,
		},
		{
			name: "oversized-retry denial fails closed",
			admitErr: &ExactTreeSearchAdmissionDeniedError{
				Operation:  ExactTreeSearchAdmissionQuery,
				RetryAfter: maximumExactTreeSearchAdmissionRetry + time.Millisecond,
			},
			wantUnavailable: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			access := &candidateSearchAccessFake{}
			admission := &candidateSearchAdmissionFake{err: test.admitErr}
			reader := &candidateSearchReaderFake{}
			trees := &candidateSearchTreeStoreFake{}
			files := &candidateSearchFileReaderFake{}
			index := &candidateSearchLiteralIndexFake{}
			service := &CandidateBootstrapService{
				candidates: reader, trees: trees, files: files, access: access,
				literalIndex: index, searchAdmission: admission,
			}
			_, err := service.SearchCandidate(context.Background(), input)
			if test.exactErr != nil && err != test.exactErr {
				t.Fatalf("admission error identity = %v, want exact %#v", err, test.exactErr)
			}
			if unavailable := errors.Is(err, ErrExactTreeSearchAdmissionUnavailable); unavailable != test.wantUnavailable {
				t.Fatalf("admission unavailable classification = %t, want %t: %v", unavailable, test.wantUnavailable, err)
			}
			if !test.wantUnavailable && !errors.Is(err, ErrExactTreeSearchAdmissionDenied) {
				t.Fatalf("valid typed denial classification was lost: %v", err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("admission contract cause was lost: %v", err)
			}
			if access.viewCalls != 1 || len(admission.requests) != 1 ||
				admission.requests[0] != (ExactTreeSearchAdmissionRequest{
					ProjectID: projectID, ActorID: actorID, Operation: ExactTreeSearchAdmissionQuery,
				}) || reader.calls != 0 || trees.calls != 0 || files.calls != 0 ||
				index.queryCalls != 0 || index.buildCalls != 0 {
				t.Fatalf(
					"admission boundary view=%d requests=%#v candidate=%d tree=%d blob=%d query=%d build=%d",
					access.viewCalls, admission.requests, reader.calls, trees.calls,
					files.calls, index.queryCalls, index.buildCalls,
				)
			}
		})
	}
}

func TestCandidateSearchScansExactBlobSnapshotDeterministically(t *testing.T) {
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	objects, err := NewFileStore(contents)
	if err != nil {
		t.Fatal(err)
	}
	catalog := newFakeFileBlobCatalog()
	fileIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	files, err := newFileBlobService(catalog, objects, time.Now, func() string {
		id := fileIDs[0]
		fileIDs = fileIDs[1:]
		return id
	})
	if err != nil {
		t.Fatal(err)
	}
	textValue := []byte("first\n雪 Needle and needle\n")
	ignoredValue := []byte("Needle outside include\n")
	binaryValue := []byte{'N', 'e', 'e', 'd', 'l', 'e', 0, 0xff}
	text, err := files.Put(ctx, projectID, actorID, textValue)
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := files.Put(ctx, projectID, actorID, ignoredValue)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := files.Put(ctx, projectID, actorID, binaryValue)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := NewTree([]TreeFile{
		{Path: "src/main.ts", Mode: "100644", ContentHash: text.Pointer.ContentHash, ByteSize: text.Pointer.ByteSize},
		{Path: "README.md", Mode: "100644", ContentHash: ignored.Pointer.ContentHash, ByteSize: ignored.Pointer.ByteSize},
		{Path: "src/raw.bin", Mode: "100644", ContentHash: binary.Pointer.ContentHash, ByteSize: binary.Pointer.ByteSize},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	reader := &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}}
	access := &candidateSearchAccessFake{}
	service := &CandidateBootstrapService{candidates: reader, files: files, access: access}

	result, err := service.SearchCandidate(ctx, CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "needle", CaseSensitive: false, IncludeGlobs: []string{"src/*"}, MaxMatches: 10,
	})
	if err != nil {
		t.Fatalf("search exact Candidate: %v", err)
	}
	if access.viewCalls != 1 || access.editCalls != 0 || reader.calls != 2 {
		t.Fatalf("search authorization/closing fence calls view=%d edit=%d loads=%d", access.viewCalls, access.editCalls, reader.calls)
	}
	if result.SchemaVersion != CandidateSearchSchemaVersion || result.ProjectID != projectID ||
		result.Head.CandidateID != candidate.ID || result.Head.Generation != candidate.Version ||
		result.Head.RootHash != tree.TreeHash || result.Truncated || result.Stats.FilesScanned != 2 ||
		result.Stats.BinaryFilesSkipped != 1 || result.Stats.BytesScanned != int64(len(textValue)+len(binaryValue)) ||
		len(result.Matches) != 2 {
		t.Fatalf("unexpected exact search result: %#v", result)
	}
	for index, match := range result.Matches {
		if match.Path != "src/main.ts" || match.Line != 2 || match.ContentHash != text.Pointer.ContentHash ||
			match.Preview != "雪 Needle and needle" || match.PreviewTruncated {
			t.Fatalf("match[%d] lost exact file/preview facts: %#v", index, match)
		}
	}
	if result.Matches[0].Column != 3 || result.Matches[1].Column != 14 {
		t.Fatalf("Unicode columns are not deterministic: %#v", result.Matches)
	}
	if result.Limits.MaxFiles != MaxCandidateSearchFiles || result.Limits.MaxBytes != MaxCandidateSearchBytes ||
		result.Limits.MaxMatches != 10 || result.Limits.MaxPreviewBytes != MaxCandidateSearchPreviewBytes {
		t.Fatalf("response omitted effective ceilings: %#v", result.Limits)
	}
}

func TestCandidateSearchBoundsPreviewMatchesAndRejectsBinaryDecode(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	prefix := strings.Repeat("雪", 180)
	value := []byte(prefix + " needle needle")
	file := TreeFile{Path: "long.txt", Mode: "100644", ContentHash: rawFileContentHash(value), ByteSize: int64(len(value))}
	tree, _ := NewTree([]TreeFile{file})
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	reader := &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}}
	files := &candidateSearchFileReaderFake{values: map[string][]byte{file.ContentHash: value}}
	service := &CandidateBootstrapService{candidates: reader, files: files, access: &candidateSearchAccessFake{}}

	result, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "needle", CaseSensitive: true, MaxMatches: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Matches) != 1 || !result.Matches[0].PreviewTruncated ||
		len([]byte(result.Matches[0].Preview)) > MaxCandidateSearchPreviewBytes ||
		!utf8.ValidString(result.Matches[0].Preview) || result.Matches[0].Column != 182 {
		t.Fatalf("preview/match ceiling failed: %#v", result)
	}
}

func TestCandidateSearchFailsClosedOnInitialAndConcurrentHeadDrift(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	value := []byte("exact")
	file := TreeFile{Path: "exact.txt", Mode: "100644", ContentHash: rawFileContentHash(value), ByteSize: int64(len(value))}
	tree, _ := NewTree([]TreeFile{file})
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	files := &candidateSearchFileReaderFake{values: map[string][]byte{file.ContentHash: value}}

	initialReader := &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}}
	service := &CandidateBootstrapService{candidates: initialReader, files: files, access: &candidateSearchAccessFake{}}
	_, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version + 1, ExpectedRootHash: tree.TreeHash,
		Query: "exact", CaseSensitive: true,
	})
	if !errors.Is(err, ErrCandidateSearchDrift) || files.calls != 0 || initialReader.calls != 1 {
		t.Fatalf("initial head drift error=%v blobCalls=%d loads=%d", err, files.calls, initialReader.calls)
	}

	drifted := cloneCandidate(candidate)
	drifted.Version++
	drifted.UpdatedAt = drifted.UpdatedAt.Add(time.Second)
	concurrentReader := &candidateSearchReaderFake{records: []CandidateMutationRecord{
		{Candidate: candidate}, {Candidate: drifted},
	}}
	service.candidates = concurrentReader
	_, err = service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "exact", CaseSensitive: true,
	})
	if !errors.Is(err, ErrCandidateSearchDrift) || concurrentReader.calls != 2 {
		t.Fatalf("concurrent head drift error=%v loads=%d", err, concurrentReader.calls)
	}
}

func TestCandidateSearchRejectsResolvedBlobDrift(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	value := []byte("exact")
	file := TreeFile{Path: "exact.txt", Mode: "100644", ContentHash: rawFileContentHash(value), ByteSize: int64(len(value))}
	tree, _ := NewTree([]TreeFile{file})
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	service := &CandidateBootstrapService{
		candidates: &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}},
		files:      &candidateSearchFileReaderFake{values: map[string][]byte{file.ContentHash: []byte("forge")}},
		access:     &candidateSearchAccessFake{},
	}
	_, err := service.SearchCandidate(context.Background(), CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "exact", CaseSensitive: true,
	})
	if !errors.Is(err, ErrFileBlobCatalogContract) {
		t.Fatalf("resolved blob drift error=%v", err)
	}
}

func TestCandidateSearchRejectsUnboundedOrAmbiguousInputAndHonorsCancellation(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	value := []byte("exact")
	file := TreeFile{Path: "exact.txt", Mode: "100644", ContentHash: rawFileContentHash(value), ByteSize: int64(len(value))}
	tree, _ := NewTree([]TreeFile{file})
	candidate := candidateSearchFixture(t, projectID, actorID, tree)
	base := CandidateSearchInput{
		ProjectID: projectID, CandidateID: candidate.ID, ActorID: actorID,
		ExpectedHeadGeneration: candidate.Version, ExpectedRootHash: tree.TreeHash,
		Query: "exact", CaseSensitive: true,
	}
	for name, mutate := range map[string]func(*CandidateSearchInput){
		"empty query":      func(input *CandidateSearchInput) { input.Query = "" },
		"control query":    func(input *CandidateSearchInput) { input.Query = "a\nb" },
		"oversized query":  func(input *CandidateSearchInput) { input.Query = strings.Repeat("a", MaxCandidateSearchQueryBytes+1) },
		"unicode fold":     func(input *CandidateSearchInput) { input.Query, input.CaseSensitive = "雪", false },
		"absolute glob":    func(input *CandidateSearchInput) { input.IncludeGlobs = []string{"/src/*"} },
		"malformed glob":   func(input *CandidateSearchInput) { input.IncludeGlobs = []string{"["} },
		"too many matches": func(input *CandidateSearchInput) { input.MaxMatches = MaxCandidateSearchMatches + 1 },
		"noncanonical hash": func(input *CandidateSearchInput) {
			input.ExpectedRootHash = strings.TrimPrefix(tree.TreeHash, "sha256:")
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			service := &CandidateBootstrapService{
				candidates: &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}},
				files:      &candidateSearchFileReaderFake{values: map[string][]byte{file.ContentHash: value}},
				access:     &candidateSearchAccessFake{},
			}
			if _, err := service.SearchCandidate(context.Background(), input); !errors.Is(err, ErrInvalidCandidateSearch) {
				t.Fatalf("error=%v, want invalid search", err)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	service := &CandidateBootstrapService{
		candidates: &candidateSearchReaderFake{records: []CandidateMutationRecord{{Candidate: candidate}}},
		files:      &candidateSearchFileReaderFake{values: map[string][]byte{file.ContentHash: value}},
		access:     &candidateSearchAccessFake{},
	}
	if _, err := service.SearchCandidate(cancelled, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search error=%v", err)
	}
}

type candidateSearchFileReaderFake struct {
	values map[string][]byte
	calls  int
}

func (reader *candidateSearchFileReaderFake) Put(
	context.Context,
	string,
	string,
	[]byte,
) (FileBlobWriteResult, error) {
	return FileBlobWriteResult{}, errors.New("not implemented by search reader")
}

func (reader *candidateSearchFileReaderFake) Resolve(
	_ context.Context,
	projectID, contentHash string,
	byteSize int64,
) (FileBlobPointer, []byte, error) {
	reader.calls++
	value, found := reader.values[contentHash]
	if !found {
		return FileBlobPointer{}, nil, ErrFileBlobNotFound
	}
	pointer := FileBlobPointer{
		Store: FileContentStore, Ref: "search-file-" + strings.TrimPrefix(contentHash, "sha256:"),
		OwnerID: uuid.NewString(), ContentHash: contentHash, ByteSize: byteSize,
		ContentObjectHash: rawFileContentHash(append([]byte("object:"), value...)),
	}
	_ = projectID
	return pointer, append([]byte(nil), value...), nil
}

func (reader *candidateSearchFileReaderFake) Settle(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) error {
	_, _, err := reader.Resolve(ctx, projectID, contentHash, byteSize)
	return err
}

func candidateSearchFixture(
	t *testing.T,
	projectID, actorID string,
	tree TreeManifest,
) CandidateWorkspace {
	t.Helper()
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	snapshot := RepositorySnapshot{
		ID: uuid.NewString(), ProjectID: projectID,
		BuildManifest:     ExactReference{ID: uuid.NewString(), ContentHash: digestFixture("search-manifest")},
		BuildContract:     ExactReference{ID: uuid.NewString(), ContentHash: digestFixture("search-contract")},
		FullStackTemplate: ExactReference{ID: uuid.NewString(), ContentHash: digestFixture("search-template")},
		Tree:              tree, CreatedBy: actorID, CreatedAt: now,
	}
	candidate, err := NewCandidate(uuid.NewString(), snapshot, actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}
