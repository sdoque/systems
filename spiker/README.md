# mbaigo System: spiker

## Purpose

The spiker reads an inertial sensor on a Raspberry Pi's I2C or SPI bus and
offers what it measures as Arrowhead services. The sensor it knows is the TDK
InvenSense **ICM-20948**, as on SparkFun's 9DoF IMU Breakout (Qwiic): a 3-axis
accelerometer and gyroscope, and an Asahi Kasei AK09916 magnetometer in the
same package.

Like the [ds18b20](../ds18b20) it runs only on the Pi, because the bus is the
Pi's: it reads through Linux's `i2c-dev` and `spidev`. Elsewhere it builds, so
its tests run, and refuses to start.

> **Not yet run on the hardware.** Every register value, bit and scale factor
> is from TDK's datasheet (DS-000189, revision 1.3) and agrees with SparkFun's
> driver, and the tests drive the configuration against a stand-in for the
> chip's registers. The first run on a Pi is still to come.

## Services

| service | form | unit | what it is |
|---|---|---|---|
| `acceleration` | `VectorA_v1a` | m/s² | the specific force: standing level it reads **+9.81 on z**, not zero |
| `angularvelocity` | `VectorA_v1a` | rad/s | the rate of turn about each axis, positive counter-clockwise; a turn to the left is positive z |
| `magneticfield` | `VectorA_v1a` | µT | the magnetic flux density, **uncalibrated** |
| `dietemperature` | `SignalA_v1a` | °C | the temperature of the sensor's die, not of the air around it |

The three vectors are in the **vehicle frame**, ISO 8855 as everywhere on the
Artitrax: x forward, y to the left, z up. `VectorA_v1a` says so in its `frame`,
and a consumer should refuse a frame it does not know.

Each answer is the mean of the samples of the last `averageMs` (100 ms by
default), stamped at the middle of that window. The mean of the angular
velocity over a window is the turn in that window divided by its length, so a
consumer that integrates it loses nothing to the averaging.

**What is not known is not served.** A request answers 503, and a subscriber
hears nothing, when:

- there has been no reading for `staleAfterMs` (a second): a zero acceleration
  is a reading somebody will act on;
- the gyroscope's zero is still being measured (below);
- no magnetometer sample in the window was both fresh and free of overflow.

`dietemperature` is a separate service definition, not `temperature`, so that
the orchestrator never hands a thermostat the IMU chip's own heat for a room's.
What it is good for is the gyroscope: its zero drifts with temperature.

## Following instead of asking

Every service is subscribable:

```bash
curl -s http://<pi>:20158/spiker/imu/acceleration
curl -s -N -H "Accept: text/event-stream" "http://<pi>:20158/spiker/imu/angularvelocity?threshold=0.01"
```

A vector's change is how far it moved, the length of the difference between it
and the vector last sent, so a threshold means for a vector what it means for a
single value. The defaults are a few times the noise of a 100 ms average:

| service | threshold | finest | heartbeat |
|---|---|---|---|
| `acceleration` | 0.05 m/s² | 0.01 | 10 s |
| `angularvelocity` | 0.005 rad/s (0.3°/s) | 0.001 | 10 s |
| `magneticfield` | 0.5 µT | 0.15 (one count) | 10 s |
| `dietemperature` | 0.1 °C | 0.01 | 30 s |

A subscriber hears at most once a window, ten times a second by default.

## The gyroscope's zero: stand still for three seconds

A gyroscope reads something when nothing turns, and integrated that is a
heading that turns by itself. So for the first `gyroZeroSeconds` (three) the
spiker averages the gyroscope, and subtracts that zero from every reading
after. **The vehicle must stand still then.** If the samples spread more than a
still gyroscope's would, it moved: the log says so and the measurement starts
again. Until it succeeds, `angularvelocity` answers 503. With
`gyroZeroSeconds` at 0 the zero is not measured and the rate is served as the
chip reads it.

The zero is measured once, at start. It drifts with temperature, which
`dietemperature` lets you watch; estimating it continuously is not done yet.

## The chip and the buses

The magnetometer is a second die behind the ICM's own I2C master: the host
cannot reach it directly over SPI, and over I2C only in a bypass mode. The
spiker uses the master on both buses. It sets the magnetometer measuring at
100 Hz and has the chip copy its nine bytes into the sample registers itself,
so one burst read of 23 bytes returns all nine axes and the temperature, taken
together. The magnetometer's y and z axes point the other way from the
accelerometer's (datasheet §15, figures 12 and 13); the spiker turns them, so
all three vectors share one frame.

The chip is set to:

- both sensors at 112.5 Hz (1125 Hz / 10), behind a 50 Hz low-pass filter, and
  read every 10 ms, so nothing above 50 Hz folds back into the readings;
- the full scales in the configuration, ±4 g and ±500 °/s by default.

### I2C (Qwiic)

1. Enable I2C: `sudo raspi-config` → Interface Options → I2C, or
   `dtparam=i2c_arm=on` in `config.txt` (`/boot/firmware/config.txt` since
   Bookworm, `/boot/config.txt` before).
   `dtparam=i2c_arm_baudrate=400000` there makes a read take 0.6 ms instead of
   2.5; either fits in the 10 ms between reads.
2. Put the user that runs the spiker in the `i2c` group.
3. Connect the breakout to the Pi's I2C pins (SDA on pin 3, SCL on pin 5, 3.3 V
   and ground) with a Qwiic cable.
4. `i2cdetect -y 1` should show `69`, or `68` with the breakout's address
   jumper closed.

### SPI

1. Enable SPI: `sudo raspi-config` → Interface Options → SPI, or `dtparam=spi=on`.
2. Put the user in the `spi` group.
3. Wire the breakout's SPI pads to the Pi's SPI0 (MOSI, MISO, SCLK and CE0 for
   `/dev/spidev0.0`); SparkFun's hookup guide shows the pads.
4. Set `"bus": "spi"` in the configuration.

Over SPI the chip's I2C interface is switched off (`I2C_IF_DIS`), as the
datasheet asks, so it cannot fall back to I2C mid-run.

## Configuration

The traits of an asset (`imu` by default; one asset per chip):

| trait | default | |
|---|---|---|
| `bus` | `"i2c"` | or `"spi"` |
| `device` | `/dev/i2c-1` | `/dev/spidev0.0` when the bus is SPI and none is given |
| `address` | `"0x69"` | `"0x68"` with the address jumper closed; I2C only |
| `spiSpeedHz` | 1000000 | the chip takes up to 7 MHz; 1 MHz forgives jumper wires |
| `accelRangeG` | 4 | 2, 4, 8 or 16 |
| `gyroRangeDegS` | 500 | 250, 500, 1000 or 2000 |
| `averageMs` | 100 | the window of each reading, and how often a subscriber can hear |
| `gyroZeroSeconds` | 3 | how long it must stand still at start; 0 skips it |
| `staleAfterMs` | 1000 | how old a reading may be and still be served |
| `mount` | all 0 | `rollDegrees`, `pitchDegrees`, `yawDegrees`: how the chip is turned on the vehicle |

**The mount.** All zero is the chip lying flat with its x forward, its axes as
TDK draws them relative to pin 1 (§15, figure 12). Otherwise the three angles
turn the chip's axes into the vehicle's, Rz(yaw)·Ry(pitch)·Rx(roll), as the
[vigie](../vigie) turns its lidar. **Check it with gravity:** with the vehicle
standing level, `acceleration` must read about (0, 0, +9.81). Tip the vehicle
nose down and x goes negative; right side down and y goes positive. The
accelerometer feels the floor pushing up, along whichever axis now leans up. Where on the vehicle the chip sits is not
modeled: an accelerometer away from the center of rotation also feels the
turn.

## Not yet done

- **Calibrating the magnetometer.** Its readings include the hard- and
  soft-iron fields of the vehicle, its motors and its steel, so
  `magneticfield` is not yet a compass.
- **Estimating the gyroscope's zero continuously**, as the vigie's open item
  says too.
- **Recovering a chip that reset.** A read that fails is counted and reported
  every ten seconds; a chip that lost power and came back with its defaults
  is not noticed or reconfigured.
- **The chip's FIFO.** The spiker polls the sample registers every 10 ms. A
  late read loses nothing a 100 ms average would notice, but timing is the
  Pi's, not the chip's.

## Compiling

```bash
go build -o spiker_amac .                        # this machine, for the tests
GOOS=linux GOARCH=arm64 go build -o spiker_rpi64 . # a 64-bit Raspberry Pi OS
GOOS=linux GOARCH=arm go build -o spiker_rpi32 .   # a 32-bit one
```

Run it from its own directory: on the first run it writes `systemconfig.json`
and stops, so the configuration can be edited.
