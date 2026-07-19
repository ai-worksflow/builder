package testsupport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

// TestingT is the small test boundary used by fixture helpers without making
// callers depend on a concrete testing implementation.
type TestingT interface {
	Helper()
	Fatalf(string, ...any)
}

type ApplicationBuildContractRef struct {
	ID           uuid.UUID
	ContractHash string
}

func ReadyApplicationBuildContract(
	t TestingT,
	database *gorm.DB,
	projectID, actorID, manifestID uuid.UUID,
) ApplicationBuildContractRef {
	t.Helper()
	var manifest struct {
		ProjectID    uuid.UUID `gorm:"column:project_id"`
		ManifestHash string    `gorm:"column:manifest_hash"`
	}
	result := database.Table("application_build_manifests").
		Select("project_id, manifest_hash").Where("id = ?", manifestID).Take(&manifest)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load BuildManifest for exact test contract: %v", result.Error)
	}
	if manifest.ProjectID != projectID {
		t.Fatalf("test BuildContract project differs from BuildManifest")
	}
	contract, err := ensureReadyApplicationBuildContract(
		database, projectID, actorID, manifestID, manifest.ManifestHash,
	)
	if err != nil {
		t.Fatalf("seed exact test BuildContract: %v", err)
	}
	return contract
}

// CreateBoundImplementationProposal preserves the production BuildContract
// trigger in tests that directly seed an already-produced implementation
// Proposal. Historical lineage tests may start with a consumed manifest; the
// exact frozen state required at Proposal creation is restored only inside the
// fixture transaction and the original state is written back before commit.
func CreateBoundImplementationProposal(
	t TestingT,
	database *gorm.DB,
	proposal *storage.ImplementationProposalModel,
) ApplicationBuildContractRef {
	t.Helper()
	if database == nil || proposal == nil {
		t.Fatalf("BuildContract fixture requires a database and Proposal")
	}

	var manifest struct {
		ProjectID    uuid.UUID `gorm:"column:project_id"`
		ManifestHash string    `gorm:"column:manifest_hash"`
		Status       string    `gorm:"column:status"`
		CreatedBy    uuid.UUID `gorm:"column:created_by"`
	}
	result := database.Table("application_build_manifests").
		Select("project_id, manifest_hash, status, created_by").
		Where("id = ?", proposal.BuildManifestID).
		Take(&manifest)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load BuildManifest for exact test contract: %v", result.Error)
	}
	if manifest.ProjectID != proposal.ProjectID {
		t.Fatalf("Proposal and BuildManifest fixture projects differ")
	}
	actorID := proposal.CreatedBy
	if actorID == uuid.Nil {
		actorID = manifest.CreatedBy
	}
	contract, err := ensureReadyApplicationBuildContract(
		database, proposal.ProjectID, actorID, proposal.BuildManifestID, manifest.ManifestHash,
	)
	if err != nil {
		t.Fatalf("seed exact test BuildContract: %v", err)
	}
	proposal.ApplicationBuildContractID = &contract.ID
	proposal.ApplicationBuildContractHash = &contract.ContractHash

	err = database.Transaction(func(transaction *gorm.DB) error {
		if manifest.Status != "frozen" {
			if err := setReplicationRole(transaction, "replica"); err != nil {
				return err
			}
			if err := transaction.Exec(
				"UPDATE application_build_manifests SET status = 'frozen' WHERE id = ?",
				proposal.BuildManifestID,
			).Error; err != nil {
				return err
			}
			if err := setReplicationRole(transaction, "origin"); err != nil {
				return err
			}
		}
		if err := transaction.Create(proposal).Error; err != nil {
			return err
		}
		if manifest.Status != "frozen" {
			if err := setReplicationRole(transaction, "replica"); err != nil {
				return err
			}
			if err := transaction.Exec(
				"UPDATE application_build_manifests SET status = ? WHERE id = ?",
				manifest.Status, proposal.BuildManifestID,
			).Error; err != nil {
				return err
			}
			if err := setReplicationRole(transaction, "origin"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert BuildContract-bound test Proposal: %v", err)
	}
	return contract
}

func ensureReadyApplicationBuildContract(
	database *gorm.DB,
	projectID, actorID, manifestID uuid.UUID,
	manifestHash string,
) (ApplicationBuildContractRef, error) {
	contract := ApplicationBuildContractRef{
		ID:           uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-build-contract:"+manifestID.String())),
		ContractHash: testDigest("contract:" + manifestID.String()),
	}
	stackID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-full-stack:"+projectID.String()))
	stackHash := testDigest("full-stack:" + projectID.String())
	templateID := "test-stack-" + projectID.String()[:8]
	createdAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	document, err := json.Marshal(map[string]any{
		"id": stackID.String(), "schemaVersion": "full-stack-template/v1",
		"templateId": templateID, "version": "1.0.0",
		"components": []any{map[string]any{"role": "web"}, map[string]any{"role": "api"}},
		"layout":     map[string]any{}, "contentHash": stackHash,
		"createdBy": actorID.String(), "createdAt": createdAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return ApplicationBuildContractRef{}, err
	}
	err = database.Transaction(func(transaction *gorm.DB) error {
		if err := setReplicationRole(transaction, "replica"); err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO full_stack_template_releases (
  id, schema_version, template_id, release_version, document,
  content_hash, created_by, created_at
) VALUES (?, 'full-stack-template/v1', ?, '1.0.0', ?::jsonb, ?, ?, ?)
ON CONFLICT (id) DO NOTHING
`, stackID, templateID, string(document), stackHash, actorID, createdAt).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO application_build_contracts (
  id, project_id, build_manifest_id, build_manifest_hash,
  full_stack_template_id, full_stack_template_hash,
  schema_version, compiler_version, compiler_hash,
  content_store, content_ref, content_hash, contract_hash,
  status, must_count, must_ready_count, obligation_count,
  source_count, template_release_count, blocking_count, conflict_count,
  version, created_by, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?,
  'application-build-contract/v2', 'test-fixture/v1', ?,
  'mongo', ?, ?, ?,
  'ready', 1, 1, 1, 0, 0, 0, 0, 1, ?, ?
)
ON CONFLICT (id) DO NOTHING
`, contract.ID, projectID, manifestID, manifestHash, stackID, stackHash,
			testDigest("compiler"), "test-contract-"+manifestID.String(),
			testDigest("contract-content:"+manifestID.String()), contract.ContractHash,
			actorID, createdAt).Error; err != nil {
			return err
		}
		return setReplicationRole(transaction, "origin")
	})
	if err != nil {
		return ApplicationBuildContractRef{}, err
	}
	return contract, nil
}

func setReplicationRole(transaction *gorm.DB, role string) error {
	if role != "replica" && role != "origin" {
		return fmt.Errorf("invalid replication role %q", role)
	}
	return transaction.Exec("SET LOCAL session_replication_role = " + role).Error
}

func testDigest(seed string) string {
	digest := sha256.Sum256([]byte("test-support:" + seed))
	return "sha256:" + hex.EncodeToString(digest[:])
}
