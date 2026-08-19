package syncapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	body := `{"device_id":"dev-a","changes":{"mangas":[{"source_id":7,"url":"/m/one","title":"One","favorite":true,"client_version":5}],"preferences":[{"key":"theme","type":"string","value":"dark"}]}}`
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

	body := `{"changes":{"mangas":[{"source_id":7,"url":"/m/one","client_version":1},{"source_id":7,"url":"/m/two","client_version":1}],"chapters":[{"manga_source_id":7,"manga_url":"/m/one","url":"/c/1"}]}}`
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
	if !info.AllowRegistration {
		t.Fatalf("want allow_registration = true")
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

	h := NewHandler(st, config.Config{AllowRegistration: false})
	w := doRequest(t, h, "POST", "/api/v1/auth/register", "", `{"label":"phone"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("register status = %d, want 403 when registration disabled", w.Code)
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
