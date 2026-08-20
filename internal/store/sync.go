package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ApplyChanges merges a pushed ChangeSet into the account inside one
// transaction, bumping the account revision once for the batch. It returns the
// new revision plus any changes other devices pushed after `since` (excluding
// this batch), so the caller can skip a follow-up pull.
//
// Merge rules mirror Mihon's backup restore: manga/chapter fields go to the
// higher client_version, favorite/read/bookmark are OR-merged, history uses
// max(), and categories/preferences are last-write-wins with tombstones.
func (s *Store) ApplyChanges(
	ctx context.Context,
	accountID, since int64,
	deviceID string,
	cs *ChangeSet,
) (int64, *ChangeSet, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var rev int64
	if err := tx.QueryRowContext(ctx,
		"UPDATE accounts SET rev = rev + 1 WHERE id = ? RETURNING rev", accountID).Scan(&rev); err != nil {
		return 0, nil, fmt.Errorf("bump revision: %w", err)
	}

	now := time.Now().Unix()

	if deviceID != "" {
		_, _ = tx.ExecContext(ctx,
			`INSERT INTO devices(account_id, device_id, last_seen)
			 VALUES (?, ?, ?)
			 ON CONFLICT(account_id, device_id) DO UPDATE SET last_seen = excluded.last_seen`,
			accountID, deviceID, now)
	}

	for _, m := range cs.Mangas {
		if err := mergeManga(ctx, tx, accountID, rev, now, m); err != nil {
			return 0, nil, err
		}
	}
	for _, c := range cs.Chapters {
		if err := mergeChapter(ctx, tx, accountID, rev, now, c); err != nil {
			return 0, nil, err
		}
	}
	for _, c := range cs.Categories {
		if err := mergeCategory(ctx, tx, accountID, rev, now, c); err != nil {
			return 0, nil, err
		}
	}
	for _, mc := range cs.MangaCategories {
		if err := mergeMangaCategory(ctx, tx, accountID, rev, now, mc); err != nil {
			return 0, nil, err
		}
	}
	for _, h := range cs.History {
		if err := mergeHistory(ctx, tx, accountID, rev, now, h); err != nil {
			return 0, nil, err
		}
	}
	for _, p := range cs.Preferences {
		if err := mergePreference(ctx, tx, accountID, rev, now, p); err != nil {
			return 0, nil, err
		}
	}
	for _, es := range cs.ExtensionStores {
		if err := mergeExtensionStore(ctx, tx, accountID, rev, now, es); err != nil {
			return 0, nil, err
		}
	}

	// Changes pushed by other devices since the caller's watermark, excluding
	// this batch (which all carries the new revision).
	others, err := changesInRange(ctx, tx, accountID, since, rev)
	if err != nil {
		return 0, nil, err
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("commit: %w", err)
	}
	return rev, others, nil
}

func mergeManga(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in Manga) error {
	// Normalize a missing update_strategy (old clients omitted it when it
	// matched their default, decoding to "" on the wire).
	if in.UpdateStrategy == "" {
		in.UpdateStrategy = "ALWAYS_UPDATE"
	}
	var ex Manga
	err := tx.QueryRowContext(ctx,
		`SELECT title, thumbnail_url, favorite, chapter_flags, viewer_flags, update_strategy, notes,
		        date_added, client_version, deleted
		 FROM mangas WHERE account_id = ? AND source_id = ? AND url = ?`,
		accountID, in.SourceID, in.URL).
		Scan(&ex.Title, &ex.ThumbnailURL, &ex.Favorite, &ex.ChapterFlags, &ex.ViewerFlags, &ex.UpdateStrategy,
			&ex.Notes, &ex.DateAdded, &ex.ClientVersion, &ex.Deleted)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`INSERT INTO mangas(account_id, source_id, url, title, thumbnail_url, favorite, chapter_flags,
			    viewer_flags, update_strategy, notes, date_added, client_version, rev, deleted, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, in.SourceID, in.URL, in.Title, in.ThumbnailURL, in.Favorite, in.ChapterFlags,
			in.ViewerFlags, in.UpdateStrategy, in.Notes, in.DateAdded, in.ClientVersion,
			rev, in.Deleted, now)
		return err
	case err != nil:
		return fmt.Errorf("select manga: %w", err)
	}

	winner := in
	if ex.ClientVersion > in.ClientVersion {
		winner = ex
	}
	thumbnailURL := winner.ThumbnailURL
	if thumbnailURL == "" {
		if in.ThumbnailURL != "" {
			thumbnailURL = in.ThumbnailURL
		} else {
			thumbnailURL = ex.ThumbnailURL
		}
	}
	deleted := winner.Deleted
	if winner.ClientVersion == ex.ClientVersion && ex.ClientVersion > in.ClientVersion {
		// Existing won: a deletion still sticks unless the winner re-added.
		deleted = ex.Deleted || in.Deleted
	}
	if in.Favorite && !in.Deleted {
		deleted = false
	}
	merged := Manga{
		SourceID:       in.SourceID,
		URL:            in.URL,
		Title:          winner.Title,
		ThumbnailURL:   thumbnailURL,
		Favorite:       ex.Favorite || in.Favorite,
		ChapterFlags:   winner.ChapterFlags,
		ViewerFlags:    winner.ViewerFlags,
		UpdateStrategy: winner.UpdateStrategy,
		Notes:          winner.Notes,
		DateAdded:      winner.DateAdded,
		ClientVersion:  max(ex.ClientVersion, in.ClientVersion),
		Deleted:        deleted,
	}
	// Skip no-op writes: bumping rev on an identical merge would echo the row
	// back to other devices forever (client re-pushes what it just applied).
	if merged.Title == ex.Title && merged.ThumbnailURL == ex.ThumbnailURL && merged.Favorite == ex.Favorite &&
		merged.ChapterFlags == ex.ChapterFlags && merged.ViewerFlags == ex.ViewerFlags &&
		merged.UpdateStrategy == ex.UpdateStrategy && merged.Notes == ex.Notes &&
		merged.DateAdded == ex.DateAdded && merged.ClientVersion == ex.ClientVersion &&
		merged.Deleted == ex.Deleted {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE mangas SET title = ?, thumbnail_url = ?, favorite = ?, chapter_flags = ?, viewer_flags = ?,
		    update_strategy = ?, notes = ?, date_added = ?, client_version = ?, rev = ?,
		    deleted = ?, updated_at = ?
		 WHERE account_id = ? AND source_id = ? AND url = ?`,
		merged.Title, merged.ThumbnailURL, merged.Favorite, merged.ChapterFlags, merged.ViewerFlags,
		merged.UpdateStrategy, merged.Notes, merged.DateAdded, merged.ClientVersion,
		rev, merged.Deleted, now, accountID, in.SourceID, in.URL)
	return err
}

func mergeChapter(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in Chapter) error {
	var ex Chapter
	err := tx.QueryRowContext(ctx,
		`SELECT read, bookmark, last_page_read, client_version
		 FROM chapters
		 WHERE account_id = ? AND manga_source_id = ? AND manga_url = ? AND url = ?`,
		accountID, in.MangaSourceID, in.MangaURL, in.URL).
		Scan(&ex.Read, &ex.Bookmark, &ex.LastPageRead, &ex.ClientVersion)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`INSERT INTO chapters(account_id, manga_source_id, manga_url, url, read, bookmark,
			    last_page_read, client_version, rev, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, in.MangaSourceID, in.MangaURL, in.URL, in.Read, in.Bookmark,
			in.LastPageRead, in.ClientVersion, rev, now)
		return err
	case err != nil:
		return fmt.Errorf("select chapter: %w", err)
	}

	page := max(ex.LastPageRead, in.LastPageRead)
	switch {
	case ex.Read && !in.Read:
		page = ex.LastPageRead
	case in.Read && !ex.Read:
		page = in.LastPageRead
	}
	read := ex.Read || in.Read
	bookmark := ex.Bookmark || in.Bookmark
	version := max(ex.ClientVersion, in.ClientVersion)
	// Skip no-op writes so applied changes don't echo back to other devices.
	if read == ex.Read && bookmark == ex.Bookmark && page == ex.LastPageRead &&
		version == ex.ClientVersion {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE chapters SET read = ?, bookmark = ?, last_page_read = ?, client_version = ?,
		    rev = ?, updated_at = ?
		 WHERE account_id = ? AND manga_source_id = ? AND manga_url = ? AND url = ?`,
		read, bookmark, page, version, rev, now,
		accountID, in.MangaSourceID, in.MangaURL, in.URL)
	return err
}

func mergeCategory(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in Category) error {
	// Skip no-op writes (identical content) so applied changes don't echo.
	_, err := tx.ExecContext(ctx,
		`INSERT INTO categories(account_id, name, sort_order, flags, rev, deleted, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, name) DO UPDATE SET
		    sort_order = excluded.sort_order, flags = excluded.flags,
		    rev = excluded.rev, deleted = excluded.deleted, updated_at = excluded.updated_at
		 WHERE categories.sort_order != excluded.sort_order
		    OR categories.flags != excluded.flags
		    OR categories.deleted != excluded.deleted`,
		accountID, in.Name, in.Order, in.Flags, rev, in.Deleted, now)
	return err
}

func mergeMangaCategory(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in MangaCategory) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO manga_categories(account_id, manga_source_id, manga_url, category, rev, deleted, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, manga_source_id, manga_url, category) DO UPDATE SET
		    rev = excluded.rev, deleted = excluded.deleted, updated_at = excluded.updated_at
		 WHERE manga_categories.deleted != excluded.deleted`,
		accountID, in.MangaSourceID, in.MangaURL, in.Category, rev, in.Deleted, now)
	return err
}

func mergeHistory(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in HistoryEntry) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO history(account_id, manga_source_id, manga_url, chapter_url,
		    last_read, read_duration, rev, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, manga_source_id, manga_url, chapter_url) DO UPDATE SET
		    last_read = MAX(last_read, excluded.last_read),
		    read_duration = MAX(read_duration, excluded.read_duration),
		    rev = excluded.rev, updated_at = excluded.updated_at
		 WHERE history.last_read < excluded.last_read
		    OR history.read_duration < excluded.read_duration`,
		accountID, in.MangaSourceID, in.MangaURL, in.ChapterURL,
		in.LastRead, in.ReadDuration, rev, now)
	return err
}

func mergePreference(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in Preference) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO preferences(account_id, key, type, value, rev, deleted, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, key) DO UPDATE SET
		    type = excluded.type, value = excluded.value, rev = excluded.rev,
		    deleted = excluded.deleted, updated_at = excluded.updated_at
		 WHERE preferences.type != excluded.type
		    OR preferences.value != excluded.value
		    OR preferences.deleted != excluded.deleted`,
		accountID, in.Key, in.Type, string(in.Value), rev, in.Deleted, now)
	return err
}

func mergeExtensionStore(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in ExtensionStore) error {
	var ex ExtensionStore
	err := tx.QueryRowContext(ctx,
		`SELECT name, badge_label, signing_key, deleted
		 FROM extension_stores WHERE account_id = ? AND index_url = ?`,
		accountID, in.IndexURL).
		Scan(&ex.Name, &ex.BadgeLabel, &ex.SigningKey, &ex.Deleted)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`INSERT INTO extension_stores(account_id, index_url, name, badge_label, signing_key,
			    rev, deleted, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, in.IndexURL, in.Name, in.BadgeLabel, in.SigningKey, rev, in.Deleted, now)
		return err
	case err != nil:
		return fmt.Errorf("select extension_store: %w", err)
	}

	name := in.Name
	badgeLabel := in.BadgeLabel
	signingKey := in.SigningKey
	if name == "" && signingKey == "" && ex.Name != "" {
		name = ex.Name
		badgeLabel = ex.BadgeLabel
		signingKey = ex.SigningKey
	}

	if ex.Name == name && ex.BadgeLabel == badgeLabel &&
		ex.SigningKey == signingKey && ex.Deleted == in.Deleted {
		return nil
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE extension_stores SET name = ?, badge_label = ?, signing_key = ?, rev = ?,
		    deleted = ?, updated_at = ?
		 WHERE account_id = ? AND index_url = ?`,
		name, badgeLabel, signingKey, rev, in.Deleted, now, accountID, in.IndexURL)
	return err
}

// ChangesSince returns all rows with rev > since plus the account's current
// revision (the high-water mark the client should store).
func (s *Store) ChangesSince(ctx context.Context, accountID, since int64) (*ChangeSet, int64, error) {
	rev, err := s.currentRev(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	cs, err := changesInRange(ctx, s.db, accountID, since, 0)
	if err != nil {
		return nil, 0, err
	}
	return cs, rev, nil
}

// queryer abstracts *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// changesInRange returns rows with since < rev, and rev < before when before
// is non-zero (used to exclude the caller's own push batch).
func changesInRange(ctx context.Context, q queryer, accountID, since, before int64) (*ChangeSet, error) {
	// before = 0 means unbounded; revisions are positive, so use max int64.
	beforeClause := before
	if beforeClause == 0 {
		beforeClause = int64(^uint64(0) >> 1)
	}
	revFilter := "rev > ? AND rev < ?"

	cs := &ChangeSet{}

	rows, err := q.QueryContext(ctx,
		`SELECT source_id, url, title, thumbnail_url, favorite, chapter_flags, viewer_flags, update_strategy,
		        notes, date_added, client_version, rev, deleted
		 FROM mangas WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull mangas: %w", err)
	}
	for rows.Next() {
		var m Manga
		if err := rows.Scan(&m.SourceID, &m.URL, &m.Title, &m.ThumbnailURL, &m.Favorite, &m.ChapterFlags,
			&m.ViewerFlags, &m.UpdateStrategy, &m.Notes, &m.DateAdded, &m.ClientVersion,
			&m.Rev, &m.Deleted); err != nil {
			rows.Close()
			return nil, err
		}
		cs.Mangas = append(cs.Mangas, m)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT manga_source_id, manga_url, url, read, bookmark, last_page_read, client_version, rev
		 FROM chapters WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull chapters: %w", err)
	}
	for rows.Next() {
		var c Chapter
		if err := rows.Scan(&c.MangaSourceID, &c.MangaURL, &c.URL, &c.Read, &c.Bookmark,
			&c.LastPageRead, &c.ClientVersion, &c.Rev); err != nil {
			rows.Close()
			return nil, err
		}
		cs.Chapters = append(cs.Chapters, c)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT name, sort_order, flags, rev, deleted
		 FROM categories WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull categories: %w", err)
	}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.Name, &c.Order, &c.Flags, &c.Rev, &c.Deleted); err != nil {
			rows.Close()
			return nil, err
		}
		cs.Categories = append(cs.Categories, c)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT manga_source_id, manga_url, category, rev, deleted
		 FROM manga_categories WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull manga_categories: %w", err)
	}
	for rows.Next() {
		var mc MangaCategory
		if err := rows.Scan(&mc.MangaSourceID, &mc.MangaURL, &mc.Category, &mc.Rev, &mc.Deleted); err != nil {
			rows.Close()
			return nil, err
		}
		cs.MangaCategories = append(cs.MangaCategories, mc)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT manga_source_id, manga_url, chapter_url, last_read, read_duration, rev
		 FROM history WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull history: %w", err)
	}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.MangaSourceID, &h.MangaURL, &h.ChapterURL, &h.LastRead,
			&h.ReadDuration, &h.Rev); err != nil {
			rows.Close()
			return nil, err
		}
		cs.History = append(cs.History, h)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT key, type, value, rev, deleted
		 FROM preferences WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull preferences: %w", err)
	}
	for rows.Next() {
		var p Preference
		var value string
		if err := rows.Scan(&p.Key, &p.Type, &value, &p.Rev, &p.Deleted); err != nil {
			rows.Close()
			return nil, err
		}
		p.Value = []byte(value)
		cs.Preferences = append(cs.Preferences, p)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT index_url, name, badge_label, signing_key, rev, deleted
		 FROM extension_stores WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull extension_stores: %w", err)
	}
	for rows.Next() {
		var es ExtensionStore
		if err := rows.Scan(&es.IndexURL, &es.Name, &es.BadgeLabel, &es.SigningKey, &es.Rev, &es.Deleted); err != nil {
			rows.Close()
			return nil, err
		}
		cs.ExtensionStores = append(cs.ExtensionStores, es)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cs, nil
}

func (s *Store) currentRev(ctx context.Context, accountID int64) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, "SELECT rev FROM accounts WHERE id = ?", accountID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrAccountNotFound
	}
	return rev, err
}

func (s *Store) Status(ctx context.Context, accountID int64) (*Status, error) {
	st := &Status{}
	err := s.db.QueryRowContext(ctx,
		"SELECT rev, created_at FROM accounts WHERE id = ?", accountID).
		Scan(&st.Rev, &st.AccountCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}

	counts := []struct {
		query string
		dst   *int64
	}{
		{"SELECT COUNT(*) FROM mangas WHERE account_id = ? AND deleted = 0", &st.MangaCount},
		{"SELECT COUNT(*) FROM chapters WHERE account_id = ?", &st.ChapterCount},
		{"SELECT COUNT(*) FROM categories WHERE account_id = ? AND deleted = 0", &st.CategoryCount},
		{"SELECT COUNT(*) FROM history WHERE account_id = ?", &st.HistoryCount},
		{"SELECT COUNT(*) FROM preferences WHERE account_id = ? AND deleted = 0", &st.PreferenceCount},
		{"SELECT COUNT(*) FROM extension_stores WHERE account_id = ? AND deleted = 0", &st.ExtensionStoreCount},
		{"SELECT COUNT(*) FROM devices WHERE account_id = ?", &st.DeviceCount},
	}
	for _, c := range counts {
		if err := s.db.QueryRowContext(ctx, c.query, accountID).Scan(c.dst); err != nil {
			return nil, err
		}
	}
	return st, nil
}
