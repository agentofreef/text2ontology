package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestByIDHandlersEnforceProjectAccess is a regression tripwire for the
// cross-project IDOR class fixed in .omc/specs/deep-interview-idor-isolation.md.
//
// Rule: any function that extracts an entity id via ExtractID(...) is a by-id
// handler and MUST also call one of the project-access enforcement helpers
// (so a foreign entity UUID cannot reach a DB op unchecked). If you add a new
// by-id handler and forget the guard, this test fails.
//
// Limitation: it keys on the ExtractID pattern (the dominant one). Handlers
// that parse the path manually (strings.Split) are not covered here and must
// be guarded by review. Add genuinely-exempt functions to `exempt` with a
// reason.
func TestByIDHandlersEnforceProjectAccess(t *testing.T) {
	enforce := map[string]bool{
		"EnforceEntityProject":      true,
		"EnforceEntityProjectVia":   true,
		"EnforceEntityOwner":        true,
		"EnforceProjectAccess":      true,
		"EnforceProjectFromRequest": true,
		"UserCanAccessProject":      true,
	}
	// Functions that use ExtractID but legitimately need no project gate.
	exempt := map[string]bool{}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var usesExtractID, usesEnforce bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fnx := call.Fun.(type) {
				case *ast.Ident:
					if fnx.Name == "ExtractID" {
						usesExtractID = true
					}
				case *ast.SelectorExpr:
					if fnx.Sel.Name == "ExtractID" {
						usesExtractID = true
					}
					if enforce[fnx.Sel.Name] {
						usesEnforce = true
					}
				}
				return true
			})
			if usesExtractID && !usesEnforce && !exempt[fn.Name.Name] {
				t.Errorf("%s: by-id handler %q calls ExtractID but no project-access guard "+
					"(EnforceEntityProject/Via/Owner, EnforceProjectAccess, or add to exempt with a reason)",
					name, fn.Name.Name)
			}
		}
	}
}
