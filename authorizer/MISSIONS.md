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
- **Typical actions:** `write` only by authorized controllers; `read` permitted
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

- **Examples:** proportional-integral-derivative (PID) controller, feedback loop, servo position-and-feedback combined.
- **Typical actions:** `read` and `write` together; the consumer expects both as a
  paired use.
- **Pairing:** location-bound, like `actuation`.

### `transaction`

Business records and exchanges rather than physical or digital process state.

- **Examples:** order entry, order persistence, maintenance notifications, work
  order confirmations, enterprise resource planning (ERP) interfaces.
- **Typical actions:** `read` and `write` by the roles that own the business
  process; rarely by process-control subjects.
- **Pairing:** not location-bound. An order is not in a room.

Named for the activity, as the other missions are. "Business" would name a
domain rather than an activity, and would invite argument about what in a plant
is *not* business.

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
if the read and write semantics ever need to be authorized differently for
different consumers.

### Multi-mission assets

Not supported: an asset declares exactly one mission.

```json
"unit_assets": [
  {
    "name": "servo1",
    "mission": "actuation",
    ...
  }
]
```

An earlier draft allowed a set of missions per asset, with policy matching on a
non-empty intersection. The case that motivated it — an asset whose single
service both reads and writes — is handled by the policy's `actions` field
instead. An asset that genuinely serves two missions is usually two assets. See
*Where missions live* below for the reasoning.

## Where missions live

**`components.UnitAsset.Mission` is the field.** That is what it is for. The
taxonomy above is its vocabulary; the free-text values in use today
(`measure_temperature`, `control_heater`, `electric_heating`, `capture_video`)
predate the taxonomy and are to be migrated to it.

A parallel field — a `Details["Missions"]` key, or any new attribute alongside
`Mission` — was considered and rejected. Optional metadata that a commissioning
technician can leave blank *is* left blank; this is the standard experience with
the information models of OPC UA (Open Platform Communications Unified
Architecture). Two fields covering one concept guarantees that the
obvious one gets filled and the one authorization depends on does not. The
plumbing saved is not worth the data not collected.

### Assets that are interfaces, not things

An asset's mission is the right granularity when the asset *is* a thing — a
sensor, a valve, a controller. It is too coarse when the asset is an **interface
to things**: a Modbus or OPC UA front end, an MQTT bridge, a ZigBee gateway. The
programmable logic controller (PLC) is not the asset; what is wired to it is. A read-only register observes and a
writable one acts, and they can sit in different functional locations.

Two mechanisms cover this:

1. **`components.Service` may declare its own `Mission`**, which overrides the
   asset's. `components.EffectiveMission(asset, service)` resolves the pair: the
   service's where it declares one, the asset's otherwise. The registration
   record carries the *service's* effective mission
   (`usecases/registration.go:239`), because that is what the authorizer must
   evaluate.
2. **Derivation at instantiation.** Where the access mode is already known, the
   mission is computed in `newResource` rather than configured. Modbus states it
   in the register map — `"00001,Slider1_Front_PB,ro,Boolean"` is `measurement`,
   `"00001,Slider1_Motor_Forward,rw,Boolean"` is `actuation` — and OPC UA node
   access levels say the same thing. No new configuration is needed for these.

Declaration is for what cannot be derived. An MQTT topic path discloses nothing
about what is behind it, so `telegrapher` declares a mission per service in its
configuration alongside `definition`, `subpath` and `details`.

### Where validation runs

Over the **constructed** unit assets, not over the configuration file:
`usecases.ValidateMissions(sys)`. For the interface family above, the mission is
determined at instantiation and is legitimately absent from the file; validating
at configuration time would reject those systems for omitting something they are
supposed to compute.

### Single-valued

`Mission` stays a single string. An earlier draft of this document allowed a set
(`["actuation", "state"]`), motivated by assets whose service both reads and
writes — parallax's `position`, modboss's `signal`. That case is handled by the
policy's `actions` field, not by a second mission (see POLICY.md, *Why `services`
is needed*). "What is this asset for? Pick one" is unambiguous for the person
filling it in, and ambiguity is what causes fields to be skipped or filled
wrongly. An asset that honestly has two missions is usually two assets.

### What has to change

- `Mission string` on `forms.ServiceRecord_v1`, populated at registration
  (`usecases/registration.go:235`). A form-version bump. Without it the mission
  never reaches the authorizer: registration currently copies only `Details` into
  the record.
- No change to the ESR, the ephemeral service registrar. Its filter walks
  `Details` only
  (`systems/esr/thing.go:261-280`), but it does not need to know about missions —
  the authorizer receives the whole candidate list and evaluates mission itself.
- `afo:hasMission` in the knowledge graph becomes a controlled vocabulary rather
  than free strings, which is a straight improvement there.

### Not skippable, rather than merely required

20 of 35 configured unit assets carry no mission today. If an absent mission
defaults to anything, the default will be discovered and relied upon — and any
default is either too permissive to be safe or too restrictive to be tolerated,
so it will be worked around.

Instead, **validate `Mission` against this taxonomy at system startup and refuse
to start** when it is missing or unrecognized, naming the valid values in the
error. A field the system will not boot without cannot be skipped. This is the
mechanism that makes the mission trustworthy enough to authorize against.

Migration is largely mechanical, since the existing free text already encodes the
intent: `measure_temperature` → `measurement`, `control_heater` → `control`,
`electric_heating` → `actuation`, `capture_video` → `measurement`. The
specificity that is lost is carried by the asset's name and its service
definitions.

## Framework invariant: every system provides at least one service

**Every mbaigo system must provide at least one service.** A system that only
consumes is not permitted.

The justification is accountability, not fairness. In a service-oriented
architecture registration *is* existence: a system that registers nothing has no
health any operator can query, no attributes for the authorizer to evaluate, no
node in the knowledge graph, and no cost or carbon accounting — `ACost` and
`CFootprint` are per-service. It cannot be seen, audited, or reasoned about.
That a purely consuming system also gives nothing back is the weaker objection;
a dashboard gives nothing to any machine and is perfectly legitimate.

The invariant already holds in practice. All twelve systems that declare cervices
— beehive, clerk, collector, ethermostat, flattener, leveler, nurse, recognizer,
sapper, telegrapher, thermostat, tracker — also provide at least one service,
including `collector`, the archetypal logger. The three configurations with no
`services` block (beekeeper, meteorologue, weatherman) build theirs in Go at
runtime from a spec table. Codifying the rule changes no existing system.

**Enforce it structurally, not by convention.** Every system already serves
`/doc`, `/kgraph`, `/smodel`, `/cert` and `/msg` unconditionally
(`usecases/servers_handlers.go:216-233`), and every unit asset already has its own
`/doc`. Promoting one of these — a per-asset `status` service is the obvious
candidate — to an automatically registered service makes the invariant hold by
construction. No reviewer has to check for compliance, and the authorizer is
guaranteed a registration record from which to resolve any subject's attributes.

Mandating a service without providing one produces hollow endpoints that exist
only to satisfy the rule. Auto-generation avoids that. A system that somehow
registers zero services should log loudly or refuse to start.

### What the invariant does and does not buy

It makes POLICY.md's step 4 — *asset has an attribute value, subject has none →
constraint violated* — correct by construction rather than by assumption. Without
it, a subject with no attributes might be a legitimate pure consumer; with it, it
is necessarily a misconfiguration, and failing closed is right.

It does **not** resolve which asset's attributes apply when a multi-asset system
consumes. `telegrapher` provides from both `Bathroom/temperature` and
`Kitchen/temperature` under one certificate common name (CN). See POLICY.md, *Deployment
constraints*.

Outward-pushing systems (SMS gateway, cloud uplink) will end up with a `status`
service and nothing else. That is acceptable — but that service is their
accountability handle, not their contribution.

## Versioning

This taxonomy is part of the authorization contract. Changes — adding a mission,
splitting one, deprecating one — must be propagated to every system's
configuration. We treat changes here as a versioned event, with a corresponding
note in the paper (or the journal-paper revision history once that is published).

| Date | Change |
|------|--------|
| 2026-04-30 | Initial taxonomy: measurement, actuation, state, event, aggregation, logging, control, core |
| 2026-07-28 | Added the every-system-provides-a-service invariant; settled `UnitAsset.Mission` as the field carrying this taxonomy, single-valued, validated at startup; dropped multi-mission assets |
| 2026-07-28 | Added the `transaction` mission for order entry, order persistence and ERP interfaces, which none of the eight process-oriented missions fitted |
| 2026-07-28 | Added service-level missions (`Service.Mission`, `EffectiveMission`) for assets that are interfaces to things rather than things; validation moved to the constructed assets |
