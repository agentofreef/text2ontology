# Business Understanding Prompt

Analyze the following Excel sheet and determine what business data it contains.

## Sheet Information

- **File:** {{file_name}}
- **Sheet:** {{sheet_name}}
- **Row Count:** {{row_count}}

## Column Schema

{{columns_description}}

## Sample Data (First 5 Rows)

{{sample_rows}}

## Task

Analyze this sheet to understand its business purpose and characteristics. Return a JSON object with the following fields:

```json
{
  "business_description": "string (1-2 sentences in Chinese describing what business data this sheet contains)",
  "table_type": "data_table | calculation_table | reference_table | unknown",
  "table_type_reason": "brief reason for the classification",
  "domain_area": "e.g., 销售, 财务, 供应链, 人力资源, 客户管理",
  "key_entities": ["list", "of", "main", "business", "entities"],
  "confidence": 0.0
}
```

## Classification Guidelines

**data_table**: Contains primary business records (e.g., sales orders, customer list, inventory items). Has clear business entities and meaningful columns.

**calculation_table**: Contains formulas, derived metrics, or temporary calculations (e.g., pivot tables, variance analysis, ratio calculations). Indicators include:
- Headers like "差值", "比率", "同比", "环比", "累计"
- Mostly formula-driven content
- No clear business entities
- Support/analysis-oriented rather than source data

**reference_table**: Lookup or configuration table (e.g., product catalog, employee directory, price list, mapping table). Has limited rows, key identifiers, and metadata.

**unknown**: Unclear purpose or mixed content that doesn't fit above categories.

## Output Instructions

- **business_description**: Write in Chinese. Be specific about the data type and business purpose. Example: "包含2024年1月至3月的销售订单记录，包括客户、产品、数量和金额信息。"
- **domain_area**: Choose the primary business area. If unclear, list the most likely area.
- **key_entities**: Identify the main entities (nouns) the data is about. E.g., for sales data: ["客户", "订单", "产品"]
- **confidence**: 0.0–1.0. Lower confidence if ambiguous columns, no clear headers, or mixed content.
- **Important**: If the sheet appears to be a calculation/temp table (lots of formulas, no clear business entities, or headers like '差值'/'比率'/'同比'), classify as **calculation_table** regardless of row count.

Return only the JSON object, no additional text.
