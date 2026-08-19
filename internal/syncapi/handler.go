// Package syncapi exposes the HTTP API of the sync server.
package syncapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/nawocci/mihon-sync/internal/auth"
	"github.com/nawocci/mihon-sync/internal/config"
	"github.com/nawocci/mihon-sync/internal/store"
	"github.com/nawocci/mihon-sync/internal/web"
)

// maxPushBody bounds push payloads; an initial full-library upload can be
// large (tens of thousands of chapters).
const maxPushBody = 64 << 20

func NewHandler(st *store.Store, cfgs ...config.Config) http.Handler {
	var cfg config.Config
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	} else {
		cfg = config.Config{AllowRegistration: true}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/v1/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, serverInfoResponse{
			AllowRegistration: cfg.AllowRegistration,
			Version:           "0.1.0",
		})
	})

	mux.HandleFunc("POST /api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if !cfg.AllowRegistration {
			writeError(w, http.StatusForbidden, "registration is disabled on this server")
			return
		}
		var req registerRequest
		if r.Body != nil && r.ContentLength > 0 {
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
		}

		key, err := auth.GenerateKey()
		if err != nil {
			slog.Error("generate key failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to generate API key")
			return
		}

		if err := st.CreateAccount(r.Context(), auth.HashKey(key), req.Label); err != nil {
			slog.Error("create account failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create account")
			return
		}

		slog.Info("new account registered via web/api", "label", req.Label)
		writeJSON(w, http.StatusCreated, registerResponse{
			APIKey: key,
			Label:  req.Label,
		})
	})

	requireAuth := auth.Middleware(st, writeError)

	mux.Handle("GET /api/v1/auth/check", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})))

	mux.Handle("DELETE /api/v1/auth/account", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID, _ := auth.AccountIDFromContext(r.Context())
		if err := st.DeleteAccountByID(r.Context(), accountID); err != nil {
			slog.Error("delete account failed", "account", accountID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to delete account")
			return
		}
		slog.Info("account deleted via web/api", "account", accountID)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})))

	mux.Handle("POST /api/v1/sync/push", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlePush(w, r, st)
	})))
	mux.Handle("GET /api/v1/sync/pull", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlePull(w, r, st)
	})))
	mux.Handle("GET /api/v1/sync/status", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleStatus(w, r, st)
	})))

	// Serve embedded web frontend for everything else
	mux.Handle("/", web.Handler())

	return mux
}

func handlePush(w http.ResponseWriter, r *http.Request, st *store.Store) {
	accountID, _ := auth.AccountIDFromContext(r.Context())

	var req pushRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPushBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := validate(&req.Changes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Since < 0 {
		writeError(w, http.StatusBadRequest, "invalid 'since' value")
		return
	}

	rev, others, err := st.ApplyChanges(r.Context(), accountID, req.Since, req.DeviceID, dtoToStore(&req.Changes))
	if err != nil {
		slog.Error("push failed", "account", accountID, "device", req.DeviceID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to apply changes")
		return
	}
	slog.Debug("push applied", "account", accountID, "device", req.DeviceID, "rev", rev)
	writeJSON(w, http.StatusOK, pushResponse{Rev: rev, Changes: storeToDTO(others)})
}

func handlePull(w http.ResponseWriter, r *http.Request, st *store.Store) {
	accountID, _ := auth.AccountIDFromContext(r.Context())

	since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil || since < 0 {
		writeError(w, http.StatusBadRequest, "invalid or missing 'since' parameter")
		return
	}

	cs, rev, err := st.ChangesSince(r.Context(), accountID, since)
	if err != nil {
		slog.Error("pull failed", "account", accountID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch changes")
		return
	}
	writeJSON(w, http.StatusOK, pullResponse{Rev: rev, Changes: storeToDTO(cs)})
}

func handleStatus(w http.ResponseWriter, r *http.Request, st *store.Store) {
	accountID, _ := auth.AccountIDFromContext(r.Context())

	status, err := st.Status(r.Context(), accountID)
	if err != nil {
		slog.Error("status failed", "account", accountID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch status")
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Rev:              status.Rev,
		MangaCount:       status.MangaCount,
		ChapterCount:     status.ChapterCount,
		CategoryCount:    status.CategoryCount,
		HistoryCount:     status.HistoryCount,
		PreferenceCount:  status.PreferenceCount,
		DeviceCount:      status.DeviceCount,
		AccountCreatedAt: status.AccountCreatedAt,
	})
}

// validate enforces basic invariants so malformed entities can't poison the
// store.
func validate(cs *changeSetDTO) error {
	for _, m := range cs.Mangas {
		if m.URL == "" {
			return errString("manga entry missing url")
		}
	}
	for _, c := range cs.Chapters {
		if c.URL == "" || c.MangaURL == "" {
			return errString("chapter entry missing url or manga_url")
		}
	}
	for _, c := range cs.Categories {
		if c.Name == "" {
			return errString("category entry missing name")
		}
	}
	for _, mc := range cs.MangaCategories {
		if mc.Category == "" || mc.MangaURL == "" {
			return errString("manga_category entry missing category or manga_url")
		}
	}
	for _, h := range cs.History {
		if h.ChapterURL == "" || h.MangaURL == "" {
			return errString("history entry missing chapter_url or manga_url")
		}
	}
	for _, p := range cs.Preferences {
		if p.Key == "" {
			return errString("preference entry missing key")
		}
		if !p.Deleted && !json.Valid(p.Value) {
			return errString("preference " + strconv.Quote(p.Key) + " has invalid JSON value")
		}
	}
	return nil
}

type stringError string

func (e stringError) Error() string { return string(e) }
func errString(s string) error      { return stringError(s) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
