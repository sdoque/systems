# Standard Mission Taxonomy

**Status:** Working specification. Pre-implementation. Subject to refinement once a
testbed deployment exercises it.

## Purpose

A *mission* is a coarse-grained classification of what a unit asset *is for*. It is
the primary axis along which the authorizer evaluates policies. Missions are
declared by each asset (in the system's `systemconfig.json`) and travel with the
asset's service-registration record, so the authorizer can read missions from the
service registrar rather than from every system's local file.

The mission taxonomy is intentionally small. Too many missions becomes
indistinguishable from per-asset enumeration; too few cannot express real
distinctions. The eight missions below are the working set. Additions require a
deliberate revision of this document.

## The taxonomy

### `measurement`

Assets that observe physical or digital state without changing it.

- **Examples:** temperature sensor, position encoder, voltage probe, packet counter.
- **Typical actions:** `read` for any consumer with a legitimate use; `write` only
  rarely (calibration parameters, set-points for the sensor's own operation).
- **Pairing:** typically location-bound. A bathroom temperature sensor is paired
  to bathroom-class consumers via `functional_location`.

### `actuation`

Assets that change physical or digital state.

- **Examples:** servo position setter, valve open/close, heater plug, pump speed
  command.
- **Typical actions:** `write` only by authorised controllers; `read` permitted
  for status display and audit.
- **Pairing:** location-bound. A kitchen heater plug is paired to kitchen-class
  controllers.

### `state`

Internal mode, schedule, or configuration of a system or asset.

- **Examples:** thermostat target temperature, scheduler entries, operating mode
  (auto/manual/off).
- **Typical actions:** `read` widely; `write` by commissioning or maintenance role.
- **Pairing:** typically per-system, not location-bound.

### `event`

Ephemeral notifications, alarms, transitions.

- **Examples:** door-opened event, threshold-crossed alarm, mode-change announcement.
- **Typical actions:** `read` (subscribe) widely; `write` (publish) only by the
  asset that owns the event source.
- **Pairing:** event-stream-bound, occasionally location-bound.

### `aggregation`

Derived or computed values built from other assets' outputs.

- **Examples:** rolling-window mean, hourly average, count over a tag set.
- **Typical actions:** `read` widely; `write` only by the aggregator producing the value.
- **Pairing:** typically not location-bound (aggregations span locations by design).

### `logging`

Write-only sinks for audit trails or data.

- **Examples:** audit log, time-series database ingestion endpoint, alarm history.
- **Typical actions:** `write` widely; `read` only by audit and analytics roles.
- **Pairing:** typically not location-bound (logs are cloud-wide by design).

### `control`

Bidirectional control loops that both observe and act on physical state.

- **Examples:** PID controller, feedback loop, servo position-and-feedback combined.
- **Typical actions:** `read` and `write` together; the consumer expects both as a
  paired use.
- **Pairing:** location-bound, like `actuation`.

### `core`

Framework infrastructure: service registrar, orchestrator, certificate authority,
authorizer itself, maitreD.

- **Examples:** the four core systems of an Arrowhead local cloud.
- **Typical actions:** restricted; framework-only roles.
- **Pairing:** never location-bound — core systems serve the whole cloud.

## When the taxonomy doesn't fit cleanly

Most assets land in exactly one mission. Two situations require care:

### A service that is genuinely both measurement and actuation

The parallax `position` service is the canonical example: GET reads the current
position; PUT sets a new one. Two design choices, in increasing complexity:

1. **Split into two services** within the same asset: `position-read` (mission
   `measurement`) and `position-write` (mission `actuation`). Cleanest but
   requires the implementation to expose two endpoints.
2. **Mark the asset as `actuation`** and let read access be granted to broader
   subjects via policy (the collector's policy in the example below). Pragmatic
   for the common case where read is permissive but write is tight.

For mbaigo today, option 2 is operationally simpler. Option 1 is the right move
if the read and write semantics ever need to be authorised differently for
different consumers.

### Multi-mission assets

If an asset honestly serves two missions (e.g. a controller that is both
`actuation` and `state`), declare both in the systemconfig:

```json
"unit_assets": [
  {
    "name": "servo1",
    "missions": ["actuation", "state"],
    ...
  }
]
```

Policy matching uses *any* match (the asset's mission set ∩ the policy's mission
set must be non-empty).

## Versioning

This taxonomy is part of the authorization contract. Changes — adding a mission,
splitting one, deprecating one — must be propagated to every system's
configuration. We treat changes here as a versioned event, with a corresponding
note in the paper (or the journal-paper revision history once that is published).

| Date | Change |
|------|--------|
| 2026-04-30 | Initial taxonomy: measurement, actuation, state, event, aggregation, logging, control, core |
