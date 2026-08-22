package main

import (
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

// TestASwitchIsRecordedAsABoolean is the regression for a historian that quietly
// kept nothing about the plugs.
//
// The cottage heats through ZigBee plugs, which report SignalB_v1a. This system
// accepted only SignalA_v1a, so a configured OnOff measurement discovered its
// four providers, read each one, and wrote an "unexpected form" line instead of
// a point — once per device per sampling period, for ever.
func TestASwitchIsRecordedAsABoolean(t *testing.T) {
	v, ok := signalValue(&forms.SignalB_v1a{Value: true})
	if !ok {
		t.Fatal("a closed switch was refused")
	}
	on, isBool := v.(bool)
	if !isBool {
		t.Fatalf("a switch recorded as %T; InfluxDB should receive a boolean", v)
	}
	if !on {
		t.Error("a closed switch recorded as false")
	}

	v, ok = signalValue(&forms.SignalB_v1a{Value: false})
	if !ok {
		t.Fatal("an open switch was refused")
	}
	// An open switch is a reading, not a missing one. Recording it as anything
	// other than false — nil, or a skipped point — would make "off" and "not
	// heard from" the same thing in the bucket.
	if off, isBool := v.(bool); !isBool || off {
		t.Errorf("an open switch recorded as %#v; want false", v)
	}
}

// TestNumbersAreUnchanged keeps the switch support from disturbing the readings
// this system was already keeping. A temperature must still arrive as a float,
// or InfluxDB rejects the point on a field-type conflict.
func TestNumbersAreUnchanged(t *testing.T) {
	v, ok := signalValue(&forms.SignalA_v1a{Value: 19.5})
	if !ok {
		t.Fatal("a temperature was refused")
	}
	if f, isFloat := v.(float64); !isFloat || f != 19.5 {
		t.Errorf("a temperature recorded as %#v; want float64 19.5", v)
	}
	// Zero is a real reading — a plug drawing no power — and must not be
	// mistaken for the absence of one.
	v, ok = signalValue(&forms.SignalA_v1a{Value: 0})
	if !ok {
		t.Fatal("a zero reading was refused")
	}
	if f, isFloat := v.(float64); !isFloat || f != 0 {
		t.Errorf("a zero reading recorded as %#v; want float64 0", v)
	}
}

// TestAnUnknownFormIsRefused: silence is the wrong answer, but so is inventing a
// value. A form this system does not understand must be reported, not recorded
// as zero or false — a bucket full of honest-looking zeros is worse than a gap,
// because nothing downstream can tell it from a plug that was off.
func TestAnUnknownFormIsRefused(t *testing.T) {
	if v, ok := signalValue(&forms.ServiceRecord_v1{}); ok {
		t.Errorf("an unrecognized form was recorded as %#v; it should be refused", v)
	}
}

// TestDutyCycleIsStillRecoverable is the cost of storing a boolean, written down
// so it is a known cost rather than a surprise.
//
// The average of a heater's state over a window is its duty cycle — how long it
// actually ran — and that is the figure a price-following controller should be
// judged by. InfluxDB cannot average a boolean field directly. This is the
// mapping a query has to do first, and it is the one in the Cottage dashboard's
// duty-cycle panel.
func TestDutyCycleIsStillRecoverable(t *testing.T) {
	samples := []*forms.SignalB_v1a{{Value: true}, {Value: true}, {Value: false}, {Value: false}}
	var sum float64
	for _, s := range samples {
		v, ok := signalValue(s)
		if !ok {
			t.Fatal("a switch reading was refused")
		}
		on, isBool := v.(bool)
		if !isBool {
			t.Fatalf("expected a boolean, got %T", v)
		}
		if on { // the Flux map(): if r._value then 1.0 else 0.0
			sum++
		}
	}
	if got := sum / float64(len(samples)); got != 0.5 {
		t.Errorf("duty cycle %v; want 0.5", got)
	}
}
