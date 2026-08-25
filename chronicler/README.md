# mbaigo System: chronicler

**Status: not built.** This is a design note, written 25 August 2026 so the
reasoning is not lost. There is no code here and the system is deliberately
absent from the Makefile's `SYSTEMS` list — adding it before it exists would
only break `make`.

The chronicler is a **Kafka gateway**: it carries what happens inside a local
cloud out to a plant's own event backbone.

---

## Why it exists, and what it is not

An Arrowhead local cloud is closed by construction. Every consumer is named by a
certificate, judged by policy, and handed a token per action. That is what makes
it trustworthy and it is also what makes it hard to get anything out of — a
problem this repository has now met twice.

[`envoy`](../envoy/README.md) was the first answer: a person cannot hold a
certificate here, so a whitelisted binary reads on their behalf and writes to
disk. The chronicler is the same shape one level up. A **plant** cannot hold a
certificate either. Its historians, its analytics, its dashboards are not
Arrowhead systems and never will be, and they already speak Kafka.

So: envoy carries the cloud's *description* out to a person; the chronicler
carries the cloud's *events* out to an enterprise. Both are delegated,
whitelisted, named in `policies.json`, and deliberately one-directional.

It sits in the same family as [`telegrapher`](../telegrapher/README.md) (MQTT),
[`uaclient`](../uaclient/README.md) (OPC UA), [`modboss`](../modboss/README.md)
(Modbus) and [`beekeeper`](../beekeeper/README.md) (ZigBee) — a unit asset
fronting something that is not Arrowhead. The difference is direction. Those
four reach *down* to equipment. This one reaches *up* to the enterprise.

## Not for the cottage

Kafka is a JVM with KRaft, and on the cottage Pi it would be comfortably the
largest thing on a box already running fourteen systems, GraphDB and InfluxDB in
8 GB. The gap it would fill there — *"I want to see what happened"* — is far
better served by the historian that is already running.

This is for a site that **already runs Kafka**, which a paper mill does. Build
it against a real broker, or not at all: a gateway designed against a
hypothetical peer is a gateway that meets the real one badly.

---

## Three decisions already taken

### Authorization ends at this boundary, and the system says so out loud

Everything the chronicler publishes leaves the reach of `policies.json`. On the
far side there is no subject, no mission, no per-action token — only whatever
ACLs the broker has, which are a different model answering a different question.

This is obvious, and being obvious is exactly why it gets forgotten. So it is
not only written here: **the chronicler announces it at startup**, in the manner
of the framework's own security-posture lines —

    chronicler: boundary — events published to kafka://… leave this cloud's
    authorization entirely; anything that can read the topic can read them,
    under the broker's rules and not this cloud's

Whoever deploys it should have to read that sentence, and should see it again
every time the system starts. A boundary nobody is reminded of is a boundary
somebody will forget.

### The cloud issues `urn:changes`; Kafka carries a copy

`kgrapher` maintains the cloud's change thread in the triple store, and that
remains authoritative. The chronicler is **downstream** of it and of the
registry — it publishes a projection.

The reverse was considered and rejected. Two logs that both claim to record what
happened will eventually disagree, and when they do, the one inside the cloud —
built from what the registry and the systems actually said, and readable without
a broker — is the one to believe. Nothing arriving on a Kafka topic should ever
write the graph. If something outside needs to act on the cloud, it comes in
through the ordinary Arrowhead path, discovered and authorized like anything
else.

### It is a chronicler, not a philosopher

The name was going to be `philosopher`. It is a good name and it belongs to a
different system.

A chronicler records what happened and carries it faithfully. A philosopher
would *reason* over that record — "this loop has been in deviation for six
hours", "this signal has not changed value since Tuesday". The second of those
would have caught, in seconds, a flat line that cost most of 24 August.

That system is worth building, it is closer to [`assessor`](../assessor/README.md)
than to this one — the assessor derives failure modes from the graph's *shape*,
a philosopher would derive them from the cloud's *behaviour* — and it needs a
history, not a broker. The two should not be conflated because they happen to
touch the same data.

---

## What it must provide

> *"otherwise they are parasites"* — [assessor](../assessor/README.md)

A gateway that only publishes outward consumes from the cloud and offers it
nothing. The collector answers this with `mquery`. The chronicler's answer
should be a service reporting **what it publishes and how far behind it is**:
topics, the event kinds on each, the last offset acknowledged, and the lag.

That is not a courtesy. A cloud whose events are being shipped somewhere ought
to be able to ask whether they are arriving, and a silent gateway is
indistinguishable from a working one until somebody downstream complains.

## The vocabulary already exists

Three forms describe what happens in a cloud, and none of them needed inventing:

| Form | Carries |
|---|---|
| `forms.RegistryEvent_v1` | a service registered or deregistered, with the record |
| `forms.SystemMessage_v1` | what `usecases.Log` emits — level, body, system |
| `forms.ServiceRecord_v1` | the registration a change concerns |

`kgrapher` already follows registry events over SSE, so the subscription path is
proven. `SystemMessage_v1` is what the retired **messenger** received, and the
chronicler is in part its successor: the operator's need it served has been
split between the [`painter`](../painter/README.md), which shows the cloud now,
and this, which keeps what happened.

Note what those forms do *not* carry: `SystemMessage_v1` has a system name and a
free-text body. Turning terminal prose into events something downstream can
reason about is a real piece of design, not a serialization detail, and it is
the part most likely to be underestimated.

---

## Open questions, for whoever builds it

**What is an event?** Registry changes and log messages are the obvious two.
Actuations — a plug switched, a setpoint moved — are the interesting ones, and
they are currently visible only as a change in a measurement.

**Provenance in the message.** Downstream there is no registry to ask. Whatever
identifies the origin — system, asset, service, functional location — must
travel in the payload. The collector's `source` tag discipline is the precedent.

**Delivery.** At-least-once is the honest default and means duplicates
downstream. Deduplication needs a key, which is another argument for carrying
provenance and a monotonic identifier.

**Schema.** The forms are JSON and self-describing by version, which fits Kafka
tolerably. A plant may expect Avro and a schema registry. That is a negotiation
with the site, not a decision to take here.

**Security to the broker.** mTLS or SASL, and the credential lives on the Pi.
Scope it to *produce* on named topics and nothing else, for the same reason
envoy is read-only: a delegated tool that could also consume or administer is a
way into the plant's data platform from a cottage cupboard.

**Whether it ever consumes.** Northbound only is the safe start and probably the
right end. Southbound means something outside the cloud commanding something
inside it, which is precisely what the authorizer exists to mediate — and a
topic is not a subject.

**Where it runs.** Almost certainly not on the same host as the control loops.
Nothing about the design requires it to be, and a broker client with a disk
queue is not a neighbour a thermostat wants.
