package services

import (
	"strings"
	"testing"

	"vetapp-backend/internal/config"
)

// ObjectKey must never derive the storage path from the user's filename.
// Original names are attacker-controlled: a name containing `../` or a
// path separator would otherwise let an upload escape its pet prefix
// and overwrite another pet's object.
func TestObjectKeyIgnoresHostileFilenames(t *testing.T) {
	hostile := []string{
		"../../etc/passwd",
		"..\\..\\windows\\system32",
		"/absolute/path.pdf",
		"name with spaces.pdf",
		strings.Repeat("a", 500) + ".pdf",
		"no-extension",
		"double..dots.pdf",
	}
	for _, name := range hostile {
		key, err := ObjectKey("1132845", name)
		if err != nil {
			t.Fatalf("ObjectKey(%q): %v", name, err)
		}
		if strings.Contains(key, "..") {
			t.Errorf("key %q from %q contains a traversal sequence", key, name)
		}
		// Exactly one separator: the pet prefix.
		if strings.Count(key, "/") != 1 {
			t.Errorf("key %q from %q should have exactly one '/'", key, name)
		}
		if !strings.HasPrefix(key, "1132845/") {
			t.Errorf("key %q from %q escaped its pet prefix", key, name)
		}
	}
}

// Keys must be unique — a collision would either overwrite an existing
// file or fail the upload, depending on upsert behaviour.
func TestObjectKeyIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		k, err := ObjectKey("1", "report.pdf")
		if err != nil {
			t.Fatalf("ObjectKey: %v", err)
		}
		if seen[k] {
			t.Fatalf("duplicate key generated: %s", k)
		}
		seen[k] = true
	}
}

// A safe extension is carried over so the object is served with a
// sensible type; anything unusual is dropped rather than trusted.
func TestObjectKeyExtensionHandling(t *testing.T) {
	cases := map[string]string{
		"report.pdf":       ".pdf",
		"scan.JPG":         ".jpg",
		"x.png":            ".png",
		"archive.tar.gz":   ".gz",
		"weird.p<df":       "",
		"no-extension":     "",
		"toolongextension": "",
	}
	for name, wantExt := range cases {
		k, err := ObjectKey("1", name)
		if err != nil {
			t.Fatalf("ObjectKey(%q): %v", name, err)
		}
		gotExt := ""
		if i := strings.LastIndex(k, "."); i >= 0 {
			gotExt = k[i:]
		}
		if gotExt != wantExt {
			t.Errorf("ObjectKey(%q) extension = %q, want %q", name, gotExt, wantExt)
		}
	}
}

// The upload allow-list is what stops executables and HTML (which can
// carry script) being parked in a bucket served back to users.
func TestIsAllowedContentType(t *testing.T) {
	for _, ct := range []string{"application/pdf", "image/jpeg", "image/png", "IMAGE/PNG", " image/webp "} {
		if !IsAllowedContentType(ct) {
			t.Errorf("IsAllowedContentType(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{
		"text/html", "application/x-msdownload", "application/octet-stream",
		"image/svg+xml", // SVG can embed script
		"", "application/javascript",
	} {
		if IsAllowedContentType(ct) {
			t.Errorf("IsAllowedContentType(%q) = true, want false", ct)
		}
	}
}

// Enabled() gates the handler's "not configured" response, so a
// deployment without credentials returns a clear error instead of a 500.
func TestStorageEnabled(t *testing.T) {
	if NewStorageService(&config.Config{}).Enabled() {
		t.Error("Enabled() true with no credentials")
	}
	if NewStorageService(&config.Config{SupabaseURL: "https://x.supabase.co"}).Enabled() {
		t.Error("Enabled() true with only a URL")
	}
	full := &config.Config{
		SupabaseURL:        "https://x.supabase.co",
		SupabaseServiceKey: "key",
		SupabaseBucket:     "procedure-files",
	}
	if !NewStorageService(full).Enabled() {
		t.Error("Enabled() false with full credentials")
	}
}
