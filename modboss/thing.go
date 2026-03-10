/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	ServerAddress string              `json:"serverAddress"`
	RegisterMap   map[string][]string `json:"register_map"`
	conn          *net.Conn           `json:"-"`
	IOtype        ioType              `json:"-"`
	Address       string              `json:"-"`
	Rights        string              `json:"-"`
	DataType      string              `json:"-"`
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"assetName"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	Traits
}

// GetName returns the name of the Resource.
func (ua *UnitAsset) GetName() string {
	return ua.Name
}

// GetServices returns the services of the Resource.
func (ua *UnitAsset) GetServices() components.Services {
	return ua.ServicesMap
}

// GetCervices returns the list of consumed services by the Resource.
func (ua *UnitAsset) GetCervices() components.Cervices {
	return ua.CervicesMap
}

// GetDetails returns the details of the Resource.
func (ua *UnitAsset) GetDetails() map[string][]string {
	return ua.Details
}

// GetTraits returns the traits of the Resource.
func (ua *UnitAsset) GetTraits() any {
	return ua.Traits
}

// ensure UnitAsset implements components.UnitAsset (this check is done at during the compilation)
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	access := components.Service{
		Definition:  "signal",
		SubPath:     "access",
		Details:     map[string][]string{"Protocol": {"tcp"}},
		RegPeriod:   30,
		Description: "accesses the Modbus slave's coil, discrete input, holding and input registers to read (GET) the information or write (PUT), ",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "PLC with Modbus slave",
		Details: map[string][]string{"PLC": {"Wago"}, "Location": {"A2307"}},
		Traits: Traits{
			ServerAddress: "192.168.1.6:502",
			RegisterMap: map[string][]string{
				"coil": {
					"00001,ConveyorStart,rw,Boolean",
					"00002,ConveyorStop,rw,Boolean",
					"00003,EmergencyStop,ro,Boolean",
				},
				"discreteInput": { // 100xxx with protocol offset
					"00001,MotorRunning,ro,Boolean",
					"00002,LimitSwitchReached,ro,Boolean",
					"00003,OverloadDetected,ro,Boolean",
				},
				"holdingRegister": { // 400xxx with protocol offset
					"00001,TargetSpeed,rw,16-bit INT",
					"00002,CurrentSpeed,ro,16-bit INT",
					"00003,BatchCounter,rw,16-bit INT",
				},
				"inputRegister": { //3000xx with protocol offset
					"00002,TemperatureSensor2,ro,16-bit INT",
					"00003,VibrationSensor,ro,16-bit INT",
				},
			},
		},

		ServicesMap: components.Services{
			access.SubPath: &access,
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) ([]*UnitAsset, func()) {
	ua := &UnitAsset{
		Name:        configuredAsset.Name,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		CervicesMap: make(map[string]*components.Cervice), // Initialize map
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}

	endpoint := ua.ServerAddress
	fmt.Printf("Trying to connect to server @ %s\n", endpoint)

	// Set a 5-second timeout
	timeout := 5 * time.Second
	slave, err := net.DialTimeout("tcp", endpoint, timeout)
	if err != nil {
		log.Fatalf("Connection error (or timed out after 5 seconds): %v", err)
	}
	fmt.Println("Connected")

	var assetCollection []*UnitAsset
	for kind, gio := range ua.RegisterMap {
		ioKind := typeOfIO(kind) //coil, discrete input, holding register or input register
		for _, str := range gio {
			newAsset := &UnitAsset{} // Create a pointer to UnitAsset
			newAsset.conn = &slave
			newAsset.IOtype = ioKind
			parts := strings.Split(str, ",")
			if len(parts) < 4 {
				log.Fatalf("Bad configuration of %s\n", ioKind)
			}
			newAsset.Address = parts[0]
			newAsset.Name = parts[1]
			newAsset.Rights = parts[2]
			newAsset.DataType = parts[3]
			newAsset.Owner = sys
			// Deep copy ua.Details for each asset
			newDetails := make(map[string][]string)
			for k, v := range ua.Details {
				newDetails[k] = v
			}
			if kind == "coil" || kind == "discreteInput" {
				newDetails["Forms"] = []string{"SignalB_v1a"}
			} else {
				newDetails["Forms"] = []string{"SignalA_v1a"}
			}
			newAsset.Details = newDetails
			newAsset.ServicesMap = usecases.MakeServiceMap(configuredAsset.Services)
			newAsset.ServicesMap["access"].Definition = newAsset.Name
			assetCollection = append(assetCollection, newAsset)
		}
	}

	// Return the unit asset(s) and a cleanup function to close any connection
	return assetCollection, func() {
		fmt.Println("Closing the Modbus TCP connection")
		_ = slave.Close()
	}
}

// UnmarshalTraits unmarshals a slice of json.RawMessage into a slice of Traits.
func UnmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {
	var traitsList []Traits
	for _, raw := range rawTraits {
		var t Traits
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("failed to unmarshal trait: %w", err)
		}
		traitsList = append(traitsList, t)
	}
	return traitsList, nil
}

// -------------------------------------Unit asset's function methods

type ioType int

const (
	Coil ioType = iota
	DiscreteInput
	HoldingRegister
	InputRegister
	numberOfIOtype //to be use as arithmetic counter (IOType + 1) % numberOfIOtype
)

func typeOfIO(nameIO string) ioType {
	var ioMap = map[string]ioType{
		"coil":            Coil,
		"discreteInput":   DiscreteInput,
		"holdingRegister": HoldingRegister,
		"inputRegister":   InputRegister,
	}
	if io, ok := ioMap[nameIO]; ok {
		return io
	}
	log.Fatalf("error with IO type name")
	return -1 // an error has occurred (which does not get executed)
}

// String returns the name of the type IO
func (iot ioType) String() string {
	dayNames := []string{"Coil", "DiscreteInput", "HoldingRegister", "InputRegister"}
	if iot < Coil || iot > InputRegister {
		return "Unknown"
	}
	return dayNames[iot]
}

// Read reads the value of the unit asset
func (ua *UnitAsset) read() (f forms.Form) {
	const unitID uint8 = 1 // Simplified Unit ID
	address, err := strconv.ParseUint(ua.Address, 10, 16)
	if err != nil {
		log.Printf("Invalid address: %v", err)
		return nil
	}

	// Prepare request frame
	request := make([]byte, 12)
	binary.BigEndian.PutUint16(request[0:2], 1) // Transaction ID
	binary.BigEndian.PutUint16(request[2:4], 0) // Protocol ID
	binary.BigEndian.PutUint16(request[4:6], 6) // Length
	request[6] = unitID                         // Unit ID

	// Function code based on IO type
	switch ua.IOtype {
	case Coil:
		request[7] = 1
	case DiscreteInput:
		request[7] = 2
	case HoldingRegister:
		request[7] = 3
	case InputRegister:
		request[7] = 4
	default:
		log.Printf("Unknown IO type: %v", ua.IOtype)
		return nil
	}

	binary.BigEndian.PutUint16(request[8:10], uint16(address))
	binary.BigEndian.PutUint16(request[10:12], 1)

	_, err = (*ua.conn).Write(request)
	if err != nil {
		log.Printf("Failed to send request: %v", err)
		return nil
	}

	fmt.Printf("The request frame is: %+v\n", request)

	// Read response
	response := make([]byte, 256)
	n, err := (*ua.conn).Read(response)
	if err != nil {
		log.Printf("Failed to read response: %v", err)
		return nil
	}
	if n < 9 {
		log.Printf("Response too short (%d bytes)", n)
		return nil
	}

	fmt.Printf("The response frame is: %+v\n", response[:n])

	// Check for Modbus exception (error response)
	if response[7] >= 0x80 {
		exceptionCode := response[8]
		modbusExceptions := map[byte]string{
			0x01: "Illegal Function",
			0x02: "Illegal Data Address",
			0x03: "Illegal Data Value",
			0x04: "Slave Device Failure",
		}
		desc, ok := modbusExceptions[exceptionCode]
		if !ok {
			desc = "Unknown Exception"
		}
		log.Printf("⚠️ Modbus exception for address %s: Function 0x%X, Code 0x%X (%s)", ua.Address, response[7], exceptionCode, desc)
		return nil
	}

	// Parse response
	if ua.IOtype == Coil || ua.IOtype == DiscreteInput {
		status := response[9] & 0x01
		fmt.Println("Binary value:", status)
		var binaryForm forms.SignalB_v1a
		binaryForm.NewForm()
		binaryForm.Value = (status != 0)
		binaryForm.Timestamp = time.Now()
		f = &binaryForm
	} else if ua.IOtype == HoldingRegister || ua.IOtype == InputRegister {
		if n < 11 {
			log.Printf("Incomplete response for register value (only %d bytes)", n)
			return nil
		}
		value := binary.BigEndian.Uint16(response[9:11])
		fmt.Println("Register value:", value)
		var analogueForm forms.SignalA_v1a
		analogueForm.NewForm()
		analogueForm.Value = float64(value)
		analogueForm.Unit = "undefined"
		analogueForm.Timestamp = time.Now()
		f = &analogueForm
	}

	return f
}

// Write writes the value of the unit asset (coil or holding register)
func (ua *UnitAsset) write(value interface{}) error {
	const unitID uint8 = 1 // same as in read()

	address, err := strconv.ParseUint(ua.Address, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid address: %v", err)
	}

	request := make([]byte, 12)
	binary.BigEndian.PutUint16(request[0:2], 1) // Transaction ID
	binary.BigEndian.PutUint16(request[2:4], 0) // Protocol ID
	request[6] = unitID                         // Unit ID

	switch ua.IOtype {
	case Coil:
		// Function Code 5: Write Single Coil
		request[7] = 5
		binary.BigEndian.PutUint16(request[8:10], uint16(address))
		var coilValue uint16
		boolVal, ok := value.(bool)
		if !ok {
			return fmt.Errorf("expected bool for coil write")
		}
		if boolVal {
			coilValue = 0xFF00 // ON
		} else {
			coilValue = 0x0000 // OFF
		}
		binary.BigEndian.PutUint16(request[10:12], coilValue)

	case HoldingRegister:
		// Function Code 6: Write Single Holding Register
		request[7] = 6
		binary.BigEndian.PutUint16(request[8:10], uint16(address))
		var intVal uint16
		switch v := value.(type) {
		case int:
			intVal = uint16(v)
		case float64:
			intVal = uint16(v) // truncate safely
		default:
			return fmt.Errorf("expected int or float64 for register write, got %T", value)
		}

		binary.BigEndian.PutUint16(request[10:12], uint16(intVal))

	default:
		return fmt.Errorf("write not supported for IO type %v", ua.IOtype)
	}

	binary.BigEndian.PutUint16(request[4:6], 6) // Length: always 6 bytes after header

	_, err = (*ua.conn).Write(request)
	if err != nil {
		return fmt.Errorf("failed to send write request: %v", err)
	}

	fmt.Printf("Write request frame: % X\n", request)

	// Read response
	response := make([]byte, 256)
	n, err := (*ua.conn).Read(response)
	if err != nil || n < 12 {
		return fmt.Errorf("failed to read response or response too short: %v", err)
	}

	fmt.Printf("Write response frame: % X\n", response[:n])

	// Check for Modbus exception
	if response[7] >= 0x80 {
		exceptionCode := response[8]
		modbusExceptions := map[byte]string{
			0x01: "Illegal Function",
			0x02: "Illegal Data Address",
			0x03: "Illegal Data Value",
			0x04: "Slave Device Failure",
		}
		desc, ok := modbusExceptions[exceptionCode]
		if !ok {
			desc = "Unknown Exception"
		}
		return fmt.Errorf("modbus exception: Function 0x%X, Code 0x%X (%s)", response[7], exceptionCode, desc)
	}

	return nil
}
