package services

import (
	"crypto/aes"
	"crypto/subtle"
	"fmt"
	"time"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/models"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// AuthService handles JWT tokens and AES password encryption.
type AuthService struct {
	cfg *config.Config
}

// NewAuthService creates a new AuthService.
func NewAuthService(cfg *config.Config) *AuthService {
	return &AuthService{cfg: cfg}
}

// --- JWT ---

// TokenPair holds access and refresh tokens.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Claims are the JWT payload fields.
// Field names match what the Next.js frontend expects when decoding the token.
//
// `LastName` historically holds the Georgian personal ID, not a surname
// (the legacy `memberlogin_members.last_name` column was reused for it
// — see models/user.go). New backend code should call `PersonalID()` so
// the intent is obvious and the JSON shape can later be split without
// touching every call site.
type Claims struct {
	UserID      uint   `json:"user_id"`
	GroupID     int    `json:"group_id"`
	Zip         string `json:"zip"`
	Email       string `json:"email"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	CompanyName string `json:"company_name"`
	jwt.RegisteredClaims
}

// PersonalID returns the user's Georgian personal ID, which is stored
// in the `last_name` column / claim for legacy reasons. Prefer this
// over reading `claims.LastName` directly so future renames don't have
// to touch every ownership check.
func (c *Claims) PersonalID() string {
	if c == nil {
		return ""
	}
	return c.LastName
}

// GenerateTokenPair creates a new access + refresh token pair for the user.
func (s *AuthService) GenerateTokenPair(user *models.User) (*TokenPair, error) {
	// Access token — 24 hours
	accessClaims := &Claims{
		UserID:      user.ID,
		GroupID:     user.GroupID,
		Zip:         user.Zip,
		Email:       user.Email,
		FirstName:   user.FirstName,
		LastName:    user.LastName,
		CompanyName: user.CompanyName,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", user.ID),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Refresh token — 30 days
	refreshClaims := &Claims{
		UserID: user.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshStr, err := refreshToken.SignedString([]byte(s.cfg.JWTRefreshSecret))
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessStr,
		RefreshToken: refreshStr,
	}, nil
}

// ValidateAccessToken parses and validates an access token, returning the claims.
func (s *AuthService) ValidateAccessToken(tokenStr string) (*Claims, error) {
	return s.parseToken(tokenStr, s.cfg.JWTSecret)
}

// ValidateRefreshToken parses and validates a refresh token, returning the claims.
func (s *AuthService) ValidateRefreshToken(tokenStr string) (*Claims, error) {
	return s.parseToken(tokenStr, s.cfg.JWTRefreshSecret)
}

func (s *AuthService) parseToken(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// --- AES Password Encryption (legacy) ---
// Passwords in Supabase are encrypted with AES-128-ECB using MySQL-style key derivation
// (XOR-fold the salt into 16 bytes). Padding is PKCS7.
// Salt for Supabase: DW3Z07FI (different from PHP/MySQL which uses RZ8HU1EB).

// aesKey derives a 16-byte key by XOR-folding the salt (MySQL's key derivation).
func (s *AuthService) aesKey() []byte {
	key := make([]byte, 16)
	for i, b := range []byte(s.cfg.AESSalt) {
		key[i%16] ^= b
	}
	return key
}

// EncryptPassword encrypts a plaintext password using AES-128-ECB with PKCS7 padding.
// Returns raw bytes suitable for storing in the bytea column.
func (s *AuthService) EncryptPassword(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(s.aesKey())
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	data := pkcs7Pad([]byte(plaintext), aes.BlockSize)

	ciphertext := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Encrypt(ciphertext[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}

	return ciphertext, nil
}

// DecryptPassword decrypts an AES-128-ECB encrypted password from raw bytes.
func (s *AuthService) DecryptPassword(encrypted []byte) (string, error) {
	if len(encrypted) == 0 {
		return "", fmt.Errorf("empty ciphertext")
	}
	if len(encrypted)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext length %d is not a multiple of block size", len(encrypted))
	}

	block, err := aes.NewCipher(s.aesKey())
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	plaintext := make([]byte, len(encrypted))
	for i := 0; i < len(encrypted); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], encrypted[i:i+aes.BlockSize])
	}

	// Remove PKCS7 padding
	unpadded, err := pkcs7Unpad(plaintext)
	if err != nil {
		return "", fmt.Errorf("failed to unpad: %w", err)
	}

	return string(unpadded), nil
}

// --- bcrypt (preferred password storage) ---

// HashPassword returns a bcrypt hash suitable for storage in the
// memberlogin_members.password_hash column. Cost 12 ≈ 250 ms on a
// modest server — slow enough to deter brute force, fast enough that
// a synchronous login still feels responsive.
func (s *AuthService) HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), 12)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// VerifyBcrypt returns true iff the bcrypt hash matches the plaintext.
// Constant-time by construction (bcrypt internals).
func (s *AuthService) VerifyBcrypt(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// VerifyLegacyAES decrypts an AES-stored password and compares it to
// `plaintext` in constant time. Used during the dual-read migration
// window for users whose password_hash is still empty.
func (s *AuthService) VerifyLegacyAES(encrypted []byte, plaintext string) bool {
	stored, err := s.DecryptPassword(encrypted)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(plaintext)) == 1
}

// pkcs7Pad pads data to a multiple of blockSize using PKCS7.
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padBytes := make([]byte, padding)
	for i := range padBytes {
		padBytes[i] = byte(padding)
	}
	return append(data, padBytes...)
}

// pkcs7Unpad removes PKCS7 padding.
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padding := int(data[len(data)-1])
	if padding > len(data) || padding == 0 || padding > aes.BlockSize {
		return nil, fmt.Errorf("invalid padding")
	}
	for i := len(data) - padding; i < len(data); i++ {
		if data[i] != byte(padding) {
			return nil, fmt.Errorf("invalid padding")
		}
	}
	return data[:len(data)-padding], nil
}
