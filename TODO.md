# garo-energy-controller — observations and TODO

Status as of 2026-10-31. The incremental controller is the default. The model
controller (`control_strategy: "model"`, v2.2, see `controller_model.go`) is
opt-in and regulates **one charging car** (1- or 3-phase); other
configurations fall back to incremental for that tick. v2.1 tested on the real
system with a 3-phase car and with a 1-phase car; v2.2 (household room within
the budget, household-aware hard stop) tested in simulation only. Two-car
charging still to do.

## Goals and constraints

- Primary goal: every hour stays under the tariff step of 10 kWh (the
  9.5 kWh hourly limit setting is the margin below it).
- Running above the instantaneous power target is fine when the hour's budget
  allows it. Do not chase catch-up when charging starts late in an hour (keep
  the catch-up fade).
- Avoid large power swings. Corrections should be small and early, never large
  and late. Ending an hour a bit under target is preferred over swings.
- Must eventually work for every configuration up to 32 A: one 1-phase, one
  3-phase, 1-phase + 3-phase, two 1-phase (same L and different L), two
  3-phase.
- Protect the Pi's SD card: no controller logging written to disk, except
  hard-stop logging (hard stops are rare; their writes are fine).
- The incremental controller stays the default until each configuration has
  been verified on the real system.

## Verified GARO behaviour

Measured with manual DLM steps and logging (3-phase car alone, 1-phase car
alone, both cars). `S_p` = CENTRAL100 current on phase p (household + cars).

### The band

- **Raise:** only when **every phase**, including phases without a car, has
  about **4 A of room** (DLM − S_p). No raise at 3.6–3.8 A, raise at
  3.9–4.1 A. Household load on a phase without a car can block raising
  entirely.
- **Hold:** anywhere between about 4 A below and about 1 A above DLM. DLM
  changes inside this band do nothing.
- **Cut:** when a phase is about **1 A over DLM**: 1 A per car per update.
  When it is about **2 A or more over**: straight to 6 A in one update.
  Observations: 25.9 A / DLM 25 held; 24.0 A / DLM 23 and 25.9 A / DLM 24 cut
  1 A per update; ~17 A / DLM 15 and ~3 A over with two cars cut to 6 A.
  Fits a whole-amp comparison of the measured current with DLM.
- The band is the same at low DLM (13–14 A) as at 24–25 A. (An earlier
  "fixed ~2.5 A reserve" formula was a simplification of this band.)
- With the same DLM, cars and household load, GARO ends in the same state
  reproducibly.

### Cutting

- Per phase: only cars drawing on the overloaded phase are cut.
- Order: **master first**, even when the slave had the higher pilot. In every
  mixed test the master carried the 3-phase car, so "master first" vs
  "3-phase car first" is not yet separated.
- **Overcutting on stale readings:** GARO acts again before measurements catch
  up, so one DLM drop can lead to several cuts (DLM 24 → 20 with the shared
  phase 3 A over gave ~10 A of cuts and a ~3.5 kW dip for ~30 s).
- Reacts within 5–10 s; cars follow within 15–30 s; a change takes 1–2 minutes
  to settle fully.

### Raising

- Master +1 A per update (about every 5–15 s). Slave +2–3 A per update. The
  1-phase car was always on the slave, so "slave" vs "1-phase car" is not yet
  separated.
- Can overshoot by 1–2 A because it raises on readings that have not caught
  up.
- The split between two chargers depends on history, not fairness: both cars
  starting together → equal; one already charging → unequal and stable. GARO
  does not rebalance a stable split.

### Floors, limits and household loads

- 6 A minimum per charging car; with two cars GARO keeps both at 6 A even when
  the target is below 12 A on a shared phase. Only a stop command goes lower.
- A car plugged in but not charging reserves nothing.
- Max pilot is decoded from `dipSwitchSettings` (SW2); CENTRAL101 acts as the
  chargers' sub-fuse.
- Large cuts go to about the minimum: 6 A in most cases, once 8 A (a phase
  ~14 A over DLM).
- Household loads that switch by more than the band (~5 A) on a car's phase
  make GARO cut the car whatever DLM is: GARO raises whenever there is ~4 A of
  room, so room left for bursts fills up. Loads that switch by less than that
  can be absorbed by holding DLM about 2.5 A above the busiest phase (a ~6 A
  cycling load was absorbed this way).
- A DLM write does **not** equalize pilots (+1/−1 test).

### Phase rotation and topology

- `/slaves/false` field `phase`: 16 = L1-L2-L3, 32 = L2-L3-L1, 48 = L3-L1-L2.
  `phase / 16` = the phase GARO assumes a 1-phase car uses on that charger.
  `/lbconfig/false` also has `masterPhase`.
- At the correct setting, the phase a 1-phase car draws on matches the
  rotation (CENTRAL101 and Tibber Faser agree).
- **The rotation describes the wiring; it does not move the car.** Changing
  the slave's rotation and restarting both sessions left the 1-phase car on
  the same physical phase.
- GARO reads the rotation at session start. With a mismatch, GARO protects the
  wrong phase (the physical phase sat at 23.3 A against DLM 24 for 3 minutes
  while GARO raised the other car). The model checks rotation against
  CENTRAL101 and falls back with a WARNING on a mismatch.
- An earlier observation (one car connected) seemed to show the car moving in
  CENTRAL101 after a rotation change. Unexplained; see measurement TODO.
- `chargeStatus: 64` = charging (reliable for both chargers; 16/80 idle,
  144 unavailable). `connector` is unreliable: the slave shows
  `INITIALIZATION` while charging.
- `pilotLevel` per charger is valid. Session fields are only filled in for the
  master. `currentChargingCurrent` (−1), `currentChargingPower` (0) and
  `nrOfPhases` (1) are never populated; chargers have no own meter. Topology
  is inferred from CENTRAL101 per phase, pilots, rotation and `chargeStatus`.

### Cars

- A 1-phase car tested draws ~0.5–2 A below its pilot (more while ramping)
  and stops at ~28–29 A (29.2 A at pilot 31).
- A 3-phase car tested follows its pilot within ~15 s; anomaly: 10.1 A at
  pilot 11 for over a minute (twice).

### SD-card writes

- POST `/lbconfig` **with** `slaves` → Derby transaction log write.
- POST `/lbconfig` **without** `slaves` (what the controller sends), even when
  changing the value → no file changes on the root filesystem. DLM changes are
  RAM-only and not a wear factor.
- Remaining real writes: settings-page saves (`config.json`), saves in GARO's
  own interface, the hard-stop state file and its log line. Mode changes
  (`/mode/...`) are untested but rare.
- Python is not installed on the GARO's OS (Jessie, EOL; apt fails). Use
  sed/grep.

### Tibber, hour boundaries and restarts

- `accumulatedConsumptionLastHour` resets correctly at the full hour.
- Hour boundaries with steady load pass without DLM writes.
- Hard stop (all strategies): stop when what is left of the budget only just
  covers the household for the rest of the hour (averaged household power,
  ~2 min, shown as `household_avg_w`) plus the car until the stop takes
  effect (lead time 180 s, or the rest of the hour). The car stays off until
  the next hour. The older check (limit reached within the lead time at the
  current power) is kept as a safety net. Loads that switch on after the stop
  can still push the hour over; nothing can stop household load.
- Known behaviour: in automatic mode the controller starts by setting DLM to
  `safe_current_a`, so a restart while a car charges drops it to 6 A; the
  model raises again after ~2 minutes (it treats the drop as a recent GARO
  cut). Left as is: restarts are very rare outside testing.

## Model controller v2.2 (implemented, single car)

Energy first, then GARO's band. Power setpoint from hourly pacing
(`EffectiveTargetPowerW`); wanted pilot = (setpoint − averaged household
power) / (phases × V). No inner PI: pacing already integrates the energy
error.

- **Raise:** one write to the DLM at which GARO's own ramp stops near the
  wanted pilot (busiest car phase + rise + 3 A, at least busiest car-free phase
  + 4 A), planned from the recent baseline so a household burst does not
  inflate it. Skipped when household on a car-free phase would let GARO
  overshoot, or when the car draws well below its pilot.
- **Lower:** DLM = busiest phase rounded − 1, i.e. just over 1 A over, so GARO
  cuts 1 A per update instead of slamming to 6 A; one further step if that
  did not trigger a cut. 90 s settle between writes.
- **Hold:** after a cut the model caused, move DLM up to about 2.5 A above the
  recent busiest phase (burst room). Move it down to that position only when
  GARO could raise past the wanted level for at least 90 s (short household
  pauses ignored).
- **No chasing:** after a GARO cut, raises wait 2 minutes. Two GARO cuts or a
  household swing of 5 A or more within 10 minutes count as a cycling load:
  no raises, slower reductions (3 min), and a 3 A deadband for lowering.
- **Deadband:** act only when the wanted pilot differs by 1.5 A or more.
- **Household room within the budget (v2.2):** when the budget covers what
  the car can take (its learned maximum, or the charger's pilot cap while that
  is unknown, so from the start of the ramp), DLM no longer has to limit the
  car. It is then set to household baseline + car maximum + 2.5 A + room,
  where the room is what the hard budget ceiling (highest average power for
  the rest of the hour that keeps it under the limit) leaves after the
  average household and the car at its maximum, capped at
  `maximum_current_a`. A household load using the full room for the rest of
  the hour still keeps the hour under the limit. Hysteresis: entered with
  ≥ 3 A of room and the budget at the car's maximum, left when the room
  drops below 1 A or the budget falls 1.5 A below the car's maximum; the room
  changes only in steps of ≥ 3 A. When the budget covers less than the car
  can take, DLM must limit the car (GARO raises whenever there is ~4 A of
  room) and the normal regulation applies.
- **Scope:** exactly one charging charger (`chargeStatus: 64`), car phases
  recognised from CENTRAL101, rotation consistent with CENTRAL101; otherwise
  incremental for that tick, with the reason in the decision text.

Results on the real system (v2.1):

- 3-phase car: long steady periods with 0 writes; hour boundaries without
  writes; 1–3 writes for large step loads; 0 writes during 10+ minutes of a
  cycling 2–4 kW heating element on a car phase (GARO still cut the car on
  each burst); hours ended under the limit.
- 1-phase car: one write to start the ramp from idle, one to the hold
  position; 0 writes during 20+ minutes of the cycling element on the car's
  phase. GARO cut the car to 6–8 A on each burst (5–6 times), a ~4.8 kW swing
  each time; v2.2's household room is meant to avoid these when the budget
  allows.

Simulation (v2.2), each scenario against five plant variants around the
measured GARO behaviour, at hourly limits of 5, 7.5, 9.5 and 15 kWh with a
1-phase and a 3-phase car and a cycling element from the start of the ramp:
every hour under its limit; where the budget covers car and element, no GARO
cuts. At low limits the car's 6 A floor plus household can exceed the limit;
then only the hard stop helps (5 kWh with a 3-phase car ended at 4.0–4.4 kWh,
so the stop is somewhat conservative).

Lessons from the earlier model (v1, replaced):

- A P term on a lagged (EMA) grid power overshoots both ways.
- Integral windup while GARO holds the car below the model makes swings
  bigger.
- Writing DLM inside GARO's band only costs dwell time; a model that does not
  know the ~4 A raise threshold gets stuck below target.
- In mixed charging a single W/A factor mispredicts power.
- Simulation tests must check swing size and write counts and use a plant
  model that differs from the controller's assumptions.
- Neither the model nor the tests may assume one hourly limit or car: the
  limit is configurable (other tariff steps, e.g. 5, 7.5 or 15 kWh, are just
  as relevant), so tests run across several limits.

| Configuration | c(L1,L2,L3) example | Status |
|---|---|---|
| One 3-phase | 1,1,1 | model v2.1 tested; v2.2 simulation |
| One 1-phase | 1,0,0 | model v2.1 tested; v2.2 simulation |
| 1-phase + 3-phase | 2,1,1 | incremental; model extension next |
| Two 1-phase, different L | 1,1,0 | incremental; needs a 1-phase cable |
| Two 1-phase, same L | 2,0,0 | incremental; needs rewiring; simulation only |
| Two 3-phase | 2,2,2 | incremental; difficult to test; simulation only |

## TODO — measurements

- [x] Model v2.1 with the 1-phase car: ramp from idle, cycling heating
      element on the car's phase.
- [ ] Model v2.2 with the 1-phase car and the cycling element: expect
      "budget covers the car", DLM well above the car, no GARO cuts.
- [ ] New hard stop: set the hourly limit low for one hour (e.g. 5 kWh) with
      the 3-phase car charging; expect an early stop, car off until the full
      hour, hour under the limit in Tibber. Set the limit back afterwards.
- [ ] Swap the cars between chargers (3-phase on slave, 1-phase on master):
      separates "master first" from "3-phase car first" when cutting, and
      "slave +2–3 A" from "1-phase car +2–3 A" when raising.
- [ ] One car only, rotation change + session restart, checked in **Tibber
      Faser and CENTRAL101**: explains the earlier observation. Set the
      rotation back afterwards.
- [ ] Optional: two 1-phase cars (with a 1-phase cable) on different L.
- [ ] Optional: test whether `/mode/...` changes write to the SD card.

## TODO — implementation

- [x] Read `chargeStatus`, `pilotLevel` and `phase` per charger.
- [x] Simulator (single car) with the measured band, cut and raise behaviour,
      stale readings, car limits and household profiles; plant variants
      around the model's assumptions.
- [x] Model v2.1 for one car, rotation/CENTRAL101 mismatch warning, fallback
      to incremental outside scope.
- [x] v2.2: household room within the budget; household-aware hard stop;
      simulation tests across hourly limits.
- [ ] Simulator for two chargers: per-phase car counts, step sizes per
      charger, history-dependent split, 6 A floor per car on a shared phase,
      hard stop for both cars. Run both hypotheses as plant variants
      ("master first" vs "3-phase car first" when cutting; "slave" vs
      "1-phase car" +2–3 A when raising) until the swap test decides.
- [ ] Model for two cars, starting with 1-phase + 3-phase: observe which
      charger GARO actually moves rather than assume it.
- [ ] Duty cycling (stop/start over the hour) for limits below the car's
      floor plus household, where today only the hard stop helps.
- [ ] Possibly: make the hard stop's household estimate less conservative
      (it currently includes the average of household bursts).
- [ ] Status page: show detected topology, binding phase and fallback reason
      outside the decision text.
- [ ] Add `catch_up_fade_seconds` to the settings page (currently config/API
      only).

## Tools

Logger (run on an always-on machine, not the GARO itself):

```bash
while true; do
  curl -s https://<controller-host>:8090/api/status |
    jq -r '.controller as $c | [ (now|strftime("%H:%M:%S")),
      ($c.grid_power_w|floor), $c.dlm_current_a,
      ([$c.pilot_levels[]?.pilot_a]|map(tostring)|join("/")),
      "site", $c.central100_phase1_a, $c.central100_phase2_a, $c.central100_phase3_a,
      "car",  $c.central101_phase1_a, $c.central101_phase2_a, $c.central101_phase3_a,
      $c.decision ] | @tsv'
  sleep 5
done | tee noise.log
```

The controller snapshot updates every ~30 s, so short household bursts may not
show in the `site` columns; Tibber Pulse shows them.

GARO charger list (on the GARO, read-only):

```bash
curl -s http://127.0.0.1:8080/servlet/rest/chargebox/slaves/false > slaves.json
```
