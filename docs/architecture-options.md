# Architecture options: what we must decide

Working notes for deciding the architecture before we write code. Every decision
below becomes a row in the report's **Design decisions** table (Section 4.4 of
the template), which the report guide says "carries most of the architecture
grade". Each row needs: the choice, a rejected alternative, the requirement it
serves, and the trade-off. So for each decision here: pick one option, and
write down why the other lost.

Sources: course repo `eislab-cps/D7065E` @ `80a4c59` (lab quickstart, tutorials,
course notes 2–4, report guide and worked example), Lecture 1 and Lecture 2
slides, our Week 2 proposal.

**Schedule (Lecture 1, slide 49; Canvas is authoritative):** design approval
was week 38. **Final report + repository due 12 Oct 23:59.** Demo and
individual oral exam 14, 16 or 19 Oct. The proposal's Week 3–8 plan no longer
fits, so this doc names steps (first slice, full room set, evaluation) instead
of weeks.

Companion docs: [interfaces.md](interfaces.md) (contracts, report §5),
[test-plan.md](test-plan.md) (report §10), [review-log.md](review-log.md)
(course-material checks and AI advice we corrected).

Lecture 2 shows a reference stack (physics engine with a climate API, sensor
gateway, actuator processes, Prometheus) that is **not in the course repo**:
checked on 2026-09-30, `80a4c59` is still the latest commit on every branch.
We take its *shape* as guidance and build the parts ourselves.

---

## Changes from the proposal

Record each one here with its reason. The report must state them in the
**§1 Summary "changes since the proposal" note**, which the report guide says
the examiner explicitly looks for, and discuss the consequences in §13 *Risks
and critical reflection*.

| # | Proposal said | Now | Reason | Affects |
|---|---|---|---|---|
| C-1 | The controller never sees occupancy. It estimates it from CO₂ (proposal §2) | **Each room has an occupancy sensor (people counter, BuildSim icon `occupancy_counter`).** Physics reads true occupancy from `/api/occupancy`; the `sensor-occupancy` gateway samples it from physics and writes a noisy count to that sensor, with the same sampling, noise and faults as the other sensors (D-2). The controller reads the sensor like any other device and never reads `/api/occupancy` directly | Lack of time: inverting the CO₂ mass balance is dropped from scope. Proposal §2 already named exposing occupancy directly as the fallback if the estimator proved infeasible, and said we would report it as such | D-2, D-5, D-6 step 1, D-8. The predictor, the policy and the baselines are unchanged |

Consequences of C-1:
- **Sensors per room** go from 2 to 3 (temperature and CO₂, plus the occupancy counter), which
  raises the write load in the D-3 scale check by half.
- **What stays smart:** the controller still has to *predict* occupancy ahead
  of time to start ventilating early. The counter only gives the present, and
  the room's CO₂ response lags behind it by τ. This is still the difference
  between the `reactive` and `predictive` baselines.
- **What we lose:** there is no claim of occupancy sensing without a dedicated
  sensor. Mention CO₂-based estimation as future work.
- **Degraded state** gets a new case: if the counter is stale, stuck or
  missing, fall back to CO₂-reactive control.

### Planned: study groups in occupancysim (not implemented yet)

occupancysim is ours to change (see §0). Add a rule so that some students
spend a gap between lectures in a **free lecture room** as a group, instead of
in the nearest fika room:

- A seeded group of about 3–8 people for 30–90 min, only in lecture rooms that
  aren't booked in that slot.
- New parameters in `model.md`, e.g. `study_group_chance` and
  `study_group_size_min` / `_max`, so the rule can be turned off.
- The groups go through `/api/occupancy` and `/api/entities` like everyone
  else, so BuildSim, the viewer and the evaluation's ground truth all see them.

Why: without it, lecture rooms are either booked or empty, and unplanned
occupancy never happens. The groups give the occupancy counter something to
catch that no pattern predicts. Log the change in `external/README.md` when it
is made.

### Parked: lecture timetable as a prediction input

`GET :8081/api/state` → `.lectures` lists the day's bookings (room, start, end,
seats). The predictor could read it as a booking system (use `seats`, never
`students`, which is the true head-count). This is on ice for now, so don't
design around it yet.

---

## 0. Fixed constraints: not ours to choose

These come from the course material or the proposal. The design has to respect
them, and the report should state them as boundaries.

BuildSim is provided infrastructure. **occupancysim is only a course example**:
we use it as the source of people, but we may change it (for example to add
behaviour our scenario needs). Its rows below describe its current design, not
limits we are stuck with. Changes are logged in `external/README.md`.

| Constraint | Source | Consequence for us |
|---|---|---|
| BuildSim stores **current state only**: no physics, no history, no occupancy generation | lab quickstart | We own physics, persistence and time series |
| The decision service **must not write actuator state to BuildSim itself**; it commands a separate actuator process that validates and applies | lab quickstart §3 | At least two containers on the control path: controller → actuator service → BuildSim |
| occupancysim is the **only writer** of `/api/occupancy` and `/api/entities` (full-collection PUTs) | occupancysim README | None of our other services write occupancy. New occupancy behaviour goes into occupancysim itself, so BuildSim keeps a single source of truth |
| **Every collection `PUT` replaces the whole collection**: room layers, alerts, effects, entities, occupancy | BuildSim visualization API | Only `viz` writes room layers and alerts. Other services publish events (overrides, degraded mode) on MQTT and `viz` turns them into alerts |
| Sensor values and actuator states are **strings** | BuildSim API | Parse and format at every boundary |
| No bulk sensor write: one `PUT /api/sensors/{id}/value` per sensor. Bulk **read** exists: `GET /api/equipment?level=…&category=…` | BuildSim equipment API | Write rate, not read rate, limits how many rooms we can simulate |
| Container diagram (C4 L2) should map **1:1** onto `compose.yaml` and repo folders | report guide §2.2 | Decide containers once; the folders follow |
| "A network boundary is justified when a component must be independently started, stopped, restarted, isolated, deployed, or replaced" | course notes 2 | Don't split into services just to look distributed |
| **Each sensor/gateway and each actuator is its own process**, independently startable, stoppable and restartable. One monolithic script = U | Lecture 1, slides 18 and 24 (pass/fail) | Sensor gateways and actuators are separate containers (D-2, D-9) |
| Processes share **no variables and no files, ever**; they talk only over HTTP or MQTT | Lecture 1, slide 18 | Storage is a service, not a shared folder (D-4, D-5) |
| A process that crashes **re-registers with BuildSim and carries on**; killing any process should degrade the system, not kill it | Lecture 1, slide 17 | Registration at start must be idempotent; every consumer needs a fallback |
| **The truth never enters BuildSim** or anything the controller can read | Lecture 2, slide 8 | Physics serves truth only to gateways and storage; the viewer shows measured values (D-8) |
| A data-driven component is required; threshold rules alone are not enough | Lecture 1, slide 22 | The occupancy predictor (FR-4) is that component |
| **Our scope:** the controller never reads `/api/occupancy` directly. It sees occupancy only through the per-room occupancy sensor (changed from the proposal, see C-1) | proposal §2, C-1 | Only physics reads `/api/occupancy`; evaluation uses the truth recorded by storage |

### Facts about BuildSim and the example occupancysim that shape the design

- **Simulated clock.** occupancysim runs at 60 simulated seconds per real
  second by default. Its clock is readable at `GET :8081/api/state` → `.clock`
  (`time`, `minute_of_day`, `weekday`, `factor`, `running`) and streamed on
  `GET :8081/api/events`. BuildSim carries no time at all.
- **Reproducible days.** Each day's plans are generated from the parameters
  plus a seed, so the same date and config give the same day. This is what
  makes the **oracle baseline** possible (see D-7).
- **Room roles by area.** Office ≤ 40 m², lecture ≥ 60 m², fika ≥ 100 m² (3 per
  floor). Floor-plan units are 0.5 m, and `rooms[].area` is in the floor-plan
  JSON, so the physics model gets each room's floor area from there. Assume a
  ceiling height (e.g. 2.7 m) to get the volume, and state that assumption.
- **Occupancy pattern.** Fika breaks are around 09:45 and 14:30. Lectures run in
  the 08:15, 10:15, 13:15 and 15:15 slots. Lunch is 12:00–13:00. The proposal's
  scenario ("fika ~11:30", "13:00 meeting") should be updated to match.
- **Size.** Rooms (excluding corridors): level0 277, level1 309, level2 252.
  By role on level0: 164 offices and 51 rooms ≥ 60 m², of which 3 are fika rooms
  and the rest lecture rooms.
- **Lectures move between rooms.** For each slot, occupancysim draws the
  floor's lecture rooms *in random order* and books only as many as the demand
  needs. That is about one lecture per floor per slot, in a different room each
  slot and each day (model.md, "Lectures"). So a single lecture room has no
  regular time-of-day pattern: it is usually empty and occasionally full. The
  predictable rooms are the **fika rooms** (09:45, 14:30) and the **offices**
  (weekday working hours, 1 person). For lecture rooms the only warning is
  students arriving 10 min early, which the occupancy counter shows. This limits
  what FR-4 can achieve and should be discussed in the report. (The planned
  study groups add unbooked use on top; the parked timetable idea would
  address the rest.)

---

## D-1. Who owns time, and how the physics steps

Everything (physics dt, sensor sampling, CO₂ rate-of-change, prediction lead
time, "minutes above 1000 ppm") must use **model time**, not wall time.
Otherwise the room time constants from the proposal are meaningless.

| Option | Pro | Con |
|---|---|---|
| **A. Physics reads occupancysim's clock each tick and integrates over the clock delta. Its truth API returns model time with every value, so sensor gateways stamp readings with it. Downstream services use those timestamps only** | One clock for the physics; the observation path never needs its own clock | Physics depends on occupancysim being up |
| B. Every service polls occupancysim's clock | Simple | Several clock readers, skew between them |
| C. Each service runs its own `wall × factor` clock | No dependency | Drifts as soon as someone pauses or jumps the clock in the UI |

**Recommendation: A.** One exception: the actuators check command TTLs
(D-6), so they also read the clock. Keep that to physics and the actuators.

Numbers to keep in mind:
- **Evaluation takes real time.** At factor 60, 5 simulated weekdays take
  2 real hours, and every baseline needs its own run: ~8 h for all four
  (NFR-8). Weekends are empty, so jump the clock over them. At factor 600 a run
  takes ~12 min, but then each 1 s tick is 10 model minutes.
- **The integration step must stay small.** Explicit Euler on a first-order
  model needs dt well below τ. With a CO₂ τ of ~12–60 min (see *Ventilation
  sizing*), cap dt at ~1 model minute and sub-step when the clock jumps further.
  A clock jump (UI "skip to 13:00") must not produce one giant step.

## D-2. Sensors: where noise, sampling and faults live

Lecture 1 settles the process question: each sensor/gateway must be its own
process (slide 24, pass/fail). Lecture 2 (slides 7–8) shows the intended shape:
the physics engine exposes the truth, a sensor gateway samples it and degrades
it, and **the truth is never published to BuildSim**.

| Option | Pro | Con |
|---|---|---|
| A. Physics adds noise and writes sensor values to BuildSim itself | Fewest parts | **Breaks the course rule**: no sensor process. Also puts truth and observation in one process |
| **B. Physics serves the truth over its own HTTP API (`GET /rooms?level=level0` → CO₂, temperature, occupancy, model time per room). One `sensor` binary runs as three gateway containers, one per kind: `sensor-co2`, `sensor-temp`, `sensor-occupancy`. Each samples the truth every 60 model-s, adds noise and faults (stuck, dropout, drift, lag), and `PUT`s the reading to BuildSim** | Matches Lecture 2. Truth stays out of BuildSim, so the controller can't read it by accident. Killing `sensor-co2` in the demo exercises degraded mode (FR-7) while the other kinds keep running | Three more containers; the truth API is a new interface to specify |
| C. One gateway per room | Real per-device isolation | 150 containers |

The course material is not consistent here. The lab quickstart diagram and the
report guide (§3.2) have the *physics* write sensor values into BuildSim and
sensor processes read them back and publish onward. Lecture 1 (slide 16) and
Lecture 2 (slide 7) have sensor processes write the readings. We follow the
lectures because they keep the truth out of BuildSim. Say so in the report and
name the quickstart shape as the rejected alternative.

**Recommendation: B.** Each gateway registers its equipment at start
(`POST /api/equipment/bulk` skips ids that already exist), so a restart
re-registers without harm (Lecture 1, slide 17). Fault injection is config on
the gateway, e.g. `FAULT=stuck:level0/A109`.

**Scale check:** 3 sensors × 50 rooms = 150 PUTs per sample, now split across
three gateways (50 each). The whole building (838 rooms) would be ~2 500 PUTs
per sample, and each one triggers a viewer broadcast.

## D-3. Transport: getting observations to their consumers

Who needs the observations: **controller** (live), **storage** (training,
evaluation), **dashboard** (`viz`) and the **actuator's safety check** (D-6).
That is four consumers.

| Option | Pro | Con |
|---|---|---|
| A. Everyone polls BuildSim over REST | Nothing extra to run; easiest to trace | No history unless someone records it; polling couples every consumer to BuildSim |
| **B. Gateways `PUT` each reading to BuildSim *and* publish it to MQTT (`obs/<level>/<room>/<kind>`, JSON with model time, sequence number and quality flag); consumers subscribe** | Fan-out to several consumers is the case course notes 2 and Lecture 1 (slide 40) give for a broker. BuildSim stays the shared state the viewer shows | A broker to run; duplicates and outages must be handled (course notes 2 warns MQTT QoS is not end-to-end). Two writes per reading are not atomic |
| C. A separate collector polls BuildSim and publishes to MQTT | Gateways stay simple | An extra process and an extra hop of latency for data the gateway already had |

**Recommendation: A for the first slice (one room), B once storage and
more rooms arrive.** Record the switch and its reason. Evidence like "REST
polling of N rooms took X ms per cycle" is exactly the evaluated trade-off the
report asks for, and the proposal already names this risk.

With B, the controller keeps REST polling of BuildSim as its **fallback when
the broker is down**. That is a degraded path worth testing (NFR-6).

## D-4. Storage

What must be answerable:
- controller: last ~30 model-minutes per room (kept in its own memory)
- training: days of per-room history keyed by time-of-day and weekday
- evaluation: 5 simulated weekdays per baseline, *with ground truth* next to
  the observations

Lecture 1 (slide 18): processes share **no files, ever**. They talk only in
messages. So the training job may not read a file the logger writes, and the
controller may not load a model file the trainer writes.

| Option | Pro | Con |
|---|---|---|
| **A. A `storage` service owns the data. It subscribes to `obs/#`, `truth/#`, `cmd/#` and `decision/#`, appends JSONL per run to its own volume, and serves it over HTTP: `GET /history?room=&kind=&from=&to=`, `PUT/GET /models/{name}`** | Transparent, replayable, one owner. Course notes 3 recommend JSONL as the laptop baseline ("bronze"). Satisfies "no shared files" | We write a small query API ourselves |
| B. TimescaleDB for everything | Live SQL queries for dashboard and controller | A DB server to run, schema migrations; hard to justify at 50 rooms |
| C. JSONL files shared between logger, trainer and controller | Least code | **Breaks the course rule** on shared files |

**Recommendation: A.** Tag every run with an experiment id (baseline, seed,
date, config hash). After a run, analysis for the report (DuckDB over the
exported JSONL) happens offline and by hand, not as a running process, so it
does not count as a shared file. DuckDB from Go needs cgo, so use the CLI or a
notebook rather than linking it into a service.

## D-5. Where the predictor and trainer live

| Option | Pro | Con |
|---|---|---|
| **A. Sensor validation + predictor + policy as Go packages in one `controller` container. Training is a batch container (`train`, a compose profile) that fetches history from storage, builds a per-room time-of-day profile, and `PUT`s it to storage. The controller fetches the latest model at start and every few model-hours, and keeps the last good copy in memory** | Critical path stays in one process with no network hops, which gives the Level 3 component diagram real content. Training is off the control path (the worked example's "heavy analysis offline"). Storage down → the controller keeps its current model | Validation and prediction can't be restarted separately from the policy |
| B. Predictor as its own service, publishing predictions | Reusable by the dashboard directly | A network boundary with no independent-deployment need, which course notes 2 advise against |

With C-1 the "estimator" shrinks to reading and validating the occupancy
sensor (staleness, stuck values), so B loses its main reason to exist.

**Recommendation: A.** No model at all → the controller runs `reactive` and
says so in its decision record. It publishes its prediction and decision per
room to `decision/<level>/<room>`, for storage and `viz`.

## D-6. Decision route and safety boundary

Course notes 4: pick one main route. Ours is **feedback/rule control with a
forecast feed-forward**, laid out as Lecture 1's two loops (slide 11):

1. Observe now: read the room's occupancy sensor (C-1). This replaces the proposal's
   inversion of the CO₂ mass balance.
2. Plan (slow loop): the predicted occupancy one lead time ahead sets the
   room's target airflow and setpoint.
3. React (fast loop): rule/hysteresis control within those targets, with a
   setback when empty.

Where the safety policy lives:

| Option | Pro | Con |
|---|---|---|
| **A. The actuator enforces it, independently of the controller: range, rate of change, command TTL in model time, the occupied/unoccupied flow floors, and the CO₂ > 1100 ppm override. The damper actuator subscribes to the CO₂ observations itself** | Survives a crashed or wrong controller, which is Lecture 1's "independent, bounded override" (slide 11). Controller dies → commands expire → design flow, and the override still works | The actuator is no longer a plain device; it needs the ventilation sizing table and the CO₂ stream |
| B. Safety check inside the controller, after the policy | Simple, one place for all logic | Dies with the controller; not independent |
| C. A separate `safety` process | Cleanest separation | Another process, and it still has to get between controller and actuator |

**Recommendation: A.** The controller proposes; the actuator disposes. That is
the propose-vs-apply split the quickstart asks for.

Also needed:
- **Degraded state.** If a sensor is stale or stuck, fall back to reactive
  control, or to design flow if CO₂ itself is missing (FR-7).
- **Hysteresis and dwell times**, measured in model time.

## D-7. Baselines and the oracle

Implement the policy as an interface with four implementations chosen by a
flag: `constant`, `reactive`, `predictive`, `oracle`. Everything else stays
identical between runs, which is what makes the comparison fair.

The oracle needs *future* occupancy. occupancysim does not expose its plans, but
days are reproducible from seed and date. So:

1. Run the day once; storage records the true occupancy that physics publishes on `truth/#`.
2. Rerun the same seed and date with `oracle`. It fetches that recording from
   storage (`GET /history?kind=true_occupancy…`), shifted by the lead time. It
   never reads the live truth.

**Verify as soon as storage runs that two runs with the same seed give identical
occupancy.** If they don't, the oracle has to be designed differently. Because
occupancysim is ours to change, exposing its day plans is the fallback.

## D-8. Dashboard

The report guide (§3.12) accepts either "BuildSim's session API or a separate
dashboard". Lecture 1 lists the dashboard as its own process (slide 17).

| Option | Pro | Con |
|---|---|---|
| **A. A `viz` container, the only room-layer publisher. It subscribes to `obs/#` and `decision/#` and `PUT`s combined room layers (measured CO₂, measured occupancy, predicted occupancy, airflow) plus `/api/alerts` for overrides and degraded mode** | The 3D view from the proposal for free; `viz` is our own process, so it counts as the dashboard | No history in the viewer |
| B. Own web dashboard (time-series charts, e.g. Grafana over storage) | Shows history, which the viewer can't | A container to build or configure |

**Recommendation: A, plus plots generated offline for the report.** Add B only
if time allows before 12 Oct. Show **measured** CO₂, not true CO₂: the truth
never enters BuildSim (Lecture 2, slide 8). The truth appears only in the
evaluation.

Two points from the lab quickstart to respect in the report:
- It shows truth in a room layer (`"source": "simulation truth"`), which
  Lecture 2 forbids. We follow Lecture 2.
- "A room layer is physical/estimated state, a sensor is an observation."
  Label the CO₂ layer as the latest *reading* (source: sensor), and treat the
  predicted-occupancy layer as the estimated state. The viewer is provided
  infrastructure: describe what we map into it, and don't claim the viewer as
  ours.

## D-9. Actuator process shape

Lecture 1 (slide 24): each actuator must be its own process. Lecture 2 (slide 7)
shows actuators that **travel** towards the command and then report the state
they reached, with faults like stuck or slow.

| Option | Pro | Con |
|---|---|---|
| **A. One `actuator` binary run as two containers by kind: `actuator-damper` and `actuator-heating`, each for all 50 rooms. `POST /rooms/{level}/{room}/command {value, cmd_id, issued_at}`, idempotent on `cmd_id`. Moves towards the target at a fixed rate in model time, `PUT`s the reached state to BuildSim, and physics reads it back from there** | Two failure domains instead of one. Registers its equipment at start, like the gateways. Faults via config | A crash affects every room of that kind |
| B. One actuator container per room and kind | Real per-device isolation | 100 containers to manage |
| C. One actuator container for everything | Fewest containers | Heating and ventilation fail together |

**Recommendation: A.** Say in the report that per-device isolation was traded
for operability, and that one process stands in for a gateway to many devices.

---

## What the recommended options add up to (draft container view)

```
      ┌──────────── from course ──────────────────────────────┐
      │ occupancysim ──PUT occupancy──► BuildSim (:9090) ◄──── browser
      │   (clock :8081)                   ▲      ▲     ▲      │
      └──────┬──────────────────────────────┼──────┼─────┼──────┘
             │ clock                 PUT readings  │     │ room layers, alerts
             ▼                              │      │     │
 ┌─────────────────────┐  GET truth  ┌──────┴──────┐ │   ┌───┴─┐
 │ physics             │◄────────────┤ sensor-co2  │ │   │ viz │
 │ reads occupancy +   │             │ sensor-temp │ │   └──▲──┘
 │ actuator state from │             │ sensor-occ. │ │      │
 │ BuildSim; truth API │             └──────┬──────┘ │      │
 └────────┬────────────┘                    │ obs/#  │      │
          │ truth/#                         ▼        │      │
          └──────────────────────────► ┌──────────┐  │      │
                                       │ mosquitto├──┼──────┤
 ┌───────────────────────┐   obs/#     └─┬───┬────┘  │      │
 │ controller            │◄──────────────┘   │       │      │
 │ validate→predict→act  │── decision/# ────►│       │      │
 └──┬─────────────▲──────┘                   ▼       │      │
    │ POST command│ GET model          ┌─────────┐   │      │
    ▼             └────────────────────┤ storage │   │      │
 ┌──────────────────┐   PUT state      │ (JSONL) │   │      │
 │ actuator-damper  ├──────────────────┼─────────┼───┘      │
 │ actuator-heating │ CO₂ for override └────▲────┘          │
 └──────────────────┘ (obs/#)               │ history/model │
                                       ┌────┴────┐
                                       │  train  │ (batch)
                                       └─────────┘
```

Our containers: `physics`, `sensor-co2`, `sensor-temp`, `sensor-occupancy`,
`actuator-damper`, `actuator-heating`, `controller`, `storage`, `viz`, `train`
(batch), plus `mosquitto`. **The first slice** needs only `physics`, `sensor-co2`,
`controller` (reactive policy) and `actuator-damper`, for one room, talking REST.

Proposed repo layout (Go, one module, one `cmd/` per binary; `sensor` and
`actuator` each run as several containers):

```
cmd/physics  cmd/sensor  cmd/actuator  cmd/controller  cmd/storage  cmd/viz  cmd/train
internal/buildsim   # thin client (or import external/buildingsim/pkg/client)
internal/model      # observation / command / decision record types + JSON schema
internal/clock      # model-time source (occupancysim)
internal/faults     # noise, stuck, dropout, drift
internal/predict  internal/policy  internal/sizing
compose.yaml  docs/  external/
```

---

## Requirements (draft, copy into report §3)

The numbers are starting targets, so change them if you need to, but keep every
row testable. Decision rows cite these IDs, and every ID must reach a test in
report §10.

### Functional

| ID | Requirement | Verified by |
|---|---|---|
| FR-1 | Physics simulates CO₂ (mass balance) and temperature (heat balance) per room with first-order dynamics. It is driven by true occupancy and the commanded airflow and setpoint, and integrates in model time with dt ≤ 1 model-min | Unit test: a step response matches the analytic τ within 5 % |
| FR-2 | Three sensor gateways (CO₂, temperature, occupancy count; C-1) sample each room's truth from physics every 60 model-s and publish readings to BuildSim, with noise: CO₂ ±(30 ppm + 3 %), temperature ±0.3 °C, count ±1 | Unit test on the noise package; inspect a logged run |
| FR-3 | The controller sets the airflow level and heating setpoint for every room in the room set, once per control period | Integration test: one room end to end (first slice) |
| FR-4 | The controller predicts each room's occupancy one lead time ahead (lead time ≈ the room's CO₂ τ). The model is trained offline from stored history | Offline test: prediction error (MAE) on a held-out day beats a naive "same as now" prediction |
| FR-5 | The actuator validates every command before applying it: value range, rate of change, TTL in model time, and duplicates (idempotent on `cmd_id`). It rejects invalid commands with a reason, and moves towards accepted targets at a fixed rate | Unit tests with out-of-range, stale and duplicate commands |
| FR-6 | Safety override, enforced in the damper actuator independently of the controller (D-6): CO₂ > 1100 ppm gives maximum airflow (1.5 × q_design) no matter what the policy says. The flow never drops below 0.35 l/s·m² during occupied hours or 0.10 l/s·m² otherwise (see *Ventilation sizing*) | Test with an injected CO₂ spike and a policy that asks for zero flow |
| FR-7 | Degraded mode: if a sensor is stale (> 3 missed samples) or stuck, the controller falls back. A bad occupancy sensor means reacting to CO₂; a bad CO₂ sensor means design flow. It publishes the mode change, which `viz` shows as an alert | Fault-injection test (stuck, dropout) |
| FR-8 | Every observation, ground-truth value, command and decision is stored with model time and a run id | Check: a run's JSONL files can rebuild its report plots |
| FR-9 | The policy is chosen by a flag: `constant`, `reactive`, `predictive`, `oracle`. Everything else is identical between runs | Config diff between runs shows only the policy changed |
| FR-10 | The BuildSim viewer shows measured CO₂, measured and predicted occupancy, and airflow per room, plus alerts for overrides and degraded mode | Demo and screenshot |
| FR-11 | Every sensor gateway and actuator registers its equipment in BuildSim at start, so a restarted process carries on without manual steps | Test: delete a gateway's equipment, restart it, check it reappears |

### Non-functional

| ID | Requirement | Verified by |
|---|---|---|
| NFR-1 | **Air quality:** CO₂ ≤ 1000 ppm for ≥ 95 % of occupied room-minutes over the evaluation period of 5 simulated weekdays (`predictive`) | Evaluation run |
| NFR-2 | **Comfort:** 20–24 °C for ≥ 95 % of occupied room-minutes | Evaluation run |
| NFR-3 | **Energy:** energy proxy (fan power + delivered heat) ≥ 30 % below `constant`, and no worse than `reactive` while meeting NFR-1 better than it does | Compare baselines |
| NFR-4 | **Latency:** a command shows up in BuildSim within 1 control period (≤ 1 model-min, which is ~1 s real at factor 60) | Timestamps in the log, reported as p95 |
| NFR-5 | **Scale:** for the chosen room set (50 rooms, 150 sensor writes per sample, which is 150 PUTs per real second at factor 60 and a 60 model-s sample period), a full sample cycle finishes within one sample period | Load test. This is also the REST → MQTT evidence for D-3 |
| NFR-6 | **Robustness:** any one of our containers can be killed and restarted without an unsafe state. The actuator falls back to design flow when commands expire (TTL). The loop resumes within 30 s real | Chaos test: `docker kill` each service |
| NFR-7 | **Reproducibility:** same seed and date give identical occupancy, and every number in the report traces to a run id | Two runs diffed (also the D-7 check) |
| NFR-8 | **Evaluation cost:** 5 simulated weekdays per baseline run in ≤ 2.5 h real, so all 4 baselines fit in one day. The last day to start them is ~9 Oct | Timed run |

Where the numbers come from:
- **1000 ppm:** the usual indoor-air guideline, and the proposal already uses it.
- **20–24 °C:** the heating-season comfort band in EN 16798-1 category II.
- **Design flow:** see *Ventilation sizing* below. It defines `constant` and
  the flow limits in FR-6.
- **NFR-3 is relative on purpose.** The proposal (§7) treats the gap between
  `reactive` and `oracle` as a result to report, not a target, so we don't
  promise a margin over `reactive`.

### Ventilation sizing

Per room, from the Swedish workplace rule (AFS: 7 l/s per person plus
0.35 l/s·m² for sedentary work) and the design capacities occupancysim uses for
each room role:

```
q_design = 7 l/s × capacity + 0.35 l/s·m² × area
capacity: office 1 · lecture area/2 · fika area/4   (occupancysim model.md)
```

| Flow level | Value | Used by |
|---|---|---|
| Maximum | 1.5 × q_design | FR-6 override (CO₂ > 1100 ppm); the upper bound of the actuator range |
| Design | q_design | `constant` baseline; actuator fallback when a command's TTL expires (NFR-6); degraded mode without CO₂ (FR-7) |
| Occupied minimum | 0.35 l/s·m² | FR-6 floor during occupied hours |
| Unoccupied minimum | 0.10 l/s·m² | Setback floor when empty (Boverket BBR minimum for when nobody is present) |

Other physics constants: ceiling height 2.7 m, outdoor CO₂ 420 ppm, and
0.005 l/s of CO₂ per seated adult (≈ 18 l/h).

Sanity check (τ = V / q):

| Room | Area | Capacity | q_design | τ at design | Steady CO₂ at capacity, design flow |
|---|---|---|---|---|---|
| Office (median) | 18.5 m² | 1 | 13.5 l/s | ~62 min | ~790 ppm |
| Lecture | 80 m² | 40 | 308 l/s | ~12 min | ~1070 ppm (~910 ppm at the 75 % fill target) |
| Fika | 120 m² | 30 | 252 l/s | ~21 min | ~1010 ppm |

At the occupied minimum, τ is ~2 h in every room, so a room that fills while
ventilation sits at the floor is far behind. That is the lag the prediction is
meant to beat. A full room at design flow settles right around 1000 ppm, which
is why the maximum sits above design.

---

## Decision record (fill in together, then copy into report §4.4)

| # | Decision | Chosen | Rejected alternative | Requirement served | Trade-off accepted |
|---|---|---|---|---|---|
| D-1 | Time source | Physics reads occupancysim's clock; observations carry model time | Every service keeps its own `wall × factor` clock | FR-1, NFR-4 | Physics depends on occupancysim being up |
| D-2 | Sensor noise/faults | Separate gateway per sensor kind, sampling a truth API on physics | Physics writes noisy values to BuildSim itself | FR-2, FR-7, FR-11 | Three extra containers and a truth API to specify |
| D-3 | Transport | REST for the first slice; gateways also publish to MQTT once storage exists | A separate collector polling BuildSim | NFR-5, NFR-6 | A broker to run; two non-atomic writes per reading |
| D-4 | Storage | A `storage` service owning JSONL per run, served over HTTP | JSONL files shared between processes | FR-8, NFR-7 | A small query API to write |
| D-5 | Decomposition of autonomy | Validation, prediction and policy in one controller; training as a batch job | Predictor as its own service | FR-3, FR-4 | Parts of the controller can't be restarted separately |
| D-6 | Safety filter placement | In the actuator, independent of the controller | Inside the controller, after the policy | FR-5, FR-6, NFR-6 | The actuator needs sizing data and the CO₂ stream |
| D-7 | Baselines/oracle | Policy interface with four implementations; the oracle replays recorded truth | Oracle reading occupancysim's plans | FR-9, NFR-7 | Depends on same-seed runs being identical |
| D-8 | Dashboard | `viz` process publishing room layers and alerts to the BuildSim viewer | Own web dashboard | FR-10 | No history in the live view; plots come from storage offline |
| D-9 | Actuator granularity | One process per actuator kind (damper, heating) for all rooms | One process per room | FR-5, NFR-6 | A crash affects every room of that kind |
| — | **Room set (scale)** | 50 rooms on level0: the 3 fika rooms, 20 lecture rooms and 27 offices, as a fixed list in config (a seeded random sample, so it is reproducible) | Whole building (838 rooms, ~2 500 PUTs per sample) or all of level0 (277 rooms) | NFR-5 | Most of level0's lecture rooms are left out, so many lectures land in rooms we don't simulate. With 20 of ~48, about 40 % of the lectures fall inside the set |

Each row cites a requirement ID from the section above. The report guide
stresses traceability: every requirement → a component → a test.
