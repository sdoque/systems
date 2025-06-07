# mbaigo System: Leveler

The Leveler is a distributed control system (DSC) for a pump station (part of an automatic control course's lab) where the aim is to keep the level of an upper tank constant, where the tank is part of a closed (water) circuit.

It offers three services, *setpoint*, *levelError* and *jitter*. 
The setpoint can be read (e.g., GET) or set (e.g., PUT). 
The error signal is the difference between the setpoint or desired temperature and the current temperature.
It can only be read. 
The jitter is the time it takes to obtain a new temperature reading and setting the new valve position.

The control loop is executed every 5 seconds, and can be configured.

## Compiling
The module needs to be initialize in the terminal or command line interface with the command *go.mod* file with ``` go mod init github.com/sdoque/systems/leveler``` before running *go mod tidy*.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o leveler_imac```, where the ending is used to clarify for which platform the code has been compiled for.


## Cross compiling/building
The following commands enable one to build for different platforms:
- Intel Mac:  ```GOOS=darwin GOARCH=amd64 go build -o leveler_imac ```
- ARM Mac: ```GOOS=darwin GOARCH=arm64 go build -o leveler_amac```
- Windows 64: ```GOOS=windows GOARCH=amd64 go build -o leveler.exe ```
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o leveler_rpi64```
- Linux: ```GOOS=linux GOARCH=amd64 go build -o leveler_linux ```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp leveler_rpi64 pi@192.168.1.9:station/leveler/` where user is the *pi* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) target *station/leveler/* directory.