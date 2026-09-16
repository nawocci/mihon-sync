package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func TestInviteHappyPathBurnsOnSuccess(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()

	code, err := generateTestToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInviteToken(ctx, hashForTest(code), "", time.Now().Add(15*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	key := testRandomKey(t)
	if err := st.RegisterWithInvite(ctx, hashForTest(key), "phone", hashForTest(code), time.Now().Unix()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := st.AccountByKeyHash(ctx, hashForTest(key)); err != nil {
		t.Fatalf("account missing after register: %v", err)
	}

	key2 := testRandomKey(t)
	if err := st.RegisterWithInvite(ctx, hashForTest(key2), "", hashForTest(code), time.Now().Unix()); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("reuse err = %v, want ErrInvalidInvite", err)
	}
}

func TestInviteExpiredRejected(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()

	code, err := generateTestToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInviteToken(ctx, hashForTest(code), "", time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	key := testRandomKey(t)
	if err := st.RegisterWithInvite(ctx, hashForTest(key), "", hashForTest(code), time.Now().Unix()); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("expired err = %v, want ErrInvalidInvite", err)
	}
}

func TestInviteUnknownRejected(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()

	key := testRandomKey(t)
	if err := st.RegisterWithInvite(ctx, hashForTest(key), "", hashForTest("mhi_bogus"), time.Now().Unix()); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("unknown err = %v, want ErrInvalidInvite", err)
	}
}

func TestInviteGC(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	used, err := generateTestToken()
	if err != nil {
		t.Fatal(err)
	}
	expired, err := generateTestToken()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := generateTestToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInviteToken(ctx, hashForTest(used), "", now+900); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInviteToken(ctx, hashForTest(expired), "", now-60); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInviteToken(ctx, hashForTest(pending), "", now+900); err != nil {
		t.Fatal(err)
	}

	key := testRandomKey(t)
	if err := st.RegisterWithInvite(ctx, hashForTest(key), "", hashForTest(used), now); err != nil {
		t.Fatal(err)
	}
	if err := st.GCInviteTokens(ctx, now); err != nil {
		t.Fatal(err)
	}

	tokens, err := st.ListInviteTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].TokenHash != hashForTest(pending) {
		t.Fatalf("want only pending token left, got %d", len(tokens))
	}
}

func testRandomKey(t *testing.T) string {
	t.Helper()
	return "testkey-" + t.Name() + "-" + time.Now().Format("150405.000000000")
}

func generateTestToken() (string, error) {
	return "mhi_test-" + time.Now().Format("150405.000000000"), nil
}

func hashForTest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
