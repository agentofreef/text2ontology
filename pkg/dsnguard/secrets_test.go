package dsnguard

import (
	"strings"
	"testing"
)

// strong is a 40-char non-placeholder secret used to satisfy the length + content checks.
const strong = "abcdef0123456789abcdef0123456789abcdef01"

func TestAssertStrongSecrets_NoopWhenDisabled(t *testing.T) {
	// REQUIRE_STRONG_SECRETS unset → always nil, even with empty secrets.
	t.Setenv("REQUIRE_STRONG_SECRETS", "")
	t.Setenv("AUTH_TOKEN_SECRET", "")
	t.Setenv("INTERNAL_TOKEN", "")
	if err := AssertStrongSecrets("svc"); err != nil {
		t.Fatalf("expected nil when enforcement disabled, got %v", err)
	}
}

func TestAssertStrongSecrets_RejectsPlaceholders(t *testing.T) {
	cases := []struct {
		name, auth, internal string
	}{
		{"empty auth", "", strong},
		{"placeholder auth", "t2o_local_auth_secret_change_me_before_real_deploy", strong},
		{"short auth", "tooshort", strong},
		{"empty internal", strong, ""},
		{"placeholder internal", strong, "t2o_internal_change_me"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("REQUIRE_STRONG_SECRETS", "true")
			t.Setenv("AUTH_TOKEN_SECRET", c.auth)
			t.Setenv("INTERNAL_TOKEN", c.internal)
			if err := AssertStrongSecrets("svc"); err == nil {
				t.Fatalf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestAssertStrongSecrets_AcceptsStrong(t *testing.T) {
	t.Setenv("REQUIRE_STRONG_SECRETS", "1")
	t.Setenv("AUTH_TOKEN_SECRET", strong)
	t.Setenv("INTERNAL_TOKEN", strong)
	if err := AssertStrongSecrets("svc"); err != nil {
		t.Fatalf("expected nil for strong secrets, got %v", err)
	}
}

func TestAssertStrongAdminPassword(t *testing.T) {
	t.Setenv("REQUIRE_STRONG_SECRETS", "true")

	t.Setenv("ADMIN_PASSWORD", "admin")
	if err := AssertStrongAdminPassword(); err == nil {
		t.Fatal("expected error for default admin password")
	}
	if err := AssertStrongAdminPassword(); err != nil && !strings.Contains(err.Error(), "ADMIN_PASSWORD") {
		t.Fatalf("error should mention ADMIN_PASSWORD, got %v", err)
	}

	t.Setenv("ADMIN_PASSWORD", "a-real-rotated-password")
	if err := AssertStrongAdminPassword(); err != nil {
		t.Fatalf("expected nil for strong admin password, got %v", err)
	}

	// Disabled → no-op even with a weak value.
	t.Setenv("REQUIRE_STRONG_SECRETS", "")
	t.Setenv("ADMIN_PASSWORD", "admin")
	if err := AssertStrongAdminPassword(); err != nil {
		t.Fatalf("expected nil when enforcement disabled, got %v", err)
	}
}
