package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// maxDiffItems is the maximum number of items in one POST /v1/sync/diff request
// (protocol/SPEC.md §3.4). It matches pull's maximum limit so clients have one
// batch size for the whole protocol. This value is fixed here, not in config:
// a YAML field would pull in the operations panel and CONFIG.md for one constant.
const maxDiffItems = 500

// diffRequest is the JSON body of POST /v1/sync/diff (SPEC §3.4).
//
// Example:
//
//	{"items":[{"id":"…","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"}]}
type diffRequest struct {
	// Items is the batch of client-held versions to compare. Empty is 400;
	// more than maxDiffItems is 413.
	Items []diffItem `json:"items"`
}

// diffItem carries one record key and the three conflict ranks as the client
// holds them. entity_type is intentionally absent: row identity is (id, part).
type diffItem struct {
	ID             string `json:"id"`
	Part           string `json:"part"`
	LastEditedAtMS int64  `json:"last_edited_at_ms"`
	Revision       int64  `json:"revision"`
	SourceID       string `json:"source_id"`
}

// diffResponse is the JSON body of a successful divergence check (SPEC §3.4).
// Both slices are always present; empty results serialize as [], never null.
//
// Example:
//
//	{"missing":[{"id":"…","part":"full"}],"stale":[]}
type diffResponse struct {
	Missing []diffMissingEntry `json:"missing"`
	Stale   []diffStaleEntry   `json:"stale"`
}

// diffMissingEntry names a key the server does not hold for this user.
type diffMissingEntry struct {
	ID   string `json:"id"`
	Part string `json:"part"`
}

// diffStaleEntry names a key whose stored row loses to the client copy by §2.
// The three rank fields are the server's values, not the client's.
type diffStaleEntry struct {
	ID             string `json:"id"`
	Part           string `json:"part"`
	LastEditedAtMS int64  `json:"last_edited_at_ms"`
	Revision       int64  `json:"revision"`
	SourceID       string `json:"source_id"`
}

// diffHandler compares client-held conflict ranks against stored rows for the
// authenticated user. It never writes envelopes and never allocates server_seq:
// StoredRanks uses the read pool only, so a long batch cannot block the writer.
//
// Ranking is delegated entirely to store.Wins; this handler does not implement
// its own comparison. A tie or a server win appears in neither output list.
func diffHandler(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req diffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			writeDiffError(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}

		switch {
		case len(req.Items) == 0:
			writeDiffError(w, http.StatusBadRequest, map[string]string{"error": "no items"})
			return
		case len(req.Items) > maxDiffItems:
			writeDiffError(w, http.StatusRequestEntityTooLarge, map[string]any{
				"error": "too many items",
				"limit": maxDiffItems,
			})
			return
		}

		order, incomingByKey, field, err := validateDiffItems(req.Items)
		if err != nil {
			writeDiffError(w, http.StatusBadRequest, map[string]string{
				"error": "invalid item",
				"field": field,
			})
			return
		}

		keys := make([]store.Key, 0, len(order))
		for _, key := range order {
			keys = append(keys, key)
		}

		stored, err := db.StoredRanks(r.Context(), UserID(r.Context()), keys)
		if err != nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}

		resp := diffResponse{
			Missing: make([]diffMissingEntry, 0),
			Stale:   make([]diffStaleEntry, 0),
		}

		for _, key := range order {
			item := incomingByKey[key]
			incoming := store.Ranks{
				LastEditedAtMS: item.LastEditedAtMS,
				Revision:       item.Revision,
				SourceID:       item.SourceID,
			}

			serverRanks, ok := stored[key]
			if !ok {
				resp.Missing = append(resp.Missing, diffMissingEntry{
					ID:   key.ID,
					Part: key.Part,
				})
				continue
			}
			if store.Wins(incoming, serverRanks) {
				resp.Stale = append(resp.Stale, diffStaleEntry{
					ID:             key.ID,
					Part:           key.Part,
					LastEditedAtMS: serverRanks.LastEditedAtMS,
					Revision:       serverRanks.Revision,
					SourceID:       serverRanks.SourceID,
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// validateDiffItems checks every item and returns first-seen order with deduped
// keys. Repeated keys in the request are answered once using the first ranks.
func validateDiffItems(items []diffItem) ([]store.Key, map[store.Key]diffItem, string, error) {
	seen := make(map[store.Key]bool, len(items))
	order := make([]store.Key, 0, len(items))
	incomingByKey := make(map[store.Key]diffItem, len(items))

	for i := range items {
		item := items[i]
		if field := validateDiffItem(item); field != "" {
			return nil, nil, field, errors.New("invalid diff item")
		}
		key := store.Key{ID: item.ID, Part: item.Part}
		if seen[key] {
			continue
		}
		seen[key] = true
		order = append(order, key)
		incomingByKey[key] = item
	}
	return order, incomingByKey, "", nil
}

// validateDiffItem returns the name of the first invalid field, or "" when the
// item is acceptable. Length limits match push so oversized index keys cannot
// enter the divergence check path.
func validateDiffItem(item diffItem) string {
	switch {
	case item.ID == "" || len([]byte(item.ID)) > maxEnvelopeIDBytes:
		return "id"
	case item.Part == "" || len([]byte(item.Part)) > maxEnvelopePartBytes:
		return "part"
	case item.SourceID == "" || len([]byte(item.SourceID)) > maxEnvelopeSourceIDBytes:
		return "source_id"
	case item.LastEditedAtMS < 0:
		return "last_edited_at_ms"
	case item.Revision < 0:
		return "revision"
	default:
		return ""
	}
}

// writeDiffError sends a JSON error body with the given HTTP status.
func writeDiffError(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
