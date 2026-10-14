package syncapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nawocci/mihon-sync/internal/auth"
	"github.com/nawocci/mihon-sync/internal/config"
	"github.com/nawocci/mihon-sync/internal/store"
)

func setupTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := st.CreateAccount(context.Background(), auth.HashKey(key), "test"); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return NewHandler(st), key
}

func doRequest(t *testing.T, h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHealthzNeedsNoAuth(t *testing.T) {
	h, _ := setupTestServer(t)
	w := doRequest(t, h, "GET", "/healthz", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", w.Code)
	}
}

func TestAuthRequired(t *testing.T) {
	h, key := setupTestServer(t)

	for _, path := range []string{"/api/v1/auth/check", "/api/v1/sync/pull?since=0", "/api/v1/sync/status"} {
		if w := doRequest(t, h, "GET", path, "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without key: status = %d, want 401", path, w.Code)
		}
		if w := doRequest(t, h, "GET", path, "mhk_wrong", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s with bad key: status = %d, want 401", path, w.Code)
		}
		if w := doRequest(t, h, "GET", path, key, ""); w.Code != http.StatusOK {
			t.Fatalf("%s with good key: status = %d, want 200 (%s)", path, w.Code, w.Body)
		}
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	h, key := setupTestServer(t)

	body := `{"device_id":"dev-a","changes":{"mangas":[{"source_id":7,"url":"/m/one","title":"One","favorite":true,"favorite_at":1700000000000,"sync_version":5}],"preferences":[{"key":"theme","type":"string","value":"dark"}]}}`
	w := doRequest(t, h, "POST", "/api/v1/sync/push", key, body)
	if w.Code != http.StatusOK {
		t.Fatalf("push status = %d (%s)", w.Code, w.Body)
	}
	var push pushResponse
	if err := json.Unmarshal(w.Body.Bytes(), &push); err != nil {
		t.Fatal(err)
	}
	if push.Rev != 1 {
		t.Fatalf("push rev = %d, want 1", push.Rev)
	}

	w = doRequest(t, h, "GET", "/api/v1/sync/pull?since=0", key, "")
	if w.Code != http.StatusOK {
		t.Fatalf("pull status = %d", w.Code)
	}
	var pull pullResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pull); err != nil {
		t.Fatal(err)
	}
	if pull.Rev != 1 || len(pull.Changes.Mangas) != 1 || pull.Changes.Mangas[0].Title != "One" {
		t.Fatalf("unexpected pull result: %s", w.Body)
	}
	if len(pull.Changes.Preferences) != 1 || string(pull.Changes.Preferences[0].Value) != `"dark"` {
		t.Fatalf("preference did not round-trip: %s", w.Body)
	}

	// Delta: nothing new since rev 1.
	w = doRequest(t, h, "GET", "/api/v1/sync/pull?since=1", key, "")
	var pull2 pullResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pull2); err != nil {
		t.Fatal(err)
	}
	if len(pull2.Changes.Mangas) != 0 {
		t.Fatalf("expected no changes since rev 1, got %s", w.Body)
	}
}

func TestPushPullChapterDatesRoundTrip(t *testing.T) {
	h, key := setupTestServer(t)

	body := `{"device_id":"dev-a","changes":{"chapters":[{"manga_source_id":7,"manga_url":"/m/one","url":"/c/1","date_fetch":1700000000001,"date_upload":1700000000002}]}}`
	w := doRequest(t, h, "POST", "/api/v1/sync/push", key, body)
	if w.Code != http.StatusOK {
		t.Fatalf("push status = %d (%s)", w.Code, w.Body)
	}

	w = doRequest(t, h, "GET", "/api/v1/sync/pull?since=0", key, "")
	if w.Code != http.StatusOK {
		t.Fatalf("pull status = %d", w.Code)
	}
	var pull pullResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pull); err != nil {
		t.Fatal(err)
	}
	if len(pull.Changes.Chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %s", w.Body)
	}
	c := pull.Changes.Chapters[0]
	if c.DateFetch == nil || *c.DateFetch != 1700000000001 || c.DateUpload == nil || *c.DateUpload != 1700000000002 {
		t.Fatalf("dates did not round-trip: %+v", c)
	}
}

func TestPushValidation(t *testing.T) {
	h, key := setupTestServer(t)

	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{"device_id":`},
		{"manga missing url", `{"changes":{"mangas":[{"source_id":1}]}}`},
		{"chapter missing manga_url", `{"changes":{"chapters":[{"url":"/c/1"}]}}`},
		{"category missing name", `{"changes":{"categories":[{"order":1}]}}`},
		{"preference invalid value", `{"changes":{"preferences":[{"key":"k","type":"string","value":not json}]}}`},
	}
	for _, tc := range cases {
		w := doRequest(t, h, "POST", "/api/v1/sync/push", key, tc.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, w.Code)
		}
	}
}

func TestPullRequiresSince(t *testing.T) {
	h, key := setupTestServer(t)
	if w := doRequest(t, h, "GET", "/api/v1/sync/pull", key, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if w := doRequest(t, h, "GET", "/api/v1/sync/pull?since=abc", key, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestStatus(t *testing.T) {
	h, key := setupTestServer(t)

	body := `{"changes":{"mangas":[{"source_id":7,"url":"/m/one","sync_version":1},{"source_id":7,"url":"/m/two","sync_version":1}],"chapters":[{"manga_source_id":7,"manga_url":"/m/one","url":"/c/1"}],"extension_stores":[{"index_url":"https://x/repo.json","name":"X"}]}}`
	if w := doRequest(t, h, "POST", "/api/v1/sync/push", key, body); w.Code != http.StatusOK {
		t.Fatalf("push: %s", w.Body)
	}

	w := doRequest(t, h, "GET", "/api/v1/sync/status", key, "")
	var st statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.MangaCount != 2 || st.ChapterCount != 1 || st.Rev != 1 {
		t.Fatalf("unexpected status: %s", w.Body)
	}
	// Regression: the handler used to drop ExtensionStoreCount from the response.
	if st.ExtensionStoreCount != 1 {
		t.Fatalf("expected 1 extension store in status, got %d: %s", st.ExtensionStoreCount, w.Body)
	}
}

func TestServerInfo(t *testing.T) {
	h, _ := setupTestServer(t)
	w := doRequest(t, h, "GET", "/api/v1/info", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("info status = %d", w.Code)
	}
	var info serverInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Registration != config.RegistrationOpen {
		t.Fatalf("want registration = open, got %q", info.Registration)
	}
	if !info.AllowRegistration {
		t.Fatalf("want allow_registration = true for backward compatibility")
	}
}

func TestRegister(t *testing.T) {
	h, _ := setupTestServer(t)

	w := doRequest(t, h, "POST", "/api/v1/auth/register", "", `{"label":"my test phone"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201 (%s)", w.Code, w.Body)
	}
	var reg registerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &reg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reg.APIKey, "mhk_") {
		t.Fatalf("invalid generated key: %s", reg.APIKey)
	}
	if reg.Label != "my test phone" {
		t.Fatalf("label = %s, want 'my test phone'", reg.Label)
	}

	// Verify the newly generated key works for auth check
	w2 := doRequest(t, h, "GET", "/api/v1/auth/check", reg.APIKey, "")
	if w2.Code != http.StatusOK {
		t.Fatalf("auth check with new key failed: %d (%s)", w2.Code, w2.Body)
	}
}

func TestRegisterDisabled(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	h := NewHandler(st, config.Config{Registration: config.RegistrationClosed})
	w := doRequest(t, h, "POST", "/api/v1/auth/register", "", `{"label":"phone"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("register status = %d, want 403 when registration disabled", w.Code)
	}
}

func setupInviteServer(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewHandler(st, config.Config{Registration: config.RegistrationInvite}), st
}

func mintInvite(t *testing.T, st *store.Store, ttl time.Duration) string {
	t.Helper()
	code, err := auth.GenerateInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInviteToken(context.Background(), auth.HashKey(code), "", time.Now().Add(ttl).Unix()); err != nil {
		t.Fatal(err)
	}
	return code
}

func TestRegisterInviteHappyPath(t *testing.T) {
	h, st := setupInviteServer(t)
	code := mintInvite(t, st, 15*time.Minute)

	w := doRequest(t, h, "POST", "/api/v1/auth/register", "", `{"label":"phone","invite_code":"`+code+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201 (%s)", w.Code, w.Body)
	}
	var reg registerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &reg); err != nil {
		t.Fatal(err)
	}
	if w2 := doRequest(t, h, "GET", "/api/v1/auth/check", reg.APIKey, ""); w2.Code != http.StatusOK {
		t.Fatalf("auth check with invite key failed: %d (%s)", w2.Code, w2.Body)
	}
}

func TestRegisterInviteRejectsBadCode(t *testing.T) {
	h, _ := setupInviteServer(t)
	for _, body := range []string{`{"label":"phone"}`, `{"label":"phone","invite_code":"mhi_bogus"}`} {
		if w := doRequest(t, h, "POST", "/api/v1/auth/register", "", body); w.Code != http.StatusForbidden {
			t.Fatalf("register status = %d, want 403 (%s)", w.Code, w.Body)
		}
	}
}

func TestRegisterInviteSingleUse(t *testing.T) {
	h, st := setupInviteServer(t)
	code := mintInvite(t, st, 15*time.Minute)
	body := `{"label":"phone","invite_code":"` + code + `"}`
	if w := doRequest(t, h, "POST", "/api/v1/auth/register", "", body); w.Code != http.StatusCreated {
		t.Fatalf("first register status = %d, want 201 (%s)", w.Code, w.Body)
	}
	if w := doRequest(t, h, "POST", "/api/v1/auth/register", "", body); w.Code != http.StatusForbidden {
		t.Fatalf("second register status = %d, want 403 (%s)", w.Code, w.Body)
	}
}

func TestRegisterInviteExpired(t *testing.T) {
	h, st := setupInviteServer(t)
	code := mintInvite(t, st, -time.Minute)
	body := `{"label":"phone","invite_code":"` + code + `"}`
	if w := doRequest(t, h, "POST", "/api/v1/auth/register", "", body); w.Code != http.StatusForbidden {
		t.Fatalf("register status = %d, want 403 (%s)", w.Code, w.Body)
	}
}

func TestDeleteAccount(t *testing.T) {
	h, key := setupTestServer(t)

	// Auth check should succeed initially
	w := doRequest(t, h, "GET", "/api/v1/auth/check", key, "")
	if w.Code != http.StatusOK {
		t.Fatalf("initial auth check failed: %d", w.Code)
	}

	// Delete account
	wDel := doRequest(t, h, "DELETE", "/api/v1/auth/account", key, "")
	if wDel.Code != http.StatusOK {
		t.Fatalf("delete account status = %d (%s)", wDel.Code, wDel.Body)
	}

	// Auth check should now fail (401)
	wAfter := doRequest(t, h, "GET", "/api/v1/auth/check", key, "")
	if wAfter.Code != http.StatusUnauthorized {
		t.Fatalf("auth check after delete status = %d, want 401", wAfter.Code)
	}
}

func TestWebStaticServing(t *testing.T) {
	h, _ := setupTestServer(t)

	// Test root index.html
	wRoot := doRequest(t, h, "GET", "/", "", "")
	if wRoot.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", wRoot.Code)
	}
	if !strings.Contains(wRoot.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("GET / did not return HTML")
	}

	// Test static assets
	wCSS := doRequest(t, h, "GET", "/app.css", "", "")
	if wCSS.Code != http.StatusOK {
		t.Fatalf("GET /app.css status = %d", wCSS.Code)
	}
	wJS := doRequest(t, h, "GET", "/app.js", "", "")
	if wJS.Code != http.StatusOK {
		t.Fatalf("GET /app.js status = %d", wJS.Code)
	}
	wLogo := doRequest(t, h, "GET", "/logo.svg", "", "")
	if wLogo.Code != http.StatusOK {
		t.Fatalf("GET /logo.svg status = %d", wLogo.Code)
	}
}

func TestPushValidationNewEntities(t *testing.T) {
	h, key := setupTestServer(t)

	cases := []struct {
		name string
		body string
	}{
		{"excluded scanlator missing scanlator", `{"changes":{"excluded_scanlators":[{"manga_source_id":1,"manga_url":"/m/a"}]}}`},
		{"extension store missing index_url", `{"changes":{"extension_stores":[{"name":"X"}]}}`},
	}
	for _, tc := range cases {
		w := doRequest(t, h, "POST", "/api/v1/sync/push", key, tc.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, w.Code)
		}
	}
}

func TestErrorResponseShape(t *testing.T) {
	h, key := setupTestServer(t)
	w := doRequest(t, h, "GET", "/api/v1/sync/pull?since=abc", key, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var resp errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("error body is not the documented shape: %v (%s)", err, w.Body)
	}
	if resp.Error.Code != "invalid_since" || resp.Error.Message == "" {
		t.Fatalf("unexpected error body: %s", w.Body)
	}
}

func TestNullTimestampsRoundTrip(t *testing.T) {
	h, key := setupTestServer(t)
	// A chapter pushed without dates must come back with nulls, not zeros.
	body := `{"changes":{"chapters":[{"manga_source_id":7,"manga_url":"/m/one","url":"/c/1"}]}}`
	if w := doRequest(t, h, "POST", "/api/v1/sync/push", key, body); w.Code != http.StatusOK {
		t.Fatalf("push: %s", w.Body)
	}
	w := doRequest(t, h, "GET", "/api/v1/sync/pull?since=0", key, "")
	if !strings.Contains(w.Body.String(), `"date_fetch":null`) {
		t.Fatalf("expected null date_fetch in %s", w.Body)
	}
}

func TestExcludedScanlatorRoundTrip(t *testing.T) {
	h, key := setupTestServer(t)
	body := `{"changes":{"excluded_scanlators":[{"manga_source_id":7,"manga_url":"/m/one","scanlator":"Group A"}]}}`
	if w := doRequest(t, h, "POST", "/api/v1/sync/push", key, body); w.Code != http.StatusOK {
		t.Fatalf("push: %s", w.Body)
	}
	w := doRequest(t, h, "GET", "/api/v1/sync/pull?since=0", key, "")
	var pull pullResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pull); err != nil {
		t.Fatal(err)
	}
	if len(pull.Changes.ExcludedScanlators) != 1 || pull.Changes.ExcludedScanlators[0].Scanlator != "Group A" {
		t.Fatalf("excluded scanlator did not round-trip: %s", w.Body)
	}
}

func TestAccountTooLarge(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	key, _ := auth.GenerateKey()
	if err := st.CreateAccount(context.Background(), auth.HashKey(key), "test"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(st, config.Config{Registration: config.RegistrationOpen, MaxEntities: 1})

	body := `{"changes":{"mangas":[{"source_id":1,"url":"/m/a"},{"source_id":1,"url":"/m/b"}]}}`
	w := doRequest(t, h, "POST", "/api/v1/sync/push", key, body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (%s)", w.Code, w.Body)
	}
}

func TestMetricsExposedWhenEnabled(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := NewHandler(st, config.Config{Registration: config.RegistrationOpen, MetricsEnabled: true})
	w := doRequest(t, h, "GET", "/metrics", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "mihon_sync_pushes_total") {
		t.Fatalf("metrics: status %d body %s", w.Code, w.Body)
	}
}

func TestCapabilitiesAdvertised(t *testing.T) {
	h, _ := setupTestServer(t)
	w := doRequest(t, h, "GET", "/api/v1/info", "", "")
	var info serverInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range info.Capabilities {
		if c == "entity.excluded_scanlator" {
			found = true
		}
	}
	if !found {
		t.Fatalf("capabilities missing excluded_scanlator: %v", info.Capabilities)
	}
}
