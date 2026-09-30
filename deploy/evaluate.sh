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

log "pausing the clock at $start 00:00"
post "{\"running\": false}"
post "{\"date\": \"$start\", \"time\": \"00:00\", \"factor\": $factor}"

log "starting the stack with POLICY=$policy"
t0=$(date -u +%s)
RUN_ID=$run_id POLICY=$policy ORACLE_RUN=$oracle_run docker compose up -d >/dev/null

# Wait until every device has checked in since the recreate.
want=$(($(wc -l < deploy/devices.csv) - 1))
for _ in $(seq 120); do
  seen=$(curl -sf "http://127.0.0.1:8070/devices?status=active" |
         T0=$t0 python3 -c "import os,sys,json,datetime as d
t0=int(os.environ['T0'])
print(sum(1 for x in json.load(sys.stdin) if x['last_seen'] and d.datetime.fromisoformat(x['last_seen'].replace('Z','+00:00')).timestamp()>=t0))" || echo 0)
  [ "$seen" -ge "$want" ] && break
  sleep 2
done
log "$seen of $want devices checked in; running until $end 00:00 at factor $factor"
sleep 10 # let the controller read the registry and the model
post "{\"running\": true}"

while :; do
  now=$(curl -sf http://127.0.0.1:8081/api/state | python3 -c "import sys,json; print(json.load(sys.stdin)['sim']['clock']['date'])" || echo "")
  [[ -n $now && ! $now < $end ]] && break
  sleep 20
done
post "{\"running\": false}"
log "done at $now; storage: $(curl -sf $storage/healthz)"
