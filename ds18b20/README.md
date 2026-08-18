# mbaigo System: ds18b20

## Purpose
This system offers as a service the temperature measured by a 1-wire digital thermometer.

Several sensors can be connected to the same pin, each offering its own temperature service.
For demonstration purposes, a Raspberry Pi is recommended since it has the hardware interface to communicate with these digital thermometers. One needs to only add the serial number of the sensor to the systemconfig.json file and relevant attributes (e.g., location).

The [ds18b20](https://www.analog.com/media/en/technical-documentation/data-sheets/ds18b20.pdf) is a 1-wire sensor (power, ground, and a data line normally pulled high with a resistor). It has a unique name or id. When connected to a Raspberry Pi ([the 1-wire interface needs to be enabled](https://www.waveshare.com/wiki/Raspberry_Pi_Tutorial_Series:_1-Wire_DS18B20_Sensor)), one can access it as a “Unix standard device” (i.e., as a file in ```/sys/bus/w1/devices```). 

The system must be configured at deployment time for each sensor.
This is done by adding the sensor's serial number (e.g., 28-0516d0bfd5ff) to the "unit_assets" array, for example: 
```
   {
         "name": "28-0516d0bfd5ff",
         "mission": "measurement",
         "details": {
            "FunctionalLocation": ["Kitchen"],
            "Unit":         ["<http://qudt.org/vocab/unit/DEG_C>"],
            "QuantityKind": ["<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"]
         }
      }
```
A unit asset block {} needs to be added for each sensor. A comma separates the resource blocks.

## Following the temperature instead of asking for it

The temperature service is subscribable. A consumer may ask for the value once,
as it always could:

```bash
curl -s http://localhost:20150/ds18b20/28-00000f030344/temperature
```

or follow it, on the same address, and be told when it moves:

```bash
curl -s -N -H "Accept: text/event-stream" \
  http://localhost:20150/ds18b20/28-00000f030344/temperature
```

The stream opens with the terms it agreed to, then the current reading, then a
reading whenever the temperature moves past the threshold or the heartbeat
elapses — whichever comes first:

```
event: terms
data: {"heartbeat":30,"threshold":0.1,"unit":"<http://qudt.org/vocab/unit/DEG_C>"}

event: value
data: {"value":21.4,"unit":"<http://qudt.org/vocab/unit/DEG_C>", ...}
```

A subscriber may propose its own terms, `?heartbeat=10&threshold=0.5`, and this
sensor clamps them to what it can honour: no faster than the two seconds between
readings, and no finer than 0.0625 °C, which is the chip's own resolution.
Anything finer would report its noise. What was agreed is in the first event, so
a consumer never has to assume its request was granted.

**A consuming system needs no code for this.** A thermostat calls `GetState` on
its own clock exactly as before; the framework follows the service and answers
from the last reading delivered. If this sensor stops publishing, the value goes
stale within a few heartbeats and the consumer goes back to asking over the
network — slower data, never no data.

| Setting | Value | Why |
|---------|-------|-----|
| `heartbeat` | 30 s | long enough not to chatter, short enough to notice this sensor has stopped |
| `threshold` | 0.1 °C | the sensor's usable resolution; a room moves slowly |
| `fastestHeartbeat` | 2 s | it is only read every two seconds |
| `finestThreshold` | 0.0625 °C | the chip's resolution; below this is noise |

## Compiling
To compile the code, one needs to get the AiGo module
```go get github.com/sdoque/mbaigo```
and initialize the *go.mod* file with ``` go mod init github.com/sdoque/systems/ds18b20``` before running *go mod tidy*.

To run the code, one just needs to type in ```go run ds18b20.go thing.go``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because program looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o ds18b20_imac```, where the ending is used here to clarify for which platform the executable file is for.


## Cross compiling/building
The following commands enable one to build for a different platform:

- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o ds18b20_rpi64 ds18b20.go thing.go```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp ds18b20_rpi64 username@ipAddress:mbaigo/ds18b20/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) destination (the *mbaigo/ds18b20/* directory in this case).