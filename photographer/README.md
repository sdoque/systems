# # mbaigo System: Photographer

The photographer system takes pictures using connected camera as a service.
The take picture service stores the file locally and provides a link to get the file.

## Compiling
To compile the code, one needs to initialize the *go.mod* file with ``` go mod init github.com/sdoque/photographer``` before running *go mod tidy*.

The reason the *go.mod* file is not included in the repository is that when developing the mbaigo module, a replace statement needs to be included to point to the development code.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt.

It is **important** to start the program from within it own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o parallax_imac```, where the ending is used to clarify for which platform the code is for.


## Cross compiling/building
The following commands enable one to build for different platforms:
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o photographer_rpi64```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp parallax_rpi64 jan@192.168.1.6:Desktop/photographer/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) target *Desktop/photographer/* directory.photographer