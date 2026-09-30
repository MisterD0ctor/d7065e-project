# Ventilation Control (D7065E)

Energy-aware ventilation for the LTU A-house in BuildSim. Design notes live in
[docs/](docs/): start with [architecture-options.md](docs/architecture-options.md).

## Run

```
docker compose up -d --build
```

The devices are one container each, listed in `compose.devices.yaml`
(generated; `.env` makes Compose read it). They start **uncommissioned**: each
waits until an installer registers it. Register them, as the technician would,
in the web form at <http://127.0.0.1:8070> or from the installation list:

```
go run ./cmd/install -by <your name> -csv deploy/devices.csv
go run ./cmd/install -by <your name> co2-0001 co2 level0/1570   # one device
```

- BuildSim viewer: <http://127.0.0.1:9090>
- occupancysim (people and the model clock): <http://127.0.0.1:8081>
- registry (installer's form and API): <http://127.0.0.1:8070>
- storage (history, models, exports): <http://127.0.0.1:8090>, e.g.
  `curl 'localhost:8090/history?kind=co2&room=level0/1570'`
- MQTT: `127.0.0.1:1883`, one login per service (see `compose.yaml`)

`RUN_ID` in `.env` tags every record; change it per experiment and restart the
stack.

On a machine where the Docker socket is root-only, point Compose at rootless
Podman first: `export DOCKER_HOST=unix:///run/user/$UID/podman/podman.sock`.

With 50 rooms the stack is ~257 containers, which a stock Linux laptop can't
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

Each room gets a CO₂, temperature and occupancy sensor, a damper and a
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

### While developing

```
POLICY=constant docker compose up -d controller            # constant | reactive
FAULT_CO2_0001=stuck docker compose up -d co2-0001         # stuck | dropout | drift:+50
curl 'localhost:8090/history?kind=decision' | tail         # decision records
curl 'localhost:8090/history?kind=truth' | tail            # true room state, per model minute
curl -o run.tar localhost:8090/runs/dev/export             # a run's JSONL, for offline analysis
curl -X POST localhost:8081/api/clock -H 'Content-Type: application/json' -d '{"time":"09:30"}'
```

## Services

| Service | Code | What it does |
|---|---|---|
| `physics` | `cmd/physics` | CO₂ and heat balance per room; serves the truth to the sensors; relays the model time |
| `registry` | `cmd/registry` | Installed devices (SQLite); installer's form; keeps BuildSim's equipment list in step |
| `co2-0001`, `temp-0001`, … | `cmd/sensor` | One sensor device each: samples its room, adds noise and faults, writes to BuildSim and MQTT |
| `damper-0001`, `heat-0001`, … | `cmd/actuator` | One actuator device each: validates commands, enforces the safety limits, travels, reports |
| `controller` | `cmd/controller` | Finds rooms and actuators in the registry; reads observations from MQTT (BuildSim as fallback); decides and commands |
| `storage` | `cmd/storage` | Records every MQTT message as JSONL per run; serves history and models |
| `mosquitto` | `deploy/mosquitto` | MQTT broker; its ACL keeps `truth/#` for storage only |

## Test

```
go test ./...
```
