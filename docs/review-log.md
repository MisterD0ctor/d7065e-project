# Review log

Two things the oral exam asks about (Lecture 1, slide 55): where AI advice was
wrong and how we caught it, and why the design looks the way it does. Add an
entry whenever either happens. Newest first.

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
