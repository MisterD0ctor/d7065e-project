#!/usr/bin/env bash
# The sensor-fault tests of the test plan in one run: a stuck temperature
# sensor and a stuck CO₂ sensor (T-36), a silent CO₂ sensor (T-13) and an
# occupancy counter stuck during a lecture (T-12).
#
#   deploy/fault-tests.sh fault-tests 2>&1 | tee results/fault-tests.log
#
# It simulates Monday 2026-09-21 from 07:30 to 12:50 at factor 60 under the
# predictive policy, about ten minutes with the start-up. Each fault goes in
# at a fixed model time with the clock paused, and occupancysim generates the
# same day every time (NFR-7), so the run can be repeated: the lecture in
# A1519 (53 people, 10:07 to 12:00) is always there.
#
# The log lists every change of the dashboard's alerts with its model time.
# The decisions are in storage under the run id.
set -euo pipefail

run_id=${1:?usage: deploy/fault-tests.sh RUN_ID}
clock=http://127.0.0.1:8081/api/clock

cd "$(dirname "$0")/.."

post() { curl -sf -X POST "$clock" -H 'Content-Type: application/json' -d "$1" >/dev/null; }
now() { curl -sf http://127.0.0.1:8081/api/state | python3 -c "import sys,json; print(json.load(sys.stdin)['sim']['clock']['time_of_day'])"; }
log() { echo "$(now) [$run_id] $*"; }
wait_until() { while [[ $(now) < $1 ]]; do sleep 0.2; done; } # model time, hh:mm
device() { awk -F, -v kind="$1" -v room="$2" '$2 == kind && $3 == room { print $1 }' deploy/devices.csv; }

# Runs a command with the clock paused. Recreating a sensor takes a few
# seconds, which is as many model-minutes at factor 60: without the pause the
# fault would land late, and a repaired sensor would first show up as silent.
paused() {
  post '{"running": false}'
  "$@" >/dev/null 2>&1
  sleep 6 # the sensor checks in and takes its first reading
  post '{"running": true}'
}

# Prints the dashboard's alerts whenever one appears or goes. (Their messages
# count minutes and readings, so those change all the time.)
watch_alerts() {
  python3 -u - <<'EOF'
import json, time, urllib.request

def get(url):
    try:
        with urllib.request.urlopen(url, timeout=3) as r:
            return True, json.load(r)
    except Exception:
        return False, None

last = None
while True:
    ok1, state = get("http://127.0.0.1:8081/api/state")
    ok2, alerts = get("http://127.0.0.1:9090/api/alerts")
    if ok1 and ok2:
        alerts = sorted(alerts or [], key=lambda a: a["id"])
        titles = [a["title"] for a in alerts]
        if titles != last:
            now = state["sim"]["clock"]["time_of_day"]
            for a in alerts:
                print(f"{now}   alert  {a['severity']}: {a['title']}. {a.get('message', '')}", flush=True)
            if not alerts:
                print(f"{now}   alert  none", flush=True)
            last = titles
    time.sleep(0.5)
EOF
}

temp=$(device temp level0/1570)
co2=$(device co2 level0/1570)
silent=$(device co2 level0/1543)
counter=$(device occupancy level0/A1519)

echo "$(date +%T) [$run_id] starting the stack at Monday 07:30, POLICY=predictive"
# As in evaluate.sh: stop physics before the clock moves, so nothing is
# recorded at the new start time under the previous run id.
docker compose stop physics >/dev/null 2>&1 || true
post '{"running": false}'
post '{"date": "2026-09-21", "time": "07:30", "factor": 60}'
t0=$(date -u +%s)
RUN_ID=$run_id POLICY=predictive ORACLE_RUN= docker compose up -d >/dev/null 2>&1

want=$(($(wc -l < deploy/devices.csv) - 1))
for _ in $(seq 120); do
  seen=$(curl -sf "http://127.0.0.1:8070/devices?status=active" |
         T0=$t0 python3 -c "import os,sys,json,datetime as d
t0=int(os.environ['T0'])
print(sum(1 for x in json.load(sys.stdin) if x['last_seen'] and d.datetime.fromisoformat(x['last_seen'].replace('Z','+00:00')).timestamp()>=t0))" || echo 0)
  [ "$seen" -ge "$want" ] && break
  sleep 2
done
echo "$(date +%T) [$run_id] $seen of $want devices checked in"
sleep 10 # let the controller read the registry and the model

watch_alerts &
watcher=$!
trap 'kill $watcher 2>/dev/null || true' EXIT
post '{"running": true}'

wait_until 07:45
log "T-36: freezing $temp (temperature, room 1570)"
paused deploy/fault.sh "$temp" stuck
wait_until 08:15
log "T-36: repairing $temp"
paused deploy/fault.sh "$temp"

wait_until 08:30
log "T-13: stopping $silent (CO₂, room 1543)"
docker compose stop "$silent" >/dev/null 2>&1
wait_until 08:50
log "T-13: starting $silent again"
paused docker compose start "$silent"

wait_until 09:30
log "T-36: freezing $co2 (CO₂, room 1570; people come at 09:40)"
paused deploy/fault.sh "$co2" stuck
wait_until 10:20
log "T-36: repairing $co2"
paused deploy/fault.sh "$co2"

wait_until 11:00
log "T-12: freezing $counter (counter, room A1519, 53 people until 12:00)"
paused deploy/fault.sh "$counter" stuck
wait_until 12:40
log "T-12: repairing $counter"
paused deploy/fault.sh "$counter"

wait_until 12:50
post '{"running": false}'
sleep 2
log "done; the clock is paused. Decisions: curl 'localhost:8090/history?run=$run_id&kind=decision'"
