# mbaigo System: parallax [for Raspberry Pi 5]

The Parallax system is an actuator demonstrator. It is a service provider. It uses a [Parallax servo motor](https://www.parallax.com/package/parallax-standard-servo-downloads/) with a PWM signal. 

The system offers one service per servomotor, *rotation*. It can be read or set (e.g., GET or PUT). The values are in percent of full range.

This version of the system addresses the hardware change from Raspberry Pi 4 to Raspberry Pi 5 where the Raspberry Pi 5 moves the GPIO/PWM hardware off the Broadcom SoC and onto a new I/O chip (RP1), the “old” PWM block many libraries and examples talk to is no longer connected to the 40‑pin header.

The overlay needs to be enabled. One has to edit /boot/firmware/config.txt (Bookworm) and add either:

- default: PWM on GPIO18 (CH2) and GPIO19 (CH3) with ```dtoverlay=pwm-2chan```
- Or, to use GPIO12 (CH0) and GPIO13 (CH1) with ```dtoverlay=pwm-2chan,pin=12,func=4,pin2=13,func2=4```

The Raspberry Pi needs to be rebooted after this change. See [also](https://pypi.org/project/rpi-hardware-pwm/?utm_source=chatgpt.com)

There is also a dependency on the Linux kernel. 
The code here expects pwmchip0 which came with version 6.12.
To check from a terminal, type ```ls -d /sys/class/pwm/pwmchip*```.


## Asset deployment 
	
Connect the servo's data line to GPIO 18 as in [this example](https://randomnerdtutorials.com/raspberry-pi-pwm-python/).

## Compiling
To compile the code, one needs to initialize the *go.mod* file with ``` go mod init github.com/sdoque/systems/parallax``` before running ```go mod tidy```.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt (on a configured Raspberry Pi 5).

It is **important** to start the program from within it own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

## Cross compiling/building
The following commands enable one to build for different platforms:
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o parallax_rpi64 .```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp parallax_rpi64 username@ipAddress:mbaigo/parallax/` where user is the *username* @ the *ipAddress* of the Raspberry Pi with a relative (to the user's home directory) target *mbaigo/parallax/* directory.