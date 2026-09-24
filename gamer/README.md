# gamer

Drives the Artitrax mini wheel loader by hand, from a game controller, through
the [loader](../loader/) system. The system is the gamer; its asset is the
Gamepad.

It commands the vehicle as any pilot does, with a velocity and a steering
command, and it is the pilot with priority: it can take control from any other
system, it is the only one that can take control after a stop, and it can stop
the vehicle whoever is driving.

It replaces the artitrax `gamepad_controller`, which published motor commands
over DDS to `can_dds`. Here there is no DDS, no ROS, no SDL and no C: the loader
is the vehicle, the gamer reads the controller straight from the Linux joystick
device, and the commands between them are ordinary service calls — discovered,
authorized and, in a secured cloud, encrypted like any other.

> **Not yet run against the vehicle.** Tested in software (the handover and
> stop rules, the stick shaping and the joystick decoding) and started on a
> machine with no controller attached. The first run on the Artitrax should be with it
> on blocks.

## What it does

**The pad drives only when it has control, and control is handed over the way
two pilots hand over an aircraft** — deliberately, and confirmed by the vehicle.
The [loader](../loader/README.md#who-drives) keeps the rules; the pad asks.

| on a PlayStation pad | does |
|---|---|
| **L1 + R1**, held **5 s**, sticks centered | take control |
| **L2 + R2**, held **5 s** | give control back |
| **L1 + L2 + R1 + R2** together | **stop the vehicle, whoever is driving** |
| any face button (✕ ○ △ □) | **stop the vehicle, whoever is driving** |
| right stick, up/down | velocity, ±`maxSpeedMetresPerSecond` (1 m/s), while in control |
| left stick, left/right | steering, while in control — see [Steering](#steering) |

**Taking control.** Hold L1 and R1. After five seconds the pad asks the loader
for control, and drives once the loader grants it — the log says
`gamer: in control`. The pad has priority, so it can take control from any
other system, and it is the only kind of system that can take control after a
stop. With a stick pushed the take waits, and says so, until both sticks are
centered.

**Giving control back.** Hold L2 and R2 for five seconds. The vehicle brakes to
rest and is free for another system — an autonomous driver — to take. That is
how the vehicle is handed to software: a person takes control, then releases it.

**Stopping.** All four shoulder buttons together, or any face button, stop the
vehicle at once, **whether or not this pad is driving**: a person can supervise
an autonomous run with a pad in hand. A stop takes control away from everyone,
including this pad, which has to take control again (five seconds of L1 + R1)
to drive. While the stop is held the pad sends it every cycle, and five more
after it is let go. After a four-button stop, let go of all the shoulder buttons
before holding a pair again — lifting L2 and R2 first does not start a take.

**A pad that does not have control sends nothing**, except stops.

**A lost pad** — unplugged, flat, out of Bluetooth range — **stops the vehicle
if it had control**, and does nothing if it did not: a supervisor's pad dropping
out says nothing about whoever is driving. When it comes back it does not have
control. The system does not exit; it keeps trying to open the device every
second.

**Closing the program while in control is a stop.** Closing it without control
leaves the vehicle alone.

**The pad learns what the loader decided.** A granted take gives it control; a
refused setpoint (`409` — somebody stopped the vehicle or took over) takes
control away, and the log gives the loader's reason. A loader that cannot be
reached is not a loss of control: the pad keeps sending, and the loader's own
half-second silence limit stops a vehicle nobody can reach.

`GET /gamer/Gamepad/engaged` answers `1` while the pad has control.

**The pad cannot show you anything.** No rumble, no light bar: everything it
has to say is in the log. The PlayStation pad's light bar is reachable through
`/sys/class/leds` on Linux and would be the natural place to show "in control";
that is not done yet.

## Steering

Left is positive, as everywhere in the vehicle (ISO 8855): stick left, vehicle
left. There are two ways, set by `steering` in the configuration.

**`effort`**, the default, until the waist sensor is calibrated. The stick says
how hard to push the waist motor, up to `maxSteeringPercent` (50 %), and the
operator closes the loop by eye. The loader's steering guard keeps the waist
inside its limit whatever the stick says — before calibration a small window
either side of straight — and stops the vehicle if the chain or the motor
misbehaves.

**`curvature`**, once the waist is calibrated. The stick asks for a curvature,
up to `maxCurvature` (0.5 /m, a 2 m radius), and the loader steers the waist to
the angle that gives it and sets the four wheels to match. Let go of the stick
and the vehicle straightens, which it does not by effort. Until the waist is
calibrated the loader refuses a curvature with `503`, the gamer logs why, and it
keeps control.

In both, the loader applies the kinematics: on a curve the inner wheels run
slower than the outer, as the geometry says.

## Running it

The controller must appear as `/dev/input/js0`. On Raspberry Pi OS the joystick
driver is built in; pair a Bluetooth pad with `bluetoothctl`, or plug a USB one
in, and check:

```bash
ls -l /dev/input/js*
```

The user running the system needs to be in the `input` group to read it:

```bash
sudo usermod -aG input $USER    # then log out and in again
```

In an authorized cloud, the policy must allow `gamer` to write to the loader's
`velocity`, `curvature`, `setpoint`, `control` and `stop` services, which are
actuation, and the loader's `priority` list must name `gamer` (it does by
default).

## Finding your controller's numbers

The defaults are the numbers the Linux `hid-sony` and `hid-playstation` drivers
should produce for a PlayStation pad (and `xpad` for an Xbox one) — they report
the same kernel codes, and the joystick device numbers them in code order: face
buttons 0–3, L1 and R1 on 4 and 5, L2 and R2 on 6 and 7 (the triggers also
appear as axes 2 and 5; the buttons are what is used), left stick X on axis 0,
right stick Y on axis 4. That is worked out from the
drivers rather than read off a pad, so check it, and expect another controller
to differ. With nothing but coreutils:

```bash
od -An -tx1 -w8 -v /dev/input/js0
```

prints one line per event. Byte 7 (the last) is the button or axis number, and
byte 6 says which: `01` for a button, `02` for an axis (`81` and `82` are the
same, sent once for every control when the device is opened). Press a button or
move a stick and read its number off the end of the line.

## Configuration

Generated on the first run.

| field | default | |
|---|---|---|
| `device` | `/dev/input/js0` | |
| `commandHz` | 20 | how often commands are sent; the loader stops after 0.5 s of silence |
| `maxSpeedMetresPerSecond` | 1.0 | full stick forward or back |
| `steering` | `effort` | `effort` until the waist is calibrated, then `curvature` |
| `maxSteeringPercent` | 50 | full stick by effort |
| `maxCurvature` | 0.5 | full stick by curvature, in 1/m |
| `steeringNodeID` | 5 | the loader's steering motor, for effort |
| `deadZone` | 0.10 | fraction of travel ignored around the center |
| `speedAxis` / `steerAxis` | 4 / 0 | |
| `stopButtons` | 0, 1, 2, 3 | any one stops |
| `stopChord` | 4, 5, 6, 7 | all together stop |
| `takeChord` | 4, 5 | held for `holdSeconds`, takes control |
| `releaseChord` | 6, 7 | held for `holdSeconds`, releases control |
| `holdSeconds` | 5 | |
| `stopRepeats` | 5 | cycles a stop is still sent after the buttons are let go |
| `vehicle` | `{"Model": ["artitrax"]}` | details that pick the loader out of the cloud |

The loader's services are found by their definitions and the `vehicle` details;
the steering motor by effort is found by its `NodeID` as well. A cloud with two
loaders needs the `vehicle` details to tell them apart.

Only the newest value is ever waiting to be sent. If a request is still in
flight when the next cycle comes, the older value is replaced rather than
queued, so a slow network gives a vehicle that follows the stick late, never
one that replays where the stick used to be.
