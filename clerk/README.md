# Clerk

Clerk is a browser-based order entry front-end for pen holder orders. It is an mbaigo-compliant system that serves a single-page web interface to operators, validates orders client- and server-side, and forwards confirmed orders to the [Tracker](../tracker/README.md) system for persistence.

## Problem and solution

Operators need a simple, self-contained way to place and retrieve pen holder orders without installing any dedicated software. Clerk solves this by embedding the entire user interface as a Go string constant — the binary serves the page directly, with no external static files or web server required.

Order data is never stored locally. Clerk acts exclusively as a front-end proxy: it validates, packages, and forwards each submission to Tracker over the mbaigo service mesh, and proxies lookup responses back to the browser.

## Architecture

```
Browser
  │  GET /clerk/product/orders        → serve embedded HTML page
  │  POST /clerk/product/orders       → submit new order
  │  GET /clerk/product/orders?id=N   → look up order (requires email too)
  ▼
Clerk (port 20190)
  │  discovers Tracker's "order" service via Orchestrator
  │  POST / GET  →  Tracker (port 20191)
  ▼
Tracker  ──►  SQLite orders.db
```

Clerk registers one service with the mbaigo service registrar:

| Service    | Sub-path | Methods     | Description                                      |
|------------|----------|-------------|--------------------------------------------------|
| `orders`   | `orders` | GET, POST   | Serve the order page, submit orders, look up     |

## User interface

Opening `http://localhost:20190/clerk/product/orders` in a browser shows two panels:

### New Order

| Field            | Required | Constraints                                      |
|------------------|----------|--------------------------------------------------|
| Name             | Yes      |                                                  |
| Email            | Yes      | Valid email format                               |
| Height           | Yes      | 0 < height ≤ 21 mm                               |
| Depth            | Yes      | 0 ≤ depth ≤ height                               |
| Surface Roughness| Yes      | Ra 32 / 63 / 125 µm (drop-down)                 |
| Production Line  | No       | Free text, e.g. `LineA`                          |
| Peppol ID        | No       | Enables e-invoice delivery via the Peppol network (e.g. `0192:987654321`) |

Validation runs client-side on submit and again server-side before the order is forwarded to Tracker.

On success the browser displays the confirmed order number assigned by Tracker.

### Look Up Order

Both an **order number** and the **email address** used when placing the order are required. This prevents enumeration: a wrong email returns the same vague error as a wrong order number, revealing nothing about whether the order exists.

## Order form (`PenHolderOrder_v1`)

```json
{
  "order_number":        0,
  "name":                "Alice Example",
  "email":               "alice@example.com",
  "height":              15.0,
  "depth":               5.0,
  "roughness":           63,
  "production_line":     "LineA",
  "peppol_id":           "0192:987654321",
  "timestamp":           "2026-04-13T08:00:00Z",
  "completed_timestamp": "0001-01-01T00:00:00Z",
  "version":             "PenHolderOrder_v1"
}
```

`order_number` must be `0` for a new order; Tracker assigns the actual number and returns it in the response.

## Peppol integration

The optional Peppol ID field records the buyer's Peppol participant identifier. The expected format is `scheme:identifier`, where the scheme is a four-digit ISO 6523 ICD code:

| Scheme | Country / Network        | Example              |
|--------|--------------------------|----------------------|
| `0007` | Sweden (Bolagsverket)    | `0007:5560564317`    |
| `0088` | Global (GS1 GLN)         | `0088:1234567890128` |
| `0192` | Norway (Brønnøysund)     | `0192:987654321`     |
| `9930` | Germany (VAT)            | `9930:DE123456789`   |

Clerk stores the Peppol ID in the order record it forwards to Tracker. Downstream systems that consume the `addorder` cervice published by Tracker can use this identifier to send a structured e-invoice via the Peppol network.

## Configuration (`systemconfig.json`)

```json
{
  "systemname": "clerk",
  "ipAddresses": ["127.0.0.1"],
  "unit_assets": [
    {
      "name": "product",
      "mission": "take_orders",
      "details": { "Collection": ["PenHolder"] },
      "services": [
        {
          "definition": "orders",
          "subpath": "orders",
          "details": { "Forms": ["PenHolderOrder_v1"] },
          "registrationPeriod": 60
        }
      ]
    }
  ],
  "protocolsNports": { "coap": 0, "http": 20190, "https": 0 },
  "coreSystems": [
    { "coreSystem": "serviceregistrar", "url": "http://127.0.0.1:20102/serviceregistrar/registry" },
    { "coreSystem": "orchestrator",     "url": "http://127.0.0.1:20103/orchestrator/orchestration" },
    { "coreSystem": "ca",               "url": "http://127.0.0.1:20100/ca/certification" },
    { "coreSystem": "maitreD",          "url": "http://127.0.0.1:20101/maitreD/maitreD" }
  ]
}
```

## Example curl commands

**Submit a new order:**
```bash
curl -s -X POST http://localhost:20190/clerk/product/orders \
  -H "Content-Type: application/json" \
  -d '{
    "order_number": 0,
    "name": "Alice Example",
    "email": "alice@example.com",
    "height": 15.0,
    "depth": 5.0,
    "roughness": 63,
    "production_line": "LineA",
    "peppol_id": "0192:987654321",
    "timestamp": "2026-04-13T08:00:00Z",
    "completed_timestamp": "0001-01-01T00:00:00Z",
    "version": "PenHolderOrder_v1"
  }'
```

**Look up an order (both id and email required):**
```bash
curl -s "http://localhost:20190/clerk/product/orders?id=1&email=alice%40example.com"
```

## Tests

| Test | What it covers |
|------|----------------|
| `TestOrdersHandler_GET_ServesPage` | GET without query params returns the HTML page |
| `TestOrdersHandler_InvalidMethod` | DELETE returns 405 |
| `TestServing_InvalidPath` | Unknown service path returns 400 |
| `TestSubmitOrder_ValidationHeight` | Height > 21 mm rejected with 400 |
| `TestSubmitOrder_ValidationDepth` | Depth > height rejected with 400 |
| `TestSubmitOrder_BadBody` | Malformed JSON returns 400 |
| `TestPenHolderOrder_v1_FormVersion` | `NewForm` sets version field correctly |
| `TestOrderPage_ContainsFormElements` | Embedded HTML contains all expected input elements |
| `TestOrdersHandler_GET_LookupRequiresBoth` | Lookup with only id or only email returns 400 |

Run the tests:
```bash
go test ./systems/clerk/...
```

## Build and run

```bash
# From the workspace root
go build -o clerk ./systems/clerk
./clerk
```

Then open `http://localhost:20190/clerk/product/orders` in a browser.

## Contributors

- Franziska Sievert — initial implementation
- Jan A. van Deventer, Luleå — modernized for current mbaigo

## License

MIT — see [LICENSE](../../LICENSE) for details.
