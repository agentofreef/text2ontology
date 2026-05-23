// OD Catalog block — inline summary of marked ODs + properties + links,
// injected into the lakehouse-mode system prompt so the LLM has the token
// universe it can use for compose_query without first calling list/inspect.
//
// Why this block exists:
//   compose_query lets the LLM emit {odName, metric, filters, groupBy} from
//   catalog tokens. If the LLM doesn't know what tokens exist, it can only
//   guess from recall hits — which is what got us here in the first place
//   (recall miss → wrong intent → wrong answer). The catalog is the
//   ground-truth list of "things you can reference".
//
// Why it's a separate block (not folded into the recall context):
//   The recall context is per-question (only intents/properties matched by
//   the user's question tokens). The catalog is per-project and stable
//   across turns. Mixing them would make the LLM think the catalog only
//   contains the recalled subset.
//
// Size: ~30-50 chars per OD + ~10 chars per property. For Northwind (4 OD,
// 23 properties total): ~500 chars. Fine.

package handler

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// buildODCatalogBlock returns a markdown summary of the project's active
// ODs with property names and 1-hop link targets. Empty string if the
// project has no marked ODs (rare; usually means project not yet built).
func buildODCatalogBlock(ctx context.Context, db *sql.DB, projectID string) string {
	if db == nil || strings.TrimSpace(projectID) == "" {
		return ""
	}

	// Pull all marked ODs + their property names in one query (left-join
	// preserves ODs with zero properties, which we still want to show as
	// "(no properties)" for the LLM to know they exist).
	rows, err := db.QueryContext(ctx, `
		SELECT o.id, o.name, COALESCE(o.kind, ''),
		       COALESCE(array_agg(p.name ORDER BY p.name) FILTER (WHERE p.name IS NOT NULL), '{}')
		FROM ont_object_type o
		LEFT JOIN ont_property p ON p.object_type_id = o.id
		WHERE o.project_id = $1 AND o.mark = true
		GROUP BY o.id, o.name, o.kind
		ORDER BY o.name`,
		projectID,
	)
	if err != nil {
		return ""
	}
	defer rows.Close()

	type odRow struct {
		id   string
		name string
		kind string
		prop []string
	}
	var ods []odRow
	odNameByID := make(map[string]string)
	for rows.Next() {
		var r odRow
		var props pq.StringArray
		if err := rows.Scan(&r.id, &r.name, &r.kind, &props); err != nil {
			continue
		}
		r.prop = []string(props)
		ods = append(ods, r)
		odNameByID[r.id] = r.name
	}
	if rows.Err() != nil || len(ods) == 0 {
		return ""
	}

	// Pull 1-hop link map: from_object_id → []{name, fk_column, to_name}.
	type linkRow struct {
		fromID   string
		toName   string
		fkColumn string
		linkName string
	}
	links := make(map[string][]linkRow)
	if lrows, err := db.QueryContext(ctx, `
		SELECT l.from_object_id, l.to_object_id,
		       COALESCE(l.link_name,''), COALESCE(l.fk_column,'')
		FROM ont_link_type l
		WHERE l.project_id = $1 AND l.mark = true AND l.deleted_at IS NULL`,
		projectID,
	); err == nil {
		defer lrows.Close()
		for lrows.Next() {
			var r linkRow
			var toID string
			if err := lrows.Scan(&r.fromID, &toID, &r.linkName, &r.fkColumn); err != nil {
				continue
			}
			r.toName = odNameByID[toID]
			if r.toName == "" {
				continue // link target isn't an active OD; skip
			}
			links[r.fromID] = append(links[r.fromID], r)
		}
	}

	// Low-cardinality value domains per (OD, property), so the LLM sees the
	// actual filterable values and cannot fabricate a non-existent one. Keyed
	// "OD\x00PROP"; high-cardinality / value-less properties are absent.
	domains := loadCatalogValueDomains(ctx, db, projectID)

	var b strings.Builder
	b.WriteString("## 📋 OD Catalog（smartquery 自由组合模式可用 token）\n\n")
	b.WriteString("调用 smartquery 自由组合模式（不带 intent）时 odName / metric arg / filters[].property / groupBy[] 都必须从下表选。\n")
	b.WriteString("**跨 OD 引用语法**：filter/groupBy 里写 `OD.Property` 形式（如 `CUSTOMER.Country` / `PRODUCT.CategoryName`），引擎会自动按下面列出的链路（→）做 JOIN。primary `odName` 仍只填一个主 OD。\n")
	b.WriteString("property 后的 `{a | b}` 是该列**完整的可筛选值域**；用户要的值不在其中，就不要臆造映射——向用户澄清或声明缺口。\n\n")
	for _, r := range ods {
		propPart := "（无 property — 先 inspect）"
		if len(r.prop) > 0 {
			parts := make([]string, 0, len(r.prop))
			for _, pn := range r.prop {
				s := pn
				if dom := domains[r.name+"\x00"+pn]; len(dom) > 0 {
					s += " {" + strings.Join(dom, " | ") + "}"
				}
				parts = append(parts, s)
			}
			propPart = strings.Join(parts, ", ")
		}
		fmt.Fprintf(&b, "- **%s** (%s): %s\n", r.name, r.kind, propPart)
		for _, ln := range links[r.id] {
			label := ln.linkName
			if label == "" {
				label = "(unnamed)"
			}
			fkPart := ""
			if ln.fkColumn != "" {
				fkPart = fmt.Sprintf(" via %s", ln.fkColumn)
			}
			fmt.Fprintf(&b, "  → %s%s → %s\n", label, fkPart, ln.toName)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// loadCatalogValueDomains returns the low-cardinality value domain for each
// (OD, property) in the project, keyed "OD\x00PROP". Only properties with a
// small, enumerable set of value-keywords (≤ valueDomainCap) are included;
// high-cardinality and value-less properties are omitted (no domain shown).
// Source is lakehouse_keyword value-keywords (enum_values is unpopulated).
func loadCatalogValueDomains(ctx context.Context, db *sql.DB, projectID string) map[string][]string {
	out := map[string][]string{}
	if db == nil || strings.TrimSpace(projectID) == "" {
		return out
	}
	rows, err := db.QueryContext(ctx, `
		SELECT o.name, p.name, array_agg(DISTINCT k.keyword ORDER BY k.keyword)
		FROM lakehouse_keyword k
		JOIN ont_property p ON p.id = k.property_id
		JOIN ont_object_type o ON o.id = p.object_type_id
		WHERE k.project_id = $1 AND o.mark = true
		  AND COALESCE(k.is_column_name, false) = false
		  AND COALESCE(k.is_stopword, false) = false
		  AND COALESCE(k.is_machine_code, false) = false
		GROUP BY o.name, p.name
		HAVING count(DISTINCT k.keyword) <= $2`,
		projectID, valueDomainCap)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var od, prop string
		var vals pq.StringArray
		if rows.Scan(&od, &prop, &vals) != nil {
			continue
		}
		dom := []string(vals)
		// Cap inline display so a ~40-value domain doesn't bloat the prompt.
		if len(dom) > 15 {
			dom = append(dom[:15:15], "…")
		}
		out[od+"\x00"+prop] = dom
	}
	return out
}
