package main

import (
	"os"
	"testing"
)

func TestRunRejectsZeroConnections(t *testing.T) {
	t.Parallel()

	if err := run(0, "http://127.0.0.1:8080", "load/tokens.json"); err == nil {
		t.Fatal("run(0, ...) error = nil, want rejection")
	}
}

func TestReadTokensMissingFile(t *testing.T) {
	t.Parallel()

	_, err := readTokens("/nonexistent/tokens.json")
	if err == nil {
		t.Fatal("readTokens(missing) error = nil, want failure")
	}
}

func TestReadTokensEmptyArray(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/tokens.json"
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tokens, err := readTokens(path)
	if err != nil {
		t.Fatalf("readTokens() error = %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("len(tokens) = %d, want 0", len(tokens))
	}
}
