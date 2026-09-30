# Ventilation Control (D7065E)

Energy-aware ventilation for the LTU A-house in BuildSim. Design notes live in
[docs/](docs/): start with [architecture-options.md](docs/architecture-options.md).

## Run

```
docker compose up -d --build
```

- BuildSim viewer: <http://127.0.0.1:9090>
- occupancysim (people and the model clock): <http://127.0.0.1:8081>
- storage (history, models, exports): <http://127.0.0.1:8090>, e.g.
  `curl 'localhost:8090/history?kind=co2&room=level0/1570'`
- MQTT: `127.0.0.1:1883`, one login per service (see `compose.yaml`)

Tag each experiment with its own run id: `RUN_ID=reactive-s1 docker compose up -d`.

On a machine where the Docker socket is root-only, point Compose at rootless
Podman first: `export DOCKER_HOST=unix:///run/user/$UID/podman/podman.sock`.

Useful while developing:

```
POLICY=constant docker compose up -d controller      # switch policy (constant | reactive)
curl 'localhost:8090/history?kind=decision' | tail  # decision records
curl 'localhost:8090/history?kind=truth' | tail     # true room state, once per model minute
curl -o run.tar localhost:8090/runs/dev/export       # a run's JSONL, for offline analysis
curl -X POST localhost:8081/api/clock -H 'Content-Type: application/json' -d '{"time":"09:30"}'
```

## Services

| Service | Code | What it does |
|---|---|---|
| `physics` | `cmd/physics` | CO₂ and heat balance per room; serves the truth to the gateways |
| `sensor-co2` | `cmd/sensor` (`KIND=co2`) | Samples the truth, adds noise and faults, writes readings to BuildSim |
| `actuator-damper` | `cmd/actuator` (`KIND=damper`) | Validates commands, enforces the safety limits, travels, reports the reached state |
| `controller` | `cmd/controller` | Reads CO₂ from MQTT (BuildSim if the broker is silent), decides, commands the damper |
| `storage` | `cmd/storage` | Records every MQTT message as JSONL per run; serves history and models |
| `mosquitto` | `deploy/mosquitto` | MQTT broker; its ACL keeps `truth/#` for storage only |

`ROOMS` in `compose.yaml` lists the rooms as `<level>/<room>`; the first slice
runs the fika room `level0/1570`. `FAULTS` on a sensor injects faults, e.g.
`stuck:level0/1570`.

## Test

```
go test ./...
```
