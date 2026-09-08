# guetteur

*Lookout.* A scanning laser rangefinder as an Arrowhead unit asset — the system
that gives a cloud eyes.

It is the sensor and nothing more. It knows the serial protocol, the scan
sector and how to tell a return from a silence; it knows nothing about the
vehicle carrying it. Mounting pose belongs to a driver, mapping belongs to the
[cartographer](../cartographer).

## Services

| service | form | what it is |
|---|---|---|
| `scan` | `ScanA_v1a` | the latest complete sweep: angle, distance, and whether each point was a return |
| `clearance` | `SignalA_v1a` | the nearest return within the forward sector, in metres |
| `scansector` | `SignalA_v1a` | the width of the scanned arc in degrees (GET/PUT) |
| `quality` | `SignalA_v1a` | the share of the last sweep that came back |

## The one idea worth understanding

**A rangefinder that sees nothing reports either its maximum range or an
error.** Fog, a black surface, standing water, beyond range — all produce the
same bytes as a clear view to fifty metres. If that reaches a consumer as a
distance, the vehicle drives into what it cannot see.

So `clearance` answers **503, with no body**, when the forward sector holds no
valid returns, and again when the newest sweep is older than the freshness
window. There is no float that safely means *blind*: every float is a distance
somebody will act on. The only honest answer is not to answer — and `quality`,
which always answers, is how a consumer learns why.

`ScanA_v1a` carries `Valid[]` beside `Distances[]` for the same reason, and
`Check()` refuses a sweep whose arrays disagree in length, because indexing them
out of step puts an obstacle at the wrong bearing.

## Running it without the sensor

The template ships `"source": "simulated"`, which ray-casts a 20 m hallway with
a doorway. The system runs on a laptop with nothing plugged in:

```
go build -o guetteur_amac . && ./guetteur_amac     # first run writes the config
./guetteur_amac
curl http://localhost:20201/guetteur/Lookout/scan
```

The doorway is in the simulated corridor deliberately. A plain corridor is the
case where scan matching is known to fail, and a simulator without one would
flatter the cartographer.

## Before it touches hardware

`lwnx.go` carries a banner saying so, and it means it. The framing is written
from the published description of LWNX and has **not** been checked against
`SF45B-Product-Guide-v3.pdf`. Three things are unconfirmed: the message IDs, the
field layout of the distance message, and how a no-return is signalled on the
wire.

What makes it safe to ship unverified is the CRC: every frame is checked, and if
the framing is wrong then no frame ever validates and the system says so after
fifty attempts. A wrong guess is loud rather than silent — which is the opposite
of how the file-based integration fails today.
