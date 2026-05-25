package llmclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// envFlagTrue reads an env var and returns true iff it's set to a recognised
// truthy value (case-insensitive). Empty / unset is false.
func envFlagTrue(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// llmCACertEnv is the env var naming a PEM file with extra CA certs to trust
// for outbound LLM/embedding HTTPS calls. When unset, system roots are used.
const llmCACertEnv = "LLM_CA_CERT"

// llmInsecureEnv is the escape-hatch env var that disables TLS verification
// for outbound LLM/embedding HTTPS calls. Set to "1" / "true" / "yes" to
// enable. INTENDED USE: intra-net or self-signed dev LLM endpoints where the
// operator cannot install a CA bundle. NEVER set true on a host that calls
// public LLM providers (OpenAI, Anthropic, …) — it disables MITM defence
// against anyone on the network path.
//
// Precedence: if both LLM_TLS_INSECURE_SKIP_VERIFY=true and LLM_CA_CERT are
// set, InsecureSkipVerify wins (the weaker setting takes effect) — useful
// when an operator has a partial CA bundle and still needs to skip another
// endpoint. A log warning is emitted at first use so the conflict is visible.
const llmInsecureEnv = "LLM_TLS_INSECURE_SKIP_VERIFY"

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
		cfg, err := buildTLSClientConfig(os.Getenv(llmCACertEnv), envFlagTrue(llmInsecureEnv))
		if err != nil {
			panic(fmt.Sprintf("llmclient: %s is set but unusable: %v", llmCACertEnv, err))
		}
		tlsConfigVal = cfg
	})
	return tlsConfigVal
}

// buildTLSClientConfig is the pure, testable core of TLSClientConfig.
//
// insecure == true → returns &tls.Config{InsecureSkipVerify: true} regardless
// of caCertPath. A custom CA is appended too (harmless) so that flipping the
// insecure flag off later goes back to a fully-validated config without
// re-loading the env. A WARN is logged once so the bypass is visible in any
// healthy log scrape.
//
// caCertPath == "" → (nil, nil): use system roots (the default).
// caCertPath set   → load system roots, append the PEM, return a config with
// that pool. Any failure (missing file, no parseable cert) is returned as an
// error so the caller can fail closed.
func buildTLSClientConfig(caCertPath string, insecure bool) (*tls.Config, error) {
	// Load custom CA first (so a bad PEM still fails closed even when
	// insecure is set — operators who set both probably want the CA usable).
	var pool *x509.CertPool
	if caCertPath != "" {
		pem, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("read CA cert file %q: %w", caCertPath, err)
		}
		p, sysErr := x509.SystemCertPool()
		if sysErr != nil || p == nil {
			p = x509.NewCertPool()
		}
		if !p.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA cert file %q contains no valid PEM certificate", caCertPath)
		}
		pool = p
	}

	if insecure {
		// One-shot WARN so it's visible in the logs but not spammy. The bypass
		// affects EVERY outbound LLM/embedding HTTPS call from this process.
		log.Printf("[llmclient] WARNING: %s=true — TLS certificate verification DISABLED for ALL outbound LLM HTTPS calls. This is unsafe for public providers; only use in trusted intra-net / self-signed dev setups.", llmInsecureEnv)
		return &tls.Config{InsecureSkipVerify: true, RootCAs: pool}, nil //nolint:gosec // operator-opted bypass for self-signed intra-net LLM
	}

	if pool == nil {
		return nil, nil
	}
	return &tls.Config{RootCAs: pool}, nil
}
