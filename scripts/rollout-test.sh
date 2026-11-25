#!/usr/bin/env bash
# Sends steady load through the gateway while it performs a rolling update, and
# fails if any request does not return 200.
#
# Requires: hey (go install github.com/rakyll/hey@latest), kubectl, and a
# running cluster from `make up`.
set -euo pipefail

CTX="${CTX:-kind-edge-gateway}"
NS="${NS:-edge}"
URL="${URL:-http://localhost:8080/api/users/rollout-test}"
DURATION="${DURATION:-60}"        # seconds of load
WORKERS="${WORKERS:-10}"
QPS_PER_WORKER="${QPS_PER_WORKER:-10}"
DEPLOYMENT="${DEPLOYMENT:-gateway}"

command -v hey >/dev/null || { echo "hey not found: go install github.com/rakyll/hey@latest" >&2; exit 2; }

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

echo "==> Checking the gateway answers before starting"
curl -fsS -o /dev/null "$URL" || { echo "gateway is not serving $URL" >&2; exit 2; }

echo "==> Starting load: ${WORKERS} workers x ${QPS_PER_WORKER} qps for ${DURATION}s against $URL"
hey -z "${DURATION}s" -c "$WORKERS" -q "$QPS_PER_WORKER" "$URL" >"$out" 2>&1 &
hey_pid=$!

sleep 5
echo "==> Rolling update: restarting deployment/${DEPLOYMENT}"
started=$(date +%s)
kubectl --context "$CTX" -n "$NS" rollout restart "deployment/${DEPLOYMENT}"
kubectl --context "$CTX" -n "$NS" rollout status "deployment/${DEPLOYMENT}" --timeout=180s
echo "==> Rollout finished in $(( $(date +%s) - started ))s"

wait "$hey_pid"
echo "==> Load finished"
sed -n '/Summary:/,$p' "$out"

# hey lists every status code it saw, and any transport errors separately.
failed=0
if grep -q "Error distribution" "$out"; then
  echo "FAIL: transport errors during rollout" >&2
  failed=1
fi
if grep -E '^\s+\[[0-9]+\]' "$out" | grep -vE '^\s+\[200\]' >/dev/null; then
  echo "FAIL: non-200 responses during rollout" >&2
  failed=1
fi
if [ "$failed" -eq 0 ]; then
  echo "PASS: zero failed requests during the rolling update"
fi
exit "$failed"
