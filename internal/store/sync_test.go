package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	if err := st.CreateAccount(ctx, "testhash", "test"); err != nil {
		t.Fatalf("create account: %v", err)
	}
	acc, err := st.AccountByKeyHash(ctx, "testhash")
	if err != nil {
		t.Fatalf("lookup account: %v", err)
	}
	return st, acc.ID
}

func mangaAt(t *testing.T, st *Store, accountID int64, url string) Manga {
	t.Helper()
	cs, rev, err := st.ChangesSince(context.Background(), accountID, 0)
	if err != nil {
		t.Fatalf("changes since: %v", err)
	}
	_ = rev
	for _, m := range cs.Mangas {
		if m.URL == url {
			return m
		}
	}
	t.Fatalf("manga %q not found", url)
	return Manga{}
}

func TestMangaVersionWins(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	push := func(title string, version int64) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{Mangas: []Manga{{
			SourceID: 1, URL: "/m/a", Title: title, ClientVersion: version,
		}}})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	push("new", 5)
	push("stale", 3)
	if got := mangaAt(t, st, accID, "/m/a").Title; got != "new" {
		t.Fatalf("older version overwrote title: got %q", got)
	}
	push("newest", 7)
	if got := mangaAt(t, st, accID, "/m/a").Title; got != "newest" {
		t.Fatalf("newer version did not win: got %q", got)
	}
}

func TestMangaFavoriteORMerge(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", Favorite: true, ClientVersion: 5,
	}}}); err != nil {
		t.Fatal(err)
	}
	// A newer version without favorite must not unset it.
	if _, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", Favorite: false, ClientVersion: 9,
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := mangaAt(t, st, accID, "/m/a").Favorite; !got {
		t.Fatal("favorite was lost in merge")
	}
}

func TestMangaDeleteAndReadd(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	apply := func(version int64, deleted bool) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{Mangas: []Manga{{
			SourceID: 1, URL: "/m/a", ClientVersion: version, Deleted: deleted,
		}}})
		if err != nil {
			t.Fatal(err)
		}
	}

	apply(5, false)
	apply(8, true)
	if !mangaAt(t, st, accID, "/m/a").Deleted {
		t.Fatal("delete did not stick")
	}
	apply(9, false)
	if mangaAt(t, st, accID, "/m/a").Deleted {
		t.Fatal("newer re-add did not clear tombstone")
	}
}

func TestChapterReadBookmarkORMerge(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	push := func(read, bookmark bool, page, version int64) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{Chapters: []Chapter{{
			MangaSourceID: 1, MangaURL: "/m/a", URL: "/c/1",
			Read: read, Bookmark: bookmark, LastPageRead: page, ClientVersion: version,
		}}})
		if err != nil {
			t.Fatal(err)
		}
	}

	push(true, false, 10, 3)
	push(false, true, 2, 4)

	cs, _, err := st.ChangesSince(ctx, accID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(cs.Chapters))
	}
	c := cs.Chapters[0]
	if !c.Read {
		t.Fatal("read flag lost")
	}
	if !c.Bookmark {
		t.Fatal("bookmark lost")
	}
	// Existing record was the read one: its page wins.
	if c.LastPageRead != 10 {
		t.Fatalf("last_page_read = %d, want 10", c.LastPageRead)
	}
}

func TestHistoryMaxMerge(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	push := func(lastRead, duration int64) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{History: []HistoryEntry{{
			MangaSourceID: 1, MangaURL: "/m/a", ChapterURL: "/c/1",
			LastRead: lastRead, ReadDuration: duration,
		}}})
		if err != nil {
			t.Fatal(err)
		}
	}

	push(1000, 60)
	push(500, 90)

	cs, _, _ := st.ChangesSince(ctx, accID, 0)
	if len(cs.History) != 1 {
		t.Fatalf("expected 1 history row, got %d", len(cs.History))
	}
	h := cs.History[0]
	if h.LastRead != 1000 {
		t.Fatalf("last_read = %d, want max 1000", h.LastRead)
	}
	if h.ReadDuration != 90 {
		t.Fatalf("read_duration = %d, want max 90", h.ReadDuration)
	}
}

func TestCategoryAndPreferenceTombstones(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{
		Categories:  []Category{{Name: "Reading", Order: 1}},
		Preferences: []Preference{{Key: "theme", Type: "string", Value: json.RawMessage(`"dark"`)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{
		Categories:  []Category{{Name: "Reading", Deleted: true}},
		Preferences: []Preference{{Key: "theme", Type: "string", Deleted: true}},
	}); err != nil {
		t.Fatal(err)
	}

	cs, _, _ := st.ChangesSince(ctx, accID, 0)
	if len(cs.Categories) != 1 || !cs.Categories[0].Deleted {
		t.Fatal("category tombstone missing")
	}
	if len(cs.Preferences) != 1 || !cs.Preferences[0].Deleted {
		t.Fatal("preference tombstone missing")
	}
}

func TestPushReturnsOtherDevicesChanges(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	// Device A pushes at watermark 0.
	revA, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{
		Mangas: []Manga{{SourceID: 1, URL: "/m/a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Device B pushes at watermark 0 (hasn't seen A's change yet).
	revB, seenByB, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{
		Mangas: []Manga{{SourceID: 1, URL: "/m/b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if revB <= revA {
		t.Fatalf("revisions out of order: %d, %d", revA, revB)
	}
	// B's push response must contain A's manga, but not B's own.
	if len(seenByB.Mangas) != 1 || seenByB.Mangas[0].URL != "/m/a" {
		t.Fatalf("B should see only A's change, got %+v", seenByB.Mangas)
	}

	// A pushes again at watermark revB: nothing new to report.
	_, seenByA, err := st.ApplyChanges(ctx, accID, revB, &ChangeSet{
		Mangas: []Manga{{SourceID: 1, URL: "/m/c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seenByA.Empty() {
		t.Fatalf("A should see no foreign changes, got %+v", seenByA)
	}
}

func TestDeltaPullAndRevisionMonotonicity(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	rev1, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{Mangas: []Manga{{SourceID: 1, URL: "/m/a"}}})
	if err != nil {
		t.Fatal(err)
	}
	rev2, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{Mangas: []Manga{{SourceID: 1, URL: "/m/b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if rev2 <= rev1 {
		t.Fatalf("revision not monotonic: %d then %d", rev1, rev2)
	}

	cs, rev, err := st.ChangesSince(ctx, accID, rev1)
	if err != nil {
		t.Fatal(err)
	}
	if rev != rev2 {
		t.Fatalf("pull rev = %d, want %d", rev, rev2)
	}
	if len(cs.Mangas) != 1 || cs.Mangas[0].URL != "/m/b" {
		t.Fatalf("delta pull returned %+v, want only /m/b", cs.Mangas)
	}

	// Pull at high-water mark: empty changes.
	cs, _, err = st.ChangesSince(ctx, accID, rev2)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.Empty() {
		t.Fatalf("expected empty changes, got %+v", cs)
	}
}

func TestAccountIsolation(t *testing.T) {
	st, accA := openTestStore(t)
	ctx := context.Background()
	if err := st.CreateAccount(ctx, "otherhash", "other"); err != nil {
		t.Fatal(err)
	}
	accB, err := st.AccountByKeyHash(ctx, "otherhash")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := st.ApplyChanges(ctx, accA, 0, &ChangeSet{Mangas: []Manga{{SourceID: 1, URL: "/m/a"}}}); err != nil {
		t.Fatal(err)
	}
	cs, _, err := st.ChangesSince(ctx, accB.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.Empty() {
		t.Fatal("account B saw account A's data")
	}
}

func TestGCRemovesOldTombstones(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, &ChangeSet{
		Categories: []Category{{Name: "Old", Deleted: true}},
	}); err != nil {
		t.Fatal(err)
	}

	// Fresh tombstones survive GC.
	if err := st.GC(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	cs, _, _ := st.ChangesSince(ctx, accID, 0)
	if len(cs.Categories) != 1 {
		t.Fatal("fresh tombstone was collected")
	}

	// Anything older than the (negative) retention is collected.
	if err := st.GC(ctx, -time.Hour); err != nil {
		t.Fatal(err)
	}
	cs, _, _ = st.ChangesSince(ctx, accID, 0)
	if len(cs.Categories) != 0 {
		t.Fatal("expired tombstone survived GC")
	}
}
