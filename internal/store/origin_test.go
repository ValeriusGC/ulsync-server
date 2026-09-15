package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"

	_ "modernc.org/sqlite"
)

const (
	fixtureOrigin      = "com.example.app/7c3e9a12-4b56-4d8e-9f01-2a3b4c5d6e7f"
	fixtureOtherOrigin = "com.example.other/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	fixtureAuthored    = "com.example.authored/11111111-2222-3333-4444-555555555555"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Storage{Driver: "sqlite", Path: filepath.Join(dir, "ulsync.db")}
	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func envelopeCounts(t *testing.T, dbPath string) (envelopes int64, maxSeq sql.NullInt64) {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer conn.Close()
	if err := conn.QueryRow(`SELECT COUNT(*) FROM envelopes`).Scan(&envelopes); err != nil {
		t.Fatalf("count envelopes: %v", err)
	}
	_ = conn.QueryRow(`SELECT MAX(server_seq) FROM envelopes`).Scan(&maxSeq)
	return envelopes, maxSeq
}

func TestEnsureOriginDecisionTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(context.Context, *store.Store) error
		authored  string
		request   string
		imprint   bool
		want      string
		wantErr   error
		wantMatch *store.OriginMismatchError
	}{
		{
			name:    "open_empty_wellformed_imprint",
			request: fixtureOrigin,
			imprint: true,
			want:    fixtureOrigin,
		},
		{
			name:    "open_empty_wellformed_no_imprint",
			request: fixtureOrigin,
			imprint: false,
			wantErr: store.ErrOriginRequired,
		},
		{
			name:    "open_empty_missing_imprint",
			imprint: true,
			wantErr: store.ErrOriginRequired,
		},
		{
			name: "open_empty_missing_legacy",
			want: "",
		},
		{
			name: "open_imprinted_same",
			setup: func(ctx context.Context, s *store.Store) error {
				_, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true)
				return err
			},
			request: fixtureOrigin,
			imprint: true,
			want:    fixtureOrigin,
		},
		{
			name: "open_imprinted_missing_imprint",
			setup: func(ctx context.Context, s *store.Store) error {
				_, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true)
				return err
			},
			imprint: true,
			wantErr: store.ErrOriginRequired,
		},
		{
			name: "open_imprinted_missing_legacy",
			setup: func(ctx context.Context, s *store.Store) error {
				_, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true)
				return err
			},
			want: fixtureOrigin,
		},
		{
			name: "open_imprinted_mismatch",
			setup: func(ctx context.Context, s *store.Store) error {
				_, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true)
				return err
			},
			request:   fixtureOtherOrigin,
			imprint:   true,
			wantMatch: &store.OriginMismatchError{StoreOrigin: fixtureOrigin, RequestOrigin: fixtureOtherOrigin},
		},
		{
			name:     "authored_matching",
			setup:    func(ctx context.Context, s *store.Store) error { return s.BindAuthoredOrigin(ctx, fixtureAuthored) },
			authored: fixtureAuthored,
			request:  fixtureAuthored,
			imprint:  false,
			want:     fixtureAuthored,
		},
		{
			name:     "authored_missing_header",
			setup:    func(ctx context.Context, s *store.Store) error { return s.BindAuthoredOrigin(ctx, fixtureAuthored) },
			authored: fixtureAuthored,
			wantErr:  store.ErrOriginRequired,
		},
		{
			name:     "authored_mismatch",
			setup:    func(ctx context.Context, s *store.Store) error { return s.BindAuthoredOrigin(ctx, fixtureAuthored) },
			authored: fixtureAuthored,
			request:  fixtureOtherOrigin,
			wantMatch: &store.OriginMismatchError{
				StoreOrigin:   fixtureAuthored,
				RequestOrigin: fixtureOtherOrigin,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openTestStore(t)
			ctx := context.Background()
			if tc.setup != nil {
				if err := tc.setup(ctx, s); err != nil {
					t.Fatalf("setup() error = %v", err)
				}
			}
			got, err := s.EnsureOrigin(ctx, tc.authored, tc.request, tc.imprint)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("EnsureOrigin() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if tc.wantMatch != nil {
				mismatch, ok := err.(*store.OriginMismatchError)
				if !ok {
					t.Fatalf("EnsureOrigin() error = %T %v, want *OriginMismatchError", err, err)
				}
				if mismatch.StoreOrigin != tc.wantMatch.StoreOrigin || mismatch.RequestOrigin != tc.wantMatch.RequestOrigin {
					t.Fatalf("mismatch = %+v, want %+v", mismatch, tc.wantMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("EnsureOrigin() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("EnsureOrigin() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHelloImprintIsIdempotent(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := context.Background()
	first, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true)
	if err != nil {
		t.Fatalf("first EnsureOrigin() error = %v", err)
	}
	second, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true)
	if err != nil {
		t.Fatalf("second EnsureOrigin() error = %v", err)
	}
	if first != fixtureOrigin || second != fixtureOrigin {
		t.Fatalf("origins = (%q, %q), want %q twice", first, second, fixtureOrigin)
	}
	stored, err := s.Origin(ctx)
	if err != nil {
		t.Fatalf("Origin() error = %v", err)
	}
	if stored != fixtureOrigin {
		t.Fatalf("stored = %q, want %q", stored, fixtureOrigin)
	}
}

func TestSecondImprintRequestDoesNotOverwriteStored(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true); err != nil {
		t.Fatalf("first imprint error = %v", err)
	}
	_, err := s.EnsureOrigin(ctx, "", fixtureOtherOrigin, true)
	if _, ok := err.(*store.OriginMismatchError); !ok {
		t.Fatalf("second imprint error = %v, want *OriginMismatchError", err)
	}
	stored, err := s.Origin(ctx)
	if err != nil {
		t.Fatalf("Origin() error = %v", err)
	}
	if stored != fixtureOrigin {
		t.Fatalf("stored = %q, want original %q", stored, fixtureOrigin)
	}
}

func TestEnsureOriginDoesNotTouchEnvelopes(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := context.Background()
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	beforeEnv, beforeSeq := envelopeCounts(t, stats.Path)
	for range 5 {
		if _, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true); err != nil {
			t.Fatalf("EnsureOrigin() error = %v", err)
		}
	}
	afterEnv, afterSeq := envelopeCounts(t, stats.Path)
	if beforeEnv != afterEnv {
		t.Fatalf("envelope count changed from %d to %d", beforeEnv, afterEnv)
	}
	if beforeSeq.Valid != afterSeq.Valid || beforeSeq.Int64 != afterSeq.Int64 {
		t.Fatalf("max(server_seq) changed from %v to %v", beforeSeq, afterSeq)
	}
}

func TestBindAuthoredOriginWritesWhenEmpty(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := context.Background()
	if err := s.BindAuthoredOrigin(ctx, fixtureAuthored); err != nil {
		t.Fatalf("BindAuthoredOrigin() error = %v", err)
	}
	stored, err := s.Origin(ctx)
	if err != nil {
		t.Fatalf("Origin() error = %v", err)
	}
	if stored != fixtureAuthored {
		t.Fatalf("stored = %q, want %q", stored, fixtureAuthored)
	}
}

func TestBindAuthoredOriginMatchesExisting(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.EnsureOrigin(ctx, "", fixtureAuthored, true); err != nil {
		t.Fatalf("imprint error = %v", err)
	}
	if err := s.BindAuthoredOrigin(ctx, fixtureAuthored); err != nil {
		t.Fatalf("BindAuthoredOrigin() error = %v", err)
	}
}

func TestBindAuthoredOriginRejectsConflict(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.EnsureOrigin(ctx, "", fixtureOrigin, true); err != nil {
		t.Fatalf("imprint error = %v", err)
	}
	err := s.BindAuthoredOrigin(ctx, fixtureAuthored)
	if err == nil {
		t.Fatal("BindAuthoredOrigin() expected error for config/table conflict")
	}
	if !strings.Contains(err.Error(), fixtureOrigin) || !strings.Contains(err.Error(), fixtureAuthored) {
		t.Fatalf("error = %q, want both origin values", err)
	}
}
