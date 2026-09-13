package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ValeriusGC/ulsync-server/internal/store"
	"github.com/golang-jwt/jwt/v5"

	_ "modernc.org/sqlite"
)

func TestDiffMissingKey(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	body := []byte(`{"items":[{"id":"no-such-id","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"}]}`)

	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	want := `{"missing":[{"id":"no-such-id","part":"full"}],"stale":[]}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestDiffStaleEqualRevisionNewerTime(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	seedDiffEnvelope(t, env.db, "alice", "env-1", "full", 1000, 2, "device-b")

	body := []byte(`{"items":[{"id":"env-1","part":"full","last_edited_at_ms":2000,"revision":2,"source_id":"device-a"}]}`)
	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}

	var resp diffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Missing) != 0 {
		t.Fatalf("missing = %+v, want empty", resp.Missing)
	}
	if len(resp.Stale) != 1 {
		t.Fatalf("stale len = %d, want 1", len(resp.Stale))
	}
	stale := resp.Stale[0]
	if stale.LastEditedAtMS != 1000 || stale.Revision != 2 || stale.SourceID != "device-b" {
		t.Fatalf("stale ranks = %+v, want server 1000/2/device-b", stale)
	}
}

func TestDiffStaleGreaterServerRevisionNewerClientTime(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	seedDiffEnvelope(t, env.db, "alice", "env-2", "full", 1000, 5, "device-b")

	body := []byte(`{"items":[{"id":"env-2","part":"full","last_edited_at_ms":2000,"revision":1,"source_id":"device-a"}]}`)
	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}

	var resp diffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Stale) != 1 {
		t.Fatalf("stale len = %d, want 1", len(resp.Stale))
	}
}

func TestDiffServerAhead(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	seedDiffEnvelope(t, env.db, "alice", "env-3", "full", 3000, 2, "device-a")

	body := []byte(`{"items":[{"id":"env-3","part":"full","last_edited_at_ms":2000,"revision":5,"source_id":"device-b"}]}`)
	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	want := `{"missing":[],"stale":[]}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestDiffFullTie(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	seedDiffEnvelope(t, env.db, "alice", "env-4", "full", 2000, 1, "device-a")

	body := []byte(`{"items":[{"id":"env-4","part":"full","last_edited_at_ms":2000,"revision":1,"source_id":"device-a"}]}`)
	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	want := `{"missing":[],"stale":[]}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestDiffEmptyResponseShape(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	body := []byte(`{"items":[{"id":"missing-only","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"}]}`)

	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != `{"missing":[{"id":"missing-only","part":"full"}],"stale":[]}`+"\n" {
		t.Fatalf("body = %q, want empty stale as [] not null", rec.Body.String())
	}
}

func TestDiffCrossUserIsolation(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	alice := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	bob := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("bob"))
	seedDiffEnvelope(t, env.db, "alice", "env-iso", "full", 1000, 1, "device-a")

	body := []byte(`{"items":[{"id":"env-iso","part":"full","last_edited_at_ms":1000,"revision":1,"source_id":"device-a"}]}`)
	rec := env.diff(t, bob, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	want := `{"missing":[{"id":"env-iso","part":"full"}],"stale":[]}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %q, want missing for other user", rec.Body.String())
	}

	rec = env.diff(t, alice, body)
	wantAlice := `{"missing":[],"stale":[]}`
	if strings.TrimSpace(rec.Body.String()) != wantAlice {
		t.Fatalf("alice body = %q, want tie", rec.Body.String())
	}
}

func TestDiffValidationErrors(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"empty_items", `{"items":[]}`, http.StatusBadRequest},
		{"empty_id", `{"items":[{"id":"","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"}]}`, http.StatusBadRequest},
		{"empty_source_id", `{"items":[{"id":"x","part":"full","last_edited_at_ms":1,"revision":1,"source_id":""}]}`, http.StatusBadRequest},
		{"string_revision", `{"items":[{"id":"x","part":"full","last_edited_at_ms":1,"revision":"3","source_id":"dev"}]}`, http.StatusBadRequest},
		{"negative_time", `{"items":[{"id":"x","part":"full","last_edited_at_ms":-1,"revision":1,"source_id":"dev"}]}`, http.StatusBadRequest},
		{"broken_json", `{`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.diff(t, token, []byte(tc.body))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestDiffTooManyItems(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	var items []string
	for i := 0; i < 501; i++ {
		items = append(items, fmt.Sprintf(`{"id":"id-%d","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"}`, i))
	}
	body := []byte(fmt.Sprintf(`{"items":[%s]}`, strings.Join(items, ",")))

	rec := env.diff(t, token, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestDiffUnauthorized(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	body := []byte(`{"items":[{"id":"x","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"}]}`)

	rec := env.diff(t, "", body)
	assertUnauthorized(t, rec)

	other := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "alice"})
	badToken, err := other.SignedString([]byte("wrong-secret"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	rec = env.diff(t, badToken, body)
	assertUnauthorized(t, rec)
}

func TestDiffDuplicateKeyOnce(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	body := []byte(`{"items":[{"id":"dup","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"},{"id":"dup","part":"full","last_edited_at_ms":2,"revision":2,"source_id":"dev"}]}`)

	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp diffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Missing) != 1 {
		t.Fatalf("missing len = %d, want 1", len(resp.Missing))
	}
}

func TestDiffResponseOrderMatchesRequest(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	seedDiffEnvelope(t, env.db, "alice", "second", "full", 1000, 1, "device-b")

	body := []byte(`{"items":[{"id":"first-missing","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"},{"id":"second","part":"full","last_edited_at_ms":2000,"revision":1,"source_id":"device-a"}]}`)
	rec := env.diff(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp diffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Missing) != 1 || resp.Missing[0].ID != "first-missing" {
		t.Fatalf("missing = %+v, want first-missing first", resp.Missing)
	}
	if len(resp.Stale) != 1 || resp.Stale[0].ID != "second" {
		t.Fatalf("stale = %+v, want second second", resp.Stale)
	}
}

func TestDiffReadOnly(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	seedDiffEnvelope(t, env.db, "alice", "ro-1", "full", 1000, 1, "device-a")

	beforeCount, beforeMaxSeq := envelopeStats(t, env.db)

	for i := 0; i < 5; i++ {
		body := []byte(fmt.Sprintf(`{"items":[{"id":"ro-%d","part":"full","last_edited_at_ms":1,"revision":1,"source_id":"dev"}]}`, i))
		rec := env.diff(t, token, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("diff %d status = %d", i, rec.Code)
		}
	}

	afterCount, afterMaxSeq := envelopeStats(t, env.db)
	if afterCount != beforeCount || afterMaxSeq != beforeMaxSeq {
		t.Fatalf("stats changed from (%d,%d) to (%d,%d)", beforeCount, beforeMaxSeq, afterCount, afterMaxSeq)
	}
}

func TestDiffProtocolFixtureGaps(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	seedDiffEnvelope(t, env.db, "alice", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "full", 1756100000000, 1, "device-a")
	seedDiffEnvelope(t, env.db, "alice", "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", "full", 1756000000000, 3, "device-b")
	seedDiffEnvelope(t, env.db, "alice", "2c7ae6a0-1f3b-4c8e-9d2a-6f4b8c1e0a73", "full", 1756000000000, 5, "device-b")

	reqBody := loadProtocolDiffFixture(t, "request.json")
	rec := env.diff(t, token, reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}

	wantBody := loadProtocolDiffFixture(t, "response_gaps.json")
	assertJSONEqual(t, rec.Body.Bytes(), wantBody)
}

func TestDiffProtocolFixtureTie(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	seedDiffEnvelope(t, env.db, "alice", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "full", 1756100000000, 1, "device-a")

	reqBody := loadProtocolDiffFixture(t, "request_tie.json")
	rec := env.diff(t, token, reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}

	wantBody := loadProtocolDiffFixture(t, "response_empty.json")
	assertJSONEqual(t, rec.Body.Bytes(), wantBody)
}

// seedDiffEnvelope stores one row with the given conflict ranks for diff tests.
func seedDiffEnvelope(t *testing.T, db *store.Store, userID, id, part string, editedAt, revision int64, sourceID string) {
	t.Helper()

	env := store.Envelope{
		ID:              id,
		Part:            part,
		EntityType:      "counter_operation",
		CreatedAtMS:     editedAt,
		LastEditedAtMS:  editedAt,
		Revision:        revision,
		SourceID:        sourceID,
		Flags:           0,
		SchemaVersion:   1,
		PayloadEncoding: "json",
		Payload:         []byte(`{}`),
	}
	applied, err := db.Upsert(context.Background(), userID, env)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if !applied {
		t.Fatalf("Upsert() applied = false, want true for seed row %q", id)
	}
}

// envelopeStats returns envelope row count and max server_seq for read-only tests.
func envelopeStats(t *testing.T, db *store.Store) (count int64, maxSeq int64) {
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

	if err := conn.QueryRow(`SELECT count(*), coalesce(max(server_seq), 0) FROM envelopes`).Scan(&count, &maxSeq); err != nil {
		t.Fatalf("query envelope stats: %v", err)
	}
	return count, maxSeq
}

// loadProtocolDiffFixture reads a golden diff JSON from the protocol submodule.
func loadProtocolDiffFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(moduleRootHTTP(t), "protocol", "fixtures", "diff", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return data
}

// moduleRootHTTP walks upward from the test working directory to find go.mod.
func moduleRootHTTP(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found while locating protocol fixture")
		}
		dir = parent
	}
}

// assertJSONEqual compares two JSON values after canonical re-encoding.
func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()

	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("Unmarshal(got) error = %v", err)
	}
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("Unmarshal(want) error = %v", err)
	}
	gotCanon, err := json.Marshal(gotVal)
	if err != nil {
		t.Fatalf("Marshal(got) error = %v", err)
	}
	wantCanon, err := json.Marshal(wantVal)
	if err != nil {
		t.Fatalf("Marshal(want) error = %v", err)
	}
	if string(gotCanon) != string(wantCanon) {
		t.Fatalf("body = %s, want %s", gotCanon, wantCanon)
	}
}
