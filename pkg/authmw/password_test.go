package authmw

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// makeSHA256iHash builds a digest in the original "sha256i$<iter>$<salt>$<hash>"
// format using the package-internal iterSHA256, so backward-compat tests have a
// stable legacy fixture regardless of what HashPassword emits today.
func makeSHA256iHash(t *testing.T, pw string) string {
	t.Helper()
	salt := []byte("0123456789abcdef") // fixed 16-byte salt for a deterministic fixture
	const iter = 200_000
	digest := iterSHA256([]byte(pw), salt, iter)
	return fmt.Sprintf("sha256i$%d$%s$%s",
		iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	)
}

// TestHashPassword_RoundTrip: a freshly hashed password verifies, and a wrong
// password is rejected. This is the core login contract.
func TestHashPassword_RoundTrip(t *testing.T) {
	const pw = "correct horse battery staple"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned empty digest")
	}
	if err := VerifyPassword(pw, hash); err != nil {
		t.Fatalf("VerifyPassword(correct) = %v, want nil", err)
	}
	if err := VerifyPassword("wrong password", hash); err == nil {
		t.Fatal("VerifyPassword(wrong) = nil, want mismatch error")
	}
}

// TestHashPassword_SaltedUnique: two hashes of the same password must differ
// (per-hash random salt), defeating rainbow tables; both must still verify.
func TestHashPassword_SaltedUnique(t *testing.T) {
	const pw = "samePassword123"
	h1, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword #1: %v", err)
	}
	h2, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword #2: %v", err)
	}
	if h1 == h2 {
		t.Fatal("two hashes of the same password are identical — salt is not random")
	}
	if err := VerifyPassword(pw, h1); err != nil {
		t.Fatalf("h1 must verify: %v", err)
	}
	if err := VerifyPassword(pw, h2); err != nil {
		t.Fatalf("h2 must verify: %v", err)
	}
}

// TestVerifyPassword_BackwardCompatSHA256i: a legacy sha256i digest produced by
// the original iterated-SHA-256 scheme MUST still verify, so a hashing-scheme
// migration never locks out existing users.
func TestVerifyPassword_BackwardCompatSHA256i(t *testing.T) {
	const pw = "legacy-user-password"
	// Build a sha256i digest exactly as the original HashPassword did.
	legacy := makeSHA256iHash(t, pw)
	if !strings.HasPrefix(legacy, "sha256i$") {
		t.Fatalf("fixture is not a sha256i hash: %q", legacy)
	}
	if err := VerifyPassword(pw, legacy); err != nil {
		t.Fatalf("VerifyPassword must accept legacy sha256i hashes: %v", err)
	}
	if err := VerifyPassword("nope", legacy); err == nil {
		t.Fatal("legacy hash must still reject the wrong password")
	}
}

// TestVerifyPassword_Rejections: empty, sentinel, and malformed digests are all
// rejected with an error (never a silent pass).
func TestVerifyPassword_Rejections(t *testing.T) {
	cases := []struct {
		name   string
		stored string
	}{
		{"empty", ""},
		{"bootstrap sentinel", BootstrapSentinel},
		{"unknown scheme", "md5$1$abc$def"},
		{"truncated", "sha256i$200000$onlythree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyPassword("any", tc.stored); err == nil {
				t.Fatalf("VerifyPassword(%q) = nil, want rejection", tc.stored)
			}
		})
	}
}
