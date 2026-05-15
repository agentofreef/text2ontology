package handler

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/lakehouse2ontology/llmclient"
)

// embedAndSaveFactVector computes and stores the embedding for a learned fact.
// Text = summary + space-joined tags (the semantic surface of the fact).
//
// Kept in the monolith after Phase 2 A4c because handler_ol.go (the live Ol
// CRUD handler) embeds new / updated learned facts synchronously on insert.
// Agent-server has its own copy of this helper; the two are intentionally
// duplicated so the monolith's Ol CRUD path has no cross-service dependency.
func embedAndSaveFactVector(db *sql.DB, factID, summary, tagsJoined string) {
	text := strings.TrimSpace(summary)
	if tagsJoined != "" {
		kws := strings.ReplaceAll(tagsJoined, "|", " ")
		if text != "" {
			text += " " + kws
		} else {
			text = kws
		}
	}
	if text == "" {
		return
	}
	embeddings, err := llmclient.EmbedTexts(db, []string{text})
	if err != nil || len(embeddings) == 0 {
		return
	}
	vecStr := "["
	for i, v := range embeddings[0] {
		if i > 0 {
			vecStr += ","
		}
		vecStr += fmt.Sprintf("%f", v)
	}
	vecStr += "]"
	db.Exec(`UPDATE ont_learned_fact SET content_vector = $1::vector WHERE id = $2`, vecStr, factID)
}
