package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mihon-sync/internal/store"
)

const remoteAddr = "192.0.2.1:1234"
const localAddr = "127.0.0.1:1234"

type testEnv struct {
	store   *store.Store
	handler http.Handler
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	handler := New(st, Config{BootstrapNetwork: "loopback", Version: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	return &testEnv{store: st, handler: handler}
}

func (e *testEnv) do(t *testing.T, method, path, token string, body any, addr string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = addr
	if req.RemoteAddr == "" {
		req.RemoteAddr = remoteAddr
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) library(t *testing.T, label string) string {
	t.Helper()
	key, _, err := e.store.CreateLibrary(context.Background(), label)
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	return key
}

func TestHealth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/v1/health", "", nil, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestDashboard(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/", "", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want 200", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("dashboard content type = %q", contentType)
	}
	if !strings.Contains(rec.Body.String(), `id="account"`) {
		t.Fatalf("dashboard body looks wrong")
	}
}

func TestBootstrapIsLocalOnly(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "POST", "/bootstrap/key", "", map[string]string{"label": "x"}, remoteAddr)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote bootstrap status = %d, want 403", rec.Code)
	}

	rec = env.do(t, "POST", "/bootstrap/key", "", map[string]string{"label": "x"}, localAddr)
	if rec.Code != http.StatusOK {
		t.Fatalf("local bootstrap status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var created keyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	if !strings.HasPrefix(created.APIKey, "mhk_") || created.Label != "x" {
		t.Fatalf("created = %+v", created)
	}

	rec = env.do(t, "GET", "/v1/me", created.APIKey, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("me with bootstrap key status = %d, want 200", rec.Code)
	}

	rec = env.do(t, "GET", "/bootstrap/status", "", nil, localAddr)
	if !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Fatalf("local status = %s", rec.Body.String())
	}
	rec = env.do(t, "GET", "/bootstrap/status", "", nil, remoteAddr)
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("remote status = %s", rec.Body.String())
	}
}

func TestMeRequiresValidKey(t *testing.T) {
	env := newTestEnv(t)

	if rec := env.do(t, "GET", "/v1/me", "", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want 401", rec.Code)
	}
	if rec := env.do(t, "GET", "/v1/me", "mhk_nope", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status = %d, want 401", rec.Code)
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	key := env.library(t, "round-trip")

	payload := "AQID"
	record := store.Record{
		Type:    "manga",
		Key:     "1|/manga/a",
		Marker:  store.Marker{WallTime: 1000, Counter: 0, DeviceID: "dev-a"},
		Payload: &payload,
	}

	rec := env.do(t, "POST", "/v1/sync/push", key, pushRequest{Records: []store.Record{record}}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("push status = %d: %s", rec.Code, rec.Body)
	}
	var pushed pushResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pushed); err != nil {
		t.Fatalf("decode push response: %v", err)
	}
	if len(pushed.Results) != 1 || !pushed.Results[0].Accepted {
		t.Fatalf("push results = %+v", pushed.Results)
	}

	rec = env.do(t, "GET", "/v1/sync/pull?since=0", key, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pull status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, field := range []string{`"records"`, `"watermark"`, `"hasMore"`, `"wallTimeMillis"`, `"deviceId"`, `"deleted"`, `"payload"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("pull body misses %s: %s", field, body)
		}
	}
	var pulled pullResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pulled); err != nil {
		t.Fatalf("decode pull response: %v", err)
	}
	if len(pulled.Records) != 1 || pulled.Watermark != 1 || pulled.HasMore {
		t.Fatalf("pull = %+v", pulled)
	}
	if pulled.Records[0].Payload == nil || *pulled.Records[0].Payload != payload {
		t.Fatalf("payload did not survive: %+v", pulled.Records[0])
	}

	// A stale edit loses, and the client learns the winning marker.
	stale := record
	stale.Marker = store.Marker{WallTime: 500, Counter: 9, DeviceID: "dev-b"}
	rec = env.do(t, "POST", "/v1/sync/push", key, pushRequest{Records: []store.Record{stale}}, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &pushed)
	if pushed.Results[0].Accepted || pushed.Results[0].Marker.DeviceID != "dev-a" {
		t.Fatalf("stale push results = %+v", pushed.Results)
	}

	rec = env.do(t, "GET", "/v1/me", key, nil, "")
	if !strings.Contains(rec.Body.String(), `"recordCount":1`) {
		t.Fatalf("me = %s", rec.Body.String())
	}
}

func TestDeletionBeatsRecord(t *testing.T) {
	env := newTestEnv(t)
	key := env.library(t, "deletes")

	payload := "AQID"
	live := store.Record{
		Type:    "manga",
		Key:     "1|/manga/a",
		Marker:  store.Marker{WallTime: 1000, Counter: 0, DeviceID: "dev-a"},
		Payload: &payload,
	}
	env.do(t, "POST", "/v1/sync/push", key, pushRequest{Records: []store.Record{live}}, "")

	deleted := store.Record{
		Type:    "manga",
		Key:     "1|/manga/a",
		Marker:  store.Marker{WallTime: 2000, Counter: 0, DeviceID: "dev-b"},
		Deleted: true,
		Payload: &payload, // the server strips payloads from deletions
	}
	rec := env.do(t, "POST", "/v1/sync/push", key, pushRequest{Records: []store.Record{deleted}}, "")
	var pushed pushResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pushed); err != nil {
		t.Fatalf("decode push: %v", err)
	}
	if !pushed.Results[0].Accepted {
		t.Fatalf("deletion was rejected: %+v", pushed.Results)
	}

	rec = env.do(t, "GET", "/v1/sync/pull?since=1", key, nil, "")
	var pulled pullResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pulled); err != nil {
		t.Fatalf("decode pull: %v", err)
	}
	if len(pulled.Records) != 1 || !pulled.Records[0].Deleted || pulled.Records[0].Payload != nil {
		t.Fatalf("pulled deletion = %+v", pulled.Records)
	}

	rec = env.do(t, "GET", "/v1/me", key, nil, "")
	if !strings.Contains(rec.Body.String(), `"recordCount":0`) {
		t.Fatalf("me after deletion = %s", rec.Body.String())
	}
}

func TestRegisterWithInvite(t *testing.T) {
	env := newTestEnv(t)
	code, err := env.store.CreateInvite(context.Background(), "invitee", 1, time.Hour)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	rec := env.do(t, "POST", "/v1/register", "", map[string]string{"inviteCode": code}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("register status = %d: %s", rec.Code, rec.Body)
	}
	var created keyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if !strings.HasPrefix(created.APIKey, "mhk_") {
		t.Fatalf("register created %+v", created)
	}

	rec = env.do(t, "GET", "/v1/me", created.APIKey, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("me with registered key status = %d", rec.Code)
	}

	rec = env.do(t, "POST", "/v1/register", "", map[string]string{"inviteCode": code}, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second redemption status = %d, want 400", rec.Code)
	}

	rec = env.do(t, "POST", "/v1/register", "", map[string]string{"inviteCode": "inv_nope"}, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown invite status = %d, want 400", rec.Code)
	}
}

func TestPushValidation(t *testing.T) {
	env := newTestEnv(t)
	key := env.library(t, "validation")

	bad := pushRequest{Records: []store.Record{
		{Type: "", Key: "x", Marker: store.Marker{DeviceID: "dev-a"}},
	}}
	rec := env.do(t, "POST", "/v1/sync/push", key, bad, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing type status = %d, want 400", rec.Code)
	}

	bad = pushRequest{Records: []store.Record{
		{Type: "manga", Key: "x", Marker: store.Marker{DeviceID: ""}},
	}}
	rec = env.do(t, "POST", "/v1/sync/push", key, bad, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing device status = %d, want 400", rec.Code)
	}
}
