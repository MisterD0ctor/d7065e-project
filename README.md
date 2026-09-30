# Ventilation Control (D7065E)

Energy-aware ventilation for the LTU A-house in BuildSim. Design notes live in
[docs/](docs/): start with [architecture-options.md](docs/architecture-options.md).

## Run

```
docker compose up -d --build
```

- BuildSim viewer: <http://127.0.0.1:9090>
- occupancysim (people and the model clock): <http://127.0.0.1:8081>

On a machine where the Docker socket is root-only, point Compose at rootless
Podman first: `export DOCKER_HOST=unix:///run/user/$UID/podman/podman.sock`.

Useful while developing:

```
POLICY=constant docker compose up -d controller      # switch policy (constant | reactive)
docker compose logs -f controller                     # decision records
docker compose logs physics | grep truth              # true room state, once per model minute
curl -X POST localhost:8081/api/clock -H 'Content-Type: application/json' -d '{"time":"09:30"}'
```

## Services

| Service | Code | What it does |
|---|---|---|
| `physics` | `cmd/physics` | CO₂ and heat balance per room; serves the truth to the gateways |
| `sensor-co2` | `cmd/sensor` (`KIND=co2`) | Samples the truth, adds noise and faults, writes readings to BuildSim |
| `actuator-damper` | `cmd/actuator` (`KIND=damper`) | Validates commands, enforces the safety limits, travels, reports the reached state |
| `controller` | `cmd/controller` | Reads readings from BuildSim, decides, commands the damper |

`ROOMS` in `compose.yaml` lists the rooms as `<level>/<room>`; the first slice
runs the fika room `level0/1570`. `FAULTS` on a sensor injects faults, e.g.
`stuck:level0/1570`.

## Test

```
go test ./...
```
