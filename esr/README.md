# Ephemeral Service Registry System

The Ephemeral Service Registry (esr) system is an alternative service registrar. It does not use an SQL database but only a simple map of unique ID number associated with a service record.
The service registrar is one of the mandatory core system of an Arrowhead local cloud.
It keeps track of the currently available services within that cloud.

The ephemeral or non persistent aspect of the registry reflects that only the current available service records are kept.
There is no need to permanently keep track of what is currently available.
If such tracking is necessary, it is best suited with the Modeler system with its graph database as asset.

## Compilation
After cloning the *Systems repository*, you will need to go to the *esr* directory in the command line interface or terminal.
There, you will need to initialize the *go.mod* file for dependency tracking and version management (this is done only once).
Type ```go mod init github.com/sdoque/systems/esr```.
As it generates the file, it will tell you to tidy it up with ```go mod tidy```.
If there are dependencies, (which you can list with ```go list -m all```), it will generate a *go.sum* file with the checksum of the downloaded dependencies for integrity verification.
You can then execute the code with ```go run .```.
The first time, the program is ran, it will generate the *systemconfig.json*, which you can update if necessary.
Then restarting the program, the system will be up and running.
It will provide you with the URL of its web server, which you can access with a standard web browser.


## Cross compilation
- Intel Mac: ```GOOS=darwin GOARCH=amd64 go build -o esr_imac``` 
- ARM Mac: ```GOOS=darwin GOARCH=arm64 go build -o esr_amac```
- Windows 64: ```GOOS=windows GOARCH=amd64 go build -o esr_win64.exe```
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o esr_rpi64```
- Linux: ```GOOS=linux GOARCH=amd64 go build -o esr_amd64```

## Testing shutdown
To test the graceful shutdown, one cannot use the IDE debugger but must use the terminal with
```go run .```
Using the IDE debugger will allow one to test device failure, i.e. unplugging the computer.