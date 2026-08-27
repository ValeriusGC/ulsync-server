package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// pullResponse is the JSON body of GET /v1/sync/pull.
//
// An empty page is encoded as envelopes:[] (never null) with next_cursor equal
// to the request since. A non-empty page sets next_cursor to the last row's
// server_seq, not since+limit, so sequence gaps cannot skip stored rows.
//
// Example:
//
//	{"envelopes":[{"id":"env-1","part":"full","server_seq":1,…}],"next_cursor":1}
type pullResponse struct {
	// Envelopes is this user's page after the cursor, ordered by server_seq.
	Envelopes []pullEnvelope `json:"envelopes"`
	// NextCursor is the since value the client should send on the next pull.
	NextCursor int64 `json:"next_cursor"`
}

// pullEnvelope is one stored envelope on the pull wire. It is a separate type
// from wireEnvelope because push omits server_seq (pointer + omitempty) while
// pull must always emit it, including zero, which would disappear on push.
type pullEnvelope struct {
	// ID is the client-stable record identity (SPEC §1.1).
	ID string `json:"id"`
	// Part names which slice of the record this row holds; identity is (id, part).
	Part string `json:"part"`
	// EntityType is a codec hint for the receiving client; the server stores it
	// verbatim and keeps no registry of allowed values.
	EntityType string `json:"entity_type"`
	// CreatedAtMS is milliseconds since Unix epoch; never updated after insert.
	CreatedAtMS int64 `json:"created_at_ms"`
	// LastEditedAtMS is milliseconds since Unix epoch of the last client edit.
	LastEditedAtMS int64 `json:"last_edited_at_ms"`
	// Revision is the client edit counter.
	Revision int64 `json:"revision"`
	// SourceID is the producing installation.
	SourceID string `json:"source_id"`
	// Flags carries protocol bits; round 1 stores them without interpreting deleted.
	Flags int64 `json:"flags"`
	// SchemaVersion is the payload format version on the producing client.
	SchemaVersion int64 `json:"schema_version"`
	// PayloadEncoding is copied as stored (for example "json"); not interpreted here.
	PayloadEncoding string `json:"payload_encoding"`
	// Payload is opaque bytes; encoding/json writes RFC 4648 section 4 base64.
	Payload []byte `json:"payload"`
	// ServerSeq is the per-user cursor assigned at write time; always present.
	ServerSeq int64 `json:"server_seq"`
}

// parseNonNegativeQueryInt reads a query parameter that must be a base-10
// integer in [0, 2^63). ok is false when the raw value is present but not
// such an integer; that is a client error, not a server failure.
// missing is true when the parameter is absent or the empty string so the
// caller can apply a default.
func parseNonNegativeQueryInt(raw string) (value int64, missing, ok bool) {
	if raw == "" {
		return 0, true, true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false, false
		}
	}
	n, err := strconv.ParseUint(raw, 10, 63)
	if err != nil {
		return 0, false, false
	}
	return int64(n), false, true
}

// pullHandler serves GET /v1/sync/pull for the authenticated user. defaultLimit
// and maxLimit come from sync.pull_limit_default and sync.pull_limit_max.
// A limit above maxLimit is truncated, not rejected. Unknown query parameters,
// including live, are ignored so step 06 can add live mode without changing this
// handler's parameter parsing.
func pullHandler(db *store.Store, defaultLimit, maxLimit int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		sinceVal, sinceMissing, sinceOK := parseNonNegativeQueryInt(q.Get("since"))
		if !sinceOK {
			writePushError(w, http.StatusBadRequest, map[string]string{
				"error": "invalid parameter",
				"param": "since",
			})
			return
		}
		since := sinceVal
		if sinceMissing {
			since = 0
		}

		limitVal, limitMissing, limitOK := parseNonNegativeQueryInt(q.Get("limit"))
		if !limitOK {
			writePushError(w, http.StatusBadRequest, map[string]string{
				"error": "invalid parameter",
				"param": "limit",
			})
			return
		}
		limit := defaultLimit
		if !limitMissing {
			limit = int(limitVal)
		}
		if limit == 0 {
			writePushError(w, http.StatusBadRequest, map[string]string{
				"error": "invalid parameter",
				"param": "limit",
			})
			return
		}
		if limit > maxLimit {
			limit = maxLimit
		}

		userID := UserID(r.Context())
		rows, cursor, err := db.Since(r.Context(), userID, since, limit)
		if err != nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}

		out := make([]pullEnvelope, 0, len(rows))
		for _, env := range rows {
			out = append(out, pullEnvelope{
				ID:              env.ID,
				Part:            env.Part,
				EntityType:      env.EntityType,
				CreatedAtMS:     env.CreatedAtMS,
				LastEditedAtMS:  env.LastEditedAtMS,
				Revision:        env.Revision,
				SourceID:        env.SourceID,
				Flags:           env.Flags,
				SchemaVersion:   env.SchemaVersion,
				PayloadEncoding: env.PayloadEncoding,
				Payload:         env.Payload,
				ServerSeq:       env.ServerSeq,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pullResponse{Envelopes: out, NextCursor: cursor})
	}
}
