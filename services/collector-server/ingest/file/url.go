package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/contracts"
	"github.com/lakehouse2ontology/services/collector-server/job"
)

// blockedCIDRs covers loopback, RFC1918, link-local, metadata, CGNAT, and IPv6 private ranges.
var blockedCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // link-local + AWS/GCP/Azure metadata endpoint
		"100.64.0.0/10",  // CGNAT (RFC6598)
		"0.0.0.0/8",      // "this" network
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 ULA (fc00:: and fd00::)
		"fe80::/10",      // IPv6 link-local
		// 不要加 "::ffff:0:0/96" — Go 的 net.ParseCIDR 对它返回畸形 IPNet
		// (IP 4 字节 + Mask 16 字节)，Network String 退化成 "0.0.0.0/0"，
		// 导致 Contains 在 4-byte IPv4 输入上误命中所有 IPv4 公网地址。
		// IPv4-mapped IPv6 由 isIPBlocked 内 ip.To4() 解构后走 IPv4 CIDR
		// 拦截路径，无需独立条目。
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, ipNet, err := net.ParseCIDR(c)
		if err == nil {
			nets = append(nets, ipNet)
		}
	}
	return nets
}()

// blockedHostSuffixes: any hostname ending with one of these is rejected.
var blockedHostSuffixes = []string{
	".consul",
	".internal",
	".local",
	".localhost",
	".metadata",
}

// blockedExactHosts: exact hostname matches that are always rejected.
var blockedExactHosts = map[string]bool{
	"localhost":                true,
	"metadata":                 true,
	"metadata.google.internal": true,
	"127.0.0.1":                true,
	"::1":                      true,
	"0.0.0.0":                  true,
}

func isHostnameBlocked(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if blockedExactHosts[h] {
		return true
	}
	for _, suf := range blockedHostSuffixes {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	return false
}

func isIPBlocked(ip net.IP) bool {
	// Unmap IPv4-in-IPv6 so 192.168.x.x presented as ::ffff:192.168.x.x is caught.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, ipNet := range blockedCIDRs {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// validateURL performs full SSRF validation:
//  1. Scheme: only http (when allowHTTP) or https.
//  2. Hostname blacklist.
//  3. Port: only 80 or 443 (or implicit).
//  4. DNS resolution: ALL returned IPs must be non-private.
//
// Returns the parsed URL and the resolved IPs (used for DNS-rebinding-safe dialing).
func validateURL(rawURL string, allowHTTP bool) (*url.URL, []net.IP, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("URL_PARSE: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, nil, errors.New("SCHEME_NOT_ALLOWED: only http/https permitted")
	}
	if scheme == "http" && !allowHTTP {
		return nil, nil, errors.New("HTTP_DISABLED: set COLLECTOR_ALLOW_HTTP=true for http:// (dev only)")
	}

	host := u.Hostname()
	if host == "" {
		return nil, nil, errors.New("HOSTNAME_EMPTY")
	}
	if isHostnameBlocked(host) {
		return nil, nil, fmt.Errorf("HOSTNAME_BLOCKED: %s", host)
	}

	// Port check: only standard web ports allowed.
	if portStr := u.Port(); portStr != "" {
		port, convErr := strconv.Atoi(portStr)
		if convErr != nil || (port != 80 && port != 443) {
			return nil, nil, fmt.Errorf("PORT_NOT_ALLOWED: %s (only 80/443)", portStr)
		}
	}

	// Resolve DNS — ALL IPs must be non-private (TOCTOU + multi-A mitigation).
	ips, resolveErr := net.LookupIP(host)
	if resolveErr != nil {
		return nil, nil, fmt.Errorf("DNS_RESOLVE_FAILED: %w", resolveErr)
	}
	if len(ips) == 0 {
		return nil, nil, errors.New("NO_IPS_RESOLVED")
	}
	for _, ip := range ips {
		if isIPBlocked(ip) {
			return nil, nil, fmt.Errorf("IP_BLOCKED: %s resolved to blocked IP %s", host, ip)
		}
	}

	return u, ips, nil
}

// HandleURL — POST /api/connector/file/url
// Body: { "url": "https://...", "project_id": "...", "label": "..." }
func (s *Service) HandleURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}

	var req struct {
		URL       string `json:"url"`
		ProjectID string `json:"project_id"`
		Label     string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BODY_PARSE", err.Error())
		return
	}
	if req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_PROJECT_ID", "project_id required")
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "MISSING_URL", "url required")
		return
	}

	allowHTTP := os.Getenv("COLLECTOR_ALLOW_HTTP") == "true"
	parsedURL, ips, err := validateURL(req.URL, allowHTTP)
	if err != nil {
		writeError(w, http.StatusForbidden, "SSRF_BLOCKED", err.Error())
		return
	}

	// DNS-rebinding defence: use the resolved IP directly in DialContext.
	// This prevents a second DNS lookup at connect time from returning a different IP.
	dialIP := ips[0].String()

	transport := &http.Transport{
		DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			if port == "" {
				if parsedURL.Scheme == "https" {
					port = "443"
				} else {
					port = "80"
				}
			}
			target := net.JoinHostPort(dialIP, port)
			d := &net.Dialer{Timeout: 30 * time.Second}
			return d.DialContext(dialCtx, network, target)
		},
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Minute,
		// Do not follow redirects to avoid redirect-based SSRF.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("REDIRECT_BLOCKED: redirects not followed for security")
		},
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "REQ_BUILD", err.Error())
		return
	}

	var resp *http.Response
	var lastErr error
	var lastStatus int
	for attempt := 0; attempt < 3; attempt++ {
		resp, lastErr = client.Do(httpReq)
		if lastErr == nil && resp != nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			lastStatus = resp.StatusCode
			resp.Body.Close()
			resp = nil
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}
	// 三次重试后 lastErr 与 resp 的可能组合：
	//   (err!=nil, resp=nil)            — 网络故障
	//   (err==nil, resp=nil, status>0)  — 上游一直非 200，最后一次也 close 了
	//   (err==nil, resp!=nil, 200)      — 成功（已 break）
	if resp == nil {
		if lastErr != nil {
			writeError(w, http.StatusBadGateway, "FETCH_FAILED", lastErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "FETCH_NON_200",
			fmt.Sprintf("upstream returned %d after 3 retries", lastStatus))
		return
	}
	defer resp.Body.Close()

	// Pre-check Content-Length if present.
	if cl := resp.ContentLength; cl > 0 && cl > s.MaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE",
			fmt.Sprintf("Content-Length %d > limit %d", cl, s.MaxBytes))
		return
	}

	// Create data_source row.
	dsID := uuid.New().String()
	label := req.Label
	if label == "" {
		label = parsedURL.Path
		if label == "" || label == "/" {
			label = parsedURL.Host
		}
	}
	configJSON := map[string]any{
		"url":    req.URL,
		"host":   parsedURL.Host,
		"scheme": parsedURL.Scheme,
	}
	cfgRaw, _ := json.Marshal(configJSON)
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO data_source (id, project_id, type, label, config_json, status)
		VALUES ($1, $2, 'file', $3, $4, 'syncing')
	`, dsID, req.ProjectID, label, cfgRaw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_INSERT_FAILED", err.Error())
		return
	}

	// Write to disk with size limit.
	dirPath := filepath.Join(s.UploadRoot, dsID)
	if mkErr := os.MkdirAll(dirPath, 0755); mkErr != nil {
		writeError(w, http.StatusInternalServerError, "MKDIR_FAILED", mkErr.Error())
		return
	}
	base := filepath.Base(parsedURL.Path)
	if base == "" || base == "." || base == "/" {
		base = "download"
	}
	diskPath := filepath.Join(dirPath, base)

	out, err := os.Create(diskPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, s.MaxBytes+1))
	out.Close()

	if copyErr != nil {
		os.Remove(diskPath)
		_, _ = s.DB.ExecContext(ctx, `UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, dsID)
		writeError(w, http.StatusBadGateway, "DOWNLOAD_FAILED", copyErr.Error())
		return
	}
	if written > s.MaxBytes {
		os.Remove(diskPath)
		_, _ = s.DB.ExecContext(ctx, `UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, dsID)
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE",
			fmt.Sprintf("downloaded %d bytes > limit %d", written, s.MaxBytes))
		return
	}

	// Parse headers — supports multi-sheet xlsx.
	ext := strings.ToLower(filepath.Ext(base))
	sheets, parseErr := parseHeaders(diskPath, ext)
	if parseErr != nil {
		_, _ = s.DB.ExecContext(ctx, `UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, dsID)
		writeError(w, http.StatusUnprocessableEntity, "PARSE_FAILED", parseErr.Error())
		return
	}
	if len(sheets) == 0 {
		_, _ = s.DB.ExecContext(ctx, `UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, dsID)
		writeError(w, http.StatusUnprocessableEntity, "EMPTY_FILE", "no readable sheets / headers found")
		return
	}

	// Persist staging_schema then enqueue async COPY job. Browser sees
	// catalog instantly while the worker pool drains rows in background.
	stagingSchema := "collector_" + strings.ReplaceAll(dsID, "-", "_")
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE data_source SET staging_schema=$1, updated_at=now() WHERE id=$2`,
		stagingSchema, dsID); err != nil {
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusInternalServerError, "DB_UPDATE_FAILED", err.Error())
		return
	}
	jobID, err := job.Enqueue(ctx, s.DB, job.EnqueueArgs{
		DataSourceID: &dsID,
		ProjectID:    req.ProjectID,
		Kind:         job.KindFileUpload,
		Payload: fileUploadPayload{
			DiskPath:      diskPath,
			Ext:           ext,
			StagingSchema: stagingSchema,
			Filename:      base,
		},
	})
	if err != nil {
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusInternalServerError, "JOB_ENQUEUE_FAILED", err.Error())
		return
	}

	tables := make([]contracts.TableInfo, 0, len(sheets))
	for _, sh := range sheets {
		tables = append(tables, contracts.TableInfo{Name: sh.Name, Columns: sh.Columns})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		ID      string                `json:"id"`
		JobID   string                `json:"jobId"`
		Status  string                `json:"status"`
		Catalog contracts.CatalogResp `json:"catalog"`
	}{
		ID:      dsID,
		JobID:   jobID,
		Status:  "queued",
		Catalog: contracts.CatalogResp{Tables: tables},
	})
}
