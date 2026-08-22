# mbaigo System: envoy

**Status: running against the cottage cloud.**

The envoy fetches the cloud's own descriptions on behalf of a person.

```bash
envoy cloudgraph cloudmodel
envoy -dir ~/captures cloudgraph
```

---

## The problem: a cloud with no place for its owner

The subject of every authorization decision is the Common Name of a **verified
client certificate**, and the CA issues one only to a binary whose SHA-256 the
maitreD finds on the whitelist. A browser cannot attest. Neither can `curl`, nor
a person at a laptop.

So the moment a local cloud adopts authorization, its owner is locked out of
reading it:

```
$ curl http://192.168.1.109:20105/kgrapher/assembler/cloudgraph
the caller presented no verified certificate
```

That is the correct answer to the request as asked. It is also an unusable
situation: the one person responsible for the cloud cannot see what it thinks it
is.

## Two tempting answers, both wrong

**Declare the services core-mission.** Core services are exempt from the token
check so the cloud can bootstrap, and marking `cloudgraph` core would open it
immediately. But `core` is a claim the [authorizer](../authorizer/POLICY.md)
reasons about, and the [assessor](../assessor/README.md) reads the same
vocabulary to decide what a failure costs. Lying to the taxonomy to gain access
corrupts it for everything that depends on it.

**Issue a certificate to a person by hand.** The CA would have to skip
attestation for that one CSR — and attestation is what the whole trust chain
rests on. A single manual exception is indistinguishable, afterwards, from a
compromise.

## The third answer

A binary that enrolls the ordinary way. It is whitelisted like any other system,
named in `policies.json` like any other subject, and can be refused like any
other. **Access is delegated rather than granted**, which is what an envoy is —
and it means the operator's reach is written down in the same file as everything
else, where it can be audited and revoked.

The operator reads what the envoy wrote to disk. Nobody is let into the cloud.

---

## What it does

For each service definition named on the command line it discovers **every**
provider through the orchestrator, reads each one with the access token the
orchestrator issues, and writes the body to its own file.

**Bodies are never parsed.** A Turtle graph and a SysML v2 document are not
forms the framework knows how to unpack, and a capture tool that could only save
what it could also interpret would be useless for exactly the documents worth
saving. What the provider sent is what lands on disk.

Files are named `system-asset-service-<timestamp>.<ext>`, so a directory of
captures can be read without opening any of them. The extension prefers the
`Format` the provider registered — a person wrote "Turtle" there — over the
Content-Type, which is often just `text/plain`.

| Flag | Meaning |
|---|---|
| `-dir` | where to write (default `.`) |
| `-wait` | how long to wait for enrollment (default 90s) |
| `-timestamp=false` | overwrite rather than accumulate |

Nothing captured exits non-zero: a script that treats an empty capture as a
success archives nothing and says nothing.

---

## It must run on the host it enrolls from

Attestation works by the CA asking the maitreD **on the requester's own host**
to verify the process behind the request. So the envoy runs on the Pi, and the
files are collected over `scp`. It cannot be run from a laptop, by design — that
is the same property that stops anyone else running it from theirs.

## Deliberately not registered

Every other system here registers what it provides. This one provides nothing:
it is a person's hand reaching in, it runs for a few seconds, and a service
registered by a process about to exit is a stale record somebody will try to
consume.

That is safe only because **no rule naming this subject may use
`must_match_attribute`**. The authorizer resolves a subject's attributes from
its registry entry; with no entry there are no attributes, and such a rule would
refuse everything. The envoy's rules are unpaired for exactly this reason.

## Read-only, and that is the point

Its policy grants `read` on the observable missions and nothing else. A
delegated tool that could also write would be a way to drive the heating from a
laptop that holds no certificate — the very thing authorization exists to
prevent. `TestCottageDecisions` asserts it: the envoy may read a temperature and
may not switch a plug.

To widen what it may read, add a mission to its rule in `policies.json`. To
revoke it entirely, delete the rule — or take its hash off the whitelist, and it
cannot even enroll.

---

## Running the tests

```bash
go test ./...
```

| Test | What it checks |
|---|---|
| `TestACaptureIsNamedAfterWhereItCameFrom` | System, asset and service in the filename |
| `TestATimestampIsAddedWhenAsked` | And that two captures do not overwrite each other |
| `TestTheRegisteredFormatBeatsTheContentType` | The considered answer wins |
| `TestAProviderCannotChooseWhereThisWrites` | A hostile service definition cannot escape the capture directory |
| `TestAnUnparseableURLStillProducesAName` | An odd URL does not drop a capture |
| `TestTheTemplateDeclaresAMission` | A subject the authorizer can classify |
