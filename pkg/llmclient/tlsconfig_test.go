package llmclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTempCACert generates a throwaway self-signed CA and writes it as PEM to
// a temp file, returning the path. Used to exercise the "valid PEM" branch
// without shipping a fixture cert in the repo.
func writeTempCACert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode pem: %v", err)
	}
	return path
}

func TestBuildTLSClientConfig_Unset(t *testing.T) {
	cfg, err := buildTLSClientConfig("", false)
	if err != nil {
		t.Fatalf("unset must not error, got %v", err)
	}
	if cfg != nil {
		t.Fatalf("unset must return nil config (system roots), got %#v", cfg)
	}
}

func TestBuildTLSClientConfig_ValidPEM(t *testing.T) {
	path := writeTempCACert(t)
	cfg, err := buildTLSClientConfig(path, false)
	if err != nil {
		t.Fatalf("valid PEM must not error, got %v", err)
	}
	if cfg == nil {
		t.Fatal("valid PEM must return a non-nil config")
	}
	if cfg.RootCAs == nil {
		t.Fatal("valid PEM must populate RootCAs")
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("default path must never set InsecureSkipVerify")
	}
}

func TestBuildTLSClientConfig_BadPath(t *testing.T) {
	cfg, err := buildTLSClientConfig(filepath.Join(t.TempDir(), "does-not-exist.pem"), false)
	if err == nil {
		t.Fatal("missing file must fail closed with an error")
	}
	if cfg != nil {
		t.Fatalf("error path must return nil config, got %#v", cfg)
	}
}

func TestBuildTLSClientConfig_InvalidPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cfg, err := buildTLSClientConfig(path, false)
	if err == nil {
		t.Fatal("non-PEM content must fail closed with an error")
	}
	if cfg != nil {
		t.Fatalf("error path must return nil config, got %#v", cfg)
	}
}

// TestBuildTLSClientConfig_Insecure verifies the escape-hatch: when the
// operator opts in via LLM_TLS_INSECURE_SKIP_VERIFY, the returned config
// disables verification. Documented as unsafe for public providers; this
// test only proves the flag wires through.
func TestBuildTLSClientConfig_Insecure(t *testing.T) {
	cfg, err := buildTLSClientConfig("", true)
	if err != nil {
		t.Fatalf("insecure must not error, got %v", err)
	}
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatalf("insecure must return config with InsecureSkipVerify=true, got %#v", cfg)
	}
}

// TestBuildTLSClientConfig_InsecureWithCA verifies the precedence rule:
// when both inputs are set, InsecureSkipVerify wins but the CA pool is still
// populated so a later config swap can re-enable validation cleanly.
func TestBuildTLSClientConfig_InsecureWithCA(t *testing.T) {
	path := writeTempCACert(t)
	cfg, err := buildTLSClientConfig(path, true)
	if err != nil {
		t.Fatalf("insecure+CA must not error, got %v", err)
	}
	if cfg == nil {
		t.Fatal("insecure+CA must return a non-nil config")
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("insecure flag must dominate when both are set")
	}
	if cfg.RootCAs == nil {
		t.Fatal("CA pool must still be populated alongside the bypass")
	}
}

// TestBuildTLSClientConfig_InsecureBadCA verifies fail-closed behaviour:
// even when insecure is opted in, a malformed CA bundle is still surfaced
// as an error (the operator clearly meant to configure both — drop a hint
// rather than silently swallowing the broken bundle).
func TestBuildTLSClientConfig_InsecureBadCA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cfg, err := buildTLSClientConfig(path, true)
	if err == nil {
		t.Fatal("bad CA must fail closed even when insecure=true")
	}
	if cfg != nil {
		t.Fatalf("error path must return nil config, got %#v", cfg)
	}
}
