# Semantic Relationship Analyst Prompt

Evaluate whether two Excel sheets have semantic relationships based on their descriptions, schemas, and algorithmic evidence.

## Sheet A

- **File:** {{file_a}}
- **Sheet:** {{sheet_a}}
- **Description:** {{description_a}}
- **Columns:** {{columns_a}}

## Sheet B

- **File:** {{file_b}}
- **Sheet:** {{sheet_b}}
- **Description:** {{description_b}}
- **Columns:** {{columns_b}}

## Algorithmic Evidence

### Structural Evidence (from compute_overlap.py)

{{structural_evidence}}

### Statistical Evidence (from compute_statistics.py)

{{statistical_evidence}}

## Task

Analyze the two sheets to determine if they have semantic relationships. Evaluate three relationship types:

1. **join**: Can records from one sheet be matched with records from the other using column values?
2. **lineage**: Does one sheet transform or derive data from the other?
3. **semantic_overlap**: Do the sheets represent the same business entity or related business processes?

## Output Format

Return a JSON object with this exact structure:

```json
{
  "join": {
    "exists": boolean,
    "confidence": float (0.0-1.0),
    "evidence": "human-readable explanation",
    "column_pairs": [
      {
        "col_a": "column name from sheet A",
        "col_b": "column name from sheet B",
        "relationship": "description of how these columns relate"
      }
    ]
  },
  "lineage": {
    "exists": boolean,
    "confidence": float (0.0-1.0),
    "direction": "A→B | B→A | bidirectional | null",
    "evidence": "human-readable explanation"
  },
  "semantic_overlap": {
    "exists": boolean,
    "confidence": float (0.0-1.0),
    "overlap_type": "same_entity_different_scope | same_data_different_format | related_business_process | null",
    "evidence": "human-readable explanation"
  },
  "overall_related": boolean,
  "overall_confidence": float (0.0-1.0)
}
```

## Evaluation Criteria

### JOIN Relationships

- **True join exists** if: Columns in Sheet A can reliably match records in Sheet B using shared identifiers or foreign keys
- **Possible join** if: Column names or data types suggest joinability, but sample data is limited
- **No join** if: No meaningful overlap in column values or semantics
- **Evidence** should explain which columns are candidates and why

### LINEAGE Relationships

- **True lineage** if: One sheet clearly transforms, aggregates, or filters data from the other
  - Examples: daily sales → monthly summary, raw data → calculated metrics
- **Possible lineage** if: Statistical patterns suggest transformation (e.g., row count ratio matches expected aggregation)
- **No lineage** if: Sheets capture independent data or different time periods
- **Direction** clarifies which sheet is source vs. derived (A→B means A is source, B is derived)

### SEMANTIC_OVERLAP Relationships

- **same_entity_different_scope**: Same entity type (e.g., "Customer") but different time periods, regions, or product lines
- **same_data_different_format**: Same underlying data but restructured (e.g., wide vs. long format)
- **related_business_process**: Different entities but connected in a business workflow (e.g., Purchase Order → Invoice)

## Critical Rules

1. **Be conservative.** If unsure about any relationship, set `confidence < 0.4`.
2. **Only set exists=true if there is clear evidence.** Speculation or weak similarity is not sufficient.
3. **Balance signals.** High column name similarity alone is not enough. Validate with structural/statistical evidence.
4. **Algorithmic evidence is secondary.** Use it to inform confidence, but LLM judgment (understanding business context) is primary.
5. **Explain your reasoning.** The `evidence` field should justify the confidence level and relationship type.

Return only the JSON object, no additional text.
