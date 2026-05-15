#!/usr/bin/env bash
# scripts/capture-baseline.sh — Phase 0 regression baseline capture (ITER-11 / OQ-10).
#
# Runs the golden prompt set through the monolith lakehouse-agent SSE endpoint
# and writes the (narrative_text, structured_output) tuple for each prompt into
# a single JSON file. Run twice per day (AM + PM) to establish the within-day
# LLM vendor drift floor per OQ-10. AM + PM outputs diff via
# scripts/diff-baselines.py → regression-fixtures/model-drift-floor.json.
#
# Required env:
#   HOST       default http://127.0.0.1:18091 (enterprise monolith)
#   TOKEN      Bearer token; default admin (bearer-a0000000-...-001)
#   LABEL      run label: AM / PM / custom; default AM
#   OUT_FILE   default regression-fixtures/baseline-$LABEL.json
#   TIMEOUT    per-prompt SSE max-time seconds; default 90
#
# Usage:
#   LABEL=AM bash scripts/capture-baseline.sh
#   LABEL=PM bash scripts/capture-baseline.sh

set -euo pipefail
cd "$(dirname "$0")/.."

HOST="${HOST:-http://127.0.0.1:18091}"
TOKEN="${TOKEN:-bearer-a0000000-0000-0000-0000-000000000001}"
LABEL="${LABEL:-AM}"
OUT_FILE="${OUT_FILE:-regression-fixtures/baseline-${LABEL}.json}"
TIMEOUT="${TIMEOUT:-90}"
PROMPTS_FILE="${PROMPTS_FILE:-regression-fixtures/golden-prompts.json}"

mkdir -p "$(dirname "$OUT_FILE")"

if [[ ! -f "$PROMPTS_FILE" ]]; then
  echo "ERROR: prompts file not found: $PROMPTS_FILE" >&2
  exit 1
fi

GIT_COMMIT=$(git rev-parse HEAD 2>/dev/null || echo "unknown")
CAPTURED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

echo "▼// BASELINE CAPTURE: label=$LABEL host=$HOST out=$OUT_FILE"
echo "   prompts=$PROMPTS_FILE commit=${GIT_COMMIT:0:12} capturedAt=$CAPTURED_AT"

PROJECT_ID=$(python3 -c "import json; print(json.load(open('$PROMPTS_FILE'))['projectId'])")
PROMPT_COUNT=$(python3 -c "import json; print(len(json.load(open('$PROMPTS_FILE'))['prompts']))")
echo "   project=$PROJECT_ID prompts=$PROMPT_COUNT timeout=${TIMEOUT}s"

# Iterate each prompt, capture raw SSE into $TMP_DIR/$ID.sse
python3 -c "
import json
p = json.load(open('$PROMPTS_FILE'))
for x in p['prompts']:
    print(x['id'] + '\t' + x['prompt'])
" | while IFS=$'\t' read -r PID PROMPT; do
  echo "   · capturing $PID: $PROMPT"
  BODY=$(python3 -c "import json; print(json.dumps({'projectId':'$PROJECT_ID','messages':[{'role':'user','content':'''$PROMPT'''}]}))")
  curl -s --max-time "$TIMEOUT" -N -X POST "$HOST/api/ontology/lakehouse-agent-stream" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "$BODY" > "$TMP_DIR/$PID.sse" || true
  BYTES=$(wc -c < "$TMP_DIR/$PID.sse")
  echo "     ← $BYTES bytes"
done

# Aggregate SSE dumps into baseline JSON (parse narrative + structured per prompt).
python3 - "$OUT_FILE" "$LABEL" "$CAPTURED_AT" "$GIT_COMMIT" "$PROMPTS_FILE" "$TMP_DIR" <<'PY'
import json, os, re, sys

out_file, label, captured_at, git_commit, prompts_file, tmp_dir = sys.argv[1:]

prompts = json.load(open(prompts_file))
results = []

for p in prompts['prompts']:
    sse_path = os.path.join(tmp_dir, p['id'] + '.sse')
    events = []
    narrative_chunks = []
    thinking_chunks = []
    function_calls = []
    thread_id = None
    done_event = None
    error = None

    if not os.path.exists(sse_path):
        results.append({'id': p['id'], 'prompt': p['prompt'], 'error': 'sse file missing'})
        continue

    with open(sse_path, 'r', errors='replace') as f:
        for line in f:
            line = line.strip()
            if not line.startswith('data:'):
                continue
            payload = line[len('data:'):].strip()
            try:
                ev = json.loads(payload)
            except json.JSONDecodeError:
                continue
            events.append(ev)
            etype = ev.get('type', '')
            if etype == 'thread':
                thread_id = ev.get('threadId')
            elif etype == 'token':
                narrative_chunks.append(ev.get('content', ''))
            elif etype == 'thinking':
                thinking_chunks.append(ev.get('content', ''))
            elif etype == 'function_call':
                # Shape: {"type":"function_call","name":"smartquery|lookup|...",
                #        "arguments":{...},"result":{...}}. Only retain name +
                #        top-level argument keys for the structured-diff gate —
                #        result bodies change per-row and would dominate the
                #        jaccard signal.
                function_calls.append({
                    'name': ev.get('name'),
                    'argumentKeys': sorted(list((ev.get('arguments') or {}).keys())),
                    'resultKeys': sorted(list((ev.get('result') or {}).keys())),
                })
            elif etype == 'done':
                done_event = ev
            elif etype == 'error':
                error = ev.get('content')

    narrative = ''.join(narrative_chunks)
    thinking = ''.join(thinking_chunks)

    results.append({
        'id': p['id'],
        'prompt': p['prompt'],
        'threadId': thread_id,
        'narrative': narrative,
        'narrativeLen': len(narrative),
        'thinkingLen': len(thinking),
        'structured': {
            'functionCallCount': len(function_calls),
            'functionCalls': function_calls,
        },
        'sseEventCount': len(events),
        'doneEvent': done_event,
        'error': error,
    })

payload = {
    'schemaVersion': 1,
    'label': label,
    'capturedAt': captured_at,
    'gitCommit': git_commit,
    'promptsFile': prompts_file,
    'results': results,
}

with open(out_file, 'w') as f:
    json.dump(payload, f, ensure_ascii=False, indent=2)

print(f"   ▼// wrote {out_file} — {len(results)} prompts captured")
for r in results:
    err = f" ERROR={r['error']}" if r.get('error') else ""
    print(f"     {r['id']}: narrative={r.get('narrativeLen',0)}B fnCalls={r.get('structured',{}).get('functionCallCount',0)}{err}")
PY
