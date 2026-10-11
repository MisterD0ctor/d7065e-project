#!/usr/bin/env bash
# Starts a run: the whole stack under a run id, with the model clock paused at
# a given moment. evaluate.sh and fault-tests.sh use it, and start the clock
# themselves.
#
#   deploy/start-run.sh RUN_ID POLICY DATE TIME FACTOR [ORACLE_RUN]
#   deploy/start-run.sh eval-reactive reactive 2026-09-21 00:00 120
#
# The order is the point. BuildSim keeps what was last written to it and is
# not restarted between runs, so the previous run's last minute is still on
# the building's clock there. A device that started now would hear that time,
# report once with it under the new run id, and a recorder still tagged with
# the old run would file the new start time under the old one. So:
#
#   1. Stop everything of ours that reports or records. Nothing speaks while
#      the clock moves.
#   2. Restart the broker, so nothing queued from the old run is delivered.
#   3. Pause the model clock and set it.
#   4. Start the physical model alone and wait until the building's clock in
#      BuildSim shows the start time.
#   5. Start everything else under the new run id, and wait until every
#      device has checked in.
set -euo pipefail

run_id=$1 policy=$2 date=$3 time=$4 factor=$5 oracle_run=${6:-}
clock=http://127.0.0.1:8081/api/clock

cd "$(dirname "$0")/.."

post() { curl -sf -X POST "$clock" -H 'Content-Type: application/json' -d "$1" >/dev/null; }
log() { echo "$(date +%T) [$run_id] $*"; }

# BuildSim and occupancysim are the building and stay up. The registry holds
# the installation, reports nothing, and keeps BuildSim's equipment in place.
mapfile -t ours < <(docker compose config --services | grep -vx -e buildsim -e occupancysim -e mosquitto -e registry)
docker compose stop "${ours[@]}" >/dev/null 2>&1 || true
docker compose up -d buildsim occupancysim registry >/dev/null 2>&1
docker compose restart mosquitto >/dev/null 2>&1

log "model clock paused at $date $time, factor $factor"
for _ in $(seq 30); do post '{"running": false}' && break; sleep 2; done
post "{\"date\": \"$date\", \"time\": \"$time\", \"factor\": $factor}"

docker compose up -d physics >/dev/null 2>&1
clock_sensor="$(awk -F, '$2 == "clock" { print $1; exit }' deploy/devices.csv)-reading"
want="${date}T${time}:00Z"
shown=
for _ in $(seq 60); do
  shown=$(curl -sf "http://127.0.0.1:9090/api/sensors/$clock_sensor" |
          python3 -c "import sys,json; print(json.loads(json.load(sys.stdin)['value'])['model_time'])" 2>/dev/null || true)
  [ "$shown" = "$want" ] && break
  sleep 1
done
if [ "$shown" != "$want" ]; then
  echo "the building's clock shows '${shown:-nothing}', not $want: is $clock_sensor registered, and physics running?" >&2
  exit 1
fi

log "the building's clock shows $want; starting the stack with POLICY=$policy"
t0=$(date -u +%s)
RUN_ID=$run_id POLICY=$policy ORACLE_RUN=$oracle_run docker compose up -d >/dev/null 2>&1

# Wait until every device has checked in since the start.
want_devices=$(($(wc -l < deploy/devices.csv) - 1))
seen=0
for _ in $(seq 120); do
  seen=$(curl -sf "http://127.0.0.1:8070/devices?status=active" |
         T0=$t0 python3 -c "import os,sys,json,datetime as d
t0=int(os.environ['T0'])
print(sum(1 for x in json.load(sys.stdin) if x.get('last_seen') and d.datetime.fromisoformat(x['last_seen'].replace('Z','+00:00')).timestamp()>=t0))" || echo 0)
  [ "$seen" -ge "$want_devices" ] && break
  sleep 2
done
log "$seen of $want_devices devices checked in"
sleep 10 # let the controller read the registry and the model
