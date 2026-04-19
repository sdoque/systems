# eThermostat

eThermostat is an electrical heating thermostat that controls ZigBee smart plugs (via [beekeeper](../beekeeper/README.md)) based on temperature readings from a weather station (via [meteorologue](../meteorologue/README.md)). It discovers its dependencies automatically through the Arrowhead service mesh and uses a proportional (P) controller to decide whether each heater plug should be on or off.

## Problem and solution

Electric panel heaters are typically controlled by individual mechanical thermostats with no network connectivity. eThermostat replaces that function in software: it discovers every beekeeper OnOff plug whose `DisplayName` ends in `"Heater"`, matches it to the closest meteorologue temperature module by `FunctionalLocation`, and applies a P-controller to switch the plug on or off each sampling period.

Because beekeeper and meteorologue may not be registered yet when eThermostat starts, the system retries service discovery every 15 s until at least one heater/temperature pair is found. No manual restart is needed after the dependencies come online.

## Architecture

```
meteorologue  ──►  Temperature service
                        │
                        ▼
                  eThermostat  (P-controller, port 20196)
                        │
                        ▼
beekeeper     ──►  OnOff service  ──►  ZigBee smart plug  ──►  panel heater
```

One unit asset (and one feedback loop goroutine) is created per matched heater plug. Each asset exposes three services:

| Service        | Sub-path       | Methods   | Description                                      |
|----------------|----------------|-----------|--------------------------------------------------|
| `setpoint`     | `setpoint`     | GET, PUT  | Read or update the thermal setpoint (°C)         |
| `thermalerror` | `thermalerror` | GET       | Current difference between setpoint and temperature |
| `jitter`       | `jitter`       | GET       | Control loop execution jitter (ms)               |

## Heater and temperature matching

**Heater discovery**: beekeeper OnOff services are filtered to those whose `DisplayName` detail ends in `"Heater"`. The prefix becomes the functional location — e.g. `"KitchenHeater"` → location `"Kitchen"`.

**Temperature matching**: meteorologue Temperature services are searched for a node whose `FunctionalLocation` detail contains the location string. If no exact match is found, the first available temperature node is used as a fallback.

## P-controller

The controller output is:

```
output = Kp × (setpoint − temperature) + 50
```

Clamped to [0, 100]. The plug is switched **ON** when `output > 50` (i.e. room temperature is below setpoint) and **OFF** otherwise.

| Parameter | Default | Description                          |
|-----------|---------|--------------------------------------|
| `setPoint`       | `20.0`  | Target temperature (°C)       |
| `samplingPeriod` | `10`    | Control loop period (seconds) |
| `kp`             | `5.0`   | Proportional gain             |

With the defaults, the plug turns on when the room is more than 0 °C below setpoint and off when it is at or above setpoint.

## Flatner integration

eThermostat registers its `setpoint` services with the Arrowhead service mesh. [Flatner](../flatner/README.md) discovers all registered `setpoint` services and adjusts them inversely to the electricity spot price — pushing the setpoints down during expensive periods to flatten peak energy demand. No extra configuration is required in eThermostat.

## Configuration (`systemconfig.json`)

```json
{
  "systemname": "ethermostat",
  "ipAddresses": ["127.0.0.1"],
  "unit_assets": [
    {
      "name": "KitchenHeater",
      "mission": "electric_heating",
      "details": { "FunctionalLocation": ["Kitchen"] },
      "traits": [
        { "setPoint": 20.0, "samplingPeriod": 10, "kp": 5.0 }
      ],
      "services": [
        { "definition": "setpoint",     "subpath": "setpoint",     "registrationPeriod": 120 },
        { "definition": "thermalerror", "subpath": "thermalerror", "registrationPeriod": 120 },
        { "definition": "jitter",       "subpath": "jitter",       "registrationPeriod": 120 }
      ]
    }
  ],
  "protocolsNports": { "coap": 0, "http": 20196, "https": 0 },
  "coreSystems": [
    { "coreSystem": "serviceregistrar", "url": "http://127.0.0.1:20102/serviceregistrar/registry" },
    { "coreSystem": "orchestrator",     "url": "http://127.0.0.1:20103/orchestrator/orchestration" },
    { "coreSystem": "ca",               "url": "http://127.0.0.1:20100/ca/certification" },
    { "coreSystem": "maitreD",          "url": "http://127.0.0.1:20101/maitreD/maitreD" }
  ]
}
```

The `name` and `FunctionalLocation` in the configuration serve as defaults for the template written to `systemconfig.json` on first run. The actual unit assets created at runtime are driven entirely by service discovery — one per matched heater plug.

## Example curl commands

**Read the setpoint for the kitchen heater:**
```bash
curl -s http://localhost:20196/ethermostat/KitchenHeater/setpoint
```

**Update the setpoint to 22 °C:**
```bash
curl -s -X PUT http://localhost:20196/ethermostat/KitchenHeater/setpoint \
  -H "Content-Type: application/json" \
  -d '{"value": 22.0, "unit": "Celsius", "version": "SignalA_v1a"}'
```

**Read the current thermal error:**
```bash
curl -s http://localhost:20196/ethermostat/KitchenHeater/thermalerror
```

**Read the control loop jitter:**
```bash
curl -s http://localhost:20196/ethermostat/KitchenHeater/jitter
```

## Build and run

```bash
# From the workspace root
go build -o ethermostat ./systems/ethermostat
./ethermostat
```

eThermostat will log a retry message every 15 s until beekeeper and meteorologue are registered, then create one thermostat per discovered heater plug automatically.

## Dependencies

| System | Role |
|--------|------|
| `esr` / `serviceregistrar` | Service registration and discovery |
| `orchestrator` | Returns service endpoints on request |
| `beekeeper` | Provides OnOff services for ZigBee smart plugs |
| `meteorologue` | Provides Temperature services from weather station modules |
| `flatner` | Optional — adjusts setpoints based on electricity spot price |

## Contributors

- Jan A. van Deventer, Luleå — initial implementation

## License

MIT — see [LICENSE](../../LICENSE) for details.
