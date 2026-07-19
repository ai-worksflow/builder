package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type exactTreeLiteralIndexResolverFake struct {
	mutex     sync.Mutex
	projectID string
	values    map[string][]byte
	pointer   func(string, []byte) FileBlobPointer
	err       error
	calls     int
}

type exactTreeLiteralIndexSlowResolver struct {
	base  *exactTreeLiteralIndexResolverFake
	delay time.Duration
}

func (resolver *exactTreeLiteralIndexSlowResolver) Resolve(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) (FileBlobPointer, []byte, error) {
	timer := time.NewTimer(resolver.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return FileBlobPointer{}, nil, ctx.Err()
	case <-timer.C:
		return resolver.base.Resolve(ctx, projectID, contentHash, byteSize)
	}
}

func (resolver *exactTreeLiteralIndexResolverFake) Resolve(
	_ context.Context,
	projectID, contentHash string,
	byteSize int64,
) (FileBlobPointer, []byte, error) {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	resolver.calls++
	if resolver.err != nil {
		return FileBlobPointer{}, nil, resolver.err
	}
	if resolver.projectID != "" && projectID != resolver.projectID {
		return FileBlobPointer{}, nil, ErrFileBlobNotFound
	}
	value, found := resolver.values[contentHash]
	if !found {
		return FileBlobPointer{}, nil, ErrFileBlobNotFound
	}
	pointer := exactTreeLiteralIndexPointer(contentHash, int64(len(value)))
	if resolver.pointer != nil {
		pointer = resolver.pointer(contentHash, value)
	}
	return pointer, append([]byte(nil), value...), nil
}

type exactTreeLiteralIndexStoreFake struct {
	mutex                sync.Mutex
	builds               []ExactTreeLiteralIndexBuild
	queryResult          ExactTreeLiteralIndexStoreQueryResult
	claimResult          *ExactTreeLiteralIndexBuildClaimResult
	claimResults         []ExactTreeLiteralIndexBuildClaimResult
	claimResultIndex     int
	acquireErr           error
	renewErr             error
	releaseErr           error
	staleRenewal         bool
	publishErr           error
	queryErr             error
	queryCalls           int
	acquireCalls         int
	claimRequests        []ExactTreeLiteralIndexBuildClaimRequest
	renewCalls           int
	releaseCalls         int
	releaseContextErrors []error
}

func (store *exactTreeLiteralIndexStoreFake) AcquireExactTreeLiteralIndexBuildClaim(
	_ context.Context,
	request ExactTreeLiteralIndexBuildClaimRequest,
) (ExactTreeLiteralIndexBuildClaimResult, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.acquireCalls++
	store.claimRequests = append(store.claimRequests, request)
	if store.acquireErr != nil {
		return ExactTreeLiteralIndexBuildClaimResult{}, store.acquireErr
	}
	if store.claimResultIndex < len(store.claimResults) {
		result := store.claimResults[store.claimResultIndex]
		store.claimResultIndex++
		return result, nil
	}
	if store.claimResult != nil {
		return *store.claimResult, nil
	}
	return ExactTreeLiteralIndexBuildClaimResult{
		Disposition: ExactTreeLiteralBuildClaimAcquired,
		Claim: ExactTreeLiteralIndexBuildClaim{
			ProjectID: request.ProjectID, TreeHash: request.TreeHash,
			OwnerToken: request.OwnerToken, Attempt: int64(store.acquireCalls),
			ReservedSourceBytes: request.SourceBytes,
			LeaseExpiresAt:      time.Now().UTC().Add(request.Lease),
		},
	}, nil
}

func (store *exactTreeLiteralIndexStoreFake) RenewExactTreeLiteralIndexBuildClaim(
	_ context.Context,
	claim ExactTreeLiteralIndexBuildClaim,
	lease time.Duration,
) (ExactTreeLiteralIndexBuildClaim, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.renewCalls++
	if store.renewErr != nil {
		return ExactTreeLiteralIndexBuildClaim{}, store.renewErr
	}
	if store.staleRenewal {
		return claim, nil
	}
	claim.LeaseExpiresAt = time.Now().UTC().Add(lease)
	return claim, nil
}

func (store *exactTreeLiteralIndexStoreFake) ReleaseExactTreeLiteralIndexBuildClaim(
	ctx context.Context,
	_ ExactTreeLiteralIndexBuildClaim,
) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.releaseCalls++
	store.releaseContextErrors = append(store.releaseContextErrors, ctx.Err())
	return store.releaseErr
}

type exactTreeLiteralIndexAdmissionFake struct {
	mutex    sync.Mutex
	requests []ExactTreeSearchAdmissionRequest
	err      error
	onAdmit  func()
}

func (admission *exactTreeLiteralIndexAdmissionFake) Admit(
	_ context.Context,
	request ExactTreeSearchAdmissionRequest,
) error {
	admission.mutex.Lock()
	admission.requests = append(admission.requests, request)
	onAdmit, err := admission.onAdmit, admission.err
	admission.mutex.Unlock()
	if onAdmit != nil {
		onAdmit()
	}
	return err
}

func (store *exactTreeLiteralIndexStoreFake) PublishExactTreeLiteralIndex(
	_ context.Context,
	build ExactTreeLiteralIndexBuild,
) (ExactTreeLiteralIndexManifest, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.publishErr != nil {
		return ExactTreeLiteralIndexManifest{}, store.publishErr
	}
	store.builds = append(store.builds, cloneExactTreeLiteralIndexBuild(build))
	return ExactTreeLiteralIndexManifest{
		SchemaVersion: build.SchemaVersion, ProjectID: build.ProjectID, TreeHash: build.TreeHash,
		FileCount: build.FileCount, TextFileCount: build.TextFileCount,
		SkippedFileCount: build.SkippedFileCount, TotalBytes: build.TotalBytes,
		TreeCommitment: build.TreeCommitment, IndexCommitment: build.IndexCommitment,
		ReadyAt: time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC),
		Reused:  len(store.builds) > 1,
	}, nil
}

func (store *exactTreeLiteralIndexStoreFake) QueryExactTreeLiteralIndex(
	_ context.Context,
	_ ExactTreeLiteralIndexStoreQuery,
) (ExactTreeLiteralIndexStoreQueryResult, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.queryCalls++
	return store.queryResult, store.queryErr
}

func TestExactTreeLiteralIndexBuildReverifiesEveryMemberAndClassifiesSkippedFiles(t *testing.T) {
	projectID := uuid.NewString()
	text := []byte("Needle and needle\n")
	binary := []byte{'N', 'e', 'e', 'd', 'l', 'e', 0, 0xff}
	textHash, binaryHash := rawFileContentHash(text), rawFileContentHash(binary)
	tree, err := NewTree([]TreeFile{
		{Path: "src/z-copy.ts", Mode: "100644", ContentHash: textHash, ByteSize: int64(len(text))},
		{Path: "src/a.ts", Mode: "100755", ContentHash: textHash, ByteSize: int64(len(text))},
		{Path: "assets/raw.bin", Mode: "100644", ContentHash: binaryHash, ByteSize: int64(len(binary))},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &exactTreeLiteralIndexResolverFake{
		projectID: projectID,
		values:    map[string][]byte{textHash: text, binaryHash: binary},
	}
	store := &exactTreeLiteralIndexStoreFake{}
	service, err := NewExactTreeLiteralIndexService(store, resolver)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Build(context.Background(), projectID, tree)
	if err != nil {
		t.Fatalf("build exact-tree literal index: %v", err)
	}
	second, err := service.Build(context.Background(), projectID, tree)
	if err != nil {
		t.Fatalf("retry exact-tree literal index: %v", err)
	}
	if first.Reused || !second.Reused || first.IndexCommitment != second.IndexCommitment ||
		first.TreeCommitment != second.TreeCommitment {
		t.Fatalf("idempotent manifest facts first=%#v second=%#v", first, second)
	}
	if resolver.calls != len(tree.Files)*2 || len(store.builds) != 2 {
		t.Fatalf("retry did not re-resolve every member: resolver=%d builds=%d", resolver.calls, len(store.builds))
	}
	build := store.builds[0]
	if build.FileCount != 3 || build.TextFileCount != 2 || build.SkippedFileCount != 1 ||
		build.TotalBytes != int64(2*len(text)+len(binary)) || len(build.Files) != 3 ||
		!isCanonicalSHA256(build.TreeCommitment) || !isCanonicalSHA256(build.IndexCommitment) {
		t.Fatalf("build omitted readiness facts: %#v", build)
	}
	if build.Files[0].Path != "assets/raw.bin" || build.Files[0].Text || build.Files[0].Body != nil {
		t.Fatalf("binary member was not recorded as skipped: %#v", build.Files[0])
	}
	for _, index := range []int{1, 2} {
		if !build.Files[index].Text || string(build.Files[index].Body) != string(text) {
			t.Fatalf("text member %d lost verified body: %#v", index, build.Files[index])
		}
	}
	if build.Files[1].Path != "src/a.ts" || build.Files[2].Path != "src/z-copy.ts" {
		t.Fatalf("publication lost canonical path order: %#v", build.Files)
	}
}

func TestExactTreeLiteralIndexBuildReservesCanonicalBytesWithDefaultAndConfiguredProjectQuota(t *testing.T) {
	projectID := uuid.NewString()
	body := []byte("quota reservation source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/quota.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name  string
		quota ExactTreeLiteralIndexProjectQuota
		want  ExactTreeLiteralIndexProjectQuota
	}{
		{
			name: "defaults",
			want: ExactTreeLiteralIndexProjectQuota{
				MaxTrees:        DefaultExactTreeLiteralIndexProjectTrees,
				MaxSourceBytes:  DefaultExactTreeLiteralIndexProjectSourceBytes,
				MaxActiveBuilds: DefaultExactTreeLiteralIndexProjectActiveBuilds,
			},
		},
		{
			name: "configured",
			quota: ExactTreeLiteralIndexProjectQuota{
				MaxTrees: 3, MaxSourceBytes: 96 << 20, MaxActiveBuilds: 1,
			},
			want: ExactTreeLiteralIndexProjectQuota{
				MaxTrees: 3, MaxSourceBytes: 96 << 20, MaxActiveBuilds: 1,
			},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store := &exactTreeLiteralIndexStoreFake{}
			resolver := &exactTreeLiteralIndexResolverFake{
				projectID: projectID, values: map[string][]byte{hash: body},
			}
			var service *ExactTreeLiteralIndexService
			var serviceErr error
			if fixture.quota == (ExactTreeLiteralIndexProjectQuota{}) {
				service, serviceErr = NewExactTreeLiteralIndexService(store, resolver)
			} else {
				service, serviceErr = NewExactTreeLiteralIndexService(store, resolver, fixture.quota)
			}
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			if _, buildErr := service.Build(context.Background(), projectID, tree); buildErr != nil {
				t.Fatal(buildErr)
			}
			if len(store.claimRequests) != 1 {
				t.Fatalf("claim requests=%d", len(store.claimRequests))
			}
			request := store.claimRequests[0]
			if request.SourceBytes != int64(len(body)) || request.MaxProjectTrees != fixture.want.MaxTrees ||
				request.MaxProjectSourceBytes != fixture.want.MaxSourceBytes ||
				request.MaxProjectActiveBuilds != fixture.want.MaxActiveBuilds {
				t.Fatalf("quota claim request=%#v want=%#v", request, fixture.want)
			}
		})
	}
}

func TestExactTreeLiteralIndexProjectQuotaFailuresAreDistinctAndPrecedeBlobResolution(t *testing.T) {
	projectID := uuid.NewString()
	body := []byte("quota rejection source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/rejected.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, quotaErr := range []error{
		ErrExactTreeLiteralProjectTreeQuota,
		ErrExactTreeLiteralProjectSourceBytesQuota,
		ErrExactTreeLiteralProjectActiveBuildQuota,
	} {
		t.Run(quotaErr.Error(), func(t *testing.T) {
			store := &exactTreeLiteralIndexStoreFake{acquireErr: quotaErr}
			resolver := &exactTreeLiteralIndexResolverFake{
				projectID: projectID, values: map[string][]byte{hash: body},
			}
			service, serviceErr := NewExactTreeLiteralIndexService(store, resolver)
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			_, buildErr := service.Build(context.Background(), projectID, tree)
			if !errors.Is(buildErr, quotaErr) || resolver.calls != 0 ||
				len(store.builds) != 0 || store.releaseCalls != 0 {
				t.Fatalf("quota error=%v resolve=%d builds=%d release=%d",
					buildErr, resolver.calls, len(store.builds), store.releaseCalls)
			}
		})
	}

	store := &exactTreeLiteralIndexStoreFake{}
	resolver := &exactTreeLiteralIndexResolverFake{}
	for _, quota := range []ExactTreeLiteralIndexProjectQuota{
		{MaxTrees: -1},
		{MaxTrees: 1, MaxSourceBytes: -1, MaxActiveBuilds: 1},
		{MaxTrees: 1, MaxSourceBytes: 1, MaxActiveBuilds: 2},
	} {
		if _, serviceErr := NewExactTreeLiteralIndexService(store, resolver, quota); !errors.Is(serviceErr, ErrInvalidExactTreeLiteralIndex) {
			t.Fatalf("invalid quota %#v error=%v", quota, serviceErr)
		}
	}
}

func TestExactTreeLiteralIndexFirstBuilderAdmissionChargesOnlyAcquiredOwner(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	body := []byte("first builder source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/owner.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := &exactTreeLiteralIndexStoreFake{}
	resolver := &exactTreeLiteralIndexResolverFake{
		projectID: projectID, values: map[string][]byte{hash: body},
	}
	admission := &exactTreeLiteralIndexAdmissionFake{}
	admission.onAdmit = func() {
		resolver.mutex.Lock()
		defer resolver.mutex.Unlock()
		if resolver.calls != 0 {
			t.Errorf("blob resolution preceded first-builder admission: calls=%d", resolver.calls)
		}
	}
	service, err := NewAdmittedExactTreeLiteralIndexService(
		store,
		resolver,
		ExactTreeLiteralIndexAdmissionConfig{FirstBuilderAdmission: admission},
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := service.BuildForActor(context.Background(), projectID, actorID, tree)
	if err != nil {
		t.Fatalf("admitted first-builder build: %v", err)
	}
	wantRequest := ExactTreeSearchAdmissionRequest{
		ProjectID: projectID, ActorID: actorID, Operation: ExactTreeSearchAdmissionFirstBuilder,
	}
	if manifest.TreeHash != tree.TreeHash || len(admission.requests) != 1 ||
		admission.requests[0] != wantRequest || resolver.calls != 1 || len(store.builds) != 1 ||
		store.releaseCalls != 1 {
		t.Fatalf("manifest=%#v admission=%#v resolve=%d builds=%d release=%d",
			manifest, admission.requests, resolver.calls, len(store.builds), store.releaseCalls)
	}
}

func TestExactTreeLiteralIndexReadyAndWaitingCallersDoNotConsumeAdmission(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	body := []byte("ready index source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/ready.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	fixtureStore := &exactTreeLiteralIndexStoreFake{}
	fixtureService, err := NewExactTreeLiteralIndexService(
		fixtureStore,
		&exactTreeLiteralIndexResolverFake{
			projectID: projectID, values: map[string][]byte{hash: body},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := fixtureService.Build(context.Background(), projectID, tree)
	if err != nil {
		t.Fatal(err)
	}

	for _, fixture := range []struct {
		name         string
		claimResults []ExactTreeLiteralIndexBuildClaimResult
		wantAcquire  int
	}{
		{
			name: "ready",
			claimResults: []ExactTreeLiteralIndexBuildClaimResult{{
				Disposition: ExactTreeLiteralBuildClaimReady, Manifest: ready,
			}},
			wantAcquire: 1,
		},
		{
			name: "waiting then ready",
			claimResults: []ExactTreeLiteralIndexBuildClaimResult{
				{Disposition: ExactTreeLiteralBuildClaimWaiting},
				{Disposition: ExactTreeLiteralBuildClaimReady, Manifest: ready},
			},
			wantAcquire: 2,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store := &exactTreeLiteralIndexStoreFake{claimResults: fixture.claimResults}
			resolver := &exactTreeLiteralIndexResolverFake{
				projectID: projectID, values: map[string][]byte{hash: body},
			}
			admission := &exactTreeLiteralIndexAdmissionFake{}
			service, serviceErr := NewAdmittedExactTreeLiteralIndexService(
				store,
				resolver,
				ExactTreeLiteralIndexAdmissionConfig{FirstBuilderAdmission: admission},
			)
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			service.claimPoll = time.Millisecond
			manifest, buildErr := service.BuildForActor(
				context.Background(), projectID, actorID, tree,
			)
			if buildErr != nil || !manifest.Reused || manifest.TreeHash != tree.TreeHash ||
				len(admission.requests) != 0 || resolver.calls != 0 || len(store.builds) != 0 ||
				store.acquireCalls != fixture.wantAcquire || store.releaseCalls != 0 {
				t.Fatalf("manifest=%#v err=%v admission=%d resolve=%d builds=%d acquire=%d release=%d",
					manifest, buildErr, len(admission.requests), resolver.calls, len(store.builds),
					store.acquireCalls, store.releaseCalls)
			}
		})
	}
}

func TestExactTreeLiteralIndexAdmissionDenialAndOutageReleaseBeforeResolution(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	body := []byte("admission rejection source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/rejected.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	denial := &ExactTreeSearchAdmissionDeniedError{
		Operation: ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Second,
	}
	outage := errors.Join(
		ErrExactTreeSearchAdmissionUnavailable,
		errors.New("redis connection unavailable"),
	)
	for _, fixture := range []struct {
		name string
		err  error
	}{
		{name: "typed denial", err: denial},
		{name: "outage", err: outage},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			store := &exactTreeLiteralIndexStoreFake{}
			resolver := &exactTreeLiteralIndexResolverFake{
				projectID: projectID, values: map[string][]byte{hash: body},
			}
			admission := &exactTreeLiteralIndexAdmissionFake{err: fixture.err, onAdmit: cancel}
			service, serviceErr := NewAdmittedExactTreeLiteralIndexService(
				store,
				resolver,
				ExactTreeLiteralIndexAdmissionConfig{FirstBuilderAdmission: admission},
			)
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			_, buildErr := service.BuildForActor(ctx, projectID, actorID, tree)
			if buildErr != fixture.err || resolver.calls != 0 || len(store.builds) != 0 ||
				store.acquireCalls != 1 || store.releaseCalls != 1 ||
				len(store.releaseContextErrors) != 1 || store.releaseContextErrors[0] != nil {
				t.Fatalf("err=%v want identity=%v resolve=%d builds=%d acquire=%d release=%d releaseCtx=%#v",
					buildErr, fixture.err, resolver.calls, len(store.builds), store.acquireCalls,
					store.releaseCalls, store.releaseContextErrors)
			}
		})
	}
}

func TestExactTreeLiteralIndexAdmissionReleaseFailureIsJoinedWithoutResolution(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	body := []byte("admission release failure source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/release.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	releaseFailure := errors.New("release storage unavailable")
	denial := &ExactTreeSearchAdmissionDeniedError{
		Operation: ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Minute,
	}
	store := &exactTreeLiteralIndexStoreFake{releaseErr: releaseFailure}
	resolver := &exactTreeLiteralIndexResolverFake{
		projectID: projectID, values: map[string][]byte{hash: body},
	}
	service, err := NewAdmittedExactTreeLiteralIndexService(
		store,
		resolver,
		ExactTreeLiteralIndexAdmissionConfig{
			FirstBuilderAdmission: &exactTreeLiteralIndexAdmissionFake{err: denial},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.BuildForActor(context.Background(), projectID, actorID, tree)
	if !errors.Is(err, ErrExactTreeSearchAdmissionDenied) ||
		!errors.Is(err, ErrExactTreeLiteralClaimRelease) || !errors.Is(err, releaseFailure) ||
		resolver.calls != 0 || len(store.builds) != 0 || store.releaseCalls != 1 {
		t.Fatalf("err=%v resolve=%d builds=%d release=%d",
			err, resolver.calls, len(store.builds), store.releaseCalls)
	}
}

func TestExactTreeLiteralIndexMalformedAdmissionDenialsBecomeUnavailable(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	body := []byte("malformed denial source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/malformed.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name string
		err  error
	}{
		{name: "bare sentinel", err: ErrExactTreeSearchAdmissionDenied},
		{name: "wrong operation", err: &ExactTreeSearchAdmissionDeniedError{
			Operation: ExactTreeSearchAdmissionQuery, RetryAfter: time.Second,
		}},
		{name: "zero retry", err: &ExactTreeSearchAdmissionDeniedError{
			Operation: ExactTreeSearchAdmissionFirstBuilder,
		}},
		{name: "retry over one hour", err: &ExactTreeSearchAdmissionDeniedError{
			Operation: ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Hour + time.Nanosecond,
		}},
		{name: "mixed denial and unavailable", err: errors.Join(
			&ExactTreeSearchAdmissionDeniedError{
				Operation: ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Second,
			},
			ErrExactTreeSearchAdmissionUnavailable,
		)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store := &exactTreeLiteralIndexStoreFake{}
			resolver := &exactTreeLiteralIndexResolverFake{
				projectID: projectID, values: map[string][]byte{hash: body},
			}
			service, serviceErr := NewAdmittedExactTreeLiteralIndexService(
				store,
				resolver,
				ExactTreeLiteralIndexAdmissionConfig{
					FirstBuilderAdmission: &exactTreeLiteralIndexAdmissionFake{err: fixture.err},
				},
			)
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			_, buildErr := service.BuildForActor(
				context.Background(), projectID, actorID, tree,
			)
			if buildErr != ErrExactTreeSearchAdmissionUnavailable ||
				errors.Is(buildErr, ErrExactTreeSearchAdmissionDenied) || resolver.calls != 0 ||
				len(store.builds) != 0 || store.releaseCalls != 1 {
				t.Fatalf("err=%v denied=%v resolve=%d builds=%d release=%d",
					buildErr, errors.Is(buildErr, ErrExactTreeSearchAdmissionDenied),
					resolver.calls, len(store.builds), store.releaseCalls)
			}
		})
	}
}

func TestExactTreeLiteralIndexAdmittedConstructionRejectsInvalidActorAndLegacyBypass(t *testing.T) {
	projectID, actorID := uuid.NewString(), uuid.NewString()
	body := []byte("admitted boundary source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/boundary.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := &exactTreeLiteralIndexStoreFake{}
	resolver := &exactTreeLiteralIndexResolverFake{
		projectID: projectID, values: map[string][]byte{hash: body},
	}
	admission := &exactTreeLiteralIndexAdmissionFake{}
	service, err := NewAdmittedExactTreeLiteralIndexService(
		store,
		resolver,
		ExactTreeLiteralIndexAdmissionConfig{FirstBuilderAdmission: admission},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalidActorID := range []string{"", "not-a-uuid", actorID + " "} {
		_, buildErr := service.BuildForActor(
			context.Background(), projectID, invalidActorID, tree,
		)
		if !errors.Is(buildErr, ErrExactTreeSearchAdmissionInvalid) {
			t.Fatalf("invalid actor %q error=%v", invalidActorID, buildErr)
		}
	}
	if _, buildErr := service.Build(context.Background(), projectID, tree); !errors.Is(
		buildErr, ErrExactTreeSearchAdmissionInvalid,
	) {
		t.Fatalf("configured admission allowed actor-less Build: %v", buildErr)
	}
	if len(admission.requests) != 0 || store.acquireCalls != 0 || resolver.calls != 0 {
		t.Fatalf("invalid boundary reached work: admission=%d acquire=%d resolve=%d",
			len(admission.requests), store.acquireCalls, resolver.calls)
	}

	if _, constructorErr := NewAdmittedExactTreeLiteralIndexService(
		store,
		resolver,
		ExactTreeLiteralIndexAdmissionConfig{},
	); !errors.Is(constructorErr, ErrExactTreeSearchAdmissionInvalid) {
		t.Fatalf("nil admission constructor error=%v", constructorErr)
	}
	legacy, err := NewExactTreeLiteralIndexService(store, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, buildErr := legacy.BuildForActor(
		context.Background(), projectID, actorID, tree,
	); !errors.Is(buildErr, ErrExactTreeSearchAdmissionUnavailable) {
		t.Fatalf("legacy service exposed actor build without admission: %v", buildErr)
	}
}

func TestExactTreeLiteralIndexBuildFailsClosedOnPointerBytesAndTenantDrift(t *testing.T) {
	projectID := uuid.NewString()
	value := []byte("authoritative source")
	hash := rawFileContentHash(value)
	tree, err := NewTree([]TreeFile{{
		Path: "src/main.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(value)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for name, resolver := range map[string]*exactTreeLiteralIndexResolverFake{
		"wrong bytes": {
			values: map[string][]byte{hash: []byte("tampered source byte")},
		},
		"wrong pointer": {
			values: map[string][]byte{hash: value},
			pointer: func(contentHash string, body []byte) FileBlobPointer {
				pointer := exactTreeLiteralIndexPointer(contentHash, int64(len(body)))
				pointer.ContentHash = rawFileContentHash([]byte("foreign"))
				return pointer
			},
		},
		"foreign tenant": {
			projectID: uuid.NewString(), values: map[string][]byte{hash: value},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &exactTreeLiteralIndexStoreFake{}
			service, serviceErr := NewExactTreeLiteralIndexService(store, resolver)
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			_, buildErr := service.Build(context.Background(), projectID, tree)
			if buildErr == nil || len(store.builds) != 0 {
				t.Fatalf("drift reached publication: err=%v builds=%d", buildErr, len(store.builds))
			}
		})
	}
}

func TestExactTreeLiteralIndexQueryIsBoundedAndReturnsCandidateMetadataOnly(t *testing.T) {
	projectID := uuid.NewString()
	treeHash := digestFixture("exact-tree-literal-query-tree")
	readyAt := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	store := &exactTreeLiteralIndexStoreFake{queryResult: ExactTreeLiteralIndexStoreQueryResult{
		Manifest: ExactTreeLiteralIndexManifest{
			SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
			ProjectID:     projectID, TreeHash: treeHash,
			IndexCommitment: digestFixture("exact-tree-literal-query-index"), ReadyAt: readyAt,
		},
		Documents: []ExactTreeLiteralCandidateDocument{
			{Path: "a.ts", Mode: "100644", ContentHash: digestFixture("query-a"), ByteSize: 4 << 20},
			{Path: "b.ts", Mode: "100644", ContentHash: digestFixture("query-b"), ByteSize: 4 << 20},
			{Path: "c.ts", Mode: "100644", ContentHash: digestFixture("query-c"), ByteSize: 1},
		},
	}}
	service, err := NewExactTreeLiteralIndexService(store, &exactTreeLiteralIndexResolverFake{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.QueryCandidateDocuments(context.Background(), ExactTreeLiteralIndexQuery{
		ProjectID: projectID, TreeHash: treeHash, Query: "Needle", CaseSensitive: true, MaxDocuments: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Documents) != 2 || result.CandidateBytes != 8<<20 ||
		result.Documents[0].Path != "a.ts" || result.Documents[1].Path != "b.ts" ||
		result.Limits.MaxDocuments != 3 || result.IndexCommitment != store.queryResult.Manifest.IndexCommitment {
		t.Fatalf("bounded candidate result = %#v", result)
	}
	if store.queryCalls != 1 {
		t.Fatalf("query calls=%d, want 1", store.queryCalls)
	}
}

func TestExactTreeLiteralIndexQueryRejectsShortUnicodeFoldAndUnsortedStoreOutput(t *testing.T) {
	projectID := uuid.NewString()
	treeHash := digestFixture("exact-tree-literal-validation-tree")
	store := &exactTreeLiteralIndexStoreFake{}
	service, err := NewExactTreeLiteralIndexService(store, &exactTreeLiteralIndexResolverFake{})
	if err != nil {
		t.Fatal(err)
	}
	base := ExactTreeLiteralIndexQuery{
		ProjectID: projectID, TreeHash: treeHash, Query: "needle", CaseSensitive: true,
	}
	for name, mutate := range map[string]func(*ExactTreeLiteralIndexQuery){
		"one rune":        func(input *ExactTreeLiteralIndexQuery) { input.Query = "雪" },
		"two runes":       func(input *ExactTreeLiteralIndexQuery) { input.Query = "雪山" },
		"no word trigram": func(input *ExactTreeLiteralIndexQuery) { input.Query = "!_%" },
		"unicode insensitive": func(input *ExactTreeLiteralIndexQuery) {
			input.Query, input.CaseSensitive = "雪山针", false
		},
		"unbounded documents": func(input *ExactTreeLiteralIndexQuery) {
			input.MaxDocuments = MaxExactTreeLiteralCandidateDocuments + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			_, queryErr := service.QueryCandidateDocuments(context.Background(), input)
			if queryErr == nil {
				t.Fatal("invalid query was accepted")
			}
		})
	}
	if store.queryCalls != 0 {
		t.Fatalf("invalid queries reached store %d times", store.queryCalls)
	}

	store.queryResult = ExactTreeLiteralIndexStoreQueryResult{
		Manifest: ExactTreeLiteralIndexManifest{
			SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
			ProjectID:     projectID, TreeHash: treeHash,
			IndexCommitment: digestFixture("validation-index"), ReadyAt: time.Now().UTC(),
		},
		Documents: []ExactTreeLiteralCandidateDocument{
			{Path: "z.ts", Mode: "100644", ContentHash: digestFixture("z"), ByteSize: 1},
			{Path: "a.ts", Mode: "100644", ContentHash: digestFixture("a"), ByteSize: 1},
		},
	}
	_, err = service.QueryCandidateDocuments(context.Background(), base)
	if !errors.Is(err, ErrExactTreeLiteralIndexContract) {
		t.Fatalf("unsorted store output error=%v", err)
	}
}

func TestExactTreeLiteralIndexReadyMetadataRecomputesCommitmentsBeyondAggregates(t *testing.T) {
	projectID := uuid.NewString()
	firstBody := []byte("first body")
	secondBody := []byte("other body")
	tree, err := NewTree([]TreeFile{
		{Path: "a.ts", Mode: "100644", ContentHash: rawFileContentHash(firstBody), ByteSize: int64(len(firstBody))},
		{Path: "b.ts", Mode: "100644", ContentHash: rawFileContentHash(secondBody), ByteSize: int64(len(secondBody))},
	})
	if err != nil {
		t.Fatal(err)
	}
	build := ExactTreeLiteralIndexBuild{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		ProjectID:     projectID, TreeHash: tree.TreeHash,
		FileCount: 2, TextFileCount: 1, SkippedFileCount: 1,
		TotalBytes: int64(len(firstBody) + len(secondBody)),
		Files: []ExactTreeLiteralIndexBuildFile{
			{Path: tree.Files[0].Path, Mode: tree.Files[0].Mode, ContentHash: tree.Files[0].ContentHash, ByteSize: tree.Files[0].ByteSize, Text: true},
			{Path: tree.Files[1].Path, Mode: tree.Files[1].Mode, ContentHash: tree.Files[1].ContentHash, ByteSize: tree.Files[1].ByteSize, Text: false},
		},
	}
	build.TreeCommitment, build.IndexCommitment, err = exactTreeLiteralIndexCommitments(build)
	if err != nil {
		t.Fatal(err)
	}
	manifest := ExactTreeLiteralIndexManifest{
		SchemaVersion: build.SchemaVersion, ProjectID: build.ProjectID, TreeHash: build.TreeHash,
		FileCount: build.FileCount, TextFileCount: build.TextFileCount,
		SkippedFileCount: build.SkippedFileCount, TotalBytes: build.TotalBytes,
		TreeCommitment: build.TreeCommitment, IndexCommitment: build.IndexCommitment,
		ReadyAt: time.Now().UTC(),
	}
	projectUUID := uuid.MustParse(projectID)
	members := []exactTreeLiteralIndexMemberRow{
		{ProjectID: projectUUID, TreeHash: tree.TreeHash, Path: tree.Files[0].Path, Mode: tree.Files[0].Mode, ContentHash: tree.Files[0].ContentHash, ByteSize: tree.Files[0].ByteSize, Indexed: true},
		{ProjectID: projectUUID, TreeHash: tree.TreeHash, Path: tree.Files[1].Path, Mode: tree.Files[1].Mode, ContentHash: tree.Files[1].ContentHash, ByteSize: tree.Files[1].ByteSize, Indexed: false},
	}
	if err := verifyExactTreeLiteralIndexMemberCommitment(manifest, members); err != nil {
		t.Fatalf("valid ready member commitment: %v", err)
	}
	// Preserve file/text counts and total bytes while swapping classifications.
	// An aggregate-only readiness check would accept this equal-count tamper.
	members[0].Indexed, members[1].Indexed = false, true
	if err := verifyExactTreeLiteralIndexMemberCommitment(manifest, members); !errors.Is(err, ErrExactTreeLiteralIndexConflict) {
		t.Fatalf("equal-count classification swap error=%v", err)
	}
}

func TestExactTreeLiteralIndexBuildRenewsClaimAndFailsClosedOnLossOrReleaseFailure(t *testing.T) {
	projectID := uuid.NewString()
	body := []byte("heartbeat source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/heartbeat.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("renewal", func(t *testing.T) {
		store := &exactTreeLiteralIndexStoreFake{}
		base := &exactTreeLiteralIndexResolverFake{
			projectID: projectID, values: map[string][]byte{hash: body},
		}
		service, serviceErr := NewExactTreeLiteralIndexService(
			store, &exactTreeLiteralIndexSlowResolver{base: base, delay: 110 * time.Millisecond},
		)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		service.claimLease = 150 * time.Millisecond
		service.claimHeartbeat = 25 * time.Millisecond
		service.claimPoll = 5 * time.Millisecond
		manifest, buildErr := service.Build(context.Background(), projectID, tree)
		if buildErr != nil || manifest.TreeHash != tree.TreeHash {
			t.Fatalf("renewed build manifest=%#v err=%v", manifest, buildErr)
		}
		if store.renewCalls < 3 || store.releaseCalls != 1 || len(store.builds) != 1 || base.calls != 1 {
			t.Fatalf("renewed calls renew=%d release=%d builds=%d resolve=%d",
				store.renewCalls, store.releaseCalls, len(store.builds), base.calls)
		}
	})

	t.Run("claim lost", func(t *testing.T) {
		store := &exactTreeLiteralIndexStoreFake{renewErr: errors.New("owner fenced")}
		base := &exactTreeLiteralIndexResolverFake{
			projectID: projectID, values: map[string][]byte{hash: body},
		}
		service, serviceErr := NewExactTreeLiteralIndexService(
			store, &exactTreeLiteralIndexSlowResolver{base: base, delay: time.Second},
		)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		service.claimLease = 120 * time.Millisecond
		service.claimHeartbeat = 20 * time.Millisecond
		service.claimPoll = 5 * time.Millisecond
		_, buildErr := service.Build(context.Background(), projectID, tree)
		if !errors.Is(buildErr, ErrExactTreeLiteralBuildClaimLost) ||
			len(store.builds) != 0 || store.renewCalls != 1 || store.releaseCalls != 1 || base.calls != 0 {
			t.Fatalf("claim loss err=%v builds=%d renew=%d release=%d resolve=%d",
				buildErr, len(store.builds), store.renewCalls, store.releaseCalls, base.calls)
		}
	})

	t.Run("renewal did not advance", func(t *testing.T) {
		store := &exactTreeLiteralIndexStoreFake{staleRenewal: true}
		base := &exactTreeLiteralIndexResolverFake{
			projectID: projectID, values: map[string][]byte{hash: body},
		}
		service, serviceErr := NewExactTreeLiteralIndexService(
			store, &exactTreeLiteralIndexSlowResolver{base: base, delay: time.Second},
		)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		service.claimLease = 120 * time.Millisecond
		service.claimHeartbeat = 20 * time.Millisecond
		service.claimPoll = 5 * time.Millisecond
		_, buildErr := service.Build(context.Background(), projectID, tree)
		if !errors.Is(buildErr, ErrExactTreeLiteralBuildClaimLost) ||
			len(store.builds) != 0 || store.renewCalls != 1 || store.releaseCalls != 1 {
			t.Fatalf("stale renewal err=%v builds=%d renew=%d release=%d",
				buildErr, len(store.builds), store.renewCalls, store.releaseCalls)
		}
	})

	t.Run("release failure", func(t *testing.T) {
		store := &exactTreeLiteralIndexStoreFake{releaseErr: errors.New("database unavailable")}
		resolver := &exactTreeLiteralIndexResolverFake{
			projectID: projectID, values: map[string][]byte{hash: body},
		}
		service, serviceErr := NewExactTreeLiteralIndexService(store, resolver)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		_, buildErr := service.Build(context.Background(), projectID, tree)
		if !errors.Is(buildErr, ErrExactTreeLiteralClaimRelease) ||
			len(store.builds) != 1 || store.releaseCalls != 1 {
			t.Fatalf("release failure err=%v builds=%d release=%d",
				buildErr, len(store.builds), store.releaseCalls)
		}
	})
}

func TestExactTreeLiteralIndexWaitingBuildIsCancellableBeforeBlobResolution(t *testing.T) {
	projectID := uuid.NewString()
	body := []byte("waiting source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/waiting.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	waiting := ExactTreeLiteralIndexBuildClaimResult{Disposition: ExactTreeLiteralBuildClaimWaiting}
	store := &exactTreeLiteralIndexStoreFake{claimResult: &waiting}
	resolver := &exactTreeLiteralIndexResolverFake{values: map[string][]byte{hash: body}}
	service, err := NewExactTreeLiteralIndexService(store, resolver)
	if err != nil {
		t.Fatal(err)
	}
	service.claimPoll = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = service.Build(ctx, projectID, tree)
	if !errors.Is(err, context.DeadlineExceeded) || resolver.calls != 0 || store.acquireCalls < 2 || store.releaseCalls != 0 {
		t.Fatalf("waiting cancellation err=%v resolve=%d acquire=%d release=%d",
			err, resolver.calls, store.acquireCalls, store.releaseCalls)
	}
}

func exactTreeLiteralIndexPointer(contentHash string, byteSize int64) FileBlobPointer {
	ownerID := uuid.NewString()
	return FileBlobPointer{
		Store: FileContentStore, Ref: "exact-tree-index-" + ownerID, OwnerID: ownerID,
		ContentHash: contentHash, ByteSize: byteSize,
		ContentObjectHash: digestFixture("exact-tree-index-object-" + ownerID),
	}
}

func cloneExactTreeLiteralIndexBuild(build ExactTreeLiteralIndexBuild) ExactTreeLiteralIndexBuild {
	cloned := build
	cloned.Files = make([]ExactTreeLiteralIndexBuildFile, len(build.Files))
	for index, file := range build.Files {
		cloned.Files[index] = file
		cloned.Files[index].Body = append([]byte(nil), file.Body...)
		if file.Body == nil {
			cloned.Files[index].Body = nil
		}
	}
	return cloned
}
