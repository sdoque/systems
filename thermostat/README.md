# mbaigo System: thermostat

The idea of a thermostat is to control the temperature within an enclosure (e.g., the kitchen) by comparing the desired temperature and the actual temperature and then “actuating” a heater.
The thermostat system consumes the services from a temperature system and from a valve system.
It regulates the valve position (assuming a hydronic system) based on the current temperature and its set point.
The thermostat system consumes services from the Service Registrar and the Orchestrator.

It offers three services, *setpoint*, *deviation* and *jitter*. The setpoint can be read (e.g., GET) or set (e.g., PUT). The error signal is the difference between the setpoint or desired temperature and the current temperature. It can only be read. The jitter is the time it takes to obtain a new temperature reading and setting the new valve position.

The control loop is executed every 10 seconds, and can be configured.

## When the loop runs: event-triggered with a periodic fallback

The loop has two clocks, deliberately.

```go
select {
case <-ticker.C:            // the guarantee that it runs at all
        t.processFeedbackLoop()
case <-fresh:               // the reason it runs *now*
        t.processFeedbackLoop()
}
```

The ticker alone is the textbook arrangement: sample every T seconds, whatever
the world is doing. Its weakness is that a sensor plunged into warm water waits
out the rest of the period before anything moves. The second arm removes that —
`fresh` is `Cervice.Updated()`, closed whenever a followed value arrives — so a
change in the process reaches the valve as soon as it is known rather than at
the end of a period that has just begun.

The ticker cannot be dropped in favor of the arrival. A provider that has died
says nothing, and a controller waiting only for news would wait for ever,
holding its last output while the room did as it pleased. Silence must not be
mistaken for steadiness. So: **event-triggered, with a periodic fallback.**

This is not badly-sampled periodic control; it is a recognized design. Åström
and Bernhardsson's comparison of periodic and event-based sampling found
event-based giving lower output variance for the same *average* sampling rate.
The service's `Threshold` is the trigger condition and `components.DefaultWakeFloor`
is the minimum inter-event time that keeps a noisy sensor from triggering
without limit.

### What this costs, and why it is free *here*

The sampling interval is no longer constant. It ranges from the wake floor up to
the full period — and in fact can be shorter still, because the ticker is not
reset when an arrival wakes the loop: an update at 9.9 s runs the loop, and the
tick at 10.0 s runs it again 100 ms later.

That is free for **this** controller and only for this one.
`calculateOutput` is `Kp·e + 50` — proportional with a bias, and no time appears
in it. Sample it whenever you like and it returns the same answer for the same
error.

**A PID controller would not survive the same treatment unexamined.**

| Term | Depends on Δt | What varying Δt does |
|---|---|---|
| P | no | nothing |
| I | ∝ Δt | the effective `Ki` follows the sensor's reporting rate |
| D | ∝ 1/Δt | a short interval amplifies the derivative without bound |

The integral is the quiet one: the common implementation accumulates
`integral += e * Ki` per *sample*, which folds the period into the tuning and is
correct only while the period is fixed. The derivative is the dangerous one, and
the danger is specific to event triggering: an update arrives *because* the value
moved past its threshold, so the numerator `e[k] − e[k−1]` is largest at exactly
the moment the denominator can be smallest.

Separately, a zero-order hold contributes roughly Δt/2 of effective dead time.
Tuning at one second and running at ten inserts about five seconds of delay into
a loop whose phase margin assumed half a second.

### If this system ever gains I and D

Recorded here rather than done, because the present controller does not need it:

1. **Measure Δt** from a monotonic clock every cycle and use it — multiply the
   integral by it, divide the derivative by it. Never assume the configured
   period; that is the assumption the second arm of the `select` invalidates.
2. **Clamp Δt** into something like [0.2 s, 30 s], so a double wake cannot divide
   by nothing and a stall cannot dump a large integral step in one go.
3. **Filter the derivative** (first-order, N ≈ 8–20). Standard practice anyway;
   mandatory once the interval varies.
4. **Reset the ticker on an arrival**, which removes the near-zero interval
   entirely and costs one line.
5. **Anti-windup by back-calculation**, needed regardless because the output
   saturates at 0 and 100 %.
6. Or **decouple the clocks**: let the subscription keep the value fresh and run
   the control law on a fixed tick. Δt is then uniform by construction and
   textbook PID applies unmodified — at the price of the "acts now" property,
   which is the whole point of the second arm. A genuine trade, not a free win.

Note also that the *jitter* service measures how long a cycle took, not how long
since the last one. For a proportional controller that is the right diagnostic.
For PID the quantity that matters is the interval between executions, and there
is no instrument for it yet — worth publishing Δt as its own service, and
watching its distribution for a day, before changing the control law.

## Compiling
To compile the code, one needs to initialize the *go.mod* file with ``` go mod init github.com/sdoque/systems/thermostat``` before running *go mod tidy*.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o thermostat_imac```, where the ending is used to clarify for which platform the code has been compiled for.


## Cross compiling/building
The following commands enable one to build for different platforms:
- Intel Mac:  ```GOOS=darwin GOARCH=amd64 go build -o thermostat_imac ```
- ARM Mac: ```GOOS=darwin GOARCH=arm64 go build -o thermostat_amac ```
- Windows 64: ```GOOS=windows GOARCH=amd64 go build -o thermostat.exe```
- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o thermostat_rpi64```
- Linux: ```GOOS=linux GOARCH=amd64 go build -o thermostat_linux ```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp thermostat_rpi64 username@ipAddress:rpiExec/thermostat/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) target *rpiExec/thermostat/* directory.