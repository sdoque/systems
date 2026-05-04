# mbaigo System: maitreD (Maître d'hôtel)

## Purpose

The *Maître d'hôtel* system is a security sentinel that runs **once per host**. Its role is to vouch for the systems running on that host before the Certificate Authority (CA) will sign their CSRs. The name comes from the French *maître d'hôtel* — the host's trusted manager.

It has three responsibilities:

1. **Own enrollment** — the maitreD enrolls with the CA over the network using IP-based pre-authorization. The CA only signs its CSR if the request originates from a pre-configured host IP.
2. **Whitelist sync** — after enrollment, the maitreD pulls the cloud-wide whitelist from the CA's `/ca/certification/whitelist` endpoint and refreshes it every 5 minutes. The fetched list lives in memory and is mirrored to `whitelist.cache.json` so the maitreD survives a CA outage. **The whitelist is no longer hand-edited per host** — the CA owns it (see [ca/README.md](../ca/README.md)).
3. **Software attestation** — once a whitelist is loaded, the maitreD answers attestation requests from the CA. When any other system on the same host requests a certificate, the CA asks the maitreD to verify the SHA-256 hash of that system's running executable against the in-memory list. Until the first successful load, the maitreD returns `503 Service Unavailable` to every attestation request — fail-closed.

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

## Configuration (`systemconfig.json`)

On first run the maitreD generates a `systemconfig.json` and exits so you can review it.

```json
{
  "systemname": "maitreD",
  "unit_assets": [
    {
      "name": "maitreD",
      "details": {
        "Role": ["host-attestation"]
      }
    }
  ],
  "protocolsNports": {
    "http":  20101,
    "https": 20101,
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

| Failure mode | Behaviour |
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
use ./security/maitreD
```
