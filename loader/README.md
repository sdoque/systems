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
expands signals, and one for the vehicle as a whole.

| asset | node | commanded in |
|---|---|---|
| `FrontLeft` | 1 | RPM at the wheel |
| `FrontRight` | 2 | RPM at the wheel |
| `BackLeft` | 3 | RPM at the wheel |
| `BackRight` | 4 | RPM at the wheel |
| `Steering` | 5 | percent of full effort |
| `Vehicle` | — | who drives, and whether anyone may |

| service | on | what it is |
|---|---|---|
| `setpoint` | every motor | **GET** what the motor is being *commanded* to; **PUT** commands it |
| `speed` | the four wheels | the RPM the wheel is *measured* to be turning, from its encoder |
| `travel` | the four wheels | revolutions turned since the loader started |
| `waist` | `Steering` | the articulation sensor's raw ten-bit reading |
| `control` | `Vehicle` | **PUT** `1` to take control, `0` to release it; **GET** `1` if you have it |
| `stop` | `Vehicle` | **PUT** `1` to stop the vehicle, whoever is driving; **GET** `1` while stopped |

`speed`, `travel` and `waist` can be followed rather than polled: every encoder
frame (20 per second per wheel) and every waist reply is published to
subscribers. Their timestamps are when the reading arrived, so a follower can
tell a wheel that is standing still from an encoder that has gone quiet.

`setpoint` and `speed` are different numbers and the difference is the point: a
stalled motor, a slipping wheel and a working one all report the same
*setpoint*. Only `speed` says what happened.

**Steering is an effort, not an angle.** The reference bridge commands the
steering motor exactly as it commands a wheel, and closes the loop to an angle
in software against the sensor on `can1`. That loop is not here yet, so a PUT of
`50` means "push half as hard as you can to the right", not "turn 50 degrees".

## Who drives

**Only one system drives at a time, and it has to take control first.** The
rules are those of two pilots sharing an aircraft:

- A system takes control with `PUT control 1` and gives it up with
  `PUT control 0`. Setpoints from anyone else are refused with `409`, and the
  body says who has control.
- **The vehicle starts stopped.** After power-up only a system with priority —
  by default `gamepad`, a person — can take control. Handing the vehicle to
  software is done by taking control and then releasing it.
- A system with priority can take control from anyone, at any time.
- **Anyone can stop the vehicle**, whoever is driving: `PUT stop 1`. The motors
  go to zero at once, skipping the braking ramp, as `can_dds` does on an
  emergency. A stop takes control away from everyone, and **only a system with
  priority can take it back**, so a stop is never undone by the software it
  stopped. Writing `0` to `stop` is refused; a stop is cleared by taking control.
- **If the system in control says nothing for `safetyStopMs` (500 ms), that is a
  stop**, not a handover.
- Taking or releasing control clears the setpoints. The new pilot starts from
  rest, and the vehicle brakes at the normal rate.

Refusals are `409 Conflict`, not `403`: the caller's credentials are fine, it is
the vehicle's state that says no, and a consumer built on this framework reads
`403` as a reason to go and fetch a new token.

**Who is who comes from the caller's certificate.** In a cloud with an
authorizer every request carries one. On a bench with no certificates at all,
callers cannot be told apart, so every caller counts as the same pilot with
priority — `curl` can take control, and control protects nothing; the loader
says so in its log. A loader that *does* hold a certificate lets a caller
without one stop the vehicle and do nothing else.

## Driving a motor

**Take control first**, then **a single PUT will not drive the vehicle**. The command has to be repeated,
at least every `safetyStopMs`, for as long as you want to move — see the next
section for why, and for the arithmetic that says a lone command can never
exceed about 4.7 RPM however large a number you put in it.

```bash
WHEEL=FrontLeft
RPM=25

curl -s -X PUT http://<host>:20197/loader/Vehicle/control \
     -H "Content-Type: application/json" \
     -d '{"value": 1, "version": "SignalA_v1.0"}'

# drive for ten seconds, re-commanding at 5 Hz
end=$((SECONDS+10))
while [ $SECONDS -lt $end ]; do
  curl -s -X PUT http://<host>:20197/loader/$WHEEL/setpoint \
       -H "Content-Type: application/json" \
       -d "{\"value\": $RPM, \"unit\": \"RPM\", \"version\": \"SignalA_v1.0\"}" >/dev/null
  sleep 0.2
done

# then stop commanding, and it stops itself within safetyStopMs;
# or stop it outright:
curl -s -X PUT http://<host>:20197/loader/Vehicle/stop \
     -H "Content-Type: application/json" \
     -d '{"value": 1, "version": "SignalA_v1.0"}' 
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

**It stops itself.** If the system in control says nothing for
`safetyStopMs` (500 ms by default) the vehicle stops, as described above. Every other actuator in this cloud
holds its last state when its controller goes quiet — right for a heater, wrong
for something with wheels. A driver system must therefore keep commanding, which
is also what makes a maneuver abortable.

**It ramps.** Commands are rate limited towards the request, `accelStep` per
cycle when speeding up and `brakeStep` when slowing or reversing. Braking gets
the larger step: stopping should never be slower than starting.

With the defaults that is 150 counts per cycle at 50 Hz — 7 500 counts per
second. One RPM is 256 counts, so the vehicle gains about **29 RPM per second**
and sheds about **98 RPM per second**:

| target | counts | time to reach it |
|---|---|---|
| 10 RPM | 2 560 | 0.3 s |
| 20 RPM | 5 120 | 0.7 s |
| 120 RPM | 30 720 | 4.1 s |
| brake to zero from 120 RPM | 30 720 | 1.2 s (a `stop` is immediate) |

These are the values `can_dds` runs with. Before 16 September 2026 the defaults
were 30 / 100 at 20 Hz — the `MotorController` constructor's defaults rather
than what `can_dds` actually passes it — which took 51 s to reach full speed
and **15 s to stop** from it. A configuration file generated before then still
carries `"commandHz": 20, "accelStep": 30, "brakeStep": 100`; delete those three
lines, or the file, to pick up the new defaults.

**A single PUT still only lasts `safetyStopMs`.** One command is followed by
silence, and after half a second the watchdog stops the vehicle. The command has
to be held for as long as the vehicle should move.

## The lowest speed that actually turns a wheel

Reported from the bench, 8 September 2026: **20 RPM turns the wheels, 10 RPM
does not**, with the command held. The threshold between the two has not been
found.

That was measured under the old, slow ramp, where 10 RPM took 4.3 s of held
commands to reach and 20 RPM took 8.5 s. It is worth repeating with the current
defaults before reading anything into it.

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
| `commandHz` | 50 | the reference's cycle; the drives treat a command older than 100 ms as stale, so this must stay above 10 |
| `safetyStopMs` | 500 | how long the system in control may be silent before the vehicle stops |
| `maxWheelRPM` | 120 | full scale, `0x7800` in the controller's units |
| `accelStep` / `brakeStep` | 150 / 500 | ramp rates per cycle, as `can_dds` sets them |
| `priority` | `["gamepad"]` | systems that may take control from anyone, and after a stop |

A configuration file written before 16 September 2026 lists no `control` or
`stop` service. The loader refuses to start from it and says so: delete
`systemconfig.json` and start again.

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

Wheel encoders are CANopen nodes `0x0B`–`0x0F` and say nothing until started.
At start-up the loader configures each one as `can_dds` does — PDO type 2
(`0x2005 = 2`), a 50 ms cycle (`0x6200 = 50`), position preset to zero
(`0x6003 = 0`) — and sends NMT start (`0x000 {01 node}`). The students found this
step missing (branch `fix-encoder-init`). They then report on `0x18B`–`0x18E`,
one per wheel in node order: position as a free-running 24-bit little-endian counter in bytes 0–2, and
speed as a signed 16-bit count of edges per 5 ms window in bytes 4–5. There are
4096 counts to an encoder revolution through a 20:1 gearbox, so 81 920 to one
revolution of the wheel. **The two left-hand encoders count backwards** and are
negated on the way in; drop that sign and a vehicle driving straight reads as
one spinning on the spot. The position counter wraps after about 205
revolutions.

The articulation sensor is polled: send `0x700` with no data, and it answers on
`0x701` with a ten-bit value, `((data[0] & 0x03) << 8) | data[1]`.
