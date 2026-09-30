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

Changes to `occupancysim/`: none yet.

Planned: study groups of students using unbooked lecture rooms (see
`docs/architecture-options.md`, "Planned: study groups in occupancysim").

Useful docs inside:

- `buildingsim/docs/lab-quickstart.md`: how the lab architecture maps to the BuildSim API
- `buildingsim/docs/api/`: full request and response shapes
- `buildingsim/pkg/client/`: a standard-library Go client we can import
- `occupancysim/docs/model.md`: the occupancy model and all parameters
- `occupancysim/docs/buildsim-api.md`: what occupancysim writes to BuildSim
