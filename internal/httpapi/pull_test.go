package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

func TestPullEmptyStore(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	got := decodePull(t, env.pull(t, token, "since=0&limit=10"))
	if len(got.Envelopes) != 0 {
		t.Fatalf("len = %d, want 0", len(got.Envelopes))
	}
	if got.NextCursor != 0 {
		t.Fatalf("next_cursor = %d, want 0", got.NextCursor)
	}

	got = decodePull(t, env.pull(t, token, "since=4&limit=10"))
	if len(got.Envelopes) != 0 {
		t.Fatalf("len(since=4) = %d, want 0", len(got.Envelopes))
	}
	if got.NextCursor != 4 {
		t.Fatalf("next_cursor(since=4) = %d, want 4", got.NextCursor)
	}
}

func TestPullReturnsAllInOrder(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	pushThreeAlice(t, env, token)

	got := decodePull(t, env.pull(t, token, "since=0"))
	if len(got.Envelopes) != 3 {
		t.Fatalf("len = %d, want 3", len(got.Envelopes))
	}
	for i, want := range []int64{1, 2, 3} {
		if got.Envelopes[i].ServerSeq != want {
			t.Fatalf("envelopes[%d].server_seq = %d, want %d", i, got.Envelopes[i].ServerSeq, want)
		}
	}
	if got.NextCursor != 3 {
		t.Fatalf("next_cursor = %d, want 3", got.NextCursor)
	}
}

func TestPullSinceLastSeqIsEmpty(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	pushThreeAlice(t, env, token)

	got := decodePull(t, env.pull(t, token, "since=3"))
	if len(got.Envelopes) != 0 {
		t.Fatalf("len = %d, want 0", len(got.Envelopes))
	}
	if got.NextCursor != 3 {
		t.Fatalf("next_cursor = %d, want 3", got.NextCursor)
	}
}

func TestPullLimitOneCatchUpLoop(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	pushThreeAlice(t, env, token)

	var ids []string
	since := int64(0)
	for {
		got := decodePull(t, env.pull(t, token, fmt.Sprintf("since=%d&limit=1", since)))
		if len(got.Envelopes) == 0 {
			if got.NextCursor != since {
				t.Fatalf("empty page next_cursor = %d, want %d", got.NextCursor, since)
			}
			break
		}
		ids = append(ids, got.Envelopes[0].ID)
		since = got.NextCursor
	}
	if len(ids) != 3 {
		t.Fatalf("walked %d ids %v, want 3", len(ids), ids)
	}
	seen := make(map[string]int)
	for _, id := range ids {
		seen[id]++
	}
	for _, id := range []string{"env-1", "env-2", "env-3"} {
		if seen[id] != 1 {
			t.Fatalf("id %q count = %d, want 1 (got %v)", id, seen[id], ids)
		}
	}
}

func TestPullClampsLimitToMax(t *testing.T) {
	t.Parallel()

	env := newHTTPEnvWith(t, nil, func(cfg *config.Config) {
		cfg.Sync.PullLimitMax = 2
	})
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	pushThreeAlice(t, env, token)

	got := decodePull(t, env.pull(t, token, "since=0&limit=100"))
	if len(got.Envelopes) != 2 {
		t.Fatalf("len = %d, want 2 (clamped to PullLimitMax)", len(got.Envelopes))
	}
}

func TestPullIsolatesUsersIncludingSameID(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	alice := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	bob := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("bob"))

	alicePayload := []byte("alice-bytes")
	bobPayload := []byte("bob-bytes")

	aliceFirst := validWireEnvelope()
	aliceFirst.Payload = base64.StdEncoding.EncodeToString(alicePayload)
	pushOrFatal(t, env, alice, aliceFirst)
	assertPushOK(t, env.push(t, alice, pushBody(t, aliceFirst)), false)
	aliceSecond := validWireEnvelope()
	aliceSecond.ID = "env-2"
	aliceSecond.Payload = base64.StdEncoding.EncodeToString(alicePayload)
	pushOrFatal(t, env, alice, aliceSecond)

	bobFirst := validWireEnvelope()
	bobFirst.SourceID = "device-b"
	bobFirst.Payload = base64.StdEncoding.EncodeToString(bobPayload)
	pushOrFatal(t, env, bob, bobFirst)
	assertPushOK(t, env.push(t, bob, pushBody(t, bobFirst)), false)
	bobSecond := validWireEnvelope()
	bobSecond.ID = "env-2"
	bobSecond.SourceID = "device-b"
	bobSecond.Payload = base64.StdEncoding.EncodeToString(bobPayload)
	pushOrFatal(t, env, bob, bobSecond)

	alicePage := decodePull(t, env.pull(t, alice, "since=0&limit=10"))
	if pullHasSourceOrPayload(alicePage, "device-b", bobPayload) {
		t.Fatalf("alice pull leaked bob data: %+v", alicePage.Envelopes)
	}
	if !pullHasID(alicePage, "env-1") || !pullHasID(alicePage, "env-2") {
		t.Fatalf("alice missing own ids: %+v", alicePage.Envelopes)
	}

	bobPage := decodePull(t, env.pull(t, bob, "since=0&limit=10"))
	if pullHasSourceOrPayload(bobPage, "device-a", alicePayload) {
		t.Fatalf("bob pull leaked alice data: %+v", bobPage.Envelopes)
	}
	if !pullHasID(bobPage, "env-1") || !pullHasID(bobPage, "env-2") {
		t.Fatalf("bob missing own ids: %+v", bobPage.Envelopes)
	}

	var aliceIDs []string
	since := int64(0)
	for {
		got := decodePull(t, env.pull(t, alice, fmt.Sprintf("since=%d&limit=1", since)))
		if len(got.Envelopes) == 0 {
			break
		}
		if got.Envelopes[0].SourceID == "device-b" || bytes.Equal(got.Envelopes[0].Payload, bobPayload) {
			t.Fatal("alice catch-up walked a bob envelope")
		}
		aliceIDs = append(aliceIDs, got.Envelopes[0].ID)
		since = got.NextCursor
	}
	if len(aliceIDs) != 2 {
		t.Fatalf("alice catch-up ids = %v, want 2 (gap must not hide env-2)", aliceIDs)
	}
}

func TestPullWalksSequenceGap(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	a := validWireEnvelope()
	pushOrFatal(t, env, token, a)
	assertPushOK(t, env.push(t, token, pushBody(t, a)), false)
	b := validWireEnvelope()
	b.ID = "env-2"
	pushOrFatal(t, env, token, b)

	var pages []pullEnvelope
	since := int64(0)
	for {
		got := decodePull(t, env.pull(t, token, fmt.Sprintf("since=%d&limit=1", since)))
		if len(got.Envelopes) == 0 {
			break
		}
		pages = append(pages, got.Envelopes[0])
		since = got.NextCursor
	}
	if len(pages) != 2 {
		t.Fatalf("non-empty pages = %d, want 2", len(pages))
	}
	if pages[0].ID != a.ID || pages[0].ServerSeq != 1 {
		t.Fatalf("page 1 = id %q seq %d, want %q seq 1", pages[0].ID, pages[0].ServerSeq, a.ID)
	}
	if pages[1].ID != b.ID || pages[1].ServerSeq != 3 {
		t.Fatalf("page 2 = id %q seq %d, want %q seq 3", pages[1].ID, pages[1].ServerSeq, b.ID)
	}
}

func TestPullOpaqueNonUTF8RoundTrip(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	fixture := protocolFixture(t, "non_utf8_payload.json")
	body := []byte(`{"envelopes":[` + string(fixture) + `]}`)
	rec := env.push(t, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("push status = %d, body = %q", rec.Code, rec.Body.String())
	}

	got := decodePull(t, env.pull(t, token, "since=0"))
	if len(got.Envelopes) == 0 {
		t.Fatal("pull returned no envelopes")
	}
	want := []byte{0xFF, 0xFE, 0x00, 0x41}
	payload := got.Envelopes[len(got.Envelopes)-1].Payload
	if !bytes.Equal(payload, want) {
		t.Fatalf("payload = %x, want %x", payload, want)
	}
}

func TestPullRejectsBadQuery(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	cases := []struct {
		query string
		param string
	}{
		{query: "since=abc", param: "since"},
		{query: "since=-1", param: "since"},
		{query: "limit=0", param: "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			rec := env.pull(t, token, tc.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.param) {
				t.Fatalf("body %q does not name %q", rec.Body.String(), tc.param)
			}
		})
	}
}

func TestPullIgnoresUnknownQueryParam(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))

	plain := decodePull(t, env.pull(t, token, "since=0"))
	withFoo := decodePull(t, env.pull(t, token, "since=0&foo=bar"))
	if withFoo.NextCursor != plain.NextCursor || len(withFoo.Envelopes) != len(plain.Envelopes) {
		t.Fatalf("unknown param changed response: %+v vs %+v", withFoo, plain)
	}
}

func TestPullUnauthorized(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	assertUnauthorized(t, env.pull(t, "", "since=0"))
}

// pushThreeAlice stores three envelopes with distinct ids for the catch-up tests.
func pushThreeAlice(t *testing.T, env *httpEnv, token string) {
	t.Helper()
	for _, id := range []string{"env-1", "env-2", "env-3"} {
		w := validWireEnvelope()
		w.ID = id
		pushOrFatal(t, env, token, w)
	}
}

// pushOrFatal posts one envelope and fails the test unless the status is 200.
func pushOrFatal(t *testing.T, env *httpEnv, token string, w wireEnvelope) {
	t.Helper()
	rec := env.push(t, token, pushBody(t, w))
	if rec.Code != http.StatusOK {
		t.Fatalf("push status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

// decodePull unmarshals a 200 pull body; any other status is a test failure.
func decodePull(t *testing.T, rec *httptest.ResponseRecorder) pullResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var got pullResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v body = %q", err, rec.Body.String())
	}
	return got
}

// pullHasID reports whether the page contains an envelope with the given id.
func pullHasID(page pullResponse, id string) bool {
	for _, env := range page.Envelopes {
		if env.ID == id {
			return true
		}
	}
	return false
}

// pullHasSourceOrPayload reports whether any envelope matches the other user's
// source_id or opaque payload bytes (the isolation leak to catch).
func pullHasSourceOrPayload(page pullResponse, sourceID string, payload []byte) bool {
	for _, env := range page.Envelopes {
		if env.SourceID == sourceID || bytes.Equal(env.Payload, payload) {
			return true
		}
	}
	return false
}

// protocolFixture reads a golden envelope JSON from the protocol submodule.
func protocolFixture(t *testing.T, name string) []byte {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			path := filepath.Join(dir, "protocol", "fixtures", "envelope", name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", path, err)
			}
			return data
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found while locating protocol fixture")
		}
		dir = parent
	}
}
