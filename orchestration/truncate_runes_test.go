package orchestration

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncateRunes is the UTF-8-safe byte-cap helper used by the continuation
// renderer and the remediation-note builder to bound error strings without
// splitting multibyte codepoints.

func TestTruncateRunes_ShorterThanCapReturnsUnchanged(t *testing.T) {
	got := truncateRunes("hello", 100)
	if got != "hello" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestTruncateRunes_ExactlyAtCapReturnsUnchanged(t *testing.T) {
	s := strings.Repeat("a", 50)
	got := truncateRunes(s, 50)
	if got != s {
		t.Errorf("expected unchanged at exact cap, got %q", got)
	}
}

func TestTruncateRunes_OneByteOverCapTruncates(t *testing.T) {
	s := strings.Repeat("a", 51)
	got := truncateRunes(s, 50)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	if got == s {
		t.Errorf("expected truncation to occur")
	}
}

func TestTruncateRunes_NeverSplitsMultibyteCodepoint(t *testing.T) {
	// "é" is 2 bytes in UTF-8 (0xC3 0xA9). 100 of them = 200 bytes.
	// A naive s[:101] would split the 51st é, leaving 0xC3 dangling.
	s := strings.Repeat("é", 100)
	got := truncateRunes(s, 101)
	body := strings.TrimSuffix(got, "…")
	if !utf8.ValidString(body) {
		t.Errorf("truncation produced invalid UTF-8: % x", []byte(body))
	}
	// Body must end on a complete é, not a half-é.
	if len(body)%2 != 0 {
		t.Errorf("expected even byte length (complete éé pairs), got %d bytes: % x", len(body), []byte(body))
	}
}

func TestTruncateRunes_ZeroOrNegativeMaxBytesReturnsUnchanged(t *testing.T) {
	// displayLen=0 (config-disabled truncation) must pass through.
	for _, n := range []int{0, -1, -100} {
		got := truncateRunes("anything", n)
		if got != "anything" {
			t.Errorf("maxBytes=%d should return unchanged, got %q", n, got)
		}
	}
}

func TestTruncateRunes_EmptyStringIsUnchanged(t *testing.T) {
	if got := truncateRunes("", 100); got != "" {
		t.Errorf("empty string should pass through, got %q", got)
	}
	if got := truncateRunes("", 0); got != "" {
		t.Errorf("empty string with maxBytes=0 should pass through, got %q", got)
	}
}

func TestTruncateRunes_MixedASCIIAndMultibyte(t *testing.T) {
	// Mix where the cap falls right after an ASCII char in the middle of
	// a sequence — no rune-boundary backup needed.
	s := "abcéfghi" // bytes: a(1) b(1) c(1) é(2) f(1) g(1) h(1) i(1) = 9 bytes
	// Cap at 3 → "abc…" (no rune split — c is at byte boundary).
	got := truncateRunes(s, 3)
	if got != "abc…" {
		t.Errorf("expected %q, got %q", "abc…", got)
	}
	// Cap at 4 → byte 4 is the second byte of é → must back up to byte 3 → "abc…"
	got = truncateRunes(s, 4)
	if got != "abc…" {
		t.Errorf("expected %q (back-up over é), got %q", "abc…", got)
	}
	// Cap at 5 → byte 5 is just past é → "abcé…"
	got = truncateRunes(s, 5)
	if got != "abcé…" {
		t.Errorf("expected %q, got %q", "abcé…", got)
	}
}
