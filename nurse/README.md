# mbaigo System: Nurse

The Nurse is a system that reacts to signals that exceed a threshold, which triggers a maintenance request.

## Status
As with the other systems, this is a prototype that shows that the mbaigo library can be used with ease.

## Compiling
After cloning the code, initialize the *go.mod* file with ``` go mod init github.com/sdoque/systems/nurse``` before running *go mod tidy*.

The reason the *go.mod* file is not included in the repository is that when developing the mbaigo module, a replace statement needs to be included to point to the development code.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o nurse```.


## Cross compiling/building
The following commands enable one to build for different platforms:
- Intel Mac:  ```GOOS=darwin GOARCH=amd64 go build -o nurse_imac```
- ARM Mac: ```GOOS=darwin GOARCH=arm64 go build -o nurse_amac ```
- Windows 64: ```GOOS=windows GOARCH=amd64 go build -o nurse.exe```
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o nurse_rpi64```
- Linux: ```GOOS=linux GOARCH=amd64 go build -o nurse_linux```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp nurse_rpi64 jan@192.168.1.10:rpiExec/nurse/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) target *rpiExec/nurse/* directory.nurse

