# Voting Rules for Consensus Relationship Detection

This document defines how the lead_agent combines signals from three evidence sources to determine final relationship confidence and output status.

## Evidence Sources

1. **Structural Result** (`compute_overlap.py`): join_confidence, best_column_pairs
2. **Statistical Result** (`compute_statistics.py`): lineage_confidence, aggregation_patterns
3. **Semantic Result** (semantic_analyst LLM): join.confidence, lineage.confidence, semantic_overlap.confidence

## Overall Voting Rules

| # | Rule | Relationship | Condition | Action |
|---|------|--------------|-----------|--------|
| 1 | **JOIN CONFIRMED** | join | `structural.join_confidence > 0.7` | **Confirm** regardless of semantic signal |
| 2 | **JOIN CONFIRMED** | join | `structural.join_confidence 0.4–0.7 AND semantic.join.confidence > 0.6` | **Confirm** relationship |
| 3 | **JOIN NEEDS_REVIEW** | join | `structural.join_confidence < 0.4 AND semantic.join.confidence > 0.6` | Output with **NEEDS_USER_REVIEW** status |
| 4 | **JOIN SAFE_FAIL** | join | Final confidence < 0.4 | **Do not output** this relationship |
| 5 | **LINEAGE CONFIRMED** | lineage | `statistical.lineage_confidence > 0.6 AND semantic.lineage.confidence > 0.5` | **Confirm** relationship |
| 6 | **LINEAGE POSSIBLE** | lineage | Only one of statistical/semantic suggests lineage (other < 0.4) AND winner > 0.5 | Output with **POSSIBLE** status |
| 7 | **SEMANTIC OVERLAP CONFIRMED** | semantic_overlap | `semantic.semantic_overlap.confidence > 0.7` | **Confirm** relationship |
| 8 | **SAME NAME WARNING** | join | `col_a.name == col_b.name BUT overlap_ratio < 0.1` | Add warning: "Same column name but low value overlap, may be coincidence" |
| 9 | **OVERALL SAFE_FAIL** | any | Final confidence < 0.4 | **Do not include** in final output |

## Confidence Calculation by Relationship Type

### JOIN Confidence

```
final_confidence = max(
  structural.join_confidence,
  (semantic.join.confidence * 0.6 + structural.join_confidence * 0.4)
)
```

If both signals exist:
- Structural > 0.7 → use structural (Rule 1)
- Structural 0.4–0.7 → blend: 40% structural + 60% semantic (Rule 2)
- Structural < 0.4 → rely on semantic only, but flag as NEEDS_REVIEW (Rule 3)

### LINEAGE Confidence

```
final_confidence = (statistical.lineage_confidence * 0.5 + semantic.lineage.confidence * 0.5)
```

- If both > threshold → CONFIRMED (Rule 5)
- If only one > 0.5 → POSSIBLE (Rule 6)
- Otherwise → do not output

### SEMANTIC_OVERLAP Confidence

```
final_confidence = semantic.semantic_overlap.confidence
```

- If > 0.7 → CONFIRMED (Rule 7)
- If 0.4–0.7 → include with moderate confidence
- If < 0.4 → SAFE_FAIL, do not output (Rule 9)

## Output Statuses

- **CONFIRMED**: High confidence, all evidence points in same direction. Include in final output.
- **POSSIBLE**: Moderate confidence, supported by one strong signal. Include with status flag.
- **NEEDS_USER_REVIEW**: Semantic suggests relationship but structural/statistical weak. Include with review flag.
- **SAFE_FAIL**: Final confidence < 0.4 or contradictory evidence. **Do not include** in output.

## Tie-Breaking Rules

When multiple relationships are detected between the same pair of sheets:

1. Prioritize by confidence (highest first)
2. If confidences are within 0.1 of each other, order by relationship type: join > lineage > semantic_overlap
3. Include all relationships above 0.4 confidence threshold

## Special Cases

### Case 1: Column Name Match with Low Value Overlap (Rule 8)

If `col_a.name == col_b.name` but `overlap_ratio < 0.1` (from structural evidence):
- Still consider join possible, but add warning in output
- Lower final confidence by 0.1
- Flag in explanation: "Same column name but low value overlap, may be coincidence"

### Case 2: Multiple Valid Column Pairs

If structural evidence identifies 3+ valid column pairs:
- Include all pairs in output
- Sort by confidence (highest first)
- Note the ambiguity in evidence field: "Multiple join candidates detected; user may need to validate join keys"

### Case 3: Bidirectional Lineage

If both statistical and semantic suggest A→B and B→A:
- Output as "bidirectional" but lower confidence by 0.15
- Add note: "Potential circular dependency or mutual transformation; requires clarification"

### Case 4: Calculation Table Involvement

If either sheet is classified as "calculation_table" (from business_understanding):
- Do not output lineage with high confidence (cap at 0.6)
- Lineage only output if semantic evidence is strong (> 0.7)
- Add note: "One sheet is a calculation table; lineage relationship may be temporary"

## Output Filtering Summary

**Include in final output:**
- All relationships with final_confidence ≥ 0.4 AND exists=true
- All NEEDS_USER_REVIEW and POSSIBLE flagged relationships with confidence ≥ 0.4

**Do NOT include in final output:**
- Any relationship with final_confidence < 0.4
- Any relationship with exists=false

## Example: Complete Voting Scenario

**Input:**
- Structural: join_confidence = 0.65, column_pairs = [("id", "customer_id")]
- Statistical: lineage_confidence = 0.55, aggregation_patterns = [ratio: 1:12]
- Semantic: join.confidence = 0.8, lineage.confidence = 0.4, semantic_overlap.confidence = 0.6

**Calculation:**
- JOIN: max(0.65, 0.6*0.8 + 0.4*0.65) = max(0.65, 0.74) = **0.74** → CONFIRMED (Rule 2)
- LINEAGE: 0.5*0.55 + 0.5*0.4 = **0.475** → under 0.5 threshold, output as POSSIBLE (Rule 6)
- SEMANTIC_OVERLAP: 0.6 → include with moderate confidence (below 0.7, above 0.4)

**Output:**
- join: CONFIRMED (confidence 0.74)
- lineage: POSSIBLE (confidence 0.475)
- semantic_overlap: included (confidence 0.6)
