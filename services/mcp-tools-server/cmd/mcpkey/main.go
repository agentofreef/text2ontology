// Command mcpkey is the operator CLI for mcp_api_key management.
//
// Usage:
//
//	mcpkey create --label <name> [--tools tool1,tool2]
//	mcpkey revoke (--label <name> | --id <uuid>)
//	mcpkey list [--include-revoked]
//	mcpkey set-tools --label <name> --tools tool1,tool2     (empty string → lockdown; "*" → admin/NULL)
//
// Create prints the raw key ONCE and never stores it; operator must
// copy and share out of band. All other subcommands never emit raw
// keys. Requires DATABASE_URL to the enterprise DB.
//
// The CLI is a thin wrapper over the same schema the service uses —
// no business logic here beyond hashing. Future: rate-limit management,
// key-rotation hint, export to vault.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/lib/pq"
	_ "github.com/lib/pq"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	db := mustDB()
	defer db.Close()

	switch cmd {
	case "create":
		cmdCreate(db, args)
	case "revoke":
		cmdRevoke(db, args)
	case "list":
		cmdList(db, args)
	case "set-tools":
		cmdSetTools(db, args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `mcpkey — operator CLI for mcp_api_key

Commands:
  create     generate and insert a new key; prints raw key once
  revoke     set revoked_at on an existing key (by label or id)
  list       show all keys (hashes redacted)
  set-tools  update allowed_tools on an existing key

Examples:
  mcpkey create --label claude-code-laptop
  mcpkey create --label analyst-only --tools recall_tokens,lookup_od
  mcpkey revoke --label claude-code-laptop
  mcpkey list
  mcpkey set-tools --label analyst-only --tools recall_tokens
  mcpkey set-tools --label admin-key --tools '*'

Environment:
  DATABASE_URL  Postgres DSN for the enterprise DB (required)`)
}

// ── create ─────────────────────────────────────────────────────────────────

func cmdCreate(db *sql.DB, args []string) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	label := fs.String("label", "", "human-readable label (required; unique per key suggested)")
	tools := fs.String("tools", "",
		"comma-separated allowed tools; '*' or empty = admin (all); 'none' = lockdown")
	_ = fs.Parse(args)
	if *label == "" {
		fmt.Fprintln(os.Stderr, "--label is required")
		os.Exit(2)
	}

	raw := randomKey()
	h := hashKey(raw)

	allowed, useNull := parseTools(*tools)
	var err error
	if useNull {
		_, err = db.Exec(
			`INSERT INTO mcp_api_key (key_hash, label) VALUES ($1, $2)`, h, *label)
	} else {
		_, err = db.Exec(
			`INSERT INTO mcp_api_key (key_hash, label, allowed_tools) VALUES ($1, $2, $3)`,
			h, *label, pq.StringArray(allowed))
	}
	if err != nil {
		log.Fatalf("insert failed: %v", err)
	}
	fmt.Printf("created key for label=%q\n", *label)
	fmt.Printf("  hash        : %s\n", h)
	fmt.Printf("  allowed     : %s\n", formatTools(allowed, useNull))
	fmt.Println()
	fmt.Println("  RAW KEY (copy now; will not be shown again):")
	fmt.Printf("    %s\n", raw)
}

// ── revoke ─────────────────────────────────────────────────────────────────

func cmdRevoke(db *sql.DB, args []string) {
	fs := flag.NewFlagSet("revoke", flag.ExitOnError)
	label := fs.String("label", "", "label to revoke")
	id := fs.String("id", "", "id (UUID) to revoke")
	_ = fs.Parse(args)
	if *label == "" && *id == "" {
		fmt.Fprintln(os.Stderr, "provide --label or --id")
		os.Exit(2)
	}
	var (
		res sql.Result
		err error
	)
	if *id != "" {
		res, err = db.Exec(`UPDATE mcp_api_key SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, *id)
	} else {
		res, err = db.Exec(`UPDATE mcp_api_key SET revoked_at=now() WHERE label=$1 AND revoked_at IS NULL`, *label)
	}
	if err != nil {
		log.Fatalf("revoke failed: %v", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Println("no matching active key (already revoked?)")
		os.Exit(1)
	}
	fmt.Printf("revoked %d row(s) — takes effect on all mcp-tools-server instances within cacheTTL (30s)\n", n)
}

// ── list ───────────────────────────────────────────────────────────────────

func cmdList(db *sql.DB, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	includeRevoked := fs.Bool("include-revoked", false, "also show revoked keys")
	_ = fs.Parse(args)

	q := `SELECT id, label, substring(key_hash, 1, 12), allowed_tools,
	              created_at, last_used_at, revoked_at
	        FROM mcp_api_key`
	if !*includeRevoked {
		q += " WHERE revoked_at IS NULL"
	}
	q += " ORDER BY created_at DESC"
	rows, err := db.Query(q)
	if err != nil {
		log.Fatalf("list failed: %v", err)
	}
	defer rows.Close()
	fmt.Printf("%-36s  %-24s  %-16s  %-10s  %-19s  %-19s  %s\n",
		"id", "label", "hash-prefix", "allowed", "created", "last_used", "revoked")
	fmt.Println(strings.Repeat("─", 160))
	for rows.Next() {
		var id, label, hash string
		var allowed pq.StringArray
		var allowedNull bool
		var createdAt, lastUsed, revokedAt sql.NullTime
		var allowedRaw sql.NullString
		if err := rows.Scan(&id, &label, &hash, &allowed, &createdAt, &lastUsed, &revokedAt); err != nil {
			log.Fatalf("scan: %v", err)
		}
		_ = allowedRaw
		_ = allowedNull
		allowedStr := "all(admin)"
		if allowed != nil {
			if len(allowed) == 0 {
				allowedStr = "[]locked"
			} else {
				allowedStr = strings.Join(allowed, ",")
			}
		}
		fmt.Printf("%-36s  %-24s  %-16s  %-10s  %-19s  %-19s  %s\n",
			id, truncate(label, 24), hash+"…", truncate(allowedStr, 10),
			tsFmt(createdAt), tsFmt(lastUsed), tsFmt(revokedAt))
	}
}

// ── set-tools ──────────────────────────────────────────────────────────────

func cmdSetTools(db *sql.DB, args []string) {
	fs := flag.NewFlagSet("set-tools", flag.ExitOnError)
	label := fs.String("label", "", "label of key to update (required)")
	tools := fs.String("tools", "",
		"new allowed_tools value; '*' = admin (NULL); empty '' = lockdown ([]); csv like 'recall_tokens,lookup_od' = whitelist")
	_ = fs.Parse(args)
	if *label == "" {
		fmt.Fprintln(os.Stderr, "--label is required")
		os.Exit(2)
	}
	allowed, useNull := parseTools(*tools)
	var (
		res sql.Result
		err error
	)
	if useNull {
		res, err = db.Exec(`UPDATE mcp_api_key SET allowed_tools=NULL WHERE label=$1 AND revoked_at IS NULL`, *label)
	} else {
		res, err = db.Exec(`UPDATE mcp_api_key SET allowed_tools=$2 WHERE label=$1 AND revoked_at IS NULL`,
			*label, pq.StringArray(allowed))
	}
	if err != nil {
		log.Fatalf("update failed: %v", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Println("no matching active key")
		os.Exit(1)
	}
	fmt.Printf("updated allowed_tools for label=%q → %s\n", *label, formatTools(allowed, useNull))
}

// ── helpers ────────────────────────────────────────────────────────────────

func mustDB() *sql.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("db.Ping: %v", err)
	}
	return db
}

func randomKey() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// parseTools maps the operator's --tools string to (allowed, useNull).
//
//	""           → useNull=true  (admin, all tools)
//	"*"          → useNull=true  (same; friendlier shell escape)
//	"none"       → useNull=false, allowed=[]  (lockdown)
//	"a,b,c"      → useNull=false, allowed=[a,b,c]
func parseTools(s string) ([]string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return nil, true
	}
	if s == "none" {
		return []string{}, false
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out, false
}

func formatTools(allowed []string, useNull bool) string {
	if useNull {
		return "all(admin)"
	}
	if len(allowed) == 0 {
		return "[]locked"
	}
	return strings.Join(allowed, ",")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func tsFmt(t sql.NullTime) string {
	if !t.Valid {
		return "-"
	}
	return t.Time.Format(time.RFC3339)[:19]
}
