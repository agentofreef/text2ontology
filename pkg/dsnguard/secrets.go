package dsnguard

import (
	"fmt"
	"os"
	"strings"
)

// requireStrongSecrets reports whether the deployment has opted into strict
// secret enforcement. It is enabled by setting REQUIRE_STRONG_SECRETS to a
// truthy value (set in docker-compose.product-ready.yml). When unset/false the
// checks are a no-op so the local quick-start (default creds) keeps working.
func requireStrongSecrets() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REQUIRE_STRONG_SECRETS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// isPlaceholderSecret reports whether v is empty, a known shipped placeholder,
// or otherwise too weak to be a real production secret. The placeholder tokens
// mirror the sentinels in .env.shared.example (e.g. "*_change_me...").
func isPlaceholderSecret(v string) bool {
	t := strings.ToLower(strings.TrimSpace(v))
	if t == "" {
		return true
	}
	for _, bad := range []string{"change_me", "changeme", "placeholder", "your_", "example"} {
		if strings.Contains(t, bad) {
			return true
		}
	}
	// Trivial defaults that must never reach a public deployment.
	switch t {
	case "admin", "password", "secret", "postgres", "test":
		return true
	}
	return false
}

// AssertStrongSecrets fails fast when REQUIRE_STRONG_SECRETS is enabled and any
// shared service secret is empty or left at a known placeholder/weak default.
// It checks the two secrets every service depends on:
//
//   - AUTH_TOKEN_SECRET — HMAC key for user session tokens
//   - INTERNAL_TOKEN    — service-to-service auth token
//
// It additionally requires AUTH_TOKEN_SECRET to be reasonably long (>= 32 chars)
// because a short HMAC key is brute-forceable. Returns nil (no-op) unless
// REQUIRE_STRONG_SECRETS is set, so dev quick-start with defaults is unaffected.
// Call once at startup, right after AssertSafeDSN.
func AssertStrongSecrets(service string) error {
	if !requireStrongSecrets() {
		return nil
	}
	var problems []string
	authSecret := os.Getenv("AUTH_TOKEN_SECRET")
	if isPlaceholderSecret(authSecret) {
		problems = append(problems, "AUTH_TOKEN_SECRET is empty or a placeholder/default")
	} else if len(strings.TrimSpace(authSecret)) < 32 {
		problems = append(problems, "AUTH_TOKEN_SECRET is too short (need >= 32 chars; use `openssl rand -hex 32`)")
	}
	if isPlaceholderSecret(os.Getenv("INTERNAL_TOKEN")) {
		problems = append(problems, "INTERNAL_TOKEN is empty or a placeholder/default")
	}
	if len(problems) > 0 {
		return fmt.Errorf(
			"SECURITY: REQUIRE_STRONG_SECRETS is set but %s has weak secrets — refusing to start: %s",
			service, strings.Join(problems, "; "))
	}
	return nil
}

// AssertStrongAdminPassword enforces a non-default ADMIN_PASSWORD when strict
// secret enforcement is on. Only the service that bootstraps the admin account
// (backend-api) needs this; other services don't read ADMIN_PASSWORD. No-op
// unless REQUIRE_STRONG_SECRETS is set.
func AssertStrongAdminPassword() error {
	if !requireStrongSecrets() {
		return nil
	}
	if isPlaceholderSecret(os.Getenv("ADMIN_PASSWORD")) {
		return fmt.Errorf("SECURITY: REQUIRE_STRONG_SECRETS is set but ADMIN_PASSWORD is empty or a weak default (e.g. \"admin\") — refusing to start")
	}
	return nil
}
