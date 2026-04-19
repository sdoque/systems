# mbaigo System: Modeler

## Purpose

The Modeler system assembles a complete **SysML v2** structural and behavioural model of a local cloud — a distributed system of systems — by collecting the individual model fragment of each registered system and merging them into a single, coherent package.

The output covers:
- **Block Definition Diagram (BDD)** — the types of all systems and their unit assets, with the services they provide and consume
- **Internal Block Diagram (IBD)** — the instantiated parts with their host metadata and live service connections at the time of the request
- **Behaviour Definitions** — per-asset action sequences derived from each unit asset's consumed services, when those cervices carry a `Mode` ("get" or "set")

The model is generated on demand by issuing an HTTP GET to the `cloudmodel` service.
It is expressed in [SysML v2 textual notation](https://www.omg.org/spec/SysML/2.0) and returned as plain text.

## How it works

```mermaid
sequenceDiagram
    actor User
    participant Modeler
    participant ServiceRegistrar
    participant System

    User->>Modeler: GET /modeler/assembler/cloudmodel
    Modeler->>ServiceRegistrar: GET /serviceregistrar/registry/syslist
    ServiceRegistrar-->>Modeler: SystemRecordList (list of base URLs)

    loop for each system in the list
        Modeler->>System: GET /<system>/smodel
        System-->>Modeler: SysML v2 package (port defs, part defs, IBD part, behaviour defs)
    end

    Modeler->>Modeler: merge packages<br/>(deduplicate port defs and abstract action defs,<br/>collect block defs, IBD parts, behaviour defs)
    Modeler-->>User: SysML v2 package 'localCloud' { … }
```

Each system's `/smodel` endpoint (provided by the `mbaigo` framework) generates a self-contained SysML v2 `package` with:
- **port defs** — one per unique service definition (provided or consumed)
- **part defs** — one for the system block and one per unit asset block, carrying `in`/`out` ports and the unit asset's `mission` attribute
- **IBD part** — the instantiated system with its host metadata, provided service URLs as comments, and `@connect` annotations for any already-resolved service providers
- **abstract action defs** — `GetState`, `SetState`, `Compute` (only those actually used)
- **behaviour defs** — one `action def` per unit asset whose cervices carry a `Mode`, with a linear `first X then Y;` sequence

The Modeler deduplicates `port def` and `abstract action def` declarations (the same definition can appear in many systems) and concatenates the rest into a single top-level `package`.

## Output example

```sysml
package 'localCloud' {

    // ── Port Definitions ─────────────────────────────────────────────────────
    port def 'temperature';
    port def 'rotation';
    port def 'setpoint';
    ...

    // ── Block Definitions (BDD) ──────────────────────────────────────────────
    part def 'thermostatSystem' {
        attribute name : String = "thermostat";
        part 'controller_1' : 'controller_1Block';
    }

    part def 'controller_1Block' {
        attribute mission : String = "control_heater";
        out port 'setpoint'        : ~'setpoint';         // provided
        out port 'thermalerror'    : ~'thermalerror';     // provided
        in port  'temperature'     : 'temperature';       // consumed
        in port  'rotation'        : 'rotation';          // consumed
    }
    ...

    // ── Internal Block Diagram (IBD) ─────────────────────────────────────────
    part 'thermostat' : 'thermostatSystem' {
        attribute host      : String  = "myhost";
        attribute ipAddress : String  = "192.168.1.10";
        attribute httpPort  : Integer = 20001;
        // provides: http://192.168.1.10:20001/thermostat/controller_1/setpoint
        // @connect controller_1.temperature → http://192.168.1.5:20110/ds18b20/sensor_1/temperature
        // @connect controller_1.rotation    → http://192.168.1.7:20200/parallax/servo_1/rotation
    }
    ...

    // ── Abstract Action Definitions ──────────────────────────────────────────
    abstract action def GetState;
    abstract action def SetState;
    abstract action def Compute;

    // ── Behaviour Definitions ────────────────────────────────────────────────
    action def 'controller_1Behavior' {
        action 'get_temperature' : GetState;
        action compute           : Compute;
        action 'set_rotation'    : SetState;

        first 'get_temperature' then compute;
        first compute then 'set_rotation';
    }
    ...
}
```

## Behaviour generation

A behaviour block is emitted for a unit asset when at least one of its consumed services (cervices) carries a `Mode` field set to `"get"` or `"set"`.  The sequence is always linear:

1. all `"get"` cervices (sorted alphabetically) — each becomes a `GetState` action
2. a `compute` step (`Compute`) — inserted only when both gets and sets are present
3. all `"set"` cervices (sorted alphabetically) — each becomes a `SetState` action

Consecutive steps are linked with `first X then Y;` pairs.

## Configuration

The only configurable trait is the name of the merged package:

```json
"traits": [{ "cloudName": "myLocalCloud" }]
```

If omitted, the package is named `localCloud`.

## Compiling

Initialise the module once (already done if `go.mod` is present):

```bash
go mod init github.com/sdoque/systems/modeler
go mod tidy
```

Run directly from the system's directory:

```bash
go run .
```

> It is **important** to start the program from within its own directory because it looks for `systemconfig.json` there. If the file is absent, a default one is generated and the program stops so the file can be reviewed and adjusted before the next start.

The address of the running web server is printed at startup and can be opened in any browser.

To build a binary for the local machine:

```bash
go build -o modeler_local
```

## Cross-compiling

| Target | Command |
|--------|---------|
| Raspberry Pi 64-bit | `GOOS=linux GOARCH=arm64 go build -o modeler_rpi64` |
| Linux x86-64 | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o modeler_linux_amd64` |
| macOS Apple Silicon | `GOOS=darwin GOARCH=arm64 go build -o modeler_mac_arm64` |

A full list of supported platforms: `go tool dist list`

To copy the binary to a remote host:

```bash
scp modeler_rpi64 user@192.168.1.x:~/demo/modeler/
```
