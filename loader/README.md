# loader

Drives the five motors of an articulated mini wheel loader over CAN and exposes
each one as an Arrowhead service.

This system is the hardware and nothing more. It knows node IDs, the motor
controller's integer scale, and how often the drives must be spoken to. It does
**not** know the wheel radius, the wheelbase, or the shape of the vehicle —
those belong to the *driver* system. That separation is the point: change the
driver's parameters and the same loader binary runs a different vehicle.

## The unit assets

One per motor, expanded from a single configured asset the way `busdriver`
expands signals.

| asset | node | commanded in |
|---|---|---|
| `FrontLeft` | 1 | RPM at the wheel |
| `FrontRight` | 2 | RPM at the wheel |
| `BackLeft` | 3 | RPM at the wheel |
| `BackRight` | 4 | RPM at the wheel |
| `Steering` | 5 | percent of full effort |

| service | on | what it is |
|---|---|---|
| `setpoint` | every motor | **GET** what the motor is being *commanded* to; **PUT** commands it |
| `speed` | the four wheels | the RPM the wheel is *measured* to be turning, from its encoder |
| `travel` | the four wheels | revolutions turned since the encoder powered up |
| `waist` | `Steering` | the articulation sensor's raw ten-bit reading |

`setpoint` and `speed` are different numbers and the difference is the point: a
stalled motor, a slipping wheel and a working one all report the same
*setpoint*. Only `speed` says what happened.

**Steering is an effort, not an angle.** The reference bridge commands the
steering motor exactly as it commands a wheel, and closes the loop to an angle
in software against the sensor on `can1`. That loop is not here yet, so a PUT of
`50` means "push half as hard as you can to the right", not "turn 50 degrees".

## Driving a motor

**A single PUT will not drive the vehicle.** The command has to be repeated,
at least every `safetyStopMs`, for as long as you want to move — see the next
section for why, and for the arithmetic that says a lone command can never
exceed about 4.7 RPM however large a number you put in it.

```bash
WHEEL=FrontLeft
RPM=25

# drive for ten seconds, re-commanding at 5 Hz
end=$((SECONDS+10))
while [ $SECONDS -lt $end ]; do
  curl -s -X PUT http://<host>:20197/loader/$WHEEL/setpoint \
       -H "Content-Type: application/json" \
       -d "{\"value\": $RPM, \"unit\": \"RPM\", \"version\": \"SignalA_v1.0\"}" >/dev/null
  sleep 0.2
done

# then stop commanding, and it stops itself within safetyStopMs
```

Read back what it was told, and what it actually did:

```bash
curl http://<host>:20197/loader/FrontLeft/setpoint   # commanded
curl http://<host>:20197/loader/FrontLeft/speed      # measured, from the encoder
```

### `"version"` is the wire version, not the Go type name

It must be **`SignalA_v1.0`**. Not `SignalA_v1a`.

This trips everyone once, because the service advertises `"Forms":
["SignalA_v1a"]` in its details — that is the *type* name. The string inside the
payload is the *form version*, which is `SignalA_v1.0`, and it is what the
receiving system looks up to decide how to read the body.

Get it wrong and the request is refused with `400 malformed request` — but the
loader's own log names the fault exactly:

    loader: FrontLeft: bad set request: unsupported form version: SignalA_v1a

so when a command does nothing, read the loader's log before anything else.
Omitting `"version"` altogether gives `'version' key not found in data`.

At 1 km/h a 12 inch wheel turns **17.4 RPM**, so `17.4` is a walking pace and
`120` is the configured maximum.

## Two behaviours worth knowing before it moves

**It stops itself.** If nothing commands the loader for `safetyStopMs`
(2 s by default) every setpoint goes to zero. Every other actuator in this cloud
holds its last state when its controller goes quiet — right for a heater, wrong
for something with wheels. A driver system must therefore keep commanding, which
is also what makes a manoeuvre abortable.

**It ramps, and slowly.** Commands are rate limited towards the request,
`accelStep` per cycle when speeding up and `brakeStep` when slowing or
reversing. Braking gets the larger step: stopping should never be slower than
starting.

With the defaults that is 30 counts per cycle at 20 Hz — 600 counts per second.
One RPM is 256 counts, so the vehicle gains about **2.3 RPM per second**:

| target | counts | time to reach it |
|---|---|---|
| 10 RPM | 2 560 | 4.3 s |
| 20 RPM | 5 120 | 8.5 s |
| 120 RPM | 30 720 | 51 s |

**The two behaviours combine into a trap.** A single PUT is followed by two
seconds of silence, after which the watchdog zeroes everything — and two seconds
of ramping only reaches 1 200 counts, or **4.7 RPM**. So one command, whatever
value it carries, produces a brief twitch at walking pace and then a stop. This
is the usual reason a motor "does not respond": nothing is wrong, the command
simply was not held.

## The lowest speed that actually turns a wheel

Reported from the bench, 8 September 2026: **20 RPM turns the wheels, 10 RPM
does not**, with the command held. The threshold between the two has not been
found.

Ten RPM is 2 560 counts, or 8.3% of full scale, which is a plausible place for a
geared drive under load to sit still — static friction has to be broken before
anything moves, and the controller is commanding velocity rather than torque.
The figure will not be a constant: expect it to change with load, with
temperature, and between the four wheels.

Now that the encoders are read, it can be measured rather than watched for:

```bash
# hold a command and see whether the wheel is actually turning
curl http://<host>:20197/loader/FrontLeft/speed
```

`speed` answers `503` when the encoder has gone quiet, and reports a value near
zero when the wheel is commanded but stationary — which is exactly the
distinction being looked for. Walk the setpoint down from 20 in steps of 1,
holding each for five seconds (longer than the ramp), and record the lowest
value where `speed` stays away from zero.

Worth doing per wheel, and worth doing under load rather than on blocks.

## Configuration

Generated on the first run; the defaults drive the vehicle as built.

| field | default | why |
|---|---|---|
| `canInterface` | `can0` | motors and wheel encoders, 500 kbit/s |
| `canSensorInterface` | `can1` | the articulation sensor alone, 250 kbit/s; empty means not fitted |
| `waistPollHz` | 10 | the articulation sensor answers only when polled |
| `feedbackStaleMs` | 500 | how old a measurement may be and still count as one |
| `commandHz` | 20 | the drives treat a command older than 100 ms as stale and zero the outputs, so this must stay above 10 |
| `safetyStopMs` | 2000 | how long silence is tolerated before stopping |
| `maxWheelRPM` | 120 | full scale, `0x7800` in the controller's units |
| `accelStep` / `brakeStep` | 30 / 100 | ramp rates per cycle |

The bus has to be up before the system starts:

```bash
sudo ip link set can0 up type can bitrate 500000
sudo ip link set can1 up type can bitrate 250000
```

Note the different bitrates. All five motors *and* the wheel encoders are on
`can0`; `can1` carries only the articulation angle sensor.

**Do not run `can_dds` at the same time.** Two writers commanding the same
motors on `can0` is how a vehicle ends up somewhere it was not sent.

## Not here yet

- **A unit for the waist angle.** `waist` publishes the sensor's raw ten-bit
  count with no unit attached, because its scale is not established. The
  reference implementation computes `(raw - 450) / 150` and calls the result
  degrees, but over the sensor's 0–1023 range that spans only about −3 to +3.8,
  which is not degrees for a machine that articulates tens of them. (That
  reference also performs the division in integer arithmetic, so it can only
  ever return −3 … +3, discarding every bit of a ten-bit sensor.)

  Calibrating it is a five-minute job: set the waist to a measured angle, read
  `waist`, repeat at a second angle, solve for zero and scale. Until then a
  number with an unknown unit is worse than no number.
- **Steering to an angle**, which needs that calibration and a loop.
- **Kinematics** — "drive 4 m at 1 km/h" — which is the driver system's job,
  because it needs the wheel size, the two joint-to-axle lengths and the
  articulated model. The measurements are known: L1 = L2 with L1 + L2 =
  1235 mm, track 627.5 mm, and a rolling **circumference** of 1335 mm
  (a diameter of about 425 mm).

## Protocol notes

Motors are Magellan motion-control ICs at `0x600 + node`. Each is brought up
with reset `{00 39}`, a current foldback setting `{00 41 00 00 98 8F}` and
operating mode 3 `{00 65 00 03}`, with the delays the reference implementation
uses. A speed is `{00 77 hi lo}` followed by `{00 1A}` to act on it, where the
16-bit value is RPM x 256.

Wheel encoders report unsolicited on `0x18B`–`0x18E`, one per wheel in node
order: position as a free-running 24-bit little-endian counter in bytes 0–2, and
speed as a signed 16-bit count of edges per 5 ms window in bytes 4–5. There are
4096 counts to an encoder revolution through a 20:1 gearbox, so 81 920 to one
revolution of the wheel. **The two left-hand encoders count backwards** and are
negated on the way in; drop that sign and a vehicle driving straight reads as
one spinning on the spot. The position counter wraps after about 205
revolutions.

The articulation sensor is polled: send `0x700` with no data, and it answers on
`0x701` with a ten-bit value, `((data[0] & 0x03) << 8) | data[1]`.
