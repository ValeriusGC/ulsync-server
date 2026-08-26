package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ValeriusGC/ulsync-server/internal/store"
	"github.com/golang-jwt/jwt/v5"

	_ "modernc.org/sqlite"
)

func TestPushNewEnvelope(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	body := pushBody(t, validWireEnvelope())

	rec := env.push(t, token, body)
	assertPushOK(t, rec, true)

	row := readStoredEnvelope(t, env.db, "alice", "env-1", "full")
	if row.ServerSeq != 1 {
		t.Fatalf("server_seq = %d, want 1", row.ServerSeq)
	}
}

func TestPushRetrySameEnvelope(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	body := pushBody(t, validWireEnvelope())

	first := env.push(t, token, body)
	assertPushOK(t, first, true)
	rowAfterFirst := readStoredEnvelope(t, env.db, "alice", "env-1", "full")

	second := env.push(t, token, body)
	assertPushOK(t, second, false)

	rowAfterSecond := readStoredEnvelope(t, env.db, "alice", "env-1", "full")
	if rowAfterSecond.ServerSeq != rowAfterFirst.ServerSeq {
		t.Fatalf("server_seq changed from %d to %d on retry", rowAfterFirst.ServerSeq, rowAfterSecond.ServerSeq)
	}
}

func TestPushSameIDNewerTime(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	first := validWireEnvelope()
	first.Payload = base64.StdEncoding.EncodeToString([]byte("first"))
	rec := env.push(t, token, pushBody(t, first))
	assertPushOK(t, rec, true)
	seqBefore := readStoredEnvelope(t, env.db, "alice", "env-1", "full").ServerSeq

	second := validWireEnvelope()
	second.LastEditedAtMS = 2000
	second.Payload = base64.StdEncoding.EncodeToString([]byte("second"))
	rec = env.push(t, token, pushBody(t, second))
	assertPushOK(t, rec, true)

	row := readStoredEnvelope(t, env.db, "alice", "env-1", "full")
	if string(row.Payload) != "second" {
		t.Fatalf("payload = %q, want second", row.Payload)
	}
	if row.ServerSeq <= seqBefore {
		t.Fatalf("server_seq = %d, want greater than %d", row.ServerSeq, seqBefore)
	}
}

func TestPushSameIDOlderTime(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	first := validWireEnvelope()
	first.LastEditedAtMS = 2000
	first.Payload = base64.StdEncoding.EncodeToString([]byte("first"))
	if rec := env.push(t, token, pushBody(t, first)); rec.Code != http.StatusOK {
		t.Fatalf("first push status = %d", rec.Code)
	}

	second := validWireEnvelope()
	second.LastEditedAtMS = 1000
	second.Payload = base64.StdEncoding.EncodeToString([]byte("second"))
	rec := env.push(t, token, pushBody(t, second))
	assertPushOK(t, rec, false)

	row := readStoredEnvelope(t, env.db, "alice", "env-1", "full")
	if string(row.Payload) != "first" {
		t.Fatalf("payload = %q, want first", row.Payload)
	}
}

func TestPushEqualTimeHigherRevision(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	first := validWireEnvelope()
	first.Revision = 1
	if rec := env.push(t, token, pushBody(t, first)); rec.Code != http.StatusOK {
		t.Fatalf("first push status = %d", rec.Code)
	}

	second := validWireEnvelope()
	second.Revision = 2
	second.Payload = base64.StdEncoding.EncodeToString([]byte("rev-2"))
	rec := env.push(t, token, pushBody(t, second))
	assertPushOK(t, rec, true)

	row := readStoredEnvelope(t, env.db, "alice", "env-1", "full")
	if row.Revision != 2 {
		t.Fatalf("revision = %d, want 2", row.Revision)
	}
}

func TestPushArrivalOrderIndependence(t *testing.T) {
	t.Parallel()

	runOrder := func(t *testing.T, firstSource, secondSource string) storedEnvelope {
		t.Helper()
		env := newHTTPEnv(t, nil)
		token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

		payload := base64.StdEncoding.EncodeToString([]byte("same-payload"))
		first := validWireEnvelope()
		first.ID = "shared-id"
		first.SourceID = firstSource
		first.LastEditedAtMS = 3000
		first.Revision = 1
		first.Payload = payload

		second := validWireEnvelope()
		second.ID = "shared-id"
		second.SourceID = secondSource
		second.LastEditedAtMS = 3000
		second.Revision = 1
		second.Payload = payload

		if rec := env.push(t, token, pushBody(t, first)); rec.Code != http.StatusOK {
			t.Fatalf("first push status = %d", rec.Code)
		}
		if rec := env.push(t, token, pushBody(t, second)); rec.Code != http.StatusOK {
			t.Fatalf("second push status = %d", rec.Code)
		}
		return readStoredEnvelope(t, env.db, "alice", "shared-id", "full")
	}

	finalAB := runOrder(t, "device-a", "device-b")
	finalBA := runOrder(t, "device-b", "device-a")

	if finalAB.SourceID != "device-b" || finalBA.SourceID != "device-b" {
		t.Fatalf("winners = (%q, %q), want device-b", finalAB.SourceID, finalBA.SourceID)
	}
	if string(finalAB.Payload) != string(finalBA.Payload) {
		t.Fatalf("payloads differ across arrival orders")
	}
}

func TestPushCrossUserIsolation(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	alice := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	bob := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("bob"))
	body := pushBody(t, validWireEnvelope())

	if rec := env.push(t, alice, body); rec.Code != http.StatusOK {
		t.Fatalf("alice push status = %d", rec.Code)
	}
	if rec := env.push(t, bob, body); rec.Code != http.StatusOK {
		t.Fatalf("bob push status = %d", rec.Code)
	}

	aliceRow := readStoredEnvelope(t, env.db, "alice", "env-1", "full")
	bobRow := readStoredEnvelope(t, env.db, "bob", "env-1", "full")
	if aliceRow.ServerSeq != 1 || bobRow.ServerSeq != 1 {
		t.Fatalf("server_seq = (%d, %d), want (1, 1)", aliceRow.ServerSeq, bobRow.ServerSeq)
	}
}

func TestPushTooManyEnvelopes(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	envWire := validWireEnvelope()
	raw, err := json.Marshal(envWire)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := []byte(fmt.Sprintf(`{"envelopes":[%s,%s]}`, raw, raw))

	rec := env.push(t, token, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var errBody struct {
		Error string `json:"error"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if errBody.Error != "too many envelopes" || errBody.Limit != 1 {
		t.Fatalf("body = %+v, want too many envelopes with limit 1", errBody)
	}
}

func TestPushNoEnvelopes(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.push(t, token, []byte(`{"envelopes":[]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPushValidation(t *testing.T) {
	t.Parallel()

	long129 := strings.Repeat("a", 129)
	long65 := strings.Repeat("b", 65)
	long33 := strings.Repeat("c", 33)

	tests := []struct {
		name   string
		field  string
		mutate func(*wireEnvelope)
	}{
		{"empty_id", "id", func(w *wireEnvelope) { w.ID = "" }},
		{"long_id", "id", func(w *wireEnvelope) { w.ID = long129 }},
		{"empty_part", "part", func(w *wireEnvelope) { w.Part = "" }},
		{"long_part", "part", func(w *wireEnvelope) { w.Part = long65 }},
		{"empty_entity_type", "entity_type", func(w *wireEnvelope) { w.EntityType = "" }},
		{"long_entity_type", "entity_type", func(w *wireEnvelope) { w.EntityType = long129 }},
		{"empty_source_id", "source_id", func(w *wireEnvelope) { w.SourceID = "" }},
		{"long_source_id", "source_id", func(w *wireEnvelope) { w.SourceID = long129 }},
		{"empty_payload_encoding", "payload_encoding", func(w *wireEnvelope) { w.PayloadEncoding = "" }},
		{"long_payload_encoding", "payload_encoding", func(w *wireEnvelope) { w.PayloadEncoding = long33 }},
		{"zero_created_at", "created_at_ms", func(w *wireEnvelope) { w.CreatedAtMS = 0 }},
		{"zero_last_edited_at", "last_edited_at_ms", func(w *wireEnvelope) { w.LastEditedAtMS = 0 }},
		{"revision_zero", "revision", func(w *wireEnvelope) { w.Revision = 0 }},
		{"invalid_base64_payload", "payload", func(w *wireEnvelope) { w.Payload = "!!!" }},
	}

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := validWireEnvelope()
			tc.mutate(&w)
			rec := env.push(t, token, pushBody(t, w))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var errBody struct {
				Error string `json:"error"`
				Field string `json:"field"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if errBody.Error != "invalid envelope" || errBody.Field != tc.field {
				t.Fatalf("body = %+v, want invalid envelope field %q", errBody, tc.field)
			}
		})
	}
}

func TestPushOversizedBody(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	w := validWireEnvelope()
	w.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 900)))
	body := pushBody(t, w)
	if len(body) <= 1024 {
		t.Fatalf("body len = %d, want > 1024 to exceed test MaxBodyBytes", len(body))
	}

	rec := env.push(t, token, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestPushUnauthorized(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/sync/push", strings.NewReader(`{"envelopes":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rec, req)
	assertUnauthorized(t, rec)
}

func TestPushResponseOmitsServerSeq(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.push(t, token, pushBody(t, validWireEnvelope()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "server_seq") {
		t.Fatalf("push response must not contain server_seq: %s", rec.Body.String())
	}
}

type storedEnvelope struct {
	ServerSeq int64
	SourceID  string
	Revision  int64
	Payload   []byte
}

func validWireEnvelope() wireEnvelope {
	return wireEnvelope{
		ID:              "env-1",
		Part:            "full",
		EntityType:      "counter_operation",
		CreatedAtMS:     1000,
		LastEditedAtMS:  1000,
		Revision:        1,
		SourceID:        "device-a",
		Flags:           0,
		SchemaVersion:   1,
		PayloadEncoding: "json",
		Payload:         base64.StdEncoding.EncodeToString([]byte(`{"type":"increment"}`)),
	}
}

func pushBody(t *testing.T, env wireEnvelope) []byte {
	t.Helper()
	body, err := json.Marshal(pushRequest{Envelopes: []wireEnvelope{env}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return body
}

func assertPushOK(t *testing.T, rec *httptest.ResponseRecorder, wantApplied bool) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var resp pushResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Applied != wantApplied {
		t.Fatalf("applied = %v, want %v", resp.Results[0].Applied, wantApplied)
	}
}

func readStoredEnvelope(t *testing.T, db *store.Store, userID, id, part string) storedEnvelope {
	t.Helper()

	stats, err := db.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	conn, err := sql.Open("sqlite", "file:"+stats.Path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer conn.Close()

	var row storedEnvelope
	err = conn.QueryRow(`
		SELECT server_seq, source_id, revision, payload
		FROM envelopes
		WHERE user_id = ? AND id = ? AND part = ?
	`, userID, id, part).Scan(&row.ServerSeq, &row.SourceID, &row.Revision, &row.Payload)
	if err != nil {
		t.Fatalf("query envelope: %v", err)
	}
	return row
}

func loadProtocolFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("protocol", "fixtures", "envelope", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return data
}
