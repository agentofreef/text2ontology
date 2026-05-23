package handler

// reachability_judge.go — MissionAct 任务可达器 (reachability judge).
//
// Runs BEFORE the ReAct loop on every lakehouse question when
// USE_MISSION_ACT is on. ONE LLM call does both decomposition AND
// shape-aware coverage in one shot:
//
//   - Decompose the question into required elements (metric / dimension /
//     filter) plus the *shape* each one needs ("年范围", "单月前缀",
//     "等值", "枚举集合", …).
//   - For every dimension/filter, judge — based on the candidate Intent
//     parameter table (property + op + type + description) — whether some
//     Intent's parameter actually supports that shape, and name which.
//
// Pure name-match used to be enough but produced false positives: a
// "year range" filter happily matched a "starts-with YYYY-MM" parameter
// even though answering the question would need 13 separate calls. The
// LLM gets the full parameter schema so it can refuse those.
//
// Deterministic guard rails in buildVerdictFromLLMHints:
//   - covered_by names are sanitised against the real Intent set, so a
//     hallucinated name cannot prop up a feasible verdict.
//   - if the LLM says covered but cites no real intent, fall back to the
//     declarative name match; if even that fails, force uncovered.
//
// Gate:
//   - Feasible        → return "" (caller continues into ReAct loop).
//   - Infeasible      → return non-empty finalAnswer string (caller
//     streams it and returns; no ReAct loop runs).
//   - Judge skipped   → return "" (fail-open).
//
// All side-effects (recordReachability, mission status) are
// best-effort: they never fail the turn.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/mission"
	"github.com/lakehouse2ontology/services/agent-server/recall"
	"github.com/lib/pq"
)

// shapeCap is one row of the data-driven shape vocabulary (table
// lakehouse_shape_capability). It is loaded fresh on every gate
// invocation. The Name is treated as opaque by every Go file: nothing
// elsewhere in the codebase compares against a specific shape string.
// The gate only does Name-equality between what the LLM emits as the
// required shape and what an Intent parameter declares.
type shapeCap struct {
	Name        string
	Description string
	Examples    []string
	// Satisfies is the subsumption list: a parameter declaring this shape
	// can also serve a requirement the LLM classified as any name here
	// (plus this shape itself). Strictly broader→narrower; see
	// docs/schema/schema.sql > lakehouse_shape_capability.
	Satisfies []string
}

// loadShapeVocab reads the registered shape vocabulary from the database.
// Any error or an unpopulated table returns nil, in which case the gate
// degrades to its prior LLM-only judgement (no deterministic shape
// match). Safe default: a missing / empty vocab MUST NEVER make the gate
// stricter than the legacy path.
func loadShapeVocab(ctx context.Context, db *sql.DB) []shapeCap {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT name, COALESCE(description, ''), COALESCE(examples, '{}'::text[]),
		       COALESCE(satisfies, '{}'::text[])
		FROM lakehouse_shape_capability
		ORDER BY name`)
	if err != nil {
		log.Printf("MISSION-ACT: shape vocab load skipped (fail-open): %v", err)
		return nil
	}
	defer rows.Close()
	var out []shapeCap
	for rows.Next() {
		var sc shapeCap
		var ex, sat pq.StringArray
		if err := rows.Scan(&sc.Name, &sc.Description, &ex, &sat); err != nil {
			log.Printf("MISSION-ACT: shape vocab scan: %v", err)
			continue
		}
		sc.Examples = []string(ex)
		sc.Satisfies = []string(sat)
		out = append(out, sc)
	}
	return out
}

// tokenRole is one token's recall classification — the ground truth the judge
// builds on instead of re-parsing the raw question.
type tokenRole struct {
	Token   string
	Role    string   // "指标" | "取值" | "列"
	Field   string   // "Table.Field" for 取值/列 (empty for 指标)
	Intents []string // matched intent names for 指标
}

// summarizeRecallResolution derives, from the recall result, what the project's
// tokenizer + recall pipeline already resolved each token to. This grounds the
// reachability judge in the system's single entry point (tokenize → recall)
// rather than letting the decompose LLM re-segment the question. Returns the
// per-token roles (for the decompose prompt) and a normalized set of every
// token recall matched to anything (metric / value / column) — used by the
// deterministic coverage guard so a recalled value like "Legion7 15ASH11" is
// never refused as "not found", even if the LLM re-split it into fragments.
func summarizeRecallResolution(rr recall.RecallResult) ([]tokenRole, map[string]bool) {
	resolved := map[string]bool{}
	roleByTok := map[string]tokenRole{}
	var order []string
	remember := func(tok string, r tokenRole) {
		if _, ok := roleByTok[tok]; !ok {
			order = append(order, tok)
		}
		roleByTok[tok] = r
		if n := normForDomain(tok); n != "" {
			resolved[n] = true
		}
	}
	// Metric tokens take precedence (a metric trigger word is a metric, not a
	// filter — this is what stops "early" being pulled out of "early order").
	metricNames := map[string][]string{}
	for _, mi := range rr.MetricIntents {
		for _, t := range mi.MatchedTokens {
			metricNames[t] = append(metricNames[t], mi.Name)
		}
	}
	for tok, names := range metricNames {
		remember(tok, tokenRole{Token: tok, Role: "指标", Intents: names})
	}
	// Value / column tokens from per-token keyword hits.
	for tok, hits := range rr.TokenDetails {
		if _, ok := roleByTok[tok]; ok {
			continue // already classified as a metric token
		}
		var valField, colField string
		for _, h := range hits {
			f := strings.Trim(strings.TrimSpace(h.MappedTable)+"."+strings.TrimSpace(h.MappedField), ".")
			if h.IsColumnRef {
				if colField == "" {
					colField = f
				}
			} else if valField == "" {
				valField = f
			}
		}
		switch {
		case valField != "":
			remember(tok, tokenRole{Token: tok, Role: "取值", Field: valField})
		case colField != "":
			remember(tok, tokenRole{Token: tok, Role: "列", Field: colField})
		}
	}
	sort.Strings(order)
	roles := make([]tokenRole, 0, len(order))
	for _, t := range order {
		roles = append(roles, roleByTok[t])
	}
	return roles, resolved
}

// recallResolves reports whether the tokenizer + recall pipeline already
// resolved this requirement's value/name to a real ontology token (metric /
// value / column). Containment (either direction, on the space/underscore-
// insensitive form) absorbs the case where the decompose LLM re-split a
// multi-word token into fragments — each fragment still matches the whole
// resolved token, so the requirement is correctly treated as covered.
func recallResolves(h llmRequirementHint, resolved map[string]bool) bool {
	for _, raw := range []string{h.Value, h.Name} {
		n := normForDomain(raw)
		if len(n) < 2 {
			continue
		}
		for r := range resolved {
			if r == n || strings.Contains(r, n) || strings.Contains(n, r) {
				return true
			}
		}
	}
	return false
}

// llmRequirementHint is what the LLM returns per decomposed item — both the
// decomposition itself and its shape-aware coverage verdict against the
// candidate Intent parameter table. The verdict half (Covered / CoveredBy /
// UncoveredReason) is the LLM's job because shape matching ("year range" vs
// "single-month YYYY-MM prefix") is semantic, not declarative.
type llmRequirementHint struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Value           string   `json:"value"` // filter value in the user's original words (e.g. "TBD", "X11")
	Shape           string   `json:"shape"`
	Why             string   `json:"why"`
	Covered         *bool    `json:"covered"`
	CoveredBy       []string `json:"covered_by"`
	UncoveredReason string   `json:"uncovered_reason"`
}

// decomposeResult is the full decompose-LLM output: the distinct
// sub-questions the user asked (so a multi-part question becomes a task
// completion list) plus the per-element coverage hints.
type decomposeResult struct {
	SubQuestions []string
	Requirements []llmRequirementHint
}

// requiredDimsFromRecall resolves the question's required GROUP-BY dimensions to
// OD property refs, grounded in recall: a decompose requirement of kind
// "dimension" counts only when recall matched its token to an actual COLUMN
// (IsColumnRef=true), which yields the real property to group by. Returns deduped
// "ODName.PropName" refs.
func requiredDimsFromRecall(hints []llmRequirementHint, rr recall.RecallResult) []string {
	// Index every recalled COLUMN reference: a property is a group-by candidate
	// only when at least one of its keyword hits is a column ref (IsColumnRef).
	// Map several normalized surface forms (prop name, display name, each
	// column-ref matched token) → the canonical "OD.Prop" group-by ref.
	colIndex := map[string]string{}
	for _, ob := range rr.OdBlocks {
		for _, mp := range ob.MatchedProps {
			hasCol := false
			for _, kw := range mp.Keywords {
				if kw.IsColumnRef {
					hasCol = true
					break
				}
			}
			if !hasCol {
				continue
			}
			ref := ob.Name + "." + mp.Name
			for _, surface := range []string{mp.Name, mp.DisplayName} {
				if n := normForDomain(surface); n != "" {
					if _, ok := colIndex[n]; !ok {
						colIndex[n] = ref
					}
				}
			}
			for _, kw := range mp.Keywords {
				if !kw.IsColumnRef {
					continue
				}
				if n := normForDomain(kw.MatchedToken); n != "" {
					if _, ok := colIndex[n]; !ok {
						colIndex[n] = ref
					}
				}
			}
		}
	}
	if len(colIndex) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	// lookup tries exact match then containment (either direction) on the
	// normalized form against the recalled column index.
	lookup := func(n string) (string, bool) {
		if n == "" {
			return "", false
		}
		if ref, ok := colIndex[n]; ok {
			return ref, true
		}
		for k, ref := range colIndex {
			if k == n || strings.Contains(k, n) || strings.Contains(n, k) {
				return ref, true
			}
		}
		return "", false
	}

	for _, h := range hints {
		if h.Kind != "dimension" {
			continue
		}
		if ref, ok := lookup(normForDomain(h.Name)); ok {
			add(ref)
			continue
		}
		if ref, ok := lookup(normForDomain(h.Value)); ok {
			add(ref)
		}
	}
	sort.Strings(out)
	return out
}

// runReachabilityJudge is the single hook call made by
// handleAgentStreamLakehouse. It returns the machine-templated infeasibility
// answer (non-empty) when the gate fires, or "" to proceed normally, plus the
// required GROUP-BY dimensions resolved from recall (used by the caller to
// force the executed query's groupBy to include them).
//
// db and intents are passed in rather than derived here so the function does
// not depend on any handler-level state beyond what the call site already has.
func runReachabilityJudge(
	ctx context.Context,
	db *sql.DB,
	projectID string,
	sm *shadowMission,
	question string,
	rr recall.RecallResult,
) (string, []string) {
	if !missionActEnabled {
		return "", nil
	}

	// Ground the judge in the project's tokenizer + recall (the system's single
	// entry point). The recalled Intents are the metric set; the per-token recall
	// resolution (roles + resolvedTokens) stops the decompose LLM from
	// re-segmenting the question and refusing on fragments recall already matched.
	intents := rr.MetricIntents
	roles, resolvedTokens := summarizeRecallResolution(rr)

	// Load the data-driven shape vocabulary once per gate invocation. Empty
	// or error → vocab is nil → all downstream code degrades to legacy
	// behaviour (LLM-only shape judgement, no deterministic match check).
	vocab := loadShapeVocab(ctx, db)

	// ── LLM call: decomposition + shape-aware coverage in one pass ───────────
	res, err := decomposeQuestion(ctx, db, question, intents, vocab, roles)
	if err != nil {
		log.Printf("MISSION-ACT: reachability decompose skipped (fail-open): %v", err)
		return "", nil
	}
	if len(res.Requirements) == 0 {
		return "", nil
	}

	// Resolve the required GROUP-BY dimensions from recall (grounded in the
	// decompose hints). Surfaced to the caller so the executed query's groupBy
	// can be forced to include them — this is the completeness contract.
	reqDims := requiredDimsFromRecall(res.Requirements, rr)

	// ── Build verdict from LLM hints; deterministic guard rails ──────────────
	verdict := buildVerdictFromLLMHints(ctx, db, projectID, res.Requirements, intents, vocab, resolvedTokens)

	// ── Persist (best-effort) ────────────────────────────────────────────────
	sm.recordReachability(ctx, verdict)

	// ── Seed the task completion list for multi-part questions ───────────────
	// Only when the question is feasible AND has 2+ distinct sub-questions —
	// a single atomic question needs no checklist. reconcileMissionTasks (run
	// in the turn-end defer) flips each task passing/blocked against the
	// final answer.
	if verdict.Feasible && len(res.SubQuestions) >= 2 {
		sm.seedTasksFromSubQuestions(ctx, res.SubQuestions)
	}

	if verdict.Feasible {
		return "", reqDims
	}
	if verdict.Kind == "clarify" {
		// Ambiguous filter value(s) — answerable once the user disambiguates.
		// Ask rather than refuse outright.
		return "〔需要你先澄清一下〕\n\n" + verdict.Reason, reqDims
	}
	return buildInfeasibilityAnswer(verdict), reqDims
}

// buildVerdictFromLLMHints turns the LLM's per-item judgement into a
// ReachabilityVerdict. The LLM owns the shape-aware coverage decision; this
// function decides feasibility with a metric-only gate plus deterministic
// guards:
//
//   - covered_by names are sanitised against the real Intent name set, so a
//     hallucinated intent name cannot prop up a coverage claim.
//   - feasibility is refused ONLY by (a) a hole-2 trap — a cited real Intent
//     whose parameter shape cannot serve the requirement — or (b) the
//     metric-only gate: recall surfaced no Intent at all. A dimension/filter
//     with no declared parameter is shown as uncovered for transparency but
//     does NOT gate: the ReAct loop resolves dimensions/filters via OD
//     property recall + SmartQuery (groupBy/filter + ont_link joins), so
//     refusing on their absence only produced false negatives.
func buildVerdictFromLLMHints(ctx context.Context, db *sql.DB, projectID string, hints []llmRequirementHint, intents []recall.MetricIntent, vocab []shapeCap, resolvedTokens map[string]bool) mission.ReachabilityVerdict {
	realIntentNames := map[string]bool{}
	for _, mi := range intents {
		realIntentNames[mi.Name] = true
	}
	vocabKnown := map[string]bool{}
	satisfies := map[string][]string{}
	for _, sc := range vocab {
		vocabKnown[sc.Name] = true
		satisfies[sc.Name] = sc.Satisfies
	}
	specs := buildIntentSpecs(intents) // for fallback declarative match

	v := mission.ReachabilityVerdict{Feasible: true}

	// blockers collects the ONLY conditions that make a turn infeasible:
	//   (1) a hole-2 trap — a cited real Intent declares a parameter whose
	//       shape cannot serve the requirement (the loop would misuse it); and
	//   (2) the metric-only gate below — recall surfaced no Intent at all.
	// A dimension/filter that simply has no declared parameter is NOT a
	// blocker: dimensions/filters are resolved downstream by the ReAct loop
	// (OD property recall + SmartQuery groupBy/filter + ont_link joins), not by
	// intent-parameter coverage. Gating on their absence produced false
	// negatives — e.g. a recalled metric whose year / product filter wasn't
	// declared as a parameter was wrongly refused.
	var blockers []string
	// clarifyMsgs collects filter values that are ambiguous across several
	// low-cardinality fields — answerable once the user picks one, so they
	// yield a "clarify" verdict rather than an outright refusal.
	var clarifyMsgs []string

	for _, h := range hints {
		rc := mission.RequirementCoverage{
			Dimension: h.Name,
			Kind:      h.Kind,
			Shape:     h.Shape,
			Why:       h.Why,
		}

		// metric / unknown kinds never gate feasibility — they're query targets.
		if h.Kind != "dimension" && h.Kind != "filter" {
			rc.Covered = true
			v.Requirements = append(v.Requirements, rc)
			continue
		}

		// Recall-grounding guard: if the tokenizer + recall pipeline already
		// resolved this filter's value (or this dimension's column) to a real
		// ontology token, it is covered by construction — the value EXISTS. This
		// overrides the LLM, which may have re-split a multi-word token into
		// fragments that no longer match a stored value (the "YGPro7 15ASH11" →
		// "YGPro7" + "15ASH11" bug). Only tokens recall did NOT resolve fall
		// through to the shape / value-domain gates below, where a genuine gap
		// (e.g. "TBD") can still be raised.
		if recallResolves(h, resolvedTokens) {
			rc.Covered = true
			rc.CoveredBy = []string{"召回已解析"}
			v.Requirements = append(v.Requirements, rc)
			continue
		}

		// Sanitise covered_by against real intent names.
		var realCoveredBy []string
		for _, n := range h.CoveredBy {
			if realIntentNames[n] {
				realCoveredBy = append(realCoveredBy, n)
			}
		}

		llmSaysCovered := h.Covered != nil && *h.Covered
		switch {
		case llmSaysCovered && len(realCoveredBy) > 0:
			// Hole-2 trap (the SOLE dimension/filter blocker): when shape is a
			// registered vocab name, the cited Intents must ALSO declare a
			// parameter with the same shapeCapability. A real intent name with
			// a wrong-shape parameter is a concrete mis-fit the loop would
			// misuse — refuse it. Skipped (legacy) when shape is empty / not in
			// the vocab, so an unpopulated vocab never gates.
			if vocabKnown[h.Shape] && !anyCitedIntentServesShape(realCoveredBy, h.Shape, intents, satisfies) {
				rc.MissingNote = fmt.Sprintf(
					"已授权指标 (%s) 真实，但没有任何参数声明可服务「%s」形态的能力",
					strings.Join(realCoveredBy, "、"), h.Shape)
				blockers = append(blockers, fmt.Sprintf("「%s」需要 %s 形态，但已召回指标的参数无法服务", h.Name, h.Shape))
				v.Requirements = append(v.Requirements, rc)
				continue
			}
			rc.Covered = true
			rc.CoveredBy = realCoveredBy
		case llmSaysCovered && len(realCoveredBy) == 0:
			// LLM said covered but cited no real intent — fall back to
			// declarative name match. If that also fails, mark uncovered for
			// display but DO NOT gate: the loop resolves it downstream.
			fallback := mission.CoveringIntents(mission.DecompItem{Name: h.Name, Kind: h.Kind}, specs)
			if len(fallback) > 0 {
				rc.Covered = true
				rc.CoveredBy = fallback
			} else {
				rc.MissingNote = "无已声明参数显式覆盖；将在执行阶段按 OD 属性 / SmartQuery 解析"
			}
		default: // LLM says uncovered (or didn't decide) — display only, no gate.
			if h.UncoveredReason != "" {
				rc.MissingNote = h.UncoveredReason
			} else {
				rc.MissingNote = fmt.Sprintf("无已授权指标以「%s」形态显式覆盖「%s」（将在执行阶段解析）", h.Shape, h.Name)
			}
		}

		// Value-domain gate (Stage B): resolve a filter's categorical value
		// against the project's value vocabulary. Absent everywhere → genuine
		// gap (refuse, so the loop can't fabricate a mapping). Present in ≥2
		// low-card fields → ambiguous (ask which). Exactly one / high-card →
		// proceed (the ReAct loop composes it). Numeric/date values are skipped.
		if h.Kind == "filter" && strings.TrimSpace(h.Value) != "" && !looksNumericOrDate(h.Value) {
			exists, lowCard := resolveFilterValue(ctx, db, projectID, h.Value)
			switch {
			case !exists:
				rc.Covered = false
				rc.MissingNote = fmt.Sprintf("筛选值「%s」在本体里找不到对应取值", h.Value)
				blockers = append(blockers, fmt.Sprintf("数据里没有任何字段的取值是「%s」（用户提到的「%s」在本体中不存在）", h.Value, h.Name))
			case len(lowCard) >= 2:
				rc.Covered = false
				rc.MissingNote = fmt.Sprintf("值「%s」同时是 %s 的取值，需澄清按哪个字段筛", h.Value, strings.Join(lowCard, "、"))
				clarifyMsgs = append(clarifyMsgs, fmt.Sprintf("「%s」在多个字段里都出现（%s）——你想按哪个筛？", h.Value, strings.Join(lowCard, " / ")))
			}
		}

		v.Requirements = append(v.Requirements, rc)
	}

	// Metric-only gate: refuse the turn only when recall surfaced NO Intent at
	// all — there is nothing to measure. (See blockers comment above.)
	if len(intents) == 0 {
		blockers = append(blockers, "未召回到任何可用于度量的指标 —— 当前本体没有与该指标相关的已授权口径")
	}

	v.Feasible = len(blockers) == 0 && len(clarifyMsgs) == 0
	switch {
	case len(blockers) > 0:
		// gap / hole-2 / metric-absent → cannot answer (gap takes precedence
		// over clarification: a missing field/value can't be fixed by asking).
		v.Kind = "gap"
		v.Reason = buildVerdictReason(v, blockers)
	case len(clarifyMsgs) > 0:
		// only ambiguity → answerable once the user disambiguates.
		v.Kind = "clarify"
		v.Reason = "需要澄清：" + strings.Join(clarifyMsgs, "；") + "。"
	default:
		v.Reason = buildVerdictReason(v, nil)
	}
	return v
}

// anyCitedIntentServesShape reports whether at least one of the cited
// Intents has a parameter that can serve the required shape — either by
// declaring it exactly, or by declaring a broader shape that subsumes it
// (declaredShape ∈ satisfies-closure). Used by the hole-2 guard in
// buildVerdictFromLLMHints.
//
// The subsumption tolerance is what prevents false refusals when the LLM
// classifies a requirement with a narrower-but-compatible label than the
// parameter's declaration (e.g. param declares a range shape, LLM labels
// the requirement as the point shape the range subsumes). Empty required
// shape returns false — by then the gate has already decided to skip the
// deterministic check for that requirement.
func anyCitedIntentServesShape(cited []string, required string, intents []recall.MetricIntent, satisfies map[string][]string) bool {
	if required == "" {
		return false
	}
	citedSet := map[string]bool{}
	for _, n := range cited {
		citedSet[n] = true
	}
	for _, mi := range intents {
		if !citedSet[mi.Name] {
			continue
		}
		for _, p := range mi.Parameters {
			declared := p.ShapeCapability
			if declared == "" {
				continue
			}
			if declared == required {
				return true
			}
			for _, sat := range satisfies[declared] {
				if sat == required {
					return true
				}
			}
		}
	}
	return false
}

// buildVerdictReason composes the human-readable verdict explanation from the
// finalised requirements list. Feasible: name the dimensions covered.
// Infeasible: name the uncovered ones AND surface the LLM's shape reason
// (since the failure is usually about shape mismatch, not absence).
func buildVerdictReason(v mission.ReachabilityVerdict, blockers []string) string {
	if !v.Feasible {
		if len(blockers) == 0 {
			return "不可行。"
		}
		return "不可行：" + strings.Join(blockers, "；") + "。"
	}
	// Feasible: name the covered dimensions and flag any that will be resolved
	// at execution time (no declared parameter, but the ReAct loop handles them
	// via OD property recall + SmartQuery).
	var covered, willResolve []string
	for _, r := range v.Requirements {
		if r.Kind != "dimension" && r.Kind != "filter" {
			continue
		}
		if r.Covered {
			covered = append(covered, r.Dimension)
		} else {
			willResolve = append(willResolve, r.Dimension)
		}
	}
	if len(covered) == 0 && len(willResolve) == 0 {
		return "可行：问题不涉及需要授权的筛选维度。"
	}
	var sb strings.Builder
	sb.WriteString("可行")
	if len(covered) > 0 {
		sb.WriteString(fmt.Sprintf("：维度（%s）已被已授权指标覆盖", strings.Join(collectNames(covered), "、")))
	} else {
		sb.WriteString("：已召回相关指标")
	}
	if len(willResolve) > 0 {
		sb.WriteString(fmt.Sprintf("；筛选/维度（%s）将在执行阶段按 OD 属性 / SmartQuery 解析", strings.Join(collectNames(willResolve), "、")))
	}
	sb.WriteString("。")
	return sb.String()
}

// collectNames dedupes a string slice while preserving order.
func collectNames(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// decomposeQuestion calls the agent LLM once to do both jobs: identify the
// distinct sub-questions and break the question into shape-aware coverage
// hints. Returns a zero-value result + err on any failure (fail-open).
//
// Prompt contract: returns ONLY a JSON object:
//
//	{"sub_questions":["..."],"requirements":[{"kind":"...","name":"...",...}]}
//
// For dimension/filter requirements, name MUST use the candidate Intent
// parameter property names — that is what the coverage match keys on.
func decomposeQuestion(
	ctx context.Context,
	db *sql.DB,
	question string,
	intents []recall.MetricIntent,
	vocab []shapeCap,
	roles []tokenRole,
) (decomposeResult, error) {
	var zero decomposeResult
	baseURL, apiKey, modelName, _, _, vendor := llmclient.GetConfigForRole(db, "agent")
	if baseURL == "" {
		return zero, fmt.Errorf("no agent LLM config available")
	}

	systemPrompt := `你是一个数据可达性裁判。给定用户问题与可用的指标参数表，做三件事：

第一件：把用户这一句里包含的「独立子问题」分出来。一句话里可能并列了多个问题（例如"上海的总营收是多少？外卖占比是多少？"是两个子问题）。每个子问题用一句自然语言描述。如果只有一个问题，就只返回一个。
第二件：把问题拆解为它所需的数据要素（metric / dimension / filter）。
第三件：对每个 dimension / filter 要素，**严格基于参数表的 op / type / 描述** 判断有哪个指标真正支持这个要素所要求的"形态"（范围 / 单月前缀 / 等值 ...）。这件事是关键，仅靠 property 名字相同就判可达是错的。

**铁律**：分词与识别一律以下方「召回已解析」小节为准，**禁止你自己重新分词或拆开多词 token**。凡命中 指标/取值/列 的 token 一律 covered=true（本体中确实存在）；只有召回完全没命中、而用户又明显想用作筛选/分组的词，才允许 covered=false。

只输出一个 JSON 对象，不要包裹 markdown 代码块，不要输出任何其他文字：
{"sub_questions":["子问题1","子问题2"],"requirements":[{"kind":"metric|dimension|filter","name":"...","value":"...","shape":"...","why":"...","covered":true|false,"covered_by":["指标a"],"uncovered_reason":"..."}]}

sub_questions 规则：
- 每个元素是一句完整的自然语言子问题，尽量保留用户原话里的关键词。
- 并列的问题要分开；递进/修饰同一目标的不要硬拆。
- 最多 6 个。

requirements 字段规则：
- kind="metric" 表示用户想查询的指标（如销售额、数量）。
- kind="dimension" 表示用户想按其分组的维度。
- kind="filter" 表示用户想按其筛选的条件。
- name：对于 dimension/filter，必须使用下方参数表中真实出现的参数 property 名称；如果整张参数表里没有匹配的 property，用问题中的原始词，并把这一项标为 covered=false。
- value（仅 filter）：该筛选要匹配的具体值，**用用户原话**（如 "TBD" / "X11" / "Beverages"）。**不要**把它替换成你猜测的字段名或你以为对应的合法值——原样照抄；服务端会拿它去本体值域里核对。没有具体筛选值就留空。
- shape：一两个词描述该要素的形态。
  - metric 用"标量/求和/计数"等。
  - dimension 用"分组"。
  - filter 用具体形态："等值"/"单值"/"前缀匹配"/"区间"/"年范围"/"月范围"/"日期范围"/"枚举集合" 等。**形态必须精确**，不要笼统写"范围"。
- why：一句话说明问题为什么需要这个要素。
- covered（仅 dimension/filter 需要；metric 可省略，等同 true）：
  - true 仅当存在某个指标的参数同时满足：(a) property 与 name 一致；(b) op/type/描述 表明它能支撑这个 shape。
  - 例如 shape="年范围" 但参数 op="starts with" 且 描述="YYYY-MM" → 单月前缀，无法直接覆盖年范围 → covered=false。
  - 例如 shape="等值" 且参数 op="=" → 覆盖。
  - 例如 shape="枚举集合" 且参数 type="enum_ref" 且 op="=" → 单次只能选一个枚举值 → 仍是 covered=false（除非问题就是单值）。
- covered_by：covered=true 时，列出真正满足形态的指标名（必须出现在参数表里）。
- uncovered_reason：covered=false 时，一句话说明为什么形态对不上（例如"参数仅支持 YYYY-MM 单月前缀，不直接支持 2024-2025 年范围"）。
- 如果问题只是通用查询（无特定筛选），requirements 只返回 metric 项。
- requirements 元素不超过 8 个。`

	userPrompt := buildDecomposeUserPrompt(question, intents, vocab, roles)

	llmMessages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	content, _, err := llmclient.DoChatWithUsage(baseURL, apiKey, map[string]interface{}{
		"model":       modelName,
		"messages":    llmMessages,
		"max_tokens":  1600,
		"temperature": 0.1,
		"_vendor":     vendor,
	})
	if err != nil {
		return zero, fmt.Errorf("decompose LLM call failed: %w", err)
	}

	return parseDecompJSON(llmclient.StripThinkTags(content))
}

// buildDecomposeUserPrompt constructs the user-turn prompt. For every Intent
// it lists each parameter's full schema (property, op, type, description) so
// the LLM can shape-match — name match alone is not enough to judge whether
// a question's "year range" filter can really be served by a "starts-with
// YYYY-MM" parameter.
func buildDecomposeUserPrompt(question string, intents []recall.MetricIntent, vocab []shapeCap, roles []tokenRole) string {
	var sb strings.Builder
	sb.WriteString("## 用户问题\n")
	sb.WriteString(question)
	if len(roles) > 0 {
		sb.WriteString("\n\n## 召回已解析（分词器+召回的权威结果 —— 必须以此为准，禁止重新分词）\n")
		sb.WriteString("下列 token 由系统分词器切分、召回引擎解析。**严禁把多词 token 拆开**，也不要把指标里的词单独抽出来当筛选：\n")
		for _, r := range roles {
			switch r.Role {
			case "指标":
				sb.WriteString(fmt.Sprintf("- 「%s」→ 指标（命中：%s）\n", r.Token, strings.Join(r.Intents, "、")))
			case "取值":
				sb.WriteString(fmt.Sprintf("- 「%s」→ 取值（字段 %s）\n", r.Token, r.Field))
			case "列":
				sb.WriteString(fmt.Sprintf("- 「%s」→ 列/维度（字段 %s）\n", r.Token, r.Field))
			}
		}
		sb.WriteString("角色为 指标/取值/列 的 token 一律 covered=true —— 它们在本体中确实存在。只有用户明显想筛/分组、却在上面**完全没出现**的词，才标 covered=false。\n")
	}
	sb.WriteString("\n\n## 可用指标参数表（按形态判断可达性时必须以此为准）\n")
	if len(intents) == 0 {
		sb.WriteString("（无已召回指标）\n")
	} else {
		for _, mi := range intents {
			sb.WriteString(fmt.Sprintf("### 指标: %s\n", mi.Name))
			if len(mi.Parameters) == 0 {
				sb.WriteString("（无参数）\n")
				continue
			}
			for _, p := range mi.Parameters {
				prop := p.Property
				if prop == "" {
					prop = p.Name
				}
				sb.WriteString(fmt.Sprintf("  - property=%q, name=%q, type=%q, op=%q",
					prop, p.Name, p.Type, p.Op))
				if p.Description != "" {
					sb.WriteString(fmt.Sprintf(", desc=%q", p.Description))
				}
				if p.ShapeCapability != "" {
					sb.WriteString(fmt.Sprintf(", shape_capability=%q", p.ShapeCapability))
				}
				if len(p.AllowedValues) > 0 {
					// Cap allowed-values length so very large enums don't blow the prompt.
					vs := p.AllowedValues
					if len(vs) > 8 {
						vs = append(append([]string{}, vs[:8]...), "…")
					}
					sb.WriteString(fmt.Sprintf(", allowed=[%s]", strings.Join(vs, ",")))
				}
				sb.WriteString("\n")
			}
		}
	}
	if len(vocab) > 0 {
		sb.WriteString("\n## 形态词表（封闭词表 — shape 字段必须从下面挑一个 name；都不匹配就留空字符串）\n")
		for _, sc := range vocab {
			sb.WriteString(fmt.Sprintf("  - %s — %s", sc.Name, sc.Description))
			if len(sc.Examples) > 0 {
				sb.WriteString(fmt.Sprintf("（例：%s）", strings.Join(sc.Examples, "; ")))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n仅当某指标的某个参数声明了 shape_capability 等于该要素 shape 时，才算形态对得上；否则即便 property 名字一致，也要把 covered 置为 false。\n")
	}
	sb.WriteString("\n请输出 JSON 对象。")
	return sb.String()
}

// reqItemJSON is one requirement entry as the LLM emits it. `covered` is
// *bool so we can distinguish "LLM didn't decide" from "explicit false".
type reqItemJSON struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Value           string   `json:"value"`
	Shape           string   `json:"shape"`
	Why             string   `json:"why"`
	Covered         *bool    `json:"covered"`
	CoveredBy       []string `json:"covered_by"`
	UncoveredReason string   `json:"uncovered_reason"`
}

// parseDecompJSON parses the LLM reply into a decomposeResult. The contract is
// an object {sub_questions, requirements}; for resilience it also accepts a
// bare requirements array (older shape) and treats sub_questions as empty.
func parseDecompJSON(raw string) (decomposeResult, error) {
	cleaned := llmclient.ExtractJSON(raw)
	if cleaned == "" {
		cleaned = strings.TrimSpace(raw)
	}

	// Preferred shape: object.
	var obj struct {
		SubQuestions []string      `json:"sub_questions"`
		Requirements []reqItemJSON `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(cleaned), &obj); err == nil && (len(obj.Requirements) > 0 || len(obj.SubQuestions) > 0) {
		return decomposeResult{
			SubQuestions: cleanSubQuestions(obj.SubQuestions),
			Requirements: normalizeReqItems(obj.Requirements),
		}, nil
	}

	// Fallback shape: bare array of requirements.
	var arr []reqItemJSON
	if err := json.Unmarshal([]byte(cleaned), &arr); err != nil {
		return decomposeResult{}, fmt.Errorf("decompose JSON parse failed (%q): %w", truncateStr(cleaned, 200), err)
	}
	return decomposeResult{Requirements: normalizeReqItems(arr)}, nil
}

// normalizeReqItems maps raw LLM items to llmRequirementHint, defaulting an
// unknown kind to "metric" and dropping nameless entries.
func normalizeReqItems(items []reqItemJSON) []llmRequirementHint {
	out := make([]llmRequirementHint, 0, len(items))
	for _, it := range items {
		kind := strings.ToLower(strings.TrimSpace(it.Kind))
		switch kind {
		case "metric", "dimension", "filter":
		default:
			kind = "metric"
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		out = append(out, llmRequirementHint{
			Kind:            kind,
			Name:            name,
			Value:           strings.TrimSpace(it.Value),
			Shape:           strings.TrimSpace(it.Shape),
			Why:             strings.TrimSpace(it.Why),
			Covered:         it.Covered,
			CoveredBy:       it.CoveredBy,
			UncoveredReason: strings.TrimSpace(it.UncoveredReason),
		})
	}
	return out
}

// cleanSubQuestions trims, drops empties, and dedupes the sub-question list.
func cleanSubQuestions(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// buildInfeasibilityAnswer assembles the machine-templated stop answer.
// NOT LLM-generated — text is built in Go so it cannot be softened.
func buildInfeasibilityAnswer(verdict mission.ReachabilityVerdict) string {
	var sb strings.Builder
	sb.WriteString("〔可达性判定：这个问题现在无法整体回答〕\n\n")
	sb.WriteString(verdict.Reason)

	if subset := verdict.AnswerableSubset(); len(subset) > 0 {
		sb.WriteString("\n\n我能覆盖的维度：")
		sb.WriteString(strings.Join(subset, "、"))
		sb.WriteString(" —— 但你的问题需要整体回答，目前做不到。")
	}
	return sb.String()
}

// reconcileMissionTasks is the turn-end hook. Given the mission's
// sub-question task list and the final assistant answer, it asks the LLM
// once which sub-questions the answer actually addressed, then records the
// per-task passing/blocked verdict. Fail-open and best-effort: any LLM /
// parse error is logged and leaves the tasks as-is. No-op when the shadow
// path is inert or there are no sub-question tasks.
func reconcileMissionTasks(ctx context.Context, db *sql.DB, sm *shadowMission, finalAnswer string) {
	if !missionActEnabled || sm == nil || sm.m == nil || len(sm.m.Tasks) == 0 {
		return
	}
	if strings.TrimSpace(finalAnswer) == "" {
		return
	}

	baseURL, apiKey, modelName, _, _, vendor := llmclient.GetConfigForRole(db, "agent")
	if baseURL == "" {
		return
	}

	var sb strings.Builder
	sb.WriteString("## 子问题清单（id → 子问题）\n")
	for _, t := range sm.m.Tasks {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.ID, t.Behavior))
	}
	sb.WriteString("\n## 系统给出的最终回答\n")
	sb.WriteString(finalAnswer)
	sb.WriteString("\n\n请判断每个子问题是否在最终回答里得到了实际回答。只输出 JSON 对象，键是子问题 id，值是 true/false：\n{\"q1\":true,\"q2\":false}")

	systemPrompt := `你是一个核对助手。给定一组子问题与系统的最终回答，判断每个子问题是否被实际回答了（有具体数值/结论才算 true；只是提到、回避、或说做不到算 false）。只输出 JSON 对象，不要包裹 markdown，不要多余文字。`

	content, _, err := llmclient.DoChatWithUsage(baseURL, apiKey, map[string]interface{}{
		"model":       modelName,
		"messages":    []map[string]interface{}{{"role": "system", "content": systemPrompt}, {"role": "user", "content": sb.String()}},
		"max_tokens":  300,
		"temperature": 0.1,
		"_vendor":     vendor,
	})
	if err != nil {
		log.Printf("MISSION-ACT: reconcile skipped (fail-open): %v", err)
		return
	}

	cleaned := llmclient.ExtractJSON(llmclient.StripThinkTags(content))
	if cleaned == "" {
		cleaned = strings.TrimSpace(content)
	}
	var results map[string]bool
	if err := json.Unmarshal([]byte(cleaned), &results); err != nil {
		log.Printf("MISSION-ACT: reconcile parse failed (%q): %v", truncateStr(cleaned, 200), err)
		return
	}
	sm.applyTaskReconciliation(ctx, results)
}

// truncateStr truncates s to max bytes for log messages.
func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// looksNumericOrDate reports whether s is a number / date / range token rather
// than a categorical term. Such values are served by range/equality on the raw
// column and are NOT expected in the value-keyword vocabulary, so the value-gap
// gate skips them (avoids false gaps on "2024", "2024/05", "2024-2025").
func looksNumericOrDate(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != ' ' && r != '/' && r != '.' && r != ':' && r != '-' {
			return false
		}
	}
	return true
}

// resolveFilterValue locates a filter VALUE in the project's value vocabulary
// (lakehouse_keyword) for the Stage B value-domain gate. Returns:
//   - existsAnywhere: the value matches a value-keyword on ANY property
//     (high- or low-cardinality), case/space/underscore-normalised.
//   - lowCardProps:   the LOW-cardinality categorical properties whose value
//     domain contains it, as "OD.PROP" labels (used to detect ambiguity).
//
// db == nil → (true, nil): inert, never gates without a database. Any query
// error also fails open (returns existsAnywhere=true) so the gate never refuses
// on infrastructure trouble.
func resolveFilterValue(ctx context.Context, db *sql.DB, projectID, value string) (existsAnywhere bool, lowCardProps []string) {
	v := strings.TrimSpace(value)
	if db == nil || projectID == "" || v == "" {
		return true, nil
	}
	// Low-cardinality categorical properties whose domain contains v.
	if rows, err := db.QueryContext(ctx, `
		SELECT o.name, p.name
		FROM ont_property p
		JOIN ont_object_type o ON o.id = p.object_type_id
		WHERE o.mark = true
		  AND p.id IN (
			SELECT k.property_id FROM lakehouse_keyword k
			WHERE k.project_id = $1 AND k.property_id IS NOT NULL
			  AND COALESCE(k.is_column_name, false) = false
			  AND COALESCE(k.is_stopword, false) = false
			  AND COALESCE(k.is_machine_code, false) = false
			  AND regexp_replace(lower(k.keyword), '[ _]', '', 'g') = regexp_replace(lower($2), '[ _]', '', 'g')
		  )
		  AND (
			SELECT count(DISTINCT k2.keyword) FROM lakehouse_keyword k2
			WHERE k2.property_id = p.id
			  AND COALESCE(k2.is_column_name, false) = false
			  AND COALESCE(k2.is_stopword, false) = false
			  AND COALESCE(k2.is_machine_code, false) = false
		  ) <= $3`,
		projectID, v, valueDomainCap); err == nil {
		for rows.Next() {
			var od, prop string
			if rows.Scan(&od, &prop) == nil {
				lowCardProps = append(lowCardProps, od+"."+prop)
			}
		}
		rows.Close()
	}
	if len(lowCardProps) > 0 {
		return true, lowCardProps
	}
	// Not in any low-card domain → does it exist anywhere (incl. high-card,
	// e.g. a specific MTM number / product name)? Machine codes included here.
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM lakehouse_keyword
		WHERE project_id = $1
		  AND COALESCE(is_column_name, false) = false
		  AND COALESCE(is_stopword, false) = false
		  AND regexp_replace(lower(keyword), '[ _]', '', 'g') = regexp_replace(lower($2), '[ _]', '', 'g')`,
		projectID, v).Scan(&n); err != nil {
		return true, nil // fail-open
	}
	return n > 0, nil
}
