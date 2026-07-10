package dataruntime

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

type PlatformDependencies struct {
	Database      *gorm.DB
	Access        ProjectAuthorizer
	EncryptionKey []byte
	StoreOptions  GORMStoreOptions
	Prober        ConnectionProber
}

type PublicRuntimePlatformDependencies struct {
	Database      *gorm.DB
	Access        ProjectAuthorizer
	EncryptionKey []byte
	StoreOptions  GORMStoreOptions
	Now           func() time.Time
	TokenSource   func() (string, string, error)
}

// NewPlatformService is the root bootstrap seam. It keeps the encryption key
// explicit (never silently generated in ephemeral storage) and installs the
// SSRF-hardened prober unless a test/controlled implementation is injected.
func NewPlatformService(dependencies PlatformDependencies) (*Service, error) {
	if dependencies.Database == nil || dependencies.Access == nil {
		return nil, errors.New("data runtime database and access control are required")
	}
	sealer, err := NewAESGCMSealer(dependencies.EncryptionKey)
	if err != nil {
		return nil, err
	}
	store, err := NewGORMStore(dependencies.Database, sealer, dependencies.StoreOptions)
	if err != nil {
		return nil, err
	}
	prober := dependencies.Prober
	if prober == nil {
		prober, err = NewSupabaseProber(SupabaseProberOptions{})
		if err != nil {
			return nil, err
		}
	}
	return NewService(Dependencies{Repository: store, Access: dependencies.Access, Prober: prober})
}

// NewPlatformPublicRuntime is intentionally separate from the authenticated
// builder service so the HTTP bootstrap can mount its public routes outside
// session/CSRF middleware. It shares only the same project-scoped repository.
func NewPlatformPublicRuntime(dependencies PublicRuntimePlatformDependencies) (*PublicRuntimeService, error) {
	if dependencies.Database == nil || dependencies.Access == nil {
		return nil, errors.New("public data runtime database and access control are required")
	}
	sealer, err := NewAESGCMSealer(dependencies.EncryptionKey)
	if err != nil {
		return nil, err
	}
	store, err := NewGORMStore(dependencies.Database, sealer, dependencies.StoreOptions)
	if err != nil {
		return nil, err
	}
	return NewPublicRuntimeService(PublicRuntimeDependencies{
		Data: store, Runtime: store, Access: dependencies.Access,
		Now: dependencies.Now, TokenSource: dependencies.TokenSource,
	})
}
