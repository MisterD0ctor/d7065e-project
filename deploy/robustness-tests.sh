#!/usr/bin/env bash
# The robustness tests of the test plan in one run: the CO₂ override with
# the controller killed (T-09, T-10), BuildSim restarted blank (T-17), each
# of our services killed in turn (T-23), and the simulation speed raised
# until readings are missed (T-22).
#
#   deploy/robustness-tests.sh robustness 2>&1 | tee results/robustness.log
#
# It simulates Monday 2026-09-21 from 08:00 at factor 60 under the predictive
# policy, about ten minutes. Command latency (T-21) is read from the same
# run's records afterwards.
set -euo pipefail

run_id=${1:?usage: deploy/robustness-tests.sh RUN_ID}
clock=http://127.0.0.1:8081/api/clock
cd "$(dirname "$0")/.."

post() { curl -sf -X POST "$clock" -H 'Content-Type: application/json' -d "$1" >/dev/null; }
now() { curl -sf http://127.0.0.1:8081/api/state | python3 -c "import sys,json; print(json.load(sys.stdin)['sim']['clock']['time_of_day'])"; }
log() { echo "$(now) [$run_id] $*"; }
wait_until() { while [[ $(now) < $1 ]]; do sleep 0.2; done; }
device() { awk -F, -v kind="$1" -v room="$2" '$2 == kind && $3 == room { print $1 }' deploy/devices.csv; }
airflow() { curl -sf "http://127.0.0.1:9090/api/actuators/$1-state" | python3 -c "import sys,json; print(json.load(sys.stdin)['state'])"; }
reading() { curl -sf "http://127.0.0.1:9090/api/sensors/$1-reading" | python3 -c "import sys,json; print(json.load(sys.stdin)['value'])"; }
events() { curl -sf "http://127.0.0.1:8090/history?run=$run_id&kind=event&room=$1" | python3 -c "import sys,json; print(', '.join(f\"{json.loads(l)['model_time'][11:16]} {json.loads(l)['kind']}\" for l in sys.stdin) or 'none')"; }

# healthy reports how many of the 50 rooms the dashboard has a fresh CO₂
# reading, a fresh airflow and no alert for: the loop is closed in those.
healthy() {
  curl -sf -m 3 'http://127.0.0.1:8060/api/overview?minutes=10' | python3 -c "
import sys,json
o=json.load(sys.stdin)
print(sum(1 for r in o['rooms'] if r['co2'] is not None and r['flow_ls'] is not None and r['severity']==0))" 2>/dev/null || echo 0
}
# recovered waits until the loop is closed in every room again and prints
# how many real seconds that took.
recovered() {
  local t0=$SECONDS
  for _ in $(seq 240); do
    [ "$(healthy)" -ge 50 ] && { echo "$((SECONDS - t0))"; return; }
    sleep 0.5
  done
  echo "not within 120"
}

co2=$(device co2 level0/1570)
damper=$(device damper level0/1570)

deploy/start-run.sh "$run_id" predictive 2026-09-21 08:00 60
post '{"running": true}'

# T-09 and T-10: the override belongs to the damper. With the controller
# dead, a CO₂ sensor reading above 1100 ppm must still open it fully.
wait_until 08:10
log "T-10: killing the controller; $damper is at $(airflow "$damper") l/s"
docker compose kill controller >/dev/null 2>&1
log "T-09: $co2 starts drifting upwards, 6000 ppm per hour"
deploy/fault.sh "$co2" drift:+6000 >/dev/null 2>&1
wait_until 08:30
log "T-09: twenty minutes on, the controller still dead: $damper is at $(airflow "$damper") l/s; events in 1570: $(events level0/1570)"
deploy/fault.sh "$co2" >/dev/null 2>&1
docker compose start controller >/dev/null 2>&1
wait_until 08:45
log "T-09: sensor repaired and controller back: $damper is at $(airflow "$damper") l/s; events in 1570: $(events level0/1570); loop closed in $(healthy) of 50 rooms"

# T-17: BuildSim comes back blank. The registry has to put the devices'
# equipment back, occupancysim its clock, the physical model its record.
log "T-17: restarting BuildSim"
docker compose restart buildsim >/dev/null 2>&1
log "T-17: loop closed in all rooms again after $(recovered) s real; the building's clock shows $(reading clock-0001 | cut -c16-36)"

# T-23: kill each of ours in turn. (occupancysim is the course's, and its
# clock would restart at its default date; the broker is T-24.)
for svc in controller storage viz truthlog physics registry clock-0001 co2-0003 damper-0003; do
  docker compose kill "$svc" >/dev/null 2>&1
  sleep 3
  state=$(docker inspect "$(docker compose ps -aq "$svc")" --format '{{.State.Status}}')
  back="came back by itself"
  if [ "$state" != running ]; then
    back="stayed down until started again"
    docker compose start "$svc" >/dev/null 2>&1
  fi
  log "T-23: killed $svc; it $back; loop closed in all rooms after $(recovered) s real; $damper at $(airflow "$damper") l/s"
done

# T-22: where it stops working. One model minute is 1 s real at factor 60;
# raise the factor and count what share of the expected readings arrive.
for factor in 120 240 480 960; do
  post "{\"factor\": $factor}"
  sleep 3
  a=$(curl -sf http://127.0.0.1:8081/api/state | python3 -c "import sys,json; print(json.load(sys.stdin)['sim']['clock']['time'][:17]+'00Z')")
  sleep 20
  b=$(curl -sf http://127.0.0.1:8081/api/state | python3 -c "import sys,json; print(json.load(sys.stdin)['sim']['clock']['time'][:17]+'00Z')")
  sleep 3
  echo "$(now) [$run_id] T-22: factor $factor, $a to $b: $(A=$a B=$b RUN=$run_id python3 - <<'PY'
import json,os,urllib.request,datetime as d
a,b,run=os.environ['A'],os.environ['B'],os.environ['RUN']
mins=int((d.datetime.fromisoformat(b.replace('Z','+00:00'))-d.datetime.fromisoformat(a.replace('Z','+00:00'))).total_seconds()//60)
def n(kind):
    u=f"http://127.0.0.1:8090/history?run={run}&kind={kind}&from={a}&to={b}"
    return sum(1 for _ in urllib.request.urlopen(u, timeout=120))
obs=sum(n(k) for k in ('co2','temp','occupancy'))
dec=n('decision')
print(f"{mins} model minutes; readings {obs} of {mins*150} ({100*obs/max(mins*150,1):.1f} %); decisions {dec} of {mins*100} ({100*dec/max(mins*100,1):.1f} %)")
PY
)"
done
post '{"factor": 60}'
sleep 5
post '{"running": false}'
log "done; the clock is paused"
