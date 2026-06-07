# mbaigo System: KGrapher

## Purpose

The *KGrapher* assembles a complete **OWL / RDF knowledge graph** of an
Arrowhead local cloud — a distributed system of systems — by collecting
the per-system RDF fragment from each registered system and merging them
into a single, coherent Turtle (TTL) document. The result is what other
systems and human consumers query when they want to reason about the cloud
as a whole.

When a GraphDB triple store is configured, the KGrapher also **pushes**
the assembled graph as a SPARQL update so the latest snapshot is queryable
remotely. Locally it always serves the same TTL over HTTP for clients that
prefer to pull.

## Architecture

The KGrapher is an **aggregator system**, the OWL/RDF sibling of the
[modeler](../modeler/) (which plays the same role for SysML v2). Both
work the same way: discover every system via the Service Registrar, fetch
each system's per-system meta-view, deduplicate and merge, hand back the
combined result. Three responsibilities:

1. **Collect.** Pull `/<system>/kgraph` from every registered system. Each
   system already emits its own RDF block — the mbaigo framework's
   `KGraphing` use case handles this. The KGrapher does no per-system
   knowledge generation; it only orchestrates collection.
2. **Merge.** Deduplicate `@prefix` declarations, rewrite prefixes against
   the local ontology files, prepend a cloud-wide IRI to per-system subjects,
   and concatenate the resulting blocks into one TTL.
3. **Publish.** Return the merged TTL as the HTTP response. If
   `graphDBurl` is set, also POST a SPARQL update that snapshots the graph
   into GraphDB.

The KGrapher is **demand-driven** — there is no background timer. Each
GET on `/cloudgraph` triggers a fresh collect + merge + push. Consumers
that want a current view ask for one; nothing is cached.

## How it fits in the cloud

```mermaid
sequenceDiagram
    autonumber
    actor User
    participant K  as KGrapher
    participant SR as ServiceRegistrar
    participant S  as System
    participant G  as GraphDB

    User->>K: GET /kgrapher/assembler/cloudgraph
    K->>SR: GET /serviceregistrar/registry/syslist
    SR-->>K: SystemRecordList (list of base URLs)

    loop for each system
        K->>S: GET /<system>/kgraph
        S-->>K: Turtle fragment (@prefix + alc:System / afo:UnitAsset / afo:Service / …)
    end

    K->>K: dedupe prefixes, rewrite via localOntologies,<br/>prepend cloud IRI, concatenate
    opt graphDBurl configured
        K->>G: SPARQL UPDATE<br/>CLEAR GRAPH <urn:state:current>;<br/>ADD GRAPH <snapshot-IRI> TO <urn:state:current>;
    end
    K-->>User: merged TTL (text/turtle)
```

The push pattern is significant: each snapshot lands in its own named graph
keyed by IRI, and `<urn:state:current>` is rotated to always point at the
latest snapshot. Consumers querying `<urn:state:current>` always see the
current view; older snapshots persist in their own named graphs as a history
trail.

## Services

### Provided

| Service definition | Subpath | Methods | Description |
|--------------------|---------|---------|-------------|
| `cloudgraph` | `cloudgraph` | `GET` | Assembles the cloud's RDF from every registered system, pushes it to GraphDB if configured, and returns the merged Turtle |
| `localOntologies` | `localontologies` | `GET` | HTML index of the ontology files the KGrapher serves locally, with links to download each |

### Consumed (via Arrowhead orchestration)

| Service definition | Used for |
|--------------------|----------|
| Pulled from every registered system via `/<system>/kgraph` | Per-system RDF fragments to merge |

The KGrapher also POSTs to a configured **GraphDB SPARQL endpoint** — that
URL is in the trait file rather than discovered via Arrowhead, since GraphDB
isn't an mbaigo system.

## Configuration

```json
{
    "systemname": "kgrapher",
    "unit_assets": [
        {
            "name": "assembler",
            "details": { "Type": ["Interactive"] },
            "services": [
                {
                    "definition": "cloudgraph",
                    "subpath": "cloudgraph",
                    "details": { "Format": ["Turtle"] },
                    "registrationPeriod": 61
                },
                {
                    "definition": "localOntologies",
                    "subpath": "localontologies",
                    "details": { "Location": ["Files"] },
                    "registrationPeriod": 61
                }
            ],
            "traits": [
                {
                    "graphDBurl": "http://<graphdb-host>:7200/repositories/<repo>/statements",
                    "localOntologies": {
                        "alc": "alc-ontology-local.ttl"
                    }
                }
            ]
        }
    ],
    "protocolsNports": { "coap": 0, "http": 20105, "https": 0 },
    "coreSystems": [ /* serviceregistrar, orchestrator, ca, maitreD */ ]
}
```

### Trait reference

| Field | Type | Description |
|-------|------|-------------|
| `graphDBurl` | string | SPARQL **update** endpoint (note the `/statements` suffix). Empty disables remote publication; local TTL serving still works |
| `localOntologies` | `map[string]string` | Per-prefix file name. The KGrapher reads each file from its own directory, rewrites the corresponding `@prefix` in the assembled output, and serves the file content over HTTP so other systems can dereference it |

## The push mechanism in detail

When `graphDBurl` is set, each `/cloudgraph` GET ends with a two-statement
SPARQL update:

```sparql
CLEAR GRAPH <urn:state:current>;
ADD GRAPH <https://arrowheadweb.org/data/<snapshot-id>> TO <urn:state:current>;
```

- The snapshot IRI is generated at push time and never reused.
- The first push to a given snapshot IRI also inserts the actual triples
  into that snapshot graph; subsequent rotations of `<urn:state:current>`
  only move the pointer.
- Old snapshots stay in the store. They are not automatically pruned —
  if you care about disk usage, drop old snapshot graphs explicitly with
  a `DROP GRAPH` update.

The advantage of this pattern over a flat *"replace all triples in graph X"*
update is that **historical state stays queryable** under each
timestamp-keyed graph IRI, while consumers who only care about "now" can
query `<urn:state:current>` and ignore the history.

## Predicate emission and the FunctionalLocation special-case

Per-system RDF emitters in mbaigo's `usecases/kgraphing.go` apply one
naming rule with one carve-out:

- Most detail keys on a unit asset become `alc:has<Key>` triples — local
  to the cloud's `alc:` namespace.
- **`FunctionalLocation`** is emitted as `afo:hasFunctionalLocation` —
  in the AFO (Arrowhead Framework Ontology) namespace — so AFO-IDO,
  AFO-DEXPI, and AFO-STEP alignment ontologies can bridge it to their
  upstream vocabularies (e.g., `ido:locatedAt`). A cloud-local predicate
  wouldn't participate in those alignments; the AFO predicate does.

If you add other detail keys that warrant cross-ontology alignment, the
list lives in `mbaigo/usecases/kgraphing.go` and adding a new carve-out
is a one-line change.

## Building and running

```bash
# Run from source (development)
go run .

# Build a binary for the current machine
go build -o kgrapher_amac .

# Cross-compile for a 64-bit Raspberry Pi
GOOS=linux GOARCH=arm64 go build -o kgrapher_rpi64 .

# Deploy
scp kgrapher_rpi64 jan@<pi-host>:oslo/kgrapher/
```

Run the binary from **inside its own directory** so it can find (or
auto-generate) `systemconfig.json` and read the `localOntologies` files.

## Startup order

```
Arrowhead core systems  →  KGrapher  →  any consumer
```

The KGrapher is **demand-driven and stateless** — it pulls from the
registrar on each request, so application systems can join after it's
already running. The first `/cloudgraph` request after a system joins
will include that system's fragment automatically.

If `graphDBurl` points at a triple store that isn't yet up, the SPARQL
update will fail and the KGrapher will log the error but still return
the assembled TTL to the HTTP caller. GraphDB outage degrades the push
side without breaking the read side.

## Development with a local mbaigo clone

Add both modules to the workspace `go.work` at the repository root:

```
use ./mbaigo
use ./systems/kgrapher
```

Or add a `replace` directive to `go.mod`:

```
require github.com/sdoque/mbaigo v0.x.x
replace github.com/sdoque/mbaigo => ../../mbaigo
```

---

## Appendix A — Deploying GraphDB on a Raspberry Pi

The KGrapher's `graphDBurl` is a URL like any other — the triple store
itself can run anywhere reachable. For a self-contained edge deployment
the Pi is a workable host. The walkthrough below was originally part of
the body of this README; it's preserved here as reference for new
deployments.

### 1. Install Docker

```bash
curl -sSL https://get.docker.com | sh
```

- `curl -sSL` fetches the install script silently and follows redirects.
- `https://get.docker.com` is Docker's official install script.
- `| sh` pipes the script straight into the shell.

To inspect the script before running:

```bash
curl -sSL https://get.docker.com -o get-docker.sh
cat get-docker.sh
sh get-docker.sh
```

### 2. Add the user to the docker group

```bash
sudo usermod -aG docker pi
```

(Replace `pi` with your actual username if different.) Log out and back
in for the new group to take effect, then `docker` commands no longer
need `sudo`.

### 3. Pull the GraphDB image

```bash
docker pull ontotext/graphdb:10.7.4
```

Pin the version explicitly — `:latest` is convenient locally but unkind
when a fleet of Pis pulls a new image at different times.

### 4. Run the container

Bridged networking (recommended for most setups):

```bash
docker run -d -p 7200:7200 --name graphdb ontotext/graphdb:10.7.4
```

Host networking (only if you need the container to bind on the host's
IP directly — e.g., for some service-discovery setups):

```bash
docker run -d --network host --name graphdb ontotext/graphdb:10.7.4
```

Choose **one** of the two. The bridged form is the default; use the host
form only when you have a specific reason.

### 5. Stop and remove

```bash
docker stop graphdb
docker rm graphdb
```
