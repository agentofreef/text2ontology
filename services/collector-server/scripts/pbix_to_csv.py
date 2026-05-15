#!/usr/bin/env python3
"""Extract PBIX → metadata.json + per-table CSV files.

Usage: python3 pbix_to_csv.py <pbix_path> <output_dir>

Output:
  <output_dir>/metadata.json   — tables, relationships, measures
  <output_dir>/<TableName>.csv — one CSV per real table (UTF-8, header row)

Filters out auto-generated LocalDateTable_* and DateTableTemplate_* tables.
Uses column-by-column fallback for tables with corrupted VertiPaq pages.
"""
import json
import os
import sys

import pandas as pd
from pbixray import PBIXRay

SKIP_PREFIXES = ("LocalDateTable_", "DateTableTemplate_")


def _get_table_partial(model, table_name):
    """Fallback: read table column-by-column, skipping broken ones."""
    try:
        from pbixray.vertipaq_decoder import get_data_slice, AMO_PANDAS_TYPE_MAPPING
    except ImportError:
        return pd.DataFrame()

    dec = model._vertipaq_decoder
    table_meta = dec._meta.schema_df[dec._meta.schema_df["TableName"] == table_name]
    data = {}
    for _, col_meta in table_meta.iterrows():
        try:
            idfmeta_buf = get_data_slice(dec._data_model, col_meta["IDF"] + "meta")
            meta = dec._read_idfmeta(idfmeta_buf)
            col_data = dec._get_column_data(col_meta, meta)
            col_data = dec._handle_special_cases(col_data, col_meta["DataType"])
            pandas_dtype = AMO_PANDAS_TYPE_MAPPING.get(col_meta["DataType"], "object")
            if pandas_dtype == "decimal.Decimal":
                pandas_dtype = "object"
            data[col_meta["ColumnName"]] = col_data.astype(pandas_dtype)
        except Exception:
            pass  # skip broken column
    return pd.DataFrame(data) if data else pd.DataFrame()


def get_table_safe(model, table_name):
    """Get table data with automatic fallback for corrupted pages."""
    try:
        return model.get_table(table_name)
    except Exception:
        return _get_table_partial(model, table_name)


def get_row_count(model, table_name):
    """Estimate row count from statistics."""
    try:
        stats = model.statistics
        tdf = stats[stats["TableName"] == table_name]
        if len(tdf) > 0:
            return int(tdf["Cardinality"].max())
    except Exception:
        pass
    return 0


def main():
    if len(sys.argv) < 3:
        print(json.dumps({"error": "usage: pbix_to_csv.py <pbix_path> <output_dir>"}))
        sys.exit(1)

    pbix_path = sys.argv[1]
    output_dir = sys.argv[2]
    os.makedirs(output_dir, exist_ok=True)

    try:
        model = PBIXRay(pbix_path)
    except Exception as e:
        print(json.dumps({"error": f"failed to parse pbix: {e}"}))
        sys.exit(1)

    # Collect calculated table names
    calc_tables = set()
    if model.dax_tables is not None and len(model.dax_tables) > 0:
        calc_tables = set(model.dax_tables["TableName"].tolist())

    # Collect relationship key columns
    key_columns = set()
    if model.relationships is not None and len(model.relationships) > 0:
        for _, r in model.relationships.iterrows():
            to_table = r.get("ToTableName")
            to_col = r.get("ToColumnName")
            if to_table and to_col and str(to_table) != "None" and str(to_col) != "None":
                key_columns.add((str(to_table), str(to_col)))

    schema = model.schema
    all_table_names = schema["TableName"].unique().tolist()

    # Filter out auto-generated tables
    table_names = [t for t in all_table_names if not any(t.startswith(p) for p in SKIP_PREFIXES)]

    tables_meta = []
    export_results = []

    for tname in table_names:
        cols_df = schema[schema["TableName"] == tname]

        # Determine table type
        if tname in calc_tables:
            table_type = "calculated"
        else:
            table_type = "table"

        # Build column metadata
        columns = []
        for _, row in cols_df.iterrows():
            col_name = row["ColumnName"]
            dt = str(row.get("PandasDataType", "string"))
            if "int" in dt.lower():
                pbi_type = "int64"
            elif "float" in dt.lower() or "decimal" in dt.lower():
                pbi_type = "double"
            elif "datetime" in dt.lower():
                pbi_type = "dateTime"
            elif "bool" in dt.lower():
                pbi_type = "boolean"
            else:
                pbi_type = "string"

            is_key = (tname, col_name) in key_columns

            columns.append({
                "name": col_name,
                "dataType": pbi_type,
                "isKey": is_key,
            })

        # Extract data and write CSV
        df = get_table_safe(model, tname)
        row_count = len(df)
        col_count = len(df.columns)

        csv_name = f"{tname}.csv"
        csv_path = os.path.join(output_dir, csv_name)

        if not df.empty:
            df.to_csv(csv_path, index=False, encoding="utf-8")
        else:
            # Write header-only CSV from schema
            header_cols = [c["name"] for c in columns]
            pd.DataFrame(columns=header_cols).to_csv(csv_path, index=False, encoding="utf-8")

        # Use actual extracted columns (may differ from schema if some cols failed)
        actual_columns = list(df.columns) if not df.empty else [c["name"] for c in columns]

        tables_meta.append({
            "name": tname,
            "tableType": table_type,
            "rowCount": row_count,
            "columnCount": col_count,
            "csvFile": csv_name,
            "columns": columns,
        })

        export_results.append({
            "table": tname,
            "rows": row_count,
            "cols": col_count,
            "csvFile": csv_name,
        })

    # Build relationships (only valid ones with both sides resolved)
    relationships = []
    if model.relationships is not None and len(model.relationships) > 0:
        for _, r in model.relationships.iterrows():
            from_table = r.get("FromTableName")
            to_table = r.get("ToTableName")
            from_col = r.get("FromColumnName")
            to_col = r.get("ToColumnName")

            if not from_table or not to_table or not from_col or not to_col:
                continue
            if str(to_table) == "None" or str(to_col) == "None":
                continue
            # Skip relationships involving filtered-out tables
            if any(str(from_table).startswith(p) for p in SKIP_PREFIXES):
                continue
            if any(str(to_table).startswith(p) for p in SKIP_PREFIXES):
                continue

            card = str(r.get("Cardinality", "M:1"))
            is_active = bool(r.get("IsActive", True))
            if isinstance(r.get("IsActive"), (int, float)):
                is_active = int(r.get("IsActive")) == 1

            cf = str(r.get("CrossFilteringBehavior", "Single"))

            relationships.append({
                "fromTable": str(from_table),
                "fromColumn": str(from_col),
                "toTable": str(to_table),
                "toColumn": str(to_col),
                "cardinality": card,
                "crossFilteringBehavior": cf,
                "isActive": is_active,
            })

    # Build measures
    measures = []
    if model.dax_measures is not None and len(model.dax_measures) > 0:
        for _, mrow in model.dax_measures.iterrows():
            measures.append({
                "tableName": str(mrow.get("TableName", "")),
                "name": str(mrow.get("Name", "")),
                "expression": str(mrow.get("Expression", "")),
            })

    metadata = {
        "tables": tables_meta,
        "relationships": relationships,
        "measures": measures,
        "summary": {
            "tableCount": len(tables_meta),
            "relationshipCount": len(relationships),
            "measureCount": len(measures),
            "totalRows": sum(t["rowCount"] for t in tables_meta),
        },
    }

    meta_path = os.path.join(output_dir, "metadata.json")
    with open(meta_path, "w", encoding="utf-8") as f:
        json.dump(metadata, f, ensure_ascii=False, indent=2)

    # Print summary to stdout
    print(json.dumps({
        "status": "ok",
        "outputDir": output_dir,
        "metadataFile": meta_path,
        "tables": export_results,
        "summary": metadata["summary"],
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
