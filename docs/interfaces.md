# Interface contracts (draft, copy into report §5)

Each contract says what is sent, in what units, how often, when it counts as
stale, what happens on failure, and which process owns it (Lecture 1,
slide 34). Decisions and requirement IDs refer to
[architecture-options.md](architecture-options.md).

Conventions for every interface:
- **Rooms** are always keyed `<level>/<room>`, e.g. `level0/A109`. Room names
  repeat between floors, so a bare `A109` is a bug.
- **Model time** is RFC 3339 exactly as occupancysim's clock gives it, e.g.
  `2026-09-14T10:15:00Z`. Wall time is used only for network timeouts.
- **Run id**: every service gets `RUN_ID` from its environment and puts it in
  every record it publishes or stores.
- Values we write to BuildSim are **strings** (BuildSim's rule). Values on MQTT
  and our own HTTP APIs are JSON numbers.

Overview:

| ID | Interface | Owner (writer) | Readers | Protocol |
|---|---|---|---|---|
| IF-1 | Model clock | occupancysim | physics, actuators | REST |
| IF-2 | Truth API | physics | sensor gateways only | REST |
| IF-3 | Sensor readings in BuildSim | one gateway per kind | controller (fallback), viewer | REST |
| IF-4 | Observations `obs/…` | one gateway per kind | controller, damper actuator, storage, viz | MQTT |
| IF-5 | Truth `truth/…` | physics | storage only | MQTT |
| IF-6 | Decisions `decision/…` | controller | storage, viz | MQTT |
| IF-7 | Actuator events `event/…` | actuators | storage, viz | MQTT |
| IF-8 | Actuator command | controller | actuators | REST |
| IF-9 | Actuator state in BuildSim | one actuator per kind | physics, viewer | REST |
| IF-10 | Storage API | storage | controller, train, oracle, people | REST |
| IF-11 | Room layers and alerts | viz | viewer | REST |

---

## IF-1. Model clock

`GET http://occupancysim:8081/api/state` → `.sim.clock`
(`time`, `minute_of_day`, `weekday`, `weekend`, `factor`, `running`).

The same response lists `.sim.rooms[]` with each room's role and `capacity`.
Physics, the actuators and the controller read the capacities **once at
start** to size the ventilation, because occupancysim decides which rooms are
fika rooms (4 m² per person) and which are lecture rooms (2 m²). Without
occupancysim they estimate the capacity from the area.

- **Rate:** physics reads it every tick (1 s real); actuators every tick too.
- **Stale:** if the clock doesn't advance for 10 s real while `running` is
  true, physics stops stepping and logs it. It never extrapolates.
- **On failure:** timeout 500 ms. Physics holds its state and retries next
  tick. Actuators keep their targets but can't expire commands until the clock
  is back.

## IF-2. Truth API (physics)

`GET http://physics:8080/rooms?level=level0&kind=co2` →

```json
{
  "run_id": "pred-s42-2026-09-14",
  "model_time": "2026-09-14T10:15:00",
  "rooms": {
    "level0/A109": {"co2_ppm": 612.4, "temp_c": 21.3, "occupancy": 6}
  }
}
```

- **Units:** ppm, °C, persons.
- **Rate:** each gateway polls once per sample period (60 model-s, which is
  1 s real at factor 60).
- **Stale:** a gateway that gets the same `model_time` twice does not publish a
  new reading.
- **On failure:** timeout 500 ms, no retry within the sample. The gateway
  publishes nothing, so the sensor goes stale downstream, which is what a dead
  device looks like.
- **Access:** only the gateways call it. The controller has no route to
  physics in `compose.yaml` (the scope constraint in §0).
- Also `GET /healthz` for compose health checks.

## IF-3. Sensor readings in BuildSim

Equipment registered by each gateway at start, with `POST /api/equipment/bulk`
(it skips ids that exist, so re-registration after a crash is safe; FR-11):

| Kind | Equipment id | `type` | Sensor id | Unit | Value format |
|---|---|---|---|---|---|
| CO₂ | `co2-level0-A109` | `co2_sensor` | `level0-A109-co2` | ppm | integer, `"612"` |
| Temperature | `temp-level0-A109` | `temperature_sensor` | `level0-A109-temp` | °C | one decimal, `"21.3"` |
| Occupancy | `occ-level0-A109` | `occupancy_counter` | `level0-A109-occ` | persons | integer, `"6"` |

`PUT /api/sensors/{sensor_id}/value` with `{"data_type": "text", "value": "612"}`.

- **Rate:** one write per sensor per sample (60 model-s).
- **Stale:** BuildSim stamps wall time. A reader using this fallback converts
  with the clock `factor`: older than 3 samples (180 model-s) is stale (FR-7).
- **On failure:** retry ×3 with 100 ms backoff, then drop the reading and count
  it in the gateway's log. A `404` means the equipment is gone: re-register,
  then retry once.

## IF-4. Observations `obs/<level>/<room>/<kind>`

`kind` is `co2`, `temp` or `occupancy`. Published by the gateway right after
its IF-3 write.

```json
{
  "run_id": "pred-s42-2026-09-14",
  "sensor_id": "level0-A109-co2",
  "room": "level0/A109",
  "kind": "co2",
  "value": 612,
  "unit": "ppm",
  "model_time": "2026-09-14T10:15:00",
  "seq": 8412
}
```

- **QoS 1, not retained.** Consumers **deduplicate on `(sensor_id, seq)`**,
  because QoS 1 can deliver twice (course notes 2).
- **Injected faults are not marked.** A stuck or drifting reading looks like
  any other; the controller has to detect it (FR-7). That is the point of the
  fault injection.
- **Stale:** the consumer judges freshness by `model_time`, never by arrival
  time. Older than 180 model-s → stale.
- **On failure (broker down):** the gateway still writes IF-3, so the
  controller falls back to polling BuildSim (D-3).

## IF-5. Truth `truth/<level>/<room>`

```json
{
  "run_id": "pred-s42-2026-09-14",
  "room": "level0/A109",
  "model_time": "2026-09-14T10:15:00",
  "co2_ppm": 612.4, "temp_c": 21.3, "occupancy": 6,
  "airflow_ls": 180.0, "setpoint_c": 21.0
}
```

- **Rate:** every 60 model-s, per room.
- **Access:** a Mosquitto ACL lets only `storage` subscribe to `truth/#`. The
  boundary is enforced by deployment, not by trust (Lecture 2, slide 8).
- **On failure:** physics doesn't buffer. Gaps in the truth show up in the
  evaluation as missing minutes and are reported, not filled.

## IF-6. Decisions `decision/<level>/<room>`

Published once per control period (60 model-s) per room.

```json
{
  "run_id": "pred-s42-2026-09-14",
  "room": "level0/A109",
  "model_time": "2026-09-14T10:15:00",
  "policy": "predictive",
  "mode": "normal",
  "observed": {"co2": 612, "temp": 21.3, "occupancy": 6},
  "predicted_occupancy": 25,
  "lead_min": 12,
  "airflow_target_ls": 308.0,
  "setpoint_target_c": 21.0,
  "cmd_ids": ["3f0c…", "9a1e…"],
  "reason": "lecture slot expected at 10:15"
}
```

`mode` is `normal`, `degraded_occupancy` (occupancy sensor bad → CO₂-reactive)
or `degraded_co2` (CO₂ bad → design flow). `viz` turns non-normal modes into
alerts (FR-7).

## IF-7. Actuator events `event/<level>/<room>`

```json
{"run_id": "…", "room": "level0/A109", "model_time": "…",
 "actuator": "damper", "kind": "override_co2", "detail": "CO₂ 1134 ppm > 1100"}
```

`kind` is `override_co2`, `rejected`, `ttl_expired` or `floor_applied`.
Published on every change, not every tick.

## IF-8. Actuator command

`POST http://actuator-damper:8080/rooms/level0/A109/command`
(same shape on `actuator-heating`):

```json
{
  "cmd_id": "3f0c6c1e-…",
  "value": 308.0,
  "unit": "l/s",
  "issued_at": "2026-09-14T10:15:00",
  "ttl_s": 180,
  "reason": "predictive: 25 expected in 12 min"
}
```

| Response | Meaning |
|---|---|
| `202 {"target": 308.0, "clamped": false}` | Accepted; the actuator moves towards the target |
| `200 {"duplicate": true}` | This `cmd_id` was already seen; nothing changes |
| `409 {"reason": "expired"}` | `issued_at + ttl_s` is before the actuator's model time |
| `422 {"reason": "…"}` | Out of range, wrong unit, or unknown room |

- **Units:** damper in l/s (range 0 … 1.5 × q_design for the room), heating
  setpoint in °C (range 16 … 24).
- **Safety (D-6):** the damper actuator clamps to the flow floor (0.35 l/s·m²
  occupied, 0.10 otherwise) and forces maximum flow while the latest CO₂
  observation is above 1100 ppm, whatever the command says. `clamped: true`
  tells the controller this happened.
- **Rate:** at most one command per room per control period.
- **TTL:** if no valid command arrives within `ttl_s`, the actuator reverts to
  design flow (damper) or 21 °C (heating) and publishes `ttl_expired`.
- **`issued_at` is optional while observations come over REST.** The
  controller has no model time until IF-4 carries it, so the TTL counts from
  the model time at which the actuator *accepted* the command. Once IF-4
  exists, the controller sends `issued_at` = the observation's `model_time`.
- **On failure (controller side):** timeout 500 ms, retry the **same**
  `cmd_id` up to 3 times. Idempotency makes this safe.

## IF-9. Actuator state in BuildSim

Registered by each actuator at start, like IF-3:

| Kind | Equipment id | `type` | Actuator id | State format |
|---|---|---|---|---|
| Damper | `vent-level0-A109` | `ventilation_fan` | `level0-A109-airflow` | l/s, one decimal |
| Heating | `heat-level0-A109` | `radiator` | `level0-A109-setpoint` | °C, one decimal |

`PUT /api/actuators/{actuator_id}/state` with `{"state": "180.0"}`.

- **Travel:** airflow moves at most 10 % of the room's maximum per model-minute;
  setpoint at most 0.5 °C per model-minute. The actuator writes the state it
  has *reached*, not the target.
- **Rate:** every 10 model-s while moving, then only on change.
- **Physics side:** reads all states with one `GET /api/equipment?level=level0`
  per tick. Missing or unparsable state → keep the last value and log it.

## IF-10. Storage API

| Call | Returns |
|---|---|
| `GET /history?room=level0/A109&kind=co2&from=…&to=…` | JSONL, one IF-4/5/6/7 record per line, in model-time order |
| `PUT /models/{name}` | Stores a model (JSON); `201` |
| `GET /models/latest` | The newest model, with its `name` |
| `GET /runs/{run_id}/export` | A tar of the run's JSONL files, for offline analysis |

`kind` is `co2`, `temp`, `occupancy`, `true_occupancy`, `truth`, `decision` or
`event`. `from` and `to` are model time.

Model format (the per-room time-of-day profile, FR-4):

```json
{
  "name": "profile-2026-09-30T1200",
  "trained_on": {"run_ids": ["…"], "from": "…", "to": "…"},
  "slot_min": 15,
  "rooms": {"level0/A109": {"weekday": [0, 0, 0.2, 1.1, …]}}
}
```

96 values per room: the mean occupancy per 15-minute slot on weekdays.

- **On failure:** consumers time out after 2 s. The controller keeps its last
  model (D-5); `train` exits non-zero and can be rerun.
- The oracle calls `GET /history?kind=true_occupancy` for a *recorded* run,
  never the live one (D-7).

## IF-11. Room layers and alerts (viz)

`viz` is the only writer (§0: every collection `PUT` replaces everything).

`PUT /api/room-layers`, all layers in one request, every 60 model-s:

| Layer id | Label | `source` | Range |
|---|---|---|---|
| `co2` | CO₂ (latest reading) | `sensor reading` | 400–1400 ppm |
| `occupancy` | Occupancy (counted) | `sensor reading` | 0–40 |
| `occupancy_predicted` | Occupancy in 15 min | `prediction` | 0–40 |
| `airflow` | Airflow | `actuator state` | 0–500 l/s |

`PUT /api/alerts`, at most 100: one per room in a degraded mode (`warning`),
one per active CO₂ override (`critical`). Alerts are the current picture only;
storage is the audit trail.
