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

## 0. Fixed constraints: not ours to choose

These come from the course material or the proposal. The design has to respect
them, and the report should state them as boundaries.

| Constraint | Source | Consequence for us |
|---|---|---|
| BuildSim stores **current state only**: no physics, no history, no occupancy generation | lab quickstart | We own physics, persistence and time series |
| The decision service **must not write actuator state to BuildSim itself**; it commands a separate actuator process that validates and applies | lab quickstart §3 | At least two containers on the control path: controller → actuator service → BuildSim |
| occupancysim is the **only writer** of `/api/occupancy` and `/api/entities` (full-collection PUTs) | occupancysim README | We never write occupancy; we only read it |
| `PUT /api/room-layers` **replaces the whole collection** | BuildSim visualization API | Only one of our services may publish room layers, or they overwrite each other |
| Sensor values and actuator states are **strings** | BuildSim API | Parse and format at every boundary |
| No bulk sensor write: one `PUT /api/sensors/{id}/value` per sensor. Bulk **read** exists: `GET /api/equipment?level=…&category=…` | BuildSim equipment API | Write rate, not read rate, limits how many rooms we can simulate |
| Container diagram (C4 L2) should map **1:1** onto `compose.yaml` and repo folders | report guide §2.2 | Decide containers once; the folders follow |
| "A network boundary is justified when a component must be independently started, stopped, restarted, isolated, deployed, or replaced" | course notes 2 | Don't split into services just to look distributed |
| Canvas may state required process boundaries | course notes 2 | **Check Canvas before finalising D-2 and D-5** |
| **Our scope:** the controller never sees true occupancy | proposal §2 | Only physics and evaluation may read `/api/occupancy` |

### Facts about the provided simulators that shape the design

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

**Scale check (applies to every option):** physics writes 2 sensors × N rooms
per sample. The whole building (838 rooms) would be ~1 700 PUTs per sample, and
each one triggers a viewer broadcast. **Decide the room set explicitly**, e.g.
level0 only, or one wing: the lecture rooms, the fika rooms and a handful of
offices. That is ~20–50 rooms, still enough to show that "all rooms" works.

## D-4. Storage

What must be answerable:
- controller: last ~30 model-minutes per room (CO₂ slope, current estimate)
- training: days of per-room history keyed by time-of-day and weekday
- evaluation: a full simulated week per baseline, *with ground truth* next to
  the estimates

| Option | Pro | Con |
|---|---|---|
| **A. Append-only JSONL (raw, one file per run) + in-memory window in the controller + DuckDB CLI for training and evaluation queries** | Transparent, replayable, zero servers. Course notes 3 recommend it as the laptop baseline ("bronze") | No live range queries for a dashboard |
| B. TimescaleDB for everything | Live SQL queries for dashboard and controller | A DB server to run, schema migrations; hard to justify at 20–50 rooms |
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

**Recommendation: A.** The controller then publishes its estimate and decision
per room (to MQTT or the log), and one service assembles room layers for the
viewer (see D-8).

## D-6. Decision route and safety boundary

Course notes 4: pick one main route. Ours is **feedback/rule control with a
forecast feed-forward**:

1. Estimate now: invert the CO₂ mass balance using the commanded flow (physical, no training).
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
| **A. BuildSim viewer: one publisher sends all room layers together (true CO₂, estimated occupancy, predicted occupancy, fan level) plus `/api/alerts` for decisions** | The 3D view from the proposal for free; no extra web app | The report must say the viewer is provided infrastructure (quickstart) and what *we* map into it |
| B. Own web dashboard (time-series charts) | Shows history, which the viewer can't | A container to build |

**Recommendation: A in Week 7, plus plots generated offline from DuckDB for the
report.** Because room layers replace the whole collection, make exactly one
service the layer publisher. The simplest choice is a small `viz` service that
subscribes to truth + estimates + decisions and PUTs the combined layers.

## D-9. Actuator service shape

| Option | Pro | Con |
|---|---|---|
| **A. One actuator service for all rooms: `POST /rooms/{room}/commands {damper, setpoint, cmd_id, issued_at}`, idempotent on `cmd_id`** | One container; room-level failure injection via config | A crash affects every room |
| B. One actuator container per room | Real per-device isolation | 20–50 containers to manage |

**Recommendation: A.** Say in the report that per-device isolation was traded
for operability.

---

## What the recommended options add up to (draft container view)

```
                 ┌────────────── provided ──────────────┐
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
| — | **Room set (scale)** | | | | |

Before this table: write the **requirements** (FR-xx / NFR-xx with numbers,
e.g. "CO₂ < 1000 ppm for ≥ 95 % of occupied minutes", "command applied within
N model-seconds"), so each row above can cite one. The report guide stresses
traceability: every requirement → a component → a test.
