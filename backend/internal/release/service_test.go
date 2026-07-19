package release

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type releaseAccessFake struct {
	projectID string
	actorID   string
	edit      bool
	err       error
}

func (access *releaseAccessFake) RequireProjectView(_ context.Context, projectID, actorID string) error {
	access.projectID, access.actorID, access.edit = projectID, actorID, false
	return access.err
}

func (access *releaseAccessFake) RequireProjectEdit(_ context.Context, projectID, actorID string) error {
	access.projectID, access.actorID, access.edit = projectID, actorID, true
	return access.err
}

type releaseBundleStoreFake struct {
	bundle      Bundle
	findErr     error
	createErr   error
	createCalls int
}

func (store *releaseBundleStoreFake) Create(
	context.Context,
	string, string, string, string, string,
) (Bundle, error) {
	store.createCalls++
	return store.bundle, store.createErr
}

func (store *releaseBundleStoreFake) Get(
	context.Context,
	string, string, string,
) (Bundle, error) {
	return store.bundle, store.findErr
}

func (store *releaseBundleStoreFake) GetByReceipt(
	context.Context,
	string, string, string,
) (Bundle, error) {
	return store.bundle, store.findErr
}

func TestReleaseServiceCreatesOnceAndRecoversExactReceiptReplay(t *testing.T) {
	receipt := passingCanonicalReceipt(t)
	bundle, err := NewBundle(NewBundleInput{
		ID: uuid.NewString(), Receipt: receipt, CreatedBy: receipt.CreatedBy, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &releaseBundleStoreFake{bundle: bundle, findErr: ErrBundleNotFound}
	access := &releaseAccessFake{}
	service, err := NewService(store, access)
	if err != nil {
		t.Fatal(err)
	}
	request := CreateBundleRequest{
		ProjectID: receipt.ProjectID, CanonicalReceiptID: receipt.ID,
		CanonicalReceiptHash: receipt.PayloadHash, ActorID: receipt.CreatedBy,
		OperationID: "release-bundle-operation",
	}
	created, err := service.CreateBundle(context.Background(), request)
	if err != nil || created.Replayed || created.Bundle.BundleHash != bundle.BundleHash || store.createCalls != 1 || !access.edit {
		t.Fatalf("create Bundle = %#v, calls=%d access=%#v err=%v", created, store.createCalls, access, err)
	}
	store.findErr = nil
	replayed, err := service.CreateBundle(context.Background(), request)
	if err != nil || !replayed.Replayed || store.createCalls != 1 {
		t.Fatalf("replay Bundle = %#v, calls=%d err=%v", replayed, store.createCalls, err)
	}
}

func TestReleaseServiceRequiresExactHashAndAuthorization(t *testing.T) {
	store := &releaseBundleStoreFake{findErr: ErrBundleNotFound}
	access := &releaseAccessFake{err: errors.New("denied")}
	service, err := NewService(store, access)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateBundle(context.Background(), CreateBundleRequest{
		ProjectID: uuid.NewString(), CanonicalReceiptID: uuid.NewString(),
		CanonicalReceiptHash: "sha256:" + string(make([]byte, 64)),
		ActorID:              uuid.NewString(), OperationID: "release",
	})
	if !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("invalid exact hash was accepted: %v", err)
	}
	_, err = service.CreateBundle(context.Background(), CreateBundleRequest{
		ProjectID: uuid.NewString(), CanonicalReceiptID: uuid.NewString(),
		CanonicalReceiptHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ActorID:              uuid.NewString(), OperationID: "release",
	})
	if err == nil || err.Error() != "authorize ReleaseBundle creation: denied" {
		t.Fatalf("authorization failure was not preserved: %v", err)
	}
}
