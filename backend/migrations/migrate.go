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
	if err := verifyAppliedPrefix(expected, applied); err != nil {
		return err
	}
	for _, name := range names {
		if err := applyFile(ctx, connection, name); err != nil {
			return err
		}
	}

	applied, err = appliedVersions(ctx, connection)
	if err != nil {
		return fmt.Errorf("read schema migration head after apply: %w", err)
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
