#!/usr/bin/env bash
# Runs one policy over a fixed span of simulated days, as its own run.
#
#   deploy/evaluate.sh RUN_ID POLICY START_DATE DAYS FACTOR [ORACLE_RUN]
#   deploy/evaluate.sh eval-reactive reactive 2026-09-21 5 120
#
# Everything except the policy is identical between runs (FR-9): the same
# rooms and devices, the same dates (occupancysim generates each day from its
# seed and the date), and physics starts every run from fresh rooms.
set -euo pipefail

run_id=$1 policy=$2 start=$3 days=$4 factor=$5 oracle_run=${6:-}
clock=http://127.0.0.1:8081/api/clock
storage=http://127.0.0.1:8090

cd "$(dirname "$0")/.."
end=$(date -u -d "$start + $days days" +%F)

post() { curl -sf -X POST "$clock" -H 'Content-Type: application/json' -d "$1" >/dev/null; }
log() { echo "$(date +%T) [$run_id] $*"; }

deploy/start-run.sh "$run_id" "$policy" "$start" 00:00 "$factor" "$oracle_run"
log "running until $end 00:00 at factor $factor"
post "{\"running\": true}"

while :; do
  now=$(curl -sf http://127.0.0.1:8081/api/state | python3 -c "import sys,json; print(json.load(sys.stdin)['sim']['clock']['date'])" || echo "")
  [[ -n $now && ! $now < $end ]] && break
  sleep 20
done
post "{\"running\": false}"
log "done at $now; storage: $(curl -sf $storage/healthz)"
