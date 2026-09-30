# Review log

Two things the oral exam asks about (Lecture 1, slide 55): where AI advice was
wrong and how we caught it, and why the design looks the way it does. Add an
entry whenever either happens. Newest first.

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
