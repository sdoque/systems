# Service Registry System

**NOTE** This system is not currently being maintained, see [issue 9](https://github.com/sdoque/mbaigo/issues/9).

The Service Registry system is one of the mandatory core system of an Arrowhead local cloud.
It keeps track of the currently available services within that cloud.

The records of currently available services are kept in a local database (*serviceregistry.db*) located in the same folder as the capsule (the database being the encapsulated *thing*).
The systemconfiguration.json file is in the same directory.
Both are created upon start up of the system with the difference that the configuration file is not replaced but the database is.

## Cross compile
- Intel Mac: ```GOOS=darwin GOARCH=amd64 go build -o sr_imac serviceregistrar.go thing.go db.go scheduler.go``` 
- ARM Mac: ```GOOS=darwin GOARCH=arm64 go build -o sr_amac serviceregistrar.go thing.go db.go scheduler.go```
- Windows 64: ```GOOS=windows GOARCH=amd64 go build -o sr_win64.exe serviceregistrar.go thing.go db.go scheduler.go```
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o sr_rpi64 serviceregistrar.go thing.go db.go scheduler.go```
- (new) Raspberry Pi 32: ```GOOS=linux GOARCH=arm GOARM=7 go build -o sr_rpi32 serviceregistrar.go thing.go db.go scheduler.go```
- Linux: ```GOOS=linux GOARCH=amd64 go build -o sr_amd64 serviceregistrar.go thing.go db.go scheduler.go```

## Testing shutdown
To test the graceful shutdown, one cannot use the IDE debugger but must use the terminal with
```go run serviceregistrar.go thing.go db.go scheduler.go```
Using the IDE debugger will allow one to test device failure, i.e. unplugging the computer.