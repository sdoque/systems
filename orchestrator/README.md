# mbaigo System: orchestrator

## Purpose
The Orchestrator system is one of the essential core systems of the Arrowhead framework.
When seeking for a service provider, all systems ask the Orchestrator for the URL of a service.

In the current state, the Orchestrator forwards this request to the Service Registrar, who replies with a list of service records of any available service that matches the request (including supported protocols).

The Orchestrator has more responsibilities, such as checking the authorization for a system to consume a specific service from another system. These will be implemented in the future.

## Compiling
To compile the code, one needs to initialize the *go.mod* file with ``` go mod init github.com/sdoque/systems/orchestrator``` before running *go mod tidy*.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o orchestrator_imac```, where the ending is used to clarify for which platform the executable binary is for.


## Cross compiling/building
The following commands enable one to build for different platforms:
- Intel Mac:  ```GOOS=darwin GOARCH=amd64 go build -o orchestrator_imac```
- ARM Mac: ```GOOS=darwin GOARCH=arm64 go build -o orchestrator_amac```
- Windows 64: ```GOOS=windows GOARCH=amd64 go build -o orchestrator.exe```
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o orchestrator_rpi64```
- Linux: ```GOOS=linux GOARCH=amd64 go build -o orchestrator_linux```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp orchestrator_rpi64 username@ipAddress:rpiExec/orchestrator/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) location *rpiExec/orchestrator/* directory.

Additionally, one must ensure that the file is an executable file (e.g., ```chmod +x orchestrator_rpi64```).