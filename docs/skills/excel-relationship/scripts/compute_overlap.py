"""
Structural Analyzer (Agent 1 of 3-agent voting).
Pure algorithmic — NO LLM calls.

Computes column-name similarity and value overlap between two Excel table regions
and emits a join_confidence score.
"""

import subprocess
import sys

for pkg in ["openpyxl"]:
    try:
        __import__(pkg)
    except ImportError:
        subprocess.check_call([sys.executable, "-m", "pip", "install", pkg, "-q"])

import argparse
import difflib
import json
import re
from typing import Any

import openpyxl


# ---------------------------------------------------------------------------
# Chinese digit normalization helpers
# ---------------------------------------------------------------------------

_CN_DIGITS = {"0": "零", "1": "一", "2": "二", "3": "三", "4": "四",
              "5": "五", "6": "六", "7": "七", "8": "八", "9": "九"}
_CN_TO_ASCII = {v: k for k, v in _CN_DIGITS.items()}

_COMMON_AFFIXES = re.compile(
    r"(ID|编号|号|名称|Name|id|name|_id|_no|_num|No|NUM)$",
    re.IGNORECASE,
)


def _strip_affixes(s: str) -> str:
    return _COMMON_AFFIXES.sub("", s).strip()


def _normalize_cn_digits(s: str) -> str:
    """Replace ASCII digits with Chinese equivalents for fuzzy comparison."""
    return "".join(_CN_DIGITS.get(c, c) for c in s)


def _normalize_value(v: Any) -> Any:
    """Normalize a cell value for set comparison."""
    if v is None:
        return None
    if isinstance(v, float):
        return round(v, 2)
    if isinstance(v, str):
        return v.strip().lower()
    return v


def _infer_type(values: list) -> str:
    """Return 'number', 'string', or 'mixed' based on a sample of values."""
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


# ---------------------------------------------------------------------------
# Column-name similarity
# ---------------------------------------------------------------------------

def column_name_similarity(name_a: str, name_b: str) -> float:
    """Return a 0.0-1.0 similarity score between two column names."""
    if name_a == name_b:
        return 1.0

    base_ratio = difflib.SequenceMatcher(None, name_a, name_b).ratio()

    # Try stripping common affixes
    stripped_a = _strip_affixes(name_a)
    stripped_b = _strip_affixes(name_b)
    stripped_ratio = difflib.SequenceMatcher(None, stripped_a, stripped_b).ratio() if stripped_a and stripped_b else 0.0

    # Try Chinese digit normalization
    cn_a = _normalize_cn_digits(name_a)
    cn_b = _normalize_cn_digits(name_b)
    cn_ratio = difflib.SequenceMatcher(None, cn_a, cn_b).ratio()

    return max(base_ratio, stripped_ratio, cn_ratio)


# ---------------------------------------------------------------------------
# Excel data reading
# ---------------------------------------------------------------------------

def read_column_values(file_path: str, sheet_name: str, col_index: int,
                       min_row: int, max_row: int, max_samples: int = 200) -> list:
    """
    Read up to max_samples values from a column (1-based col_index).
    Returns raw cell values (None excluded at caller's discretion).
    """
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
# Join-confidence calculation
# ---------------------------------------------------------------------------

def compute_join_confidence(overlap_ratio: float, name_sim: float) -> float:
    if overlap_ratio > 0.5 and name_sim > 0.6:
        return 0.9
    if overlap_ratio > 0.3 and name_sim > 0.4:
        return 0.7
    if overlap_ratio > 0.1 and name_sim > 0.3:
        return 0.5
    if name_sim > 0.8:
        return 0.4
    return 0.1


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

    best_pairs = []

    for ca in cols_a:
        for cb in cols_b:
            name_sim = column_name_similarity(ca["name"], cb["name"])

            # Skip pairs with very low name similarity for value overlap
            if name_sim <= 0.4:
                best_pairs.append({
                    "col_a": ca["name"],
                    "col_b": cb["name"],
                    "name_similarity": round(name_sim, 4),
                    "overlap_ratio": 0.0,
                    "type_compatible": False,
                    "sample_a_count": 0,
                    "sample_b_count": 0,
                })
                continue

            # Read values
            vals_a = read_column_values(
                file_a, sheet_a, ca["col_index"],
                region_a["min_row"], region_a["max_row"],
            )
            vals_b = read_column_values(
                file_b, sheet_b, cb["col_index"],
                region_b["min_row"], region_b["max_row"],
            )

            # Type compatibility (sample 10 from each)
            type_a = _infer_type(vals_a[:10])
            type_b = _infer_type(vals_b[:10])
            type_compatible = (
                type_a == type_b or type_a == "mixed" or type_b == "mixed"
            )

            # Normalize for set comparison
            norm_a = {_normalize_value(v) for v in vals_a if v is not None}
            norm_b = {_normalize_value(v) for v in vals_b if v is not None}

            # Skip sparse columns
            if len(norm_a) < 5 or len(norm_b) < 5:
                best_pairs.append({
                    "col_a": ca["name"],
                    "col_b": cb["name"],
                    "name_similarity": round(name_sim, 4),
                    "overlap_ratio": 0.0,
                    "type_compatible": type_compatible,
                    "sample_a_count": len(vals_a),
                    "sample_b_count": len(vals_b),
                })
                continue

            intersection = norm_a & norm_b
            union = norm_a | norm_b
            jaccard = len(intersection) / len(union) if union else 0.0

            best_pairs.append({
                "col_a": ca["name"],
                "col_b": cb["name"],
                "name_similarity": round(name_sim, 4),
                "overlap_ratio": round(jaccard, 4),
                "type_compatible": type_compatible,
                "sample_a_count": len(vals_a),
                "sample_b_count": len(vals_b),
            })

    # Sort by a combined score descending
    best_pairs.sort(
        key=lambda p: p["overlap_ratio"] * 0.6 + p["name_similarity"] * 0.4,
        reverse=True,
    )

    # Overall join confidence from the best pair
    if best_pairs:
        top = best_pairs[0]
        join_confidence = compute_join_confidence(
            top["overlap_ratio"], top["name_similarity"]
        )
        evidence = (
            f"Column '{top['col_a']}' and '{top['col_b']}' share "
            f"{top['overlap_ratio']*100:.0f}% of distinct values "
            f"(Jaccard, sample n={top['sample_b_count']})"
        )
    else:
        join_confidence = 0.1
        evidence = "No column pairs found"

    return {
        "best_column_pairs": best_pairs,
        "join_confidence": round(join_confidence, 4),
        "evidence": evidence,
    }


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(
        description="Structural overlap analyzer for two Excel table regions."
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
