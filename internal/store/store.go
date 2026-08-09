// Package store implements the SQLite persistence layer for the sync server.
package store

import (
	"context"
	"database/sql"
	"fmt"
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
	// Single writer: one connection avoids SQLITE_BUSY under concurrent
	// requests and is plenty for a sync workload.
	db.SetMaxOpenConns(1)

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

CREATE TABLE IF NOT EXISTS mangas (
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source_id       INTEGER NOT NULL,
    url             TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    favorite        INTEGER NOT NULL DEFAULT 1,
    chapter_flags   INTEGER NOT NULL DEFAULT 0,
    viewer_flags    INTEGER NOT NULL DEFAULT 0,
    update_strategy TEXT NOT NULL DEFAULT 'ALWAYS_UPDATE',
    notes           TEXT NOT NULL DEFAULT '',
    date_added      INTEGER NOT NULL DEFAULT 0,
    client_version  INTEGER NOT NULL DEFAULT 0,
    rev             INTEGER NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, source_id, url)
);
CREATE INDEX IF NOT EXISTS idx_mangas_rev ON mangas(account_id, rev);

CREATE TABLE IF NOT EXISTS chapters (
    account_id      INTEGER NOT NULL,
    manga_source_id INTEGER NOT NULL,
    manga_url       TEXT NOT NULL,
    url             TEXT NOT NULL,
    read            INTEGER NOT NULL DEFAULT 0,
    bookmark        INTEGER NOT NULL DEFAULT 0,
    last_page_read  INTEGER NOT NULL DEFAULT 0,
    client_version  INTEGER NOT NULL DEFAULT 0,
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

CREATE TABLE IF NOT EXISTS history (
    account_id      INTEGER NOT NULL,
    manga_source_id INTEGER NOT NULL,
    manga_url       TEXT NOT NULL,
    chapter_url     TEXT NOT NULL,
    last_read       INTEGER NOT NULL DEFAULT 0,
    read_duration   INTEGER NOT NULL DEFAULT 0,
    rev             INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, manga_source_id, manga_url, chapter_url)
);
CREATE INDEX IF NOT EXISTS idx_history_rev ON history(account_id, rev);

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
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// GC removes tombstoned rows whose last update is older than the retention
// window. Tombstones must outlive every device's sync interval so deletions
// can propagate.
func (s *Store) GC(ctx context.Context, retention time.Duration) error {
	cutoff := time.Now().Add(-retention).Unix()
	tables := []string{"mangas", "categories", "manga_categories", "preferences"}
	for _, t := range tables {
		if _, err := s.db.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE deleted = 1 AND updated_at < ?", t), cutoff); err != nil {
			return fmt.Errorf("gc %s: %w", t, err)
		}
	}
	return nil
}
