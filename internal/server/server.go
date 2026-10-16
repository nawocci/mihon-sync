// Package server exposes the wire protocol and the admin dashboard.
package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"mihon-sync/internal/store"
)

//go:embed dashboard.html
var dashboardHTML []byte

const (
	maxBodyBytes   = 8 << 20
	maxPushRecords = 500
)

// Config carries operator choices into the server.
type Config struct {
	// BootstrapNetwork decides who may generate keys from the web: "loopback"
	// (default), "private" or "any".
	BootstrapNetwork string
	Version          string
}

type Server struct {
	store *store.Store
	cfg   Config
	log   *slog.Logger
	mux   *http.ServeMux
}

func New(st *store.Store, cfg Config, logger *slog.Logger) *Server {
	s := &Server{store: st, cfg: cfg, log: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /bootstrap/status", s.handleBootstrapStatus)
	mux.HandleFunc("POST /bootstrap/key", s.handleBootstrapKey)
	mux.HandleFunc("POST /v1/register", s.handleRegister)
	mux.HandleFunc("GET /v1/me", s.authenticated(s.handleMe))
	mux.HandleFunc("POST /v1/sync/push", s.authenticated(s.handlePush))
	mux.HandleFunc("GET /v1/sync/pull", s.authenticated(s.handlePull))
	s.mux = mux
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

type accountInfo struct {
	Label       string `json:"label"`
	CreatedAt   int64  `json:"createdAt"`
	RecordCount int64  `json:"recordCount"`
}

type pushRequest struct {
	Records []store.Record `json:"records"`
}

type pushResponse struct {
	Results []store.PushResult `json:"results"`
}

type pullResponse struct {
	Records   []store.Record `json:"records"`
	Watermark int64          `json:"watermark"`
	HasMore   bool           `json:"hasMore"`
}

type registerRequest struct {
	InviteCode string `json:"inviteCode"`
	Label      string `json:"label"`
}

type keyResponse struct {
	APIKey    string `json:"apiKey"`
	Label     string `json:"label"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.cfg.Version})
}

func (s *Server) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.LibraryCount(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": s.bootstrapAllowed(r),
		"libraries": count,
		"version":   s.cfg.Version,
	})
}

func (s *Server) handleBootstrapKey(w http.ResponseWriter, r *http.Request) {
	if !s.bootstrapAllowed(r) {
		writeError(w, http.StatusForbidden, "bootstrap is only allowed from local networks")
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if err := decodeJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	key, lib, err := s.store.CreateLibrary(r.Context(), strings.TrimSpace(req.Label))
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keyResponse{APIKey: key, Label: lib.Label, CreatedAt: lib.CreatedAt})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	key, lib, err := s.store.RedeemInvite(r.Context(), strings.TrimSpace(req.InviteCode), strings.TrimSpace(req.Label))
	if errors.Is(err, store.ErrInvalidInvite) {
		writeError(w, http.StatusBadRequest, "invalid or expired invite")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keyResponse{APIKey: key, Label: lib.Label, CreatedAt: lib.CreatedAt})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, lib store.Library) {
	count, err := s.store.RecordCount(r.Context(), lib.ID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountInfo{Label: lib.Label, CreatedAt: lib.CreatedAt, RecordCount: count})
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request, lib store.Library) {
	var req pushRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Records) > maxPushRecords {
		writeError(w, http.StatusRequestEntityTooLarge, "too many records in one push")
		return
	}
	for i := range req.Records {
		rec := &req.Records[i]
		if rec.Type == "" || rec.Key == "" || rec.Marker.DeviceID == "" {
			writeError(w, http.StatusBadRequest, "records need a type, a key and a device id")
			return
		}
		if rec.Deleted {
			rec.Payload = nil
		}
	}

	results, _, err := s.store.Push(r.Context(), lib.ID, req.Records)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pushResponse{Results: results})
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request, lib store.Library) {
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))

	records, watermark, hasMore, err := s.store.Pull(r.Context(), lib.ID, since, limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pullResponse{Records: records, Watermark: watermark, HasMore: hasMore})
}

type authedHandler func(w http.ResponseWriter, r *http.Request, lib store.Library)

func (s *Server) authenticated(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := bearerToken(r)
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key")
			return
		}
		lib, err := s.store.Authenticate(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		if err != nil {
			s.internalError(w, err)
			return
		}
		next(w, r, lib)
	}
}

func (s *Server) bootstrapAllowed(r *http.Request) bool {
	ip := remoteIP(r)
	if ip == nil {
		return false
	}
	switch s.cfg.BootstrapNetwork {
	case "any":
		return true
	case "private":
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	default:
		return ip.IsLoopback()
	}
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	return net.ParseIP(host)
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
