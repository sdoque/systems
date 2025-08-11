# mbaigo systems
## Collections of systems that rely on the mbaigo module

- Service Registrar (asset: SQLite database)
	- alternatively, esr (Ephemeral Service Register)
- Orchestrator (asset:match making algorithm)
- ds18b20 (asset: 1-wire temperature sensor)
- Parallax4 (asset: servomotor)
- Thermostat (asset: PID controller)

## Other systems under development (off dev branch)
- UAClient (asset: OPC UA server)
- Modboss (asset: Modbus slave or server)
- Telegrapher (asset: MQTT broker)
- Weatherman (asset: Davis Vantage Pro2 weatherstation)
- Busdriver (asset: car engine via CAN-bus OBD2)
- Photographer (asset: RPi camera)
- Recorder (asset: USB microphone)


Many of the testing is done with the Raspberry Pi (3, 4, &5) with [GPIO](https://www.raspberrypi.com/documentation/computers/raspberry-pi.html#gpio)

## Default http ports for different systems
- 20100  Certificate Authority
- 20101  Maitre d’ (Authentication)
- 20102  Service Registrar
- 20103  Orchestrator
- 20104  Authorizer
- 20105  Modeler (local cloud semantics with GraphDB)
- 20150  ds18b20 (1-wire sensor)
- 20151  Parallax (PWM)
- 20152  Thermostat
- 20153  Revolutionary (Rev Pi PLC)
- 20154  Levler (Level control)
- 20160  Picam
- 20161  USB microphone 
- 20170  UA client (OPC UA)
- 20171  Modboss (Modbus TCP)
- 20172  Telegrapher (MQTT)
- 20180  Influxer (Influx DB)