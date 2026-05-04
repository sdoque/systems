# mbaigo System: Certificate Authority (ca)

## Purpose

The Certificate Authority (CA) is the trust anchor for a local cloud of mbaigo systems. It:

- Generates its own self-signed X.509 certificate on first run (stored in `ca_certificate.pem` and `ca_private_key.pem`)
- Signs certificate signing requests (CSRs) from other systems so they can use mutual TLS (mTLS)
- Exposes the CA certificate at `GET /ca/certification` so systems can build their trust store
- Enforces IP-based pre-authorization for maitreD enrollment
- Delegates executable verification to the maitreD before signing any other system's CSR
- **Owns the cloud's approved-binary whitelist** at `whitelist.json` and serves it to maitreDs on demand

Because the CA certificate is the root of trust for the entire local cloud, `ca_certificate.pem` and `ca_private_key.pem` must be kept secure and backed up. The same applies to `whitelist.json`: anyone who can edit it can authorise a binary to run anywhere in the cloud.

## Whitelist file (`whitelist.json`)

A flat JSON array of hex-encoded SHA-256 hashes of approved executables, kept next to `ca_certificate.pem`:

```json
[
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "abc123..."
]
```

A missing file is a deliberate "no binaries approved yet" — the CA serves an empty list and every maitreD denies every attestation request until the file appears. The on-disk file's modification time becomes the wire-format `version`; bumping the file (any edit, or `touch`) signals every maitreD to refresh on its next sync (5 min by default).

To approve a new binary:
1. Compute its hash: `shasum -a 256 path/to/binary | cut -d' ' -f1`
2. Add the line to `whitelist.json`
3. Within 5 minutes every maitreD will pick it up.

## Certificate issuance flow

### maitreD enrollment (IP-based authorization)

```mermaid
sequenceDiagram
    participant MD as maitreD
    participant CA as Certificate Authority

    Note over MD: Startup — generate key pair + CSR
    MD->>CA: POST /ca/certification/certify<br/>Body: CSR PEM (CommonName="maitreD")<br/>Header: X-Process-PID: &lt;pid&gt;
    CA->>CA: Extract client IP from remote address
    CA->>CA: Check IP against maitreDHosts list
    alt IP is authorized
        CA->>CA: Sign CSR with CA private key
        CA-->>MD: 200 OK — signed certificate PEM
        MD->>CA: GET /ca/certification (fetch CA cert)
        CA-->>MD: CA certificate PEM
        Note over MD: Save cert + key to disk<br/>Install mTLS on http.DefaultClient
    else IP not in maitreDHosts
        CA-->>MD: 403 Forbidden
    end
```

### General system enrollment (PID-based attestation)

```mermaid
sequenceDiagram
    participant S as System (any)
    participant CA as Certificate Authority
    participant MD as maitreD

    Note over S: Startup — generate key pair + CSR
    S->>CA: POST /ca/certification/certify<br/>Body: CSR PEM<br/>Header: X-Process-PID: &lt;pid&gt;
    CA->>CA: Extract client IP and PID
    alt maitreDPort != 0 (attestation enabled)
        CA->>MD: POST /maitreD/maitreD/attest<br/>Body: {"pid": &lt;pid&gt;}
        MD->>MD: readlink /proc/&lt;pid&gt;/exe
        MD->>MD: SHA-256 hash of executable
        MD->>MD: Check hash against whitelist
        alt hash is approved
            MD-->>CA: 200 OK
        else hash not in whitelist
            MD-->>CA: 403 Forbidden
            CA-->>S: 403 Forbidden — attestation failed
        end
    end
    CA->>CA: Sign CSR with CA private key
    CA-->>S: 200 OK — signed certificate PEM
    S->>CA: GET /ca/certification (fetch CA cert)
    CA-->>S: CA certificate PEM
    Note over S: Save cert + key to disk<br/>Install mTLS on http.DefaultClient
```

On subsequent startups, a system reuses its saved certificate if it has not expired (with a 24-hour renewal buffer), skipping the CA entirely.

## Starting order

The CA must start **before** any other system. Systems that request a certificate retry every minute until the CA is reachable, so order matters but strict timing does not.

## Configuration (`systemconfig.json`)

On first run the CA generates a `systemconfig.json` and then exits so you can review it. The key fields are:

```json
{
  "systemname": "ca",
  "unit_assets": [
    {
      "name": "certification",
      "details": {
        "Location": ["LocalCloud"],
        "PKI": ["X.509"]
      },
      "safeSWare": false,
      "maitreDHosts": ["192.168.1.10", "192.168.1.11"],
      "maitreDPort": 20101
    }
  ],
  "protocolsNports": {
    "http":  20100,
    "https": 0,
    "coap":  0
  },
  "coreSystems": [
    { "coreSystem": "serviceregistrar", "url": "http://localhost:20102/serviceregistrar/registry" },
    { "coreSystem": "orchestrator",     "url": "http://localhost:20103/orchestrator/orchestration" },
    { "coreSystem": "ca",               "url": "http://localhost:20100/ca/certification" },
    { "coreSystem": "maitreD",          "url": "http://localhost:20101/maitreD/maitreD" }
  ]
}
```

### Enabling mTLS

Set `"https"` to a non-zero port (it can be the same value as `"http"`):

```json
"protocolsNports": { "http": 20100, "https": 20100, "coap": 0 }
```

All systems that also have a non-zero https port will use mTLS for their outbound calls.

### Authorizing maitreD hosts

`maitreDHosts` is the list of IPs from which a maitreD system is permitted to enroll. Any CSR with `CommonName = "maitreD"` from an unlisted IP is rejected with `403 Forbidden`.

```json
"maitreDHosts": ["192.168.1.10", "192.168.1.11"]
```

### Enabling PID-based attestation

Set `maitreDPort` to the port the maitreD listens on (default 20101). When non-zero, every non-maitreD CSR triggers an attestation call to the maitreD on the requester's host before the CSR is signed. Set to `0` to disable attestation (development mode — all CSRs are signed without verification).

```json
"maitreDPort": 20101
```

## Building and running

```bash
# Run in place (for development)
go run .

# Build for the current machine
go build -o ca_local

# Cross-compile for Raspberry Pi 64-bit
GOOS=linux GOARCH=arm64 go build -o ca_rpi64

# Copy to a Raspberry Pi
scp ca_rpi64 user@192.168.1.6:mbaigo/ca/
```

Run the binary from **inside its own directory** so it can find (or create) `systemconfig.json`, `ca_certificate.pem`, and `ca_private_key.pem`.

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
use ./security/ca
```
