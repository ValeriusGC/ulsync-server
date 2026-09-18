package httpapi

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

const (
	originFixturePrimary  = "com.example.app/7c3e9a12-4b56-4d8e-9f01-2a3b4c5d6e7f"
	originFixtureOther    = "com.example.other/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	originFixtureAuthored = "com.example.authored/11111111-2222-3333-4444-555555555555"
)

func readProtocolFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "protocol", "fixtures", "origin", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

func readServerMetaOrigin(t *testing.T, env *httpEnv) string {
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
	var value string
	err = conn.QueryRow(`SELECT v FROM server_meta WHERE k = 'origin'`).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return ""
		}
		t.Fatalf("query server_meta: %v", err)
	}
	return value
}

func envelopeTableStats(t *testing.T, env *httpEnv) (count int64, maxSeq sql.NullInt64) {
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
	if err := conn.QueryRow(`SELECT COUNT(*) FROM envelopes`).Scan(&count); err != nil {
		t.Fatalf("count envelopes: %v", err)
	}
	_ = conn.QueryRow(`SELECT MAX(server_seq) FROM envelopes`).Scan(&maxSeq)
	return count, maxSeq
}

func TestHelloImprintsOpenStore(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.hello(t, token, originFixturePrimary)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	assertHelloBody(t, rec.Body.Bytes(), originFixturePrimary, "alice")
	if got := readServerMetaOrigin(t, env); got != originFixturePrimary {
		t.Fatalf("server_meta origin = %q, want %q", got, originFixturePrimary)
	}
}

func TestHelloRepeatSameOrigin(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	if rec := env.hello(t, token, originFixturePrimary); rec.Code != 200 {
		t.Fatalf("first hello status = %d", rec.Code)
	}
	if rec := env.hello(t, token, originFixturePrimary); rec.Code != 200 {
		t.Fatalf("second hello status = %d", rec.Code)
	}
	stats, err := env.db.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Envelopes != 0 {
		t.Fatalf("envelopes = %d, want 0", stats.Envelopes)
	}
}

func TestHelloDifferentOriginConflict(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	if rec := env.hello(t, token, originFixturePrimary); rec.Code != 200 {
		t.Fatalf("first hello status = %d", rec.Code)
	}
	rec := env.hello(t, token, originFixtureOther)
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	wantBody := readProtocolFixture(t, "mismatch.json")
	if strings.TrimSpace(rec.Body.String()) != wantBody {
		t.Fatalf("body = %q, want fixture %q", rec.Body.String(), wantBody)
	}
	if stats, _ := env.db.Stats(context.Background()); stats.Envelopes != 0 {
		t.Fatalf("envelopes = %d, want 0", stats.Envelopes)
	}
}

func TestPushDifferentOriginAfterImprint(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	if rec := env.hello(t, token, originFixturePrimary); rec.Code != 200 {
		t.Fatalf("hello status = %d", rec.Code)
	}
	rec := env.pushWithOrigin(t, token, originFixtureOther, pushBody(t, validWireEnvelope()))
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	stats, err := env.db.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Envelopes != 0 {
		t.Fatalf("envelopes = %d, want 0", stats.Envelopes)
	}
}

func TestPushWithoutHeaderOnImprintedOpenStoreLegacy(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	if rec := env.hello(t, token, originFixturePrimary); rec.Code != 200 {
		t.Fatalf("hello status = %d", rec.Code)
	}
	rec := env.push(t, token, pushBody(t, validWireEnvelope()))
	assertPushOK(t, rec, true)
}

func TestHelloWithoutHeaderRequired(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.hello(t, token, "")
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	wantBody := readProtocolFixture(t, "origin_required.json")
	if strings.TrimSpace(rec.Body.String()) != wantBody {
		t.Fatalf("body = %q, want %q", rec.Body.String(), wantBody)
	}
}

func TestAuthoredStoreRejectsForeignOrigin(t *testing.T) {
	t.Parallel()

	env := newHTTPEnvWith(t, nil, func(cfg *config.Config) {
		cfg.Origin = originFixtureAuthored
	})
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.hello(t, token, originFixtureOther)
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestAuthoredStorePushWithoutHeaderRequired(t *testing.T) {
	t.Parallel()

	env := newHTTPEnvWith(t, nil, func(cfg *config.Config) {
		cfg.Origin = originFixtureAuthored
	})
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.push(t, token, pushBody(t, validWireEnvelope()))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	wantBody := readProtocolFixture(t, "origin_required.json")
	if strings.TrimSpace(rec.Body.String()) != wantBody {
		t.Fatalf("body = %q, want %q", rec.Body.String(), wantBody)
	}
}

func TestAuthoredStoreMatchingOriginThenPush(t *testing.T) {
	t.Parallel()

	env := newHTTPEnvWith(t, nil, func(cfg *config.Config) {
		cfg.Origin = originFixtureAuthored
	})
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	if rec := env.hello(t, token, originFixtureAuthored); rec.Code != 200 {
		t.Fatalf("hello status = %d", rec.Code)
	}
	rec := env.pushWithOrigin(t, token, originFixtureAuthored, pushBody(t, validWireEnvelope()))
	assertPushOK(t, rec, true)
}

func TestHelloWithoutBearerDoesNotWriteMeta(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	rec := env.hello(t, "", originFixturePrimary)
	assertUnauthorized(t, rec)
	if got := readServerMetaOrigin(t, env); got != "" {
		t.Fatalf("server_meta origin = %q, want empty", got)
	}
}

func TestOriginInvalidHeaderRejected(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	wantBody := readProtocolFixture(t, "origin_invalid.json")
	cases := []string{
		"has space inside",
		"кириллица",
		"a/" + strings.Repeat("b", 256),
	}
	for _, origin := range cases {
		rec := env.hello(t, token, origin)
		if rec.Code != 400 {
			t.Fatalf("origin %q: status = %d, want 400", origin, rec.Code)
		}
		if strings.TrimSpace(rec.Body.String()) != wantBody {
			t.Fatalf("origin %q: body = %q, want %q", origin, rec.Body.String(), wantBody)
		}
	}
}

func TestHelloSeriesDoesNotTouchEnvelopes(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	beforeCount, beforeSeq := envelopeTableStats(t, env)
	for range 5 {
		if rec := env.hello(t, token, originFixturePrimary); rec.Code != 200 {
			t.Fatalf("hello status = %d", rec.Code)
		}
	}
	afterCount, afterSeq := envelopeTableStats(t, env)
	if beforeCount != afterCount {
		t.Fatalf("envelope count changed from %d to %d", beforeCount, afterCount)
	}
	if beforeSeq.Valid != afterSeq.Valid || beforeSeq.Int64 != afterSeq.Int64 {
		t.Fatalf("max(server_seq) changed from %v to %v", beforeSeq, afterSeq)
	}
}

func TestPushWithHeaderOnUnimprintedOpenStoreRequiresHello(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.pushWithOrigin(t, token, originFixturePrimary, pushBody(t, validWireEnvelope()))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	wantBody := readProtocolFixture(t, "origin_required.json")
	if strings.TrimSpace(rec.Body.String()) != wantBody {
		t.Fatalf("body = %q, want %q", rec.Body.String(), wantBody)
	}
	if got := readServerMetaOrigin(t, env); got != "" {
		t.Fatalf("server_meta origin = %q, want empty", got)
	}
	stats, err := env.db.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Envelopes != 0 {
		t.Fatalf("envelopes = %d, want 0", stats.Envelopes)
	}
}
