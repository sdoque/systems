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
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// Define the types of requests the measurement manager can handle
type STray struct {
	Action string
	ValueP chan forms.SignalA_v1a
	Error  chan error
}

// -------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	temperature float64    `json:"-"`
	tStamp      time.Time  `json:"-"`
	trayChan    chan STray `json:"-"`
	name        string     `json:"-"`
	// ctx is the system's, so a request arriving during shutdown is refused
	// rather than sent to a reader that has stopped attending.
	ctx context.Context `json:"-"`
	// unit is the QUDT unit this sensor reports in, taken from the
	// configuration. The chip itself always reports millidegrees Celsius; the
	// reading is converted on the way out, so the unit a consumer sees and the
	// unit registered in the service record are the same by construction rather
	// than by two people remembering to change both.
	unit usecases.UnitDef `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	temperature := components.Service{
		Definition:  "temperature",
		SubPath:     "temperature",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the temperature (GET) of the resource temperature sensor",
	}

	return &components.UnitAsset{
		Name:    "sensor_Id",
		Mission: components.MissionMeasurement,
		Details: map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/DEG_F>"}, "QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"}, "FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{
			temperature.SubPath: &temperature,
		},
		Traits: &Traits{},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		trayChan: make(chan STray),
		name:     configuredAsset.Name,
		ctx:      sys.Ctx,
	}

	declared := ""
	if u := configuredAsset.Details["Unit"]; len(u) > 0 {
		declared = u[0]
	}
	unit, ok := usecases.LookupUnit(declared)
	if !ok {
		// Name the replacement where there is one. A pre-QUDT name resolves
		// through the framework's alias table, so reaching here means the unit
		// is outside the table altogether — and an error that says only "not
		// known" leaves the operator to guess what to write instead.
		if canonical, legacy := usecases.CanonicalUnit(declared); legacy {
			log.Fatalf("%s declares the unit %q; write <%s> instead\n", configuredAsset.Name, declared, canonical)
		}
		log.Fatalf("%s declares the unit %q, which is not a QUDT unit this framework knows. Temperatures are <http://qudt.org/vocab/unit/DEG_C>, <...DEG_F> or <...K>\n", configuredAsset.Name, declared)
	}
	if _, err := usecases.Convert(0, celsius(), unit, false); err != nil {
		log.Fatalf("%s cannot report in %s: %v\n", configuredAsset.Name, declared, err)
	}
	t.unit = unit

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	go t.readTemperature(sys.Ctx)

	return ua, func() {
		log.Printf("disconnecting from %s\n", configuredAsset.Name)
	}
}

//-------------------------------------Service handlers

// readTemp gets the unit asset's temperature datum and sends it in a signal form
func (t *Traits) readTemp(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		getMeasuremet := STray{
			Action: "read",
			ValueP: make(chan forms.SignalA_v1a),
			Error:  make(chan error),
		}
		// Bounded, and abandoned at shutdown. main cancels the context and then
		// sleeps two seconds with the servers still accepting, so a GET in that
		// window used to send on a channel whose reader had already returned —
		// and, while readTemperature still closed it, on a closed channel, which
		// panics and takes the system down on every Ctrl-C that lands badly.
		select {
		case t.trayChan <- getMeasuremet:
		case <-t.shuttingDown():
			http.Error(w, "the system is shutting down", http.StatusServiceUnavailable)
			return
		case <-time.After(5 * time.Second):
			http.Error(w, "the sensor is not answering", http.StatusServiceUnavailable)
			return
		}

		select {
		case err := <-getMeasuremet.Error:
			// A sensor that has not answered yet is not a fault in this system,
			// and 500 would tell a consumer to give up on it. 503 says come
			// back, which is what a control loop should do.
			if errors.Is(err, errNoReading) {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			fmt.Printf("Logic error in getting measurement, %s\n", err)
			w.WriteHeader(http.StatusInternalServerError) // Use 500 for an internal error
			return
		case temperatureForm := <-getMeasuremet.ValueP:
			usecases.HTTPProcessGetRequest(w, r, &temperatureForm)
			return
		case <-time.After(5 * time.Second): // Optional timeout
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Failure to process temperature reading request")
			return
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// shuttingDown reports the system's cancellation channel, or nil when the asset
// was built without a context. Receiving from a nil channel blocks forever, so
// in a select that case simply never fires — which is the right reading of "no
// context, so no shutdown to hear about" and keeps the handler from
// dereferencing one that is not there.
func (t *Traits) shuttingDown() <-chan struct{} {
	if t.ctx == nil {
		return nil
	}
	return t.ctx.Done()
}

// errNoReading says the sensor has produced nothing valid yet, so there is no
// temperature to serve. Distinguished from a fault because a consumer should
// retry rather than give up.
var errNoReading = errors.New("no valid reading yet")

//-------------------------------------Unit asset's functionalities

// readTemperature obtains the temperature from respective ds18b20 resource at regular intervals
// The tray channel is deliberately not closed here. Closing from the receive
// side is what made a request during shutdown a panic rather than a refusal:
// the sender cannot check whether a channel is closed, so the only safe party to
// close it is the one that sends, and nothing needs it closed at all.
func (t *Traits) readTemperature(ctx context.Context) {
	// Create a ticker that triggers every 2 seconds
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop() // Clean up the ticker when done

	tempChan := make(chan float64) // Channel for latest temperature readings
	tStampChan := make(chan time.Time)

	// Start a separate goroutine for temperature reading
	go func() {
		for {
			select {
			case <-ctx.Done(): // Stop when the context is canceled
				return

			case <-ticker.C: // Read temperature at regular intervals
				deviceFile := "/sys/bus/w1/devices/" + t.name + "/w1_slave"
				rawData, err := os.ReadFile(deviceFile)
				if err != nil {
					log.Printf("Error reading temperature file: %s, error: %v\n", deviceFile, err)
					continue // Retry on the next cycle
				}

				celsiusValue, err := parseDeviceFile(rawData)
				if err != nil {
					log.Printf("%s: %v\n", deviceFile, err)
					continue // Retry on the next cycle
				}

				// Send the temperature and timestamp back to the main loop
				select {
				case tempChan <- celsiusValue:
					tStampChan <- time.Now()
				case <-ctx.Done(): // Stop the goroutine if context is canceled
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done(): // Shutdown
			log.Println("Context canceled, stopping temperature readings.")
			return

		case temp := <-tempChan: // Update temperature and timestamp
			t.temperature = temp
			t.tStamp = <-tStampChan

		case order := <-t.trayChan: // Address a GET request
			// Nothing has been read yet, so there is nothing to serve. The zero
			// value is not a reading: a thermostat given 0 C computes an error
			// of twenty and holds the valve wide open, and it cannot tell that
			// number from a cold kitchen.
			//
			// This matters more since the reader started rejecting the 85 C
			// power-on value. A chip that browned out is refused on every tick,
			// so without this the asset would serve 0.000 for the life of the
			// process while its own log said it was discarding bad readings.
			if t.tStamp.IsZero() {
				select {
				case order.Error <- fmt.Errorf("%w from %s", errNoReading, t.name):
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
				continue
			}
			reading, err := usecases.Convert(t.temperature, celsius(), t.unit, false)
			if err != nil {
				// Bounded like the two sends around it. The requesting handler
				// stops waiting after five seconds, and an unbounded send to a
				// channel nobody will read again wedges this loop for the life
				// of the process — every later request then times out against a
				// system that is up and answering nothing. Hard to reach, since
				// startup validates the conversion, but the cost of reaching it
				// is the asset.
				select {
				case order.Error <- err:
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
					log.Printf("%s: no one collected the conversion error; the caller had already given up\n", t.name)
				}
				continue
			}
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = reading
			f.Unit = t.unit.IRI
			f.Timestamp = t.tStamp
			// Bounded, and abandoned at shutdown. The requesting handler stops
			// waiting after five seconds; if this send is still blocked when it
			// does, the loop is stuck on a channel nobody will ever read and
			// every later request times out against a system that is up and
			// answering nothing. Bounding the handoff made this the last
			// unbounded step on that path.
			select {
			case order.ValueP <- f:
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				log.Printf("%s: no one collected the reading; the caller had already given up\n", t.name)
			}
		}
	}
}

// celsius is what the DS18B20 hardware reports: the chip returns millidegrees
// Celsius, which readTemperature divides down. Everything else is a conversion
// away from here.
func celsius() usecases.UnitDef {
	unit, ok := usecases.LookupUnit("http://qudt.org/vocab/unit/DEG_C")
	if !ok {
		panic("the QUDT table has no degree Celsius")
	}
	return unit
}

// parseDeviceFile turns the 1-Wire device file into degrees Celsius.
//
// The file is two lines: a CRC line and a reading. Everything here is a
// guard against hardware this code does not control, and every one of them
// stands for a way the reader goroutine used to panic or lie:
//
//   - A CRC failure or a sensor unplugged mid-read yields one line. Indexing
//     the second one panicked, killing the system, every two seconds.
//   - The DS18B20 reports 85 C as its power-on default, so a chip that reset
//     mid-read hands the control loop a plausible number. The part is
//     specified for -55..125 C; outside that is the sensor failing, not the
//     weather.
func parseDeviceFile(rawData []byte) (float64, error) {
	if len(rawData) == 0 {
		return 0, fmt.Errorf("empty read from the temperature file")
	}

	lines := strings.Split(string(rawData), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("short read from the temperature file: %q", rawData)
	}

	rawValue := lines[1]
	marker := strings.Index(rawValue, "t=")
	if marker < 0 {
		return 0, fmt.Errorf("no temperature in the device file: %q", rawData)
	}

	temp, err := strconv.ParseFloat(strings.TrimSpace(rawValue[marker+len("t="):]), 64)
	if err != nil {
		return 0, fmt.Errorf("parsing the temperature: %w", err)
	}

	if temp < -55000 || temp > 125000 {
		return 0, fmt.Errorf("the reading %.3f C is outside what a DS18B20 can measure", temp/1000)
	}
	// Exactly 85.000 C is the chip's power-on reset value, and it is inside the
	// measurable range, so the range check above cannot catch it. A chip that
	// browned out mid-conversion reports it with a valid CRC, and a control loop
	// cannot tell it from a measurement.
	//
	// This does reject a genuine 85.000 C. That is the trade: the reading is
	// refused and logged, the previous value stands until the next tick two
	// seconds later, and a sensor that really sits at 85 C reports 84.9 or 85.1
	// on almost every other conversion. Silently trusting the reset value is the
	// worse failure — it looks like data.
	if temp == 85000 {
		return 0, fmt.Errorf("85.000 C is the DS18B20 power-on reset value, not a measurement")
	}
	return temp / 1000.0, nil
}
