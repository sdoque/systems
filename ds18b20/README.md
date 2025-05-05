# mbaigo System: ds18b20

## Purpose
This system offers as a service the temperature measured by a 1-wire digital thermometer.

## Sequence of events
The Ds18b20 system provides temperature as service to any other system that wants to consume it.
For the service to be discovered, it must be in the registry of currently avaialbe services.
The consume system asks the Orchestrator for the desired service by describing it.
The orchestrator inquires with the Service Registrar if the service is available and if so provides the service URL.
The consuming system will request the service directly from the Ds18B20 system via this URL until it stops working.

```mermaid
sequenceDiagram

participant ServiceRegistrar as ServiceRegistrar
participant Orchestrator as Orchestrator
participant Ds18b20 as Ds18b20
participant ArrowheadSystem as Any Arrowhead System

loop Before registration expiration
    activate Ds18b20
    Ds18b20->>+ServiceRegistrar: Register system
    ServiceRegistrar-->>-Ds18b20: New expiration time
    deactivate Ds18b20
end


loop Every x period
    activate ArrowheadSystem
    
    alt Service location is unknown
        ArrowheadSystem->>+Orchestrator: Discover service provider
        activate Orchestrator
        Orchestrator->>+ServiceRegistrar: Query for service
        ServiceRegistrar-->>-Orchestrator: Return service location
        Orchestrator-->>ArrowheadSystem: Forward service location
        deactivate Orchestrator
    else Service location is known
        Note over ArrowheadSystem: Use cached location
    end

 loop For each wanted service
        ArrowheadSystem->>Ds18b20: Get statistics from service
        activate Ds18b20
        Ds18b20-->>ArrowheadSystem: Return latest data
        deactivate Ds18b20
        ArrowheadSystem->>ArrowheadSystem: Cache sampled data
    end

    deactivate ArrowheadSystem
end
```

## Configuration

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
To compile the code, the directory must include a *go.mod* file which is initialized in a terminal with ``` go mod init github.com/sdoque/systems/ds18b20``` before running *go mod tidy*.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because program looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o ds18b20_imac```, where the ending is used here to clarify for which platform the executable file is for.


## Cross compiling/building
The following commands enable one to build for a different platform:

- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o ds18b20_rpi64```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp ds18b20_rpi64 username@ipAddress:demo/ds18b20/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) destination (the *demo/ds18b20/* directory in this case).