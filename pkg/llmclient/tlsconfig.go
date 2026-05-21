package llmclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
)

// llmCACertEnv is the env var naming a PEM file with extra CA certs to trust
// for outbound LLM/embedding HTTPS calls. When unset, system roots are used.
const llmCACertEnv = "LLM_CA_CERT"

var (
	tlsConfigOnce sync.Once
	tlsConfigVal  *tls.Config
)

// TLSClientConfig returns the *tls.Config every outbound LLM HTTP transport
// must use.
//
//   - LLM_CA_CERT unset/empty → returns nil, so http.Transport falls back to
//     the platform's system root CA pool (the secure default).
//   - LLM_CA_CERT set to a readable PEM bundle → returns a *tls.Config whose
//     RootCAs is (system roots + the appended PEM), letting operators trust a
//     private/corporate CA without disabling verification.
//
// FAIL CLOSED: if LLM_CA_CERT is set but the file is unreadable or contains no
// usable certificate, this PANICS at first use. Returning an
// InsecureSkipVerify-equivalent (nil) config in that case would silently
// re-open the MITM hole this builder exists to close, so a hard failure at
// startup is the safe behaviour.
//
// The result is computed once and cached — the env var is read at process
// start and never changes for the life of the process, matching how the LLM
// http.Client cache is keyed.
func TLSClientConfig() *tls.Config {
	tlsConfigOnce.Do(func() {
		cfg, err := buildTLSClientConfig(os.Getenv(llmCACertEnv))
		if err != nil {
			panic(fmt.Sprintf("llmclient: %s is set but unusable: %v", llmCACertEnv, err))
		}
		tlsConfigVal = cfg
	})
	return tlsConfigVal
}

// buildTLSClientConfig is the pure, testable core of TLSClientConfig.
//
// caCertPath == "" → (nil, nil): use system roots.
// caCertPath set   → load system roots, append the PEM, return a config with
// that pool. Any failure (missing file, no parseable cert) is returned as an
// error so the caller can fail closed.
func buildTLSClientConfig(caCertPath string) (*tls.Config, error) {
	if caCertPath == "" {
		return nil, nil
	}

	pem, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert file %q: %w", caCertPath, err)
	}

	// Start from the system pool so the custom CA is additive, not a
	// replacement — public endpoints (OpenAI, Anthropic, …) keep verifying.
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA cert file %q contains no valid PEM certificate", caCertPath)
	}

	return &tls.Config{RootCAs: pool}, nil
}
