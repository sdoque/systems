# mbaigo System: maitreD (Maître d'hôtel)

## Purpose

The *Maître d'hôtel* system is a security sentinel that runs **once per host**. Its role is to vouch for the systems running on that host before the Certificate Authority (CA) will sign their CSRs. The name comes from the French *maître d'hôtel* — the host's trusted manager.

It has four responsibilities — three of them security, and one that is there
because of where it runs rather than what it is for:

1. **Own enrollment** — the maitreD enrolls with the CA over the network using IP-based pre-authorization. The CA only signs its CSR if the request originates from a pre-configured host IP.
2. **Whitelist sync** — after enrollment, the maitreD pulls the cloud-wide whitelist from the CA's `/ca/certification/whitelist` endpoint and refreshes it every 5 minutes. The fetched list lives in memory and is mirrored to `whitelist.cache.json` so the maitreD survives a CA outage. **The whitelist is no longer hand-edited per host** — the CA owns it (see [ca/README.md](../ca/README.md)).
3. **Software attestation** — once a whitelist is loaded, the maitreD answers attestation requests from the CA. When any other system on the same host requests a certificate, the CA asks the maitreD to verify the SHA-256 hash of that system's running executable against the in-memory list. Until the first successful load, the maitreD returns `503 Service Unavailable` to every attestation request — fail-closed.
4. **Host load reporting** — the maitreD samples the machine it runs on and offers a `loadstatus` service, so that something deciding where work should run can ask. See *[Reporting the host's load](#reporting-the-hosts-load)* below.

## Startup order

```
CA  →  maitreD  →  all other systems on the host
```

The maitreD must be running and enrolled before it can vouch for other systems. It retries its own certificate request every minute until the CA is reachable.

## Sequence diagrams

### maitreD own enrollment

```mermaid
sequenceDiagram
    participant MD as maitreD
    participant CA as Certificate Authority

    Note over MD: Startup — generate key pair + CSR
    MD->>CA: POST /ca/certification/certify<br/>Body: CSR PEM (CommonName="maitreD")<br/>Header: X-Process-PID: &lt;pid&gt;
    CA->>CA: Check source IP against maitreDHosts
    alt IP authorized
        CA-->>MD: 200 OK — signed certificate PEM
        MD->>CA: GET /ca/certification
        CA-->>MD: CA certificate PEM
        Note over MD: Save cert + key to disk<br/>mTLS active on all outbound calls
    else IP not authorized
        CA-->>MD: 403 Forbidden
        Note over MD: Retry in 1 minute
    end
```

### Attesting another system's executable

```mermaid
sequenceDiagram
    participant S as System (any)
    participant CA as Certificate Authority
    participant MD as maitreD

    S->>CA: POST /ca/certification/certify<br/>Body: CSR PEM<br/>Header: X-Process-PID: &lt;pid&gt;
    CA->>MD: POST /maitreD/maitreD/attest<br/>Body: {"pid": &lt;pid&gt;}
    MD->>MD: readlink /proc/&lt;pid&gt;/exe → executable path
    MD->>MD: SHA-256 hash of executable file
    MD->>MD: Check hash against whitelist
    alt hash approved
        MD-->>CA: 200 OK
        CA->>CA: Sign CSR
        CA-->>S: 200 OK — signed certificate PEM
    else hash not in whitelist
        MD-->>CA: 403 Forbidden
        CA-->>S: 403 Forbidden — attestation failed
    end
```

## Reporting the host's load

```
GET /maitreD/<asset>/loadstatus                          → HostLoad_v1
GET /maitreD/<asset>/loadstatus  (text/event-stream)     → the same, whenever it moves
```

### Why this system and not another

The maitreD is the only system in the framework with a **per-host** identity.
Exactly one runs on each machine, the CA reaches it by source address rather
than through the registry, and it already reads `/proc/<pid>/exe` to do its
real job. A separate monitoring system would have to solve "exactly one of
these per host, enrolled and whitelisted" all over again and gain nothing by it.

The cost is worth naming rather than glossing over: this puts an operational
reading beside the attestation duty that the whole trust chain rests on.
Reading a few files under `/proc` and serving a struct is a small addition to
that surface — but it is an addition, and if the two ever pull in different
directions, attestation wins.

### What it reports

A [`HostLoad_v1`](../../mbaigo/forms/host_forms.go). Two kinds of field,
deliberately:

**`headroom`**, from 0 (saturated) to 1 (idle), is the single comparable
number. Only the host can compute it, because only the host knows its core
count and its thermal ceiling — a load average of 4.0 saturates a four-core Pi
and idles a sixteen-core server, so a bare load figure cannot be compared
across a fleet.

**The raw figures** — `load1/5/15`, `memAvailableMB`, `cpuTempC`,
`throttledNow` — are what let a reader disagree with the summary. Something
applying its own policy needs to see whether a load is a spike or a trend, and
`headroom` alone cannot say.

Three decisions inside that are easy to get wrong:

- **Headroom is the worst constraint, not the average.** A machine with idle
  CPUs and no memory is not half-available; it is unavailable. On `canbus` this
  shows immediately — headroom 0.65 with the CPU at zero, because memory is
  what binds.
- **Throttling is unavailability, not busyness.** A Raspberry Pi under
  sustained load derates rather than queues, so it can read as quiet while
  delivering half its clock. A reader that ignores this moves work *onto* a
  degraded machine. `throttledNow` and `throttledSince` answer different
  questions: avoid this host today, and fix this host's cooling.
- **Pressure-stall figures are optional and are pointers.** `/proc/pressure`
  measures how much work was actually *delayed*, which is what "too loaded"
  means, and it is a better signal than load average. Stock Raspberry Pi OS is
  built without `CONFIG_PSI`, so the fields are absent — and absent is not
  zero. A stall of `0.0` means nothing was delayed; a missing one means this
  kernel does not measure. Add `psi=1` to `/boot/firmware/cmdline.txt` and
  reboot to enable them.

### Monitoring without loading the host

Both halves of that claim have to be true.

**Sampling** is a handful of virtual-file reads — microseconds, no disk, no
syscall storm.

**Serving** is where the cost would actually appear, and it is why the service
is *subscribable*. Something following ten hosts and polling each once a second
is 864,000 requests a day for a number that changes slowly. Following instead,
with a threshold, means hearing when load moves — and the heartbeat doubles as
liveness, so a host that goes quiet is a host that is gone. The reading is taken
on a timer (`loadPeriod`, 15 s by default) and served from cache, so the cost is
one read per period whatever the audience.

### A host that cannot measure itself says so

If `/proc/loadavg` cannot be read — a developer's Mac, a container with `/proc`
masked — the service answers **503** rather than serving a reading of zeros.

This was found by running it rather than by reasoning about it. Every figure
came back zero, `headroom` computed 1.0, and the machine reported itself
*completely idle*. That is the most dangerous answer available: anything
balancing work would send it to the one host that cannot say how loaded it is.

### It is behind the authorizer, and `attest` is not

`loadstatus` declares `Mission: MissionMeasurement`, and that single field is
the whole mechanism.

A core-mission service is served **without a token**, because the plane that
makes tokens possible cannot itself require one — attestation squarely belongs
there, since the CA calls it before any certificate exists. Reporting how busy a
machine is does not belong there. It is a measurement, and it is also
reconnaissance: which host is loaded, when, and how the cloud's work is
distributed is useful to somebody choosing a moment.

`EffectiveMission` takes a service's own mission over its asset's, so this one
line puts `loadstatus` behind the authorizer while leaving `attest` exempt. A
policy granting it is one rule:

```json
{ "subject": "balancer", "missions": ["measurement"],
  "services": ["loadstatus"], "actions": ["read"], "ttl": "5m" }
```

See [../authorizer/POLICY.md](../authorizer/POLICY.md), *The bootstrap plane is
exempt*.

### What it deliberately does not report

**No recommendation.** No `shouldMigrate` field: reporting and deciding are
different jobs, and putting the policy here would scatter it across every
maitreD in the cloud instead of keeping it in the one system that decides.

**No per-process list.** Tempting, since that is what `htop` shows — but it is a
far richer disclosure than a load average, and nothing needs it: the knowledge
graph already says which systems run where, and the unit assets say whether they
could run elsewhere. **Load from the host, placement from the graph, mobility
from the asset** — three sources, each saying only what it is the authority on.

---

## Configuration (`systemconfig.json`)

On first run the maitreD generates a `systemconfig.json` and exits so you can review it.

| Key | Meaning |
|---|---|
| `traits[0].loadPeriod` | Seconds between host samples. 15 is plenty — the figures move slowly, and subscribers are told when they move rather than polling |
| `details.Mobility` | `fixed`, always. The maitreD attests the host it runs on, so a moved maitreD would be attesting a different machine — bound by purpose rather than by wiring |

```json
{
  "systemname": "maitreD",
  "unit_assets": [
    {
      "name": "maitreD",
      "mission": "core",
      "details": {
        "Role": ["host-attestation"],
        "Mobility": ["fixed"]
      },
      "traits": [
        { "loadPeriod": 15 }
      ]
    }
  ],
  "protocolsNports": {
    "http":  20101,
    "https": 30101,
    "coap":  0
  },
  "coreSystems": [
    { "coreSystem": "serviceregistrar", "url": "http://192.168.1.1:20102/serviceregistrar/registry" },
    { "coreSystem": "orchestrator",     "url": "http://192.168.1.1:20103/orchestrator/orchestration" },
    { "coreSystem": "ca",               "url": "http://192.168.1.1:20100/ca/certification" },
    { "coreSystem": "maitreD",          "url": "http://192.168.1.10:20101/maitreD/maitreD" }
  ]
}
```

### The whitelist (CA-mastered)

The maitreD does **not** carry a hand-edited whitelist. It pulls the cloud's
approved-executable list from the CA on startup and every 5 minutes
afterwards, caching the last-good copy in `whitelist.cache.json` next to the
binary. Until the first successful load (cache or fetch), every attestation
request returns `503 Service Unavailable`.

To approve a new binary, edit the CA's `whitelist.json`. See
[ca/README.md](../ca/README.md) for the CA-side instructions.

| Failure mode | Behavior |
|---|---|
| First-ever startup, CA reachable | Pull, cache, then start serving |
| First-ever startup, CA unreachable | Log fatal, exit (no cache to fall back on) |
| Subsequent startup, cache present | Use cache immediately, then refresh in background |
| CA unreachable mid-run | Keep using current in-memory list, log a warning per failed sync |

### CA-side prerequisites

Before the maitreD can enroll, two fields must be set in the **CA's** `systemconfig.json`:

| Field | Purpose |
|-------|---------|
| `maitreDHosts` | List of host IPs permitted to enroll a maitreD |
| `maitreDPort` | Port the maitreD listens on (default 20101) |

```json
"maitreDHosts": ["192.168.1.10"],
"maitreDPort": 20101
```

## Building and running

```bash
# Run in place (for development)
go run .

# Build for the current machine
go build -o maitreD_local

# Cross-compile for Raspberry Pi 64-bit
GOOS=linux GOARCH=arm64 go build -o maitreD_rpi64

# Copy to a Raspberry Pi
scp maitreD_rpi64 user@192.168.1.10:mbaigo/maitreD/
```

Run the binary from **inside its own directory** so it can find (or create) `systemconfig.json`.

The `attest` service uses `/proc/<pid>/exe`, which is Linux-specific. The maitreD is designed to run on Linux hosts (e.g. Raspberry Pi). Running it on macOS is supported for development but attestation requests will fail because `/proc` does not exist.

A full list of supported platforms: `go tool dist list`

## Development with a local mbaigo clone

Add a `replace` directive to `go.mod`:

```
require github.com/sdoque/mbaigo v0.x.x
replace github.com/sdoque/mbaigo => ../../mbaigo
```

Or add both modules to the workspace `go.work` at the repository root:

```
use ./mbaigo
use ./systems/maitreD
```
