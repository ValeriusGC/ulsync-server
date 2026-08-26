package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ValeriusGC/ulsync-server/internal/config"
)

func TestUpsertNewEnvelope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	applied, err := s.Upsert(ctx, "user-a", sampleEnvelope("device-a", 1000, 1, []byte(`{"type":"increment"}`)))
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if !applied {
		t.Fatal("Upsert() applied = false, want true")
	}

	row := readEnvelopeRow(t, s, "user-a", sampleEnvelope("device-a", 1000, 1, nil).ID, "full")
	if row.ServerSeq != 1 {
		t.Fatalf("server_seq = %d, want 1", row.ServerSeq)
	}
	if row.SourceID != "device-a" {
		t.Fatalf("source_id = %q, want device-a", row.SourceID)
	}
}

func TestUpsertSameIDOlderTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	base := sampleEnvelope("device-a", 2000, 1, []byte("first"))

	if _, err := s.Upsert(ctx, "user-a", base); err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}

	older := base
	older.LastEditedAtMS = 1000
	older.Payload = []byte("second")
	applied, err := s.Upsert(ctx, "user-a", older)
	if err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}
	if applied {
		t.Fatal("Upsert() applied = true, want false for older edit time")
	}

	row := readEnvelopeRow(t, s, "user-a", base.ID, base.Part)
	if string(row.Payload) != "first" {
		t.Fatalf("payload = %q, want first", row.Payload)
	}
	if row.ServerSeq != 1 {
		t.Fatalf("server_seq = %d, want unchanged 1", row.ServerSeq)
	}
}

func TestUpsertArrivalOrderIndependence(t *testing.T) {
	t.Parallel()

	const (
		editedAt = int64(3000)
		revision = int64(1)
	)

	runOrder := func(t *testing.T, firstSource, secondSource string) envelopeRow {
		t.Helper()

		ctx := context.Background()
		s := openTestStore(t)
		id := "shared-id"
		payload := []byte("same-payload")

		first := sampleEnvelope(firstSource, editedAt, revision, payload)
		first.ID = id
		second := sampleEnvelope(secondSource, editedAt, revision, payload)
		second.ID = id

		if _, err := s.Upsert(ctx, "user-a", first); err != nil {
			t.Fatalf("first Upsert() error = %v", err)
		}
		if _, err := s.Upsert(ctx, "user-a", second); err != nil {
			t.Fatalf("second Upsert() error = %v", err)
		}
		return readEnvelopeRow(t, s, "user-a", id, "full")
	}

	finalAB := runOrder(t, "device-a", "device-b")
	finalBA := runOrder(t, "device-b", "device-a")

	if finalAB.SourceID != "device-b" || finalBA.SourceID != "device-b" {
		t.Fatalf("winners = (%q, %q), want device-b for both orders", finalAB.SourceID, finalBA.SourceID)
	}
	if string(finalAB.Payload) != string(finalBA.Payload) {
		t.Fatalf("payloads differ: %q vs %q", finalAB.Payload, finalBA.Payload)
	}
}

func TestUpsertOpaqueNonUTF8Payload(t *testing.T) {
	t.Parallel()

	fixture := loadProtocolFixture(t, "non_utf8_payload.json")
	var wire struct {
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
	}
	if err := json.Unmarshal(fixture, &wire); err != nil {
		t.Fatalf("Unmarshal fixture: %v", err)
	}
	payload, err := base64.StdEncoding.DecodeString(wire.Payload)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}

	ctx := context.Background()
	s := openTestStore(t)
	env := Envelope{
		ID:              wire.ID,
		Part:            wire.Part,
		EntityType:      wire.EntityType,
		CreatedAtMS:     wire.CreatedAtMS,
		LastEditedAtMS:  wire.LastEditedAtMS,
		Revision:        wire.Revision,
		SourceID:        wire.SourceID,
		Flags:           wire.Flags,
		SchemaVersion:   wire.SchemaVersion,
		PayloadEncoding: wire.PayloadEncoding,
		Payload:         payload,
	}
	applied, err := s.Upsert(ctx, "user-a", env)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if !applied {
		t.Fatal("Upsert() applied = false, want true")
	}

	row := readEnvelopeRow(t, s, "user-a", wire.ID, wire.Part)
	want := []byte{0xFF, 0xFE, 0x00, 0x41}
	if !bytes.Equal(row.Payload, want) {
		t.Fatalf("payload = % x, want % x", row.Payload, want)
	}
}

type envelopeRow struct {
	ServerSeq int64
	SourceID  string
	Payload   []byte
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	dir := t.TempDir()
	cfg := config.Storage{Driver: "sqlite", Path: filepath.Join(dir, "ulsync.db")}
	s, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleEnvelope(sourceID string, editedAt, revision int64, payload []byte) Envelope {
	return Envelope{
		ID:              "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		Part:            "full",
		EntityType:      "counter_operation",
		CreatedAtMS:     editedAt,
		LastEditedAtMS:  editedAt,
		Revision:        revision,
		SourceID:        sourceID,
		Flags:           0,
		SchemaVersion:   1,
		PayloadEncoding: "json",
		Payload:         payload,
	}
}

func readEnvelopeRow(t *testing.T, s *Store, userID, id, part string) envelopeRow {
	t.Helper()

	var row envelopeRow
	err := s.readDB.QueryRowContext(context.Background(), `
		SELECT server_seq, source_id, payload
		FROM envelopes
		WHERE user_id = ? AND id = ? AND part = ?
	`, userID, id, part).Scan(&row.ServerSeq, &row.SourceID, &row.Payload)
	if err != nil {
		t.Fatalf("read envelope row: %v", err)
	}
	return row
}

func loadProtocolFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(moduleRoot(t), "protocol", "fixtures", "envelope", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return data
}

func moduleRoot(t *testing.T) string {
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
