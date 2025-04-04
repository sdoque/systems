# mbaigo System: Telegrapher

The Telegrapher system is built around an MQTT broker.  
It offers the broker’s topics as services, which can be published or subscribed to.  
MQTT is a messaging protocol, not a service-oriented solution.  
Telegrapher transforms MQTT topics into services by extracting path information and interpreting it as metadata describing the service.

---

## 💪 Compiling

To compile this code, ensure you have Go installed and fetch the `mbaigo` module:

```bash
go get github.com/sdoque/mbaigo@latest
```

Then initialize your Go module:

```bash
go mod init github.com/sdoque/arrowsys/telegrapher
go mod tidy
```

> **Note**: The `go.mod` file is not included in this repository. This allows you to use a local development version of the `mbaigo` module by adding a `replace` directive, e.g.:
>
> ```go
> replace github.com/sdoque/mbaigo => ../path/to/local/mbaigo
> ```

To run the code:

```bash
go run telegrapher.go thing.go
```

> **Important**: Run the program from its own directory. Each system should use a dedicated directory because the program reads/writes its configuration file locally.  
If the file is missing, the program will generate a template and shut down, allowing you to edit it.

The system includes a web server for configuration and status monitoring. The server address is printed at startup and can be accessed via a standard web browser.

To build the executable for your current machine:

```bash
go build -o telegrapher_imac
```

(The suffix is optional and simply helps you identify the platform.)

---

## 🚀 Cross-compiling

To build for different platforms:

### Raspberry Pi 64-bit:
```bash
GOOS=linux GOARCH=arm64 go build -o telegrapher_rpi64 telegrapher.go thing.go
```

You can see all available platform targets with:

```bash
go tool dist list
```

To copy the binary to a Raspberry Pi:

```bash
scp telegrapher_rpi64 jan@192.168.1.195:demo/telegrapher/
```

Where:
- `jan` is your Pi's username
- `192.168.1.195` is the Pi's IP address
- `demo/telegrapher/` is the target directory (relative to the user's home directory)

---

## 📦 Deploying the MQTT Broker (Asset)

If you don't have an MQTT broker for testing, you can install the [Eclipse Mosquitto broker](https://mosquitto.org). On a Raspberry Pi or Debian-based system:

```bash
sudo apt update && sudo apt upgrade
sudo apt install -y mosquitto mosquitto-clients
mosquitto -v
```

### 🔁 Basic Publish/Subscribe Test

**Publish**:

```bash
mosquitto_pub -h localhost -t /test/topic -m "Hello from localhost"
```

**Subscribe**:

```bash
mosquitto_sub -h localhost -t /test/topic
```

If you do not have a publisher available, a test publisher is provided in the `mqttGen/` subdirectory of the [source code repository](https://github.com/sdoque/systems/tree/main/telegrapher). It publishes temperature values in a sine wave pattern every second using MQTT.

To run it:

```bash
cd mqttGen
go run mqttGen.go
```

---

## 🔐 Adding Some Security

Edit the Mosquitto configuration file:

```bash
sudo nano /etc/mosquitto/mosquitto.conf
```

Add the following lines:

```conf
listener 1883
allow_anonymous false
password_file /etc/mosquitto/pwdfile
```

### Add Users

Create a password file and prompt for password:

```bash
sudo mosquitto_passwd -c /etc/mosquitto/pwdfile publisher_user
```

Add another user with password on the command line:

```bash
sudo mosquitto_passwd -b /etc/mosquitto/pwdfile subscriber_user subpwd
```

Restart Mosquitto to apply the changes:

```bash
sudo service mosquitto restart
```

### Test Authenticated Subscription

```bash
mosquitto_sub -h localhost -t kitchen/temperature -u subscriber_user -P subpwd
```


Excellent question — and one that matters a lot for actual deployment!

---


### 📡 To Allow Subscribers from Other Computers

Here’s what needs to be true:

#### ✅ 1. **Mosquitto must be listening on an external interface**

Your current config might:

```conf
listener 1883
```

By default, this binds to **all interfaces**, including external ones (e.g., your Wi-Fi IP). So this part is good *unless* it was previously restricted with `bind_address`.

To be sure it listens on all interfaces, don’t specify `bind_address`, or do this:

```conf
listener 1883 0.0.0.0
```

---

#### ✅ 2. **The device's firewall must allow port 1883**

If you're running `ufw` (Uncomplicated Firewall) or `iptables`, make sure port 1883 is open:

```bash
sudo ufw allow 1883/tcp
```

You can check with:

```bash
sudo ufw status
```

---

#### ✅ 3. **Clients must connect using the broker's IP address**

From another computer (on the same network), use:

```bash
mosquitto_sub -h <BROKER_IP_ADDRESS> -t kitchen/temperature -u subscriber_user -P subpwd
```

Replace `<BROKER_IP_ADDRESS>` with the IP of the Raspberry Pi or host running Mosquitto (e.g., `192.168.1.195`).

---

### 🛡️ Bonus: Secure Remote Access

If you're exposing the broker outside your local network (e.g. over the internet), you should:

- Use **port 8883** (MQTT over TLS)
- Set up a certificate with Let's Encrypt or OpenSSL
- Consider firewalling by IP, or using a VPN

---

### ✅ TL;DR

Your current config **supports remote connections**, but only if:

- Mosquitto is listening on `0.0.0.0`
- Port 1883 is open on the host
- Clients use the correct IP