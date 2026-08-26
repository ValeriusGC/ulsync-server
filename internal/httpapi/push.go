package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// Field length limits are server-side guards against oversized index keys and
// rows. They are not part of the wire contract in protocol/SPEC.md; without
// them a single multi-megabyte string field can bloat the database and indexes.
const (
	maxEnvelopeIDBytes              = 128
	maxEnvelopePartBytes            = 64
	maxEnvelopeEntityTypeBytes      = 128
	maxEnvelopeSourceIDBytes        = 128
	maxEnvelopePayloadEncodingBytes = 32
)

// pushRequest is the JSON body of POST /v1/sync/push.
//
// Example:
//
//	{"envelopes":[{...}]}
type pushRequest struct {
	// Envelopes is the batch the client wants to store. Round 1 accepts exactly
	// one element; the slice type stays so round 2 can raise max_envelopes_per_push
	// without changing the wire shape.
	Envelopes []wireEnvelope `json:"envelopes"`
}

// wireEnvelope is one envelope as it appears on the wire. There is no user_id
// field: ownership comes from the JWT sub placed in the context by requireBearer.
//
// The server does not validate entity_type or part against a registry and does
// not interpret payload bytes after base64 decoding (SPEC §8).
type wireEnvelope struct {
	ID              string `json:"id"`
	Part            string `json:"part"`
	EntityType      string `json:"entity_type"`
	CreatedAtMS     int64  `json:"created_at_ms"`
	LastEditedAtMS  int64  `json:"last_edited_at_ms"`
	Revision        int64  `json:"revision"`
	SourceID        string `json:"source_id"`
	Flags           int64  `json:"flags"`
	SchemaVersion   int64  `json:"schema_version"`
	PayloadEncoding string `json:"payload_encoding"`
	// Payload is RFC 4648 section 4 base64 (StdEncoding), not base64url.
	Payload string `json:"payload"`
	// ServerSeq is read from the wire and intentionally ignored per SPEC §1.3.
	// The server never uses a client-supplied sequence number for writes.
	ServerSeq *int64 `json:"server_seq,omitempty"`
}

// pushResponse is the JSON body of a successful push. Each result names the
// envelope and whether the upsert stored it.
//
// server_seq is physically absent from this type so clients cannot advance the
// pull cursor from a push response (SPEC §1.3).
//
// Example:
//
//	{"results":[{"id":"…","part":"full","applied":true}]}
type pushResponse struct {
	Results []pushResult `json:"results"`
}

// pushResult reports the outcome for one envelope in the request.
type pushResult struct {
	ID   string `json:"id"`
	Part string `json:"part"`
	// Applied is true when the incoming envelope won last-write-wins and was
	// stored. false means the server already holds a row that is not inferior;
	// the HTTP status is still 200 (SPEC §7) so clients do not retry forever.
	Applied bool `json:"applied"`
}

// pushHandler accepts envelopes for the authenticated user and returns whether
// each one won last-write-wins. maxEnvelopes comes from sync.max_envelopes_per_push.
//
// Validation rejects malformed wire fields before storage. Storage errors yield
// 503; a losing envelope yields applied:false with 200, never 409.
func pushHandler(db *store.Store, maxEnvelopes int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pushRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// limitPOSTBody wraps the body with MaxBytesReader; distinguish that
			// from malformed JSON so oversized requests get 413, not 400.
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			writePushError(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}

		switch {
		case len(req.Envelopes) == 0:
			writePushError(w, http.StatusBadRequest, map[string]string{"error": "no envelopes"})
			return
		case len(req.Envelopes) > maxEnvelopes:
			// The limit value is echoed so operators and clients know the configured cap.
			writePushError(w, http.StatusRequestEntityTooLarge, map[string]any{
				"error": "too many envelopes",
				"limit": maxEnvelopes,
			})
			return
		}

		// Round 1 accepts one envelope; the loop shape stays so round 2 only
		// changes the body of the loop, not the request format.
		results := make([]pushResult, 0, len(req.Envelopes))
		for _, wire := range req.Envelopes {
			env, field, err := wireToEnvelope(wire)
			if err != nil {
				writePushError(w, http.StatusBadRequest, map[string]string{
					"error": "invalid envelope",
					"field": field,
				})
				return
			}

			applied, err := db.Upsert(r.Context(), UserID(r.Context()), env)
			if err != nil {
				http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
				return
			}
			results = append(results, pushResult{
				ID:      env.ID,
				Part:    env.Part,
				Applied: applied,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pushResponse{Results: results})
	}
}

// wireToEnvelope validates wire fields, decodes payload from base64, and maps
// into the store type. The second return value is the first invalid field name
// for 400 responses; it is empty on success.
func wireToEnvelope(w wireEnvelope) (store.Envelope, string, error) {
	if field := validateWireEnvelope(w); field != "" {
		return store.Envelope{}, field, fmt.Errorf("invalid field %q", field)
	}

	// SPEC §1.1 requires standard base64 with padding, not base64url.
	payload, err := base64.StdEncoding.DecodeString(w.Payload)
	if err != nil {
		return store.Envelope{}, "payload", err
	}

	return store.Envelope{
		ID:              w.ID,
		Part:            w.Part,
		EntityType:      w.EntityType,
		CreatedAtMS:     w.CreatedAtMS,
		LastEditedAtMS:  w.LastEditedAtMS,
		Revision:        w.Revision,
		SourceID:        w.SourceID,
		Flags:           w.Flags,
		SchemaVersion:   w.SchemaVersion,
		PayloadEncoding: w.PayloadEncoding,
		Payload:         payload,
	}, "", nil
}

// validateWireEnvelope returns the name of the first invalid field, or "" when
// the envelope is acceptable. Length checks use byte length, not rune count, so
// limits match SQLite TEXT storage and index key size.
//
// entity_type and part are not checked against a registry: the server must stay
// ignorant of application semantics (SPEC §1.1, §8).
func validateWireEnvelope(w wireEnvelope) string {
	switch {
	case w.ID == "" || len([]byte(w.ID)) > maxEnvelopeIDBytes:
		return "id"
	case w.Part == "" || len([]byte(w.Part)) > maxEnvelopePartBytes:
		return "part"
	case w.EntityType == "" || len([]byte(w.EntityType)) > maxEnvelopeEntityTypeBytes:
		return "entity_type"
	case w.SourceID == "" || len([]byte(w.SourceID)) > maxEnvelopeSourceIDBytes:
		return "source_id"
	case w.PayloadEncoding == "" || len([]byte(w.PayloadEncoding)) > maxEnvelopePayloadEncodingBytes:
		return "payload_encoding"
	case w.CreatedAtMS <= 0:
		return "created_at_ms"
	case w.LastEditedAtMS <= 0:
		return "last_edited_at_ms"
	case w.Revision < 1:
		return "revision"
	case w.Payload == "":
		return "payload"
	default:
		return ""
	}
}

// writePushError sends a JSON error body with the given HTTP status. Push
// validation and batch-limit errors use this helper; auth failures and
// MaxBytesReader overflow use other shapes by design.
func writePushError(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
