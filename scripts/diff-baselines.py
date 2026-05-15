#!/usr/bin/env python3
"""scripts/diff-baselines.py — OQ-10 / ITER-11.

Compare two baseline captures (AM + PM) produced by capture-baseline.sh and
emit regression-fixtures/model-drift-floor.json. Runs pairwise per prompt:

  - narrative drift: 1 - Jaccard(5-char shingles over narrative text).
    Tracks the floor of LLM vendor within-day non-determinism. NOT cosine yet:
    OQ-10 spec asks for cosine via bge-large-zh embeddings. Current
    implementation uses character-shingle Jaccard as a dependency-free
    placeholder that ranks drift the same way (within-run variance vs cross-
    run breakage). Upgrade to cosine once an embedding endpoint is exposed
    (see regression-fixtures/README.md "Planned refinements").

  - structured drift: exact-equality count of function call shape (name +
    top-level arg keys). OQ-10: MUST be 0 for cutover. Any non-zero hit
    blocks cutover pending investigation.

Inputs:  regression-fixtures/baseline-AM.json, baseline-PM.json
Output:  regression-fixtures/model-drift-floor.json
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


def shingles(text: str, n: int = 5) -> set[str]:
    text = (text or '').strip()
    if len(text) < n:
        return {text} if text else set()
    return {text[i:i + n] for i in range(len(text) - n + 1)}


def jaccard_distance(a: str, b: str) -> float:
    sa, sb = shingles(a), shingles(b)
    if not sa and not sb:
        return 0.0
    inter = len(sa & sb)
    union = len(sa | sb)
    if union == 0:
        return 0.0
    return 1.0 - (inter / union)


def structured_shape(fn_calls: list[dict]) -> list[tuple]:
    """Shape = (tool name, sorted tuple of top-level arg keys, sorted tuple of result keys).
    Values (rows, SQL strings, pivots) change per-invocation; only shape changes between AM
    and PM within-day block cutover per OQ-10.
    """
    shape = []
    for fc in fn_calls:
        name = fc.get('name') or '?'
        args_keys = tuple(fc.get('argumentKeys') or [])
        result_keys = tuple(fc.get('resultKeys') or [])
        shape.append((name, args_keys, result_keys))
    return shape


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--am', default='regression-fixtures/baseline-AM.json')
    ap.add_argument('--pm', default='regression-fixtures/baseline-PM.json')
    ap.add_argument('--out', default='regression-fixtures/model-drift-floor.json')
    args = ap.parse_args()

    am_path, pm_path, out_path = Path(args.am), Path(args.pm), Path(args.out)
    if not am_path.exists():
        print(f'ERROR: AM baseline missing: {am_path}', file=sys.stderr)
        return 2
    if not pm_path.exists():
        print(f'ERROR: PM baseline missing: {pm_path}', file=sys.stderr)
        return 2

    am = json.loads(am_path.read_text())
    pm = json.loads(pm_path.read_text())

    am_by_id = {r['id']: r for r in am.get('results', [])}
    pm_by_id = {r['id']: r for r in pm.get('results', [])}
    all_ids = sorted(set(am_by_id) | set(pm_by_id))

    per_prompt = []
    narrative_drift_values: list[float] = []
    structured_diff_total = 0

    for pid in all_ids:
        ar = am_by_id.get(pid, {})
        pr = pm_by_id.get(pid, {})
        a_narr = ar.get('narrative', '')
        p_narr = pr.get('narrative', '')
        a_shape = structured_shape(ar.get('structured', {}).get('functionCalls', []))
        p_shape = structured_shape(pr.get('structured', {}).get('functionCalls', []))

        narr_drift = jaccard_distance(a_narr, p_narr)
        struct_diff = 0 if a_shape == p_shape else abs(len(a_shape) - len(p_shape)) + sum(
            1 for i, j in zip(a_shape, p_shape) if i != j)

        narrative_drift_values.append(narr_drift)
        structured_diff_total += struct_diff

        per_prompt.append({
            'id': pid,
            'narrativeDriftJaccard': round(narr_drift, 4),
            'amNarrativeLen': len(a_narr),
            'pmNarrativeLen': len(p_narr),
            'structuredDiffCount': struct_diff,
            'amFunctionCallShapes': [f'{n}({",".join(ak)})→({",".join(rk)})' for n, ak, rk in a_shape],
            'pmFunctionCallShapes': [f'{n}({",".join(ak)})→({",".join(rk)})' for n, ak, rk in p_shape],
        })

    floor = {
        'schemaVersion': 1,
        'methodology': 'jaccard-5char-shingle-placeholder; cosine upgrade pending',
        'amCapturedAt': am.get('capturedAt'),
        'pmCapturedAt': pm.get('capturedAt'),
        'amGitCommit': am.get('gitCommit'),
        'pmGitCommit': pm.get('gitCommit'),
        'promptCount': len(all_ids),
        'narrativeDriftFloor': {
            'max': round(max(narrative_drift_values or [0.0]), 4),
            'mean': round(sum(narrative_drift_values) / max(len(narrative_drift_values), 1), 4),
            'values': [round(v, 4) for v in narrative_drift_values],
        },
        'structuredDiffTotal': structured_diff_total,
        'cutoverGate': {
            'structuredDiffMustBeZero': structured_diff_total == 0,
            'note': 'OQ-10: any structured diff blocks cutover pending investigation.',
        },
        'perPrompt': per_prompt,
    }

    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(floor, ensure_ascii=False, indent=2))
    print(f'▼// wrote {out_path}')
    print(f'   narrativeDriftFloor max={floor["narrativeDriftFloor"]["max"]} mean={floor["narrativeDriftFloor"]["mean"]}')
    print(f'   structuredDiffTotal={structured_diff_total} (0 = cutover-safe)')
    return 0


if __name__ == '__main__':
    sys.exit(main())
