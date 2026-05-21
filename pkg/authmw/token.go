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
//   - Passwords are hashed with argon2id (golang.org/x/crypto/argon2), the
//     current best-practice memory-hard KDF. Stored as a self-describing
//     PHC-style string:
//       "argon2id$v=19$m=<mem>,t=<time>,p=<par>$<saltb64>$<hashb64>"
//     VerifyPassword prefix-dispatches: legacy "sha256i$..." digests (the
//     previous iterated-SHA-256 scheme) still verify, so existing users are
//     not locked out; new hashes are always argon2id. Constant-time
//     comparison is enforced for both schemes.
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

	"golang.org/x/crypto/argon2"
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

// argon2id parameters (RFC 9106 second-recommended profile, tuned for an
// interactive login). 64 MiB memory, 1 pass, 4 lanes ≈ tens of ms on a
// modern server while being memory-hard against GPU/ASIC cracking.
const (
	argonTime    uint32 = 1
	argonMemory  uint32 = 64 * 1024 // KiB → 64 MiB
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// BootstrapSentinel is the placeholder password_hash value the seed
// admin row carries. The backend-api startup loop detects this value
// and forces a password reset from ADMIN_PASSWORD env before the
// service accepts logins.
const BootstrapSentinel = "BOOTSTRAP_REQUIRED"

// HashPassword returns a self-describing argon2id digest in PHC-ish form:
//
//	argon2id$v=19$m=<mem>,t=<time>,p=<par>$<saltb64>$<hashb64>
//
// The scheme prefix lets VerifyPassword prefix-dispatch, so legacy
// "sha256i$" digests continue to verify during/after migration.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	), nil
}

// VerifyPassword compares a plaintext attempt against a stored digest.
// Returns nil on match, an error on any failure (wrong password, bad
// format, sentinel). Prefix-dispatches on the scheme tag so both the new
// argon2id format and legacy sha256i digests verify. Constant-time
// comparison is used for both.
func VerifyPassword(password, stored string) error {
	if stored == "" || stored == BootstrapSentinel {
		return errors.New("password not set — operator must bootstrap admin via ADMIN_PASSWORD env")
	}
	switch {
	case strings.HasPrefix(stored, "argon2id$"):
		return verifyArgon2id(password, stored)
	case strings.HasPrefix(stored, "sha256i$"):
		return verifySHA256i(password, stored)
	default:
		return errors.New("unsupported password hash format")
	}
}

// verifyArgon2id validates an "argon2id$v=..$m=..,t=..,p=..$salt$hash" digest.
func verifyArgon2id(password, stored string) error {
	parts := strings.Split(stored, "$")
	// ["argon2id", "v=19", "m=..,t=..,p=..", saltb64, hashb64]
	if len(parts) != 5 || parts[0] != "argon2id" {
		return errors.New("invalid argon2id hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil {
		return errors.New("invalid argon2id version")
	}
	var mem, t uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &mem, &t, &par); err != nil {
		return errors.New("invalid argon2id parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return errors.New("invalid salt encoding")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return errors.New("invalid hash encoding")
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, par, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return errors.New("password mismatch")
	}
	return nil
}

// verifySHA256i validates a legacy "sha256i$<iter>$<salt>$<hash>" digest.
func verifySHA256i(password, stored string) error {
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
