// Package store persists libraries, invites and synced records.
//
// The server is a dumb, per-library log with last-writer-wins semantics: it never
// parses payloads, it only compares hybrid logical clock markers.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidInvite = errors.New("invalid or expired invite")
)

const schema = `
CREATE TABLE IF NOT EXISTS libraries (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    label      TEXT NOT NULL,
    key_hash   TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS invites (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    label       TEXT NOT NULL,
    code_hash   TEXT NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER,
    uses_left   INTEGER NOT NULL,
    redeemed_at INTEGER
);

CREATE TABLE IF NOT EXISTS records (
    library_id  INTEGER NOT NULL,
    record_type TEXT NOT NULL,
    record_key  TEXT NOT NULL,
    deleted     INTEGER NOT NULL DEFAULT 0,
    payload     TEXT,
    wall_time   INTEGER NOT NULL,
    counter     INTEGER NOT NULL,
    device_id   TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    PRIMARY KEY (library_id, record_type, record_key)
);

CREATE INDEX IF NOT EXISTS records_library_seq ON records (library_id, seq);
`

// Marker is a hybrid logical clock reading, matching the Android client's wire format.
type Marker struct {
	WallTime int64  `json:"wallTimeMillis"`
	Counter  int64  `json:"counter"`
	DeviceID string `json:"deviceId"`
}

// Less reports whether m loses against other.
func (m Marker) Less(other Marker) bool {
	if m.WallTime != other.WallTime {
		return m.WallTime < other.WallTime
	}
	if m.Counter != other.Counter {
		return m.Counter < other.Counter
	}
	return m.DeviceID < other.DeviceID
}

// Record is one synced record as it travels on the wire.
type Record struct {
	Type    string  `json:"type"`
	Key     string  `json:"key"`
	Marker  Marker  `json:"marker"`
	Deleted bool    `json:"deleted"`
	Payload *string `json:"payload,omitempty"`
}

// PushResult tells a client what happened to one pushed record. A rejected record
// already has a newer value on the server, which the following pull brings down.
type PushResult struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Accepted bool   `json:"accepted"`
	Marker   Marker `json:"marker"`
}

// Library is one tenant: an API key and everything synced under it.
type Library struct {
	ID        int64
	Label     string
	CreatedAt int64
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite accepts a single writer; serializing here keeps transactions simple and
	// the workload is small by design.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// CreateLibrary makes a new library and returns its API key, shown exactly once.
func (s *Store) CreateLibrary(ctx context.Context, label string) (string, Library, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Library{}, err
	}
	defer tx.Rollback()

	key, lib, err := createLibrary(ctx, tx, label)
	if err != nil {
		return "", Library{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", Library{}, err
	}
	return key, lib, nil
}

// CreateInvite mints an invite code; ttl of zero means it never expires, a negative
// ttl means it is already expired.
func (s *Store) CreateInvite(ctx context.Context, label string, uses int, ttl time.Duration) (string, error) {
	if uses < 1 {
		uses = 1
	}
	code := "inv_" + newToken(18)
	now := time.Now()
	var expires any
	if ttl != 0 {
		expires = now.Add(ttl).UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invites (label, code_hash, created_at, expires_at, uses_left)
		 VALUES (?, ?, ?, ?, ?)`,
		label, hashToken(code), now.UnixMilli(), expires, uses,
	)
	if err != nil {
		return "", err
	}
	return code, nil
}

// RedeemInvite turns an invite code into a new library.
func (s *Store) RedeemInvite(ctx context.Context, code, label string) (string, Library, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Library{}, err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM invites
		 WHERE code_hash = ? AND uses_left > 0 AND (expires_at IS NULL OR expires_at > ?)`,
		hashToken(code), now,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Library{}, ErrInvalidInvite
	}
	if err != nil {
		return "", Library{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE invites SET uses_left = uses_left - 1, redeemed_at = ? WHERE id = ?`, now, id,
	); err != nil {
		return "", Library{}, err
	}

	key, lib, err := createLibrary(ctx, tx, label)
	if err != nil {
		return "", Library{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", Library{}, err
	}
	return key, lib, nil
}

// Authenticate resolves an API key to its library.
func (s *Store) Authenticate(ctx context.Context, key string) (Library, error) {
	var lib Library
	err := s.db.QueryRowContext(ctx,
		`SELECT id, label, created_at FROM libraries WHERE key_hash = ?`, hashToken(key),
	).Scan(&lib.ID, &lib.Label, &lib.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Library{}, ErrNotFound
	}
	if err != nil {
		return Library{}, err
	}
	return lib, nil
}

// Push applies records with last-writer-wins and returns the verdict per record plus
// the highest sequence written, so callers can wake up live connections.
func (s *Store) Push(ctx context.Context, libraryID int64, records []Record) ([]PushResult, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	var nextSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM records WHERE library_id = ?`, libraryID,
	).Scan(&nextSeq); err != nil {
		return nil, 0, err
	}

	results := make([]PushResult, 0, len(records))
	for _, rec := range records {
		stored, exists, err := storedMarker(ctx, tx, libraryID, rec.Type, rec.Key)
		if err != nil {
			return nil, 0, err
		}
		if exists && !stored.Less(rec.Marker) {
			results = append(results, PushResult{Type: rec.Type, Key: rec.Key, Accepted: false, Marker: stored})
			continue
		}

		nextSeq++
		if exists {
			_, err = tx.ExecContext(ctx,
				`UPDATE records
				 SET deleted = ?, payload = ?, wall_time = ?, counter = ?, device_id = ?, seq = ?
				 WHERE library_id = ? AND record_type = ? AND record_key = ?`,
				rec.Deleted, rec.Payload, rec.Marker.WallTime, rec.Marker.Counter, rec.Marker.DeviceID,
				nextSeq, libraryID, rec.Type, rec.Key,
			)
		} else {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO records (library_id, record_type, record_key, deleted, payload, wall_time, counter, device_id, seq)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				libraryID, rec.Type, rec.Key, rec.Deleted, rec.Payload,
				rec.Marker.WallTime, rec.Marker.Counter, rec.Marker.DeviceID, nextSeq,
			)
		}
		if err != nil {
			return nil, 0, err
		}
		results = append(results, PushResult{Type: rec.Type, Key: rec.Key, Accepted: true, Marker: rec.Marker})
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return results, nextSeq, nil
}

// Pull returns records after since in sequence order, capped at limit. When the page
// is empty the watermark advances to the library's head, so clients stop asking.
func (s *Store) Pull(ctx context.Context, libraryID, since int64, limit int) ([]Record, int64, bool, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT record_type, record_key, deleted, payload, wall_time, counter, device_id, seq
		 FROM records
		 WHERE library_id = ? AND seq > ?
		 ORDER BY seq
		 LIMIT ?`,
		libraryID, since, limit,
	)
	if err != nil {
		return nil, since, false, err
	}
	defer rows.Close()

	records := make([]Record, 0, limit)
	watermark := since
	for rows.Next() {
		var (
			rec     Record
			deleted int64
			payload sql.NullString
			seq     int64
		)
		if err := rows.Scan(
			&rec.Type, &rec.Key, &deleted, &payload,
			&rec.Marker.WallTime, &rec.Marker.Counter, &rec.Marker.DeviceID, &seq,
		); err != nil {
			return nil, since, false, err
		}
		rec.Deleted = deleted != 0
		if payload.Valid {
			p := payload.String
			rec.Payload = &p
		}
		watermark = seq
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, since, false, err
	}

	if len(records) == 0 {
		var head int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) FROM records WHERE library_id = ?`, libraryID,
		).Scan(&head); err != nil {
			return nil, since, false, err
		}
		if head > watermark {
			watermark = head
		}
	}
	return records, watermark, len(records) == limit, nil
}

// RecordCount is how many live records the library holds.
func (s *Store) RecordCount(ctx context.Context, libraryID int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM records WHERE library_id = ? AND deleted = 0`, libraryID,
	).Scan(&count)
	return count, err
}

// LibraryCount is used by the dashboard to know whether setup has happened yet.
func (s *Store) LibraryCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraries`).Scan(&count)
	return count, err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func createLibrary(ctx context.Context, db execer, label string) (string, Library, error) {
	key := "mhk_" + newToken(24)
	now := time.Now().UnixMilli()
	res, err := db.ExecContext(ctx,
		`INSERT INTO libraries (label, key_hash, created_at) VALUES (?, ?, ?)`,
		label, hashToken(key), now,
	)
	if err != nil {
		return "", Library{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", Library{}, err
	}
	lib := Library{ID: id, Label: label, CreatedAt: now}
	if lib.Label == "" {
		lib.Label = fmt.Sprintf("key-%d", id)
		if _, err := db.ExecContext(ctx, `UPDATE libraries SET label = ? WHERE id = ?`, lib.Label, id); err != nil {
			return "", Library{}, err
		}
	}
	return key, lib, nil
}

func storedMarker(ctx context.Context, db execer, libraryID int64, recordType, key string) (Marker, bool, error) {
	var marker Marker
	err := db.QueryRowContext(ctx,
		`SELECT wall_time, counter, device_id FROM records
		 WHERE library_id = ? AND record_type = ? AND record_key = ?`,
		libraryID, recordType, key,
	).Scan(&marker.WallTime, &marker.Counter, &marker.DeviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	return marker, true, nil
}

func newToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken is what actually goes in the database: keys and invites are only ever
// shown once, in the response or terminal that created them.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
