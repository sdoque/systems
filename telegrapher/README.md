# mbaigo System: Telegrapher

The word *telegrapher* was chosen because there is no direct equivalent for *telemetry* — the one-way, periodic transmission of measurements to a remote system. Just as a telegrapher relays messages between two worlds, this system bridges the Arrowhead local cloud and an MQTT broker.

MQTT is a messaging protocol, not a service-oriented solution. The Telegrapher transforms MQTT topics into Arrowhead services by extracting path segments and interpreting them as service metadata. It works in two modes, selected by the sign of the `period` trait:

- **period < 0 — subscriber mode**: the Telegrapher subscribes to an MQTT topic and exposes the latest message as an Arrowhead service (GET/PUT).
- **period > 0 — publisher mode**: the Telegrapher periodically consumes an Arrowhead service and publishes the result to an MQTT topic. A read-only GET service describes what is being published, from where, and how often.

---

## Subscriber mode (period < 0)

The Telegrapher subscribes to the MQTT broker and caches the latest message. It registers the topic as an Arrowhead service so other systems in the local cloud can consume it via HTTP.

```mermaid
sequenceDiagram
    participant Broker as MQTT Broker
    participant T as Telegrapher
    participant R as Arrowhead Service Registrar
    participant C as Arrowhead Consumer

    T->>Broker: SUBSCRIBE Kitchen/temperature
    Broker-->>T: (subscription confirmed)
    T->>R: POST /registry (register temperature service)

    loop on every MQTT message
        Broker->>T: PUBLISH Kitchen/temperature payload
        T->>T: cache latest message
    end

    C->>T: GET /telegrapher/Kitchen_temperature/access
    T-->>C: 200 OK (SignalA_v1a: value, unit, arrival time)
```

### What the service returns

Most MQTT topics in a plant carry an analog reading — a temperature or a
pressure from an ESP32 — so that is what the configuration assumes by default.

**Declaring a `Unit` is what says the topic is a signal.** With one, the payload
is parsed and served as a `SignalA_v1a`, which a consumer can unpack and
`GetState` can convert into whatever unit that consumer asked for. Without one,
the payload is passed through exactly as it arrived, because a topic carrying
something other than a reading must still work.

The reading is taken from either shape firmware authors publish. Where the
payload is an object, the field read is **the one the topic is named after** —
the part after the last slash, which is also the service definition the asset
registers:

| Topic | Payload | Value |
|-------|---------|-------|
| any | `21.5` | 21.5 — a bare number needs no name |
| any | `{"value": 21.5}` | 21.5 |
| any | `{"unit":"C","value":19.75,"rssi":-58}` | 19.75 |
| `Kitchen/temperature` | `{"temperature":21.5,"humidity":45,"pressure":1013.2}` | 21.5 |
| `Kitchen/pressure` | `{"temperature":21.5,"humidity":45,"pressure":1013.2}` | 1013.2 |
| `Kitchen/temperature` | `{"humidity":45}` | **error** — the topic promises a temperature |
| any | `{"status":"ok"}` | **error** — a payload with no number is refused |

A sensor publishing several quantities in one object — a BME280, a Netatmo
module — is therefore read correctly on each of its topics. `value` is still
accepted whatever the topic, since it is the one name that means "the reading",
and a field matching the topic in another casing is matched too.

A payload with no number in it fails rather than defaulting to zero: zero is a
plausible temperature, and a fabricated reading in a control loop would look
entirely normal.

The signal's timestamp is **when the message arrived**, not when it was
requested, so a consumer can tell a stale topic from a live one.

---

## Publisher mode (period > 0)

The Telegrapher discovers and periodically polls an Arrowhead service, then publishes the result to the MQTT broker. A companion GET service reports the source, topic, broker, and period.

```mermaid
sequenceDiagram
    participant S as Arrowhead Service Provider
    participant O as Arrowhead Orchestrator
    participant T as Telegrapher
    participant R as Arrowhead Service Registrar
    participant Broker as MQTT Broker
    participant B as Browser

    T->>R: POST /registry (register publish info service)
    T->>O: POST /orchestration (discover temperature provider)
    O-->>T: provider URL

    loop every period seconds
        T->>S: GET provider URL
        S-->>T: temperature value
        T->>Broker: PUBLISH Kitchen/temperature payload
    end

    B->>T: GET /telegrapher/Kitchen_temperature/publish
    T-->>B: Source: http://192.168.1.6:20150/ds18b20/28-00000f030344/temperature
            MQTT topic: Kitchen/temperature
            Broker: tcp://192.168.1.10:1883
            Period: 2 s
```

---

## Configuration

The unit asset name is the MQTT topic (e.g. `Kitchen/temperature`). The `pattern` trait maps topic path segments to Arrowhead service metadata keys.

Example `systemconfig.json` excerpt:

```json
{
    "name": "Kitchen/temperature",
    "traits": [{
        "broker": "tcp://192.168.1.10:1883",
        "pattern": ["FunctionalLocation"],
        "username": "user",
        "password": "password",
        "period": 2
    }],
    "services": [{
        "definition": "temperature",
        "subpath": "access",
        "mission": "measurement",
        "details": {
            "Forms":        ["SignalA_v1a"],
            "Unit":         ["<http://qudt.org/vocab/unit/DEG_C>"],
            "QuantityKind": ["<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"]
        }
    }]
}
```

`pattern` names the detail keys the topic's segments are filed under, and the
key matters: the authorizer's pairing rule and the knowledge graph both read the
literal string `FunctionalLocation`. A topic filed under any other key is an
asset with *no* location — and an asset with no location is universally
reachable, so the wrong key is the permissive answer rather than a broken one.

`mission` is declared per service rather than on the asset, because a topic path
discloses nothing about whether what sits behind it is observed or driven. Only
whoever configures the topic knows.

`QuantityKind` is what a consumer is matched on, so a topic can stand in for a
sensor reporting the same quantity in a different unit; `Unit` is what the
reading is converted from.

---

## Compiling

```bash
go build -o telegrapher
```

Cross-compile for Raspberry Pi 4/5 (64-bit):

```bash
GOOS=linux GOARCH=arm64 go build -o telegrapher_rpi64
```

Run from its own directory — the system reads and writes `systemconfig.json` locally. If the file is missing, a template is generated and the program exits so you can edit it.

---

## Deploying the MQTT Broker

If you need an MQTT broker for testing, install [Eclipse Mosquitto](https://mosquitto.org):

```bash
sudo apt update && sudo apt install -y mosquitto mosquitto-clients
```

### Basic publish/subscribe test

```bash
mosquitto_pub -h localhost -t Kitchen/temperature -m '{"value":21.5}'
mosquitto_sub -h localhost -t Kitchen/temperature
```

A test publisher that generates a sine-wave temperature signal is provided in the `mqttGen/` subdirectory:

```bash
cd mqttGen && go run mqttGen.go
```

---

## Adding authentication

Edit `/etc/mosquitto/mosquitto.conf`:

```conf
listener 1883 0.0.0.0
allow_anonymous false
password_file /etc/mosquitto/pwdfile
```

Add users:

```bash
sudo mosquitto_passwd -c /etc/mosquitto/pwdfile myuser
sudo service mosquitto restart
```

Test authenticated access from another host:

```bash
mosquitto_sub -h 192.168.1.10 -t Kitchen/temperature -u myuser -P mypassword
```

For external (internet-facing) deployments, use port 8883 with TLS.
