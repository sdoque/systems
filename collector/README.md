# mbaigo System: Collector

The Collector is a system that as for asset the time series database [InfluxDB](https://en.wikipedia.org/wiki/InfluxDB).

It offers one services, *squery*. squery provides a list of signals present in its bucket’s measurements.

In the configuration file, the specifics to connect to the database have to be entered and which signals are to be recorded and at which sampling rate.

## Status
As with the other systems, this is a prototype that shows that the mbaigo library can be used with ease.

## Compiling
To compile the code, one needs to get the AiGo module
```go get github.com/vanDeventer/mbaigo```
and initialize the *go.mod* file with ``` go mod init github.com/vanDeventer/arrowsys/collector``` before running *go mod tidy*.

The reason the *go.mod* file is not included in the repository is that when developing the mbaigo module, a replace statement needs to be included to point to the development code.

To run the code, one just needs to type in ```go run Collector.go thing.go``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o Collector```.


## Cross compiling/building
The following commands enable one to build for different platforms:
- Intel Mac:  ```GOOS=darwin GOARCH=amd64 go build -o Collector_imac```
- ARM Mac: ```GOOS=darwin GOARCH=arm64 go build -o Collector_amac ```
- Windows 64: ```GOOS=windows GOARCH=amd64 go build -o Collector.exe```
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o Collector_rpi64```
- Linux: ```GOOS=linux GOARCH=amd64 go build -o Collector_linux```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp Collector_rpi64 jan@192.168.1.10:rpiExec/Collector/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) target *rpiExec/Collector/* directory.Collector


## Deployment of the asset
Following https://docs.influxdata.com/influxdb/v2/install/?t=Linux

1. Open your terminal (you’re already using SSH, so you should be connected to your Raspberry Pi for remote installation).

2. **First command** to download the key file:
   ```bash
   curl --silent --location -O https://repos.influxdata.com/influxdata-archive.key
   ```

3. **Second command** to verify the key's checksum:
   ```bash
   echo "943666881a1b8d9b849b74caebf02d3465d6beb716510d86a39f6c8e8dac7515  influxdata-archive.key" | sha256sum --check -
   ```
   Check for a message that says something like "influxdata-archive.key: OK" to ensure the checksum is valid.

4. **Third command** to convert the key and place it in the correct location:
   ```bash
   cat influxdata-archive.key | gpg --dearmor | sudo tee /etc/apt/trusted.gpg.d/influxdata-archive.gpg > /dev/null
   ```

5. **Fourth command** to add the InfluxDB repository:
   ```bash
   echo 'deb [signed-by=/etc/apt/trusted.gpg.d/influxdata-archive.gpg] https://repos.influxdata.com/debian stable main' | sudo tee /etc/apt/sources.list.d/influxdata.list
   ```

6. **Final two commands** to update the package list and install InfluxDB:
   ```bash
   sudo apt-get update
   sudo apt-get install influxdb2
   ```

Make sure to run each command separately, one after the other. If any step gives you an error, stop and troubleshoot that specific issue before proceeding.

Start the InfluxDB service
```sudo service influxdb start```

Check the status of the service ```sudo service influxdb status```

