package handler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncatePlanMarkdown_KeepsValidUTF8 pins the boundary the daemon's plan
// cap has to respect: the gate writes this string to a file and hands it to an
// annotator, so a byte cut through a multi-byte rune would deliver a broken
// document at the one place a reviewer is actually reading.
func TestTruncatePlanMarkdown_KeepsValidUTF8(t *testing.T) {
	// "é" starts at byte 8 and is two bytes, "→" starts at 28 and is three.
	// Every cut below lands on a continuation byte, and each one made the
	// pre-fix byte slice produce invalid UTF-8.
	body := "# Plan\n\n" + strings.Repeat("é", 10) + strings.Repeat("→", 10)

	cases := []struct {
		name string
		max  int
	}{
		{"one byte into a two-byte rune", 9},
		{"second byte of a two-byte rune", 15},
		{"one byte into a three-byte rune", 29},
		{"last byte of a three-byte rune", 30},
		{"exactly at a rune boundary", 28},
		{"cut exactly at the cap", len(body)},
		{"cap larger than the document", len(body) + 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncatePlanMarkdown(body, tc.max)
			if len(got) > tc.max {
				t.Fatalf("truncated to %d bytes, cap was %d", len(got), tc.max)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncation split a rune: %q", got)
			}
			if !strings.HasPrefix(body, got) {
				t.Fatalf("truncation must keep a prefix of the document")
			}
			if tc.max >= len(body) && got != body {
				t.Fatalf("a document within the cap must be untouched")
			}
		})
	}
}

// TestTruncatePlanMarkdown_ShrinksOnlyWhenNeeded guards against a fix that
// drops bytes whenever it sees a multi-byte rune.
func TestTruncatePlanMarkdown_ShrinksOnlyWhenNeeded(t *testing.T) {
	body := strings.Repeat("→", 4) // 12 bytes
	if got := truncatePlanMarkdown(body, 12); got != body {
		t.Fatalf("exact-cap document changed: %q", got)
	}
	if got := truncatePlanMarkdown(body, 13); got != body {
		t.Fatalf("document under the cap changed: %q", got)
	}
}
