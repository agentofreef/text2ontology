#!/usr/bin/env python3
"""
demo-seed/generate.py — programmatic generation of demo Workstream/Milestone/Dependency.

Reads the 25 demo_launch rows (mirrored as constants below to keep the script
self-contained) and produces a deterministic SQL file with INSERTs.

Determinism: fixed random seed -> re-running produces byte-identical output.
This makes the seed pack reproducible and `git diff`-friendly.

Output: scripts/demo-seed/seed_generated.sql

Usage:
    python3 scripts/demo-seed/generate.py
    # then mount the resulting SQL at /docker-entrypoint-initdb.d/04-demo-generated.sql
"""

from __future__ import annotations
import random
from datetime import date, timedelta
from pathlib import Path

random.seed(42)
TODAY = date(2027, 3, 15)
OUTPUT = Path(__file__).parent / "seed_generated.sql"

# ============================================================================
# Embedded launch table (mirror of seed_static.sql; keep in sync manually)
# ============================================================================

LAUNCHES = [
    # (launch_id, name, product_line, target_date, status)
    ("L-S25",         "S25",            "Phone",    date(2027,  9,  1), "on_track"),
    ("L-S25-PRO",     "S25 Pro",        "Phone",    date(2027,  9, 15), "on_track"),  # HERO
    ("L-S25-SLIM",    "S25 Slim",       "Phone",    date(2027, 10,  5), "planned"),
    ("L-S25-LITE",    "S25 Lite",       "Phone",    date(2027, 10, 20), "planned"),
    ("L-A25",         "A25",            "Phone",    date(2027, 11, 10), "planned"),
    ("L-A25-PRO",     "A25 Pro",        "Phone",    date(2027, 11, 25), "planned"),
    ("L-T-PRO-14",    "T-Pro 14",       "Laptop",   date(2027,  5, 15), "on_track"),
    ("L-X1-CARBON-S", "X1 Carbon Slim", "Laptop",   date(2027,  9, 22), "at_risk"),
    ("L-Y-GAMING-16", "Y Gaming 16",    "Laptop",   date(2027,  8, 10), "on_track"),
    ("L-Z-LITE-13",   "Z Lite 13",      "Laptop",   date(2027, 11,  5), "planned"),
    ("L-X1-FOLD",     "X1 Fold",        "Laptop",   date(2027, 12, 10), "planned"),
    ("L-M14",         "M14",            "Tablet",   date(2027,  6, 20), "on_track"),
    ("L-M14-PRO",     "M14 Pro",        "Tablet",   date(2027, 10, 15), "planned"),
    ("L-M14-MINI",    "M14 Mini",       "Tablet",   date(2027,  7, 25), "on_track"),
    ("L-WATCH-S",     "Watch S",        "Wearable", date(2027,  4, 15), "launched"),
    ("L-WATCH-S-PRO", "Watch S Pro",    "Wearable", date(2027,  9, 20), "on_track"),
    ("L-WATCH-ACT",   "Watch S Active", "Wearable", date(2027, 11, 30), "planned"),
    ("L-BUDS-AIR",    "Buds Air",       "Audio",    date(2027,  5,  5), "launched"),
    ("L-BUDS-PRO",    "Buds Pro",       "Audio",    date(2027,  9, 18), "on_track"),
    ("L-BUDS-SPORT",  "Buds Sport",     "Audio",    date(2027,  8, 25), "on_track"),
    ("L-OVEREAR-PRO", "Over-Ear Pro",   "Audio",    date(2027, 12,  5), "planned"),
    ("L-P27-4K",      "P27 4K",         "Display",  date(2027,  7, 10), "on_track"),
    ("L-G27-165",     "G27 165Hz",      "Display",  date(2027, 10, 30), "planned"),
    ("L-BAND",        "智能手环",        "Other",    date(2027,  6, 15), "on_track"),
    ("L-FOLD-KB",     "折叠键盘",        "Other",    date(2027,  8, 20), "planned"),
]
assert len(LAUNCHES) == 25, "expected 25 launches"

# ============================================================================
# Workstream templates per product line
#   (name_zh, ws_type, team_id, hours/week, start_weeks_before, end_weeks_before)
# ============================================================================

LINE_WORKSTREAMS = {
    "Phone": [
        ("硬件设计",   "HW Design",     "T-PHONE-HW", 60, 24,  8),
        ("系统软件",   "SW Dev",        "T-PHONE-SW", 80, 20,  4),
        ("供应链准备", "Supply Chain",  "T-SUPPLY",   40, 16,  4),
        ("SDM 排产",   "Supply Chain",  "T-SDM",      30, 12,  2),
        ("CCC 认证",   "Certification", "T-LEGAL",    20, 10,  2),
        ("QA 测试",    "QA",            "T-QA",       40, 12,  1),
        ("营销与发布", "Marketing",     "T-MKT",      30,  8, -1),
        ("渠道铺货",   "Channel",       "T-CHANNEL",  25,  6,  0),
    ],
    "Laptop": [
        ("硬件设计",   "HW Design",     "T-PC-HW",    60, 26,  8),
        ("系统软件",   "SW Dev",        "T-PC-SW",    70, 22,  4),
        ("供应链准备", "Supply Chain",  "T-SUPPLY",   40, 18,  4),
        ("SDM 排产",   "Supply Chain",  "T-SDM",      30, 14,  2),
        ("FCC/CE 认证","Certification", "T-LEGAL",    20, 12,  2),
        ("QA 测试",    "QA",            "T-QA",       40, 14,  1),
        ("营销与发布", "Marketing",     "T-MKT",      30,  8, -1),
    ],
    "Tablet": [
        ("硬件设计",   "HW Design",     "T-PC-HW",    50, 22,  8),
        ("系统软件",   "SW Dev",        "T-PHONE-SW", 60, 18,  4),
        ("供应链准备", "Supply Chain",  "T-SUPPLY",   35, 14,  4),
        ("CCC 认证",   "Certification", "T-LEGAL",    20, 10,  2),
        ("QA 测试",    "QA",            "T-QA",       35, 10,  1),
        ("营销与发布", "Marketing",     "T-MKT",      25,  6, -1),
    ],
    "Wearable": [
        ("硬件设计",   "HW Design",     "T-IOT",      50, 20,  8),
        ("系统软件",   "SW Dev",        "T-IOT",      50, 16,  4),
        ("供应链准备", "Supply Chain",  "T-SUPPLY",   30, 12,  4),
        ("CCC 认证",   "Certification", "T-LEGAL",    15,  8,  2),
        ("QA 测试",    "QA",            "T-QA",       30, 10,  1),
        ("营销与发布", "Marketing",     "T-MKT",      20,  5, -1),
    ],
    "Audio": [
        ("硬件设计",   "HW Design",     "T-IOT",      45, 18,  6),
        ("音频调校",   "HW Design",     "T-IOT",      30, 12,  3),
        ("供应链准备", "Supply Chain",  "T-SUPPLY",   30, 10,  4),
        ("CCC 认证",   "Certification", "T-LEGAL",    15,  8,  2),
        ("QA 测试",    "QA",            "T-QA",       25,  8,  1),
        ("营销与发布", "Marketing",     "T-MKT",      20,  5, -1),
    ],
    "Display": [
        ("硬件设计",   "HW Design",     "T-PERI",     40, 18,  6),
        ("色彩调校",   "HW Design",     "T-PERI",     25, 12,  3),
        ("供应链准备", "Supply Chain",  "T-SUPPLY",   30, 12,  4),
        ("FCC/CE 认证","Certification", "T-LEGAL",    15,  8,  2),
        ("QA 测试",    "QA",            "T-QA",       25,  8,  1),
        ("营销与发布", "Marketing",     "T-MKT",      20,  5, -1),
    ],
    "Other": [
        ("硬件设计",   "HW Design",     "T-PERI",     30, 16,  6),
        ("供应链准备", "Supply Chain",  "T-SUPPLY",   25, 10,  4),
        ("QA 测试",    "QA",            "T-QA",       20,  8,  1),
        ("营销与发布", "Marketing",     "T-MKT",      15,  5, -1),
    ],
}

# ============================================================================
# Milestone templates per workstream type
#   (name_zh, ms_type, fraction (0..1), resource_id_or_None, required_capacity)
# ============================================================================

TYPE_MILESTONES = {
    "HW Design": [
        ("设计冻结", "design_review",  0.15, None,          0),
        ("EVT 样机", "EVT",            0.40, "R-SAMPLE-A", 20),
        ("DVT 样机", "DVT",            0.65, "R-SAMPLE-A", 30),
        ("PVT 样机", "PVT",            0.90, "R-SAMPLE-B", 40),
    ],
    "SW Dev": [
        ("Alpha",  "design_review",  0.30, None, 0),
        ("Beta",   "design_review",  0.60, None, 0),
        ("RC",     "design_review",  0.85, None, 0),
        ("GA",     "first_release",  0.98, None, 0),
    ],
    "Supply Chain": [
        ("BOM 冻结", "design_review",  0.10, None,        0),
        ("PP Build", "pp_build",       0.50, "R-PCB-A", 100),
        ("量产爬升",  "mass_production",0.80, "R-PCB-A", 200),
        ("交付准备",  "gtm_ready",      0.95, "R-PKG-S", 500),
    ],
    "Certification": [
        ("提交测试", "design_review", 0.30, None,           0),
        ("认证完成", "certification", 0.85, "R-CERT-CCC",   1),
    ],
    "QA": [
        ("测试用例",   "design_review", 0.20, None,        0),
        ("回归测试",   "design_review", 0.55, "R-TEST-RF", 2),
        ("可靠性验收", "design_review", 0.85, "R-RELY",    1),
    ],
    "Marketing": [
        ("Kickoff",   "kickoff",       0.10, None, 0),
        ("物料制作",   "design_review", 0.50, None, 0),
        ("GTM Ready", "gtm_ready",     0.95, None, 0),
    ],
    "Channel": [
        ("渠道培训", "design_review", 0.30, None,       0),
        ("首发上架", "first_release", 0.95, "R-LOG-S", 500),
    ],
}

# Certification resource pick-by-product-line
def cert_resource(line: str) -> str:
    return {
        "Phone": "R-CERT-CCC",
        "Tablet": "R-CERT-CCC",
        "Wearable": "R-CERT-CCC",
        "Audio": "R-CERT-CCC",
        "Laptop": "R-CERT-CE",
        "Display": "R-CERT-CE",
        "Other": "R-CERT-3C",
    }.get(line, "R-CERT-CCC")


# ============================================================================
# Generation
# ============================================================================

def generate():
    workstreams = []
    milestones = []
    dependencies = []

    # Pass 1: workstreams + milestones (within-WS deps queued in pass 2)
    for launch_id, launch_name, line, target_date, launch_status in LAUNCHES:
        for tmpl_idx, tmpl in enumerate(LINE_WORKSTREAMS[line]):
            name_zh, ws_type, team_id, hours, start_wk, end_wk = tmpl
            ws_short = ws_type.replace(" ", "").replace("/", "").upper()[:6]
            ws_id = f"WS-{launch_id[2:]}-{ws_short}-{tmpl_idx}"

            start_date = target_date - timedelta(weeks=start_wk)
            end_date = target_date - timedelta(weeks=end_wk)
            duration_days = (end_date - start_date).days
            ws_name = f"{launch_name} · {name_zh}"

            # Map launch.status to workstream.status
            if launch_status == "launched":
                ws_status = "done"
            elif launch_status == "at_risk":
                ws_status = random.choice(["at_risk", "in_progress", "in_progress"])
            else:
                # If WS already ended before TODAY: done; spans TODAY: in_progress; future: planned
                if end_date < TODAY:
                    ws_status = "done"
                elif start_date < TODAY:
                    ws_status = "in_progress"
                else:
                    ws_status = "planned"

            workstreams.append({
                "id": ws_id, "launch_id": launch_id, "name": ws_name,
                "type": ws_type, "team_id": team_id,
                "start": start_date, "end": end_date,
                "hours": hours, "status": ws_status,
            })

            # Milestones
            for ms_idx, ms_tmpl in enumerate(TYPE_MILESTONES.get(ws_type, [])):
                ms_name_zh, ms_type, frac, resource, capacity = ms_tmpl

                # Override resource for Certification by product line
                if ws_type == "Certification" and resource is None:
                    resource = cert_resource(line)
                if ws_type == "Certification" and ms_type == "certification":
                    resource = cert_resource(line)

                planned = start_date + timedelta(days=int(duration_days * frac))
                ms_id = f"MS-{ws_id[3:]}-{ms_idx}"

                actual = None
                if ws_status == "done" and planned <= TODAY:
                    actual = planned + timedelta(days=random.randint(-3, 5))
                elif ws_status == "in_progress" and planned < TODAY:
                    actual = planned + timedelta(days=random.randint(-2, 4))

                ms_status = "done" if actual else (
                    "in_progress" if (ws_status == "in_progress" and planned > TODAY and (planned - TODAY).days < 21)
                    else ("at_risk" if ws_status == "at_risk" else "planned")
                )
                is_critical = ms_type in ("pp_build", "mass_production", "certification", "first_release")

                milestones.append({
                    "id": ms_id, "workstream_id": ws_id,
                    "name": f"{launch_name} · {ms_name_zh}",
                    "type": ms_type, "planned": planned, "actual": actual,
                    "status": ms_status, "is_critical": is_critical,
                    "resource": resource, "capacity": capacity,
                    "launch_id": launch_id,  # bookkeeping for cross-launch deps
                })

    # Pass 2: dependencies
    # 2a. Within-workstream sequential deps
    by_ws: dict[str, list[dict]] = {}
    for m in milestones:
        by_ws.setdefault(m["workstream_id"], []).append(m)
    for ws_id, ms_list in by_ws.items():
        ms_list.sort(key=lambda x: x["planned"])
        for a, b in zip(ms_list, ms_list[1:]):
            dependencies.append({
                "id": f"DEP-{a['id'][3:]}-{b['id'][3:]}",
                "from": a["id"], "to": b["id"],
                "type": "blocks",
                "lead_days": (b["planned"] - a["planned"]).days,
                "cross_launch": False,
                "notes": "within-workstream sequential",
            })

    # 2b. Cross-workstream same-launch deps (standard release shape)
    # Index by (launch_id, ws_type, milestone_type)
    idx: dict[tuple, dict] = {}
    for m in milestones:
        ws = next(w for w in workstreams if w["id"] == m["workstream_id"])
        idx[(ws["launch_id"], ws["type"], m["type"])] = m

    cross_ws_rules = [
        # (from_ws_type, from_ms_type, to_ws_type, to_ms_type, dep_type, lead_days)
        ("HW Design",    "PVT",             "Supply Chain",  "pp_build",        "blocks", 7),
        ("HW Design",    "DVT",             "QA",            "design_review",   "informs", 14),
        ("SW Dev",       "Beta",            "QA",            "design_review",   "blocks", 5),
        ("Supply Chain", "pp_build",        "Certification", "design_review",   "blocks", 3),
        ("Certification","certification",   "Channel",       "first_release",   "blocks", 14),
        ("Supply Chain", "mass_production", "Channel",       "first_release",   "blocks", 10),
        ("QA",           "design_review",   "Marketing",     "gtm_ready",       "informs", 7),
        ("Marketing",    "gtm_ready",       "Channel",       "first_release",   "blocks", 3),
    ]
    for launch_id, *_ in LAUNCHES:
        for from_ws, from_ms, to_ws, to_ms, dtype, lead in cross_ws_rules:
            a = idx.get((launch_id, from_ws, from_ms))
            b = idx.get((launch_id, to_ws, to_ms))
            if a and b and a["id"] != b["id"]:
                dependencies.append({
                    "id": f"DEP-X-{a['id'][3:]}-{b['id'][3:]}",
                    "from": a["id"], "to": b["id"],
                    "type": dtype, "lead_days": lead,
                    "cross_launch": False,
                    "notes": f"cross-WS within {launch_id}",
                })

    # 2c. Cross-launch shared-resource deps (the killer ones)
    # Group milestones by required_resource_id + planned-week bucket.
    # If two milestones land in the same week on the same resource, add a
    # "shares_resource" dep from the earlier one to the later one.
    from collections import defaultdict
    res_buckets: dict[tuple, list[dict]] = defaultdict(list)
    for m in milestones:
        if m["resource"] is None:
            continue
        week_key = m["planned"].isocalendar()[:2]  # (year, week)
        res_buckets[(m["resource"], week_key)].append(m)

    for (resource, week), ms_in_week in res_buckets.items():
        if len(ms_in_week) < 2:
            continue
        ms_in_week.sort(key=lambda x: x["planned"])
        # only link earliest to others to keep dep count reasonable
        first = ms_in_week[0]
        for other in ms_in_week[1:]:
            if first["launch_id"] == other["launch_id"]:
                continue  # same launch handled above
            dependencies.append({
                "id": f"DEP-R-{first['id'][3:]}-{other['id'][3:]}"[:38],
                "from": first["id"], "to": other["id"],
                "type": "shares_resource",
                "lead_days": (other["planned"] - first["planned"]).days,
                "cross_launch": True,
                "notes": f"shares {resource} in W{week[1]}",
            })

    return workstreams, milestones, dependencies


# ============================================================================
# SQL emission
# ============================================================================

def emit_sql(workstreams, milestones, dependencies):
    def esc(v):
        if v is None:
            return "NULL"
        if isinstance(v, (int, float)):
            return str(v)
        if isinstance(v, bool):
            return "TRUE" if v else "FALSE"
        if isinstance(v, date):
            return f"'{v.isoformat()}'"
        s = str(v).replace("'", "''")
        return f"'{s}'"

    lines = [
        "-- Auto-generated by scripts/demo-seed/generate.py · DO NOT EDIT MANUALLY",
        "-- Re-run: python3 scripts/demo-seed/generate.py",
        f"-- Determinism: random.seed(42), TODAY={TODAY.isoformat()}",
        f"-- Generated counts: {len(workstreams)} WS, {len(milestones)} MS, {len(dependencies)} DEP",
        "",
    ]

    # Workstreams
    lines.append("-- ==================== demo_workstream ====================")
    lines.append("INSERT INTO demo_workstream (workstream_id, launch_id, name, workstream_type, owner_team_id, start_date, end_date, status, weekly_hours_required) VALUES")
    ws_rows = [
        f"  ({esc(w['id'])}, {esc(w['launch_id'])}, {esc(w['name'])}, {esc(w['type'])}, "
        f"{esc(w['team_id'])}, {esc(w['start'])}, {esc(w['end'])}, {esc(w['status'])}, {esc(w['hours'])})"
        for w in workstreams
    ]
    lines.append(",\n".join(ws_rows) + "\nON CONFLICT (workstream_id) DO NOTHING;\n")

    # Milestones — chunked into batches of 100 to avoid mega-statements
    lines.append("-- ==================== demo_milestone ====================")
    BATCH = 100
    for i in range(0, len(milestones), BATCH):
        batch = milestones[i:i + BATCH]
        lines.append("INSERT INTO demo_milestone (milestone_id, workstream_id, name, milestone_type, planned_date, actual_date, status, is_critical, required_resource_id, required_capacity) VALUES")
        rows = [
            f"  ({esc(m['id'])}, {esc(m['workstream_id'])}, {esc(m['name'])}, {esc(m['type'])}, "
            f"{esc(m['planned'])}, {esc(m['actual'])}, {esc(m['status'])}, {esc(m['is_critical'])}, "
            f"{esc(m['resource'])}, {esc(m['capacity'])})"
            for m in batch
        ]
        lines.append(",\n".join(rows) + "\nON CONFLICT (milestone_id) DO NOTHING;\n")

    # Dependencies — chunked
    lines.append("-- ==================== demo_dependency ====================")
    for i in range(0, len(dependencies), BATCH):
        batch = dependencies[i:i + BATCH]
        lines.append("INSERT INTO demo_dependency (dependency_id, from_milestone_id, to_milestone_id, dependency_type, lead_time_days, cross_launch, notes) VALUES")
        rows = [
            f"  ({esc(d['id'])}, {esc(d['from'])}, {esc(d['to'])}, {esc(d['type'])}, "
            f"{esc(d['lead_days'])}, {esc(d['cross_launch'])}, {esc(d['notes'])})"
            for d in batch
        ]
        lines.append(",\n".join(rows) + "\nON CONFLICT (dependency_id) DO NOTHING;\n")

    # Counts / sanity
    lines.append("-- ==================== Sanity checks ====================")
    lines.append("DO $$")
    lines.append("DECLARE cnt INTEGER;")
    lines.append("BEGIN")
    lines.append(f"  SELECT COUNT(*) INTO cnt FROM demo_workstream;")
    lines.append(f"  ASSERT cnt >= {len(workstreams)}, format('demo_workstream got %s', cnt);")
    lines.append(f"  SELECT COUNT(*) INTO cnt FROM demo_milestone;")
    lines.append(f"  ASSERT cnt >= {len(milestones)}, format('demo_milestone got %s', cnt);")
    lines.append(f"  SELECT COUNT(*) INTO cnt FROM demo_dependency;")
    lines.append(f"  ASSERT cnt >= {len(dependencies)}, format('demo_dependency got %s', cnt);")
    lines.append(f"  RAISE NOTICE 'demo-seed generated: {len(workstreams)} WS, {len(milestones)} MS, {len(dependencies)} DEP loaded OK';")
    lines.append("END $$;")

    return "\n".join(lines) + "\n"


def main():
    ws, ms, deps = generate()
    sql = emit_sql(ws, ms, deps)
    OUTPUT.write_text(sql, encoding="utf-8")
    print(f"Wrote {OUTPUT.relative_to(Path.cwd()) if OUTPUT.is_absolute() else OUTPUT}:")
    print(f"  {len(ws)} workstreams")
    print(f"  {len(ms)} milestones")
    print(f"  {len(deps)} dependencies")
    cross = sum(1 for d in deps if d["cross_launch"])
    print(f"  {cross} cross-launch deps (the multi-Launch tension you want hero question to surface)")


if __name__ == "__main__":
    main()
