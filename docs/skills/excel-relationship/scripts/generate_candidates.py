"""
Candidate Generator — prunes O(n²) sheet pairs to a manageable candidate set
for deep relationship analysis.

Consumes:
  - --schemas-json: path to a JSON file containing a list of extract_schema.py
                    output objects (one per Excel file).

Emits to stdout: JSON with intra_file_pairs, cross_file_pairs, and summary counts.

No external dependencies — stdlib only.
"""

import argparse
import difflib
import json
import os
import sys
from dataclasses import dataclass, field
from typing import Optional


# ---------------------------------------------------------------------------
# Data model
# ---------------------------------------------------------------------------

@dataclass
class SheetRef:
    file: str
    sheet: str
    table_index: int
    columns: list  # list of ColumnSchema dicts


def flatten_schemas(all_schemas: list) -> list[SheetRef]:
    """Flatten a list of file-level schema objects into SheetRef records."""
    refs: list[SheetRef] = []
    for file_schema in all_schemas:
        file_name = file_schema.get("file", "unknown")
        for sheet in file_schema.get("sheets", []):
            sheet_name = sheet.get("name", "")
            for table in sheet.get("tables", []):
                refs.append(SheetRef(
                    file=file_name,
                    sheet=sheet_name,
                    table_index=table.get("table_index", 0),
                    columns=table.get("columns", []),
                ))
    return refs


# ---------------------------------------------------------------------------
# Pruning filters
# ---------------------------------------------------------------------------

def _col_name_similarity(a: str, b: str) -> float:
    """Return SequenceMatcher ratio on lowercased names."""
    return difflib.SequenceMatcher(None, a.lower(), b.lower()).ratio()


def _has_column_name_overlap(ref_a: SheetRef, ref_b: SheetRef) -> tuple[bool, list[str]]:
    """
    Filter 2: column name overlap.

    Include if any col_a × col_b pair satisfies:
      - exact match (case-insensitive), OR
      - SequenceMatcher ratio > 0.7 for names with len > 3

    Returns (matched, matching_column_labels).
    """
    matching: list[str] = []
    for ca in ref_a.columns:
        for cb in ref_b.columns:
            name_a = ca.get("name", "")
            name_b = cb.get("name", "")
            if name_a.lower() == name_b.lower():
                matching.append(f"{name_a}/{name_b}")
                continue
            if len(name_a) > 3 and len(name_b) > 3:
                if _col_name_similarity(name_a, name_b) > 0.7:
                    matching.append(f"{name_a}/{name_b}")
    # Deduplicate while preserving order
    seen: dict = {}
    for m in matching:
        seen[m] = True
    return bool(seen), list(seen.keys())


def _has_id_cardinality_match(ref_a: SheetRef, ref_b: SheetRef) -> bool:
    """
    Filter 3: likely-ID column cardinality match.

    Include if both tables have at least one is_likely_id_column=True column
    and the cardinalities of any such pair are within 10x of each other.
    """
    ids_a = [c for c in ref_a.columns if c.get("is_likely_id_column")]
    ids_b = [c for c in ref_b.columns if c.get("is_likely_id_column")]
    if not ids_a or not ids_b:
        return False
    for ca in ids_a:
        card_a = ca.get("cardinality", 0)
        for cb in ids_b:
            card_b = cb.get("cardinality", 0)
            if card_a == 0 or card_b == 0:
                continue
            ratio = card_a / card_b if card_b >= card_a else card_b / card_a
            if ratio >= 0.1:  # within 10x
                return True
    return False


def _has_shared_date_domain(ref_a: SheetRef, ref_b: SheetRef) -> bool:
    """
    Filter 4: same inferred domain.

    Include if both tables contain at least one column with inferred_type="date".
    """
    has_date = lambda ref: any(
        c.get("inferred_type") == "date" for c in ref.columns
    )
    return has_date(ref_a) and has_date(ref_b)


# ---------------------------------------------------------------------------
# Pair generation
# ---------------------------------------------------------------------------

def generate_pairs(refs: list[SheetRef]) -> tuple[list[dict], list[dict]]:
    """
    Generate all intra-file and qualifying cross-file pairs.

    Returns (intra_file_pairs, cross_file_pairs).
    """
    intra: list[dict] = []
    cross: list[dict] = []

    n = len(refs)
    for i in range(n):
        for j in range(i + 1, n):
            a = refs[i]
            b = refs[j]

            if a.file == b.file:
                # Filter 1: same file — always include
                intra.append({
                    "file": a.file,
                    "sheet_a": a.sheet,
                    "table_a_index": a.table_index,
                    "sheet_b": b.sheet,
                    "table_b_index": b.table_index,
                    "scope": "intra_file",
                    "reason": "same_file",
                    "priority": "high",
                })
            else:
                # Cross-file: apply filters in priority order
                matched, matching_cols = _has_column_name_overlap(a, b)
                if matched:
                    cross.append({
                        "file_a": a.file,
                        "sheet_a": a.sheet,
                        "table_a_index": a.table_index,
                        "file_b": b.file,
                        "sheet_b": b.sheet,
                        "table_b_index": b.table_index,
                        "scope": "cross_file",
                        "reason": "column_name_overlap",
                        "matching_columns": matching_cols,
                        "priority": "high",
                    })
                    continue

                if _has_id_cardinality_match(a, b):
                    cross.append({
                        "file_a": a.file,
                        "sheet_a": a.sheet,
                        "table_a_index": a.table_index,
                        "file_b": b.file,
                        "sheet_b": b.sheet,
                        "table_b_index": b.table_index,
                        "scope": "cross_file",
                        "reason": "id_cardinality_match",
                        "matching_columns": [],
                        "priority": "medium",
                    })
                    continue

                if _has_shared_date_domain(a, b):
                    cross.append({
                        "file_a": a.file,
                        "sheet_a": a.sheet,
                        "table_a_index": a.table_index,
                        "file_b": b.file,
                        "sheet_b": b.sheet,
                        "table_b_index": b.table_index,
                        "scope": "cross_file",
                        "reason": "shared_date_domain",
                        "matching_columns": [],
                        "priority": "low",
                    })
                    # Do not continue — fall through to pruned

    return intra, cross


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(
        description="Prune O(n²) sheet pairs to a manageable candidate set."
    )
    parser.add_argument(
        "--schemas-json",
        required=True,
        help="Path to JSON file: list of extract_schema.py output objects",
    )
    args = parser.parse_args()

    schemas_path = args.schemas_json
    if not os.path.isfile(schemas_path):
        print(
            json.dumps(
                {"error": f"Schemas JSON file not found: {schemas_path}"},
                ensure_ascii=False,
                indent=2,
            )
        )
        return 1

    try:
        with open(schemas_path, encoding="utf-8") as fh:
            all_schemas = json.load(fh)
    except json.JSONDecodeError as exc:
        print(
            json.dumps(
                {"error": f"Invalid JSON in schemas file: {exc}"},
                ensure_ascii=False,
                indent=2,
            )
        )
        return 1

    if not isinstance(all_schemas, list):
        print(
            json.dumps(
                {"error": "schemas-json must be a JSON array of file schema objects"},
                ensure_ascii=False,
                indent=2,
            )
        )
        return 1

    try:
        refs = flatten_schemas(all_schemas)
    except Exception as exc:  # noqa: BLE001
        print(
            json.dumps({"error": f"Failed to flatten schemas: {exc}"}, ensure_ascii=False, indent=2)
        )
        return 1

    total_sheets = len(refs)
    total_possible = total_sheets * (total_sheets - 1) // 2

    try:
        intra_pairs, cross_pairs = generate_pairs(refs)
    except Exception as exc:  # noqa: BLE001
        print(
            json.dumps({"error": f"Pair generation failed: {exc}"}, ensure_ascii=False, indent=2)
        )
        return 1

    candidate_count = len(intra_pairs) + len(cross_pairs)
    pruned_count = total_possible - candidate_count

    output = {
        "total_sheets": total_sheets,
        "total_possible_pairs": total_possible,
        "intra_file_pairs": intra_pairs,
        "cross_file_pairs": cross_pairs,
        "pruned_count": pruned_count,
    }
    print(json.dumps(output, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
