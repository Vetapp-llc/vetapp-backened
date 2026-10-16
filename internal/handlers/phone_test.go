package handlers

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"555123456":         "555123456",
		" 555 12 34 56 ":    "555123456",
		"0555123456":        "555123456",
		"995555123456":      "555123456",
		"+995555123456":     "555123456",
		"+995 555-12-34-56": "555123456",
		"00995555123456":    "555123456",
		"+49 1512 3456789":  "+4915123456789",
		"+995322004482":     "+995322004482",
		"":                  "",
		"123123":            "",
		"55663737373":       "",
		"+1":                "",
	}
	for in, want := range cases {
		if got := normalizePhone(in); got != want {
			t.Errorf("normalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsHEIF(t *testing.T) {
	heic := append([]byte{0, 0, 0, 0x18}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	if !isHEIF(heic) {
		t.Error("heic not recognised")
	}
	if isHEIF([]byte("\x00\x00\x00\x18ftypmp42")) {
		t.Error("mp4 taken for heif")
	}
}

func TestSandboxAllowed(t *testing.T) {
	t.Setenv("APPLE_SANDBOX_EMAILS", "owner.test@vetapp.ge, Review@Example.com")
	for email, want := range map[string]bool{
		"owner.test@vetapp.ge": true, "review@example.com": true, " OWNER.TEST@vetapp.ge ": true,
		"someone@vetapp.ge": false, "": false,
	} {
		if got := sandboxAllowed(email); got != want {
			t.Errorf("sandboxAllowed(%q) = %v, want %v", email, got, want)
		}
	}
	t.Setenv("APPLE_SANDBOX_EMAILS", "")
	if sandboxAllowed("owner.test@vetapp.ge") {
		t.Error("empty list must allow nobody")
	}
}
