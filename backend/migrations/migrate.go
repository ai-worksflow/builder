package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed *.up.sql *.down.sql
var files embed.FS

const advisoryLockID int64 = 0x57534B464C4F57 // "WSKFLOW"

const migrationLockReleaseTimeout = 5 * time.Second

type Applied struct {
	Version           string
	Checksum          string
	DownChecksum      string
	DownChecksumValid bool
	AppliedAt         time.Time
}

type migrationPair struct {
	version  string
	upName   string
	downName string
}

type migrationChecksumRotation struct {
	version             string
	fromChecksum        string
	toChecksum          string
	fromDown            string
	toDown              string
	requiresVersion     string
	replayCurrentUpInTx bool
}

type migrationVersionRelocation struct {
	fromVersion         string
	toVersion           string
	checksum            string
	downChecksum        string
	requiredPredecessor string
}

type migrationLineagePlan struct {
	prefix     []Applied
	relocation *migrationVersionRelocation
}

// These exact historical identities have an audited forward path to the
// embedded migration contract. A rotation that changes physical DDL replays
// the current up migration and updates both ledger checksums in the same
// transaction. Adding or changing any other historical identity fails closed.
var acceptedMigrationChecksumRotations = map[string][]migrationChecksumRotation{
	"000073_qualification_evidence_event_store": {{
		version:      "000073_qualification_evidence_event_store",
		fromChecksum: "ef8227e3bfa6867aadcbd969557482ad06a95d63e506f566bd74eb5ae84b194d",
		toChecksum:   "ef8227e3bfa6867aadcbd969557482ad06a95d63e506f566bd74eb5ae84b194d",
		fromDown:     "ebeb8bc2081eefe5b66a27a805451760f9c7d8ce72b516cdb3e14ae1a3ff131c",
		toDown:       "830ff07927d4d3d25dde6ff02ebf4c1a840db61c559e1f1480b5c2f2b5404bbd",
	}},
	"000074_qualification_plan_authority": {{
		version:      "000074_qualification_plan_authority",
		fromChecksum: "cc14d6577dd9facc8da47d0aab8ae24e1a390854d909d5b64a94de8de52a23b2",
		toChecksum:   "cc14d6577dd9facc8da47d0aab8ae24e1a390854d909d5b64a94de8de52a23b2",
		fromDown:     "d1191909124f8ba28e28236d9c5cfe3c7e3b57d8bd1fb267e8581f0d378cd0dc",
		toDown:       "7760b1b4485e359b1b67ecf4941c0893a2fca8cbc6caeadaa16176731582f2e3",
	}},
	"000075_qualification_receipt_v3_store": {{
		version:      "000075_qualification_receipt_v3_store",
		fromChecksum: "563b59a930670439ccd265efa7bc6f0ec6425b7b09b370ec864ad42f2df75cf8",
		toChecksum:   "563b59a930670439ccd265efa7bc6f0ec6425b7b09b370ec864ad42f2df75cf8",
		fromDown:     "548dee41879045d32eb5148867c4bcd036595afa1a9a986798d11f7440b29ad9",
		toDown:       "d976ec41ec44912dfe23c3da9f5ce4136cec8323497f510c16c6e052287587be",
	}},
	"000077_canonical_review_authority_hardening": {{
		version:         "000077_canonical_review_authority_hardening",
		fromChecksum:    "796e064e76a0013093569a7cb2ab33697bd5d5308cf6560df05429f95d20dba6",
		toChecksum:      "f0f2339ecd2ffefb6589cbd2b0e93dcd673ac6be1ff6204315fc1a91d51b9f35",
		fromDown:        "78c6ca91a242e2ed7b69d7fbccdec1b8b95335e8c53dd7f1a1408a0f50fed0f1",
		toDown:          "bd9f5ac973067f506e0e5953abb4b809798038242c1063cc0f4c6f07b35ac038",
		requiresVersion: "000083_canonical_review_authority_forward_equivalence",
	}},
	"000082_qualification_handoff_v1": {{
		version:         "000082_qualification_handoff_v1",
		fromChecksum:    "486e22f4a56c1bfd72c4fda838cb925ec4068bd5539a5f6f826deb5231d9e48c",
		toChecksum:      "35fab8eed53b3a18445b18c941b2fc0fb003710204af082b629071f7f8d27747",
		fromDown:        "a75a80c29e448ee238d819e4f764f00741652508c4a91b54c619b113c4faaedf",
		toDown:          "a75a80c29e448ee238d819e4f764f00741652508c4a91b54c619b113c4faaedf",
		requiresVersion: "000084_workflow_execution_profile_v3_qualified_release",
	}},
	"000083_canonical_review_authority_forward_equivalence": {
		{
			version:             "000083_canonical_review_authority_forward_equivalence",
			fromChecksum:        "8012de08459951e4c9aaff9aed21bb86d4c451d1e6e4932cff60fd99756b4e42",
			toChecksum:          "08383d054fb24b3cfc8542391bce9971584c9543a5621e1be3a1302b5409ed20",
			fromDown:            "fa23583719be79393ead487515142836d71ac19f62a5ea7ed537fceabaae4a5e",
			toDown:              "df418ab352c1ddc8c41b4919d23dbb46b4c404b7eb001502141cfc6a04a79a16",
			replayCurrentUpInTx: true,
		},
	},
}

// This is the only accepted migration-version relocation. An earlier build
// shipped the exact Candidate sandbox lifecycle gate as sequence 84. The
// current lineage inserts qualified-release and quality-precommit authorities
// before that unchanged gate, so the already-applied row must move to 86. Both
// SQL checksums and the complete predecessor prefix are required; no other
// future migration or gap is admitted.
var acceptedMigrationVersionRelocation = migrationVersionRelocation{
	fromVersion:         "000084_candidate_sandbox_lifecycle_write_gate_v2",
	toVersion:           "000086_candidate_sandbox_lifecycle_write_gate_v2",
	checksum:            "b119d29fce582c365cd8eaa5c72e050117a954c56fd0e4ececc4067e9cc0a13b",
	downChecksum:        "1a431137f4c2bb3683fe9fa3cca07d0fb0f782e3b4c0b9884d412ee894d97956",
	requiredPredecessor: "000083_canonical_review_authority_forward_equivalence",
}

const canonicalReviewForwardRepairPreflightSQL = `
DO $canonical_review_forward_repair_preflight$
DECLARE
  v_schema name := pg_catalog.current_schema();
  v_migration_owner oid;
  v_object_owner oid;
  v_relation regclass;
  v_function regprocedure;
  v_identity text;
BEGIN
  SELECT oid INTO v_migration_owner
  FROM pg_catalog.pg_roles
  WHERE rolname = 'worksflow_migration_owner';
  IF v_migration_owner IS NULL THEN
    RAISE EXCEPTION '000083 forward repair requires worksflow_migration_owner'
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.has_schema_privilege(
       current_user, v_schema, 'CREATE'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION '000083 forward repair requires CREATE on schema %', v_schema
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.pg_has_role(
       current_user, v_migration_owner, 'MEMBER'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION '000083 forward repair requires membership in worksflow_migration_owner'
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.to_regclass(pg_catalog.format(
       '%I.canonical_review_83_legacy_release_acl_provenance', v_schema
     )) IS NOT NULL THEN
    RAISE EXCEPTION '000083 old checksum conflicts with an existing ACL provenance table'
      USING ERRCODE = '55000';
  END IF;

  FOREACH v_identity IN ARRAY ARRAY[
    'review_requests',
    'review_decisions',
    'canonical_review_approval_receipts'
  ] LOOP
    v_relation := pg_catalog.to_regclass(
      pg_catalog.format('%I.%I', v_schema, v_identity)
    );
    IF v_relation IS NULL THEN
      RAISE EXCEPTION '000083 forward repair requires relation %', v_identity
        USING ERRCODE = '55000';
    END IF;
    SELECT relowner INTO v_object_owner
    FROM pg_catalog.pg_class
    WHERE oid = v_relation;
    IF pg_catalog.pg_has_role(
         current_user, v_object_owner, 'MEMBER'
       ) IS NOT TRUE THEN
      RAISE EXCEPTION '000083 forward repair cannot manage relation %', v_identity
        USING ERRCODE = '42501';
    END IF;
  END LOOP;
  v_relation := pg_catalog.to_regclass(
    pg_catalog.format('%I.review_decisions', v_schema)
  );
  IF (
    SELECT pg_catalog.count(*)
    FROM pg_catalog.pg_constraint
    WHERE conrelid = v_relation
      AND conname = 'review_decisions_authority_facts_check'
      AND contype = 'c'
      AND convalidated IS TRUE
  ) <> 1 THEN
    RAISE EXCEPTION '000083 forward repair requires the validated review_decisions_authority_facts_check constraint'
      USING ERRCODE = '55000';
  END IF;

  FOREACH v_identity IN ARRAY ARRAY[
    pg_catalog.format('%I.canonical_review_timestamp_is_exact(text)', v_schema),
    pg_catalog.format(
      '%I.canonical_review_approval_receipt_record_is_exact(%I.canonical_review_approval_receipts)',
      v_schema, v_schema
    ),
    pg_catalog.format('%I.issue_canonical_review_approval_receipt(uuid)', v_schema)
  ] LOOP
    v_function := pg_catalog.to_regprocedure(v_identity);
    IF v_function IS NULL THEN
      RAISE EXCEPTION '000083 forward repair requires function %', v_identity
        USING ERRCODE = '55000';
    END IF;
    SELECT proowner INTO v_object_owner
    FROM pg_catalog.pg_proc
    WHERE oid = v_function;
    IF pg_catalog.pg_has_role(
         current_user, v_object_owner, 'MEMBER'
       ) IS NOT TRUE THEN
      RAISE EXCEPTION '000083 forward repair cannot replace function %', v_identity
        USING ERRCODE = '42501';
    END IF;
  END LOOP;

  FOREACH v_identity IN ARRAY ARRAY[
    'release_delivery_canonical_json(jsonb)',
    'release_delivery_embedded_hash_is_exact(jsonb,text)',
    'release_delivery_rfc3339_microsecond(timestamptz)'
  ] LOOP
    v_function := pg_catalog.to_regprocedure(
      pg_catalog.format('%I.%s', v_schema, v_identity)
    );
    IF v_function IS NULL THEN
      RAISE EXCEPTION '000083 forward repair requires legacy Release helper %', v_identity
        USING ERRCODE = '55000';
    END IF;
    IF pg_catalog.has_function_privilege(
         'worksflow_migration_owner', v_function, 'EXECUTE'
       ) IS NOT TRUE
       AND pg_catalog.has_function_privilege(
         current_user, v_function, 'EXECUTE WITH GRANT OPTION'
       ) IS NOT TRUE THEN
      RAISE EXCEPTION '000083 forward repair cannot grant legacy Release helper %', v_identity
        USING ERRCODE = '42501';
    END IF;
  END LOOP;
END;
$canonical_review_forward_repair_preflight$;`

var canonicalMigrationName = regexp.MustCompile(`^([0-9]{6})_[a-z0-9]+(?:_[a-z0-9]+)*\.(up|down)\.sql$`)

// VerifyCurrent is the API's read-only schema-head gate. It proves that every
// migration embedded in the running binary is recorded with its exact
// checksum and that the database contains no unknown migration version. It
// never creates schema_migrations or applies DDL.
func VerifyCurrent(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("database is required")
	}
	expected, err := expectedVersions()
	if err != nil {
		return err
	}
	applied, err := AppliedVersions(ctx, database)
	if err != nil {
		return fmt.Errorf("read schema migration head: %w", err)
	}
	return verifyAppliedVersions(expected, applied)
}

func Up(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("database is required")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	locked := false
	defer func() {
		if locked {
			releaseMigrationAdvisoryLock(connection)
		}
		_ = connection.Close()
	}()

	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	locked = true

	names, err := migrationFiles()
	if err != nil {
		return err
	}
	expected, err := expectedVersions()
	if err != nil {
		return err
	}

	if _, err := connection.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version text PRIMARY KEY,
  checksum text NOT NULL,
  down_checksum text,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `
ALTER TABLE schema_migrations
ADD COLUMN IF NOT EXISTS down_checksum text`); err != nil {
		return fmt.Errorf("add schema_migrations down checksum: %w", err)
	}
	applied, err := appliedVersions(ctx, connection)
	if err != nil {
		return fmt.Errorf("read schema migration prefix before apply: %w", err)
	}
	lineage, err := plannedMigrationLineage(expected, applied)
	if err != nil {
		return err
	}
	rotations, err := plannedMigrationChecksumRotations(expected, lineage.prefix)
	if err != nil {
		return err
	}
	if lineage.relocation != nil {
		if err := applyMigrationVersionRelocation(ctx, connection, *lineage.relocation); err != nil {
			return err
		}
		applied, err = appliedVersions(ctx, connection)
		if err != nil {
			return fmt.Errorf("read schema migration prefix after version relocation: %w", err)
		}
	}
	rotations, applied, err = applyReadyMigrationChecksumRotations(
		ctx, connection, expected, rotations, applied,
	)
	if err != nil {
		return err
	}
	if err := verifyAppliedUpgradeablePrefix(expected, applied); err != nil {
		return err
	}
	for index, name := range names {
		migration := expected[index]
		existing, exists := appliedMigrationByVersion(applied, migration.Version)
		needsApply := !exists ||
			(existing.Checksum == migration.Checksum && !existing.DownChecksumValid)
		if needsApply {
			if err := applyFile(ctx, connection, name); err != nil {
				return err
			}
			applied, err = appliedVersions(ctx, connection)
			if err != nil {
				return fmt.Errorf("read schema migration prefix after applying %s: %w", migration.Version, err)
			}
		}
		rotations, applied, err = applyReadyMigrationChecksumRotations(
			ctx, connection, expected, rotations, applied,
		)
		if err != nil {
			return err
		}
	}
	if len(rotations) != 0 {
		return fmt.Errorf(
			"database migration %s checksum rotation requires applied forward-equivalence migration %s",
			rotations[0].version, rotations[0].requiresVersion,
		)
	}

	if err := verifyAppliedVersions(expected, applied); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `
ALTER TABLE schema_migrations
ALTER COLUMN down_checksum SET NOT NULL`); err != nil {
		return fmt.Errorf("require schema_migrations down checksum: %w", err)
	}
	return nil
}

func releaseMigrationAdvisoryLock(connection *sql.Conn) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationLockReleaseTimeout)
	defer cancel()
	var unlocked bool
	err := connection.QueryRowContext(
		cleanupCtx,
		"SELECT pg_advisory_unlock($1)",
		advisoryLockID,
	).Scan(&unlocked)
	if err == nil && unlocked {
		return
	}
	// Returning ErrBadConn from Raw forces database/sql to discard the
	// physical session instead of returning a possibly still-locking session
	// to the pool. The cleanup query above is independently bounded.
	_ = connection.Raw(func(any) error { return driver.ErrBadConn })
}

func AppliedVersions(ctx context.Context, database *sql.DB) ([]Applied, error) {
	return appliedVersions(ctx, database)
}

type migrationRowsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func appliedVersions(ctx context.Context, querier migrationRowsQuerier) ([]Applied, error) {
	rows, err := querier.QueryContext(ctx, `
SELECT version, checksum, down_checksum, applied_at
FROM schema_migrations
ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Applied
	for rows.Next() {
		var applied Applied
		var downChecksum sql.NullString
		if err := rows.Scan(
			&applied.Version,
			&applied.Checksum,
			&downChecksum,
			&applied.AppliedAt,
		); err != nil {
			return nil, err
		}
		if downChecksum.Valid {
			applied.DownChecksum = downChecksum.String
			applied.DownChecksumValid = true
		}
		result = append(result, applied)
	}
	return result, rows.Err()
}

func expectedVersions() ([]Applied, error) {
	pairs, err := migrationPairs(files)
	if err != nil {
		return nil, err
	}
	expected := make([]Applied, 0, len(pairs))
	for _, pair := range pairs {
		upContents, err := files.ReadFile(pair.upName)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", pair.upName, err)
		}
		downContents, err := files.ReadFile(pair.downName)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", pair.downName, err)
		}
		upDigest := sha256.Sum256(upContents)
		downDigest := sha256.Sum256(downContents)
		expected = append(expected, Applied{
			Version:           pair.version,
			Checksum:          hex.EncodeToString(upDigest[:]),
			DownChecksum:      hex.EncodeToString(downDigest[:]),
			DownChecksumValid: true,
		})
	}
	return expected, nil
}

func verifyAppliedVersions(expected, applied []Applied) error {
	if len(applied) != len(expected) {
		return fmt.Errorf(
			"database migration set differs from binary: applied=%d expected=%d",
			len(applied), len(expected),
		)
	}
	for index := range expected {
		if applied[index].Version != expected[index].Version {
			return fmt.Errorf(
				"database migration version %d is %q, expected %q",
				index+1, applied[index].Version, expected[index].Version,
			)
		}
		if applied[index].Checksum != expected[index].Checksum {
			return fmt.Errorf(
				"database migration %s checksum differs from the running binary",
				expected[index].Version,
			)
		}
		if !applied[index].DownChecksumValid || applied[index].DownChecksum != expected[index].DownChecksum {
			return fmt.Errorf(
				"database migration %s down checksum differs from the running binary",
				expected[index].Version,
			)
		}
	}
	return nil
}

func verifyAppliedPrefix(expected, applied []Applied) error {
	if len(applied) > len(expected) {
		return fmt.Errorf(
			"database migration prefix is longer than the binary contract: applied=%d expected=%d",
			len(applied), len(expected),
		)
	}
	for index := range applied {
		if applied[index].Version != expected[index].Version {
			return fmt.Errorf(
				"database migration prefix version %d is %q, expected %q",
				index+1, applied[index].Version, expected[index].Version,
			)
		}
		if applied[index].Checksum != expected[index].Checksum {
			return fmt.Errorf(
				"database migration %s checksum differs from the running binary",
				expected[index].Version,
			)
		}
		if applied[index].DownChecksumValid && applied[index].DownChecksum != expected[index].DownChecksum {
			return fmt.Errorf(
				"database migration %s down checksum differs from the running binary",
				expected[index].Version,
			)
		}
	}
	return nil
}

func plannedMigrationChecksumRotations(expected, applied []Applied) ([]migrationChecksumRotation, error) {
	if len(applied) > len(expected) {
		return nil, fmt.Errorf(
			"database migration prefix is longer than the binary contract: applied=%d expected=%d",
			len(applied), len(expected),
		)
	}
	expectedVersions := make(map[string]struct{}, len(expected))
	for _, migration := range expected {
		expectedVersions[migration.Version] = struct{}{}
	}
	rotations := make([]migrationChecksumRotation, 0)
	for index := range applied {
		if applied[index].Version != expected[index].Version {
			return nil, fmt.Errorf(
				"database migration prefix version %d is %q, expected %q",
				index+1, applied[index].Version, expected[index].Version,
			)
		}
		if applied[index].Checksum == expected[index].Checksum &&
			(!applied[index].DownChecksumValid || applied[index].DownChecksum == expected[index].DownChecksum) {
			continue
		}
		candidates, allowed := acceptedMigrationChecksumRotations[expected[index].Version]
		var rotation *migrationChecksumRotation
		if applied[index].DownChecksumValid {
			for candidateIndex := range candidates {
				candidate := &candidates[candidateIndex]
				if candidate.version == expected[index].Version &&
					candidate.fromChecksum == applied[index].Checksum &&
					candidate.toChecksum == expected[index].Checksum &&
					candidate.fromDown == applied[index].DownChecksum &&
					candidate.toDown == expected[index].DownChecksum {
					rotation = candidate
					break
				}
			}
		}
		if !allowed || rotation == nil {
			kind := "checksum"
			if applied[index].Checksum == expected[index].Checksum {
				kind = "down checksum"
			}
			return nil, fmt.Errorf(
				"database migration %s %s differs from the running binary",
				expected[index].Version, kind,
			)
		}
		if rotation.requiresVersion != "" {
			if _, ok := expectedVersions[rotation.requiresVersion]; !ok {
				return nil, fmt.Errorf(
					"database migration %s checksum rotation requires forward-equivalence migration %s",
					rotation.version, rotation.requiresVersion,
				)
			}
		}
		rotations = append(rotations, *rotation)
	}
	return rotations, nil
}

func applyReadyMigrationChecksumRotations(
	ctx context.Context,
	connection *sql.Conn,
	expected []Applied,
	pending []migrationChecksumRotation,
	applied []Applied,
) ([]migrationChecksumRotation, []Applied, error) {
	for len(pending) > 0 {
		remaining := make([]migrationChecksumRotation, 0, len(pending))
		progressed := false
		for _, rotation := range pending {
			if !migrationChecksumRotationIsReady(expected, applied, rotation) {
				remaining = append(remaining, rotation)
				continue
			}
			if err := applyMigrationChecksumRotation(ctx, connection, rotation); err != nil {
				return pending, applied, err
			}
			progressed = true
		}
		pending = remaining
		if !progressed {
			return pending, applied, nil
		}
		var err error
		applied, err = appliedVersions(ctx, connection)
		if err != nil {
			return pending, nil, fmt.Errorf("read schema migration prefix after checksum rotation: %w", err)
		}
	}
	return pending, applied, nil
}

func migrationChecksumRotationIsReady(
	expected []Applied,
	applied []Applied,
	rotation migrationChecksumRotation,
) bool {
	return rotation.requiresVersion == "" ||
		appliedMigrationIsCurrent(expected, applied, rotation.requiresVersion)
}

func verifyAppliedUpgradeablePrefix(expected, applied []Applied) error {
	lineage, err := plannedMigrationLineage(expected, applied)
	if err != nil {
		return err
	}
	if lineage.relocation != nil {
		return fmt.Errorf(
			"database migration %s has not been relocated to %s",
			lineage.relocation.fromVersion, lineage.relocation.toVersion,
		)
	}
	_, err = plannedMigrationChecksumRotations(expected, lineage.prefix)
	return err
}

func appliedMigrationIsCurrent(expected, applied []Applied, version string) bool {
	want, exists := appliedMigrationByVersion(expected, version)
	if !exists {
		return false
	}
	got, exists := appliedMigrationByVersion(applied, version)
	return exists && got.Checksum == want.Checksum && got.DownChecksumValid &&
		got.DownChecksum == want.DownChecksum
}

func appliedMigrationByVersion(migrations []Applied, version string) (Applied, bool) {
	index := appliedMigrationIndex(migrations, version)
	if index < 0 {
		return Applied{}, false
	}
	return migrations[index], true
}

func plannedMigrationLineage(expected, applied []Applied) (migrationLineagePlan, error) {
	relocation := acceptedMigrationVersionRelocation
	targetIndex := appliedMigrationIndex(expected, relocation.toVersion)
	predecessorIndex := appliedMigrationIndex(expected, relocation.requiredPredecessor)
	if targetIndex < 0 || predecessorIndex < 0 || predecessorIndex >= targetIndex {
		return migrationLineagePlan{}, fmt.Errorf(
			"running binary does not contain the accepted migration relocation lineage %s -> %s",
			relocation.fromVersion, relocation.toVersion,
		)
	}
	target := expected[targetIndex]
	if target.Checksum != relocation.checksum || !target.DownChecksumValid ||
		target.DownChecksum != relocation.downChecksum {
		return migrationLineagePlan{}, fmt.Errorf(
			"running binary migration %s differs from the accepted relocation identity",
			relocation.toVersion,
		)
	}

	specialIndex := -1
	needsRelocation := false
	for index := range applied {
		if applied[index].Version != relocation.fromVersion && applied[index].Version != relocation.toVersion {
			continue
		}
		if specialIndex >= 0 {
			return migrationLineagePlan{}, fmt.Errorf(
				"database contains conflicting migration relocation versions %s and %s",
				applied[specialIndex].Version, applied[index].Version,
			)
		}
		specialIndex = index
		needsRelocation = applied[index].Version == relocation.fromVersion
	}
	if specialIndex < 0 {
		return migrationLineagePlan{prefix: applied}, nil
	}

	special := applied[specialIndex]
	if special.Checksum != relocation.checksum || !special.DownChecksumValid ||
		special.DownChecksum != relocation.downChecksum {
		return migrationLineagePlan{}, fmt.Errorf(
			"database migration %s checksums differ from the accepted relocation identity",
			special.Version,
		)
	}
	// Once the relocated migration occupies its canonical index, subsequent
	// migrations are an ordinary append-only prefix. Keep the complete applied
	// lineage so the generic checksum verifier can admit current and future
	// heads without treating every later migration as part of the temporary
	// 83..85 crash-recovery gap.
	if !needsRelocation && specialIndex == targetIndex {
		return migrationLineagePlan{prefix: applied}, nil
	}
	prefix := make([]Applied, 0, len(applied)-1)
	prefix = append(prefix, applied[:specialIndex]...)
	prefix = append(prefix, applied[specialIndex+1:]...)
	minimumPrefixLength := predecessorIndex + 1
	if len(prefix) < minimumPrefixLength || len(prefix) > targetIndex {
		return migrationLineagePlan{}, fmt.Errorf(
			"database migration %s is outside its accepted temporary gap: prefix=%d allowed=%d..%d",
			special.Version, len(prefix), minimumPrefixLength, targetIndex,
		)
	}
	for index := range prefix {
		if prefix[index].Version != expected[index].Version {
			return migrationLineagePlan{}, fmt.Errorf(
				"database migration prefix version %d is %q, expected %q",
				index+1, prefix[index].Version, expected[index].Version,
			)
		}
	}

	plan := migrationLineagePlan{prefix: prefix}
	if needsRelocation {
		plan.relocation = &acceptedMigrationVersionRelocation
	}
	return plan, nil
}

func verifyAppliedPrefixForUp(expected, applied []Applied) error {
	lineage, err := plannedMigrationLineage(expected, applied)
	if err != nil {
		return err
	}
	if lineage.relocation != nil {
		return fmt.Errorf(
			"database migration %s has not been relocated to %s",
			lineage.relocation.fromVersion, lineage.relocation.toVersion,
		)
	}
	return verifyAppliedPrefix(expected, lineage.prefix)
}

func appliedMigrationIndex(migrations []Applied, version string) int {
	for index := range migrations {
		if migrations[index].Version == version {
			return index
		}
	}
	return -1
}

func applyMigrationChecksumRotation(
	ctx context.Context,
	connection *sql.Conn,
	rotation migrationChecksumRotation,
) error {
	var currentUp []byte
	if rotation.replayCurrentUpInTx {
		name := rotation.version + ".up.sql"
		contents, err := files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration repair %s: %w", name, err)
		}
		digest := sha256.Sum256(contents)
		if hex.EncodeToString(digest[:]) != rotation.toChecksum {
			return fmt.Errorf(
				"migration %s repair bytes differ from the accepted target checksum",
				rotation.version,
			)
		}
		currentUp = contents
	}

	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s checksum rotation: %w", rotation.version, err)
	}
	defer transaction.Rollback()
	if len(currentUp) > 0 {
		if _, err := transaction.ExecContext(ctx, canonicalReviewForwardRepairPreflightSQL); err != nil {
			return fmt.Errorf("preflight migration %s physical forward repair: %w", rotation.version, err)
		}
		if _, err := transaction.ExecContext(ctx, string(currentUp)); err != nil {
			return fmt.Errorf("repair migration %s physical schema: %w", rotation.version, err)
		}
	}
	result, err := transaction.ExecContext(ctx, `
UPDATE schema_migrations
	SET checksum = $1,
	    down_checksum = $2
	WHERE version = $3
	  AND checksum = $4
	  AND down_checksum = $5`, rotation.toChecksum, rotation.toDown, rotation.version, rotation.fromChecksum, rotation.fromDown)
	if err != nil {
		return fmt.Errorf("rotate migration %s checksums: %w", rotation.version, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm migration %s checksum rotation: %w", rotation.version, err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("migration %s checksum rotation was not applied exactly once", rotation.version)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %s checksum rotation: %w", rotation.version, err)
	}
	return nil
}

func applyMigrationVersionRelocation(
	ctx context.Context,
	connection *sql.Conn,
	relocation migrationVersionRelocation,
) error {
	result, err := connection.ExecContext(ctx, `
UPDATE schema_migrations
SET version = $1
WHERE version = $2
  AND checksum = $3
  AND down_checksum = $4`, relocation.toVersion, relocation.fromVersion, relocation.checksum, relocation.downChecksum)
	if err != nil {
		return fmt.Errorf(
			"relocate migration %s to %s: %w",
			relocation.fromVersion, relocation.toVersion, err,
		)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"confirm migration %s relocation: %w",
			relocation.fromVersion, err,
		)
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"migration %s relocation was not applied exactly once",
			relocation.fromVersion,
		)
	}
	return nil
}

func migrationFiles() ([]string, error) {
	pairs, err := migrationPairs(files)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		result = append(result, pair.upName)
	}
	return result, nil
}

func migrationPairs(fileSystem fs.FS) ([]migrationPair, error) {
	entries, err := fs.ReadDir(fileSystem, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	pairsByVersion := make(map[string]migrationPair, len(entries)/2)
	versionBySequence := make(map[string]string, len(entries)/2)
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("embedded migration entry %q is not a canonical migration file", entry.Name())
		}
		matches := canonicalMigrationName.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("embedded migration entry %q is not canonically named", entry.Name())
		}
		direction := matches[2]
		version := strings.TrimSuffix(entry.Name(), "."+direction+".sql")
		sequence := matches[1]
		if existing, ok := versionBySequence[sequence]; ok && existing != version {
			return nil, fmt.Errorf("embedded migration sequence %s is duplicated", sequence)
		}
		versionBySequence[sequence] = version

		pair := pairsByVersion[version]
		pair.version = version
		switch direction {
		case "up":
			if pair.upName != "" {
				return nil, fmt.Errorf("embedded migration %s has duplicate up files", version)
			}
			pair.upName = entry.Name()
		case "down":
			if pair.downName != "" {
				return nil, fmt.Errorf("embedded migration %s has duplicate down files", version)
			}
			pair.downName = entry.Name()
		}
		pairsByVersion[version] = pair
	}

	result := make([]migrationPair, 0, len(pairsByVersion))
	for _, pair := range pairsByVersion {
		if pair.upName == "" || pair.downName == "" {
			return nil, fmt.Errorf("embedded migration %s does not have exactly one up/down pair", pair.version)
		}
		result = append(result, pair)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].version < result[right].version
	})
	for index, pair := range result {
		expectedSequence := fmt.Sprintf("%06d", index+1)
		actualSequence := strings.SplitN(pair.version, "_", 2)[0]
		if actualSequence != expectedSequence {
			return nil, fmt.Errorf(
				"embedded migration sequence is not contiguous: found %s, expected %s",
				actualSequence, expectedSequence,
			)
		}
	}
	return result, nil
}

func applyFile(ctx context.Context, connection *sql.Conn, name string) error {
	contents, err := files.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	version := strings.TrimSuffix(name, ".up.sql")
	upDigest := sha256.Sum256(contents)
	checksum := hex.EncodeToString(upDigest[:])
	downName := version + ".down.sql"
	downContents, err := files.ReadFile(downName)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", downName, err)
	}
	downDigest := sha256.Sum256(downContents)
	downChecksum := hex.EncodeToString(downDigest[:])

	var existingChecksum string
	var existingDownChecksum sql.NullString
	err = connection.QueryRowContext(ctx,
		"SELECT checksum, down_checksum FROM schema_migrations WHERE version = $1", version,
	).Scan(&existingChecksum, &existingDownChecksum)
	if err == nil {
		if existingChecksum != checksum {
			return fmt.Errorf("migration %s checksum changed after it was applied", version)
		}
		if existingDownChecksum.Valid {
			if existingDownChecksum.String != downChecksum {
				return fmt.Errorf("migration %s down checksum changed after it was applied", version)
			}
			return nil
		}
		result, err := connection.ExecContext(ctx, `
UPDATE schema_migrations
SET down_checksum = $2
WHERE version = $1
  AND checksum = $3
  AND down_checksum IS NULL`, version, downChecksum, checksum)
		if err != nil {
			return fmt.Errorf("establish migration %s down checksum baseline: %w", version, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("confirm migration %s down checksum baseline: %w", version, err)
		}
		if rowsAffected != 1 {
			return fmt.Errorf("migration %s down checksum baseline was not established exactly once", version)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect migration %s: %w", version, err)
	}

	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", version, err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, string(contents)); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, checksum, down_checksum) VALUES ($1, $2, $3)",
		version, checksum, downChecksum,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}
