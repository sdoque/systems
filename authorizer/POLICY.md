# Authorizer Policy Schema

**Status:** Working specification. Pre-implementation.

This document defines the policy file format read by the authorizer service, the
evaluation semantics, and the wire shape of the tokens the authorizer issues.
It is the contract every other piece of code in the authorizer system
will touch; getting it right before implementation prevents rework.

## The file: `policies.json`

Operator-edited, version-controlled, lives in the authorizer's working directory.
A flat JSON object with two top-level keys:

```json
{
  "policies":  [ ... ],   // explicit allow rules (deny by default)
  "denials":   [ ... ]    // optional, narrow exceptions to allow rules
}
```

A missing or empty file means *deny everything* — the authorizer issues no
tokens and every authenticated system is functionally inert. This is fail-closed
by construction, mirroring the CA's whitelist semantics.

## Policy entries

Each entry in the `policies` array is an allow rule:

```json
{
  "subject":              "thermostat",
  "missions":             ["actuation", "measurement"],
  "services":             ["signal"],
  "actions":              ["read", "write"],
  "must_match_attribute": "FunctionalLocation",
  "ttl":                  "10m"
}
```

| Field | Type | Required | Meaning |
|-------|------|----------|---------|
| `subject` | string | yes | The common name (CN) of the consumer's mutual Transport Layer Security (mTLS) certificate. `"*"` matches any authenticated subject. |
| `missions` | string[] | yes | Mission names from MISSIONS.md the policy authorizes. `["*"]` matches any mission. |
| `services` | string[] | no | Service definitions the policy authorizes. Omitted or `["*"]` means every service of a matching asset. |
| `actions` | string[] | yes | One or more of `read`, `write`, `invoke`, or `*`. |
| `must_match_attribute` | string | no | If set, an additional attribute-based access control (ABAC) constraint: the named attribute must match between subject and asset (see *Pairing semantics* below). |
| `ttl` | duration string | no | Token lifetime if this policy authorizes the request. Defaults to `5m`. |

### `ttl` and which one applies

The time to live (TTL) is how long the issued token stays valid — how long the consumer may
keep calling the provider on the strength of one authorization. It is the only
bound on revocation: edit `policies.json` to withdraw a permission and tokens
already issued keep working until they expire. Short TTLs mean fast revocation
and more orchestration traffic; long ones mean the opposite.

More than one rule can authorize the same request, and they may set different
lifetimes. **The shortest TTL among the matching rules applies.** Revocation
latency should follow the most cautious rule an operator wrote, not the order
the rules happen to appear in — a broad `"*"` rule with a generous lifetime must
not lengthen the leash a narrow rule deliberately kept short.

### Why `services` is needed

Mission is a property of the *unit asset*; the permission boundary is frequently
inside a single service. `modboss` — the Wago programmable logic controller (PLC) — has one unit asset and one
service, `signal`, whose handler serves both GET and PUT
(`systems/modboss/thing.go:179-182`). The parallax `position` service has the same
shape.

So in "the thermostat may set the valve position, the collector may only read it",
both consumers face the same asset, the same mission and the same service. Mission
separates nothing there; the `actions` field does all the work. Mission earns its
keep one level up, by keeping a controller away from an entire *class* of asset
(`logging`, `core`, `aggregation`) whatever the action.

`services` covers the remaining case: two services on one asset, same mission,
same action. If the PLC later exposes `firmware` alongside `signal`, both are
`write` on mission `actuation`, and without a service selector no policy can
permit one and refuse the other. The `denials` list keys on `(subject, asset)`,
not on service, and the issued token carries a `service` claim that no policy
field would otherwise constrain.

A request is authorized iff at least one policy entry matches AND no `denials`
entry matches.

## Denials (the escape hatch)

For the rare case where a broad policy must carve out a specific exception:

```json
{
  "denials": [
    {"subject": "thermostat", "asset": "parallax/basement-servo"}
  ]
}
```

Each denial blocks one (subject, asset) pair regardless of any matching policy.
Denials should be kept few; if they multiply, the corresponding policy is
likely too broad and should be tightened instead.

## Action vocabulary

Three abstract actions, each mapped to mbaigo cervice modes and HTTP semantics:

| Action | Cervice mode | HTTP method | Meaning |
|--------|--------------|-------------|---------|
| `read` | `get` | GET | Observe state without changing it |
| `write` | `set` | PUT, PATCH | Change asset state |
| `invoke` | `do` | POST | Trigger an ephemeral action (e.g. publish event, fire alarm) |

`*` matches any of the three.

**`invoke` is specified ahead of the framework.** Only `get` (7 uses) and `set`
(6 uses) appear as cervice modes anywhere in the codebase; there is no `do` mode
in `components.Cervice`. Either add it when the first genuine event-publishing
consumer appears, or drop `invoke` from the first implementation. It should not
be treated as working until a cervice can express it.

## Pairing semantics (`must_match_attribute`)

A policy may declare `must_match_attribute` to require that the named attribute
match between the subject and the asset.

**The name is the registration detail key, exactly as spelled there** —
`FunctionalLocation`, not `functional_location`. Details travel from the unit
asset into every service record verbatim
(`usecases/registration.go:235-237`), and the lookup is a plain map access, so a
policy naming a key that differs by so much as a capital letter pairs with
nothing and silently refuses every request it governs.

The spelling is not free to change either: `usecases/kgraphing.go:269` keys on
the literal `"FunctionalLocation"` to emit `afo:hasFunctionalLocation` rather
than a local `alc:has…` term. Renaming the detail to suit a policy file would
quietly drop the asset out of the alignment with the reference ontologies —
the Arrowhead Framework Ontology (AFO), the Industrial Data Ontology (IDO),
Data Exchange in the Process Industry (DEXPI) and the Standard for the Exchange
of Product model data (STEP) — that the graph is built to match.

The match algorithm:

1. Look up the named attribute on the subject (from its registration record).
2. Look up the named attribute on the asset (from the service-registrar entry).
3. If the asset has no value for the attribute → **constraint satisfied**
   (asset is "unpaired" and universally accessible to subjects of the right mission).
4. Else if the asset has a value AND the subject has no value → **constraint violated**.
5. Else: at least one of the subject's values must equal at least one of the
   asset's values (multi-valued match by intersection non-empty).

Rationale for step 3: in operational technology (OT) plants, many sensors and actuators are not associated
with a specific location/zone — they're cloud-wide utilities (audit logs,
aggregations, framework infrastructure). Forcing every consumer to declare a
match key for these would be over-engineering.

Rationale for step 4: a subject *with* a defined location/zone consuming an
asset *with* a defined location/zone is the security-relevant case; a missing
subject side is an operator misconfiguration that should fail closed.

That last claim is only true because of the framework invariant in MISSIONS.md:
every system provides at least one service, therefore every system has a
registration record, therefore every subject has attributes to look up. Without
the invariant a missing subject side could equally be a legitimate
consume-only system, and failing closed would be wrong. The two documents are
coupled here.

## Worked examples

The eThermostat scenario, expressed in policy form. Setup:

- Asset `bathroom-sensor` has mission `measurement`, attribute `FunctionalLocation: ["Bathroom"]`.
- Asset `bathroom-heater` has mission `actuation`, attribute `FunctionalLocation: ["Bathroom"]`.
- Asset `cloud-aggregator` has mission `aggregation`, no `FunctionalLocation`.
- Subject `thermostat-bathroom` has attribute `FunctionalLocation: ["Bathroom"]`.
- Subject `thermostat-kitchen` has attribute `FunctionalLocation: ["Kitchen"]`.
- Subject `collector` has no `FunctionalLocation`.

Policies:

```json
{
  "policies": [
    {
      "subject": "thermostat-*",
      "missions": ["measurement", "actuation"],
      "actions": ["read", "write"],
      "must_match_attribute": "FunctionalLocation"
    },
    {
      "subject": "collector",
      "missions": ["measurement", "actuation", "aggregation"],
      "actions": ["read"]
    }
  ]
}
```

Resolution:

| Request | Match? | Reason |
|---------|--------|--------|
| `thermostat-bathroom` reads `bathroom-sensor` | allow | mission `measurement` ∈ policy; locations match |
| `thermostat-bathroom` writes `bathroom-heater` | allow | mission `actuation` ∈ policy; locations match |
| `thermostat-kitchen` writes `bathroom-heater` | deny | locations don't match |
| `thermostat-bathroom` reads `cloud-aggregator` | deny | policy missions don't include `aggregation` |
| `collector` reads `bathroom-sensor` | allow | no `must_match_attribute`; mission and action allowed |
| `collector` writes `bathroom-heater` | deny | `write` not in collector's actions |

## Subject identity

The `subject` of every decision is the Common Name of the **verified client
certificate** on the incoming TLS connection. It is never
`ServiceQuest_v1.RequesterName`, which is filled from `sys.Name`
(`usecases/service_discovery.go:109`) and is therefore self-asserted — any system
can claim to be the thermostat. A policy engine fed a self-asserted subject is
decoration.

This has a hard prerequisite: the connection must be mTLS. The HTTPS server is
configured with `tls.RequireAndVerifyClientCert` against the CA pool
(`usecases/servers_handlers.go:128-133`), so identity is available and verified
there. On the plain-HTTP server — the same handler, since both bind the default
mux — `r.TLS` is nil and there is no identity at all.

**A request carrying no verified subject is refused before any rule is
consulted**, including a rule whose subject is `"*"`. The wildcard means "any
*authenticated* subject", never "no subject required": a policy file written to
be permissive during commissioning must not become an open door on the plain-HTTP
port, where nothing identifies the caller. Refusing first also keeps the two
failures distinguishable in the audit trail — "nobody may do this" reads very
differently from "we do not know who asked".

## Enforcement model

Two distinct mechanisms, both required:

1. **Filtering, at the Orchestrator.** The Authorizer prunes the provider list
   before the Orchestrator selects from it, so a consumer never learns the URL of
   a service it may not use. This is least privilege applied to *discovery*.
2. **Verification, at the provider.** The provider checks the token's signature,
   expiry, and claims against the request actually being served. This is the only
   mechanism that *enforces* anything.

Filtering alone is advisory. `stateHandler` caches provider URLs in `cer.Nodes`
and re-orchestrates only when that cache is empty
(`usecases/consumption.go:51-56`), and nothing compels a client to consult the
Orchestrator at all. A hand-written consumer can call any provider URL directly.

### The bootstrap plane is exempt

**A service whose effective mission is `core` is served without a token.**

This is not a concession; without it authorization cannot be switched on at all.
One configuration list, `Husk.CoreS`, says two different things: *this is the
authorizer I verify against*, and *I demand tokens on my own services*. The
Orchestrator must name the Authorizer to consult it — and doing so made
`/squest` demand a token, when a service quest is precisely where a token comes
from. Every discovery request would have been refused and no token could ever
have been issued. The Service Registrar deadlocks the same way: `RegisterServices`
sends a bare POST, and it could not do otherwise, since obtaining a token
requires a registry to discover the provider in.

So discovery, registration, certification and attestation are exempt. They are
what make tokens possible and therefore cannot be gated by one.

The exemption is decided on the **mission**, not on the system name, for two
reasons. The mission is already the vocabulary policy is written in, so a core
system that gains a service does not have to be remembered in a list. And a
service's own mission overrides its asset's, so a core system offering something
that is not core — a registrar publishing a temperature — has it gated like
anything else. Without that, `core` would become a place to hide a service from
policy.

It is checked **before** the key is required, so a provider still fetching the
Authorizer's public key answers core requests rather than 503. A registrar that
refused registrations for the length of that fetch would make the cloud look as
though it had to be started in a fixed order, which it does not.

**State the boundary plainly: any system this cloud has enrolled may call a core
service without a token.** It can register services, query the registry, request
orchestration and ask the CA to certify it. What protects that plane is the layer
beneath — mutual TLS with a certificate the CA signed only for a binary whose
SHA-256 is on the whitelist (see *Composition with the CA whitelist*).
Policy governs what a system may *do with* the cloud's assets; attestation
governs whether it is in the cloud at all.

This is not a hole a rogue provider can climb through by declaring itself core.
Enforcement is already the provider's own choice — a system that did not want to
check tokens would simply leave the Authorizer out of its configuration — so a
provider calling its own service core weakens nothing that was not already its
to weaken.

Two consequences worth knowing before enabling anything:

- The Registrar can be given the Authorizer in its core list, and registration
  will still work. But **deregistration will not**: `ActionForMethod` maps DELETE
  to the empty string by design, and no token claim can match it. A registrar's
  `unregister` is core-mission and therefore exempt today; if that ever changes,
  deregistration breaks.
- Filtering at the Orchestrator still applies to core services. The exemption is
  about the *provider's* token check, not about which candidates a consumer is
  shown.

### Token delivery

The token travels back to the consumer inside the orchestration response:
`forms.ServicePoint_v1` already carries a `Token string` field
(`forms/servicequest_forms.go:62`) that is currently set and read nowhere. No new
form is required for delivery, and the consumer needs no extra round trip.

The Authorizer is consulted once per orchestration with the whole candidate list,
not once per candidate. That call needs a new request/response form pair; the
candidate list itself reuses `ServiceRecordList_v1`.

### Token renewal

Renewal already works, by accident. `sendHTTPReq` returns an error for any
non-2xx response (`usecases/utilities.go:192-194`), and `stateHandler` clears
`cer.Nodes` whenever a request errors (`usecases/consumption.go:68`), which forces
a fresh `Search4Services` — and therefore a fresh token — on the next call. A
provider answering 403 to an expired token self-heals. It is coarse, discarding
every cached node rather than the stale one, but no separate renewal loop is
needed.

## Deployment constraints

### Reach the Orchestrator over HTTPS

**A consumer whose `coreSystems` entry for the Orchestrator is `http://…` can
never be authorized, however the policy file is written.**

The subject is the Common Name of a verified client certificate, and a client
certificate exists only on a mutual-TLS connection. A service quest that arrives
over plain HTTP therefore carries no subject at all, the Authorizer is asked
about `""`, and nothing in `policies.json` names `""` — so every candidate is
refused and the consumer is told it may use none of them.

This is not hypothetical; it is what the first enabled deployment did. The
generated configuration seeds every core system over HTTP, which is correct for
two of the three:

| Core system | Scheme | Why |
|---|---|---|
| `ca` | **http** | Enrollment precedes any certificate. It cannot be otherwise. |
| `serviceregistrar` | **http** | Registration is core-mission and needs no token. |
| `orchestrator` | **https** once authorization is adopted | This is where the subject is established. |

So exactly one line changes per consumer:

```json
{ "coreSystem": "orchestrator",
  "url": "https://192.168.1.10:30103/orchestrator/orchestration" }
```

The address rather than `localhost`: the certificate the CA issues carries the
host's addresses and never mentions `localhost`, so `https://localhost:30103`
fails to verify. Generated configurations now seed the host's own address for
this reason, and the HTTPS port is the HTTP port with its leading `2` replaced
by a `3`.

The failure is quiet from the consumer's side — it looks like a policy refusal,
which sends an operator to the one file the answer is not in — so the
Orchestrator now says so in the refusal it returns rather than only in its own
log.

### The authorizer slot in a generated configuration

`systemconfig.json` is generated with a fourth core system:

```json
{ "coreSystem": "authorizer", "url": "" }
```

An empty URL means **absent**: a cloud that has not adopted authorization
behaves exactly as it did before, and `GetRunningCoreSystemURL` skips the entry.
What the slot buys is discovery — an operator opening the file sees that the
framework has an authorizer and where its URL goes, rather than needing to know
the JSON shape and copy it from another system.

It is deliberately not seeded with a real URL. `AuthorizeRequest` does not check
whether an authorizer is *reachable*, only whether one is *configured*, so a
seeded URL would make every newly generated system answer 503 to everything but
its core services until an authorizer existed — breaking every cloud that has
not adopted authorization, which is the opposite of adoption per deployment.


**One functional location per system.** The subject is a certificate CN, which
identifies a *system*; attributes such as `FunctionalLocation` are declared on
*unit assets*. A system whose assets sit in different locations therefore has no
well-defined subject attribute. This is not hypothetical: `telegrapher` ships
`Bathroom/temperature` and `Kitchen/temperature` in one system under one CN, and
`emulator` likewise has two assets.

As a provider this is harmless — each service record carries its own asset's
details. As a *consumer* it is not: the Authorizer cannot tell which asset is
asking, and the kitchen-must-not-control-the-bathroom rule silently degrades to
system-level. The worked examples above already assume this constraint by naming
subjects `thermostat-bathroom` and `thermostat-kitchen`.

The alternative — letting the consumer declare which asset it acts for — makes
the attribute self-asserted, and a self-asserted location is not a security
control. Left as a deployment rule until there is a reason to do otherwise.

## Token format (issued by the authorizer)

When a request is authorized, the authorizer returns a signed token the consumer
attaches to its provider request. A payload in the style of a JSON Web Token
(JWT):

```json
{
  "sub":      "thermostat-bathroom",         // CN of the requester's cert
  "provider": "ethermostat-bathroom",        // target system
  "asset":    "bathroom-heater",
  "service":  "plug-state",
  "action":   "write",
  "iat":      "2026-04-30T14:23:00Z",
  "exp":      "2026-04-30T14:33:00Z",
  "iss":      "authorizer",
  "sig":      "<authorizer's signature>"
}
```

The provider verifies the signature locally using the authorizer's public key
(distributed at startup, same trust chain as the CA), checks expiry, and confirms
that the token's claimed `provider`/`asset`/`service`/`action` match the request
being made. No network round-trip to the authorizer is required at request time.

## Revocation

Revocation latency is bounded by token TTL. The authorizer will not issue a new
token for a deauthorized request the moment `policies.json` is edited; existing
tokens remain valid until they expire (default 5 minutes; tunable per policy).

For revocation-sensitive deployments, set short TTLs (1–5 min). For low-frequency
control loops where renewal cost matters, longer TTLs are acceptable. Trade-off
explicit in the operator's hands.

## Composition with the CA whitelist

The authorizer is the *second* gate in a two-gate chain:

1. **Authentication (`systems/ca`):** the binary's hash is on `whitelist.json` →
   issue mTLS certificate. The system *exists* in the cloud.
2. **Authorization (this system):** the system's CN matches a policy →
   issue tokens for specific (provider, asset, service, action). The system
   *acts* in the cloud.

A binary that is whitelisted but not policy-authorized has cryptographic identity
but no permissions. A binary that has a token but whose certificate is revoked
fails at the mTLS handshake before any policy check runs. Both files,
operator-edited, version-controlled, fail-closed.

## Versioning

| Date | Change |
|------|--------|
| 2026-04-30 | Initial schema: subject, missions, actions, must_match_attribute, ttl, denials |
| 2026-07-28 | Added optional `services` selector; specified subject identity as the verified certificate CN; separated filtering from enforcement; recorded token delivery via the existing `ServicePoint_v1.Token` field and the accidental renewal path; added deployment constraints; flagged `invoke` as ahead of the framework |
| 2026-07-28 | Settled three points the engine forced: `must_match_attribute` names the exact registration detail key (`FunctionalLocation`); the shortest TTL among matching rules applies; a request with no verified subject is refused before any rule, wildcard included |
