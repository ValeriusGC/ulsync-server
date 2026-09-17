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

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/golang-jwt/jwt/v5"

	_ "modernc.org/sqlite"
)

func newBatchHTTPEnv(t *testing.T) *httpEnv {
	return newHTTPEnvWith(t, nil, func(cfg *config.Config) {
		cfg.Server.MaxBodyBytes = 1048576
		cfg.Sync.MaxEnvelopesPerPush = 500
	})
}

func loadPushFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(moduleRootHTTP(t), "protocol", "fixtures", "push", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return data
}

func countStoredEnvelopes(t *testing.T, env *httpEnv, userID string) int {
	t.Helper()
	stats, err := env.db.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	conn, err := sql.Open("sqlite", "file:"+stats.Path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer conn.Close()

	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM envelopes WHERE user_id = ?`, userID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return count
}

func TestPushBatch100DistinctKeys(t *testing.T) {
	t.Parallel()

	env := newBatchHTTPEnv(t)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	wires := make([]wireEnvelope, 100)
	for i := range wires {
		w := validWireEnvelope()
		w.ID = fmt.Sprintf("batch-id-%03d", i)
		wires[i] = w
	}
	body, err := json.Marshal(pushRequest{Envelopes: wires})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	rec := env.push(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var resp pushResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Results) != 100 {
		t.Fatalf("results len = %d, want 100", len(resp.Results))
	}
	for i, r := range resp.Results {
		if !r.Applied {
			t.Fatalf("results[%d].applied = false, want true", i)
		}
	}
	if count := countStoredEnvelopes(t, env, "alice"); count != 100 {
		t.Fatalf("stored count = %d, want 100", count)
	}
}

func TestPushBatchMalformedSecondEnvelope(t *testing.T) {
	t.Parallel()

	env := newBatchHTTPEnv(t)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	first := validWireEnvelope()
	second := validWireEnvelope()
	second.ID = ""
	body, err := json.Marshal(pushRequest{Envelopes: []wireEnvelope{first, second}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	rec := env.push(t, token, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if count := countStoredEnvelopes(t, env, "alice"); count != 0 {
		t.Fatalf("stored count = %d, want 0", count)
	}
}

func TestPushBatchDuplicateIdPart(t *testing.T) {
	t.Parallel()

	env := newBatchHTTPEnv(t)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	first := validWireEnvelope()
	second := validWireEnvelope()
	second.Revision = 2
	body, err := json.Marshal(pushRequest{Envelopes: []wireEnvelope{first, second}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	rec := env.push(t, token, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if count := countStoredEnvelopes(t, env, "alice"); count != 0 {
		t.Fatalf("stored count = %d, want 0", count)
	}
}

func TestPushBatchMixedApplied(t *testing.T) {
	t.Parallel()

	env := newBatchHTTPEnv(t)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	existing := validWireEnvelope()
	existing.ID = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	rec := env.push(t, token, pushBody(t, existing))
	assertPushOK(t, rec, true)

	newer := validWireEnvelope()
	newer.ID = "3f2504e0-4f89-11d3-9a0c-0305e82c3302"
	body, err := json.Marshal(pushRequest{Envelopes: []wireEnvelope{existing, newer}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	rec = env.push(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var resp pushResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Applied {
		t.Fatal("results[0].applied = true, want false (unchanged envelope loses LWW)")
	}
	if !resp.Results[1].Applied {
		t.Fatal("results[1].applied = false, want true")
	}
	if count := countStoredEnvelopes(t, env, "alice"); count != 2 {
		t.Fatalf("stored count = %d, want 2", count)
	}
}

func TestPushBatchProtocolFixture(t *testing.T) {
	t.Parallel()

	env := newBatchHTTPEnv(t)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	rec := env.push(t, token, loadPushFixture(t, "request_batch.json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if count := countStoredEnvelopes(t, env, "alice"); count != 2 {
		t.Fatalf("stored count = %d, want 2", count)
	}
}

func TestPushTwoPartsProtocolFixture(t *testing.T) {
	t.Parallel()

	env := newBatchHTTPEnv(t)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	rec := env.push(t, token, loadPushFixture(t, "request_two_parts.json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if count := countStoredEnvelopes(t, env, "alice"); count != 2 {
		t.Fatalf("stored count = %d, want 2", count)
	}

	full := readStoredEnvelope(t, env.db, "alice", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "full")
	done := readStoredEnvelope(t, env.db, "alice", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "done")
	if full.ServerSeq == 0 || done.ServerSeq == 0 {
		t.Fatalf("expected both parts stored: full seq=%d done seq=%d", full.ServerSeq, done.ServerSeq)
	}
}

func TestPushBatchPullReadsDonePart(t *testing.T) {
	t.Parallel()

	env := newBatchHTTPEnv(t)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	rec := env.push(t, token, loadPushFixture(t, "request_two_parts.json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("push status = %d", rec.Code)
	}

	pullRec := env.pull(t, token, "since=0&limit=100")
	if pullRec.Code != http.StatusOK {
		t.Fatalf("pull status = %d, body = %q", pullRec.Code, pullRec.Body.String())
	}
	body := pullRec.Body.String()
	if !strings.Contains(body, `"part":"done"`) || !strings.Contains(body, `"part":"full"`) {
		t.Fatalf("pull body missing done/full parts: %s", body)
	}
}
