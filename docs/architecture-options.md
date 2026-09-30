# Architecture options: what we must decide

Working notes for deciding the architecture before Week 3 code. Every decision
below becomes a row in the report's **Design decisions** table (Section 4.4 of
the template), which the report guide says "carries most of the architecture
grade". Each row needs: the choice, a rejected alternative, the requirement it
serves, and the trade-off. So for each decision here: pick one option, and
write down why the other lost.

Sources: course repo `eislab-cps/D7065E` @ `80a4c59` (lab quickstart, tutorials,
course notes 2–4, report guide and worked example), our Week 2 proposal.

---

## Changes from the proposal

Record each one here with its reason. The report must state them too
(report §13, *Risks and critical reflection*, and wherever the proposal's
claim would otherwise be assumed).

| # | Proposal said | Now | Reason | Affects |
|---|---|---|---|---|
| C-1 | The controller never sees occupancy. It estimates it from CO₂ (proposal §2) | **Each room has an occupancy sensor (people counter, BuildSim icon `occupancy_counter`).** Physics reads true occupancy from `/api/occupancy` and writes it to that sensor, with the same sampling and noise and faults as the other sensors (D-2). The controller reads the sensor like any other device and never reads `/api/occupancy` directly | Lack of time: inverting the CO₂ mass balance is dropped from scope. Proposal §2 already named exposing occupancy directly as the fallback if the estimator proved infeasible, and said we would report it as such | D-2, D-5, D-6 step 1, D-8. The predictor, the policy and the baselines are unchanged |

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
| `PUT /api/room-layers` **replaces the whole collection** | BuildSim visualization API | Only one of our services may publish room layers, or they overwrite each other |
| Sensor values and actuator states are **strings** | BuildSim API | Parse and format at every boundary |
| No bulk sensor write: one `PUT /api/sensors/{id}/value` per sensor. Bulk **read** exists: `GET /api/equipment?level=…&category=…` | BuildSim equipment API | Write rate, not read rate, limits how many rooms we can simulate |
| Container diagram (C4 L2) should map **1:1** onto `compose.yaml` and repo folders | report guide §2.2 | Decide containers once; the folders follow |
| "A network boundary is justified when a component must be independently started, stopped, restarted, isolated, deployed, or replaced" | course notes 2 | Don't split into services just to look distributed |
| Canvas may state required process boundaries | course notes 2 | **Check Canvas before finalising D-2 and D-5** |
| **Our scope:** the controller never reads `/api/occupancy` directly. It sees occupancy only through the per-room occupancy sensor (changed from the proposal, see C-1) | proposal §2, C-1 | Only physics and evaluation may read `/api/occupancy` |

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
| **A. Physics reads occupancysim's clock each tick, integrates over the clock delta, and stamps every observation with model time. Downstream services use that timestamp only** | One clock source; downstream never needs the clock | Physics depends on occupancysim being up |
| B. Every service polls occupancysim's clock | Simple | Several clock readers, skew between them |
| C. Each service runs its own `wall × factor` clock | No dependency | Drifts as soon as someone pauses or jumps the clock in the UI |

**Recommendation: A.**

Numbers to keep in mind:
- **Evaluation takes real time.** At factor 60, one simulated week takes 2.8
  real hours, and every baseline needs its own week. At factor 600 it takes
  ~17 min, but then each 1 s tick is 10 model minutes.
- **The integration step must stay small.** Explicit Euler on a first-order
  model needs dt well below τ. With a CO₂ τ of ~20–30 min, cap dt at ~1 model
  minute and sub-step when the clock jumps further. A clock jump (UI "skip to
  13:00") must not produce one giant step.

## D-2. Sensors: where noise and sampling live

The proposal promises "realistic sampling intervals and noise". The quickstart
separates the *physical model* (writes sensor values) from *sensor processes*
(read readings, publish to the pipeline).

| Option | Pro | Con |
|---|---|---|
| **A. Physics keeps the true state; it writes noisy, sampled sensor values to BuildSim sensors and the truth to a room layer (`source: "simulation truth"`)** | Matches the quickstart's "a sensor is an observation, a room layer is state". Truth is available for evaluation | Physics is also the room-layer publisher (see the constraint above) |
| B. Separate sensor-emulator container reads truth and adds noise, sampling and faults | Fault injection (stuck, dropout, drift) is isolated and easy to demo | One more container, and truth must travel physics → sensor-emulator somehow |
| C. Physics writes perfect values; noise is added in the collector | Fewest parts | Noise in the wrong place: the controller's input no longer passes through BuildSim like a device |

**Recommendation: A, with noise and fault injection as a separate package inside
physics.** Upgrade to B only if you want fault injection as a demo feature. The
worked example makes a "stuck sensor" a safety risk, so graders will look for
failure handling.

## D-3. Transport: getting observations to their consumers

Who needs the observations: **controller** (live), **storage/history**
(training, evaluation), **dashboard**, and later the **evaluator**. That is
3–4 consumers.

| Option | Pro | Con |
|---|---|---|
| A. Everyone polls BuildSim over REST | Nothing extra to run; easiest to trace | No history unless someone records it; polling couples every consumer to BuildSim |
| **B. One collector polls BuildSim (bulk `GET /api/equipment?category=hvac`) and publishes observations to MQTT (Mosquitto); consumers subscribe** | Fan-out to several consumers is the case course notes 2 gives for a broker; the collector adds model time, sequence number and quality flags in one place | A broker to run; duplicates and outages must be handled (course notes 2 warns MQTT QoS is not end-to-end) |
| C. Collector pushes to consumers over HTTP | No broker | The collector must know every consumer and retry for each |

**Recommendation: A for Week 3 (one room), move to B in Week 4 when storage and
more rooms arrive.** Record the switch and its reason. Evidence like "REST
polling of N rooms took X ms per cycle" is exactly the evaluated trade-off the
report asks for. The proposal already names this risk.

**Scale check (applies to every option):** physics writes 3 sensors × N rooms
per sample (temperature, CO₂ and occupancy; see C-1). The whole building (838 rooms)
would be ~2 500 PUTs per sample, and
each one triggers a viewer broadcast. **Room set: 50 rooms on level0** (see the
decision record): 150 PUTs per sample. That is still enough to show that
"all rooms" works.

## D-4. Storage

What must be answerable:
- controller: last ~30 model-minutes per room (CO₂ slope, current estimate)
- training: days of per-room history keyed by time-of-day and weekday
- evaluation: a full simulated week per baseline, *with ground truth* next to
  the estimates

| Option | Pro | Con |
|---|---|---|
| **A. Append-only JSONL (raw, one file per run) + in-memory window in the controller + DuckDB CLI for training and evaluation queries** | Transparent, replayable, zero servers. Course notes 3 recommend it as the laptop baseline ("bronze") | No live range queries for a dashboard |
| B. TimescaleDB for everything | Live SQL queries for dashboard and controller | A DB server to run, schema migrations; hard to justify at 50 rooms |
| C. SQLite (pure Go: `modernc.org/sqlite`) | Queryable, single file, no server | Concurrent writers need care |

**Recommendation: A.** Tag every run with an experiment id (baseline name,
seed, date, config hash). Evaluation is then just DuckDB queries over
`runs/<id>/*.jsonl`, and the numbers in the report can be traced to a run.
Note that DuckDB from Go needs cgo, so use the DuckDB CLI or a notebook for
analysis rather than linking it into a service.

## D-5. Where the estimator, predictor and trainer live

| Option | Pro | Con |
|---|---|---|
| **A. Estimator + predictor + policy as Go packages inside one autonomous-service container; training as a separate batch job (`cmd/train`) that reads JSONL and writes a model file (per-room time-of-day profile as JSON) the controller loads at start or on SIGHUP** | Critical path stays in one process with no network hops; this gives the Level 3 component diagram real content; training is off the control path (the worked example's "heavy analysis offline" decision) | Estimator can't be restarted independently |
| B. Estimator as its own service, publishing occupancy estimates | Estimator reusable by the dashboard directly | A network boundary with no independent-deployment need, which course notes 2 advise against |

With C-1 the "estimator" shrinks to reading and validating the occupancy
sensor (staleness, stuck values), so B loses its main reason to exist.

**Recommendation: A.** The controller then publishes its observation, prediction and decision
per room (to MQTT or the log), and one service assembles room layers for the
viewer (see D-8).

## D-6. Decision route and safety boundary

Course notes 4: pick one main route. Ours is **feedback/rule control with a
forecast feed-forward**:

1. Observe now: read the room's occupancy sensor (C-1). This replaces the proposal's
   inversion of the CO₂ mass balance.
2. Predict: per-room time-of-day profile → gradient boosting only if needed.
3. Act: rule/hysteresis controller that starts early, with a setback when empty.

What course notes 4 say a threshold controller still needs, and what to decide:

- **Safety filter (Simplex-style runtime assurance).** A simple override sits
  after the smart policy, e.g. CO₂ > 1100 ppm → full ventilation whatever the
  prediction says, and a minimum fresh-air flow during occupied hours. It lives
  in the actuator service (validation) or in the controller. Recommendation: the
  controller proposes; the **actuator service enforces hard limits** (range,
  rate-of-change, command TTL in model time). That is exactly the
  propose-vs-apply split the quickstart asks for.
- **Degraded state.** If a sensor is stale or stuck, fall back to reactive
  control, or to design flow if CO₂ itself is missing.
- **Hysteresis and dwell times**, measured in model time.

## D-7. Baselines and the oracle

Implement the policy as an interface with four implementations chosen by a
flag: `constant`, `reactive`, `predictive`, `oracle`. Everything else stays
identical between runs, which is what makes the comparison fair.

The oracle needs *future* occupancy. occupancysim does not expose its plans, but
days are reproducible from seed and date. So:

1. Run the day once and record the ground truth that physics reads from `/api/occupancy`.
2. Rerun the same seed and date with `oracle`, fed from that recording shifted by the lead time.

**Verify early (Week 4) that two runs with the same seed give identical
occupancy.** If they don't, the oracle has to be designed differently.

## D-8. Dashboard

| Option | Pro | Con |
|---|---|---|
| **A. BuildSim viewer: one publisher sends all room layers together (true CO₂, measured occupancy, predicted occupancy, fan level) plus `/api/alerts` for decisions** | The 3D view from the proposal for free; no extra web app | The report must say the viewer is provided infrastructure (quickstart) and what *we* map into it |
| B. Own web dashboard (time-series charts) | Shows history, which the viewer can't | A container to build |

**Recommendation: A in Week 7, plus plots generated offline from DuckDB for the
report.** Because room layers replace the whole collection, make exactly one
service the layer publisher. The simplest choice is a small `viz` service that
subscribes to truth + observations + predictions + decisions and PUTs the combined layers.

## D-9. Actuator service shape

| Option | Pro | Con |
|---|---|---|
| **A. One actuator service for all rooms: `POST /rooms/{room}/commands {damper, setpoint, cmd_id, issued_at}`, idempotent on `cmd_id`** | One container; room-level failure injection via config | A crash affects every room |
| B. One actuator container per room | Real per-device isolation | 50 containers to manage |

**Recommendation: A.** Say in the report that per-device isolation was traded
for operability.

---

## What the recommended options add up to (draft container view)

```
                 ┌──────────── from course ─────────────┐
                 │ occupancysim ──PUT occupancy──► BuildSim ◄──── browser
                 │   (clock :8081)                (:9090) │
                 └───────────▲──────────────────────▲─────┘
       read clock + truth    │                      │ bulk GET sensors
 ┌───────────┐  PUT sensors  │                ┌─────┴─────┐  publish obs  ┌──────────┐
 │  physics  ├───────────────┘                │ collector ├──────────────►│   MQTT   │
 │ (truth,   │── truth obs (MQTT) ───────────────────────────────────────►│ broker   │
 │  noise)   │◄── GET actuator state ── BuildSim                           └─┬──┬──┬──┘
 └───────────┘                                                             │  │  │
      ┌────────────────────────────────────────────────────────────────────┘  │  │
      ▼                                                                       ▼  ▼
 ┌─────────────────────┐  POST command  ┌───────────┐  PUT state        ┌────────┐ ┌─────┐
 │ controller          ├───────────────►│ actuator  ├──► BuildSim       │ logger │ │ viz │─► room layers,
 │ estimate→predict→act│                │ (validate,│                   │ (JSONL)│ └─────┘   alerts
 └─────────▲───────────┘                │  limits)  │                   └───┬────┘
           │ model file                 └───────────┘                       │
      ┌────┴────┐ ◄──────────────── reads runs/*.jsonl ─────────────────────┘
      │  train  │  (batch job)          evaluation: DuckDB over runs/*.jsonl
      └─────────┘
```

Our containers: `physics`, `collector`, `controller`, `actuator`, `logger`,
`viz`, `train` (batch), plus `mosquitto`. Week 3 needs only `physics`,
`controller` (reactive policy) and `actuator`, talking REST.

Proposed repo layout (Go, one module, one `cmd/` per container):

```
cmd/physics  cmd/collector  cmd/controller  cmd/actuator  cmd/logger  cmd/viz  cmd/train  cmd/seed
internal/buildsim   # thin client (or import external/buildingsim/pkg/client)
internal/model      # observation / command / decision record types + JSON schema
internal/clock      # model-time source (occupancysim)
internal/estimate  internal/predict  internal/policy
compose.yaml  docs/  external/  runs/ (gitignored)
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
| FR-2 | Each room publishes CO₂, temperature and occupancy-count sensors (C-1) to BuildSim every 60 model-s, with noise: CO₂ ±(30 ppm + 3 %), temperature ±0.3 °C, count ±1 | Unit test on the noise package; inspect a logged run |
| FR-3 | The controller sets the airflow level and heating setpoint for every room in the room set, once per control period | Integration test: one room end to end (Week 3 goal) |
| FR-4 | The controller predicts each room's occupancy one lead time ahead (lead time ≈ the room's CO₂ τ). The model is trained offline from stored history | Offline test: prediction error (MAE) on a held-out day beats a naive "same as now" prediction |
| FR-5 | The actuator validates every command before applying it: value range, rate of change, TTL in model time, and duplicates (idempotent on `cmd_id`). It rejects invalid commands with a reason | Unit tests with out-of-range, stale and duplicate commands |
| FR-6 | Safety override: CO₂ > 1100 ppm gives maximum airflow (1.5 × q_design) no matter what the policy says. The flow never drops below 0.35 l/s·m² during occupied hours or 0.10 l/s·m² otherwise (see *Ventilation sizing*) | Test with an injected CO₂ spike and a policy that asks for zero flow |
| FR-7 | Degraded mode: if a sensor is stale (> 3 missed samples) or stuck, the controller falls back. A bad occupancy sensor means reacting to CO₂; a bad CO₂ sensor means design flow. It raises an alert | Fault-injection test (stuck, dropout) |
| FR-8 | Every observation, ground-truth value, command and decision is stored with model time and a run id | Check: a run's JSONL files can rebuild its report plots |
| FR-9 | The policy is chosen by a flag: `constant`, `reactive`, `predictive`, `oracle`. Everything else is identical between runs | Config diff between runs shows only the policy changed |
| FR-10 | The BuildSim viewer shows true CO₂, measured and predicted occupancy, and airflow per room, plus alerts for overrides and degraded mode | Demo and screenshot |

### Non-functional

| ID | Requirement | Verified by |
|---|---|---|
| NFR-1 | **Air quality:** CO₂ ≤ 1000 ppm for ≥ 95 % of occupied room-minutes over the evaluation week (`predictive`) | Evaluation run |
| NFR-2 | **Comfort:** 20–24 °C for ≥ 95 % of occupied room-minutes | Evaluation run |
| NFR-3 | **Energy:** energy proxy (fan power + delivered heat) ≥ 30 % below `constant`, and no worse than `reactive` while meeting NFR-1 better than it does | Compare baselines |
| NFR-4 | **Latency:** a command shows up in BuildSim within 1 control period (≤ 1 model-min, which is ~1 s real at factor 60) | Timestamps in the log, reported as p95 |
| NFR-5 | **Scale:** for the chosen room set (50 rooms, 150 sensor writes per sample, which is 150 PUTs per real second at factor 60 and a 60 model-s sample period), a full sample cycle finishes within one sample period | Load test. This is also the REST → MQTT evidence for D-3 |
| NFR-6 | **Robustness:** any one of our containers can be killed and restarted without an unsafe state. The actuator falls back to design flow when commands expire (TTL). The loop resumes within 30 s real | Chaos test: `docker kill` each service |
| NFR-7 | **Reproducibility:** same seed and date give identical occupancy, and every number in the report traces to a run id | Two runs diffed (also the D-7 check) |
| NFR-8 | **Evaluation cost:** one simulated week per baseline runs in ≤ 3 h real, so all 4 baselines fit in one day | Timed run |

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
| D-1 | Time source | | | | |
| D-2 | Sensor noise/faults | | | | |
| D-3 | Transport | | | | |
| D-4 | Storage | | | | |
| D-5 | Decomposition of autonomy | | | | |
| D-6 | Safety filter placement | | | | |
| D-7 | Baselines/oracle | | | | |
| D-8 | Dashboard | | | | |
| D-9 | Actuator granularity | | | | |
| — | **Room set (scale)** | 50 rooms on level0: the 3 fika rooms, 20 lecture rooms and 27 offices, as a fixed list in config (a seeded random sample, so it is reproducible) | Whole building (838 rooms, ~2 500 PUTs per sample) or all of level0 (277 rooms) | NFR-5 | Most of level0's lecture rooms are left out, so many lectures land in rooms we don't simulate. With 20 of ~48, about 40 % of the lectures fall inside the set |

Each row cites a requirement ID from the section above. The report guide
stresses traceability: every requirement → a component → a test.
