#!/usr/bin/env bash
# Injects a fault on one sensor of the running stack, or clears it.
#
#   deploy/fault.sh co2-0002 stuck      # stuck | dropout | drift:+50
#   deploy/fault.sh co2-0002            # healthy again
#
# This recreates the sensor's container, and compose takes RUN_ID from .env
# unless it is given. In a stack started with another RUN_ID on the command
# line (deploy/evaluate.sh does that), a plain `docker compose up -d co2-0002`
# makes the sensor tag its readings with the wrong run, and recreates physics
# too, because its settings appear to have changed. So the run id is read
# from the running controller, and nothing but the sensor is touched.
set -euo pipefail

device=$1 fault=${2:-}
cd "$(dirname "$0")/.."

controller=$(docker compose ps -q controller)
if [ -z "$controller" ]; then
  echo "the controller is not running: start the stack first" >&2
  exit 1
fi
run_id=$(docker inspect "$controller" --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^RUN_ID=//p')
var="FAULT_$(tr 'a-z-' 'A-Z_' <<<"$device")"

env RUN_ID="$run_id" "$var=$fault" docker compose up -d --no-deps "$device"
echo "$device: ${fault:-no fault} (run $run_id)"
