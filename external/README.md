# External (course-provided) services

Copied from the course repository
<https://github.com/eislab-cps/D7065E>, commit `80a4c59` (2026-09-02).
Both started out unmodified.

| Folder | What it is | Ours? |
|---|---|---|
| `buildingsim/` | BuildSim: holds building state (sensors, actuators, occupancy), 3D viewer on :9090 | No, provided infrastructure |
| `occupancysim/` | Simulates people; publishes ground-truth occupancy to BuildSim; owns the simulated clock (UI/API on :8081) | An **example**, not infrastructure: we may change it to fit our scenario |

Do not edit `buildingsim/`. Our own services live at the repository root. To
update it, re-copy the folder from a newer course commit and change the hash above.

`occupancysim/` may be edited. Record every change here (what and why), so it
can be diffed against the course commit and listed in the report. Updating
from the course repo then means merging, not re-copying.

Changes to `occupancysim/`:

| Date | Change | Why | Files |
|---|---|---|---|
| 2026-10-06 | The simulation advances in **fixed 5-second steps** on a grid, where it used to step by whatever the wall-clock tick gave it (up to 10 s). A remainder shorter than a step is carried to the next tick | Two runs of the same day differed by a few seconds here and there, because step boundaries followed wall-clock jitter. With a fixed grid the state at a given simulated time depends only on the day's plan | `internal/sim/sim.go` (`gridStep`, `advanceLocked`, `SetTime`, `New`) |
| 2026-10-06 | A **per-minute occupancy record**: at every whole simulated minute the simulator notes how many people are in each room, and how many of them are guards. `GET /api/minutes?after=<RFC 3339>` returns the minutes after a time (the newest one without `after`), with the clock. The last 240 minutes are kept | BuildSim's occupancy carries no time and is refreshed at most every 5 real seconds (10 simulated minutes at factor 120), so a reader couldn't tell which moment it described. Our physics needs occupancy by simulated minute to be reproducible, and the evaluation needs to tell a guard's patrol from a room in use | `internal/sim/sim.go` (`MinuteOccupancy`, `RoomCount`, `recordMinute`, `Minutes`), `internal/api/server.go` (`getMinutes`), `internal/sim/minutes_test.go`, `README.md` |

Unchanged: the day plans, the people's behaviour, and everything written to
BuildSim (`/api/entities`, `/api/occupancy`). All of the simulator's original
tests pass.

Planned: study groups of students using unbooked lecture rooms (see
`docs/architecture-options.md`, "Planned: study groups in occupancysim").

Useful docs inside:

- `buildingsim/docs/lab-quickstart.md`: how the lab architecture maps to the BuildSim API
- `buildingsim/docs/api/`: full request and response shapes
- `buildingsim/pkg/client/`: a standard-library Go client we can import
- `occupancysim/docs/model.md`: the occupancy model and all parameters
- `occupancysim/docs/buildsim-api.md`: what occupancysim writes to BuildSim
