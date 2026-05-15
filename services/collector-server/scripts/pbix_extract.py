#!/usr/bin/env python3
"""Extract Power BI model metadata from .pbix files using pbixray.

Usage: python3 pbix_extract.py <path-to-pbix>
Outputs JSON to stdout in the same structure as .pbit DataModelSchema,
enriched with sampleValues (all distinct values) and rowCount per table.
"""
import json
import sys
import traceback

import pandas as pd
from pbixray import PBIXRay


def _get_table_cached(model, table_name, _cache={}):
    """Cache get_table results to avoid re-reading for each column."""
    if table_name not in _cache:
        try:
            _cache[table_name] = model.get_table(table_name)
        except Exception:
            # Full table read failed — try column-by-column via internal API
            _cache[table_name] = _get_table_partial(model, table_name)
    return _cache[table_name]


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


def safe_distinct(model, table_name, col_name):
    """Get all distinct values for a column. Returns list of strings."""
    try:
        df = _get_table_cached(model, table_name)
        if df.empty or col_name not in df.columns:
            return []
        series = df[col_name].dropna().unique()
        return [str(v) for v in series]
    except Exception:
        return []


def get_row_count(model, table_name):
    """Estimate row count from statistics (max cardinality) or get_table."""
    try:
        stats = model.statistics
        tdf = stats[stats["TableName"] == table_name]
        if len(tdf) > 0:
            return int(tdf["Cardinality"].max())
    except Exception:
        pass
    try:
        df = model.get_table(table_name)
        return len(df)
    except Exception:
        return 0


def main():
    if len(sys.argv) < 2:
        print(json.dumps({"error": "usage: pbix_extract.py <file>"}))
        sys.exit(1)

    path = sys.argv[1]
    try:
        model = PBIXRay(path)
    except Exception as e:
        print(json.dumps({"error": f"failed to parse pbix: {e}"}))
        sys.exit(1)

    # Collect relationship key columns (ToColumn side = primary key)
    key_columns = set()  # (table_name, column_name)
    if model.relationships is not None and len(model.relationships) > 0:
        for _, r in model.relationships.iterrows():
            to_table = r.get("ToTableName")
            to_col = r.get("ToColumnName")
            if to_table and to_col and str(to_table) != "None" and str(to_col) != "None":
                key_columns.add((str(to_table), str(to_col)))

    # Collect calculated table names
    calc_tables = set()
    if model.dax_tables is not None and len(model.dax_tables) > 0:
        calc_tables = set(model.dax_tables["TableName"].tolist())

    schema = model.schema
    table_names = schema["TableName"].unique().tolist()

    tables = []
    for tname in table_names:
        cols_df = schema[schema["TableName"] == tname]
        row_count = get_row_count(model, tname)

        # Determine table type
        if tname in calc_tables:
            table_type = "calculated"
        else:
            table_type = "table"

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
            sample_values = safe_distinct(model, tname, col_name)

            columns.append({
                "name": col_name,
                "dataType": pbi_type,
                "isHidden": False,
                "isKey": is_key,
                "description": "",
                "sampleValues": sample_values,
            })

        # Measures belonging to this table
        measures = []
        if model.dax_measures is not None and len(model.dax_measures) > 0:
            tbl_measures = model.dax_measures[model.dax_measures["TableName"] == tname]
            for _, mrow in tbl_measures.iterrows():
                measures.append({
                    "name": mrow.get("Name", ""),
                    "expression": mrow.get("Expression", ""),
                    "displayFolder": mrow.get("DisplayFolder", "") or "",
                    "formatString": "",
                    "isHidden": False,
                    "description": mrow.get("Description", "") or "",
                })

        tables.append({
            "name": tname,
            "isHidden": False,
            "tableType": table_type,
            "rowCount": row_count,
            "columns": columns,
            "measures": measures,
        })

    # Build relationships (only those with both sides resolved)
    relationships = []
    if model.relationships is not None and len(model.relationships) > 0:
        rels = model.relationships
        for _, r in rels.iterrows():
            from_table = r.get("FromTableName")
            to_table = r.get("ToTableName")
            from_col = r.get("FromColumnName")
            to_col = r.get("ToColumnName")
            if not from_table or not to_table or not from_col or not to_col:
                continue
            if str(to_table) == "None" or str(to_col) == "None":
                continue

            card = str(r.get("Cardinality", "M:1"))
            card_map = {
                "M:1": "manyToOne",
                "1:M": "oneToMany",
                "1:1": "oneToOne",
                "M:M": "manyToMany",
            }
            pbi_card = card_map.get(card, "manyToOne")

            cf = str(r.get("CrossFilteringBehavior", "Single"))
            cf_map = {
                "Both": "bothDirections",
                "Single": "oneDirection",
            }
            pbi_cf = cf_map.get(cf, "oneDirection")

            is_active = bool(r.get("IsActive", True))
            if isinstance(r.get("IsActive"), (int, float)):
                is_active = int(r.get("IsActive")) == 1

            relationships.append({
                "fromTable": from_table,
                "fromColumn": from_col,
                "toTable": to_table,
                "toColumn": to_col,
                "cardinality": pbi_card,
                "crossFilteringBehavior": pbi_cf,
                "isActive": is_active,
            })

    result = {
        "model": {
            "tables": tables,
            "relationships": relationships,
        }
    }
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
