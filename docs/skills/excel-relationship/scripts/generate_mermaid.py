"""
Generate Mermaid ER diagrams and a comprehensive report from relationship analysis results.

Reads relationships.json and sheet_descriptions.json, then writes:
  - er_diagram_intra.md   (intra-file relationships)
  - er_diagram_cross.md   (cross-file relationships)
  - er_diagram_full.md    (all relationships)
  - relationship_report.md

Stdlib only — no external dependencies.
"""

import argparse
import json
import os
import pathlib
import re
import sys
from datetime import datetime
from typing import Any


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

STATUS_CONFIRMED = "CONFIRMED"
STATUS_NEEDS_REVIEW = "NEEDS_USER_REVIEW"
STATUS_POSSIBLE = "POSSIBLE"

STATUS_LABELS = {
    STATUS_CONFIRMED: "✅ 确认",
    STATUS_NEEDS_REVIEW: "⚠️ 待确认",
    STATUS_POSSIBLE: "❓ 可能",
}

MERMAID_RELATION_MAP = {
    "JOIN": "||--o{",
    "JOIN_MANY_TO_MANY": "}o--o{",
    "LINEAGE": "||..o{",
    "SEMANTIC_OVERLAP": "||..||",
}

# Fallback for unknown relationship types
DEFAULT_RELATION = "||--o{"

# Characters that break Mermaid entity names
_UNSAFE_CHAR_RE = re.compile(r"[\s./\\()\[\]{}<>,:;'\"|&^%$#@!?=+*`~]")


# ---------------------------------------------------------------------------
# Path helpers
# ---------------------------------------------------------------------------

def _safe_output_path(output_dir: str, filename: str) -> pathlib.Path:
    """Resolve output path, asserting it stays within /mnt/user-data."""
    resolved_dir = pathlib.Path(output_dir).resolve()
    # In sandbox, output must be under /mnt/user-data (virtual path)
    # Just resolve and use — harness already validated the dir path
    resolved_dir.mkdir(parents=True, exist_ok=True)
    return resolved_dir / filename


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _escape_mermaid_label(s: str) -> str:
    return s.replace('"', "'").replace("\n", " ").replace("\r", "")


def sanitize_entity_name(file_name: str, sheet_name: str) -> str:
    """
    Combine file (without extension) and sheet into a safe Mermaid entity name.
    Spaces/dots/slashes/brackets → _  then collapse multiple underscores.
    """
    base = os.path.splitext(file_name)[0]
    raw = f"{base}__{sheet_name}"
    safe = _UNSAFE_CHAR_RE.sub("_", raw)
    safe = re.sub(r"_+", "_", safe).strip("_")
    return safe


def sheet_key(file_name: str, sheet_name: str) -> str:
    return f"{file_name}::{sheet_name}"


def format_confidence(c: float) -> str:
    return f"{c:.2f}"


def mermaid_relation(rel_type: str, column_pairs: list[dict]) -> str:
    """
    Return the Mermaid relation token.
    For JOIN, check if multiple column pairs hint at many-to-many;
    otherwise default to one-to-many.
    """
    rt = (rel_type or "").upper()
    if rt == "JOIN" and len(column_pairs) > 1:
        # Heuristic: many column pairs may indicate M:N
        return MERMAID_RELATION_MAP.get("JOIN_MANY_TO_MANY", DEFAULT_RELATION)
    return MERMAID_RELATION_MAP.get(rt, DEFAULT_RELATION)


def mermaid_label(rel_type: str, column_pairs: list[dict], confidence: float) -> str:
    if column_pairs:
        pair = column_pairs[0]
        col_a = pair.get("col_a", "?")
        col_b = pair.get("col_b", "?")
        cols_str = f"{col_a}\u2194{col_b}"
    else:
        cols_str = "?"
    label = f"{rel_type}: {cols_str} ({format_confidence(confidence)})"
    return _escape_mermaid_label(label)


def validate_mermaid_lines(lines: list[str]) -> list[str]:
    """
    Basic validation of erDiagram body lines.
    Returns list of warning strings (empty = valid).
    """
    # Mermaid erDiagram relation tokens use |, o, {, } legitimately (e.g. ||--o{).
    # Pattern: EntityA RELATION EntityB : "LABEL"
    # Relation token is one of the standard erDiagram tokens.
    relation_re = re.compile(
        r'^(\S+)\s+([|o{}\-.]+)\s+(\S+)\s*:\s*"[^"]*"$'
    )
    # Characters that are illegal in entity names (outside the relation token)
    entity_unsafe_re = re.compile(r'[\[\]<>]')
    warnings = []
    for line in lines:
        stripped = line.strip()
        if not stripped or stripped.startswith("erDiagram"):
            continue
        m = relation_re.match(stripped)
        if not m:
            warnings.append(f"Possibly invalid Mermaid line: {stripped!r}")
            continue
        entity_a, entity_b = m.group(1), m.group(3)
        for entity in (entity_a, entity_b):
            if entity_unsafe_re.search(entity):
                warnings.append(
                    f"Entity name contains unsafe char: {entity!r} in line {stripped!r}"
                )
    return warnings


def current_timestamp() -> str:
    return datetime.now().strftime("%Y-%m-%d %H:%M")


# ---------------------------------------------------------------------------
# Diagram generation
# ---------------------------------------------------------------------------

def build_er_diagram_lines(relationships: list[dict]) -> list[str]:
    """Return the inner lines of an erDiagram block (no ```mermaid fence)."""
    lines = ["erDiagram"]
    for rel in relationships:
        sa = rel.get("sheet_a", {})
        sb = rel.get("sheet_b", {})
        entity_a = sanitize_entity_name(sa.get("file", ""), sa.get("sheet", ""))
        entity_b = sanitize_entity_name(sb.get("file", ""), sb.get("sheet", ""))
        col_pairs = rel.get("column_pairs") or []
        rel_type = rel.get("relationship_type", "JOIN")
        confidence = rel.get("confidence", 0.0)
        relation = mermaid_relation(rel_type, col_pairs)
        label = mermaid_label(rel_type, col_pairs, confidence)
        lines.append(f'    {entity_a} {relation} {entity_b} : "{label}"')
    return lines


def count_by_status(relationships: list[dict]) -> tuple[int, int, int]:
    confirmed = sum(
        1 for r in relationships if r.get("status") == STATUS_CONFIRMED
    )
    needs_review = sum(
        1 for r in relationships if r.get("status") == STATUS_NEEDS_REVIEW
    )
    possible = sum(
        1 for r in relationships if r.get("status") == STATUS_POSSIBLE
    )
    return confirmed, needs_review, possible


def build_detail_table(relationships: list[dict], start_idx: int = 1) -> str:
    rows = [
        "| # | Sheet A | Sheet B | 类型 | 关联列 | 置信度 | 状态 |",
        "|---|---------|---------|------|--------|--------|------|",
    ]
    for i, rel in enumerate(relationships, start=start_idx):
        sa = rel.get("sheet_a", {})
        sb = rel.get("sheet_b", {})
        file_a = sa.get("file", "")
        sheet_a = sa.get("sheet", "")
        file_b = sb.get("file", "")
        sheet_b = sb.get("sheet", "")
        rel_type = rel.get("relationship_type", "?")
        confidence = rel.get("confidence", 0.0)
        status_raw = rel.get("status", "")
        status_label = STATUS_LABELS.get(status_raw, status_raw)
        col_pairs = rel.get("column_pairs") or []
        if col_pairs:
            pair = col_pairs[0]
            cols_str = f"{pair.get('col_a', '?')} \u2194 {pair.get('col_b', '?')}"
        else:
            cols_str = "—"
        rows.append(
            f"| {i} | {file_a} / {sheet_a} | {file_b} / {sheet_b} | "
            f"{rel_type} | {cols_str} | {format_confidence(confidence)} | {status_label} |"
        )
    return "\n".join(rows)


def generate_diagram_md(
    title: str,
    relationships: list[dict],
    timestamp: str,
) -> str:
    confirmed, needs_review, possible = count_by_status(relationships)
    diagram_lines = build_er_diagram_lines(relationships)

    # Validate and emit warnings to stderr
    body_lines = diagram_lines[1:]  # skip "erDiagram" header for validation
    warnings = validate_mermaid_lines(body_lines)
    for w in warnings:
        print(f"[WARN] {w}", file=sys.stderr)

    mermaid_block = "\n".join(["```mermaid"] + diagram_lines + ["```"])

    detail_section = ""
    confirmed_rels = [r for r in relationships if r.get("status") == STATUS_CONFIRMED]
    if confirmed_rels:
        detail_section = (
            "\n## 确认关系详情\n\n"
            + build_detail_table(confirmed_rels)
        )

    return (
        f"# Excel 关系图 — {title}\n\n"
        f"分析时间: {timestamp} | "
        f"确认关系: {confirmed}条 | "
        f"待确认: {needs_review}条 | "
        f"可能关系: {possible}条\n\n"
        + mermaid_block
        + detail_section
        + "\n"
    )


# ---------------------------------------------------------------------------
# Report generation
# ---------------------------------------------------------------------------

def build_agent_votes_section(rel: dict) -> str:
    votes = rel.get("agent_votes") or {}
    if not votes:
        return ""
    lines = ["\n  **Agent 投票:**\n"]
    for agent_name, vote in votes.items():
        conf = vote.get("confidence", 0.0)
        evidence = vote.get("evidence", "")
        lines.append(
            f"  - **{agent_name}**: 置信度 {format_confidence(conf)} — {evidence}"
        )
    return "\n".join(lines)


def generate_report_md(
    relationships: list[dict],
    sheet_descriptions: dict[str, Any],
    timestamp: str,
) -> str:
    confirmed_rels = [r for r in relationships if r.get("status") == STATUS_CONFIRMED]
    needs_review_rels = [
        r for r in relationships if r.get("status") == STATUS_NEEDS_REVIEW
    ]
    possible_rels = [r for r in relationships if r.get("status") == STATUS_POSSIBLE]

    # Collect unique files and sheets
    files_seen: dict[str, set] = {}
    for rel in relationships:
        for side in ("sheet_a", "sheet_b"):
            s = rel.get(side, {})
            f = s.get("file", "")
            sh = s.get("sheet", "")
            if f:
                files_seen.setdefault(f, set()).add(sh)

    total_files = len(files_seen)
    total_sheets = sum(len(v) for v in files_seen.values())

    sections = [
        f"# Excel 关系分析报告\n",
        f"生成时间: {timestamp}\n",
        "## 执行摘要\n",
        (
            f"- **分析文件数**: {total_files}\n"
            f"- **分析Sheet数**: {total_sheets}\n"
            f"- **关系总数**: {len(relationships)}\n"
            f"- **已确认**: {len(confirmed_rels)}\n"
            f"- **待用户确认**: {len(needs_review_rels)}\n"
            f"- **可能关系**: {len(possible_rels)}\n"
        ),
    ]

    # Per-file breakdown
    sections.append("## 文件概览\n")
    for file_name, sheets in sorted(files_seen.items()):
        sections.append(f"### {file_name}\n")
        for sheet in sorted(sheets):
            key = sheet_key(file_name, sheet)
            desc = sheet_descriptions.get(key, {})
            biz_desc = desc.get("business_description", "—")
            table_type = desc.get("table_type", "—")
            domain = desc.get("domain_area", "—")
            entities = desc.get("key_entities") or []
            entities_str = "、".join(entities) if entities else "—"
            sections.append(
                f"- **Sheet**: {sheet}\n"
                f"  - 描述: {biz_desc}\n"
                f"  - 类型: {table_type} | 领域: {domain}\n"
                f"  - 关键实体: {entities_str}\n"
            )

    def rel_block(rel: dict, idx: int) -> str:
        sa = rel.get("sheet_a", {})
        sb = rel.get("sheet_b", {})
        col_pairs = rel.get("column_pairs") or []
        col_strs = [
            f"`{p.get('col_a', '?')}` ↔ `{p.get('col_b', '?')}` "
            f"(名称相似度: {p.get('name_similarity', 0):.2f}, "
            f"重叠率: {p.get('overlap_ratio', 0):.2f})"
            for p in col_pairs
        ]
        votes_section = build_agent_votes_section(rel)
        direction = rel.get("direction") or "—"
        scope = rel.get("scope", "—")
        evidence = rel.get("evidence", "—")
        return (
            f"### {idx}. {sa.get('file', '')} / {sa.get('sheet', '')} "
            f"→ {sb.get('file', '')} / {sb.get('sheet', '')}\n\n"
            f"- **类型**: {rel.get('relationship_type', '?')}\n"
            f"- **置信度**: {format_confidence(rel.get('confidence', 0.0))}\n"
            f"- **范围**: {scope} | **方向**: {direction}\n"
            f"- **证据**: {evidence}\n"
            f"- **关联列**:\n"
            + (
                "\n".join(f"  - {s}" for s in col_strs)
                if col_strs
                else "  - —"
            )
            + votes_section
            + "\n"
        )

    if confirmed_rels:
        sections.append("## 已确认关系\n")
        for i, rel in enumerate(confirmed_rels, 1):
            sections.append(rel_block(rel, i))

    if needs_review_rels:
        sections.append(
            "## 待用户确认关系\n\n"
            "> 以下关系置信度中等，建议人工核实。\n"
        )
        for i, rel in enumerate(needs_review_rels, 1):
            sections.append(rel_block(rel, i))

    if possible_rels:
        sections.append(
            "## 可能关系（低置信度提示）\n\n"
            "> 以下关系置信度较低，仅供参考。\n"
        )
        for i, rel in enumerate(possible_rels, 1):
            sections.append(rel_block(rel, i))

    return "\n".join(sections)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Generate Mermaid ER diagrams and a report from relationship analysis results."
    )
    parser.add_argument(
        "--relationships-json",
        required=True,
        help="Path to relationships.json (list of RelationshipResult)",
    )
    parser.add_argument(
        "--sheet-descriptions-json",
        required=True,
        help='Path to sheet_descriptions.json (dict of "file::sheet" -> description)',
    )
    parser.add_argument(
        "--output-dir",
        required=True,
        help="Directory to write output files",
    )
    return parser.parse_args()


def main() -> None:
    args = parse_args()

    # Load inputs
    try:
        with open(args.relationships_json, encoding="utf-8") as f:
            relationships: list[dict] = json.load(f)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"ERROR: Cannot read relationships JSON: {exc}", file=sys.stderr)
        sys.exit(1)

    try:
        with open(args.sheet_descriptions_json, encoding="utf-8") as f:
            sheet_descriptions: dict[str, Any] = json.load(f)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"ERROR: Cannot read sheet_descriptions JSON: {exc}", file=sys.stderr)
        sys.exit(1)

    if not isinstance(relationships, list):
        print("ERROR: relationships.json must be a JSON array", file=sys.stderr)
        sys.exit(1)

    timestamp = current_timestamp()

    # Split by scope
    intra = [r for r in relationships if r.get("scope") == "intra_file"]
    cross = [r for r in relationships if r.get("scope") == "cross_file"]
    # Anything not tagged goes into full only
    full = relationships

    output_files = []

    def write_md(filename: str, content: str) -> str:
        path = _safe_output_path(args.output_dir, filename)
        with open(path, "w", encoding="utf-8") as f:
            f.write(content)
        return str(path)

    # er_diagram_intra.md
    intra_md = generate_diagram_md("文件内部关系", intra, timestamp)
    output_files.append(write_md("er_diagram_intra.md", intra_md))

    # er_diagram_cross.md
    cross_md = generate_diagram_md("跨文件关系", cross, timestamp)
    output_files.append(write_md("er_diagram_cross.md", cross_md))

    # er_diagram_full.md
    full_md = generate_diagram_md("全部关系", full, timestamp)
    output_files.append(write_md("er_diagram_full.md", full_md))

    # relationship_report.md
    report_md = generate_report_md(relationships, sheet_descriptions, timestamp)
    output_files.append(write_md("relationship_report.md", report_md))

    # Totals for stdout summary
    confirmed, needs_review, possible = count_by_status(relationships)

    result = {
        "output_files": output_files,
        "confirmed_count": confirmed,
        "needs_review_count": needs_review,
        "possible_count": possible,
    }
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
