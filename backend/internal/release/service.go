package release

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type ProjectAuthorizer interface {
	RequireProjectView(context.Context, string, string) error
	RequireProjectEdit(context.Context, string, string) error
}

type BundleStore interface {
	Create(context.Context, string, string, string, string, string) (Bundle, error)
	Get(context.Context, string, string, string) (Bundle, error)
	GetByReceipt(context.Context, string, string, string) (Bundle, error)
}

type CreateBundleRequest struct {
	ProjectID            string `json:"projectId"`
	CanonicalReceiptID   string `json:"canonicalReceiptId"`
	CanonicalReceiptHash string `json:"canonicalReceiptHash"`
	ActorID              string `json:"-"`
	OperationID          string `json:"-"`
}

type BundleView struct {
	Bundle   Bundle `json:"bundle"`
	Replayed bool   `json:"replayed"`
}

type Service struct {
	store  BundleStore
	access ProjectAuthorizer
}

func NewService(store BundleStore, access ProjectAuthorizer) (*Service, error) {
	if store == nil || access == nil {
		return nil, errors.New("release Bundle store and authorizer are required")
	}
	return &Service{store: store, access: access}, nil
}

func (service *Service) CreateBundle(ctx context.Context, request CreateBundleRequest) (BundleView, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.CanonicalReceiptID = strings.TrimSpace(request.CanonicalReceiptID)
	request.CanonicalReceiptHash = strings.TrimSpace(request.CanonicalReceiptHash)
	request.ActorID = strings.TrimSpace(request.ActorID)
	request.OperationID = strings.TrimSpace(request.OperationID)
	if !validUUID(request.ProjectID) || !validUUID(request.CanonicalReceiptID) || !validUUID(request.ActorID) ||
		!exactHash(request.CanonicalReceiptHash) || request.OperationID == "" || len(request.OperationID) > 128 ||
		strings.ContainsRune(request.OperationID, '\x00') {
		return BundleView{}, invalid("create Bundle request")
	}
	if err := service.access.RequireProjectEdit(ctx, request.ProjectID, request.ActorID); err != nil {
		return BundleView{}, fmt.Errorf("authorize ReleaseBundle creation: %w", err)
	}
	if existing, err := service.store.GetByReceipt(
		ctx, request.ProjectID, request.CanonicalReceiptID, request.CanonicalReceiptHash,
	); err == nil {
		return BundleView{Bundle: existing, Replayed: true}, nil
	} else if !errors.Is(err, ErrBundleNotFound) {
		return BundleView{}, err
	}
	bundleID := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte("release-bundle\x00"+request.ProjectID+"\x00"+request.CanonicalReceiptID+"\x00"+request.CanonicalReceiptHash),
	).String()
	bundle, err := service.store.Create(
		ctx, request.ProjectID, request.CanonicalReceiptID, request.CanonicalReceiptHash, bundleID, request.ActorID,
	)
	if err == nil {
		return BundleView{Bundle: bundle, Replayed: false}, nil
	}
	if errors.Is(err, ErrBundleConflict) {
		existing, findErr := service.store.GetByReceipt(
			ctx, request.ProjectID, request.CanonicalReceiptID, request.CanonicalReceiptHash,
		)
		if findErr == nil {
			return BundleView{Bundle: existing, Replayed: true}, nil
		}
	}
	return BundleView{}, err
}

func (service *Service) GetBundle(
	ctx context.Context,
	projectID, bundleID, bundleHash, actorID string,
) (Bundle, error) {
	projectID, bundleID = strings.TrimSpace(projectID), strings.TrimSpace(bundleID)
	bundleHash, actorID = strings.TrimSpace(bundleHash), strings.TrimSpace(actorID)
	if !validUUID(projectID) || !validUUID(bundleID) || !validUUID(actorID) || !exactHash(bundleHash) {
		return Bundle{}, invalid("get Bundle request")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return Bundle{}, fmt.Errorf("authorize ReleaseBundle view: %w", err)
	}
	return service.store.Get(ctx, projectID, bundleID, bundleHash)
}

func (service *Service) GetBundleByReceipt(
	ctx context.Context,
	projectID, receiptID, receiptHash, actorID string,
) (Bundle, error) {
	projectID, receiptID = strings.TrimSpace(projectID), strings.TrimSpace(receiptID)
	receiptHash, actorID = strings.TrimSpace(receiptHash), strings.TrimSpace(actorID)
	if !validUUID(projectID) || !validUUID(receiptID) || !validUUID(actorID) || !exactHash(receiptHash) {
		return Bundle{}, invalid("get Bundle by Receipt request")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return Bundle{}, fmt.Errorf("authorize ReleaseBundle view: %w", err)
	}
	return service.store.GetByReceipt(ctx, projectID, receiptID, receiptHash)
}

func validUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
