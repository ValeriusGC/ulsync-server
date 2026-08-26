package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// Field length limits are server-side guards against oversized index keys and
// rows. They are not part of the wire contract in protocol/SPEC.md.
const (
	maxEnvelopeIDBytes              = 128
	maxEnvelopePartBytes            = 64
	maxEnvelopeEntityTypeBytes      = 128
	maxEnvelopeSourceIDBytes        = 128
	maxEnvelopePayloadEncodingBytes = 32
)

type pushRequest struct {
	Envelopes []wireEnvelope `json:"envelopes"`
}

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
	Payload         string `json:"payload"`
	// ServerSeq is read from the wire and intentionally ignored per SPEC §1.3.
	// The server never uses a client-supplied sequence number for writes.
	ServerSeq *int64 `json:"server_seq,omitempty"`
}

type pushResponse struct {
	Results []pushResult `json:"results"`
}

type pushResult struct {
	ID      string `json:"id"`
	Part    string `json:"part"`
	Applied bool   `json:"applied"`
}

func pushHandler(db *store.Store, maxEnvelopes int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pushRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writePushError(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}

		switch {
		case len(req.Envelopes) == 0:
			writePushError(w, http.StatusBadRequest, map[string]string{"error": "no envelopes"})
			return
		case len(req.Envelopes) > maxEnvelopes:
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

func wireToEnvelope(w wireEnvelope) (store.Envelope, string, error) {
	if field := validateWireEnvelope(w); field != "" {
		return store.Envelope{}, field, fmt.Errorf("invalid field %q", field)
	}

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

func writePushError(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
