# External (course-provided) services

Unmodified copies from the course repository
<https://github.com/eislab-cps/D7065E>, commit `80a4c59` (2026-09-02).

| Folder | What it is | Ours? |
|---|---|---|
| `buildingsim/` | BuildSim: holds building state (sensors, actuators, occupancy), 3D viewer on :9090 | No, provided infrastructure |
| `occupancysim/` | Simulates people; publishes ground-truth occupancy to BuildSim; owns the simulated clock (UI/API on :8081) | No, provided infrastructure |

Do not edit these folders; our own services live at the repository root. To
update, re-copy the folders from a newer course commit and change the hash above.

Useful docs inside:

- `buildingsim/docs/lab-quickstart.md`: how the lab architecture maps to the BuildSim API
- `buildingsim/docs/api/`: full request and response shapes
- `buildingsim/pkg/client/`: a standard-library Go client we can import
- `occupancysim/docs/model.md`: the occupancy model and all parameters
- `occupancysim/docs/buildsim-api.md`: what occupancysim writes to BuildSim
