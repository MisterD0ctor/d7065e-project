# Review log

Two things the oral exam asks about (Lecture 1, slide 55): where AI advice was
wrong and how we caught it, and why the design looks the way it does. Add an
entry whenever either happens. Newest first.

## 2026-10-08: the stuck-sensor detector

FR-7 promised a fallback for a *stuck* sensor and nothing detected one (the
entry below). Now built: `internal/stuck`, design in D-11. CO₂ and
temperature sensors are found by a run of identical readings; the counter by
two bounds from the room's CO₂ mass balance that hold for any airflow. A
stuck sensor is then treated exactly like a missing one.

| Finding | How it was caught | Fix |
|---|---|---|
| "Within 3 samples" (T-12) is not possible. Readings are rounded, so a healthy CO₂ sensor repeats a value three times running about 15 times a day in the building, and a healthy temperature sensor far more often | Summing the chance of a repeat over the values a sensor can show, before choosing the limit | The limit is the shortest run with under one false alarm per 100 building-days: 5 readings for CO₂, 12 for temperature. A unit test recomputes both and checks the sum against a simulated week |
| The margin for "CO₂ contradicts the counter" was first a fixed 100 ppm. The CO₂ sensor's noise grows with the level (±(30 ppm + 3 %)), so at a few thousand ppm 100 ppm is only two standard errors | Working through the test case with the damper held at the floor, where CO₂ reaches 4000 ppm | The margin is five standard errors of the mean at the level measured |
| Live, the first frozen counter was never found. The container took five model-minutes to restart at factor 60, the lecture had just ended, and the counter froze at 4 in a 25-person room: four people's CO₂ at full flow (63 ppm) is inside the sensor noise | Watching the live test for three model-hours | None: this is the detector's blind spot, now documented and pinned by a unit test (`TestSmallOvercountInABigRoomGoesUnseen`). The live test was repeated with the clock paused during the injection (T-12) |
| The second attempt: counter frozen at 32 with 33 in the room. Not blamed for the remaining 55 minutes of the lecture; found 15 minutes after the room emptied; the flow went from 247 l/s (for "32 people") to 6 l/s | Decision records and the dashboard | — |
| The live test's records went to the wrong run. `.env` still named an old run (`smoke-predictive`), the stack had been started with `RUN_ID=viz-dev` on the command line, and every `docker compose up -d <service>` without it recreated that service under the `.env` run id, physics included | The decisions with the fault text were missing from `viz-dev` in storage | `deploy/fault.sh` injects a fault with the run id read from the running controller and touches only the sensor. The stray records (model date 2026-09-21) are still in `smoke-predictive`, a development run no result uses; the evaluation runs were not touched |
| Does the detector change the evaluation? | `cmd/evaluate -stuck-check`: the four final runs replayed through it | No verdict in 1.44 million CO₂ readings, 1.44 million temperature readings and 1.44 million counter checks, so not one decision would have differed (T-38) |

AI advice that was wrong here:
- The detector first proposed, in the entry below, was "a count that has not
  changed in N minutes while CO₂ moves". That blames every healthy counter:
  CO₂ lags the count by the room's time constant, so after 40 people sit down
  the count is still while CO₂ climbs for half an hour. Caught on paper,
  before any code, by asking what a healthy room looks like right after a
  lecture starts. What replaced it compares CO₂ with what the *counted*
  people could cause, not with whether the count moved.
- The fixed 100 ppm margin, above.
- `FAULT_CO2_0001=stuck docker compose up -d co2-0001` has been the
  documented way to inject a fault since the per-device containers. It is
  only right when `.env` names the live run. Nobody noticed because the
  dashboard and the controller don't care about the run id; storage does.

What the detector cannot do is in D-11 (*Limits, measured*).

## 2026-10-08: the dashboard (`viz`)

`viz` is built (D-8, FR-10, IF-11): five heatmap layers and the current
alerts in the BuildSim viewer, from the observations, decisions and actuator
events on MQTT. The logic is in `internal/dashboard` so it can be unit-tested
without a broker or BuildSim; `cmd/viz` is only the plumbing.

| Finding | How it was caught | Fix |
|---|---|---|
| The planned airflow layer was 0–500 l/s. An office's maximum is ~20 l/s and a lecture room's ~900, so offices would always look off and lecture rooms always saturated | Looking at the sizing table before writing the layer | Airflow is shown as a share of each room's own maximum (from the registry) |
| The viewer prints the unit straight after the number: "0people" | Headless screenshot of the legend | The people layers have no unit; their labels say "People" |
| The first labels wrapped over three lines in the viewer's legend | Same screenshot | Shorter labels, with the meaning of the grey midpoint in the label itself |
| A stopped CO₂ sensor shows up twice, correctly: its own "no reading" alert, and "damper on fallback" once the commands expire | Stopping `co2-0002` in the 50-room stack | None needed; both clear when the sensor returns |
| A drifting CO₂ sensor (`drift:+6000`) is not detected as a fault. The reading passes 1100 ppm and the actuator's override opens the damper fully: wasteful, but the safe side | Injecting the fault and reading the alerts | Not fixed; see the gap below |

Gap found while writing the alerts: **FR-7 promises more than is built.** It
says the controller falls back when a sensor is "stale or stuck". Stale is
built and tested (readings older than 180 model-s are dropped; the actuator's
command TTL does the rest). A *stuck* sensor, one that keeps repeating its
last value, is not detected anywhere: its readings arrive on time and look
valid. T-12 as written ("detects it within 3 samples") cannot pass. Either a
detector is added (a count that has not changed in N minutes while CO₂ moves,
say) or FR-7 and T-12 are narrowed to what is built. Closed the same day with
a detector; see the entry above.

What could not be verified here: the room colours in the 3D scene. The
headless browser drew the viewer's panels and the legend but not the WebGL
scene, so the layers were checked through the API (five layers, 50 rooms
each, values matching the readings) and the colour scales numerically (a unit
test on their lightness). One of us has to look at the viewer and take the
screenshot for the report.

AI advice that was wrong here: the first draft of IF-11, written before any
code, had the 0–500 l/s airflow scale above and layers published "every 60
model-s". The second would have made the viewer lag a minute behind at
factor 1; `viz` publishes once per real second instead, which is also what
makes a restarted BuildSim get its layers back at once.

## 2026-10-06: second evaluation

Five runs with the three fixes below, all complete, no gaps (sleep was
blocked with `systemd-inhibit` this time). Results in
[evaluation-results.md](evaluation-results.md).

| Finding | How it was caught | Fix |
|---|---|---|
| The fixes held at full scale: the four runs have the same head-count and guard count in all 360 550 shared truth records; comfort is 100 % under every policy; 99.9 % or more of sensor readings arrived | Diffing the runs' truth; `cmd/evaluate` | — |
| The predictive policy went from +10 % to +25 % energy over reactive | The summary table: the other three policies rose ~2.5 % (the pre-heating), predictive 17 % | Fixed 2026-10-08, see the next row |
| Cause: the new model expects ≥ 1 person in ~86 % of all slots in every room, nights included (the first model: 17–26 %). Empty rooms at night got 30.9 l/s under predictive, 7.5 l/s under reactive | Inspecting the stored model, then the airflow in empty rooms | `predict.Train` takes the 75th percentile of a slot's readings per day instead of the highest; a forecast below half a person counts as empty. Retrained on the stored training week (`profile-train-week-v3`) and only the predictive run repeated (`eval-predictive-v3`): the model expects nobody at night, empty rooms at night get 8.7 l/s, and energy is +3.8 % over reactive (the oracle: +1.4 %) with no occupied minute above 1000 ppm |
| The first retrain reported "6 weekdays" for a five-day training week | The trainer's own log line | When the next run reset the clock, the previous run's physics and sensors were still alive and recorded 50 readings at the new start time under the old run id. `train` now takes explicit bounds (`TRAIN_FROM`, `TRAIN_UNTIL`), and `deploy/evaluate.sh` stops physics before it moves the clock. `eval-predictive-v3` has no stray records |

AI advice that was wrong here, twice over:
- `predict.Train` takes the **highest** occupancy reading per slot and day.
  The occupancy sensor reports one person too many in ~5 % of readings, so
  with 15 readings per slot an empty room looks occupied in most slots. The
  predictor's unit tests used noise-free data and passed.
- For this evaluation the training week was moved from factor 300 to factor
  120 "so the predictor trains on a reading every minute instead of every
  five". That gave five times the data and made the model worse, because it
  gave the noise five times the chances per slot. Caught only by comparing
  the two evaluations.

A regression test now trains on a week of minute readings from an empty room
with the counter's noise (`TestTrainIgnoresCounterNoise`); it fails with the
old behaviour in the very first slot. That only the predictive run had to be
repeated is the reproducibility fix paying off: the other three runs are
valid as they stand, because the simulated week is identical.

## 2026-10-06: heating, guards, and reproducible days

Three follow-ups to the first evaluation. All three change what the
evaluation measures, so its tables have to be regenerated
(evaluation-results.md says which numbers are affected).

**1. Cold rooms in the morning.** In `eval-reactive`, 195 of 7 011 occupied
samples were below 20 °C, none above 24 °C:

| When | Cold samples | Who | Cause |
|---|---|---|---|
| Night 18–05 | 108 | One person | Guards on patrol during the 18 °C setback |
| Pre-dawn 05–07 | 54 | One person | Guards and the earliest staff, same setback |
| Morning 07–09 | 33 | First arrivals | Heating switched to 21 °C at 07:00 sharp; rooms were at 17.9 °C and reached 20 °C about 08:13 |

Fix: optimal start (D-6). Checked on six rooms, Mon–Tue of the evaluation
week, reactive policy:

| | Before | After |
|---|---|---|
| Room temperature at 07:00 | 17.9–18.1 °C | 20.9 °C |
| Reaches 20 °C | ~08:10 | 05:37–06:01 |
| Cold occupied samples 07–09 | 13 of 60 | 0 of 51 |
| Cold occupied samples 05–07 | 9 of 9 | 0 of 9 |
| Cold occupied samples at night | 5 of 11 | 5 of 11 |
| Energy, six rooms | 190 kWh | 196 kWh (+3 %) |

The pre-heat started between 04:23 and 05:15 depending on how cold each room
was. Rooms are warm about an hour early, because the assumed warm-up rate
(1.5 K/h) is slower than the rooms' real one (~1.8 K/h) and the target is the
setpoint, not the edge of the comfort band. Learning the rate per room would
trim that.

**2. Guards.** The night samples are patrols, and heating an empty building
for a five-minute visit would be the wrong fix. The measure changed instead:
a room counts as occupied when someone other than a guard on patrol is in it
(`cmd/evaluate`). Guard-only records are counted separately as patrols, so
they stay visible. This needs the truth to say who is a guard, which it now
does (IF-5, `guards`).

**3. Runs of the same day were not identical** (NFR-7, T-25; details in
D-7). Found while checking whether the before/after comparison above was
like-for-like.

| Finding | How it was caught | Fix |
|---|---|---|
| Two runs of the same dates agreed in only 99.6–99.7 % of samples; a fika group was 36 in one run and 23 in another at the same minute | Diffing the true occupancy of the four evaluation runs | occupancysim: fixed 5-second steps and a per-minute occupancy record. Physics: steps through those minutes instead of sampling BuildSim's occupancy on its own tick |
| At factor 120 the truth had a sample only every 2 model-minutes (FR-2 says 60 s) | Same analysis: the runs didn't even share timestamps (the oracle run and the reactive run had 17 % in common) | Same change: physics computes every minute occupancysim recorded, at any speed |
| Our first version of the occupancysim change replayed the day from midnight at start-up, and broke one of the simulator's own tests, which relies on everyone starting outside | Running occupancysim's test suite after the change | That part was dropped: setting the clock already replays the day, and every experiment sets the clock |
| IF-2 claimed the controller has "no route" to physics. It shares the compose network with it | Re-reading the contract while editing it | The contract now says it is a convention, not enforced; listed as a limitation |

Result of T-25 after the fix: the same Monday morning at factor 600 and at
factor 120, 39 200 truth records in common (50 rooms × 784 minutes), **zero**
differences in head-count, guard count, CO₂, temperature or radiator power.

**Load after the change** (run `load120`: all 50 rooms, 257 containers,
factor 120, Mon 07:00–10:30, reactive). With a truth record every model
minute the sensors report twice as often as in the first evaluation: 300
readings and 200 commands per real second.

| Measure | Result |
|---|---|
| Truth, temperature and occupancy readings per model minute | 50 of 50 rooms in all 210 minutes |
| CO₂ readings per model minute | 50 of 50 in 206 minutes, 49 in the other 4 (4 readings of 10 500 missed: a sensor polls every 250 ms and a model minute lasts 500 ms) |
| Commands during the run | 21 362 sent, all accepted |
| Readings the controller dropped as stale | 0 |
| Records rejected by storage | 0 of 63 792 |

So NFR-5 holds at 50 rooms with FR-2's 60-second sampling at factor 120,
which the first evaluation did not have. (34 commands failed at 07:00, before
the clock was started, while the actuators were still booting.)

AI advice corrected here: the first evaluation write-up listed "same rooms,
devices, dates and occupancysim seed" under fairness without having run T-25,
which the design doc had said to verify "as soon as storage runs". It was
only verified a week later, and failed.

## 2026-10-01: evaluation run

Results in [evaluation-results.md](evaluation-results.md).

| Finding | How it was caught | Fix |
|---|---|---|
| The evaluator showed *negative* fan and AHU energy for `eval-constant` | Implausible numbers in the first table | Each run ends with a stray truth record at Mon 00:00 (the next run reset the clock before this run's physics was replaced); its gap to the previous record was −5 days. Records earlier than a room's previous one are now skipped |
| Mean absolute error made the predictor look worse than "same as now" | Reading the first accuracy table | Added arrivals foreseen and false alarms: the forecast foresees 72 % of fika arrivals, which MAE hides |
| The PC slept mid-run | `eval-constant` took 1 h 38 min instead of 1 h | One 16 model-minute gap (Fri night, empty building); reported as a caveat. Keep the PC awake during runs |

## 2026-09-30: predictor, predictive and oracle policies

- `train` builds the per-room weekday profile from the occupancy **sensor**
  readings of recorded runs and stores it in storage; the controller picks up
  the newest model every 5 min and keeps the last good one.
- `predictive` ventilates for the most of the count now and the profile's
  80 % quantile within the lead time (the room's τ, 10–30 min), and still
  reacts to CO₂. `oracle` does the same with the true occupancy of a recorded
  run of the same date.
- Smoke test (trained and tested on the same day, so not an evaluation):
  in fika room `level0/1570` the predictive policy raised the airflow to
  112 l/s at 09:28 for 11 expected people, ~20 min before the break, with CO₂
  at 417 ppm. The oracle loaded the recorded truth of `scale50-reactive-3`.
- Storage now refuses the live run's truth (`403`, T-33).

## 2026-09-30: first 50-room run (250 device containers)

Every limit hit was the laptop's, not the design's. Each one is a data point
for the report's limits section (grade 5: "finds the limits").

| Limit hit | Symptom | Fix |
|---|---|---|
| Kernel keyring quota: `kernel.keys.maxkeys = 200` per user, and Podman gives every container a keyring | Only ~195 containers started; the rest failed with `Disk quota exceeded` | `keyring = false` in `~/.config/containers/containers.conf` (user-level, no root) |
| Compose's default network is a /24: 254 addresses for 257 containers | `IPAM error: failed to find free IP` | The project network is a /22 in `compose.yaml` |
| The floor plan (rooms plus the walkable graph) took over 2 s from BuildSim under load | 10 of 250 registrations failed with a timeout | Floor-plan reads get 15 s; other BuildSim calls keep 2 s |
| Kernel neighbour (ARP) table: `gc_thresh3 = 1024` entries, **shared by all network namespaces**. Each container holds one entry per peer it talks to (gateway, physics, registry, BuildSim, broker): ~5 × 257 > 1024 | DNS lookups failed (`Message too large`, `No buffer space available`); sensors couldn't reach physics, so no readings | Needs root: raise `net.ipv4.neigh.default.gc_thresh1/2/3` to 1024/4096/8192 |

The architecture scales linearly (5 containers per room), but a stock Fedora
laptop stops at roughly **200 containers**, i.e. ~40 rooms, before any code of
ours is the bottleneck.

After raising the neighbour-table limit (run `scale50-reactive-2`), two more
findings:

| Finding | How it was caught | Fix |
|---|---|---|
| `temp-0017` missed about one reading in five. Sensors polled physics at 1 Hz, the same rate physics updates at factor 60, so a sensor whose phase lined up with the update sometimes read one snapshot twice and then skipped a minute (aliasing) | Per-minute counts of readings per sensor in storage | Sensors poll physics every 250 ms real. Rerun: no gaps |
| 17 commands (8 in the rerun) were rejected as expired (`409`), all at 08:00 | Decision statuses in storage | None needed: that is the moment the clock was moved *back* to 08:00. For one tick the actuators still held the later time, so commands stamped 08:00 looked expired. Only a manual clock jump causes it |

**Result, run `scale50-reactive-3`** (50 rooms, 257 containers, factor 60,
Monday 08:00 onwards, reactive policy, 184 model-minutes):

| Measure | Result |
|---|---|
| CO₂, temperature, occupancy readings per model minute | 50 of 50 in **every** minute (184/184 each), so NFR-5 holds at 50 rooms |
| Truth records per model minute | 50 of 50 (211/211) |
| Commands | 21 400, of which 21 392 accepted (`202`); 8 expired at the clock jump |
| Occupied room-minutes with CO₂ > 1000 ppm | 0 of 894 (max 958 ppm) in this morning window |
| Resources | ~1 CPU core in total across all containers (of 16); memory ~8 GB free of 31 |
| Start-up | `docker compose up` ~40 s; registering 250 devices ~26 s |

## 2026-09-30: device registry, one process per device

Design change (D-2, D-9, new D-10): the lecturer suggested a device database
that an installer fills in. The gateways-per-kind design had no installer
step at all; rooms came from `ROOMS` in `compose.yaml`. Now every device is
its own container, knows only its id and kind, and learns its room from the
registry. We chose one process per device over gateways for faithfulness to a
real installation, accepting ~250 containers for 50 rooms.

| Finding | How it was caught | Fix |
|---|---|---|
| Actuators advertised their container hostname, which other containers can't resolve on the compose network | Controller log: `no such host` for every command | `gendevices` sets `ADVERTISE_URL=http://<device id>:8080`, the service name |
| The controller kept the old address until its 30 s registry refresh | Commands failed for up to 30 s after the damper restarted | A failed command triggers an immediate refresh (at most every 5 s) |
| The SQLite driver needs Go 1.26; the image built with 1.25 | `go mod download` failed in the image build | Build image `golang:1.26-alpine` |
| Sizing came out of the registry as `Design`, `AreaM2` (no JSON tags) | Reading `GET /devices` | JSON tags on `sizing.Room` |
| Restarting one service without `RUN_ID` sent its records to run `dev`, so they looked lost | Heating decisions "missing" from the run | `RUN_ID` lives in `.env` for the whole stack |
| T-17, T-28–T-30 pass: BuildSim restart restored all 5 devices in < 15 s; unregistered devices wait; a room typo (`level0/1507`) is rejected; a retired device stops at its next check-in | Manual runs | — |

## 2026-09-30: storage and MQTT

| Finding | How it was caught | Fix |
|---|---|---|
| The fallback waited 5 s real before polling BuildSim, but at factor 60 that is 5 model-minutes, longer than the 3-minute command TTL. Every broker outage expired the damper's commands before the fallback began | T-24: stopped `mosquitto`, saw `ttl_expired` in the actuator log | Silence threshold adapts to the arrival rate (2.5× the gap, ≥ 1.5 s); command TTL raised to 300 model-s. Rerun: no expiry; fallback after 4 s real, back on MQTT when the broker returned |
| The broker couldn't read its bind-mounted config on Fedora | Container log: `Permission denied` | SELinux relabel (`:z`) on the mounts |
| While the broker was down, storage recorded nothing for ~20 model-minutes | T-24, gap in `truth.jsonl` | None: IF-5 says gaps are reported, not filled. The evaluation must count missing minutes |
| T-27 passes: with the controller's login, subscribing to `truth/#` receives nothing; storage's login receives the truth | Manual `mosquitto_sub` with both logins | — |

## 2026-09-30: first slice running

| Finding | How it was caught | Fix |
|---|---|---|
| The clock client read `.clock`; occupancysim nests it under `.sim.clock`. Unit tests passed because nothing tested the real shape | Running the stack: physics logged no model time | Parse `.sim.clock`; `clock_test.go` now uses the real response shape |
| Sizing by area alone treated fika room `1570` (101 m²) as a lecture room: capacity 50, design flow 385 l/s instead of 25 and 210 l/s | Comparing with occupancysim's `/api/state`, which lists each room's role and capacity | Capacities come from occupancysim at start (IF-1); the area rule is only the fallback |
| The controller has no model time over REST, so it can't stamp `issued_at` | Writing the controller | TTL counts from acceptance until observations carry model time (IF-8) |
| Observed: with the reactive policy, 16 people at fika lifted CO₂ to ~640 ppm, and it stayed there for over an hour after they left (τ ≈ 2 h at the occupied minimum) | First run, room `level0/1570` | None needed: this is the lag the predictor is meant to beat. Keep the plot for the report |

## 2026-09-30: design checked against the lecture slides and course repo

Sources: Lecture 1 and 2 slides, course repo `eislab-cps/D7065E` @ `80a4c59`
(still the latest commit on every branch).

### Where the course material disagrees with itself

| Topic | One source says | Another says | What we chose |
|---|---|---|---|
| Who writes sensor values to BuildSim | Lab quickstart diagram and report guide §3.2: the physics model writes them; sensor processes read BuildSim and publish onward | Lecture 1 slide 16 and Lecture 2 slide 7: sensor processes write the readings | The lectures (D-2), because they keep the truth out of BuildSim |
| Truth in a room layer | Lab quickstart §5: `"source": "simulation truth"` layer | Lecture 2 slide 8: the truth never crosses into BuildSim | Lecture 2 (D-8): layers show readings and predictions only |
| Reference stack | Lecture 2 shows a physics engine with a climate API, sensor gateway, actuator processes, `POST /api/actuators/{id}/command`, Prometheus | The course repo contains none of these | We take the shape as guidance and build the parts ourselves |

### Mismatches found in our own docs, and the fixes

| Finding | Source | Fix |
|---|---|---|
| Physics added the sensor noise itself: no sensor process | Lecture 1 slide 24 (pass/fail: each sensor/gateway is its own process) | D-2 → one gateway container per sensor kind |
| Logger, trainer and controller shared JSONL and model files | Lecture 1 slide 18 (no shared files, ever) | D-4 → a `storage` service serving history and models over HTTP |
| A true-CO₂ room layer in the viewer | Lecture 2 slide 8 | D-8 → measured CO₂ only |
| Only room layers were treated as single-writer; alerts are too | BuildSim visualization API: every collection `PUT` replaces it | Only `viz` writes layers and alerts; others publish events (IF-7) |
| "Changes from the proposal" were placed in report §13 | Report guide §3.1: they belong in the Summary, and the examiner looks for them | Moved to §1 Summary; consequences in §13 |
| The plan counted proposal weeks 3–8; design approval (week 38) had passed and the final is due 12 Oct | Lecture 1 slide 49 | Doc now plans by steps and states the real dates |
| No interface contracts | Lecture 1 slide 34, report §5 | [interfaces.md](interfaces.md) |
| No test plan | Lecture 1 slide 50, report §10 | [test-plan.md](test-plan.md) |
| Evaluation over a 7-day week per baseline (~11 h real for four) | Deadline | NFR-8 → 5 simulated weekdays (~2 h each, ~8 h for all four); weekends are empty anyway |

### AI advice that was wrong, and how it was caught

| Advice | Why it was wrong | Caught by |
|---|---|---|
| Treated occupancysim as provided infrastructure we must not edit | It is a course example; we may change it | Kasper, from knowing the course |
| Recommended noise inside physics with no sensor process (D-2, option A) | Breaks a pass/fail rule | Checking the Lecture 1 slides |
| Recommended JSONL files shared between processes (D-4) | Breaks "no shared files" | Checking the Lecture 1 slides |
| Put true CO₂ in a viewer layer (D-8) | Breaks the truth boundary | Checking the Lecture 2 slides |
| Put "changes from the proposal" in report §13 | Wrong section | Re-reading the report guide |
| Planned by the proposal's week numbers | Ignored the real deadlines | Re-reading Lecture 1 slide 49 |
