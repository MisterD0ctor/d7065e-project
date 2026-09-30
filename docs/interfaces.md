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
| IF-1 | Model clock | occupancysim; physics relays it on `time/model` | physics (REST); actuators (MQTT, REST fallback) | REST, MQTT |
| IF-2 | Truth API | physics | sensor devices only | REST |
| IF-3 | Sensor readings in BuildSim | each sensor device | controller and damper (fallback), viewer | REST |
| IF-4 | Observations `obs/…` | each sensor device | controller, damper, storage, viz | MQTT |
| IF-5 | Truth `truth/…` | physics | storage only | MQTT |
| IF-6 | Decisions `decision/…` | controller | storage, viz | MQTT |
| IF-7 | Actuator events `event/…` | actuators | storage, viz | MQTT |
| IF-8 | Actuator command | controller | actuators | REST |
| IF-9 | Actuator state in BuildSim | each actuator device | physics, viewer | REST |
| IF-10 | Storage API | storage | controller, train, oracle, people | REST |
| IF-11 | Room layers and alerts | viz | viewer | REST |
| IF-12 | Device registry | registry | installer, devices, controller | REST |

---

## IF-1. Model clock

`GET http://occupancysim:8081/api/state` → `.sim.clock`
(`time`, `minute_of_day`, `weekday`, `weekend`, `factor`, `running`).

The same response lists `.sim.rooms[]` with each room's role and `capacity`.
Physics and the registry read the capacities **once** to size the ventilation,
because occupancysim decides which rooms are fika rooms (4 m² per person) and
which are lecture rooms (2 m²). The registry stores the sizing with the room
at installation, and devices and the controller get it from there (IF-12).
Without occupancysim the capacity is estimated from the area.

Physics relays the time on MQTT every tick, as a retained message on
`time/model` (`{"model_time": "…", "factor": 60}`), the way a building network
distributes time. The actuators use that instead of each polling
occupancysim, and poll occupancysim only while the relay is silent.

- **Rate:** physics reads it every tick (1 s real).
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

The registry registers each installed device's equipment (IF-12) and
re-registers it every 10 s (`POST /api/equipment/bulk` skips ids that exist),
which restores everything after a BuildSim restart (FR-11). The equipment id
is the device id the installer entered; the sensor inside gets a suffix:

| Kind | Equipment id (example) | `type` | Sensor id | Unit | Value format |
|---|---|---|---|---|---|
| CO₂ | `co2-0001` | `co2_sensor` | `co2-0001-reading` | ppm | integer, `"612"` |
| Temperature | `temp-0001` | `temperature_sensor` | `temp-0001-reading` | °C | one decimal, `"21.3"` |
| Occupancy | `occ-0001` | `occupancy_counter` | `occ-0001-reading` | persons | integer, `"6"` |

`PUT /api/sensors/{sensor_id}/value` with `{"data_type": "text", "value": "612"}`.

- **Rate:** one write per sensor per sample (60 model-s).
- **Stale:** BuildSim stamps wall time. A reader using this fallback converts
  with the clock `factor`: older than 3 samples (180 model-s) is stale (FR-7).
- **On failure:** retry ×3 with 100 ms backoff, then drop the reading and log
  it. A `404` means BuildSim has lost the equipment; the registry recreates it
  within 10 s, so the device just drops that one reading.

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
  controller and the damper actuator fall back to polling BuildSim (D-3).
  "Silent" means no observation for 2.5× the usual real-time gap between
  them (at least 1.5 s). The threshold adapts because one model minute is
  1 s real at factor 60 but 0.1 s at factor 600. It must stay below the
  command TTL (IF-8), or an outage expires commands before the fallback
  starts.

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

One record per command: an airflow decision carries `airflow_target_ls`, a
heating decision `setpoint_target_c`. For `predictive` and `oracle`,
`predicted_occupancy` is the number of people ventilated for (the most of the
count now and the forecast within `lead_min`).

`mode` is `normal`; `degraded_occupancy` (the occupancy count is stale or
missing, so the decision rests on the forecast and CO₂); `no_forecast` (no
trained model yet, so `predictive` acts as `reactive`); or `degraded_co2`
(CO₂ missing: no command is sent, and the damper's TTL returns it to design
flow). `viz` turns non-normal modes into alerts (FR-7).

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
  "issued_at": "2026-09-14T10:15:00Z",
  "ttl_s": 300,
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

Registered by the registry, like IF-3:

| Kind | Equipment id (example) | `type` | Actuator id | State format |
|---|---|---|---|---|
| Damper | `damper-0001` | `ventilation_fan` | `damper-0001-state` | l/s, one decimal |
| Heating | `heat-0001` | `radiator` | `heat-0001-state` | °C, one decimal |

`PUT /api/actuators/{actuator_id}/state` with `{"state": "180.0"}`.

- **Travel:** airflow moves at most 10 % of the room's maximum per model-minute;
  setpoint at most 0.5 °C per model-minute. The actuator writes the state it
  has *reached*, not the target.
- **Rate:** every 10 model-s while moving, then only on change.
- **Physics side:** reads all states with one `GET /api/equipment?level=level0`
  per tick and finds each room's damper and valve by equipment `type` and
  room, so physics doesn't need the registry. Missing or unparsable state →
  keep the last value.

## IF-10. Storage API

| Call | Returns |
|---|---|
| `GET /history?run=…&room=level0/A109&kind=co2&from=…&to=…` | JSONL, one IF-4/5/6/7 record per line, in the order stored. `run` defaults to storage's own `RUN_ID`; `from` is inclusive, `to` exclusive |
| `GET /runs` | The recorded run ids, oldest first |
| `PUT /models/{name}` | Stores a model (JSON); `201` |
| `GET /models/latest` | The newest model, with its `name` |
| `GET /runs/{run_id}/export` | A tar of the run's JSONL files, for offline analysis |

`kind` is `co2`, `temp`, `occupancy`, `true_occupancy`, `truth`, `decision` or
`event`. `from` and `to` are model time.

Storage validates every record before appending it: JSON, a safe `run_id`, a
room, and an RFC 3339 `model_time` (decisions from the REST fallback may lack
one). Bad records are logged and counted at `GET /healthz`, never silently
dropped. Observations are deduplicated on `(run_id, sensor_id, seq)`.
Published on `127.0.0.1:8090` for people and notebooks.

Model format (the per-room time-of-day profile, FR-4), written by `train`:

```json
{
  "name": "profile-20261001T120000",
  "slot_min": 15,
  "quantile": 0.8,
  "trained_on": {"run_ids": ["train-1"], "from": "…", "to": "…", "weekdays": 5, "readings": 140000},
  "rooms": {"level0/1570": {"mean": [0, …], "high": [0, …], "days": [5, …]}}
}
```

96 slots per room (15 min each), weekdays only. `mean` is the mean of each
day's peak count in the slot; `high` the 80 % quantile across days, which is
what the controller ventilates for. Trained on **occupancy sensor readings**,
never on the truth.

**The live run's truth is not served:** `GET /history?kind=truth` or
`kind=true_occupancy` for storage's own `RUN_ID` returns `403`. The oracle
replays an earlier, finished run; the evaluation reads the export.

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

## IF-12. Device registry

The installer's record of what is installed where (D-10). Published on
`127.0.0.1:8070`; the web form for installers is at `/`.

| Call | Who | Returns |
|---|---|---|
| `POST /devices {id, kind, room, installed_by}` | installer | `201` + the device. `409` if the id is taken, `422` if the kind is unknown, the id has characters other than letters, digits, `-` and `_`, or the room is not in the floor plan |
| `GET /devices?kind=&room=&status=` | controller, people | The matching devices |
| `GET /devices/{id}` | anyone | One device, or `404` |
| `DELETE /devices/{id}` | installer | Retires it (`204`); it stays in the database, and its equipment is removed from BuildSim |
| `PUT /devices/{id}/checkin {endpoint}` | the device itself | `200` + its record (room and sizing); `404` not installed; `410` retired |

A device record:

```json
{
  "id": "damper-0001", "kind": "damper", "room": "level0/1570",
  "status": "active", "installed_at": "2026-09-30T18:17:41Z", "installed_by": "kasper",
  "endpoint": "http://damper-0001:8080", "last_seen": "2026-09-30T18:21:12Z",
  "size": {"area_m2": 101.2, "volume_m3": 273.2, "capacity": 25,
           "design_ls": 210.4, "max_ls": 315.6, "occupied_min_ls": 35.4, "empty_min_ls": 10.1}
}
```

- **Boot:** a device checks in every 5 s until it gets `200`. Until then it
  publishes nothing and accepts no commands: it doesn't know its room.
- **Kind check:** a device whose hardware kind differs from the registered kind
  refuses to start, so a mistyped kind can't make a CO₂ sensor report as a
  thermometer.
- **Heartbeat:** every 30 s real. `last_seen` shows dead devices; a `404` or
  `410` stops the device reporting.
- **Addresses:** actuators advertise `endpoint` at check-in. The controller
  re-reads the registry every 30 s, and at once (at most every 5 s) when a
  command fails, so a moved actuator is found quickly.
- **On failure:** running devices keep working without the registry; only
  starting devices and the controller's view of new installations wait for it.
