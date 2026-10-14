// Package store implements the SQLite persistence layer for the sync server.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and runs
// schema migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// Support concurrent readers with WAL mode while busy_timeout handles concurrent writes.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key_hash   TEXT NOT NULL UNIQUE,
    label      TEXT NOT NULL DEFAULT '',
    rev        INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    device_id  TEXT NOT NULL,
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (account_id, device_id)
);

CREATE TABLE IF NOT EXISTS mangas (
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source_id       INTEGER NOT NULL,
    url             TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    favorite        INTEGER NOT NULL DEFAULT 1,
    favorite_at     INTEGER,
    reader_flags    INTEGER NOT NULL DEFAULT 0,
    chapter_flags   INTEGER NOT NULL DEFAULT 0,
    update_strategy TEXT NOT NULL DEFAULT 'ALWAYS_UPDATE',
    notes           TEXT NOT NULL DEFAULT '',
    memo            BLOB,
    sync_version    INTEGER NOT NULL DEFAULT 0,
    rev             INTEGER NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, source_id, url)
);
CREATE INDEX IF NOT EXISTS idx_mangas_rev ON mangas(account_id, rev);
CREATE INDEX IF NOT EXISTS idx_mangas_active ON mangas(account_id) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS chapters (
    account_id      INTEGER NOT NULL,
    manga_source_id INTEGER NOT NULL,
    manga_url       TEXT NOT NULL,
    url             TEXT NOT NULL,
    read            INTEGER NOT NULL DEFAULT 0,
    bookmark        INTEGER NOT NULL DEFAULT 0,
    last_page_read  INTEGER NOT NULL DEFAULT 0,
    date_fetch      INTEGER,
    date_upload     INTEGER,
    memo            BLOB,
    sync_version    INTEGER NOT NULL DEFAULT 0,
    rev             INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, manga_source_id, manga_url, url)
);
CREATE INDEX IF NOT EXISTS idx_chapters_rev ON chapters(account_id, rev);

CREATE TABLE IF NOT EXISTS categories (
    account_id INTEGER NOT NULL,
    name       TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    flags      INTEGER NOT NULL DEFAULT 0,
    rev        INTEGER NOT NULL,
    deleted    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (account_id, name)
);
CREATE INDEX IF NOT EXISTS idx_categories_rev ON categories(account_id, rev);
CREATE INDEX IF NOT EXISTS idx_categories_active ON categories(account_id) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS manga_categories (
    account_id      INTEGER NOT NULL,
    manga_source_id INTEGER NOT NULL,
    manga_url       TEXT NOT NULL,
    category        TEXT NOT NULL,
    rev             INTEGER NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, manga_source_id, manga_url, category)
);
CREATE INDEX IF NOT EXISTS idx_manga_categories_rev ON manga_categories(account_id, rev);
CREATE INDEX IF NOT EXISTS idx_manga_categories_active ON manga_categories(account_id) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS history (
    account_id      INTEGER NOT NULL,
    manga_source_id INTEGER NOT NULL,
    manga_url       TEXT NOT NULL,
    chapter_url     TEXT NOT NULL,
    last_read       INTEGER,
    read_duration   INTEGER NOT NULL DEFAULT 0,
    rev             INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, manga_source_id, manga_url, chapter_url)
);
CREATE INDEX IF NOT EXISTS idx_history_rev ON history(account_id, rev);

CREATE TABLE IF NOT EXISTS excluded_scanlators (
    account_id      INTEGER NOT NULL,
    manga_source_id INTEGER NOT NULL,
    manga_url       TEXT NOT NULL,
    scanlator       TEXT NOT NULL,
    rev             INTEGER NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, manga_source_id, manga_url, scanlator)
);
CREATE INDEX IF NOT EXISTS idx_excluded_scanlators_rev ON excluded_scanlators(account_id, rev);
CREATE INDEX IF NOT EXISTS idx_excluded_scanlators_active ON excluded_scanlators(account_id) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS preferences (
    account_id INTEGER NOT NULL,
    key        TEXT NOT NULL,
    type       TEXT NOT NULL,
    value      TEXT NOT NULL,
    rev        INTEGER NOT NULL,
    deleted    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (account_id, key)
);
CREATE INDEX IF NOT EXISTS idx_preferences_rev ON preferences(account_id, rev);
CREATE INDEX IF NOT EXISTS idx_preferences_active ON preferences(account_id) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS extension_stores (
    account_id          INTEGER NOT NULL,
    index_url           TEXT NOT NULL,
    name                TEXT NOT NULL DEFAULT '',
    badge_label         TEXT NOT NULL DEFAULT '',
    signing_key         TEXT NOT NULL DEFAULT '',
    contact_website     TEXT NOT NULL DEFAULT '',
    contact_discord     TEXT NOT NULL DEFAULT '',
    is_legacy           INTEGER NOT NULL DEFAULT 0,
    extension_list_url  TEXT NOT NULL DEFAULT '',
    rev                 INTEGER NOT NULL,
    deleted             INTEGER NOT NULL DEFAULT 0,
    updated_at          INTEGER NOT NULL,
    PRIMARY KEY (account_id, index_url)
);
CREATE INDEX IF NOT EXISTS idx_extension_stores_rev ON extension_stores(account_id, rev);
CREATE INDEX IF NOT EXISTS idx_extension_stores_active ON extension_stores(account_id) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS registration_tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    label      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Columns added after the first release of a table are added here so an
	// existing database picks them up; a fresh one already has them from schema.
	alterations := []string{
		`ALTER TABLE mangas ADD COLUMN favorite_at INTEGER`,
		`ALTER TABLE mangas ADD COLUMN memo BLOB`,
		`ALTER TABLE mangas ADD COLUMN sync_version INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE chapters ADD COLUMN date_fetch INTEGER`,
		`ALTER TABLE chapters ADD COLUMN date_upload INTEGER`,
		`ALTER TABLE chapters ADD COLUMN memo BLOB`,
		`ALTER TABLE chapters ADD COLUMN sync_version INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE extension_stores ADD COLUMN contact_website TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE extension_stores ADD COLUMN contact_discord TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE extension_stores ADD COLUMN is_legacy INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE extension_stores ADD COLUMN extension_list_url TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range alterations {
		if _, err := s.db.Exec(stmt); err != nil {
			// Ignore "duplicate column" on re-run; surface anything else.
			msg := err.Error()
			if !(strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")) {
				return fmt.Errorf("migrate: %s: %w", stmt, err)
			}
		}
	}
	return nil
}

// GC removes tombstoned rows whose last update is older than the retention
// window. Tombstones must outlive every device's sync interval so deletions
// can propagate.
func (s *Store) GC(ctx context.Context, retention time.Duration) error {
	cutoff := time.Now().Add(-retention).Unix()
	tables := []string{
		"mangas", "categories", "manga_categories", "excluded_scanlators",
		"preferences", "extension_stores",
	}
	for _, t := range tables {
		if _, err := s.db.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE deleted = 1 AND updated_at < ?", t), cutoff); err != nil {
			return fmt.Errorf("gc %s: %w", t, err)
		}
	}
	if err := s.GCInviteTokens(ctx, time.Now().Unix()); err != nil {
		return err
	}
	return nil
}

// TouchDevice records or refreshes the last-seen timestamp for a device.
func (s *Store) TouchDevice(ctx context.Context, accountID int64, deviceID string) error {
	if deviceID == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO devices(account_id, device_id, last_seen)
		 VALUES (?, ?, ?)
		 ON CONFLICT(account_id, device_id) DO UPDATE SET last_seen = excluded.last_seen`,
		accountID, deviceID, now)
	return err
}

// ActiveEntityCount returns the number of non-tombstoned entities held by the
// account, used to enforce the per-account cap.
func (s *Store) ActiveEntityCount(ctx context.Context, accountID int64) (int64, error) {
	var total int64
	for _, q := range []string{
		`SELECT COUNT(*) FROM mangas WHERE account_id = ? AND deleted = 0`,
		`SELECT COUNT(*) FROM chapters WHERE account_id = ?`,
		`SELECT COUNT(*) FROM categories WHERE account_id = ? AND deleted = 0`,
		`SELECT COUNT(*) FROM manga_categories WHERE account_id = ? AND deleted = 0`,
		`SELECT COUNT(*) FROM history WHERE account_id = ?`,
		`SELECT COUNT(*) FROM excluded_scanlators WHERE account_id = ? AND deleted = 0`,
		`SELECT COUNT(*) FROM preferences WHERE account_id = ? AND deleted = 0`,
		`SELECT COUNT(*) FROM extension_stores WHERE account_id = ? AND deleted = 0`,
	} {
		var n int64
		if err := s.db.QueryRowContext(ctx, q, accountID).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
