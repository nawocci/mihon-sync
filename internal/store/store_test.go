package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newTestLibrary(t *testing.T, st *Store) (string, Library) {
	t.Helper()
	key, lib, err := st.CreateLibrary(context.Background(), "test")
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	return key, lib
}

func TestLibraryLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	key, lib, err := st.CreateLibrary(ctx, "")
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	if lib.Label != "key-1" {
		t.Fatalf("default label = %q, want key-1", lib.Label)
	}

	got, err := st.Authenticate(ctx, key)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got.ID != lib.ID || got.Label != lib.Label {
		t.Fatalf("authenticate returned %+v, want %+v", got, lib)
	}

	if _, err := st.Authenticate(ctx, "mhk_wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong key error = %v, want ErrNotFound", err)
	}
	if _, err := st.Authenticate(ctx, key+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tampered key error = %v, want ErrNotFound", err)
	}
}

func TestInviteRedemption(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	code, err := st.CreateInvite(ctx, "invitee", 2, time.Hour)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	for i := 0; i < 2; i++ {
		key, lib, err := st.RedeemInvite(ctx, code, "")
		if err != nil {
			t.Fatalf("redeem %d: %v", i, err)
		}
		if _, err := st.Authenticate(ctx, key); err != nil {
			t.Fatalf("redeem %d produced unusable key: %v", i, err)
		}
		if lib.Label == "" {
			t.Fatalf("redeem %d produced empty label", i)
		}
	}

	if _, _, err := st.RedeemInvite(ctx, code, ""); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("exhausted invite error = %v, want ErrInvalidInvite", err)
	}

	expired, err := st.CreateInvite(ctx, "", 1, -time.Hour)
	if err != nil {
		t.Fatalf("create expired invite: %v", err)
	}
	if _, _, err := st.RedeemInvite(ctx, expired, ""); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("expired invite error = %v, want ErrInvalidInvite", err)
	}

	if _, _, err := st.RedeemInvite(ctx, "inv_garbage", ""); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("unknown invite error = %v, want ErrInvalidInvite", err)
	}
}

func TestPushLastWriterWins(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_, lib := newTestLibrary(t, st)

	payload := "AQID"
	first := Marker{WallTime: 1000, Counter: 0, DeviceID: "dev-a"}
	results, seq, err := st.Push(ctx, lib.ID, []Record{
		{Type: "manga", Key: "1|/manga/a", Marker: first, Payload: &payload},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(results) != 1 || !results[0].Accepted {
		t.Fatalf("results = %+v, want one accepted record", results)
	}
	if seq != 1 {
		t.Fatalf("seq = %d, want 1", seq)
	}

	// An older marker loses and the stored marker comes back.
	older := Marker{WallTime: 500, Counter: 9, DeviceID: "dev-b"}
	results, seq, err = st.Push(ctx, lib.ID, []Record{
		{Type: "manga", Key: "1|/manga/a", Marker: older, Payload: &payload},
	})
	if err != nil {
		t.Fatalf("push older: %v", err)
	}
	if results[0].Accepted {
		t.Fatal("older marker was accepted")
	}
	if results[0].Marker != first {
		t.Fatalf("rejected marker = %+v, want stored %+v", results[0].Marker, first)
	}
	if seq != 1 {
		t.Fatalf("seq after reject = %d, want 1", seq)
	}

	// Device id breaks exact ties: same wall time and counter, higher device wins.
	tieWinner := Marker{WallTime: 1000, Counter: 0, DeviceID: "dev-z"}
	results, _, err = st.Push(ctx, lib.ID, []Record{
		{Type: "manga", Key: "1|/manga/a", Marker: tieWinner, Payload: &payload},
	})
	if err != nil {
		t.Fatalf("push tie: %v", err)
	}
	if !results[0].Accepted {
		t.Fatal("tie break by device id was not accepted")
	}
}

func TestPushDeletionAndPull(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_, lib := newTestLibrary(t, st)

	payload := "AQID"
	if _, _, err := st.Push(ctx, lib.ID, []Record{
		{Type: "manga", Key: "1|/manga/a", Marker: Marker{1000, 0, "dev-a"}, Payload: &payload},
	}); err != nil {
		t.Fatalf("push insert: %v", err)
	}

	// Pushing the same record again re-enters the pull stream with a new sequence.
	if _, seq, err := st.Push(ctx, lib.ID, []Record{
		{Type: "manga", Key: "1|/manga/a", Marker: Marker{1000, 1, "dev-a"}, Payload: &payload},
	}); err != nil {
		t.Fatalf("push update: %v", err)
	} else if seq != 2 {
		t.Fatalf("seq after update = %d, want 2", seq)
	}

	records, watermark, hasMore, err := st.Pull(ctx, lib.ID, 1, 200)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(records) != 1 || records[0].Marker.Counter != 1 {
		t.Fatalf("pull after since=1 returned %+v, want only the update", records)
	}
	if watermark != 2 || hasMore {
		t.Fatalf("watermark = %d hasMore = %v, want 2 false", watermark, hasMore)
	}

	// A deletion is just a newer marker with no payload.
	if _, _, err := st.Push(ctx, lib.ID, []Record{
		{Type: "manga", Key: "1|/manga/a", Marker: Marker{2000, 0, "dev-b"}, Deleted: true},
	}); err != nil {
		t.Fatalf("push deletion: %v", err)
	}
	records, watermark, _, err = st.Pull(ctx, lib.ID, watermark, 200)
	if err != nil {
		t.Fatalf("pull deletion: %v", err)
	}
	if len(records) != 1 || !records[0].Deleted || records[0].Payload != nil {
		t.Fatalf("deletion record = %+v, want deleted with no payload", records[0])
	}

	// An empty pull still advances to the head, so clients can stop asking.
	records, watermark, _, err = st.Pull(ctx, lib.ID, watermark, 200)
	if err != nil {
		t.Fatalf("pull empty: %v", err)
	}
	if len(records) != 0 || watermark != 3 {
		t.Fatalf("empty pull watermark = %d, want 3", watermark)
	}

	count, err := st.RecordCount(ctx, lib.ID)
	if err != nil {
		t.Fatalf("record count: %v", err)
	}
	if count != 0 {
		t.Fatalf("live records = %d, want 0 after deletion", count)
	}
}

func TestPullPaging(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_, lib := newTestLibrary(t, st)

	for i := 0; i < 5; i++ {
		payload := "AQID"
		if _, _, err := st.Push(ctx, lib.ID, []Record{
			{
				Type:    "chapter",
				Key:     "1|/manga/a|" + string(rune('a'+i)),
				Marker:  Marker{int64(1000 + i), 0, "dev-a"},
				Payload: &payload,
			},
		}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}

	page, watermark, hasMore, err := st.Pull(ctx, lib.ID, 0, 2)
	if err != nil {
		t.Fatalf("pull page 1: %v", err)
	}
	if len(page) != 2 || !hasMore || watermark != 2 {
		t.Fatalf("page 1 = %d records, hasMore = %v, watermark = %d", len(page), hasMore, watermark)
	}

	page, watermark, hasMore, err = st.Pull(ctx, lib.ID, watermark, 2)
	if err != nil {
		t.Fatalf("pull page 2: %v", err)
	}
	if len(page) != 2 || !hasMore || watermark != 4 {
		t.Fatalf("page 2 = %d records, hasMore = %v, watermark = %d", len(page), hasMore, watermark)
	}

	page, watermark, hasMore, err = st.Pull(ctx, lib.ID, watermark, 2)
	if err != nil {
		t.Fatalf("pull page 3: %v", err)
	}
	if len(page) != 1 || hasMore || watermark != 5 {
		t.Fatalf("page 3 = %d records, hasMore = %v, watermark = %d", len(page), hasMore, watermark)
	}
}

func TestLibrariesAreIsolated(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_, first := newTestLibrary(t, st)
	_, second := newTestLibrary(t, st)

	payload := "AQID"
	if _, _, err := st.Push(ctx, first.ID, []Record{
		{Type: "manga", Key: "1|/manga/a", Marker: Marker{1000, 0, "dev-a"}, Payload: &payload},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	records, watermark, _, err := st.Pull(ctx, second.ID, 0, 200)
	if err != nil {
		t.Fatalf("pull other library: %v", err)
	}
	if len(records) != 0 || watermark != 0 {
		t.Fatalf("other library saw %d records at watermark %d", len(records), watermark)
	}
}
