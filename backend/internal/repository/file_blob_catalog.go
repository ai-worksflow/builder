package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type fileBlobModel struct {
	ID                uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	ProjectID         uuid.UUID `gorm:"column:project_id;type:uuid;not null"`
	Store             string    `gorm:"column:store;not null"`
	OwnerID           uuid.UUID `gorm:"column:owner_id;type:uuid;not null"`
	ContentRef        string    `gorm:"column:content_ref;not null"`
	ContentObjectHash string    `gorm:"column:content_object_hash;not null"`
	ContentHash       string    `gorm:"column:content_hash;not null"`
	ByteSize          int64     `gorm:"column:byte_size;not null"`
	CreatedBy         uuid.UUID `gorm:"column:created_by;type:uuid;not null"`
	CreatedAt         time.Time `gorm:"column:created_at;not null"`
}

func (fileBlobModel) TableName() string { return "repository_file_blobs" }

type GORMFileBlobCatalog struct {
	database *gorm.DB
}

func NewGORMFileBlobCatalog(database *gorm.DB) (*GORMFileBlobCatalog, error) {
	if database == nil {
		return nil, errors.New("repository file blob database is required")
	}
	return &GORMFileBlobCatalog{database: database}, nil
}

func (catalog *GORMFileBlobCatalog) RegisterFileBlob(
	ctx context.Context,
	registration FileBlobRegistration,
) (FileBlobPointer, error) {
	model, err := fileBlobModelFromRegistration(registration)
	if err != nil {
		return FileBlobPointer{}, err
	}
	var registered fileBlobModel
	err = catalog.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "content_hash"}, {Name: "byte_size"}},
			DoNothing: true,
		}).Create(&model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			registered = model
			return nil
		}
		return transaction.Where(
			"project_id = ? AND content_hash = ? AND byte_size = ?",
			model.ProjectID, model.ContentHash, model.ByteSize,
		).Take(&registered).Error
	})
	if err != nil {
		return FileBlobPointer{}, fmt.Errorf("register repository file blob: %w", err)
	}
	pointer, err := pointerFromFileBlobModel(registered)
	if err != nil {
		return FileBlobPointer{}, err
	}
	return pointer, nil
}

func (catalog *GORMFileBlobCatalog) FindFileBlob(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) (FileBlobPointer, bool, error) {
	projectUUID, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil || !isCanonicalSHA256(contentHash) || byteSize < 0 || byteSize > MaxFileBytes {
		return FileBlobPointer{}, false, ErrInvalidFilePointer
	}
	var model fileBlobModel
	err = catalog.database.WithContext(ctx).Where(
		"project_id = ? AND content_hash = ? AND byte_size = ?",
		projectUUID, contentHash, byteSize,
	).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return FileBlobPointer{}, false, nil
	}
	if err != nil {
		return FileBlobPointer{}, false, fmt.Errorf("find repository file blob: %w", err)
	}
	pointer, err := pointerFromFileBlobModel(model)
	if err != nil {
		return FileBlobPointer{}, false, err
	}
	return pointer, true, nil
}

func fileBlobModelFromRegistration(registration FileBlobRegistration) (fileBlobModel, error) {
	id, idErr := uuid.Parse(strings.TrimSpace(registration.ID))
	projectID, projectErr := uuid.Parse(strings.TrimSpace(registration.ProjectID))
	createdBy, actorErr := uuid.Parse(strings.TrimSpace(registration.CreatedBy))
	if idErr != nil || projectErr != nil || actorErr != nil || registration.CreatedAt.IsZero() ||
		registration.Pointer.OwnerID != registration.ID || registration.Pointer.validate() != nil {
		return fileBlobModel{}, ErrFileBlobCatalogContract
	}
	return fileBlobModel{
		ID: id, ProjectID: projectID, Store: registration.Pointer.Store,
		OwnerID: id, ContentRef: registration.Pointer.Ref,
		ContentObjectHash: registration.Pointer.ContentObjectHash,
		ContentHash:       registration.Pointer.ContentHash, ByteSize: registration.Pointer.ByteSize,
		CreatedBy: createdBy, CreatedAt: registration.CreatedAt.UTC(),
	}, nil
}

func pointerFromFileBlobModel(model fileBlobModel) (FileBlobPointer, error) {
	if model.ID == uuid.Nil || model.ProjectID == uuid.Nil || model.OwnerID != model.ID ||
		model.CreatedBy == uuid.Nil || model.CreatedAt.IsZero() {
		return FileBlobPointer{}, ErrFileBlobCatalogContract
	}
	pointer := FileBlobPointer{
		Store: model.Store, Ref: model.ContentRef, OwnerID: model.OwnerID.String(),
		ContentHash: model.ContentHash, ByteSize: model.ByteSize,
		ContentObjectHash: model.ContentObjectHash,
	}
	if err := pointer.validate(); err != nil {
		return FileBlobPointer{}, fmt.Errorf("%w: %v", ErrFileBlobCatalogContract, err)
	}
	return pointer, nil
}
