// Package auth implements API-key authentication. An API key identifies an
// account; multiple devices share one key to sync with each other.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/nawocci/mihon-sync/internal/store"
)

// KeyPrefix makes keys recognizable in config files and logs.
const KeyPrefix = "mhk_"

// GenerateKey returns a new random API key. Only its SHA-256 hash is stored
// server-side, so the plaintext key is shown to the user exactly once.
func GenerateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

type ctxKey struct{}

// AccountIDFromContext returns the authenticated account id, or false.
func AccountIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(ctxKey{}).(int64)
	return id, ok
}

var ErrMissingKey = errors.New("missing or malformed Authorization header")
var ErrInvalidKey = errors.New("invalid API key")

// Authenticate resolves the Bearer key in the request to an account id.
func Authenticate(ctx context.Context, st *store.Store, r *http.Request) (int64, error) {
	header := r.Header.Get("Authorization")
	key, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || key == "" {
		return 0, ErrMissingKey
	}

	hash := HashKey(key)
	account, err := st.AccountByKeyHash(ctx, hash)
	if errors.Is(err, store.ErrAccountNotFound) {
		return 0, ErrInvalidKey
	}
	if err != nil {
		return 0, err
	}
	// Constant-time compare on top of the indexed lookup.
	if subtle.ConstantTimeCompare([]byte(account.KeyHash), []byte(hash)) != 1 {
		return 0, ErrInvalidKey
	}
	return account.ID, nil
}

// Middleware wraps a handler, requiring a valid API key and injecting the
// account id into the request context.
func Middleware(st *store.Store, writeError func(http.ResponseWriter, int, string)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := Authenticate(r.Context(), st, r)
			if errors.Is(err, ErrMissingKey) || errors.Is(err, ErrInvalidKey) {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "auth error")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
		})
	}
}
