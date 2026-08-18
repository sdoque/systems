# Deploying a protected local cloud

A local cloud runs at whatever level of protection you have deployed. Every level
below the last one is a legitimate deployment: a cloud with no certificate
authority is how everybody starts, and a framework that refused to run without
one would teach nothing.

So the framework never withholds function to enforce security. It states what it
is doing instead, and leaves the choice to you. What it will not let you do is
believe you are protected when you are not.

## The three systems that raise the level

| System | What it adds | Without it |
|---|---|---|
| [`ca`](ca/) | Issues the certificates that give every system a verifiable identity. | Nothing is identified. All traffic is in the clear. |
| [`maitreD`](maitreD/) | Attests a requesting binary's SHA-256 hash — a fingerprint of its exact contents — against the CA-mastered whitelist before the CA signs for it. | The CA signs for any process that asks, so a certificate proves the host was reachable, not that the executable is the approved one. |
| [`authorizer`](authorizer/) | Decides which system may use which service, and mints the access token that proves it. | Any identified system may call any service on any other. |

Deploy them in that order. Each one is useful without the ones after it; none is
useful without the ones before it.

## What each system reports

Every system prints one line when it starts serving:

```
thermostat: security: identified — callers over TLS are identified; no authorizer
configured: any identified system may use any service; an HTTP port is open, so
this system is also reachable without TLS
```

and publishes the same facts at `http://<host>:<port>/<system>/kgraph`:

```turtle
alc:pihome_thermostat_Security a afo:SecurityPosture ;
    afo:hasSecurityLevel "identified" ;
    afo:namesCertificateAuthority "true"^^xsd:boolean ;
    afo:namesAuthorizer "false"^^xsd:boolean ;
    afo:isIdentified "true"^^xsd:boolean ;
    afo:canVerifyPeers "true"^^xsd:boolean ;
    afo:verifiesTokens "false"^^xsd:boolean ;
    afo:offersTLS "true"^^xsd:boolean ;
    afo:acceptsPlaintext "true"^^xsd:boolean .
```

Collect those with the [`kgrapher`](kgrapher/) and the posture of the whole cloud
can be queried rather than inferred: which systems are enrolled, which are still
reachable in the clear, which name an authorizer they cannot reach.

The levels, and what each property means, are the framework's contract —
see [`mbaigo/usecases/SECURITY.md`](https://github.com/sdoque/mbaigo/blob/main/usecases/SECURITY.md).

## Three deployments

### 1. No CA, no maitreD, no authorizer

**Works, completely.** The HTTP server binds immediately and does not wait for
enrollment. Every system reports `open`.

**Protection: none.** No identity, no attestation, no authorization. Anyone who
can reach the port can call any service. This is the right way to learn the
framework and the wrong way to run anything.

One nuisance worth knowing: if a `ca` entry is left in `coreSystems` pointing at
a CA that is not running, every system retries enrollment once a minute, forever,
and logs each failure. Remove the entry to get a quiet `open` cloud.

### 2. CA and maitreD, no authorizer

**Works.** Systems enroll, receive certificates, bind their HTTPS endpoints, and
present their client certificate on outbound calls. Providers verify it. Systems
report `identified`.

**Protection: authentication, not authorization.** You know who is calling — a
verified common name chaining to your CA, whose binary maitreD attested at
enrollment. You do not restrict what they may do: any enrolled system may call any
service on any other.

Two hops stay in the clear at this level, by construction:

- **Enrollment.** A system with no certificate cannot complete a mutual
  Transport Layer Security (mTLS) handshake, in which both ends present one, so
  its certificate signing request (CSR) goes to the CA over plain HTTP. This is exactly what maitreD is for:
  the hop is not authenticated, so the *executable* is.
- **The core hops.** Registration, orchestration and certification all use the
  `coreSystems` URLs, which are `http://` in the generated configuration. Point
  them at the HTTPS ports to close this. The CA must keep an HTTP port either
  way — see below.

### 3. `http = 0` everywhere

**The cloud never starts, and this is not something you can configure around.**

Enrollment is the reason. A system with no certificate cannot complete the mTLS
handshake the CA's HTTPS listener demands, and with `http = 0` there is no
plaintext port left to send the CSR to. Nothing enrolls, so nothing receives a
certificate, so no HTTPS listener ever binds. Every system retries once a minute
forever, reporting `enrolling`.

maitreD compounds it: the CA reaches it at a hardcoded `http://` URL, so a
maitreD with no HTTP port can never attest anyone and the CA signs nothing.

**What does work** is `http = 0` on everything *except* the CA and maitreD, with
`coreSystems` pointed at the HTTPS ports. Enrollment is then the only plaintext
hop — the ordinary bootstrap compromise — and everything after it is mTLS.

## Turning authorization on

Authorization is opt-in **per system**, through that system's own configuration:
a provider enforces only if its own `coreSystems` list has an `authorizer` entry.
Adding one switches enforcement on for that system alone.

This means a cloud can be authorized in part, which is how you migrate one. A
system that names an authorizer and cannot reach it refuses every request with
503 rather than serving them unauthorized — visible on the graph as
`namesAuthorizer true` with `verifiesTokens false`.

Policies live in the authorizer's `policies.json`; see
[`authorizer/POLICY.md`](authorizer/POLICY.md) for the rules and
[`authorizer/MISSIONS.md`](authorizer/MISSIONS.md) for the mission vocabulary
they are written against.

## Ports

The convention in these systems is that an HTTPS port is its HTTP port with the
leading `2` replaced by a `3` — the authorizer serves `20104` and `30104`. Nothing
enforces this; it is a habit that makes a `netstat` readable.
