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
	cs, _, err := st.ChangesSince(context.Background(), accountID, 0)
	if err != nil {
		t.Fatalf("changes since: %v", err)
	}
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
		_, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
			SourceID: 1, URL: "/m/a", Title: title, SyncVersion: version,
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

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", Favorite: true, FavoriteAt: 1000, SyncVersion: 5,
	}}}); err != nil {
		t.Fatal(err)
	}
	// A newer version without favorite must not unset it.
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", Favorite: false, FavoriteAt: 500, SyncVersion: 9,
	}}}); err != nil {
		t.Fatal(err)
	}
	got := mangaAt(t, st, accID, "/m/a")
	if !got.Favorite {
		t.Fatal("favorite was lost in merge")
	}
	// favorite_at takes the earliest known value, matching backup restore.
	if got.FavoriteAt != 500 {
		t.Fatalf("favorite_at = %d, want earliest 500", got.FavoriteAt)
	}
}

func TestMangaDeleteAndReadd(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	apply := func(version int64, deleted bool) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
			SourceID: 1, URL: "/m/a", SyncVersion: version, Deleted: deleted,
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

// A favorite toggled off is sent as a tombstone at the same sync_version.
// The server must not persist the contradictory (favorite=true, deleted=true)
// state, and a stale device re-pushing the favorite must not resurrect it.
func TestMangaUnfavoriteDoesNotResurrect(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	// Device A adds the manga as a favorite.
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", Title: "M", Favorite: true, FavoriteAt: 1000, SyncVersion: 1,
	}}}); err != nil {
		t.Fatal(err)
	}

	// Device A removes it from the library; the client sends a tombstone.
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", Favorite: false, SyncVersion: 1, Deleted: true,
	}}}); err != nil {
		t.Fatal(err)
	}

	got := mangaAt(t, st, accID, "/m/a")
	if !got.Deleted {
		t.Fatal("removal did not stick")
	}
	if got.Favorite {
		t.Fatal("tombstoned manga should not remain a favorite")
	}
	if got.FavoriteAt != 0 {
		t.Fatalf("tombstoned manga should have no favorite_at, got %d", got.FavoriteAt)
	}

	// A stale device (same sync_version) re-pushes the favorite.
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", Title: "M", Favorite: true, SyncVersion: 1,
	}}}); err != nil {
		t.Fatal(err)
	}
	if !mangaAt(t, st, accID, "/m/a").Deleted {
		t.Fatal("stale device resurrected a removed manga")
	}
}

func TestChapterReadBookmarkORMerge(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	push := func(read, bookmark bool, page, version int64) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Chapters: []Chapter{{
			MangaSourceID: 1, MangaURL: "/m/a", URL: "/c/1",
			Read: read, Bookmark: bookmark, LastPageRead: page, SyncVersion: version,
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

func TestChapterDateEarliestWins(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	push := func(fetch, upload int64) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Chapters: []Chapter{{
			MangaSourceID: 1, MangaURL: "/m/a", URL: "/c/1",
			DateFetch: fetch, DateUpload: upload,
		}}})
		if err != nil {
			t.Fatal(err)
		}
	}

	push(2000, 2000)
	// Fresh device bulk-fetch stamps now-dates; they must not overwrite originals.
	push(5000, 5000)

	cs, _, err := st.ChangesSince(ctx, accID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(cs.Chapters))
	}
	c := cs.Chapters[0]
	if c.DateFetch != 2000 || c.DateUpload != 2000 {
		t.Fatalf("earliest dates lost: got fetch=%d upload=%d", c.DateFetch, c.DateUpload)
	}

	// Unknown (0) from old clients never overwrites.
	push(0, 0)
	cs, _, _ = st.ChangesSince(ctx, accID, 0)
	if cs.Chapters[0].DateFetch != 2000 || cs.Chapters[0].DateUpload != 2000 {
		t.Fatalf("zero dates overwrote known dates: %+v", cs.Chapters[0])
	}

	// An earlier date discovered later still wins.
	push(1000, 1500)
	cs, _, _ = st.ChangesSince(ctx, accID, 0)
	if cs.Chapters[0].DateFetch != 1000 || cs.Chapters[0].DateUpload != 1500 {
		t.Fatalf("earlier date did not win: %+v", cs.Chapters[0])
	}
}

func TestHistoryMaxMerge(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	push := func(readAt, duration int64) {
		_, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{History: []HistoryEntry{{
			MangaSourceID: 1, MangaURL: "/m/a", ChapterURL: "/c/1",
			ReadAt: readAt, ReadDuration: duration,
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
	if h.ReadAt != 1000 {
		t.Fatalf("read_at = %d, want max 1000", h.ReadAt)
	}
	if h.ReadDuration != 90 {
		t.Fatalf("read_duration = %d, want max 90", h.ReadDuration)
	}
}

func TestCategoryAndPreferenceTombstones(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		Categories:  []Category{{Name: "Reading", Order: 1}},
		Preferences: []Preference{{Key: "theme", Type: "string", Value: json.RawMessage(`"dark"`)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
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

func TestExcludedScanlatorMembershipAndTombstone(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		ExcludedScanlators: []ExcludedScanlator{
			{MangaSourceID: 1, MangaURL: "/m/a", Scanlator: "Group A"},
			{MangaSourceID: 1, MangaURL: "/m/a", Scanlator: "Group B"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	cs, _, _ := st.ChangesSince(ctx, accID, 0)
	if len(cs.ExcludedScanlators) != 2 {
		t.Fatalf("expected 2 excluded scanlators, got %d", len(cs.ExcludedScanlators))
	}

	status, _ := st.Status(ctx, accID)
	if status.ExcludedScanlatorCount != 2 {
		t.Fatalf("status count = %d, want 2", status.ExcludedScanlatorCount)
	}

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		ExcludedScanlators: []ExcludedScanlator{
			{MangaSourceID: 1, MangaURL: "/m/a", Scanlator: "Group A", Deleted: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	status, _ = st.Status(ctx, accID)
	if status.ExcludedScanlatorCount != 1 {
		t.Fatalf("active count = %d, want 1 after tombstone", status.ExcludedScanlatorCount)
	}
	cs, _, _ = st.ChangesSince(ctx, accID, 0)
	var deleted int
	for _, es := range cs.ExcludedScanlators {
		if es.Scanlator == "Group A" && es.Deleted {
			deleted++
		}
	}
	if deleted != 1 {
		t.Fatal("excluded scanlator tombstone missing")
	}
}

func TestMemoMergeShallowUnion(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", SyncVersion: 1,
		Memo: json.RawMessage(`{"a":1,"b":2}`),
	}}}); err != nil {
		t.Fatal(err)
	}
	// A later version adds a key and overrides another; both survive.
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{
		SourceID: 1, URL: "/m/a", SyncVersion: 2,
		Memo: json.RawMessage(`{"b":9,"c":3}`),
	}}}); err != nil {
		t.Fatal(err)
	}

	got := mangaAt(t, st, accID, "/m/a")
	var memo map[string]int
	if err := json.Unmarshal(got.Memo, &memo); err != nil {
		t.Fatalf("memo is not an object: %v (%s)", err, got.Memo)
	}
	if memo["a"] != 1 || memo["b"] != 9 || memo["c"] != 3 {
		t.Fatalf("unexpected merged memo: %v", memo)
	}
}

func TestPushReturnsOtherDevicesChanges(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	// Device A pushes at watermark 0.
	revA, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		Mangas: []Manga{{SourceID: 1, URL: "/m/a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Device B pushes at watermark 0 (hasn't seen A's change yet).
	revB, seenByB, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
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
	_, seenByA, err := st.ApplyChanges(ctx, accID, revB, "", &ChangeSet{
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

	rev1, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{SourceID: 1, URL: "/m/a"}}})
	if err != nil {
		t.Fatal(err)
	}
	rev2, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{Mangas: []Manga{{SourceID: 1, URL: "/m/b"}}})
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

	if _, _, err := st.ApplyChanges(ctx, accA, 0, "", &ChangeSet{Mangas: []Manga{{SourceID: 1, URL: "/m/a"}}}); err != nil {
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

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
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

func TestDeviceTracking(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	// Push from device A
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "device-a", &ChangeSet{
		Mangas: []Manga{{SourceID: 1, URL: "/m/a"}},
	}); err != nil {
		t.Fatal(err)
	}

	status, err := st.Status(ctx, accID)
	if err != nil {
		t.Fatal(err)
	}
	if status.DeviceCount != 1 {
		t.Fatalf("expected 1 device, got %d", status.DeviceCount)
	}

	// Push from device B
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "device-b", &ChangeSet{
		Mangas: []Manga{{SourceID: 1, URL: "/m/b"}},
	}); err != nil {
		t.Fatal(err)
	}

	status, err = st.Status(ctx, accID)
	if err != nil {
		t.Fatal(err)
	}
	if status.DeviceCount != 2 {
		t.Fatalf("expected 2 devices, got %d", status.DeviceCount)
	}
}

func TestExtensionStoreSyncAndTombstones(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		ExtensionStores: []ExtensionStore{{
			IndexURL:         "https://raw.githubusercontent.com/keiyoushi/extensions/repo/index.min.json",
			Name:             "Keiyoushi",
			BadgeLabel:       "Official",
			SigningKey:       "abcd1234key",
			ContactWebsite:   "https://keiyoushi.github.io",
			ExtensionListURL: "https://keiyoushi.github.io/extensions/",
		}},
	}); err != nil {
		t.Fatal(err)
	}

	cs, _, err := st.ChangesSince(ctx, accID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.ExtensionStores) != 1 {
		t.Fatalf("expected 1 extension store, got %d", len(cs.ExtensionStores))
	}
	es := cs.ExtensionStores[0]
	if es.Name != "Keiyoushi" || es.SigningKey != "abcd1234key" || es.ContactWebsite != "https://keiyoushi.github.io" {
		t.Fatalf("unexpected extension store data: %+v", es)
	}

	status, err := st.Status(ctx, accID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ExtensionStoreCount != 1 {
		t.Fatalf("expected 1 active extension store, got %d", status.ExtensionStoreCount)
	}

	// Tombstone deletion
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		ExtensionStores: []ExtensionStore{{
			IndexURL: "https://raw.githubusercontent.com/keiyoushi/extensions/repo/index.min.json",
			Deleted:  true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	cs, _, err = st.ChangesSince(ctx, accID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.ExtensionStores) != 1 || !cs.ExtensionStores[0].Deleted {
		t.Fatalf("expected tombstoned extension store, got %+v", cs.ExtensionStores)
	}
}

// A sparse push that only carries the index url must not blank a store the
// server already knows about.
func TestExtensionStoreSparsePushKeepsDetails(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		ExtensionStores: []ExtensionStore{{
			IndexURL:   "https://example.com/index.min.json",
			Name:       "Store",
			SigningKey: "key",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		ExtensionStores: []ExtensionStore{{IndexURL: "https://example.com/index.min.json"}},
	}); err != nil {
		t.Fatal(err)
	}

	cs, _, _ := st.ChangesSince(ctx, accID, 0)
	if cs.ExtensionStores[0].Name != "Store" || cs.ExtensionStores[0].SigningKey != "key" {
		t.Fatalf("sparse push blanked the store: %+v", cs.ExtensionStores[0])
	}
}

func TestActiveEntityCount(t *testing.T) {
	st, accID := openTestStore(t)
	ctx := context.Background()

	if _, _, err := st.ApplyChanges(ctx, accID, 0, "", &ChangeSet{
		Mangas:   []Manga{{SourceID: 1, URL: "/m/a"}, {SourceID: 1, URL: "/m/b"}},
		Chapters: []Chapter{{MangaSourceID: 1, MangaURL: "/m/a", URL: "/c/1"}},
		History:  []HistoryEntry{{MangaSourceID: 1, MangaURL: "/m/a", ChapterURL: "/c/1", ReadAt: 5}},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := st.ActiveEntityCount(ctx, accID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("entity count = %d, want 4", n)
	}
}
