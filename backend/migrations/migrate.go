package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed *.up.sql *.down.sql
var files embed.FS

const advisoryLockID int64 = 0x57534B464C4F57 // "WSKFLOW"

type Applied struct {
	Version   string
	Checksum  string
	AppliedAt time.Time
}

func Up(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("database is required")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Close()

	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = connection.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockID)
	}()

	if _, err := connection.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version text PRIMARY KEY,
  checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := applyFile(ctx, connection, name); err != nil {
			return err
		}
	}
	return nil
}

func AppliedVersions(ctx context.Context, database *sql.DB) ([]Applied, error) {
	rows, err := database.QueryContext(ctx, `
SELECT version, checksum, applied_at
FROM schema_migrations
ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Applied
	for rows.Next() {
		var applied Applied
		if err := rows.Scan(&applied.Version, &applied.Checksum, &applied.AppliedAt); err != nil {
			return nil, err
		}
		result = append(result, applied)
	}
	return result, rows.Err()
}

func migrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			result = append(result, entry.Name())
		}
	}
	sort.Strings(result)
	return result, nil
}

func applyFile(ctx context.Context, connection *sql.Conn, name string) error {
	contents, err := files.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	version := strings.TrimSuffix(name, ".up.sql")
	digest := sha256.Sum256(contents)
	checksum := hex.EncodeToString(digest[:])

	var existing string
	err = connection.QueryRowContext(ctx,
		"SELECT checksum FROM schema_migrations WHERE version = $1", version,
	).Scan(&existing)
	if err == nil {
		if existing != checksum {
			return fmt.Errorf("migration %s checksum changed after it was applied", version)
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
		"INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)", version, checksum,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}
