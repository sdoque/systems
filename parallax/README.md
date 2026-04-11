# mbaigo System: Parallax

The Parallax system is an actuator demonstrator. It exposes one `rotation` service per servo motor, which can be read (GET) or set (PUT). Values are expressed as a percentage of the full range (0 = 0°, 100 = 180°).

The system uses a [Parallax standard servo](https://www.parallax.com/package/parallax-standard-servo-downloads/) driven by a 50 Hz PWM signal (pulse widths 620 µs → 2420 µs).

---

## Platform support

The system automatically detects whether it is running on a **Raspberry Pi 5** or a **Raspberry Pi 4** (and earlier) at startup, and selects the appropriate PWM backend:

| Platform | PWM backend | How it works |
|---|---|---|
| Raspberry Pi 5 | RP1 sysfs (`/sys/class/pwm/`) | The Pi 5 moves GPIO/PWM off the Broadcom SoC onto a new I/O chip (RP1). The kernel exposes it through the standard sysfs PWM interface. |
| Raspberry Pi 4 | BCM via go-rpio (`/dev/mem`) | The Pi 4 uses the Broadcom BCM2711 hardware PWM block, accessed directly through memory-mapped registers via the go-rpio library. |

The detection reads `/proc/device-tree/model`. If the file is absent (e.g., on a development machine) the system defaults to the Pi 4 path.

---

## Hardware wiring

Connect the servo's signal wire to **GPIO 18** (physical pin 12, PWM-capable on both platforms). Power the servo from the 5 V rail; do not power it from the 3.3 V rail.

```
Servo red   → Pin 2  (5 V)
Servo brown → Pin 6  (GND)
Servo orange → Pin 12 (GPIO 18)
```

See [this wiring example](https://randomnerdtutorials.com/raspberry-pi-pwm-python/) for reference.

The GPIO pin can be changed in `systemconfig.json` under `traits[0].gpioPin`. Valid PWM-capable pins are **12, 13, 18, and 19** on both platforms.

---

## Setup: Raspberry Pi 5

The RP1 PWM overlay must be enabled before the system will start.

### Step 1 — Enable the PWM overlay

Edit `/boot/firmware/config.txt` and add one of:

```
# GPIO 18 (CH2) and GPIO 19 (CH3) — default
dtoverlay=pwm-2chan

# GPIO 12 (CH0) and GPIO 13 (CH1) — alternative
dtoverlay=pwm-2chan,pin=12,func=4,pin2=13,func2=4
```

### Step 2 — Reboot

```bash
sudo reboot
```

### Step 3 — Verify

```bash
ls /sys/class/pwm/
# expected output: pwmchip0  (or pwmchip2 on some kernel versions)
```

> **Kernel note:** The chip is enumerated as `pwmchip0` from kernel 6.12 onward; earlier kernels may show `pwmchip2`. The system handles both automatically.

---

## Setup: Raspberry Pi 4

No overlay is required. The go-rpio library accesses BCM hardware registers directly through `/dev/mem`, which requires either:

- Running the binary as **root** (`sudo`), or
- Adding the user to the **gpio** group:

```bash
sudo usermod -aG gpio $USER
# log out and back in for the group change to take effect
```

---

## Configuration

Edit `systemconfig.json` to match your setup:

| Field | Description |
|---|---|
| `ipAddresses` | IP addresses of the machine running the system |
| `protocolsNports` → `http` | Port the system listens on (default: 20151) |
| `unit_assets[0].traits[0].gpioPin` | BCM GPIO pin number for the servo signal line (default: 18) |
| `coreSystems` | URLs of the Service Registrar, Orchestrator, CA, and maitreD |

---

## Compiling

Build for the current machine:

```bash
go build -o parallax
```

Cross-compile for Raspberry Pi 4/5 (64-bit):

```bash
GOOS=linux GOARCH=arm64 go build -o parallax_rpi64
```

Copy the binary to the Raspberry Pi:

```bash
scp parallax_rpi64 jan@192.168.1.x:rpiExec/parallax/
```

Run from the system's own directory:

```bash
cd ~/rpiExec/parallax
./parallax_rpi64
```

On first run without a `systemconfig.json`, the system generates one and exits so you can fill in the correct values.

> The system must be started from within its own directory because it reads and writes `systemconfig.json` from the current working directory.
