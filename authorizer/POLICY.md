# Authorizer Policy Schema

**Status:** Working specification. Pre-implementation.

This document defines the policy file format read by the authorizer service, the
evaluation semantics, and the wire shape of the tokens the authorizer issues.
It is the contract every other piece of code in the security/authorizer system
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
by construction, mirroring the security/ca's whitelist semantics.

## Policy entries

Each entry in the `policies` array is an allow rule:

```json
{
  "subject":              "thermostat",
  "missions":             ["actuation", "measurement"],
  "actions":              ["read", "write"],
  "must_match_attribute": "functional_location",
  "ttl":                  "10m"
}
```

| Field | Type | Required | Meaning |
|-------|------|----------|---------|
| `subject` | string | yes | The CN of the consumer's mTLS certificate. `"*"` matches any authenticated subject. |
| `missions` | string[] | yes | Mission names from MISSIONS.md the policy authorises. `["*"]` matches any mission. |
| `actions` | string[] | yes | One or more of `read`, `write`, `invoke`, or `*`. |
| `must_match_attribute` | string | no | If set, an additional ABAC constraint: the named attribute must match between subject and asset (see *Pairing semantics* below). |
| `ttl` | duration string | no | Token lifetime if this policy authorises the request. Defaults to `5m`. |

A request is authorised iff at least one policy entry matches AND no `denials`
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

## Pairing semantics (`must_match_attribute`)

A policy may declare `must_match_attribute` to require that the named attribute
match between the subject and the asset. The match algorithm:

1. Look up the named attribute on the subject (from its registration record).
2. Look up the named attribute on the asset (from the service-registrar entry).
3. If the asset has no value for the attribute → **constraint satisfied**
   (asset is "unpaired" and universally accessible to subjects of the right mission).
4. Else if the asset has a value AND the subject has no value → **constraint violated**.
5. Else: at least one of the subject's values must equal at least one of the
   asset's values (multi-valued match by intersection non-empty).

Rationale for step 3: in OT plants, many sensors and actuators are not associated
with a specific location/zone — they're cloud-wide utilities (audit logs,
aggregations, framework infrastructure). Forcing every consumer to declare a
match key for these would be over-engineering.

Rationale for step 4: a subject *with* a defined location/zone consuming an
asset *with* a defined location/zone is the security-relevant case; a missing
subject side is an operator misconfiguration that should fail closed.

## Worked examples

The eThermostat scenario, expressed in policy form. Setup:

- Asset `bathroom-sensor` has mission `measurement`, attribute `functional_location: ["Bathroom"]`.
- Asset `bathroom-heater` has mission `actuation`, attribute `functional_location: ["Bathroom"]`.
- Asset `cloud-aggregator` has mission `aggregation`, no `functional_location`.
- Subject `thermostat-bathroom` has attribute `functional_location: ["Bathroom"]`.
- Subject `thermostat-kitchen` has attribute `functional_location: ["Kitchen"]`.
- Subject `collector` has no `functional_location`.

Policies:

```json
{
  "policies": [
    {
      "subject": "thermostat-*",
      "missions": ["measurement", "actuation"],
      "actions": ["read", "write"],
      "must_match_attribute": "functional_location"
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

## Token format (issued by the authorizer)

When a request is authorised, the authorizer returns a signed token the consumer
attaches to its provider request. JWT-style payload:

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
token for a deauthorised request the moment `policies.json` is edited; existing
tokens remain valid until they expire (default 5 minutes; tunable per policy).

For revocation-sensitive deployments, set short TTLs (1–5 min). For low-frequency
control loops where renewal cost matters, longer TTLs are acceptable. Trade-off
explicit in the operator's hands.

## Composition with the security/ca whitelist

The authorizer is the *second* gate in a two-gate chain:

1. **Authentication (security/ca):** the binary's hash is on `whitelist.json` →
   issue mTLS certificate. The system *exists* in the cloud.
2. **Authorization (security/authorizer):** the system's CN matches a policy →
   issue tokens for specific (provider, asset, service, action). The system
   *acts* in the cloud.

A binary that is whitelisted but not policy-authorised has cryptographic identity
but no permissions. A binary that has a token but whose certificate is revoked
fails at the mTLS handshake before any policy check runs. Both files,
operator-edited, version-controlled, fail-closed.

## Versioning

| Date | Change |
|------|--------|
| 2026-04-30 | Initial schema: subject, missions, actions, must_match_attribute, ttl, denials |
