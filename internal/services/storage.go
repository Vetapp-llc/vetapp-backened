package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	cryptorand "crypto/rand"
	"encoding/hex"

	"vetapp-backend/internal/config"
)

// StorageService stores procedure attachments in Supabase Storage.
//
// Supabase is already the database, so its object store avoids adding a
// second vendor. The bucket is PRIVATE: uploads go through the service
// role key held only by this server, and downloads are served via
// short-lived signed URLs minted per request. Nothing is ever publicly
// addressable, which matters because these are veterinary records.
//
// The legacy PHP app wrote uploads to local disk. That cannot work here
// — Railway containers have ephemeral filesystems, so any file would
// vanish on the next deploy.
type StorageService struct {
	cfg    *config.Config
	client *http.Client
}

// NewStorageService creates a StorageService.
func NewStorageService(cfg *config.Config) *StorageService {
	return &StorageService{
		cfg: cfg,
		// Uploads carry file bodies, so this is more generous than the
		// payment-gateway timeouts, but still bounded.
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Enabled reports whether storage credentials are configured. Handlers
// use this to return a clear "not configured" error rather than a
// confusing 500 on a deployment without the key.
func (s *StorageService) Enabled() bool {
	return s.cfg.SupabaseURL != "" && s.cfg.SupabaseServiceKey != ""
}

// MaxUploadBytes caps a single attachment. Lab results are PDFs and
// photos; anything larger is likely a mistake, and an unbounded upload
// is a cheap way to exhaust storage quota.
const MaxUploadBytes = 15 << 20 // 15 MiB

// allowedContentTypes is the upload allow-list. It is an allow-list
// rather than a deny-list on purpose: accepting arbitrary types would
// let someone park executables or HTML (which can carry scripts) in a
// bucket that is served back to users.
var allowedContentTypes = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
	"image/heic":      true,
	"image/webp":      true,
}

// IsAllowedContentType reports whether a MIME type may be uploaded.
func IsAllowedContentType(ct string) bool {
	return allowedContentTypes[strings.ToLower(strings.TrimSpace(ct))]
}

// AllowedContentTypeList returns the allow-list for error messages.
func AllowedContentTypeList() string {
	out := make([]string, 0, len(allowedContentTypes))
	for ct := range allowedContentTypes {
		out = append(out, ct)
	}
	return strings.Join(out, ", ")
}

// ObjectKey mints a storage key for a new upload.
//
// The key is `<petID>/<random>.<ext>` — deliberately NOT the user's
// filename. Original names are attacker-controlled and can contain path
// separators or traversal sequences; keeping them only as a display
// label in the database means a hostile name cannot escape its prefix.
func ObjectKey(petID, originalName string) (string, error) {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(originalName))
	// Only carry across a short, alphanumeric extension.
	if len(ext) > 6 || !isAlnumExt(ext) {
		ext = ""
	}
	return fmt.Sprintf("%s/%s%s", url.PathEscape(petID), hex.EncodeToString(b[:]), ext), nil
}

func isAlnumExt(ext string) bool {
	if !strings.HasPrefix(ext, ".") {
		return false
	}
	for _, r := range ext[1:] {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Upload streams a file into the bucket under objectKey.
func (s *StorageService) Upload(objectKey, contentType string, body io.Reader) error {
	if !s.Enabled() {
		return fmt.Errorf("storage: not configured")
	}
	endpoint := fmt.Sprintf("%s/storage/v1/object/%s/%s",
		strings.TrimRight(s.cfg.SupabaseURL, "/"), s.cfg.SupabaseBucket, objectKey)

	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("storage: build upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SupabaseServiceKey)
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(objectKey))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	// Never silently replace an existing object — keys are random, so a
	// collision means something is wrong and should surface.
	req.Header.Set("x-upsert", "false")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("storage: upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("storage: upload status %d: %s", resp.StatusCode, truncateForLog(b))
	}
	return nil
}

// SignedURL mints a short-lived download URL for a stored object.
//
// The bucket is private, so this is the only way to read a file. The
// TTL is short because the URL is a bearer credential: anyone holding
// it can fetch the object until it expires.
func (s *StorageService) SignedURL(objectKey string, ttl time.Duration) (string, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("storage: not configured")
	}
	endpoint := fmt.Sprintf("%s/storage/v1/object/sign/%s/%s",
		strings.TrimRight(s.cfg.SupabaseURL, "/"), s.cfg.SupabaseBucket, objectKey)

	payload, _ := json.Marshal(map[string]any{"expiresIn": int(ttl.Seconds())})
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("storage: build sign request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("storage: sign failed: %w", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("storage: sign status %d: %s", resp.StatusCode, truncateForLog(b))
	}

	var out struct {
		SignedURL string `json:"signedURL"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("storage: decode sign response: %w", err)
	}
	if out.SignedURL == "" {
		return "", fmt.Errorf("storage: empty signedURL in response")
	}
	// Supabase returns a path relative to /storage/v1.
	return strings.TrimRight(s.cfg.SupabaseURL, "/") + "/storage/v1" + out.SignedURL, nil
}

// Delete removes an object. A failure here is not fatal to the caller:
// the database row is what the app reads, so an orphaned object costs
// storage but never shows a broken file to a user.
func (s *StorageService) Delete(objectKey string) error {
	if !s.Enabled() {
		return fmt.Errorf("storage: not configured")
	}
	endpoint := fmt.Sprintf("%s/storage/v1/object/%s/%s",
		strings.TrimRight(s.cfg.SupabaseURL, "/"), s.cfg.SupabaseBucket, objectKey)

	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SupabaseServiceKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("storage: delete failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("storage: delete status %d: %s", resp.StatusCode, truncateForLog(b))
	}
	return nil
}
