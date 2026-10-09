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
| IF-1 | Model clock and occupancy per minute | occupancysim; physics relays the time on `time/model` | physics (REST); actuators (MQTT, REST fallback) | REST, MQTT |
| IF-2 | Truth API | physics | sensor devices only | REST |
| IF-3 | Sensor readings in BuildSim | each sensor device | controller and damper (fallback), viewer | REST |
| IF-4 | Observations `obs/…` | each sensor device | controller, damper, storage, viz | MQTT |
| IF-5 | Truth `truth/…` | physics | storage only | MQTT |
| IF-6 | Decisions `decision/…` | controller | storage, viz | MQTT |
| IF-7 | Actuator events `event/…` | actuators | storage, viz | MQTT |
| IF-8 | Actuator command | controller | actuators | REST |
| IF-9 | Actuator state in BuildSim | each actuator device | physics, viz, viewer | REST |
| IF-10 | Storage API | storage | controller, train, oracle, viz (at start), people | REST |
| IF-11 | Room layers and alerts; the graphs page | viz | viewer; a browser | REST |
| IF-12 | Device registry | registry | installer, devices, controller | REST |

---

## IF-1. Model clock and occupancy per minute

`GET http://occupancysim:8081/api/state` → `.sim.clock`
(`time`, `minute_of_day`, `weekday`, `weekend`, `factor`, `running`).

`GET http://occupancysim:8081/api/minutes?after=2026-09-21T10:14:00Z` → the
clock, and who was in each room at every whole model minute after `after`,
oldest first (without `after`: the newest minute only, where a reader starts):

```json
{
  "clock": {"time": "2026-09-21T10:16:25Z", "factor": 120, "running": true},
  "minutes": [
    {"time": "2026-09-21T10:15:00Z", "rooms": {"level0/1570": {"occupants": 16}}},
    {"time": "2026-09-21T10:16:00Z", "rooms": {"level0/1570": {"occupants": 17, "guards": 1}}}
  ]
}
```

- `occupants` counts everyone in the room; `guards` says how many of them
  are night guards on patrol. Rooms with nobody, and corridors, are left out.
- **Reproducible:** each entry is the state exactly on its minute, and
  occupancysim advances in fixed 5-second steps, so the sequence depends only
  on the day's plan. Every run of the same date gets the same minutes, at any
  speed factor (NFR-7, T-25).
- **Kept:** the last 240 minutes of the current day. A reader further behind
  than that sees a gap and steps over it in one go.
- This is our addition to occupancysim (`external/README.md`). Physics uses
  it instead of BuildSim's `/api/occupancy`, which has no time attached and is
  refreshed at most every 5 real seconds.

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

- **Rate:** physics asks for new minutes every 250 ms real, which is shorter
  than a model minute lasts up to factor 240.
- **Stale:** a paused or stuck clock produces no new minutes, so physics does
  not step. It never extrapolates. If the clock is moved back (a new
  experiment), physics restarts from the newest minute.
- **On failure:** timeout 500 ms. Physics holds its state and retries next
  tick; the minutes it missed are still in the record. Actuators keep their
  targets but can't expire commands until the time is back.

## IF-2. Truth API (physics)

`GET http://physics:8080/rooms?room=level0/A109` (or `?level=level0`, or
nothing for every room) →

```json
{
  "run_id": "pred-s42-2026-09-14",
  "model_time": "2026-09-14T10:15:00Z",
  "rooms": {
    "level0/A109": {"co2_ppm": 612.4, "temp_c": 21.3, "occupancy": 6}
  }
}
```

- **Units:** ppm, °C, persons (guards included: a people counter can't tell).
- `model_time` is always a whole model minute: the rooms' state at the latest
  minute physics has computed.
- **Rate:** each sensor polls every 250 ms real and takes a reading when
  `model_time` has advanced by its sample period (60 model-s).
- **Stale:** a sensor that gets the same `model_time` again does not publish a
  new reading.
- **On failure:** timeout 500 ms, no retry within the sample. The sensor
  publishes nothing, so it goes stale downstream, which is what a dead device
  looks like.
- **Access:** only the sensors call it. This is a convention, not yet enforced:
  the controller has no code that calls physics, but it shares the compose
  network with it. Putting the truth layer on its own network would enforce
  it (known limitation; the MQTT truth topic *is* enforced, IF-5).
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
  "model_time": "2026-09-14T10:15:00Z",
  "co2_ppm": 612.4, "temp_c": 21.3, "occupancy": 6, "guards": 0,
  "airflow_ls": 180.0, "setpoint_c": 21.0,
  "fan_w": 270.0, "ahu_heat_w": 2160.0, "radiator_w": 310.0
}
```

- `occupancy` is everyone in the room; `guards` how many of them are night
  guards on patrol. The evaluation counts a room as occupied when
  `occupancy − guards > 0`.
- The three powers are averages over the minute that ended at `model_time`,
  for the energy proxy (NFR-3).
- **Rate:** one record per room at every whole model minute, at any speed
  factor: physics computes each minute occupancysim recorded (IF-1), also
  when it has to catch up.
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
  "fault": "",
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
heating decision `setpoint_target_c`. A heating decision's `reason` names the
rule that applied: `schedule: occupied hours`, `schedule: setback`, or
`pre-heat for <day hh:mm>` when the setback has ended early so the room is
warm by then (D-6). For `predictive` and `oracle`,
`predicted_occupancy` is the number of people ventilated for (the most of the
count now and the forecast within `lead_min`).

`mode` says what the decision rests on:

| `mode` | Meaning | What is commanded |
|---|---|---|
| `normal` | Every input is trusted | The policy's flow; the heating schedule |
| `no_forecast` | No trained model yet | `predictive` acts as `reactive` |
| `degraded_occupancy` | The occupancy count is stale, missing or stuck (`predictive` only: no other policy uses it) | The flow for the forecast and CO₂, without the count |
| `degraded_co2` | The CO₂ sensor is stuck | Design flow |
| `degraded_temp` | The temperature sensor is stuck (heating decisions) | 21 °C |

`fault` is set when the controller found a sensor stuck (D-11) and says what
it saw, e.g. `CO₂ sensor has read 425 ppm 5 times in a row` or `Occupancy
counter has read 32 for 69 min, but CO₂ is 158 ppm above outdoor: that many
people would keep it at least 216 above`. It is empty when the count is
merely stale or missing.

A **silent** CO₂ or temperature sensor produces no decisions at all:
decisions are made per reading, so the room's actuator gets no commands, its
TTL expires and it goes to the same fallback on its own (IF-8). `viz` turns
non-normal modes into alerts (IF-11, FR-7).

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

A history query reads the run's whole file and filters it. That is fine for
training, the oracle and the evaluation, which read a run once. It is too
slow to draw graphs from, which is why `viz` keeps its own copy of the recent
past and comes here only once, when it starts (IF-11).

Model format (the per-room time-of-day profile, FR-4), written by `train`:

```json
{
  "name": "profile-train-week-v3",
  "slot_min": 15,
  "quantile": 0.8,
  "slot_quantile": 0.75,
  "trained_on": {"run_ids": ["train-week-v2"], "from": "…", "to": "…", "weekdays": 5, "readings": 360000},
  "rooms": {"level0/1570": {"mean": [0, …], "high": [0, …], "days": [5, …]}}
}
```

96 slots per room (15 min each), weekdays only. A day's value for a slot is
the `slot_quantile` of that slot's readings: at 0.75 a count has to show in
more than a quarter of them to register, so a gathering does and the
counter's stray false readings don't. `mean` is the mean of those values
across the training days; `high` their `quantile` (80 %), which is what the
controller ventilates for. A forecast below half a person is treated as an
empty room. Trained on **occupancy sensor readings**, never on the truth.
`train` takes `TRAIN_FROM` and `TRAIN_UNTIL` to bound the readings used.

**The live run's truth is not served:** `GET /history?kind=truth` or
`kind=true_occupancy` for storage's own `RUN_ID` returns `403`. The oracle
replays an earlier, finished run; the evaluation reads the export.

- **On failure:** consumers time out after 2 s. The controller keeps its last
  model (D-5); `train` exits non-zero and can be rerun.
- The oracle calls `GET /history?kind=true_occupancy` for a *recorded* run,
  never the live one (D-7).

## IF-11. The dashboard: room layers, alerts and graphs (viz)

`viz` is the only writer (§0: every collection `PUT` replaces everything). It
builds both from what the control system itself sees: the observations
(IF-4), the decisions (IF-6) and the actuator events (IF-7) on MQTT. The truth
is not on any topic it may read.

Open the viewer on a layer with
`http://127.0.0.1:9090/?layer=co2&floor=level0`, or pick one under **Layers**.

**`PUT /api/room-layers`**, all layers in one request, once per real second:

| Layer id | Label in the viewer | `source` | Range | Colours |
|---|---|---|---|---|
| `co2` | CO₂ (grey = 1000 ppm limit) | `sensor reading` | 400–1600 ppm | blue – grey – red |
| `occupancy` | People counted | `sensor reading` | 0–50 | light to dark blue |
| `occupancy_predicted` | People expected soon | `prediction` | 0–50 | light to dark blue |
| `airflow` | Airflow (% of room max) | `actuator state` | 0–100 % | light to dark blue |
| `temperature` | Temperature (grey = 22 °C) | `sensor reading` | 17–27 °C | blue – grey – red |

- **Colours follow the job of the layer.** "How much" (people, airflow) is one
  hue from light to dark, so more is always darker. "Which side of a target"
  (CO₂ against the 1000 ppm limit, temperature against the middle of the
  comfort band) is blue and red around a neutral grey, and the range is
  symmetric so that the grey sits exactly on the target. No rainbow scales.
  A unit test checks both properties on the lightness of the colours.
- **People expected soon** is the number the controller is ventilating for:
  the most of the count now and the forecast within the room's lead time
  (`predicted_occupancy` in IF-6). Under a policy that doesn't forecast the
  layer has no values and is left out.
- **Airflow** is what the damper reports in BuildSim (IF-9) as a share of the
  room's maximum from the registry (IF-12): an office's 20 l/s and a lecture
  room's 900 l/s would not fit one scale.
- **A room drops out of a layer** when its reading is more than 180 model-s
  old, or when the controller has found that sensor stuck (IF-6): either
  colour would claim something nobody measured.
- The people layers carry no unit, because the viewer prints it straight
  after the number; their labels say "People".

**`PUT /api/alerts`**, at most 100, most severe first; sent when the list
changes and again every 30 real seconds:

| Severity | Alert | Raised by | Cleared by |
|---|---|---|---|
| `critical` | CO₂ override in *room* | actuator event `override_co2` | `override_released` |
| `warning` | No CO₂ / temperature / occupancy reading from *room* | the sensor's last reading being more than 180 model-s old | its next reading |
| `warning` | CO₂ sensor / temperature sensor / occupancy counter in *room* looks stuck | a decision with a `fault` (IF-6), while the sensor is still reporting. The message is the fault and what the system does about it | the next decision without one |
| `warning` | Damper / radiator valve in *room* on fallback | actuator event `ttl_expired` | the next command it accepts |
| `warning` | Command rejected in *room* | actuator event `rejected` | 5 model-minutes later |
| `warning` | No occupancy count in *room* | a decision with `mode: degraded_occupancy` (unless the counter already has a silent or a stuck alert) | the next normal decision |
| `info` | No trained model | decisions with `mode: no_forecast`, one notice for the whole building | the next normal decisions |

- Past 100, the last entry says how many more there are.
- Alerts are the current picture only; storage is the audit trail.
- **On failure:** if BuildSim is down, `viz` logs it once and retries every
  second; the layers repair themselves within a second of BuildSim returning
  blank, the alerts within 30. If the model clock is moved back by more than
  half an hour (a new experiment), `viz` forgets the old picture.

### The graphs page

`http://127.0.0.1:8060`, served by `viz` itself. The viewer shows the building
now; this page shows how it got there. It only reads.

- **Building:** four headline numbers (people counted; ventilation power and
  its share of constant flow; rooms above the CO₂ limit; alerts), three graphs
  (ventilation power against every room at design flow; people counted and
  ventilated for; the highest and the middle room's CO₂ against the limit),
  the alerts now, their history, and a table of every room.
- **Room** (click a room): CO₂ with the limit and the override level; people
  counted and ventilated for; airflow commanded and reached, with design and
  maximum flow; temperature and setpoint with the comfort range. The times an
  alert was active in the room are shaded in all four.
- The range is the last hour, 6 hours, 24 hours or 7 days of model time.
  Pointing at a moment in one graph shows it in all of them; the arrow keys do
  the same. Every graph has its values as a table underneath.

| Call | Returns |
|---|---|
| `GET /` | The page: one HTML file, no outside dependencies |
| `GET /api/overview?minutes=N` | The building view: model time, policy, the limits to draw, every registered room's current values, the building's series, the alerts and the alert log |
| `GET /api/room?room=level0/A109&minutes=N` | One room's series and its part of the alert log. `404` for a room nothing has been heard from |

Four minutes of T-12, from 12:13: the counter is stuck at 53 in an empty
room, and at 12:16 the controller stops believing it.

```json
{
  "room": "level0/A1519",
  "size": {"capacity": 52, "design_ls": 400.6, "max_ls": 600.8},
  "start": 1789992780, "step": 60,
  "co2": [576, 582, 564, 545],
  "people": [53, 53, 53, 53], "expected": [53, 53, 53, 0],
  "target": [407.6, 407.6, 407.6, 36.6], "flow": [408, 408, 408, 408],
  "temp": [21.7, 21.9, 22, 21.8], "setpoint": [21, 21, 21, 21],
  "severity": [0, 0, 0, 1],
  "log": [{"model_time": "2026-09-21T12:16:00Z", "severity": "warning", "title": "Occupancy counter in A1519 looks stuck", "cleared": false}]
}
```

- **One value per model minute** from `start` (model time, Unix seconds). A
  minute in which nothing was seen is `null` and a hole in the line: never a
  zero, never the previous value.
- **`flow`** is what the damper reports in BuildSim (IF-9), read once per real
  second. **`target`** and **`setpoint`** are what the actuator accepted
  (status 202 in IF-6); a refused or lost command leaves a hole.
  **`expected`** is the decision's `predicted_occupancy`. **`severity`** is the
  worst alert in the room that minute: 0 none, 1 warning, 2 critical.
- **The building's series** are sums over the rooms. A room's last value
  stands in for a missing one for up to three minutes (the staleness limit),
  so a lost reading doesn't show as people leaving.
- **Ventilation power** is the summed reached airflow times 13.5 kW per m³/s:
  1.5 for the fans and 12 for heating outdoor air from 8 to 18 °C. These are
  the energy proxy's constants (NFR-3) and the two parts of it that follow
  from the airflow alone. The radiators are left out: their power depends on
  the room's true temperature, which the dashboard never sees. "Every room at
  design flow" is what the `constant` policy would use.
- **Colours.** Blue is what was measured or reached, orange what the
  controller expected or commanded, in every graph. Limits are dashed grey
  lines with their label on the line. Yellow and red mean an alert and
  nothing else, and always come with an icon and a word. The two series
  colours were checked for colour-blind separation in light and dark mode
  (worst pair ΔE 24.7 and 26.8, against a target of 8).
- **History** is held in memory, 7 model days per room. **After a restart**
  `viz` reads the run's readings and decisions so far from storage (IF-10) in
  the background. The airflow reached before the restart and the alert log
  are not in storage and start again from now. Without storage the graphs
  start empty.
- **On failure:** if the page can't reach `viz` it keeps the last picture,
  dimmed, and says so. If the model clock is moved back by more than half an
  hour, the history is dropped with the rest of the old picture.

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
