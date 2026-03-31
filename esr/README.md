# Ephemeral Service Registry System

The Ephemeral Service Registry (ESR) is a mandatory Arrowhead core system that
tracks the currently available services in a local cloud. It uses an in-memory
map (keyed by a unique integer ID) rather than an SQL database, reflecting the
fact that only the *currently* available services need to be kept — hence
"ephemeral".

If permanent history is needed, the Modeler system with its graph database is
the right complement.

## Services

| Sub-path    | Methods      | Description |
|-------------|--------------|-------------|
| `register`  | POST, PUT    | Register a new service (POST) or renew its expiry time (PUT). |
| `query`     | GET, POST    | Browser view of all registered services (GET) or orchestrator lookup by definition and details (POST). |
| `unregister`| DELETE       | Remove a service record by ID. |
| `status`    | GET          | Reports whether this instance is the leading registrar or on standby. |

## Registration service

An Arrowhead system registers itself by sending a POST to `/register` with a
`ServiceRecord_v1` payload. The ESR assigns an ID, sets the validity window, and
returns the completed record. The system must renew its registration before the
`EndOfValidity` time passes by sending a PUT with the same record (including the
assigned ID); if it does not, `checkExpiration` removes the record automatically.

### Sequence diagram

```mermaid
sequenceDiagram
    participant System as Arrowhead System
    participant ESR
    participant Registry as serviceRegistryHandler

    Note over System,Registry: Initial registration (POST)

    System->>ESR: POST /register  (ServiceRecord_v1, Id=0)
    ESR->>Registry: add record
    Note over Registry: new ID assigned<br/>EndOfValidity = now + RegLife<br/>expiration timer scheduled
    Registry-->>ESR: success
    ESR-->>System: 200  (ServiceRecord_v1 with Id, Created, EndOfValidity)

    Note over System,Registry: Renewal before expiry (PUT)

    System->>ESR: PUT /register  (ServiceRecord_v1, same Id)
    ESR->>Registry: add record
    Note over Registry: Id exists → renew<br/>EndOfValidity extended<br/>expiration timer rescheduled
    Registry-->>ESR: success
    ESR-->>System: 200  (ServiceRecord_v1 with updated EndOfValidity)

    Note over System,Registry: Expiry (no renewal sent)

    Note over Registry: checkExpiration() fires<br/>record deleted + notify()

    Note over System,Registry: Graceful unregistration (DELETE)

    System->>ESR: DELETE /unregister/{id}
    ESR->>Registry: delete record
    Note over Registry: record removed<br/>expiration timer cancelled<br/>notify()
    Registry-->>ESR: success
    ESR-->>System: 200
```

## Live browser view (Server-Sent Events)

Opening `http://<host>:<port>/serviceregistrar/registry/query` in a browser
returns a page that immediately opens a persistent
[Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)
connection back to the same URL. Every time the registry changes — a service
registers, renews, is unregistered, or expires — the server pushes a fresh
sorted snapshot to every open browser tab with no polling and no manual refresh
required.

### Sequence diagram

```mermaid
sequenceDiagram
    participant Browser
    participant System as Arrowhead System
    participant ESR
    participant Registry as serviceRegistryHandler

    Note over Browser,Registry: Opening the page

    Browser->>ESR: GET /query (Accept: text/html)
    ESR-->>Browser: 200 HTML skeleton + EventSource script

    Browser->>ESR: GET /query (Accept: text/event-stream)
    Note over ESR: SSE connection kept open,<br/>subscriber channel registered
    ESR->>Registry: read all records
    Registry-->>ESR: sorted service list
    ESR-->>Browser: data: list items (initial snapshot)

    Note over Browser,Registry: New system registers

    System->>ESR: POST /register (ServiceRecord_v1)
    ESR->>Registry: add record
    Registry-->>ESR: success + notify()
    ESR->>Registry: read all records
    Registry-->>ESR: updated sorted list
    ESR-->>Browser: data: list items (push update)

    Note over Browser,Registry: Service registration expires

    Note over Registry: checkExpiration() fires<br/>record deleted + notify()
    ESR->>Registry: read all records
    Registry-->>ESR: updated sorted list
    ESR-->>Browser: data: list items (push update)

    Note over Browser,Registry: Browser tab closed

    Browser--xESR: TCP connection closed
    Note over ESR: r.Context().Done()<br/>subscriber channel removed
```

### Why not polling?

| Approach | Requests while idle | Requests per change |
|---|---|---|
| Browser auto-refresh every 5 s | 12 / min continuously | 1 |
| Server-Sent Events | 0 | 1 push per open tab |

Each open browser tab costs one persistent TCP connection and one goroutine.
For a deployment tool used by a handful of engineers this overhead is
negligible.

## Compilation

After cloning the *Systems* repository, navigate to the `esr` directory and
initialise the module (once only):

```bash
go mod init github.com/sdoque/systems/esr
go mod tidy
```

Run directly:

```bash
go run .
```

On first run the program generates `systemconfig.json` and exits so you can
edit it. On the next run the system starts, prints the URL of its web server,
and is ready to use.

Build for the local machine:

```bash
go build -o esr
```

## Cross-compilation

| Target | Command |
|--------|---------|
| Intel Mac | `GOOS=darwin GOARCH=amd64 go build -o esr_imac` |
| ARM Mac | `GOOS=darwin GOARCH=arm64 go build -o esr_amac` |
| Windows 64 | `GOOS=windows GOARCH=amd64 go build -o esr_win64.exe` |
| Raspberry Pi 64 | `GOOS=linux GOARCH=arm64 go build -o esr_rpi64` |
| Linux x86-64 | `GOOS=linux GOARCH=amd64 go build -o esr_amd64` |

## Testing shutdown

To test graceful shutdown use the terminal (not the IDE debugger):

```bash
go run .
```

Press **Ctrl+C** to send SIGINT. Using the IDE debugger instead simulates
device failure (process kill), which is a different scenario.
