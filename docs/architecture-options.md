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
| C-1 | The controller never sees occupancy. It estimates it from CO₂ (proposal §2) | **Each room has an occupancy sensor (people counter, BuildSim icon `occupancy_counter`).** Physics takes the true occupancy from occupancysim's per-minute record; the occupancy sensor in each room samples it from physics and writes a noisy count to that sensor, with the same sampling, noise and faults as the other sensors (D-2). The controller reads the sensor like any other device and never reads `/api/occupancy` directly | Lack of time: inverting the CO₂ mass balance is dropped from scope. Proposal §2 already named exposing occupancy directly as the fallback if the estimator proved infeasible, and said we would report it as such | D-2, D-5, D-6 step 1, D-8. The predictor, the policy and the baselines are unchanged |

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
  missing, fall back to CO₂-reactive control. Finding a stuck counter uses
  the same CO₂ mass balance the proposal wanted to invert, but only as two
  bounds (D-11).

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
| **Our scope:** the controller never reads `/api/occupancy` directly. It sees occupancy only through the per-room occupancy sensor (changed from the proposal, see C-1) | proposal §2, C-1 | Nothing of ours reads `/api/occupancy`: BuildSim's copy is for the viewer. Physics takes occupancy from occupancysim's per-minute record (the two simulators are the truth layer), and the evaluation uses the truth recorded by storage |

### Facts about BuildSim and the example occupancysim that shape the design

- **Simulated clock.** occupancysim runs at 60 simulated seconds per real
  second by default. Its clock is readable at `GET :8081/api/state` → `.clock`
  (`time`, `minute_of_day`, `weekday`, `factor`, `running`) and streamed on
  `GET :8081/api/events`. BuildSim carries no time at all.
- **Reproducible days.** Each day's plans are generated from the parameters
  plus a seed, so the same date and config give the same day. This is what
  makes the **oracle baseline** possible (see D-7). The plans were
  reproducible from the start, but what a reader *saw* of a day was not: the
  simulator stepped on wall-clock ticks and refreshed BuildSim's occupancy at
  most every 5 real seconds. We changed it to fixed steps and a per-minute
  record (see `external/README.md` and D-1).
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
(D-6), so they need the time too; physics relays it to them over MQTT.

**Refined 2026-10-06: physics advances by whole model minutes, not by its own
tick.** Each tick it asks occupancysim for the minutes recorded since the
last one (`GET /api/minutes`) and steps every room through each of them, with
the occupancy of that minute. The truth is then the same function of model
time in every run, and there is one truth record per model minute at any
speed factor. Rejected alternative: sampling BuildSim's `/api/occupancy` on
physics' own tick, which is what the first evaluation did. That occupancy has
no time attached and can be 10 model-minutes old at factor 120, so two runs of
one day disagreed in ~0.3 % of samples and the truth had a sample only every
2 model-minutes. Trade-off: physics depends on an endpoint we added to
occupancysim, and BuildSim's occupancy is now used by the viewer only.

Numbers to keep in mind:
- **Evaluation takes real time.** At factor 60, 5 simulated weekdays take
  2 real hours, and every baseline needs its own run: ~8 h for all four
  (NFR-8). Weekends are empty, so jump the clock over them. At factor 600 a run
  takes ~12 min; physics still computes every model minute, but the sensors,
  which sample in real time, then see only every second or third one.
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
| B. Physics serves the truth over its own HTTP API; one gateway container per sensor kind samples it for all rooms, adds noise and faults, and writes to BuildSim | Matches Lecture 2; only three containers | A gateway crash silences that kind in every room; ids and rooms come from configuration, not from an installer |
| **C. Physics serves the truth over its own HTTP API (`GET /rooms?room=level0/1570`). Every sensor is its own container: it knows only its device id and hardware kind, learns its room from the registry (D-10), samples its room's truth every 60 model-s, adds noise and faults, and `PUT`s the reading to BuildSim** | One process per device, as a real installation has. A dead sensor affects one room. Truth stays out of BuildSim | ~150 sensor containers for 50 rooms; the compose file is generated (`cmd/gendevices`) |

The course material is not consistent here. The lab quickstart diagram and the
report guide (§3.2) have the *physics* write sensor values into BuildSim and
sensor processes read them back and publish onward. Lecture 1 (slide 16) and
Lecture 2 (slide 7) have sensor processes write the readings. We follow the
lectures because they keep the truth out of BuildSim. Say so in the report and
name the quickstart shape as the rejected alternative.

**Chosen: C** (decided 2026-09-30, after first building B). The registry, not
the sensor, registers the equipment in BuildSim (D-10). Fault injection is
config on one device (`FAULT_CO2_0001=stuck`), set with
`deploy/fault.sh co2-0001 stuck`.

**Scale check:** 3 sensors × 50 rooms = 150 PUTs per sample, one per sensor
container. The whole building (838 rooms) would be ~2 500 PUTs per sample, and
each one triggers a viewer broadcast.

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
  control, or to design flow if CO₂ itself is missing (FR-7). A stale sensor
  is found by its silence. A stuck one keeps talking; D-11 is how it is found.
- **Hysteresis and dwell times**, measured in model time.

**Heating** is the same for every policy, so the baselines differ only in
ventilation: 21 °C in the occupied hours (weekdays 07–18) and an 18 °C setback
otherwise, with an **optimal start**. The setback ends early enough for the
room to be warm at 07:00: the time needed is the degrees still missing
divided by an assumed warm-up rate (1.5 K/h, a commissioning estimate on the
slow side), plus 15 minutes for the valve and the sensor. Once a room has
started pre-heating it keeps going until 07:00, even if it warms faster than
assumed. Rejected alternative: switching at 07:00 sharp, which the first
evaluation showed leaves rooms at 17.9 °C when people arrive and below 20 °C
until about 08:13 (see evaluation-results.md). Trade-off: a little more
radiator energy each morning, and the warm-up rate is assumed, not learned.

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

**Checked 2026-10-06 on the four evaluation runs: close, but not identical.**
99.6–99.7 % of matching samples had the same head-count and the daily totals
agreed within about 2 %. The day's *plan* was the same every time; what
differed was how each run saw it. occupancysim stepped on wall-clock ticks and
refreshed BuildSim's occupancy at most every 5 real seconds (10 model-minutes
at factor 120), and physics sampled that on its own tick, so every run looked
at the same day through a differently-phased, coarse window. A fika group
leaving gradually showed as 36 → 10 in one run and 36 → 23 → 10 in another.
For the first evaluation this means the oracle replayed a week slightly
different from its own, and energy differences of a per cent or two between
baselines are within the noise.

**Fixed the same day** (D-1, `external/README.md`): occupancysim now advances
in fixed steps and records the occupancy at every whole model minute, and
physics steps through those minutes. Test T-25: the same Monday morning run at
factor 600 and at factor 120 gave 39 200 truth records (50 rooms × 784
minutes) with **no difference** in head-count, guard count, CO₂ or
temperature. NFR-7 holds for the truth layer. What still varies between runs
is the control loop's own timing (when a reading arrives, when a command
lands), which is inherent to separate processes and is what the system is
meant to tolerate.

## D-8. Dashboard

The report guide (§3.12) accepts either "BuildSim's session API or a separate
dashboard". Lecture 1 lists the dashboard as its own process (slide 17).

| Option | Pro | Con |
|---|---|---|
| **A. A `viz` container, the only writer of room layers and alerts. It subscribes to `obs/#`, `decision/#` and `event/#` and `PUT`s the room layers (measured CO₂, people counted, people expected, airflow, temperature) plus `/api/alerts` for overrides, silent sensors and actuators on fallback (IF-11)** | The 3D view from the proposal for free; `viz` is our own process, so it counts as the dashboard | No history in the viewer |
| **B. Own web page with graphs over time, served by the same `viz` process** | Shows history, which the viewer can't: how a room got to where it is, and what the building has used | A page to build; the history has to be held somewhere |
| C. Grafana over storage | Graphs without writing a page | A container to configure, plus a data source for our JSONL; storage reads the whole run for every query (IF-10), so it would need an index first |

**Chosen: A (built 2026-10-08) and B (2026-10-09).** A alone was the first
decision, with plots made offline for the report. It was not enough: the
viewer shows the building *now*, and the questions asked of a ventilation
system are about time (did the air stay good through the lecture, when did
the damper open, what did the fault do). B is one page with no outside
dependencies, so it also works without internet in the lab.

The graphs' history is held in `viz`'s memory, a point per room and model
minute for a week (about 16 MB for 50 rooms), because a page that redraws
every two seconds can't wait for storage to read a whole run. Storage stays
the owner of the history (D-4): after a restart `viz` reads the run so far
from it, so a killed dashboard comes back with its graphs (NFR-6).

Show **measured** CO₂, not true CO₂: the truth never enters BuildSim
(Lecture 2, slide 8), and it never reaches `viz` either. The truth appears
only in the evaluation. The one derived number on the page, ventilation
power, is computed from the airflow the dampers report with the energy
proxy's constants (IF-11).

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
| A. One actuator container per kind (`actuator-damper`, `actuator-heating`), each for all rooms | Two containers | A crash affects every room of that kind; built first, then replaced |
| **B. One container per actuator: a damper or a radiator valve in one room. It learns its room and the room's sizing from the registry and advertises where to send commands (D-10). `POST /rooms/{level}/{room}/command {value, cmd_id, issued_at}`, idempotent on `cmd_id`. Moves towards the target at a fixed rate in model time, `PUT`s the reached state to BuildSim, and physics reads it back from there** | Real per-device isolation: one failed damper affects one room | ~100 actuator containers for 50 rooms, generated |
| C. One actuator container for everything | Fewest containers | Heating and ventilation fail together |

**Chosen: B** (2026-09-30). The controller finds each room's actuators through
the registry, so nothing is configured twice.

## D-10. Device commissioning

In a real building a technician mounts each device and records what went
where. The system must find out from that record, not from configuration
files a developer edits.

| Option | Pro | Con |
|---|---|---|
| A. Device lists in configuration (`ROOMS` per service, ids derived from room names) | No extra service | Nobody installs anything; a new device means editing and redeploying services |
| B. The installer registers devices straight in BuildSim's equipment list | No new service | BuildSim is "restart = blank" (Lecture 1, slide 16), so every installation is lost on restart; BuildSim isn't ours |
| **C. A `registry` service with its own SQLite database. The installer enters id, kind and room (web form at `:8070` or `cmd/install`). The registry checks the room exists in the floor plan, stores the room's sizing, and keeps BuildSim's equipment list in step, recreating it after a BuildSim restart. A device boots knowing only its id and kind, checks in, and learns its room; until it is registered it does nothing. Actuators advertise their address at check-in, so the controller finds them (the service-registry pattern, Lecture 1, slide 41)** | Matches how a building is commissioned. Typos are caught at installation. BuildSim restarts are harmless | One more service on the start-up path; if the registry is down, new devices can't start (running ones keep going) |

**Chosen: C** (suggested by the lecturer). Known limitation: devices don't
authenticate to the registry. A real installation would hand each device a
secret at commissioning, so a random device on the LAN can't pose as a sensor.
State this in the report.

## D-11. Telling a stuck sensor from a quiet room

A stuck sensor repeats its last value (FR-7). Its readings arrive on time and
look valid, so the staleness check and the command TTL never trigger. The
costs differ by sensor. A CO₂ sensor stuck at 500 ppm blinds both the policy
and the actuator's override while a lecture fills the room. A counter stuck
at 40 keeps the damper open for 40 people all night. A stuck temperature
sensor only mistimes the morning pre-heat.

| Option | Pro | Con |
|---|---|---|
| A. Too many identical readings in a row, for every sensor | One rule, no model | Wrong for the counter: an empty room really reads 0 for hours. A limit long enough to be safe there is several hours |
| B. Counter unchanged while CO₂ moves | Simple to say | Wrong: CO₂ lags the count by the room's time constant. After 40 people sit down the count is still and CO₂ climbs for half an hour. It would blame every healthy counter at the start of every lecture |
| C. Estimate the head-count from CO₂ and the airflow, compare with the counter | Two-sided, quantitative | Needs the damper's true flow at every moment (travel, floors, override), so an error there becomes a false alarm; and it is the estimator C-1 dropped |
| **D. A for CO₂ and temperature. For the counter: two bounds from the CO₂ mass balance that hold for any airflow** | No airflow knowledge needed; each bound is one line of physics; false-alarm rates can be computed | Blind to small errors (below); the counter is only found once the air has had time to disagree |

**Chosen: D** (2026-10-08, `internal/stuck`).

*CO₂ and temperature* are noisy (FR-2) and rounded (1 ppm, 0.1 °C), so a
healthy sensor seldom repeats itself. The chance of k identical readings in a
row is the sum over every value the sensor can show of (the chance of showing
it)^k. The limit is the shortest run that a healthy 50-room building produces
less than once in 100 days: **5 readings for CO₂** (chance 5.5·10⁻⁸ per
reading) and **12 for temperature** (1.2·10⁻⁷; its noise is only 1.5 rounding
steps wide, so it repeats more). One different reading and the sensor is
trusted again.

*The counter* is judged against the room's CO₂. With x the CO₂ above outdoor,
n people, G the CO₂ one person gives off and q the airflow:

```
V·dx/dt = G·n − q·x        and        0 ≤ q ≤ q_max
```

Whatever q is, two things follow:

- **x can't rise faster than G·n/V.** Only people add CO₂. If the counter has
  not moved for 20 minutes and CO₂ has risen more than its n people can
  explain in the last 15, more people are there than it counts.
- **x can't stay below (G·n/q_max)·(1 − e^(−t·q_max/V))** after n people have
  been there for t. That is the level they would give with the damper fully
  open all the time, and any smaller flow gives more. If CO₂ is under it, fewer
  people are there than the counter says.

Both are checked on five-minute means of CO₂, must be exceeded by five
standard errors of that mean, and allow a person to give off twice or half
the nominal CO₂. A counter that has not moved and breaks either bound is
stuck. The verdict lasts until its reading changes.

**What happens then** is what already happens for a missing sensor, so there
is one answer to "no trustworthy reading": CO₂ → design flow
(`degraded_co2`); temperature → 21 °C (`degraded_temp`); counter → forecast
and CO₂ only (`degraded_occupancy`). The decision says what was found
(`fault`, IF-6) and `viz` shows it (IF-11).

**Limits, measured** (T-12, T-36 to T-38):
- A counter is found only after the room's air has had time to disagree:
  15 to 20 minutes after a lecture room empties (15 live in two rooms, 22 in
  the unit test), about 75 for an office (small room, small flow, slow air).
- A counter stuck on a number whose CO₂ would be lost in the sensor noise
  even at full flow is not found: roughly fewer people than a quarter of the
  room's capacity. Seen live: a counter stuck at 4 in a 25-person room was
  never blamed in three hours. It costs the flow for four people.
- A counter stuck on a number that stays right is not found, and costs nothing.
- A drifting sensor is not a stuck one and is not detected. A CO₂ sensor
  drifting up ends in the override (safe, wasteful); one drifting down would
  be believed, and would also make a healthy counter look like it over-counts.
- The run lengths come from *our* sensor model. A real sensor quieter than
  its resolution would repeat itself when healthy, and would need the limits
  set from its own noise.

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

Our containers: `physics`, `registry`, `controller`, `storage`, `viz`, `train`
(batch) and `mosquitto`, plus **one container per device** (5 per room:
CO₂, temperature and occupancy sensors, damper, radiator valve), generated
into `compose.devices.yaml` from the installation list `deploy/devices.csv`.
The sketch above predates the registry and per-device containers; the D2
diagram (`docs/diagrams/container.d2`) is current.

Repo layout (Go, one module, one `cmd/` per binary; `sensor` and `actuator`
run as one container per device):

```
cmd/physics cmd/sensor cmd/actuator cmd/controller cmd/storage cmd/registry
cmd/viz cmd/train cmd/evaluate
cmd/install cmd/gendevices                     # installer's tool, compose generator
internal/buildsim internal/clock internal/devreg internal/registry internal/store
internal/msg internal/mqttx internal/faults internal/roommodel internal/sizing
internal/predict internal/stuck internal/dashboard internal/actuator
compose.yaml compose.devices.yaml (generated) deploy/ docs/ external/
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
| FR-7 | Degraded mode: if a sensor is stale (> 3 missed samples) or stuck (repeating one value, D-11), the system falls back. A bad occupancy sensor means reacting to CO₂ and the forecast; a bad CO₂ sensor means design flow; a bad temperature sensor means 21 °C. A stuck CO₂ sensor is found within 5 samples and a stuck temperature sensor within 12; a stuck counter once the room's CO₂ contradicts it. The decision names the mode and the fault, which `viz` shows as an alert | T-12, T-13, T-36 to T-38 |
| FR-8 | Every observation, ground-truth value, command and decision is stored with model time and a run id | Check: a run's JSONL files can rebuild its report plots |
| FR-9 | The policy is chosen by a flag: `constant`, `reactive`, `predictive`, `oracle`. Everything else is identical between runs | Config diff between runs shows only the policy changed |
| FR-10 | The BuildSim viewer shows measured CO₂, measured and predicted occupancy, airflow and temperature per room, plus alerts for overrides, silent or stuck sensors and actuators on fallback. `viz`'s own page shows the same per room and for the whole building as graphs over time, up to a week back, with the periods an alert was active | T-16, T-39 |
| FR-11 | Devices are commissioned in the registry: a device does nothing until an installer has registered its id, kind and room, and the registry keeps BuildSim's equipment list in step with what is installed, also after a BuildSim restart | T-17, T-28–T-30 |

### Non-functional

| ID | Requirement | Verified by |
|---|---|---|
| NFR-1 | **Air quality:** CO₂ ≤ 1000 ppm for ≥ 95 % of occupied room-minutes over the evaluation period of 5 simulated weekdays (`predictive`) | Evaluation run |
| NFR-2 | **Comfort:** 20–24 °C for ≥ 95 % of occupied room-minutes | Evaluation run |
| NFR-3 | **Energy:** energy proxy (fan power + delivered heat) ≥ 30 % below `constant`, and no worse than `reactive` while meeting NFR-1 better than it does | Compare baselines |
| NFR-4 | **Latency:** a command shows up in BuildSim within 1 control period (≤ 1 model-min, which is ~1 s real at factor 60) | Timestamps in the log, reported as p95 |
| NFR-5 | **Scale:** for the chosen room set (50 rooms, 150 sensor writes per sample, which is 150 PUTs per real second at factor 60 and a 60 model-s sample period), a full sample cycle finishes within one sample period | Load test. This is also the REST → MQTT evidence for D-3 |
| NFR-6 | **Robustness:** any one of our containers can be killed and restarted without an unsafe state. The actuator falls back to design flow when commands expire (TTL). The loop resumes within 30 s real | Chaos test: `docker kill` each service |
| NFR-7 | **Reproducibility:** same seed and date give identical occupancy, and every number in the report traces to a run id | T-25: two runs diffed record by record. Failed in the first evaluation (99.6 % equal); holds since the 2026-10-06 fix (D-7) |
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
| D-1 | Time source and stepping | Physics steps by whole model minutes from occupancysim's per-minute record; observations carry model time | Every service keeps its own `wall × factor` clock; physics sampling BuildSim's occupancy on its own tick | FR-1, NFR-4, NFR-7 | Physics depends on occupancysim being up, and on an endpoint we added to it |
| D-2 | Sensor noise/faults | One container per sensor device, sampling its room from a truth API on physics | Physics writes noisy values to BuildSim itself; one gateway per kind | FR-2, FR-7, FR-11 | ~150 sensor containers; a truth API to specify |
| D-3 | Transport | REST for the first slice; gateways also publish to MQTT once storage exists | A separate collector polling BuildSim | NFR-5, NFR-6 | A broker to run; two non-atomic writes per reading |
| D-4 | Storage | A `storage` service owning JSONL per run, served over HTTP | JSONL files shared between processes | FR-8, NFR-7 | A small query API to write |
| D-5 | Decomposition of autonomy | Validation, prediction and policy in one controller; training as a batch job | Predictor as its own service | FR-3, FR-4 | Parts of the controller can't be restarted separately |
| D-6 | Safety filter placement | In the actuator, independent of the controller | Inside the controller, after the policy | FR-5, FR-6, NFR-6 | The actuator needs sizing data and the CO₂ stream |
| D-7 | Baselines/oracle | Policy interface with four implementations; the oracle replays recorded truth | Oracle reading occupancysim's plans | FR-9, NFR-7 | Depends on same-seed runs being identical, which needed the D-1 change |
| D-8 | Dashboard | `viz` process: room layers and alerts in the BuildSim viewer, and its own page with graphs over time | Grafana over storage; the viewer alone | FR-10, FR-7 | The graphs' history is a second copy in `viz`'s memory, refilled from storage after a restart; the airflow reached before a restart is not recovered |
| D-9 | Actuator granularity | One container per actuator device | One container per kind for all rooms | FR-5, NFR-6 | ~100 actuator containers, generated |
| D-10 | Device commissioning | Registry service with SQLite; devices learn their room at check-in | Device lists in configuration; registering straight in BuildSim | FR-11 | One more service on the start-up path; no device authentication |
| D-11 | Stuck-sensor detection | Run of identical readings for CO₂ and temperature; the counter against two CO₂ mass-balance bounds that hold for any airflow | "Counter unchanged while CO₂ moves"; estimating the head-count from CO₂ and the airflow | FR-7 | Blind to small over-counts; a counter is found only after the room's air disagrees (15–20 min in a lecture room, 75 in an office) |
| — | **Room set (scale)** | 50 rooms on level0: the 3 fika rooms, 20 lecture rooms and 27 offices, as a fixed list in config (a seeded random sample, so it is reproducible) | Whole building (838 rooms, ~2 500 PUTs per sample) or all of level0 (277 rooms) | NFR-5 | Most of level0's lecture rooms are left out, so many lectures land in rooms we don't simulate. With 20 of ~48, about 40 % of the lectures fall inside the set |

Each row cites a requirement ID from the section above. The report guide
stresses traceability: every requirement → a component → a test.
