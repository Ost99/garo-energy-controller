# garo-energy-controller — observations and TODO

Status as of 2026-10-30. The incremental controller is the default; the
model controller (`control_strategy: "model"`) is opt-in and will be rewritten
from scratch based on the GARO behaviour measured below. Use incremental with
two cars until then.

## Goals and constraints

- Primary goal: every hour stays under the tariff step of 10 kWh (the
  9.5 kWh hourly limit setting is the margin below it).
- Running above the instantaneous power target is fine when the hour's budget
  allows it. Do not chase catch-up when charging starts late in an hour (keep
  the catch-up fade).
- Avoid large power swings. Corrections should be small and early, never large
  and late. Ending an hour a bit under target is preferred over swings.
- Must work for every configuration up to 32 A: one 1-phase, one 3-phase,
  1-phase + 3-phase, two 1-phase (same L and different L), two 3-phase.
- Protect the Pi's SD card: no controller logging written to disk, except
  hard-stop logging (hard stops are rare; their writes are fine).
- The incremental controller stays the default until each configuration has
  been verified on the real system.

## Verified GARO behaviour

### Load balancing — the band model

Measured with manual DLM steps (3-phase car alone; 1-phase car alone; both cars).
`site_p` = CENTRAL100 current on phase p (household + cars).

- **Cut:** when any phase is about **1 A or more over DLM**. Held at
  25.9 A / DLM 25 and 18.2 A / DLM 18; cut at 24.0 A / DLM 23. Fits a whole-amp
  comparison (floor(site_p) > DLM).
- **Raise:** only when **every phase**, including phases without a car, has
  about **4 A of room** (DLM − site_p). No raise at 3.6–3.8 A room, raise at
  4.1 A. Household load on a phase without a car can block raising entirely
  (a 1-phase car stayed at 6 A while another phase, with no car on it, had
  9–12 A of household load).
- **Hold:** anywhere between about 4 A below and about 1 A above DLM. DLM
  changes inside this band do nothing.
- The band is the same at low DLM (13–14 A) as at 24–25 A. No sign that the
  reserve shrinks at low DLM. (The earlier "fixed ~2.5 A reserve" formula was a
  simplification of this band; superseded.)
- With the same DLM, cars and household load, GARO ends in the same state
  reproducibly (11/11 at DLM 24 several times).

### Cutting

- Per phase: only cars drawing on the overloaded phase are cut (a household
  load on a phase used only by the 3-phase car cut only that car; the 1-phase
  car on another phase kept its pilot).
- Order: **master first**, even when the slave had the higher pilot (9/14 →
  6/14). In every mixed test the master carried the 3-phase car, so "master
  first" vs "3-phase car first" is not yet separated.
- Size: small overloads → 1 A per car per update. Large overloads (a big
  household load switching on) → straight to 6 A.
- **Overcutting on stale readings:** GARO acts again before measurements catch
  up. DLM 24 → 20 with the shared phase 3 A over gave ~10 A of cuts (master
  9 → 6, then slave 14 → 7 with the phase already at 13.4 A); grid
  11.2 → 7.7 kW for ~30 s, then re-ramp. Even a 1 A DLM step gave an extra 1 A cut.
- Reacts within 5–10 s; cars follow within 15–30 s; a change takes 1–2 minutes
  to settle fully.

### Raising

- Master +1 A per update (about every 5–15 s). Slave +2–3 A per update. The
  1-phase car was always on the slave, so "slave" vs "1-phase car" is not yet
  separated.
- Can overshoot DLM by about 1 A because it raises on readings that have not
  caught up (25.8–25.9 A at DLM 25).
- The split between two chargers depends on history, not fairness: both cars
  starting together → equal (9/9, 11/11); one already charging → unequal and
  stable (9/14 at DLM 24). GARO does not rebalance a stable split.

### Floors, limits and oscillating loads

- 6 A minimum per charging car; with two cars GARO keeps both at 6 A even when
  the target is below 12 A on a shared phase. Only a stop command goes lower.
- A car plugged in but not charging reserves nothing.
- Max pilot is decoded from `dipSwitchSettings` (SW2); CENTRAL101 acts as
  the chargers' sub-fuse.
- Oscillating household loads on a car phase cause unavoidable cut cycles when
  they exceed about 5 A (the width of the band): GARO raises whenever there is
  about 4 A of room, so room left for bursts fills up. Example: a cycling
  2–3 kW heating element on the phase shared by both cars, at fixed DLM 24:
  master cut to 6 roughly every 60 s, ~4 kW grid swings. GARO's 10–15 s reaction often lands as the burst ends, so the
  cut and the load drop add up.
- A DLM write does **not** equalize pilots (+1/−1 test). Earlier apparent cases
  were GARO reacting to a household load.

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
  wrong phase: the physical phase sat at 23.3 A against DLM 24 for 3 minutes
  while GARO raised the master and cut the slave. A rotation/CENTRAL101 mismatch is a
  safety issue.
- An earlier observation (one car connected) seemed to show the car moving in
  CENTRAL101 after a rotation change. Unexplained; see measurement TODO.
- `chargeStatus: 64` = charging (reliable for both chargers; 16/80 idle,
  144 unavailable). `connector` is unreliable: the slave shows
  `INITIALIZATION` while charging.
- `pilotLevel` per charger is valid. Session fields are only filled in for the
  master. `currentChargingCurrent` (−1), `currentChargingPower` (0) and
  `nrOfPhases` (1) are never populated; chargers have no own meter. Topology
  must be inferred from CENTRAL101 per phase, pilots, rotation and
  `chargeStatus`.

### Cars

- A 1-phase car tested draws ~0.5–1 A below its pilot and stops at ~28 A.
- A 3-phase car tested follows its pilot within ~15 s; anomaly: 10.1 A at
  pilot 11 for over a minute (twice).

### SD-card writes

- POST `/lbconfig` **with** `slaves` → Derby transaction log write.
- POST `/lbconfig` **without** `slaves` (what the controller sends), even when
  changing the value → no file changes anywhere on `/dev/root` (the only
  filesystem). DLM changes are RAM-only and not a wear factor.
- Remaining real writes: settings-page saves (`config.json`), saves in GARO's
  own interface, the hard-stop state file and its log line. Mode changes
  (`/mode/...`) are untested but rare.
- Python is not installed on the Pi (Jessie, EOL; apt fails). Use sed/grep.

### Tibber and hour boundaries

- `accumulatedConsumptionLastHour` resets correctly at the full hour.
- A mixed-charging hour boundary with steady load was uneventful (no DLM write,
  no drop).

## Lessons from the current model controller

- P term on a 180 s EMA of grid power lags by minutes: over-reduction and
  overshoot around hour boundaries. Do not put a P term on lagged data.
- Integral wound up while GARO held the car below the model (cycling loads).
  Patched (no integration while the car is > 1.5 A below the prediction).
- The final-minutes no-increase rule locked the model at a too-low DLM.
  Patched (model exempt).
- It does not know GARO's band: it wrote small DLM changes inside the band
  (no effect, only dwell time), and got stuck when raising needed ~4 A of room
  (pilots 7/7 at DLM 17, ~1.5 kW under target, hour ~1 kWh short). The dwell
  from useless writes delayed the start of a new hour by 35 s.
- In mixed mode it converts at 720 W/A as if one 3-phase car, so a given DLM
  gives more power than predicted (~700 W above target for 10+ minutes).
- A 1-phase car at its own maximum made the model write DLM five times without
  effect.
- Simulation tests must check swing size (peak grid power, max integral) and
  use a plant model that differs from the controller's assumptions.

## New model design (to implement)

1. **Energy first:** power setpoint from the hour's remaining budget (pacing,
   catch-up fade, hard budget ceiling, as today). Running above target is fine
   when the budget allows; accuracy matters so corrections stay small and early.
2. **Work with GARO's band, not against it.** Let `S` = max over all phases of
   `site_p` (CENTRAL100), including phases without a car.
   - **Raise one GARO step:** DLM ≥ S + ~4 A (global condition), one write;
     GARO then steps the pilots itself.
   - **Hold:** keep DLM inside the band. Prefer the upper part (about S + 3.5 A)
     so household bursts of up to ~4.5 A do not trigger cuts, as long as
     household noise does not trigger raises.
   - **Lower:** 1 A per write, at least 60–90 s apart, to avoid GARO's
     overcutting. Larger drops only when the hard budget requires it; the hard
     stop handles emergencies.
   - After any write, wait 1–2 minutes before judging the result.
3. **Predict power from actual pilots**, not an assumed split: power per phase =
   CENTRAL101; which charger gets the next amp follows GARO's order (master +1,
   slave +2–3; master cut first).
4. **Floors and blocks:** no DLM writes below the floor (6 A per charging car
   on a shared phase) — leave that to the hard stop. Recognise when household
   load on a phase without a car blocks raising (S − car-free phase household
   < 4 A) and do not write uselessly.
5. **Oscillating loads:** detect repeated GARO cuts with DLM unchanged and do
   not chase them; keep DLM steady and let hour pacing handle the energy.
6. **Topology:** active chargers from `chargeStatus: 64`; GARO's wiring view
   from `phase / 16`; physical phases from CENTRAL101. A 1-phase car drawing on
   a different phase than the rotation says → clear warning on the status page,
   conservative DLM, fall back to incremental.
7. **Car limits:** learn per session when a car draws less than its pilot
   (e.g. the 1-phase car at ~28 A) and plan around it.
8. **Per-configuration enablement:** the model only regulates configurations
   verified on the real system; others fall back to incremental until marked
   as tested in settings.

| Configuration | c(L1,L2,L3) example | Real test possible |
|---|---|---|
| One 1-phase | 1,0,0 | yes |
| One 3-phase | 1,1,1 | yes |
| 1-phase + 3-phase | 2,1,1 | yes |
| Two 1-phase, different L | 1,1,0 | with a 1-phase cable |
| Two 1-phase, same L | 2,0,0 | needs rewiring; simulation only |
| Two 3-phase | 2,2,2 | difficult; simulation only |

## TODO — measurements

Done: reserve/band (high and low DLM), two-car split and ramp order, per-phase
cutting, cut tolerance, raise threshold, larger DLM drop, DLM-write
equalization (rejected), oscillating load on the shared phase, hour
boundary with two cars, plugged-in idle car, rotation codes 16/32/48, rotation
as wiring description, per-charger API fields, DLM writes RAM-only.

- [ ] Swap the cars between chargers (3-phase on slave, 1-phase on master):
      separates "master first" from "3-phase car first" when cutting, and
      "slave +2–3 A" from "1-phase car +2–3 A" when raising.
- [ ] One car only, rotation change + session restart, checked in **Tibber
      Faser and CENTRAL101**: explains the earlier observation (physical move,
      CENTRAL101 relabelling, or a misread). Set the rotation back afterwards.
- [ ] Optional: two 1-phase cars (with a 1-phase cable) on different L.
- [ ] Optional: test whether `/mode/...` changes write to the SD card
      (`touch marker` → mode change → `find -newer` → restore mode).

## TODO — implementation

- [ ] Simulator for all six configurations with the measured GARO behaviour:
      band (cut ≥ ~1 A over, raise only with ~4 A room on every phase), cut
      size (1 A per car per update, straight to 6 for large overloads),
      overcutting on stale readings, master-first cutting, master +1 /
      slave +2–3 raising with ~1 A overshoot, history-dependent splits, 6 A
      floor per car, 32 A CENTRAL101, cars drawing below pilot, household
      profiles (steady, cycling single-phase loads of 2–4 kW,
      steady load on a car-free phase).
- [ ] Tests per configuration: hour energy ≤ limit, peak grid power and swing
      size, number of DLM writes, no writes inside the band, behaviour at the
      floor and under oscillating loads. Plant model must differ from the
      controller's assumptions.
- [ ] Read `chargeStatus`, `pilotLevel` and `phase` per charger from
      `/slaves/false` (read-only); map 16/32/48, anything else = unknown.
- [ ] Rewrite `controller_model.go` per the design above; keep pacing, hard
      stop, incremental controller, strategy switch and settings plumbing.
- [ ] Rotation/CENTRAL101 mismatch detection and warning.
- [ ] Settings: per-configuration "verified" flags; status page shows the
      detected topology, the binding phase and any fallback reason.
- [ ] Add `catch_up_fade_seconds` to the settings page (currently config/API
      only).

## Tools

Logger (run on the Linux machine, not the Pi):

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

Note: the controller snapshot updates every ~30 s, so short household bursts
may not show in the `site` columns; Tibber Pulse shows them.

GARO charger list (on the Pi, read-only):

```bash
curl -s http://127.0.0.1:8080/servlet/rest/chargebox/slaves/false > slaves.json
```
