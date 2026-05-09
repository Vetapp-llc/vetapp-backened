package handlers

import (
	"strings"
	"testing"
)

// generateVerificationToken is the source of the entropy for our
// email-verification links. Two properties matter:
//
//   1. URL safety — base64url encoding, no padding, no chars that need
//      escaping in a query string.
//   2. Sufficient entropy — 32 raw bytes → 43 chars; collisions over a
//      24h TTL are astronomically unlikely.
//
// Tests below pin both invariants so a future refactor can't silently
// shorten the token or introduce unsafe characters.
func TestGenerateVerificationToken_URLSafeAndUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		tok, err := generateVerificationToken()
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
		if len(tok) < 40 {
			t.Errorf("iter %d: token shorter than expected: %d chars (%q)", i, len(tok), tok)
		}
		// base64url alphabet: A-Z a-z 0-9 - _ (no padding because we
		// use RawURLEncoding).
		for _, c := range tok {
			ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_'
			if !ok {
				t.Errorf("iter %d: token contains non-URL-safe character %q in %q", i, c, tok)
			}
		}
		if _, dup := seen[tok]; dup {
			t.Errorf("iter %d: duplicate token across 50 iterations: %q", i, tok)
		}
		seen[tok] = struct{}{}
	}
}

// emailVerificationHTML / emailVerificationText render the body of the
// email Resend dispatches. The link must appear verbatim in BOTH the
// HTML and the plaintext fallback (Outlook strips HTML, some users
// configure clients to plaintext-only) — verifying this prevents a
// regression where the link survives in HTML but goes missing from
// the text fallback.
func TestEmailVerificationBodies_ContainLink(t *testing.T) {
	link := "https://example.com/api/auth/email/verify/confirm?token=ABC123"

	html := emailVerificationHTML("Nika", link)
	if !strings.Contains(html, link) {
		t.Errorf("HTML body does not contain the verification link")
	}
	if !strings.Contains(html, "Hello Nika") {
		t.Errorf("HTML body missing personalized greeting")
	}

	text := emailVerificationText("Nika", link)
	if !strings.Contains(text, link) {
		t.Errorf("plaintext body does not contain the verification link")
	}
	if !strings.Contains(text, "Hello Nika") {
		t.Errorf("plaintext body missing personalized greeting")
	}
}

func TestEmailVerificationBodies_FallbackGreeting(t *testing.T) {
	// Empty first name → must fall back to a generic greeting rather
	// than an awkward "Hello ,".
	html := emailVerificationHTML("", "https://x.test/")
	if strings.Contains(html, "Hello ,") {
		t.Errorf("HTML has awkward empty-name greeting: %q", html)
	}
	if !strings.Contains(html, "Hello,") && !strings.Contains(html, "Hello<") {
		t.Errorf("HTML missing fallback greeting; body=%q", html)
	}

	text := emailVerificationText("", "https://x.test/")
	if strings.Contains(text, "Hello ,") {
		t.Errorf("plaintext has awkward empty-name greeting: %q", text)
	}
	if !strings.Contains(text, "Hello,") {
		t.Errorf("plaintext missing fallback greeting; body=%q", text)
	}
}
