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
// Merge rules mirror the app's backup restore:
//   - manga: fields follow the higher sync_version, favorite is OR-merged and
//     favorite_at takes the earliest known value
//   - chapters: read/bookmark are OR-merged, last_page_read is furthest, and
//     Updates-page dates take the earliest known value
//   - history: last_read and read_duration take the maximum
//   - categories, links, scanlators, preferences and stores are last-write-wins
//
// Deletions are tombstones. Nullable timestamps arrive as 0 on the wire, which
// means "unknown" and never overwrites a known value.
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
	for _, es := range cs.ExcludedScanlators {
		if err := mergeExcludedScanlator(ctx, tx, accountID, rev, now, es); err != nil {
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
	var exFavoriteAt sql.NullInt64
	var exMemo []byte
	err := tx.QueryRowContext(ctx,
		`SELECT title, favorite, favorite_at, reader_flags, chapter_flags, update_strategy,
		        notes, memo, sync_version, deleted
		 FROM mangas WHERE account_id = ? AND source_id = ? AND url = ?`,
		accountID, in.SourceID, in.URL).
		Scan(&ex.Title, &ex.Favorite, &exFavoriteAt, &ex.ReaderFlags, &ex.ChapterFlags,
			&ex.UpdateStrategy, &ex.Notes, &exMemo, &ex.SyncVersion, &ex.Deleted)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`INSERT INTO mangas(account_id, source_id, url, title, favorite, favorite_at,
			    reader_flags, chapter_flags, update_strategy, notes, memo, sync_version,
			    rev, deleted, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, in.SourceID, in.URL, in.Title, in.Favorite, nullIfZero(in.FavoriteAt),
			in.ReaderFlags, in.ChapterFlags, in.UpdateStrategy, in.Notes, nullBlob(in.Memo),
			in.SyncVersion, rev, in.Deleted, now)
		return err
	case err != nil:
		return fmt.Errorf("select manga: %w", err)
	}
	ex.FavoriteAt = exFavoriteAt.Int64
	ex.Memo = exMemo

	// Higher sync_version wins. On a tie, a deletion beats a non-deletion so a
	// stale device cannot resurrect an entry another device removed.
	inWins := in.SyncVersion > ex.SyncVersion ||
		(in.SyncVersion == ex.SyncVersion && in.Deleted && !ex.Deleted)
	winner := in
	if !inWins {
		winner = ex
	}
	deleted := winner.Deleted
	// A strictly-newer re-add (favorited and not a tombstone) clears the tombstone.
	if in.Favorite && !in.Deleted && in.SyncVersion > ex.SyncVersion {
		deleted = false
	}
	favorite := ex.Favorite || in.Favorite
	favoriteAt := earliestKnown(ex.FavoriteAt, in.FavoriteAt)
	if deleted {
		// A removed entry is never in the library; otherwise a tombstone would
		// persist as the contradictory (favorite=true, deleted=true) state, which
		// lets a stale device flip it back to alive.
		favorite = false
		favoriteAt = 0
	}
	merged := Manga{
		SourceID:       in.SourceID,
		URL:            in.URL,
		Title:          winner.Title,
		Favorite:       favorite,
		FavoriteAt:     favoriteAt,
		ReaderFlags:    winner.ReaderFlags,
		ChapterFlags:   winner.ChapterFlags,
		UpdateStrategy: winner.UpdateStrategy,
		Notes:          winner.Notes,
		Memo:           mergeMemo(ex.Memo, in.Memo),
		SyncVersion:    max(ex.SyncVersion, in.SyncVersion),
		Deleted:        deleted,
	}
	// Skip no-op writes: bumping rev on an identical merge would echo the row
	// back to other devices forever (a client re-pushes what it just applied).
	if merged.Title == ex.Title && merged.Favorite == ex.Favorite &&
		merged.FavoriteAt == ex.FavoriteAt && merged.ReaderFlags == ex.ReaderFlags &&
		merged.ChapterFlags == ex.ChapterFlags && merged.UpdateStrategy == ex.UpdateStrategy &&
		merged.Notes == ex.Notes && sameBlob(merged.Memo, ex.Memo) &&
		merged.SyncVersion == ex.SyncVersion && merged.Deleted == ex.Deleted {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE mangas SET title = ?, favorite = ?, favorite_at = ?, reader_flags = ?,
		    chapter_flags = ?, update_strategy = ?, notes = ?, memo = ?, sync_version = ?,
		    rev = ?, deleted = ?, updated_at = ?
		 WHERE account_id = ? AND source_id = ? AND url = ?`,
		merged.Title, merged.Favorite, nullIfZero(merged.FavoriteAt), merged.ReaderFlags,
		merged.ChapterFlags, merged.UpdateStrategy, merged.Notes, nullBlob(merged.Memo),
		merged.SyncVersion, rev, merged.Deleted, now, accountID, in.SourceID, in.URL)
	return err
}

func mergeChapter(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in Chapter) error {
	var ex Chapter
	var exDateFetch, exDateUpload sql.NullInt64
	var exMemo []byte
	err := tx.QueryRowContext(ctx,
		`SELECT read, bookmark, last_page_read, date_fetch, date_upload, memo, sync_version
		 FROM chapters
		 WHERE account_id = ? AND manga_source_id = ? AND manga_url = ? AND url = ?`,
		accountID, in.MangaSourceID, in.MangaURL, in.URL).
		Scan(&ex.Read, &ex.Bookmark, &ex.LastPageRead, &exDateFetch, &exDateUpload,
			&exMemo, &ex.SyncVersion)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`INSERT INTO chapters(account_id, manga_source_id, manga_url, url, read, bookmark,
			    last_page_read, date_fetch, date_upload, memo, sync_version, rev, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, in.MangaSourceID, in.MangaURL, in.URL, in.Read, in.Bookmark,
			in.LastPageRead, nullIfZero(in.DateFetch), nullIfZero(in.DateUpload),
			nullBlob(in.Memo), in.SyncVersion, rev, now)
		return err
	case err != nil:
		return fmt.Errorf("select chapter: %w", err)
	}
	ex.DateFetch = exDateFetch.Int64
	ex.DateUpload = exDateUpload.Int64
	ex.Memo = exMemo

	page := max(ex.LastPageRead, in.LastPageRead)
	switch {
	case ex.Read && !in.Read:
		page = ex.LastPageRead
	case in.Read && !ex.Read:
		page = in.LastPageRead
	}
	read := ex.Read || in.Read
	bookmark := ex.Bookmark || in.Bookmark
	version := max(ex.SyncVersion, in.SyncVersion)
	// Updates-page dates: earliest known wins; 0 means unknown (old client) and
	// never overwrites. Lets a fresh device's bulk-fetch now-dates converge back
	// to the originals so its Updates list matches the old device.
	dateFetch := earliestKnown(ex.DateFetch, in.DateFetch)
	dateUpload := earliestKnown(ex.DateUpload, in.DateUpload)
	memo := mergeMemo(ex.Memo, in.Memo)
	// Skip no-op writes so applied changes don't echo back to other devices.
	if read == ex.Read && bookmark == ex.Bookmark && page == ex.LastPageRead &&
		dateFetch == ex.DateFetch && dateUpload == ex.DateUpload &&
		version == ex.SyncVersion && sameBlob(memo, ex.Memo) {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE chapters SET read = ?, bookmark = ?, last_page_read = ?, date_fetch = ?,
		    date_upload = ?, memo = ?, sync_version = ?, rev = ?, updated_at = ?
		 WHERE account_id = ? AND manga_source_id = ? AND manga_url = ? AND url = ?`,
		read, bookmark, page, nullIfZero(dateFetch), nullIfZero(dateUpload), nullBlob(memo),
		version, rev, now, accountID, in.MangaSourceID, in.MangaURL, in.URL)
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
	// A null last_read is a reset and does not propagate; only advance it.
	_, err := tx.ExecContext(ctx,
		`INSERT INTO history(account_id, manga_source_id, manga_url, chapter_url,
		    last_read, read_duration, rev, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, manga_source_id, manga_url, chapter_url) DO UPDATE SET
		    last_read = MAX(coalesce(last_read, 0), coalesce(excluded.last_read, 0)),
		    read_duration = MAX(read_duration, excluded.read_duration),
		    rev = excluded.rev, updated_at = excluded.updated_at
		 WHERE coalesce(history.last_read, 0) < coalesce(excluded.last_read, 0)
		    OR history.read_duration < excluded.read_duration`,
		accountID, in.MangaSourceID, in.MangaURL, in.ChapterURL,
		nullIfZero(in.ReadAt), in.ReadDuration, rev, now)
	return err
}

func mergeExcludedScanlator(ctx context.Context, tx *sql.Tx, accountID, rev, now int64, in ExcludedScanlator) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO excluded_scanlators(account_id, manga_source_id, manga_url, scanlator, rev, deleted, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, manga_source_id, manga_url, scanlator) DO UPDATE SET
		    rev = excluded.rev, deleted = excluded.deleted, updated_at = excluded.updated_at
		 WHERE excluded_scanlators.deleted != excluded.deleted`,
		accountID, in.MangaSourceID, in.MangaURL, in.Scanlator, rev, in.Deleted, now)
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
		`SELECT name, badge_label, signing_key, contact_website, contact_discord,
		        is_legacy, extension_list_url, deleted
		 FROM extension_stores WHERE account_id = ? AND index_url = ?`,
		accountID, in.IndexURL).
		Scan(&ex.Name, &ex.BadgeLabel, &ex.SigningKey, &ex.ContactWebsite, &ex.ContactDiscord,
			&ex.IsLegacy, &ex.ExtensionListURL, &ex.Deleted)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`INSERT INTO extension_stores(account_id, index_url, name, badge_label, signing_key,
			    contact_website, contact_discord, is_legacy, extension_list_url, rev, deleted, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, in.IndexURL, in.Name, in.BadgeLabel, in.SigningKey, in.ContactWebsite,
			in.ContactDiscord, in.IsLegacy, in.ExtensionListURL, rev, in.Deleted, now)
		return err
	case err != nil:
		return fmt.Errorf("select extension_store: %w", err)
	}

	// A sparse push (name and signing key blank) keeps whatever is stored, so an
	// older client that only knew the index url cannot blank a known store.
	name, badgeLabel, signingKey := in.Name, in.BadgeLabel, in.SigningKey
	contactWebsite, contactDiscord := in.ContactWebsite, in.ContactDiscord
	isLegacy, extensionListURL := in.IsLegacy, in.ExtensionListURL
	if name == "" && signingKey == "" && ex.Name != "" {
		name, badgeLabel, signingKey = ex.Name, ex.BadgeLabel, ex.SigningKey
		contactWebsite, contactDiscord = ex.ContactWebsite, ex.ContactDiscord
		isLegacy, extensionListURL = ex.IsLegacy, ex.ExtensionListURL
	}

	if ex.Name == name && ex.BadgeLabel == badgeLabel && ex.SigningKey == signingKey &&
		ex.ContactWebsite == contactWebsite && ex.ContactDiscord == contactDiscord &&
		ex.IsLegacy == isLegacy && ex.ExtensionListURL == extensionListURL &&
		ex.Deleted == in.Deleted {
		return nil
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE extension_stores SET name = ?, badge_label = ?, signing_key = ?,
		    contact_website = ?, contact_discord = ?, is_legacy = ?, extension_list_url = ?,
		    rev = ?, deleted = ?, updated_at = ?
		 WHERE account_id = ? AND index_url = ?`,
		name, badgeLabel, signingKey, contactWebsite, contactDiscord, isLegacy, extensionListURL,
		rev, in.Deleted, now, accountID, in.IndexURL)
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
		`SELECT source_id, url, title, favorite, coalesce(favorite_at, 0), reader_flags,
		        chapter_flags, update_strategy, notes, memo, sync_version, rev, deleted
		 FROM mangas WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull mangas: %w", err)
	}
	for rows.Next() {
		var m Manga
		var memo []byte
		if err := rows.Scan(&m.SourceID, &m.URL, &m.Title, &m.Favorite, &m.FavoriteAt,
			&m.ReaderFlags, &m.ChapterFlags, &m.UpdateStrategy, &m.Notes, &memo,
			&m.SyncVersion, &m.Rev, &m.Deleted); err != nil {
			rows.Close()
			return nil, err
		}
		m.Memo = memo
		cs.Mangas = append(cs.Mangas, m)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT manga_source_id, manga_url, url, read, bookmark, last_page_read,
		        coalesce(date_fetch, 0), coalesce(date_upload, 0), memo, sync_version, rev
		 FROM chapters WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull chapters: %w", err)
	}
	for rows.Next() {
		var c Chapter
		var memo []byte
		if err := rows.Scan(&c.MangaSourceID, &c.MangaURL, &c.URL, &c.Read, &c.Bookmark,
			&c.LastPageRead, &c.DateFetch, &c.DateUpload, &memo, &c.SyncVersion, &c.Rev); err != nil {
			rows.Close()
			return nil, err
		}
		c.Memo = memo
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
		`SELECT manga_source_id, manga_url, chapter_url, coalesce(last_read, 0), read_duration, rev
		 FROM history WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull history: %w", err)
	}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.MangaSourceID, &h.MangaURL, &h.ChapterURL, &h.ReadAt,
			&h.ReadDuration, &h.Rev); err != nil {
			rows.Close()
			return nil, err
		}
		cs.History = append(cs.History, h)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT manga_source_id, manga_url, scanlator, rev, deleted
		 FROM excluded_scanlators WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull excluded_scanlators: %w", err)
	}
	for rows.Next() {
		var es ExcludedScanlator
		if err := rows.Scan(&es.MangaSourceID, &es.MangaURL, &es.Scanlator, &es.Rev, &es.Deleted); err != nil {
			rows.Close()
			return nil, err
		}
		cs.ExcludedScanlators = append(cs.ExcludedScanlators, es)
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
		`SELECT index_url, name, badge_label, signing_key, contact_website, contact_discord,
		        is_legacy, extension_list_url, rev, deleted
		 FROM extension_stores WHERE account_id = ? AND `+revFilter, accountID, since, beforeClause)
	if err != nil {
		return nil, fmt.Errorf("pull extension_stores: %w", err)
	}
	for rows.Next() {
		var es ExtensionStore
		if err := rows.Scan(&es.IndexURL, &es.Name, &es.BadgeLabel, &es.SigningKey,
			&es.ContactWebsite, &es.ContactDiscord, &es.IsLegacy, &es.ExtensionListURL,
			&es.Rev, &es.Deleted); err != nil {
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

	now := time.Now().Unix()
	cutoff24h := now - 24*3600
	// Prune devices inactive for > 24 hours
	_, _ = s.db.ExecContext(ctx, "DELETE FROM devices WHERE account_id = ? AND last_seen < ?", accountID, cutoff24h)

	counts := []struct {
		query string
		args  []any
		dst   *int64
	}{
		{"SELECT COUNT(*) FROM mangas WHERE account_id = ? AND deleted = 0", []any{accountID}, &st.MangaCount},
		{"SELECT COUNT(*) FROM chapters WHERE account_id = ?", []any{accountID}, &st.ChapterCount},
		{"SELECT COUNT(*) FROM categories WHERE account_id = ? AND deleted = 0", []any{accountID}, &st.CategoryCount},
		{"SELECT COUNT(*) FROM history WHERE account_id = ?", []any{accountID}, &st.HistoryCount},
		{"SELECT COUNT(*) FROM preferences WHERE account_id = ? AND deleted = 0", []any{accountID}, &st.PreferenceCount},
		{"SELECT COUNT(*) FROM extension_stores WHERE account_id = ? AND deleted = 0", []any{accountID}, &st.ExtensionStoreCount},
		{"SELECT COUNT(*) FROM excluded_scanlators WHERE account_id = ? AND deleted = 0", []any{accountID}, &st.ExcludedScanlatorCount},
		{"SELECT COUNT(*) FROM devices WHERE account_id = ? AND last_seen >= ?", []any{accountID, cutoff24h}, &st.DeviceCount},
	}
	for _, c := range counts {
		if err := s.db.QueryRowContext(ctx, c.query, c.args...).Scan(c.dst); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// max returns the larger of two int64s.
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// earliestKnown returns the smaller of two epoch values, treating 0 as unknown
// (a 0 never wins over a real value).
func earliestKnown(a, b int64) int64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	default:
		if a < b {
			return a
		}
		return b
	}
}

// nullIfZero maps the wire's 0 ("unknown") to a SQL NULL.
func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// nullBlob maps an empty blob to SQL NULL.
func nullBlob(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
