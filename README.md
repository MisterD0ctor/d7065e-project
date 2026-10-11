# Ventilation Control (D7065E)

Energy-aware ventilation for the LTU A-house in BuildSim. Design notes live in
[docs/](docs/): start with [architecture-options.md](docs/architecture-options.md).

BuildSim is in the middle, as in the course's lab quickstart:

```
occupancysim ──people, clock──► BuildSim ◄──true values── physics
                                 │   ▲  └──actuator states──► physics
                    its own value│   │its own state
                                 ▼   │
                         sensor devices   actuator devices ◄──commands── controller
                                 │ readings (MQTT)                           ▲
                                 └───────────────────────────────────────────┘
```

occupancysim and the physical model talk only to BuildSim. The controller
never does: what it knows about the building comes from the sensor devices,
each of which reads the value at its own place in BuildSim and reports it
with its own measurement error (docs/architecture-options.md, D-12).

## Run

```
docker compose up -d --build
```

The devices are one container each, listed in `compose.devices.yaml`
(generated; `.env` makes Compose read it). They start **uncommissioned**: each
waits until an installer registers it. Register them, as the technician would,
in the web form at <http://127.0.0.1:8070> or from the installation list:

```
go run ./cmd/install -by <your name> -csv deploy/devices.csv -rooms deploy/rooms.csv
go run ./cmd/install -by <your name> -capacity 25 co2-0001 co2 level0/1570   # one device
```

`deploy/rooms.csv` is the installer's room list: how many people each room
is designed for, which sizes its ventilation. A room without one is sized
from its floor area.

- Dashboard, graphs: <http://127.0.0.1:8060>. The building and every room
  over time (CO₂, people, airflow, temperature, alerts), up to a week back
- Dashboard, 3D: the BuildSim viewer at <http://127.0.0.1:9090> with our
  layers and alerts, e.g. <http://127.0.0.1:9090/?layer=co2&floor=level0>
  (also `occupancy`, `occupancy_predicted`, `airflow`, `temperature`)
- occupancysim (people and the model clock): <http://127.0.0.1:8081>
- registry (installer's form and API): <http://127.0.0.1:8070>
- storage (history, models, exports): <http://127.0.0.1:8090>, e.g.
  `curl 'localhost:8090/history?kind=co2&room=level0/1570'`
- MQTT: `127.0.0.1:1883`, one login per service (see `compose.yaml`)

`RUN_ID` in `.env` tags every record; change it per experiment and restart the
stack.

On a machine where the Docker socket is root-only, point Compose at rootless
Podman first: `export DOCKER_HOST=unix:///run/user/$UID/podman/podman.sock`.

With 50 rooms the stack is ~260 containers, which a stock Linux laptop can't
run without two changes (see docs/review-log.md, "first 50-room run"):

```
# ~/.config/containers/containers.conf  (Podman: no kernel keyring per container)
[containers]
keyring = false

# /etc/sysctl.d/90-neigh.conf  (root; the ARP table is shared by all containers)
net.ipv4.neigh.default.gc_thresh1 = 1024
net.ipv4.neigh.default.gc_thresh2 = 4096
net.ipv4.neigh.default.gc_thresh3 = 8192
```

### Planning the installation

```
go run ./cmd/gendevices -plan 50   # pick 50 rooms, write deploy/devices.csv
go run ./cmd/gendevices            # regenerate compose.devices.yaml from it
```

The building gets one clock device, and each room a CO₂, temperature and occupancy sensor, a damper and a
radiator valve. Physics simulates exactly the rooms in the list.

### Policies, training and the oracle

```
POLICY=reactive docker compose up -d controller                       # constant | reactive | predictive | oracle
TRAIN_RUNS=train-1 MODEL_NAME=profile-a docker compose run --rm train # train on recorded runs
POLICY=predictive docker compose up -d controller                     # uses the newest model
POLICY=oracle ORACLE_RUN=eval-reactive docker compose up -d controller # replay that run's true occupancy
```

The oracle must replay a run of the **same seed and dates**. Storage refuses
the live run's truth, so it can only ever read a finished run.

### Evaluation

```
SUFFIX=-v2 deploy/evaluate-all.sh 2>&1 | tee results/evaluate-all-v2.log
```

A training week, the model, then the four policies over the same held-out
week: five runs of about an hour each at factor 120, then `cmd/evaluate`
prints the tables (docs/evaluation-results.md). Pick a new `SUFFIX` each time,
keep the PC awake (`systemd-inhibit --what=sleep:idle deploy/evaluate-all.sh`),
and don't restart containers while it runs.

`deploy/robustness-tests.sh robustness` does the same for the robustness
tests (T-09, T-10, T-17, T-22, T-23): the override with the controller
killed, BuildSim restarted, each service killed in turn, and the simulation
speed raised until readings are missed.

All three scripts start each run with `deploy/start-run.sh`, which brings the
stack up in the order that keeps one run's last minute out of the next: stop
everything of ours, set the clock, start the physical model and wait until
the building's clock shows the start time, then the rest under the new run id.

`go run ./cmd/evaluate -runs <runs> -stuck-check` also replays the runs'
readings through the stuck-sensor detector (FR-7): in a run without injected
faults every verdict would be a false alarm.

`deploy/fault-tests.sh fault-tests` runs the fault tests (T-12, T-13, T-24,
T-36, T-40) as one run of a simulated Monday morning, about ten minutes: a
stuck temperature sensor, a silent and a stuck CO₂ sensor, the building's
clock stopped, the broker stopped, and a counter stuck during a lecture. It
restarts the whole stack under the run id it is given.

Inject single faults with `deploy/fault.sh`, not with a bare
`FAULT_CO2_0001=stuck docker compose up -d co2-0001`. Compose takes `RUN_ID`
from `.env` unless it is given, so in a stack started under another run id
the bare command files the sensor's readings under the wrong run and
restarts physics as well. The script reads the run id from the running
controller.

### While developing

```
POLICY=constant docker compose up -d controller            # constant (design flow by the clock) | reactive
deploy/fault.sh co2-0001 stuck                             # stuck | dropout | drift:+50
deploy/fault.sh co2-0001                                   # healthy again
curl 'localhost:8090/history?kind=decision' | tail         # decision records
curl 'localhost:8090/history?kind=truth&run=<a finished run>' | tail   # true room state, per model minute
curl -o run.tar localhost:8090/runs/dev/export             # a run's JSONL, for offline analysis
curl -X POST localhost:8081/api/clock -H 'Content-Type: application/json' -d '{"time":"09:30"}'
```

## Services

| Service | Code | What it does |
|---|---|---|
| `physics` | `cmd/physics` | The physical model: CO₂ and heat balance per room, one model minute at a time. Talks only to BuildSim: reads the clock, the people per minute and the actuators' states there, writes the true value at every sensor |
| `registry` | `cmd/registry` | Installed devices and their rooms' sizes (SQLite); installer's form; keeps BuildSim's equipment list in step |
| `co2-0001`, `temp-0001`, … | `cmd/sensor` | One sensor device each: reads the value at its place in BuildSim, adds its own noise and faults, publishes the reading on MQTT |
| `clock-0001` | `cmd/sensor` | The building's clock, a sensor too: reads the time physics writes and publishes it for the other devices |
| `damper-0001`, `heat-0001`, … | `cmd/actuator` | One actuator device each: validates commands, enforces the safety limits, travels, writes its state to BuildSim and reports it on MQTT |
| `controller` | `cmd/controller` | Finds rooms and actuators in the registry; reads observations from MQTT, its only way to the building; decides airflow by policy and heating by schedule with pre-heat; commands the actuators |
| `truthlog` | `cmd/truthlog` | The evaluation's recorder, outside the control system: copies the physical model's room-state record from BuildSim to the truth topics |
| `storage` | `cmd/storage` | Records every MQTT message as JSONL per run; serves history and models |
| `viz` | `cmd/viz` | The dashboard, from the observations, decisions and actuator states and events on MQTT: heatmap layers and alerts in the BuildSim viewer, and its own page with graphs over time |
| `mosquitto` | `deploy/mosquitto` | MQTT broker; its ACL keeps `truth/#` for storage only, and lets only the clock tell the time |

## Test

```
go test ./...
```
