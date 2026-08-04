# mbaigo System: ds18b20F

## Purpose
This system offers as a service the temperature measured by a 1-wire digital
thermometer, reported in **degrees Fahrenheit**.

It is the same driver as [ds18b20](../ds18b20), differing only in the unit its
configuration declares. The DS18B20 chip reports millidegrees Celsius whatever
the configuration says, so the reading is *converted* on the way out rather than
relabelled — a relabelling would be wrong by 32 degrees and look entirely
plausible.

Run whichever of the two suits the deployment. A consumer that asks for a
temperature by quantity kind rather than by unit is paired with either, and
`GetState` converts the reading into the unit that consumer asked for. The
thermostat needs no knowledge of which sensor is running.

```json
"details": {
    "FunctionalLocation": ["Kitchen"],
    "Unit":         ["<http://qudt.org/vocab/unit/DEG_F>"],
    "QuantityKind": ["<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"]
}
```

`QuantityKind` is what the registrar matches on, so a Fahrenheit sensor and a
Celsius consumer can find each other at all; `Unit` is what the reading is
converted from. A unit the framework cannot convert is fatal at startup rather
than a wrong number later.

Several sensors can be connected to the same pin, each offering its own temperature service.
For demonstration purposes, a Raspberry Pi is recommended since it has the hardware interface to communicate with these digital thermometers. One needs to only add the serial number of the sensor to the systemconfig.json file and relevant attributes (e.g., location).

The [ds18b20](https://www.analog.com/media/en/technical-documentation/data-sheets/ds18b20.pdf) is a 1-wire sensor (power, ground, and a data line normally pulled high with a resistor). It has a unique name or id. When connected to a Raspberry Pi ([the 1-wire interface needs to be enabled](https://www.waveshare.com/wiki/Raspberry_Pi_Tutorial_Series:_1-Wire_DS18B20_Sensor)), one can access it as a “Unix standard device” (i.e., as a file in ```/sys/bus/w1/devices```). 

The system must be configured at deployment time for each sensor.
This is done by adding the sensor's serial number (e.g., 28-0516d0bfd5ff) to the "unit_assets" array, for example: 
```
   {
         "name": "28-0516d0bfd5ff",
         "details": {
            "Location": [
               "Kitchen"
            ]
         }
      }
```
A unit asset block {} needs to be added for each sensor. A comma separates the resource blocks.

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