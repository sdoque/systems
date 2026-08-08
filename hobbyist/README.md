# mbaigo System: Hobbyist

## Purpose

The *Hobbyist* brings a Märklin model railway into an Arrowhead local cloud. Each
locomotive the Central Station knows becomes a unit asset, and each thing that
locomotive can be told to do — its speed, its direction, its light, its horn —
becomes a service.

The Central Station remains in charge of the layout. It performs the mfx
registration handshake when an engine is placed on the track, and it holds the
locomotive list persistently. This system listens and commands; it does not try
to be a command station.

## The model

**A locomotive is a unit asset.** The CAN interface is not — what is wired to it
is, which is the same rule that makes a Modbus register rather than the PLC the
asset in `modboss`.

**Its functional location is its own name.** A locomotive moves, so no place on
the layout describes it; but its horn is on *that* engine, and a consumer paired
to one locomotive should reach that one's horn and no other's.

| Service | Form | Mission | Notes |
|---------|------|---------|-------|
| `speed` | `SignalA_v1a` | `actuation` | percent of the decoder's full scale |
| `direction` | `SignalB_v1a` | `actuation` | true is forward |
| one per function | `SignalB_v1a` | `actuation` | named by the station: `light`, `horn`, … |

Speed is a **ratio, not a physical speed**: the wire carries 0 to 1000, a
fraction of whatever that decoder's maximum is. The service therefore reports
percent and states its range, because 50 means nothing without knowing what it
is half of. A function the station has not named keeps its number — `f4` is at
least honest.

**A locomotive in its box is still one the station knows.** Its services exist
from startup and answer 503 until the layout reports something about that UID.
Lifting an engine off the rails does not churn the service registry.

## Configuration

The locomotive list is read from `locomotives.json` beside the binary. It is not
in version control, because it describes one person's engines:

```json
[
  {
    "uid": "0x4001",
    "name": "421 393-0",
    "functions": {"0": "Light", "3": "Horn"}
  },
  {
    "uid": "0x4002",
    "name": "421 387-2",
    "functions": {"0": "Light", "3": "Horn"}
  }
]
```

`uid` is hexadecimal, with or without the `0x`. The function keys are the numbers
the decoder uses; their values are what the service will be called. The path can
be moved with a `LocomotiveList` detail on the unit asset.

The Central Station will also stream its locomotive list over CAN, which is
where this should eventually come from. A file separates two questions that are
independent — *which* locomotives exist, and *how* to command them — so the wire
format for the first can be settled without disturbing the second.

## Protocol

`marklin.go` implements Märklin's CAN protocol as published in
*Kommunikationsprotokoll GUI &lt;-&gt; GFP über CAN*, version 1.0. Every constant is
cited to a section of that document, and the two things version 1.0 does not
define — the DCC address range and any S88 feedback command — are marked as
such rather than guessed.

## What is not done yet

**There is no CAN transport.** `Bus` is an interface and the current
implementation refuses every command with *"not connected to the layout"*, so a
command that goes nowhere never looks like one that arrived. The transport
follows the pattern `busdriver` uses: raw SocketCAN syscalls in build-tagged
`can_linux.go` and `can_other.go`, no external library.

Until it is written, the system starts, registers its services, and reports its
locomotives as off the track.
