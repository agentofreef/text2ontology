// Package authmw — token signing + password hashing primitives.
//
// Both stay in this package on purpose: the middleware needs Verify, the
// login handler needs Sign + VerifyPassword, and consolidating them avoids
// a new go.mod just for two tiny files.
//
// Design choices, deliberate:
//
//   - Tokens are HMAC-SHA256 signed strings of the form
//     "<userID>.<expiryUnix>.<sig>". No JWT — JWT brings 12 alg/header
//     combinations and a long history of header-confusion bugs. This is
//     one alg, three fields, constant-time verified.
//
//   - Passwords are stored as "sha256i$<iter>$<saltb64>$<hashb64>"
//     (PBKDF2-lite using iterated SHA-256 in the standard library).
//     bcrypt would be ideal but requires vendoring x/crypto, which the
//     community repo deliberately avoids for footprint reasons.
//     Iterated SHA-256 at 200k rounds gives ~50ms verify on modern HW
//     and resists rainbow tables via per-user salt. Constant-time
//     comparison is enforced.
//
//   - AUTH_TOKEN_SECRET is required (fail-closed). No silent fallback
//     to a random per-process secret, no default value. Missing env =
//     Sign/Verify return ErrMissingSecret and the caller must surface
//     a startup error.
package authmw

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Token errors. Callers should treat any non-nil error as "unauthorized"
// and emit a 401 — never echo the specific reason to the client.
var (
	ErrMissingSecret = errors.New("AUTH_TOKEN_SECRET environment variable is not set")
	ErrInvalidFormat = errors.New("token has invalid format")
	ErrExpired       = errors.New("token expired")
	ErrBadSignature  = errors.New("token signature does not match")
)

// DefaultTTL is the lifetime of a freshly-issued session token.
// 7 days mirrors most session-cookie defaults; long enough that
// active users don't re-login mid-day, short enough to bound damage
// from a stolen token. Operators can call SignWithTTL for shorter
// scopes (e.g. MCP API exchange tokens).
const DefaultTTL = 7 * 24 * time.Hour

// SignToken issues an HMAC-signed bearer token for the given user.
// Returns ErrMissingSecret if AUTH_TOKEN_SECRET is unset — the caller
// must propagate this as a 500-class startup failure, not a 401.
func SignToken(userID string) (string, error) {
	return SignTokenWithTTL(userID, DefaultTTL)
}

// SignTokenWithTTL is the explicit-TTL form for short-lived tokens
// (e.g. password-reset links, MCP exchange flows).
func SignTokenWithTTL(userID string, ttl time.Duration) (string, error) {
	secret := os.Getenv("AUTH_TOKEN_SECRET")
	if secret == "" {
		return "", ErrMissingSecret
	}
	if userID == "" {
		return "", ErrInvalidFormat
	}
	expiry := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s.%d", userID, expiry)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

// VerifyToken validates signature + expiry and returns the embedded
// userID. Any failure returns "" + a typed error.
func VerifyToken(token string) (string, error) {
	secret := os.Getenv("AUTH_TOKEN_SECRET")
	if secret == "" {
		return "", ErrMissingSecret
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrInvalidFormat
	}
	userID, expStr, sig := parts[0], parts[1], parts[2]
	if userID == "" || expStr == "" || sig == "" {
		return "", ErrInvalidFormat
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", ErrInvalidFormat
	}
	if time.Now().Unix() > exp {
		return "", ErrExpired
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(userID + "." + expStr))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", ErrBadSignature
	}
	return userID, nil
}

// ---- Password hashing -----------------------------------------------------

// passwordIter is the SHA-256 iteration count. 200k ≈ 50ms on a 2024
// laptop; raise it once we move off pure stdlib.
const passwordIter = 200_000

// BootstrapSentinel is the placeholder password_hash value the seed
// admin row carries. The backend-api startup loop detects this value
// and forces a password reset from ADMIN_PASSWORD env before the
// service accepts logins.
const BootstrapSentinel = "BOOTSTRAP_REQUIRED"

// HashPassword returns a self-describing password digest:
//
//	sha256i$<iter>$<saltb64>$<hashb64>
//
// The format is intentionally explicit so future migrations to bcrypt
// or argon2id can coexist (prefix-dispatch in VerifyPassword).
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest := iterSHA256([]byte(password), salt, passwordIter)
	return fmt.Sprintf("sha256i$%d$%s$%s",
		passwordIter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	), nil
}

// VerifyPassword compares a plaintext attempt against a stored digest.
// Returns nil on match, an error on any failure (wrong password,
// bad format, sentinel). Use ConstantTimeCompare internally.
func VerifyPassword(password, stored string) error {
	if stored == "" || stored == BootstrapSentinel {
		return errors.New("password not set — operator must bootstrap admin via ADMIN_PASSWORD env")
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "sha256i" {
		return errors.New("unsupported password hash format")
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 {
		return errors.New("invalid iteration count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("invalid salt encoding")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return errors.New("invalid hash encoding")
	}
	got := iterSHA256([]byte(password), salt, iter)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return errors.New("password mismatch")
	}
	return nil
}

// iterSHA256 runs SHA-256 over (salt || password) and then re-hashes
// the digest N-1 more times. Deterministic, constant memory, stdlib only.
func iterSHA256(password, salt []byte, iter int) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write(password)
	d := h.Sum(nil)
	for i := 1; i < iter; i++ {
		h2 := sha256.New()
		h2.Write(d)
		d = h2.Sum(nil)
	}
	return d
}
