# Test plan (draft, copy into report §10)

Every test names the requirement it verifies, and every requirement in
[architecture-options.md](architecture-options.md) has at least one test.
Interface IDs (IF-x) refer to [interfaces.md](interfaces.md).

Levels: **unit** (Go test, no network), **integration** (the containers under
`docker compose`), **fault** (fault injected on purpose), **perf** (measured
under load), **eval** (full evaluation runs).

| ID | Test | Level | Requirement | Expected result |
|---|---|---|---|---|
| T-01 | Step response: empty room, fill with N people at t=0, constant flow | unit | FR-1 | CO₂ reaches 63 % of the steady-state rise at t = τ, within 5 % |
| T-02 | Clock jump: advance the model clock by 3 h in one tick | unit | FR-1, D-1 | Physics sub-steps with dt ≤ 1 model-min; no overshoot or NaN |
| T-03 | Noise statistics: 10 000 samples of a constant truth | unit | FR-2 | Mean error ≈ 0, spread matches the configured σ for each kind |
| T-04 | Fault modes: stuck, dropout, drift, lag on one sensor | unit | FR-2 | Each produces its configured signal; other sensors are untouched |
| T-05 | One room end to end: raise the airflow command, watch the next readings | integration | FR-3 | The loop closes: CO₂ readings fall after the reached airflow rises (IF-8 → IF-9 → physics → IF-3) |
| T-06 | Predictor vs. naive "same as now" on held-out days | unit (miniature: `predict_test.go`) + eval | FR-4 | Lower error at the lead time for fika rooms and offices; lecture rooms reported separately. The unit test checks the profile foresees a break that "same as now" misses |
| T-07 | Actuator validation: out-of-range, wrong unit, expired, duplicate `cmd_id` | unit | FR-5 | 422, 422, 409, 200-duplicate; state unchanged for all four |
| T-08 | Travel rate: command a jump from minimum to maximum | unit | FR-5 | Reached state rises at ≤ 10 % of max per model-minute |
| T-09 | CO₂ override: inject 1200 ppm while the policy asks for the floor | fault | FR-6 | Damper goes to maximum; `override_co2` event; releases below 1100 ppm |
| T-10 | Override with the controller killed | fault | FR-6, NFR-6 | Same as T-09: the override doesn't depend on the controller |
| T-11 | Flow floors: policy asks for zero flow, occupied and unoccupied | unit | FR-6 | Clamped to 0.35 and 0.10 l/s·m²; `clamped: true` |
| T-12 | Stuck occupancy sensor during a lecture | fault | FR-7 | Controller detects it within 3 samples, switches to `degraded_occupancy`, alert shown |
| T-13 | CO₂ sensor dropout (kill `sensor-co2`) | fault | FR-7, NFR-6 | `degraded_co2` → design flow within 3 samples; recovers when the gateway restarts |
| T-14 | Rebuild a report plot from `GET /runs/{id}/export` alone | integration | FR-8 | The plot matches the one in the report |
| T-15 | Policy flag: diff the configs of two baseline runs | integration | FR-9 | Only `POLICY` differs |
| T-16 | Viewer: layers and alerts during T-09 and T-12 | integration | FR-10 | All four layers present; override and degraded alerts visible (screenshot) |
| T-17 | BuildSim restart: it comes back blank | fault | FR-11 | The registry recreates every device's equipment within 10 s; readings resume |
| T-18 | Air quality over the evaluation period, per baseline | eval | NFR-1 | `predictive`: CO₂ ≤ 1000 ppm for ≥ 95 % of occupied room-minutes |
| T-19 | Comfort over the evaluation period, per baseline | eval | NFR-2 | ≥ 95 % of occupied room-minutes in 20–24 °C |
| T-20 | Energy proxy per baseline | eval | NFR-3 | `predictive` ≥ 30 % below `constant`; compared with `reactive` and `oracle` |
| T-21 | Command latency: `issued_at` → reached state written in BuildSim | perf | NFR-4 | p95 ≤ 1 control period |
| T-22 | Load: 50 rooms, then 100, 200, … until a sample cycle overruns | perf | NFR-5, D-3 | 50 rooms fits one sample period; report the breaking point and what broke first |
| T-23 | Chaos: `docker kill` each of our containers in turn during a run | fault | NFR-6 | No unsafe state; loop resumes within 30 s real; gap visible in storage |
| T-24 | Broker down: stop `mosquitto` for 5 min | fault | NFR-6, D-3 | Controller falls back to polling BuildSim; resumes MQTT afterwards |
| T-25 | Reproducibility: two runs with the same seed and dates | integration | NFR-7, D-7 | True occupancy identical, room by room, minute by minute |
| T-26 | Timed evaluation run | eval | NFR-8 | One baseline over 5 simulated weekdays in ≤ 2.5 h real |
| T-27 | Truth boundary: controller tries to subscribe to `truth/#` and call the truth API | integration | §0 scope, C-1 | Both refused (Mosquitto ACL; no network route) |
| T-28 | Start a device that no installer has registered | integration | FR-11, D-10 | It publishes nothing and accepts no commands; once registered it starts within 5 s |
| T-29 | Installer mistakes: a room not in the floor plan, an unknown kind, an id already taken | unit + integration | FR-11, D-10 | 422, 422, 409; nothing stored |
| T-30 | Retire a device | integration | FR-11, D-10 | It stops reporting at its next check-in (≤ 30 s); its equipment leaves BuildSim |
| T-31 | Register a CO₂ sensor's id as a damper, then start the sensor | integration | D-10 | The device refuses to start and says the registry entry is wrong |
| T-32 | Policies differ only where they should (`policy_test.go`) | unit | FR-9 | Constant = design flow; reactive ignores people; predictive ventilates for expected people in clean air and still reacts to CO₂; flows capped at the room's maximum |
| T-33 | The live run's truth over the storage API | integration | §0 scope | `403`; a finished run's truth is served (for the oracle) |

T-22 is the grade-5 test ("finds the limits", Lecture 1, slide 53): report
where the system stops working, not only that 50 rooms work.
