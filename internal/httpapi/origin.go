package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// headerUlsyncOrigin is the application contour header from SPEC §1.5. It is
// not the CORS Origin header and must not be confused with it in middleware.
const headerUlsyncOrigin = "Ulsync-Origin"

// originKey stores the resolved store origin after requireOrigin succeeds.
type originKey struct{}

// originInvalidBody is the 400 body when Ulsync-Origin is present but not in
// the allowed character class or longer than 256 characters. Bytes match
// protocol/fixtures/origin/origin_invalid.json.
const originInvalidBody = `{
  "error": "origin_invalid"
}`

// originRequiredBody matches protocol/fixtures/origin/origin_required.json.
const originRequiredBody = `{
  "error": "origin_required"
}`

// requireOrigin wraps sync routes so every /v1/sync/* request compares
// Ulsync-Origin after bearer auth. GET /v1/whoami and GET /health never pass
// through this gate.
//
// imprint is true only for GET /v1/sync/hello. If middleware called hello with
// imprint=false, an open empty store would answer 400 on the handshake itself.
func requireOrigin(cfg *config.Config, db *store.Store, next http.Handler) http.Handler {
	authored := strings.TrimSpace(cfg.Origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.Header.Get(headerUlsyncOrigin))
		request := raw
		if raw != "" {
			if err := config.ValidateOrigin(raw); err != nil {
				writeOriginInvalid(w)
				return
			}
		}

		imprint := r.Method == http.MethodGet && r.URL.Path == "/v1/sync/hello"
		origin, err := db.EnsureOrigin(r.Context(), authored, request, imprint)
		if err != nil {
			writeOriginError(w, err)
			return
		}

		ctx := context.WithValue(r.Context(), originKey{}, origin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// storeOrigin returns the origin resolved by requireOrigin for this request.
func storeOrigin(ctx context.Context) string {
	origin, ok := ctx.Value(originKey{}).(string)
	if !ok {
		panic("httpapi.storeOrigin: origin middleware did not run")
	}
	return origin
}

// helloResponse is the JSON body of GET /v1/sync/hello on HTTP 200 (SPEC §3.5).
// Field order matches protocol/fixtures/origin/hello_response.json so golden
// readers see origin before user_id before server_now_ms.
type helloResponse struct {
	// Origin is the store contour held after imprint or configuration.
	Origin string `json:"origin"`
	// UserID is the verified JWT sub so the client can confirm the account.
	UserID string `json:"user_id"`
	// ServerNowMS is the store clock sample at this response (SPEC §Server clock).
	ServerNowMS int64 `json:"server_now_ms"`
}

// helloHandler serves GET /v1/sync/hello. Imprinting already happened in
// requireOrigin; this handler only echoes the store origin and authenticated
// subject so the client can confirm account and contour before mail.
func helloHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(helloResponse{
		Origin:      storeOrigin(r.Context()),
		UserID:      UserID(r.Context()),
		ServerNowMS: serverNowMs(),
	})
}

func writeOriginInvalid(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(originInvalidBody))
}

func writeOriginError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrOriginRequired) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(originRequiredBody))
		return
	}
	if match := originMismatch(err); match != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(formatMismatchBody(match.StoreOrigin, match.RequestOrigin))
		return
	}
	http.Error(w, "origin check failed", http.StatusInternalServerError)
}

func originMismatch(err error) *store.OriginMismatchError {
	var match *store.OriginMismatchError
	if errors.As(err, &match) {
		return match
	}
	return nil
}

// formatMismatchBody matches protocol/fixtures/origin/mismatch.json field order.
func formatMismatchBody(storeOrigin, requestOrigin string) []byte {
	return []byte(`{
  "error": "origin_mismatch",
  "store_origin": "` + storeOrigin + `",
  "request_origin": "` + requestOrigin + `"
}`)
}
