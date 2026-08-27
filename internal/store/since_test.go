package store

import (
	"context"
	"strings"
	"testing"
)

func TestSinceEmptyStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	rows, cursor, err := s.Since(ctx, "alice", 0, 10)
	if err != nil {
		t.Fatalf("Since() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("len = %d, want 0", len(rows))
	}
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0", cursor)
	}

	rows, cursor, err = s.Since(ctx, "alice", 7, 10)
	if err != nil {
		t.Fatalf("Since(since=7) error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("len(since=7) = %d, want 0", len(rows))
	}
	if cursor != 7 {
		t.Fatalf("cursor(since=7) = %d, want 7", cursor)
	}
}

func TestSinceReturnsRowsInSeqOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	seedAliceThree(t, s)

	rows, cursor, err := s.Since(ctx, "alice", 0, 10)
	if err != nil {
		t.Fatalf("Since() error = %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3", len(rows))
	}
	for i, want := range []int64{1, 2, 3} {
		if rows[i].ServerSeq != want {
			t.Fatalf("rows[%d].ServerSeq = %d, want %d", i, rows[i].ServerSeq, want)
		}
	}
	if cursor != 3 {
		t.Fatalf("cursor = %d, want 3", cursor)
	}
}

func TestSinceEqualToLastSeqIsEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	seedAliceThree(t, s)

	rows, cursor, err := s.Since(ctx, "alice", 3, 10)
	if err != nil {
		t.Fatalf("Since() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("len = %d, want 0", len(rows))
	}
	if cursor != 3 {
		t.Fatalf("cursor = %d, want 3", cursor)
	}
}

func TestSinceLimitOneWalksAllRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	ids := seedAliceThree(t, s)

	var got []string
	since := int64(0)
	for {
		rows, next, err := s.Since(ctx, "alice", since, 1)
		if err != nil {
			t.Fatalf("Since(since=%d) error = %v", since, err)
		}
		if len(rows) == 0 {
			if next != since {
				t.Fatalf("empty page cursor = %d, want %d", next, since)
			}
			break
		}
		got = append(got, rows[0].ID)
		since = next
	}
	if len(got) != 3 {
		t.Fatalf("walked %d ids %v, want 3 %v", len(got), got, ids)
	}
	seen := make(map[string]int)
	for _, id := range got {
		seen[id]++
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("id %q count = %d, want 1 (got %v)", id, seen[id], got)
		}
	}
}

func TestSinceIsolatesUsersAndAllowsSameID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	env := sampleEnvelope("device-a", 1000, 1, []byte("shared-id"))

	applied, err := s.Upsert(ctx, "alice", env)
	if err != nil || !applied {
		t.Fatalf("Upsert(alice) applied=%v err=%v", applied, err)
	}
	applied, err = s.Upsert(ctx, "bob", env)
	if err != nil || !applied {
		t.Fatalf("Upsert(bob) applied=%v err=%v", applied, err)
	}

	aliceRows, aliceCursor, err := s.Since(ctx, "alice", 0, 10)
	if err != nil {
		t.Fatalf("Since(alice) error = %v", err)
	}
	if len(aliceRows) != 1 {
		t.Fatalf("alice len = %d, want 1", len(aliceRows))
	}
	if aliceCursor != 1 {
		t.Fatalf("alice cursor = %d, want 1", aliceCursor)
	}

	bobRows, bobCursor, err := s.Since(ctx, "bob", 0, 10)
	if err != nil {
		t.Fatalf("Since(bob) error = %v", err)
	}
	if len(bobRows) != 1 {
		t.Fatalf("bob len = %d, want 1", len(bobRows))
	}
	if bobCursor != 1 {
		t.Fatalf("bob cursor = %d, want 1", bobCursor)
	}
}

func TestSinceWalksAcrossSequenceGap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	a := sampleEnvelope("device-a", 1000, 1, []byte("A"))
	applied, err := s.Upsert(ctx, "alice", a)
	if err != nil || !applied {
		t.Fatalf("first Upsert(A) applied=%v err=%v", applied, err)
	}
	applied, err = s.Upsert(ctx, "alice", a)
	if err != nil {
		t.Fatalf("retry Upsert(A) error = %v", err)
	}
	if applied {
		t.Fatal("retry Upsert(A) applied = true, want false (seq 2 consumed, no row)")
	}

	b := sampleEnvelope("device-a", 1000, 1, []byte("B"))
	b.ID = "env-b"
	applied, err = s.Upsert(ctx, "alice", b)
	if err != nil || !applied {
		t.Fatalf("Upsert(B) applied=%v err=%v", applied, err)
	}

	var pages []Envelope
	since := int64(0)
	for {
		rows, next, err := s.Since(ctx, "alice", since, 1)
		if err != nil {
			t.Fatalf("Since(since=%d) error = %v", since, err)
		}
		if len(rows) == 0 {
			break
		}
		pages = append(pages, rows[0])
		since = next
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

func TestSinceUsesCursorIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	rows, err := s.readDB.QueryContext(ctx, `
EXPLAIN QUERY PLAN
SELECT server_seq FROM envelopes
WHERE user_id = ? AND server_seq > ?
ORDER BY server_seq
LIMIT ?
`, "x", 0, 10)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("Columns() error = %v", err)
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		for i, v := range vals {
			if i > 0 {
				plan.WriteByte(' ')
			}
			switch typed := v.(type) {
			case []byte:
				plan.Write(typed)
			case string:
				plan.WriteString(typed)
			}
		}
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}

	text := plan.String()
	if !strings.Contains(text, "envelopes_by_cursor") {
		t.Fatalf("plan missing envelopes_by_cursor:\n%s", text)
	}
}

// seedAliceThree writes three envelopes for alice with distinct ids and returns those ids.
func seedAliceThree(t *testing.T, s *Store) []string {
	t.Helper()
	ctx := context.Background()
	ids := []string{"env-1", "env-2", "env-3"}
	for _, id := range ids {
		env := sampleEnvelope("device-a", 1000, 1, []byte(id))
		env.ID = id
		applied, err := s.Upsert(ctx, "alice", env)
		if err != nil || !applied {
			t.Fatalf("Upsert(%q) applied=%v err=%v", id, applied, err)
		}
	}
	return ids
}
