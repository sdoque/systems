# MQTT signal generator

## Purpose

This software is designed to simulate an **MQTT publisher**, enabling users to test the **Telegrapher** system in evaluation scenarios—without requiring access to a real device or production environment.

## Usage

Upon startup, the program generates a `config.json` file. This file must be updated with the following information:

- The **address** of the MQTT broker  
- The **username and password** (if authentication is required)  
- The **topic** to which the simulator should publish messages  

**Note**  
Instructions for deploying an open-source MQTT broker (e.g., **Mosquitto**) are available in the **Telegrapher README**. This can be helpful if you need a broker instance for testing purposes.
