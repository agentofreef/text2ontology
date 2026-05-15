package file

import (
	"testing"
)

func TestValidateURL_BlockedHostnames(t *testing.T) {
	cases := []string{
		"http://localhost/",
		"http://metadata.google.internal/",
		"http://foo.consul/",
		"http://internal-svc.internal/",
		"http://myapp.local/",
		"http://0.0.0.0/",
	}
	for _, u := range cases {
		_, _, err := validateURL(u, true)
		if err == nil {
			t.Errorf("expected hostname block for %q, got nil", u)
		}
	}
}

func TestValidateURL_BlockedIPs(t *testing.T) {
	// These hostnames parse directly as IPs so DNS is bypassed.
	cases := []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://172.16.0.1/",
		"http://169.254.169.254/",        // AWS/GCP/Azure metadata endpoint
		"http://[::1]/",                  // IPv6 loopback
		"http://[fc00::1]/",              // IPv6 ULA
		"http://[fe80::1]/",              // IPv6 link-local
	}
	for _, u := range cases {
		_, _, err := validateURL(u, true)
		if err == nil {
			t.Errorf("expected IP block for %q, got nil", u)
		}
	}
}

func TestValidateURL_BlockedSchemes(t *testing.T) {
	cases := []string{
		"ftp://example.com/",
		"file:///etc/passwd",
		"gopher://example.com/",
		"javascript:alert(1)",
		"data:text/html,<h1>hi</h1>",
		"dict://example.com/",
	}
	for _, u := range cases {
		_, _, err := validateURL(u, true)
		if err == nil {
			t.Errorf("expected scheme block for %q, got nil", u)
		}
	}
}

func TestValidateURL_HTTPDisabledByDefault(t *testing.T) {
	// allowHTTP=false should reject http://.
	_, _, err := validateURL("http://example.com/", false)
	if err == nil {
		t.Error("expected http:// rejection when allowHTTP=false, got nil")
	}
}

func TestValidateURL_BlockedPorts(t *testing.T) {
	// Ports other than 80/443 must be rejected (prevents pivoting to internal services).
	cases := []string{
		"https://example.com:22/",    // SSH
		"https://example.com:25/",    // SMTP
		"https://example.com:6379/",  // Redis
		"https://example.com:3306/",  // MySQL
		"https://example.com:5432/",  // Postgres
		"https://example.com:8080/",  // common alt-HTTP
		"https://example.com:9200/",  // Elasticsearch
	}
	for _, u := range cases {
		_, _, err := validateURL(u, true)
		if err == nil {
			t.Errorf("expected port block for %q, got nil", u)
		}
	}
}
