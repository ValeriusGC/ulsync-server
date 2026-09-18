package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const serverNowSlackMS = 5000

// assertServerNowMsLive checks that got is within serverNowSlackMS of the local
// clock. Fixtures freeze 1756100123456; a live server must not copy it.
func assertServerNowMsLive(t *testing.T, got int64) {
	t.Helper()

	now := time.Now().UTC().UnixMilli()
	delta := math.Abs(float64(got - now))
	if delta > float64(serverNowSlackMS) {
		t.Fatalf("server_now_ms = %d, |got-now| = %.0f ms, want <= %d", got, delta, serverNowSlackMS)
	}
}

// assertHelloBody checks a hello 200: origin and user_id match; server_now_ms is live.
func assertHelloBody(t *testing.T, body []byte, wantOrigin, wantUserID string) {
	t.Helper()

	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	assertJSONFieldString(t, got, "origin", wantOrigin)
	assertJSONFieldString(t, got, "user_id", wantUserID)
	raw, ok := got["server_now_ms"]
	if !ok {
		t.Fatal("server_now_ms missing from hello body")
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		t.Fatalf("server_now_ms is not a number: %v", err)
	}
	gotMS, err := num.Int64()
	if err != nil {
		t.Fatalf("server_now_ms is not an integer: %v", err)
	}
	assertServerNowMsLive(t, gotMS)
}

func assertJSONFieldString(t *testing.T, obj map[string]json.RawMessage, key, want string) {
	t.Helper()

	raw, ok := obj[key]
	if !ok {
		t.Fatalf("%s missing from body", key)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s is not a string: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

// assertMailJSONEqual compares got to a protocol fixture while treating
// server_now_ms as a live clock on the response, not a frozen golden value.
func assertMailJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()

	var gotVal map[string]any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("Unmarshal(got) error = %v", err)
	}
	rawNow, ok := gotVal["server_now_ms"]
	if !ok {
		t.Fatal("server_now_ms missing from response body")
	}
	gotMS, ok := rawNow.(float64)
	if !ok {
		t.Fatalf("server_now_ms has unexpected type %T", rawNow)
	}
	assertServerNowMsLive(t, int64(gotMS))

	var wantVal map[string]any
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("Unmarshal(want) error = %v", err)
	}
	delete(gotVal, "server_now_ms")
	delete(wantVal, "server_now_ms")

	gotCanon, err := json.Marshal(gotVal)
	if err != nil {
		t.Fatalf("Marshal(got) error = %v", err)
	}
	wantCanon, err := json.Marshal(wantVal)
	if err != nil {
		t.Fatalf("Marshal(want) error = %v", err)
	}
	if string(gotCanon) != string(wantCanon) {
		t.Fatalf("body without server_now_ms = %s, want %s", gotCanon, wantCanon)
	}
}

func TestHelloServerNowMsLive(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.hello(t, token, originFixturePrimary)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	assertHelloBody(t, rec.Body.Bytes(), originFixturePrimary, "alice")
}

func TestHelloServerNowMsChangesBetweenCalls(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec1 := env.hello(t, token, originFixturePrimary)
	rec2 := env.hello(t, token, originFixturePrimary)
	if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
		t.Fatalf("status first=%d second=%d", rec1.Code, rec2.Code)
	}

	var first, second map[string]json.RawMessage
	if err := json.Unmarshal(rec1.Body.Bytes(), &first); err != nil {
		t.Fatalf("Unmarshal(first) error = %v", err)
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &second); err != nil {
		t.Fatalf("Unmarshal(second) error = %v", err)
	}
	var n1, n2 json.Number
	if err := json.Unmarshal(first["server_now_ms"], &n1); err != nil {
		t.Fatalf("first server_now_ms: %v", err)
	}
	if err := json.Unmarshal(second["server_now_ms"], &n2); err != nil {
		t.Fatalf("second server_now_ms: %v", err)
	}
	ms1, err := n1.Int64()
	if err != nil {
		t.Fatalf("first server_now_ms integer: %v", err)
	}
	ms2, err := n2.Int64()
	if err != nil {
		t.Fatalf("second server_now_ms integer: %v", err)
	}
	assertServerNowMsLive(t, ms1)
	assertServerNowMsLive(t, ms2)
}

func TestPushResponseCarriesServerNowMs(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.push(t, token, pushBody(t, validWireEnvelope()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var resp pushResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	assertServerNowMsLive(t, resp.ServerNowMS)
}

func TestPullEmptyPageCarriesServerNowMs(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.pull(t, token, "since=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	got := decodePull(t, rec)
	assertServerNowMsLive(t, got.ServerNowMS)
	if len(got.Envelopes) != 0 {
		t.Fatalf("len(envelopes) = %d, want 0", len(got.Envelopes))
	}
}

func TestLiveCursorCarriesServerNowMs(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	pushOrFatal(t, env, token, validWireEnvelope())

	ts := startStreamServer(t, env)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp := openSSE(t, ctx, ts, token, "since=0&live=sse")
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var body map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &body); err != nil {
			continue
		}
		if _, ok := body["next_cursor"]; !ok {
			continue
		}
		var num json.Number
		if err := json.Unmarshal(body["server_now_ms"], &num); err != nil {
			t.Fatalf("cursor server_now_ms is not a number: %v", err)
		}
		gotMS, err := num.Int64()
		if err != nil {
			t.Fatalf("cursor server_now_ms is not an integer: %v", err)
		}
		assertServerNowMsLive(t, gotMS)
		return
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	t.Fatal("stream ended before cursor event with server_now_ms")
}
