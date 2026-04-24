# mbaigo System: tracker

Tracker is an Arrowhead-integrated order management system for pen holder orders.
It persists orders to a local **SQLite** database and exposes a single `order`
service that other systems — such as a production TSP — can use to file,
update, and retrieve orders over HTTP.

```
POST   /tracker/product/order          →  create new order, return assigned OrderNumber
PUT    /tracker/product/order          →  update existing order by OrderNumber
GET    /tracker/product/order?id=N     →  retrieve order N
```

---

## The problem: order data scattered across tools

In a small manufacturing operation an engineer receives a customer request, enters
it into a spreadsheet, emails the production floor, and later updates a second
spreadsheet when the job is done.  Three tools hold the same facts.  When a
detail changes — production line reassignment, revised dimensions — at least two
of them must be kept in sync manually, and they will eventually diverge.

---

## The solution: a single service-oriented order record

Tracker is the single place where an order lives.  Any Arrowhead system that
needs to act on an order — a production scheduler, a quality dashboard, a
shipping notifier — discovers the `order` service through the Orchestrator and
reads or updates the record directly.  No email, no spreadsheet, no copy-paste.

When a new order arrives (`OrderNumber ≤ 0`), tracker:

1. Assigns an `OrderNumber` (SQLite `AUTOINCREMENT`).
2. Persists the record.
3. Forwards the complete record to whatever system has registered the `addorder`
   service with the Orchestrator — typically a production TSP that schedules
   the job on a production line.

```
┌─────────────────────────────────────────────────────────────┐
│                     Arrowhead local cloud                   │
│                                                             │
│  Customer UI ──POST──► tracker ──► SQLite (orders.db)       │
│                            │                                │
│                            └──addorder──► production TSP    │
│                                                             │
│  Dashboard  ──GET───► tracker                               │
│  Scheduler  ──PUT───► tracker                               │
└─────────────────────────────────────────────────────────────┘
```

---

## The order form: `PenHolderOrder_v1`

Every request and response body carries a `PenHolderOrder_v1` JSON object.

| Field | Type | Description |
|---|---|---|
| `order_number` | `int` | Assigned by tracker on insert (send `0` for a new order) |
| `name` | `string` | Customer name |
| `email` | `string` | Customer email |
| `height` | `float64` | Pen holder height (mm) |
| `depth` | `float64` | Pen holder depth (mm) |
| `roughness` | `int` | Surface roughness grade |
| `timestamp` | `time.Time` | When the order was placed |
| `completed_timestamp` | `time.Time` | When production finished (zero until complete) |
| `production_line` | `string` | Assigned production line |
| `version` | `string` | Always `"PenHolderOrder_v1"` |

The `version` field identifies the form type to the mbaigo unpacking machinery
so the correct Go struct is instantiated automatically.

---

## Database schema

The `orders.db` SQLite file is created automatically on first run in the
working directory.

```sql
CREATE TABLE IF NOT EXISTS PenHolderOrders (
    OrderNumber        INTEGER PRIMARY KEY AUTOINCREMENT,
    Name               TEXT    NOT NULL,
    Email              TEXT    NOT NULL,
    Height             REAL    NOT NULL,
    Depth              REAL    NOT NULL,
    Roughness          INTEGER NOT NULL,
    OrderedTimestamp   DATETIME NOT NULL,
    CompletedTimestamp DATETIME,
    ProductionLine     TEXT    NOT NULL,
    Version            TEXT    NOT NULL
);
```

The database file persists across restarts.  To reset the order history, delete
`orders.db` before starting — it will be recreated empty.

---

## Architecture

### Files

| File | Responsibility |
|---|---|
| `tracker.go` | `main()` bootstrap, `serving()` dispatcher, `orderHandler` |
| `thing.go` | `Traits`, `initTemplate`, `newResource`, `PenHolderOrder_v1` form, database helpers |
| `thing_test.go` | Unit and handler tests using an in-memory SQLite database |

### Handler logic

```
orderHandler
 ├─ GET  ?id=N  →  GetOrder(db, N)  →  200 JSON
 ├─ POST        →  InsertOrder      →  forward to addorder  →  200 JSON (with assigned number)
 ├─ PUT         →  UpdateOrder      →  200 JSON
 └─ other       →  405
```

The distinction between POST (new order) and PUT (update) is made by
`order_number` in the body: **`0` means new**, any positive value means update.
Both methods are accepted because some HTTP clients cannot distinguish between
creating and updating from their perspective; the body field is the authoritative
signal.

### Forwarding to downstream systems

After inserting a new order tracker attempts to deliver it to whichever system
has registered an `addorder` service with the Orchestrator.  If no such system
is found, or if the call fails, tracker logs a warning and continues — the order
is safely in the database regardless.

---

## Configuration

Edit `systemconfig.json` to match your environment:

| Field | Description |
|---|---|
| `ipAddresses` | IP address of the machine running tracker |
| `protocolsNports` → `http` | HTTP port (default `20191`) |
| `unit_assets[0].name` | Asset name, used in the URL path (default `"product"`) |
| `unit_assets[0].details.Status` | Deployment status tag shown in the service registry |

---

## Usage

### File a new order

```bash
curl -s -X POST http://localhost:20191/tracker/product/order \
  -H "Content-Type: application/json" \
  -d '{
    "order_number": 0,
    "name": "Alice",
    "email": "alice@example.com",
    "height": 15.0,
    "depth": 5.0,
    "roughness": 3,
    "timestamp": "2026-04-13T09:00:00Z",
    "completed_timestamp": "0001-01-01T00:00:00Z",
    "production_line": "LineA",
    "version": "PenHolderOrder_v1"
  }'
```

Response — same object with the assigned `order_number`:

```json
{
  "order_number": 1,
  "name": "Alice",
  "email": "alice@example.com",
  "height": 15,
  "depth": 5,
  "roughness": 3,
  "timestamp": "2026-04-13T09:00:00Z",
  "completed_timestamp": "0001-01-01T00:00:00Z",
  "production_line": "LineA",
  "version": "PenHolderOrder_v1"
}
```

### Retrieve an order

```bash
curl http://localhost:20191/tracker/product/order?id=1
```

### Mark an order as complete

```bash
curl -s -X PUT http://localhost:20191/tracker/product/order \
  -H "Content-Type: application/json" \
  -d '{
    "order_number": 1,
    "name": "Alice",
    "email": "alice@example.com",
    "height": 15.0,
    "depth": 5.0,
    "roughness": 3,
    "timestamp": "2026-04-13T09:00:00Z",
    "completed_timestamp": "2026-04-13T14:30:00Z",
    "production_line": "LineA",
    "version": "PenHolderOrder_v1"
  }'
```

---

## Running the tests

All tests run without a running Arrowhead local cloud — database operations use
an in-memory SQLite instance.

```bash
go test ./...
```

| Test | What it checks |
|---|---|
| `TestInsertOrder` | New order receives a positive `OrderNumber` |
| `TestGetOrder` | Inserted order can be retrieved by number |
| `TestGetOrder_NotFound` | Non-existent ID returns an error |
| `TestUpdateOrder` | Field change persists after update |
| `TestUpdateOrder_NotFound` | Update on missing order returns an error |
| `TestOrderHandler_GET` | HTTP GET with `?id=N` returns 200 JSON |
| `TestOrderHandler_GET_MissingID` | HTTP GET without `?id` returns 400 |
| `TestOrderHandler_POST` | HTTP POST with `order_number=0` returns 200 with assigned number |
| `TestOrderHandler_InvalidMethod` | DELETE returns 405 |
| `TestServing_InvalidPath` | Unknown service path returns 400 |
| `TestPenHolderOrder_v1_FormVersion` | `NewForm()` sets `version` field correctly |

---

## Building and deploying

```bash
# build locally
go build -o tracker

# cross-compile for Linux x86-64
GOOS=linux GOARCH=amd64 go build -o tracker_linux

# cross-compile for Raspberry Pi 4/5
GOOS=linux GOARCH=arm64 go build -o tracker_rpi64
```

---

## Background

The design philosophy — service-oriented IoT systems registered in an Arrowhead
local cloud — is described in:

> van Deventer, J. A. (2025). *Building Arrowhead-compliant IoT systems with
> mbaigo: a Go-based framework for service-oriented automation*.
> Zenodo. <https://doi.org/10.5281/zenodo.18504110>
