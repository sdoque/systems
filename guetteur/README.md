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

## On the sensor

The students ran it on the SF45/B (their commit `e5d4545`, "working guetteur
and loader"), and rewrote the command set from the product guide, which the
first version had been written without. It is now checked against
`SF45B-Product-Guide-v3.pdf` (revision 3, July 2025):

- distances are **centimeters**, after command 27 selects the first raw return
  and the yaw angle, in that order;
- streaming is started by writing **5** to command 30;
- a lost signal reads **−1000 cm**, and every non-positive distance is kept as
  a point that did not come back, never as a distance;
- the sector limits are **float32**, the low one negative.

Three things the guide leaves unclear, and which the sensor has so far accepted
as sent: command 96 is listed as "1/Uint16" and one byte is sent; commands 98
and 99 are listed as `uint32` while taking negative angles, and `float32` is
sent; and the **update rate** (command 66) is an enumeration whose revision-3
meaning is 8 = 1250 samples per second, where an older table said 388 Hz.
Revision 3 gives ±5 cm accuracy up to 500 samples per second and ±10 cm above.

The framing, the CRC and the distance parser are now covered by tests
(`lwnx_test.go`); before, a change of unit from millimeters to centimeters went
through without a single test noticing.

| field | default | |
|---|---|---|
| `updateRate` | 8 | command 66, the sensor's own enumeration; see above |
| `scanDelay` | 5 | command 85, the delay between scan positions, 5–2000 |
| `sweepHz`, `pointsPerSweep` | 5, 160 | the simulator only |

