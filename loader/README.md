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

Each provides one service, `setpoint`: **GET** reports what the motor is being
commanded to, **PUT** commands it.

**Steering is an effort, not an angle.** The reference bridge commands the
steering motor exactly as it commands a wheel, and closes the loop to an angle
in software against the sensor on `can1`. That loop is not here yet, so a PUT of
`50` means "push half as hard as you can to the right", not "turn 50 degrees".

## Driving a motor

```bash
# turn the front left wheel at 10 RPM
curl -X PUT http://<host>:20197/loader/FrontLeft/setpoint \
     -H "Content-Type: application/json" \
     -d '{"value": 10, "unit": "RPM", "version": "SignalA_v1a"}'

# ask what it is doing
curl http://<host>:20197/loader/FrontLeft/setpoint

# stop it
curl -X PUT http://<host>:20197/loader/FrontLeft/setpoint \
     -H "Content-Type: application/json" \
     -d '{"value": 0, "unit": "RPM", "version": "SignalA_v1a"}'
```

At 1 km/h a 12 inch wheel turns **17.4 RPM**, so `17.4` is a walking pace and
`120` is the configured maximum.

## Two behaviours worth knowing before it moves

**It stops itself.** If nothing commands the loader for `safetyStopMs`
(2 s by default) every setpoint goes to zero. Every other actuator in this cloud
holds its last state when its controller goes quiet — right for a heater, wrong
for something with wheels. A driver system must therefore keep commanding, which
is also what makes a manoeuvre abortable.

**It ramps.** Commands are rate limited towards the request, `accelStep` per
cycle when speeding up and `brakeStep` when slowing or reversing. Braking gets
the larger step: stopping should never be slower than starting.

## Configuration

Generated on the first run; the defaults drive the vehicle as built.

| field | default | why |
|---|---|---|
| `canInterface` | `can0` | motors and wheel encoders |
| `commandHz` | 20 | the drives treat a command older than 100 ms as stale and zero the outputs, so this must stay above 10 |
| `safetyStopMs` | 2000 | how long silence is tolerated before stopping |
| `maxWheelRPM` | 120 | full scale, `0x7800` in the controller's units |
| `accelStep` / `brakeStep` | 30 / 100 | ramp rates per cycle |

The bus has to be up before the system starts:

```bash
sudo ip link set can0 up type can bitrate 500000
```

**Do not run `can_dds` at the same time.** Two writers commanding the same
motors on `can0` is how a vehicle ends up somewhere it was not sent.

## Not here yet

- **Encoder feedback.** The wheel encoders (`0x18B`–`0x18E`) and the steering
  angle sensor on `can1` are not read, so nothing reports measured speed or
  distance. No service pretends to: there is no measurement service rather than
  one that returns the commanded value dressed as a reading.
- **Steering to an angle**, which needs the `can1` sensor and a loop.
- **Kinematics** — "drive 4 m at 1 km/h" — which is the driver system's job,
  because it needs wheel radius, the two joint-to-axle lengths and the
  articulated model.

## Protocol notes

Motors are Magellan motion-control ICs at `0x600 + node`. Each is brought up
with reset `{00 39}`, a current foldback setting `{00 41 00 00 98 8F}` and
operating mode 3 `{00 65 00 03}`, with the delays the reference implementation
uses. A speed is `{00 77 hi lo}` followed by `{00 1A}` to act on it, where the
16-bit value is RPM x 256.
