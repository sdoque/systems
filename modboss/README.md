# mbaigo System: Modboss

## Purpose
This system offers as services specific digital input and outputs (discrete inputs and coils) as well as analog (16 bits) registers holding registers and input registers.

The system was named Modboss because the system has a Modbus client as unit asset and the server on the PLC was called a slave.

## Compiling
To compile the code, one needs to get the mbaigo module
```go get github.com/sdoque/mbaigo```
and initialize the *go.mod* file with ``` github.com/sdoque/systems/modboss``` before running *go mod tidy*.

The reason the *go.mod* file is not included in the repository is that when developing the aigo module, a replace statement needs to be included to point to the development code.

To run the code, one just needs to type in ```go run modbus.go thing.go``` within a terminal or at a command prompt.

It is **important** to start the program from within it own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o modboss_imac```, where the ending is used to clarify for which platform the code is for.


## Cross compiling/building
The following commands enable one to build for different platforms:

- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o modboss_rpi64 modboss.go thing.go```


One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp modboss_rpi64 jan@192.168.1.6:mbaigo/modboss/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) target *mbaigo/modboss/* directory.


---

Installing OpenPLC Runtime on Raspberry Pi 5
Prerequisites
Make sure your Raspberry Pi 5 is running Raspberry Pi OS (64-bit) and is up to date:
```bash
sudo apt update && sudo apt upgrade -y
```

Step 1 — Clone the OpenPLC Runtime Repository
```bash
git clone https://github.com/thiagoralves/OpenPLC_v3.git
cd OpenPLC_v3
```

Step 2 — Run the Installer
```bash
sudo ./install.sh linux
```

This will take 10–20 minutes as it compiles everything from source. It installs all dependencies automatically, including the web server, Modbus libraries, and the PLC runtime engine.


Step 3 — Start the OpenPLC Runtime
Once installation is complete, start the service:
```bash
sudo service openplc start
```

To enable it to start automatically on boot:
```bash
sudo systemctl enable openplc
```

---

### Step 4 — Access the Web Interface

Open a browser (on the Pi or any device on the same network) and navigate to:
```
http://<your-pi-ip-address>:8080
```
```
Username: openplc
Password: openplc
```

You can find your Pi's IP with: ```hostname -I```


Step 5 — Enable Modbus TCP

Log into the web interface.
Go to Settings in the left menu.
Scroll to Slave Devices → find Modbus TCP
Check Enable Modbus TCP Slave
Default port is 502 (you may need sudo privileges for ports below 1024)
Click Save — the runtime will restart automatically


Step 6 — Upload Your First PLC Program

Go to Programs → Upload Program
Upload a .st (Structured Text) or .xml (ladder logic from OpenPLC Editor) file
Click Run to start execution

```
PROGRAM ConveyorControl
VAR
    (* Internal timer for 1-second ticks *)
    SecondTimer     : TON;
    Tick            : BOOL;
END_VAR
VAR
    ConveyorStart   AT %QX0.0 : BOOL;
    ConveyorStop    AT %QX0.1 : BOOL;
    EmergencyStop   AT %QX0.2 : BOOL;
    MotorRunning    AT %IX0.0 : BOOL;
    LimitSwitch     AT %IX0.1 : BOOL;
    OverloadDetect  AT %IX0.2 : BOOL;
    TargetSpeed     AT %QW0   : INT;
    CurrentSpeed    AT %QW1   : INT;
    BatchCounter    AT %QW2   : INT;
    TempSensor2     AT %IW1   : INT;
    VibrationSensor AT %IW2   : INT;
END_VAR

(* --- 1-second pulse using TON timer --- *)
SecondTimer(IN := NOT SecondTimer.Q, PT := T#1000ms);
Tick := SecondTimer.Q;

(* --- Count up registers every second --- *)
IF Tick THEN
    IF BatchCounter >= 9999 THEN
        BatchCounter := 0;
    ELSE
        BatchCounter := BatchCounter + 1;
    END_IF;

    IF TargetSpeed >= 1000 THEN
        TargetSpeed := 0;
    ELSE
        TargetSpeed := TargetSpeed + 10;
    END_IF;

    ConveyorStart := NOT ConveyorStart;
END_IF;

(* --- Safety logic --- *)
IF EmergencyStop THEN
    ConveyorStart := FALSE;
    ConveyorStop  := TRUE;
END_IF;

(* --- Derived outputs --- *)
MotorRunning := ConveyorStart AND NOT ConveyorStop;
CurrentSpeed := TargetSpeed;

END_PROGRAM


CONFIGURATION Config0

  RESOURCE Res0 ON PLC
    TASK Main(INTERVAL := T#20ms, PRIORITY := 0);
    PROGRAM Inst0 WITH Main : ConveyorControl;
  END_RESOURCE

END_CONFIGURATION
```


You can download the OpenPLC Editor on your PC from openplcproject.com to write and compile ladder logic programs.
