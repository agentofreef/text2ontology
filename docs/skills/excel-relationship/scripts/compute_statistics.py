"""
Statistical Analyzer (Agent 3 of 3-agent voting).
Pure algorithmic — NO LLM calls.

Detects aggregation patterns (SUM/AVG/MAX/MIN) and cardinality relationships
between two Excel table regions.
"""

import subprocess
import sys

for pkg in ["openpyxl"]:
    try:
        __import__(pkg)
    except ImportError:
        subprocess.check_call([sys.executable, "-m", "pip", "install", pkg, "-q"])

import argparse
import json
from typing import Any

import openpyxl


# ---------------------------------------------------------------------------
# Value helpers
# ---------------------------------------------------------------------------

def _normalize_value(v: Any) -> Any:
    if v is None:
        return None
    if isinstance(v, float):
        return round(v, 2)
    if isinstance(v, str):
        return v.strip().lower()
    return v


def _infer_type(values: list) -> str:
    non_null = [v for v in values if v is not None]
    if not non_null:
        return "mixed"
    numeric = sum(1 for v in non_null if isinstance(v, (int, float)))
    if numeric / len(non_null) >= 0.8:
        return "number"
    string_count = sum(1 for v in non_null if isinstance(v, str))
    if string_count / len(non_null) >= 0.8:
        return "string"
    return "mixed"


def _to_float(v: Any) -> float | None:
    if isinstance(v, (int, float)):
        return float(v)
    return None


# ---------------------------------------------------------------------------
# Excel reading
# ---------------------------------------------------------------------------

def read_column_values(file_path: str, sheet_name: str, col_index: int,
                       min_row: int, max_row: int, max_samples: int = 200) -> list:
    """Read up to max_samples values from a column (1-based col_index)."""
    MAX_ROWS = 10_000
    min_row = max(1, int(min_row))
    max_row = min(int(max_row), min_row + MAX_ROWS - 1)
    wb = openpyxl.load_workbook(file_path, read_only=True, data_only=True)
    ws = wb[sheet_name]
    values = []
    for row in ws.iter_rows(min_row=min_row, max_row=max_row,
                             min_col=col_index, max_col=col_index,
                             values_only=True):
        values.append(row[0])
        if len(values) >= max_samples:
            break
    wb.close()
    return values


# ---------------------------------------------------------------------------
# Aggregation pattern detection
# ---------------------------------------------------------------------------

def _approx_equal(a: float, b: float, tolerance: float) -> bool:
    if b == 0:
        return abs(a) < tolerance
    return abs(a - b) / abs(b) <= tolerance


def detect_aggregation_patterns(
    col_a_name: str,
    col_b_name: str,
    vals_a: list,
    vals_b: list,
) -> list[dict]:
    """
    Test whether numeric aggregates of col_a (SUM/AVG/MAX/MIN) match
    any value in col_b. Returns a list of pattern results.
    """
    nums_a = [_to_float(v) for v in vals_a[:100] if v is not None]
    nums_b = [_to_float(v) for v in vals_b if v is not None]

    if len(nums_a) < 2 or not nums_b:
        return []

    patterns = []

    total_a = sum(nums_a)
    avg_a = total_a / len(nums_a)
    max_a = max(nums_a)
    min_a = min(nums_a)

    checks = [
        ("SUM", total_a, 0.01),
        ("AVG", avg_a, 0.05),
        ("MAX", max_a, 0.01),
        ("MIN", min_a, 0.01),
    ]

    for pattern_name, agg_value, tol in checks:
        matches = sum(1 for b in nums_b if _approx_equal(agg_value, b, tol))
        if matches > 0:
            confidence = round(min(0.99, 0.5 + 0.1 * matches), 4)
            patterns.append({
                "col_a": col_a_name,
                "col_b": col_b_name,
                "pattern": pattern_name,
                "confidence": confidence,
                "evidence": (
                    f"{pattern_name} of '{col_a_name}' values matches "
                    f"values in '{col_b_name}' in {matches}/{len(nums_b)} tested cases"
                ),
            })

    return patterns


# ---------------------------------------------------------------------------
# Cardinality analysis
# ---------------------------------------------------------------------------

def analyze_cardinality(
    col_a_name: str,
    col_b_name: str,
    vals_a: list,
    vals_b: list,
) -> dict:
    """
    Compare cardinalities and compute containment (missing ratio).
    """
    norm_a = {_normalize_value(v) for v in vals_a if v is not None}
    norm_b = {_normalize_value(v) for v in vals_b if v is not None}

    card_a = len(norm_a)
    card_b = len(norm_b)

    ratio = (card_a / card_b) if card_b > 0 else float("inf")

    # Subset signal: how many values in A are missing from B
    missing_in_b = norm_a - norm_b
    missing_ratio = len(missing_in_b) / card_a if card_a > 0 else 1.0

    # JOIN signal heuristics
    suggests_join = False
    direction = "unknown"

    if 0.5 < ratio < 2.0:
        suggests_join = True
        direction = "similar cardinality — peer JOIN"
    elif ratio > 10:
        suggests_join = True
        direction = "A is fact, B is dimension"
    elif ratio < 0.1:
        suggests_join = True
        direction = "A references B"

    # Missing ratio = 0 means A is fully contained in B (FK relationship)
    if missing_ratio == 0.0 and card_a < card_b:
        suggests_join = True
        direction = "A references B"
    elif missing_ratio == 0.0 and card_a >= card_b:
        suggests_join = True
        direction = "B references A (or equal)"

    return {
        "col_a": col_a_name,
        "col_b": col_b_name,
        "cardinality_a": card_a,
        "cardinality_b": card_b,
        "ratio": round(ratio, 4) if ratio != float("inf") else None,
        "missing_ratio": round(missing_ratio, 4),
        "suggests_join": suggests_join,
        "direction": direction,
    }


# ---------------------------------------------------------------------------
# Main analysis
# ---------------------------------------------------------------------------

def analyze(table_a: dict, table_b: dict, file_a: str, file_b: str) -> dict:
    cols_a = table_a["columns"]
    cols_b = table_b["columns"]
    region_a = table_a["region"]
    region_b = table_b["region"]
    sheet_a = table_a["sheet"]
    sheet_b = table_b["sheet"]

    all_agg_patterns: list[dict] = []
    all_cardinality: list[dict] = []

    for ca in cols_a:
        vals_a = read_column_values(
            file_a, sheet_a, ca["col_index"],
            region_a["min_row"], region_a["max_row"],
        )
        type_a = _infer_type(vals_a[:10])

        for cb in cols_b:
            vals_b = read_column_values(
                file_b, sheet_b, cb["col_index"],
                region_b["min_row"], region_b["max_row"],
            )
            type_b = _infer_type(vals_b[:10])

            # Cardinality analysis for all same-type pairs
            if type_a == type_b or type_a == "mixed" or type_b == "mixed":
                card_result = analyze_cardinality(ca["name"], cb["name"], vals_a, vals_b)
                all_cardinality.append(card_result)

            # Aggregation pattern detection for numeric column pairs
            if type_a in ("number", "mixed") and type_b in ("number", "mixed"):
                agg_patterns = detect_aggregation_patterns(
                    ca["name"], cb["name"], vals_a, vals_b
                )
                all_agg_patterns.extend(agg_patterns)

    # Sort aggregation patterns by confidence descending
    all_agg_patterns.sort(key=lambda p: p["confidence"], reverse=True)

    # Lineage confidence: highest single aggregation confidence found
    if all_agg_patterns:
        lineage_confidence = all_agg_patterns[0]["confidence"]
        top = all_agg_patterns[0]
        lineage_evidence = (
            f"Column '{top['col_b']}' values are consistent with "
            f"{top['pattern']} aggregation of '{top['col_a']}'"
        )
    else:
        lineage_confidence = 0.0
        lineage_evidence = "No aggregation patterns detected"

    return {
        "aggregation_patterns": all_agg_patterns,
        "cardinality_analysis": all_cardinality,
        "lineage_confidence": round(lineage_confidence, 4),
        "evidence": lineage_evidence,
    }


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(
        description="Statistical analyzer for two Excel table regions."
    )
    parser.add_argument("--table-a", required=True, help="JSON descriptor for table A")
    parser.add_argument("--table-b", required=True, help="JSON descriptor for table B")
    parser.add_argument("--file-a", required=True, help="Absolute path to file A")
    parser.add_argument("--file-b", required=True, help="Absolute path to file B")
    args = parser.parse_args()

    try:
        table_a = json.loads(args.table_a)
        table_b = json.loads(args.table_b)
    except json.JSONDecodeError as exc:
        print(json.dumps({"error": f"Invalid JSON in table descriptor: {exc}"}, ensure_ascii=False, indent=2))
        return 1

    try:
        result = analyze(table_a, table_b, args.file_a, args.file_b)
    except FileNotFoundError as exc:
        print(json.dumps({"error": f"File not found: {exc}"}, ensure_ascii=False, indent=2))
        return 1
    except KeyError as exc:
        print(json.dumps({"error": f"Missing key in table descriptor: {exc}"}, ensure_ascii=False, indent=2))
        return 1
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc)}, ensure_ascii=False, indent=2))
        return 1

    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
