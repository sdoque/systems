# mbaigo System: collector

The collector is an Arrowhead-compliant system whose asset is a time-series
database ([InfluxDB](https://en.wikipedia.org/wiki/InfluxDB)). It periodically
discovers every provider of each configured measurement type via the Arrowhead
Service Registry, queries all of them individually, and writes each reading as
a tagged data point into an InfluxDB bucket.

## Services

| Sub-path | Method | Description |
|----------|--------|-------------|
| `mquery` | GET    | Returns the list of measurements currently present in the configured InfluxDB bucket. Answers 503 when the database cannot be reached — see *[Which InfluxDB](#which-influxdb)*, since this is the one call that will not work against InfluxDB 3. |

## How it works

Each measurement entry in the configuration file describes:
- **serviceDefinition** — the Arrowhead service name to look up (e.g. `pressure`)
- **mdetails** — optional filter details passed to the orchestrator
- **samplingPeriod** — polling interval in seconds

On every tick the collector:
1. Calls `Search4MultipleServices` to discover *all* registered providers of that measurement type.
2. Iterates over every discovered node; for each one it performs an HTTP GET to retrieve a `SignalA_v1a` form.
3. Writes one InfluxDB point per provider, tagged with the **source** node name and any metadata (e.g. `Unit`, `Location`) that the provider registered with the orchestrator.
4. If a provider returns an error its node entry is cleared so re-discovery happens on the next tick.

### Sequence diagram

```mermaid
sequenceDiagram
    participant Collector
    participant Orchestrator
    participant Provider as Signal Provider(s)
    participant InfluxDB

    note over Collector: startup — newResource()

    Collector->>Orchestrator: RegisterServices (mquery)
    Orchestrator-->>Collector: 201 Created

    loop every samplingPeriod
        alt Nodes list is empty (first tick or after failure)
            Collector->>Orchestrator: Search4MultipleServices(serviceDefinition)
            Orchestrator-->>Collector: list of NodeInfo {URL, Details}
        end

        loop for each discovered node
            Collector->>Provider: GET <node URL>
            Provider-->>Collector: SignalA_v1a {value, unit, timestamp}

            alt successful response
                Collector->>InfluxDB: WritePoint(measurement, tags={source, Unit, …}, value)
            else HTTP or JSON error
                Collector->>Collector: clear Nodes → re-discover next tick
            end
        end
    end

    note over Collector: SIGINT received
    Collector->>InfluxDB: Flush & Close client
```

### Configuration example (`systemconfig.json` traits section)

```json
{
  "db_url": "http://localhost:8086",
  "token": "<influxdb-token>",
  "organization": "myorg",
  "bucket": "demo",
  "measurements": [
    {
      "serviceDefinition": "pressure",
      "mdetails": {},
      "samplingPeriod": 4
    }
  ]
}
```

## Status

**Running on AlphaCloud** against InfluxDB 2.9.1, ingesting a temperature every
three seconds with the provider's unit, quantity kind and functional location
carried through as tags:

```
collected temperature from canbus_ds18b20_28-00000f030344_temperature  value=23.5000
```



Prototype demonstrating that the mbaigo library can simultaneously collect the
same measurement type from multiple distributed providers and store them as
distinguishable time series in a single InfluxDB bucket.

## Compiling

Fetch the mbaigo module and tidy dependencies:

```bash
go get github.com/sdoque/mbaigo
go mod tidy
```

Run directly:

```bash
go run collector.go thing.go
```

> It is **important** to start the program from within its own directory because
> it looks for `systemconfig.json` there. If the file is missing it is generated
> automatically and the program exits so the file can be edited before the next
> start.

Build for the local machine:

```bash
go build -o Collector
```

## Cross-compiling

| Target | Command |
|--------|---------|
| Intel Mac | `GOOS=darwin GOARCH=amd64 go build -o Collector_imac` |
| ARM Mac | `GOOS=darwin GOARCH=arm64 go build -o Collector_amac` |
| Windows 64 | `GOOS=windows GOARCH=amd64 go build -o Collector.exe` |
| Raspberry Pi 64 | `GOOS=linux GOARCH=arm64 go build -o Collector_rpi64` |
| Linux x86-64 | `GOOS=linux GOARCH=amd64 go build -o Collector_linux` |

Full platform list: `go tool dist list`

Copy to a Raspberry Pi:

```bash
scp Collector_rpi64 jan@192.168.1.10:rpiExec/Collector/
```

## Which InfluxDB

**This system targets InfluxDB 2.x**, and is developed against the 2.9 line.
That is not the newest InfluxDB — 3.x is — and the reason is a single query
rather than a preference.

| Version | Writes | `mquery` | Verdict |
|---|---|---|---|
| 1.x | no | no | No token, organization or bucket. Will not work |
| **2.x** | yes | yes | **What this system targets** |
| 3 Core / Enterprise | **yes** | **no** | Ingests correctly, `mquery` cannot work |

The trap is in that third row and it is worth understanding before pointing this
at a v3 server. The collector writes through the **v2 line-protocol endpoint**,
which 3.x keeps for compatibility — so **the data lands, and everything looks
healthy**. But `mquery` asks which measurements a bucket holds using Flux:

```flux
import "influxdata/influxdb/schema"
schema.measurements(bucket: "demo")
```

**Flux is supported by no version of InfluxDB 3**, and there is no migration
path for it. So on v3 the collector ingests happily and the one service it
offers to the cloud fails — a shape of failure that reads, from the outside,
like a working system.

Licensing is not the obstacle: InfluxDB 3 Core is open source, and Enterprise is
free for at-home use. Two things argue for staying on 2.x anyway. Core bounds
how much history a single query may span — by default about 72 hours' worth of
stored files — which is the wrong shape for a historian. And moving `mquery` to
SQL would produce a collector that only works on 3.x, breaking every 2.x
deployment.

If both are ever needed, the honest split is to keep writes on the
v2-compatible endpoint, which works everywhere, and make only the measurement
query version-aware. It is the single Flux call in the system.

## Deploying InfluxDB 2 (Linux / Raspberry Pi)

The collector uses `influxdb-client-go/v2` with a token, an organization and a
bucket.

### 1. Add InfluxData's repository

```bash
curl --silent --location -O https://repos.influxdata.com/influxdata-archive.key

# Verify the key by its GPG fingerprint. This prints nothing and exits 0 when
# the key is genuine.
gpg --show-keys --with-fingerprint --with-colons ./influxdata-archive.key 2>&1 \
  | grep -q '^fpr:\+24C975CBA61A024EE1B631787C3D57159FC2F927:$'

sudo mkdir -p /etc/apt/keyrings
cat influxdata-archive.key | gpg --dearmor \
  | sudo tee /etc/apt/keyrings/influxdata-archive.gpg > /dev/null

echo 'deb [signed-by=/etc/apt/keyrings/influxdata-archive.gpg] https://repos.influxdata.com/debian stable main' \
  | sudo tee /etc/apt/sources.list.d/influxdata.list
```

> **This step used to be a SHA-256 check against a hard-coded digest, and that
> digest went stale.** InfluxData re-published the key file, `sha256sum --check`
> then failed, and a failing checksum on a signing key is indistinguishable from
> an attack — so the correct response was to stop, which is where this
> walkthrough stranded at least one reader. The fingerprint above is a property
> of the key itself rather than of the file that carries it, so it survives
> re-publication. It was verified against the key as served, on an aarch64
> Raspberry Pi, when this was written.

### 2. Install and start

```bash
sudo apt-get update
sudo apt-get install influxdb2 influxdb2-cli
sudo systemctl enable --now influxdb
systemctl status influxdb
```

**Both packages.** `influxdb2` is the server; `influx`, the command every step
below uses, is in `influxdb2-cli`. Installing only the server leaves you at
step 3 with `influx: command not found` and nothing to explain why — the two
were separated upstream so a machine can hold the client without the database.

`enable --now` starts it and makes it come back after a reboot, which the
earlier `service influxdb start` did not.

Verified on Raspberry Pi OS (Debian 13, aarch64): server 2.9.1, CLI 2.8.0.

### 3. Set it up — the step that is easy to miss

**A freshly installed InfluxDB 2 is running and refuses everything.** There is
no user, no organization, no bucket and no token until you create them, and the
collector will fail with an authorization error that says nothing about setup
being incomplete.

Either from the command line:

```bash
influx setup \
  --username <you> \
  --password '<a real password>' \
  --org mbaigo \
  --bucket demo \
  --retention 0 \
  --force
```

or in a browser at `http://<pi-address>:8086`, which walks through the same
four answers.

`--retention 0` keeps data indefinitely. The organization and bucket names are
yours to choose; they must match `organization` and `bucket` in the collector's
`systemconfig.json`.

### 4. Get a token for the collector

Setup creates an **operator token**, which can do anything to anything. Read it:

```bash
influx auth list
# or, if the CLI is not configured:
sudo cat /etc/influxdb2/influx-configs
```

Use it to confirm the collector's write path works, then prefer a narrower one:

```bash
influx auth create \
  --org mbaigo \
  --write-bucket $(influx bucket list --org mbaigo --name demo --hide-headers | cut -f1) \
  --read-bucket  $(influx bucket list --org mbaigo --name demo --hide-headers | cut -f1) \
  --description "collector"
```

The collector reads and writes one bucket and needs nothing else. Giving it the
operator token means a compromised collector can delete every bucket on the
server, and the narrower token costs one command.

### 5. Check it before starting the collector

```bash
influx ping
influx bucket list --org mbaigo
```

If both answer, the collector's four settings — `db_url`, `token`,
`organization`, `bucket` — are the whole of what remains.

> **The token in `initTemplate` is a real one from a past deployment.** It is
> checked into this repository and written into every generated
> `systemconfig.json`, so it must be replaced, and it should be revoked on any
> server that still honours it: `influx auth delete --id <id>`.
