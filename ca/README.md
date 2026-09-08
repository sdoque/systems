# mbaigo System: Certificate Authority (ca)

## Purpose

The Certificate Authority (CA) is the trust anchor for a local cloud of mbaigo systems. It:

- Generates its own self-signed X.509 certificate on first run (stored in `ca_certificate.pem` and `ca_private_key.pem`)
- Signs certificate signing requests (CSRs) from other systems so they can use mutual TLS (mTLS)
- Exposes the CA certificate at `GET /ca/certification` so systems can build their trust store
- Enforces IP-based pre-authorization for maitreD enrollment
- Delegates executable verification to the maitreD before signing any other system's CSR
- **Owns the cloud's approved-binary whitelist** at `whitelist.json`, and is the only thing that reads it

Because the CA certificate is the root of trust for the entire local cloud, `ca_certificate.pem` and `ca_private_key.pem` must be kept secure and backed up. The same applies to `whitelist.json`: anyone who can edit it can authorize a binary to run anywhere in the cloud.

## Whitelist file (`whitelist.json`)

A flat JSON array of hex-encoded SHA-256 hashes of approved executables, kept next to `ca_certificate.pem`:

```json
[
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "abc123..."
]
```

A missing file is a deliberate "no binaries approved yet" — an empty list matches no hash, so every certificate request is refused until the file appears.

**An edit takes effect on the next certificate request.** The file is read on each attestation, not held in memory and not distributed, so there is nothing to sync and nobody to wait for. The file's modification time is recorded as the `version` in the CA's log line, so a review can say which policy was applied.

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
        CA->>MD: POST /maitreD/maitreD/attest<br/>Body: {"pid": &lt;pid&gt;, "nonce": &lt;challenge&gt;}
        MD->>MD: readlink /proc/&lt;pid&gt;/exe
        MD->>MD: SHA-256 hash of executable
        MD->>MD: Sign (pid, hash, nonce, time)<br/>with its own enrolled key
        MD-->>CA: 200 OK — signed statement + certificate
        CA->>CA: Check the certificate was issued<br/>by this CA to a "maitreD"
        CA->>CA: Check the signature, the pid<br/>and the nonce
        CA->>CA: Look the hash up in whitelist.json
        alt not trusted, or hash not approved
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
      "mission": "core",
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
    "https": 30100,
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

Set `"https"` to a non-zero port. The convention across the systems is the
HTTP port with its leading 2 replaced by a 3:

```json
"protocolsNports": { "http": 20100, "https": 30100, "coap": 0 }
```

All systems that also have a non-zero https port will use mTLS for their outbound calls.

### Authorizing maitreD hosts

`maitreDHosts` is the list of IPs from which a maitreD system is permitted to enroll. Any CSR with `CommonName = "maitreD"` from an unlisted IP is rejected with `403 Forbidden`.

```json
"maitreDHosts": ["192.168.1.10", "192.168.1.11"]
```

### Enabling PID-based attestation

Set `maitreDPort` to the port the maitreD listens on (default 20101). When non-zero, every non-maitreD CSR triggers an attestation call to the maitreD on the requester's host before the CSR is signed. Set to `0` to disable attestation (development mode — all CSRs are signed without verification).

**Why the answer is signed.** The CA reaches that port over plain HTTP, at an address the requesting system controls, and the maitreD's `attest` service is exempt from authorization because the bootstrap plane cannot require the tokens it exists to create. So anything could listen there and answer. The statement is therefore signed with the key behind the maitreD's own certificate, and the CA checks that the certificate is one it issued to a `maitreD`, that the signature covers this pid and this challenge, and only then looks the hash up. A bare `200 OK` — which is what the maitreD used to send on approval — is now refused.

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
use ./systems/ca
```
