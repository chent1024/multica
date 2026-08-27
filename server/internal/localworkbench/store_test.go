package localworkbench

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenCreatesStableLocalIdentity(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "nested", "multica.db")

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	identity, err := store.Identity(ctx)
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	if identity.Workspace.ID != LocalWorkspaceID || identity.Operator.ID != LocalOperatorID {
		t.Fatalf("Identity() = %#v, want fixed local identifiers", identity)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.Identity(ctx)
	if err != nil {
		t.Fatalf("reopened Identity() error = %v", err)
	}
	if !reflect.DeepEqual(got, identity) {
		t.Fatalf("reopened Identity() = %#v, want %#v", got, identity)
	}
}

func TestApplyMigrationRollsBackFailedUpgrade(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	migrations := []migration{{
		version: 1,
		name:    "fails after creating table",
		statements: []string{
			"CREATE TABLE should_rollback (id TEXT PRIMARY KEY)",
			"INSERT INTO missing_table(id) VALUES ('nope')",
		},
	}}
	err = applyMigrations(t.Context(), db, migrations)
	if err == nil {
		t.Fatal("applyMigrations() error = nil, want failure")
	}

	var tableCount int
	if err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'should_rollback'",
	).Scan(&tableCount); err != nil {
		t.Fatalf("query rolled back table error = %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("rolled back table count = %d, want 0", tableCount)
	}
	var version int
	err = db.QueryRowContext(t.Context(), "SELECT version FROM schema_migrations").Scan(&version)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("migration ledger error = %v, want sql.ErrNoRows", err)
	}
}

func TestOpenAppliesOrderedUpgradeAndSQLitePolicy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := configureSQLite(ctx, db); err != nil {
		t.Fatal(err)
	}
	const earlierVersion = 10
	if err := applyMigrations(ctx, db, schemaMigrations[:earlierVersion]); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(upgrade) error=%v", err)
	}
	defer store.Close()
	var count, latest int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(version) FROM schema_migrations`).Scan(&count, &latest); err != nil {
		t.Fatal(err)
	}
	if count != len(schemaMigrations) || latest != schemaMigrations[len(schemaMigrations)-1].version {
		t.Fatalf("migration ledger count=%d latest=%d", count, latest)
	}
	var journal, synchronous string
	var foreignKeys int
	if err := store.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || synchronous != "2" || foreignKeys != 0 {
		t.Fatalf("sqlite policy journal=%q synchronous=%q foreign_keys=%d", journal, synchronous, foreignKeys)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions=%#o", got)
	}
}
