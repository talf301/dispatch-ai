package id

import (
	"regexp"
	"testing"
)

func TestGenerateLength(t *testing.T) {
	got := Generate()
	if len(got) != 4 {
		t.Errorf("expected length 4, got %d (%q)", len(got), got)
	}
}

func TestGenerateHexChars(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{4}$`)
	for i := 0; i < 100; i++ {
		got := Generate()
		if !re.MatchString(got) {
			t.Fatalf("expected 4 lowercase hex chars, got %q", got)
		}
	}
}

func TestGenerateUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	// Generate many IDs and check for duplicates.
	// With 2 bytes (65536 possibilities), collisions are expected
	// over large runs, but 50 should be safe.
	for i := 0; i < 50; i++ {
		id := Generate()
		if seen[id] {
			t.Fatalf("duplicate id after %d generations: %q", i, id)
		}
		seen[id] = true
	}
}
