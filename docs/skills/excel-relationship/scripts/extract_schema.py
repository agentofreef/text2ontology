"""
Schema Extractor — reads actual cell values from each LogicalTable detected by
parse_structure.py and produces a rich ColumnSchema with type inference,
cardinality, null counts, and numeric statistics.

Consumes:
  - --file          : path to the .xlsx file
  - --structure-json: JSON string (or @path) produced by parse_structure.py
  - --sample-size   : max rows to sample per column (default 100)

Emits to stdout: JSON with the same file/sheets envelope as parse_structure.py,
each table augmented with full ColumnSchema objects.
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
import os
from datetime import datetime, date
from typing import Any

import openpyxl


# ---------------------------------------------------------------------------
# Type inference
# ---------------------------------------------------------------------------

def _is_null(v: Any) -> bool:
    """Return True if the value counts as null/empty."""
    if v is None:
        return True
    if isinstance(v, str) and v.strip() == "":
        return True
    return False


def infer_type(values: list) -> str:
    """
    Infer the dominant type from a list of raw cell values.

    Rules (applied to non-null values only):
      all datetime/date          → "date"
      all bool                   → "boolean"
      all int                    → "int"
      all float or int           → "float"
      >80% numeric               → "float"
      >80% str                   → "string"
      none non-null              → "empty"
      else                       → "mixed"
    """
    non_null = [v for v in values if not _is_null(v)]
    if not non_null:
        return "empty"

    n = len(non_null)

    if all(isinstance(v, (datetime, date)) for v in non_null):
        return "date"

    if all(isinstance(v, bool) for v in non_null):
        return "boolean"

    # bools are subclass of int in Python — exclude them before int check
    non_bool = [v for v in non_null if not isinstance(v, bool)]
    if non_bool and all(isinstance(v, int) for v in non_bool) and len(non_bool) == n:
        return "int"

    if all(isinstance(v, (int, float)) and not isinstance(v, bool) for v in non_null):
        return "float"

    numeric_count = sum(
        1 for v in non_null
        if isinstance(v, (int, float)) and not isinstance(v, bool)
    )
    if numeric_count / n > 0.8:
        return "float"

    string_count = sum(1 for v in non_null if isinstance(v, str))
    if string_count / n > 0.8:
        return "string"

    return "mixed"


# ---------------------------------------------------------------------------
# Numeric stats
# ---------------------------------------------------------------------------

def _numeric_stats(values: list) -> dict | None:
    """Return {min, max, mean} for float/int columns, else None."""
    nums = []
    for v in values:
        if isinstance(v, bool) or _is_null(v):
            continue
        if isinstance(v, (int, float)):
            nums.append(float(v))
    if not nums:
        return None
    return {
        "min": min(nums),
        "max": max(nums),
        "mean": round(sum(nums) / len(nums), 6),
    }


# ---------------------------------------------------------------------------
# Column schema builder
# ---------------------------------------------------------------------------

def build_column_schema(
    ws,
    col_index: int,
    col_name: str,
    data_rows: list[int],
    sample_size: int,
) -> dict:
    """
    Read values for one column from the worksheet and build a ColumnSchema dict.

    Parameters
    ----------
    ws         : openpyxl worksheet (opened with data_only=True)
    col_index  : 1-based column index
    col_name   : column name from the structure JSON
    data_rows  : list of 1-based row numbers that are DATA rows
    sample_size: max rows to sample
    """
    rows_to_read = data_rows[:sample_size]
    total_count = len(rows_to_read)

    raw_values: list[Any] = []
    for r in rows_to_read:
        cell = ws.cell(row=r, column=col_index)
        raw_values.append(cell.value)

    null_count = sum(1 for v in raw_values if _is_null(v))
    non_null_values = [v for v in raw_values if not _is_null(v)]

    inferred_type = infer_type(raw_values)

    # Distinct non-null values (preserve insertion order, cap at 10)
    seen: dict = {}
    for v in non_null_values:
        key = str(v).strip()
        if key not in seen:
            seen[key] = True
    cardinality = len(seen)
    sample_values = list(seen.keys())[:10]

    unique_ratio = round(cardinality / total_count, 6) if total_count > 0 else 0.0

    # Numeric stats only for numeric types
    num_stats = None
    if inferred_type in ("float", "int"):
        num_stats = _numeric_stats(raw_values)

    is_likely_id = (
        unique_ratio > 0.8
        and inferred_type in ("string", "int")
        and total_count > 0
    )

    return {
        "name": col_name,
        "col_index": col_index,
        "inferred_type": inferred_type,
        "sample_values": sample_values,
        "cardinality": cardinality,
        "null_count": null_count,
        "total_count": total_count,
        "unique_ratio": unique_ratio,
        "numeric_stats": num_stats,
        "is_likely_id_column": is_likely_id,
    }


# ---------------------------------------------------------------------------
# Sheet / table processing
# ---------------------------------------------------------------------------

def process_sheet(sheet_struct: dict, wb, sample_size: int) -> dict:
    """
    Process one sheet from the structure JSON.

    Returns the same shape as the input but replaces each table's 'columns'
    list with full ColumnSchema objects and adds 'row_count'.
    """
    sheet_name = sheet_struct["name"]
    try:
        ws = wb[sheet_name]
    except KeyError:
        # Sheet not found in workbook — return structure unchanged
        return sheet_struct

    tables_out = []
    for table in sheet_struct.get("logical_tables", []):
        data_rows: list[int] = table.get("data_rows", [])
        row_count = len(data_rows)

        columns_out = []
        for col in table.get("columns", []):
            schema = build_column_schema(
                ws=ws,
                col_index=col["col_index"],
                col_name=col["name"],
                data_rows=data_rows,
                sample_size=sample_size,
            )
            columns_out.append(schema)

        tables_out.append({
            "table_index": table["table_index"],
            "row_count": row_count,
            "region": table.get("region", {}),
            "columns": columns_out,
        })

    return {
        "name": sheet_name,
        "tables": tables_out,
    }


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(
        description="Extract full column schemas from LogicalTables detected by parse_structure.py."
    )
    parser.add_argument("--file", required=True, help="Path to the .xlsx file")
    parser.add_argument(
        "--structure-json",
        required=True,
        help="JSON string from parse_structure.py, or @/path/to/file.json",
    )
    parser.add_argument(
        "--sample-size",
        type=int,
        default=100,
        help="Max data rows to sample per column (default: 100)",
    )
    args = parser.parse_args()

    # Resolve structure JSON (inline string or @filepath)
    if args.structure_json.startswith("@"):
        json_path = args.structure_json[1:]
        if not os.path.isfile(json_path):
            print(
                json.dumps(
                    {"error": f"Structure JSON file not found: {json_path}"},
                    ensure_ascii=False,
                    indent=2,
                )
            )
            return 1
        with open(json_path, encoding="utf-8") as fh:
            raw_json = fh.read()
    else:
        raw_json = args.structure_json

    try:
        structure = json.loads(raw_json)
    except json.JSONDecodeError as exc:
        print(
            json.dumps(
                {"error": f"Invalid structure JSON: {exc}"},
                ensure_ascii=False,
                indent=2,
            )
        )
        return 1

    file_path = args.file
    if not os.path.isfile(file_path):
        print(
            json.dumps(
                {"error": f"File not found: {file_path}"},
                ensure_ascii=False,
                indent=2,
            )
        )
        return 1

    try:
        wb = openpyxl.load_workbook(file_path, data_only=True, read_only=True)
    except Exception as exc:  # noqa: BLE001
        print(
            json.dumps({"error": f"Cannot open workbook: {exc}"}, ensure_ascii=False, indent=2)
        )
        return 1

    sheets_out = []
    for sheet_struct in structure.get("sheets", []):
        try:
            sheets_out.append(process_sheet(sheet_struct, wb, args.sample_size))
        except Exception as exc:  # noqa: BLE001
            sheets_out.append({
                "name": sheet_struct.get("name", "unknown"),
                "error": str(exc),
                "tables": [],
            })

    wb.close()

    output = {
        "file": structure.get("file", os.path.basename(file_path)),
        "sheets": sheets_out,
    }
    print(json.dumps(output, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
