package main

import (
	"strings"
	"testing"
)

// A cloud shaped like the cottage: one indoor module whose temperature drives
// two heaters in two rooms, a heater published with nobody watching its power,
// a setpoint anyone may write to any value, and a binding to an outdoor
// temperature that no system provides.
//
// Built here rather than loaded from the real cottage.ttl on purpose. That file
// describes somebody's house — addresses, room names, which rooms have heating
// — and a test fixture in a public repository is a poor place for it.
func cottageShaped() *Cloud {
	indoor := &Service{IRI: "alc:met_Indoor_temperature", Definition: "temperature",
		Name: "IndoorModule/temperature", Subscribable: false, Unit: "unit:DEG_C"}
	power := &Service{IRI: "alc:bee_Kitchen_Power", Definition: "Power",
		Name: "KitchenHeater/Power", Subscribable: true}
	onoff := &Service{IRI: "alc:bee_Kitchen_OnOff", Definition: "OnOff",
		Name: "KitchenHeater/OnOff", Methods: []string{"http://www.w3.org/2011/http-methods#PUT"}}
	setpoint := &Service{IRI: "alc:eth_Kitchen_setpoint", Definition: "setpoint",
		Name: "KitchenHeater/setpoint", Unit: "unit:DEG_C",
		Methods: []string{"http://www.w3.org/2011/http-methods#GET",
			"http://www.w3.org/2011/http-methods#PUT"}}

	met := &Asset{IRI: "alc:met_Indoor", System: "meteorologue", Name: "IndoorModule",
		Mission: "measurement", Location: "Kalkholmen ", Provides: []*Service{indoor}}
	bee := &Asset{IRI: "alc:bee_Kitchen", System: "beekeeper", Name: "KitchenHeater",
		Mission: "actuation", Location: "alc:Kitchen", LocationIsIRI: true,
		Provides: []*Service{power, onoff}}
	kitchen := &Asset{IRI: "alc:eth_Kitchen", System: "ethermostat", Name: "KitchenHeater",
		Mission: "control", Location: "alc:Kitchen", LocationIsIRI: true,
		Provides: []*Service{setpoint},
		Consumes: []*Consumption{
			{IRI: indoor.IRI, Definition: "temperature"},
			{IRI: onoff.IRI, Definition: "OnOff", Mode: "set"},
			// Bound to something nothing provides.
			{IRI: "alc:met_Outdoor_temperature", Definition: "outdoorTemperature"},
		}}
	dining := &Asset{IRI: "alc:eth_Dining", System: "ethermostat", Name: "DiningroomHeater",
		Mission: "control", Location: "alc:Diningroom", LocationIsIRI: true,
		Consumes: []*Consumption{{IRI: indoor.IRI, Definition: "temperature"}}}

	c := &Cloud{Name: "Cottage",
		Assets: []*Asset{met, bee, kitchen, dining},
		Hosts:  map[string][]string{"cottage-pi": {"meteorologue", "beekeeper", "ethermostat"}}}
	c.resolve()
	return c
}

func by(t *testing.T, findings []*Finding, substring string) *Finding {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f.FailureMode, substring) || strings.Contains(f.Item, substring) {
			return f
		}
	}
	return nil
}

// The traversal a spreadsheet makes somebody do by hand: losing one sensor
// opens the loop on two rooms, and the graph already knew which two.
func TestOneSensorLostOpensTwoLoops(t *testing.T) {
	c := cottageShaped()
	indoor := c.Providers("temperature")
	if len(indoor) != 1 {
		t.Fatalf("fixture has %d providers of temperature, want 1", len(indoor))
	}

	reached := c.Downstream(indoor[0])
	if len(reached) != 2 {
		t.Errorf("losing the indoor module reaches %d assets, want both controllers", len(reached))
	}
	names := namesOf(reached)
	for _, want := range []string{"KitchenHeater", "DiningroomHeater"} {
		if !strings.Contains(names, want) {
			t.Errorf("the end effect %q does not mention %s", names, want)
		}
	}
}

// A dependence that resolves to nothing is not a risk, it is a defect present
// now — which is why its occurrence class is model-omission and rated 10.
func TestADanglingDependenceIsFound(t *testing.T) {
	f := by(t, Assess(cottageShaped()), "outdoorTemperature")
	if f == nil {
		t.Fatal("a binding to a service nothing provides was not reported")
	}
	if f.CauseClass != "model-omission" {
		t.Errorf("cause class %q; a missing binding has already happened", f.CauseClass)
	}
	if !strings.Contains(f.Evidence, "afo:consumes") {
		t.Errorf("evidence %q does not point at the triple", f.Evidence)
	}
}

// Published, and consumed by nobody. The graph cannot say whether that is spare
// capacity or an omission, and the finding says so rather than choosing.
func TestAServiceNobodyConsumesIsFound(t *testing.T) {
	f := by(t, Assess(cottageShaped()), "Power")
	if f == nil {
		t.Fatal("a service with no consumer was not reported")
	}
	if f.DetectionClass != "published-not-consumed" {
		t.Errorf("detection class %q", f.DetectionClass)
	}
	if !strings.Contains(f.LocalEffect, "no declared consumer") {
		t.Errorf("local effect %q", f.LocalEffect)
	}
}

// A page for a person to read is documentation. Nothing downstream stops
// working when it is unavailable, so it has no failure mode worth a row — and
// in a cloud that enforces authorization a browser cannot reach it anyway,
// having no client certificate to be named by a policy.
//
// A control deviation is the opposite case and must stay: nothing consuming it
// is exactly the finding, because a loop drifting out of bounds is then noticed
// by nobody.
func TestAPageForAPersonIsNotAFailureMode(t *testing.T) {
	c := cottageShaped()
	page := &Service{IRI: "alc:eth_view", Definition: "view", Name: "KitchenHeater/view",
		Forms: []string{"text/html"}}
	deviation := &Service{IRI: "alc:eth_deviation", Definition: "deviation",
		Name: "KitchenHeater/deviation", Forms: []string{"SignalA_v1a"}}
	for _, a := range c.Assets {
		if a.Name == "KitchenHeater" && a.System == "ethermostat" {
			a.Provides = append(a.Provides, page, deviation)
		}
	}
	c.resolve()

	findings := Assess(c)
	if by(t, findings, "view fails") != nil {
		t.Error("a text/html page was reported as a failure mode")
	}
	if by(t, findings, "deviation fails") == nil {
		t.Error("a control deviation nobody watches was not reported; that is the finding")
	}
}

// A setpoint with a unit, a quantity kind and no bounds is fully described and
// entirely unprotected.
func TestAWritableServiceWithNoRangeIsFound(t *testing.T) {
	f := by(t, Assess(cottageShaped()), "setpoint is written")
	if f == nil {
		t.Fatal("an unbounded writable service was not reported")
	}
	if f.EffectClass != "loss-of-control" {
		t.Errorf("effect class %q", f.EffectClass)
	}

	// And a service that does declare a range is not reported.
	c := cottageShaped()
	for _, a := range c.Assets {
		for _, s := range a.Provides {
			if s.Definition == "setpoint" {
				s.Range = []string{"5", "25"}
			}
		}
	}
	if by(t, Assess(c), "setpoint is written") != nil {
		t.Error("a bounded setpoint was still reported as unbounded")
	}
}

// One sensor, two rooms. Neither loop is faulty and one room is wrong.
func TestASharedSensorAcrossRoomsIsFound(t *testing.T) {
	f := by(t, Assess(cottageShaped()), "One source drives control")
	if f == nil {
		t.Fatal("a sensor driving two locations was not reported")
	}
	if f.EffectClass != "wrong-by-design" {
		t.Errorf("effect class %q", f.EffectClass)
	}
	for _, room := range []string{"Kitchen", "Diningroom"} {
		if !strings.Contains(f.FailureMode, room) {
			t.Errorf("the failure mode %q does not name %s", f.FailureMode, room)
		}
	}
}

// A location literal with a trailing space compares unequal to the same name
// written cleanly, and nothing about reading it reveals that.
func TestAnUntrimmedLocationIsFound(t *testing.T) {
	f := by(t, Assess(cottageShaped()), "whitespace")
	if f == nil {
		t.Fatal("a location literal with trailing whitespace was not reported")
	}
	if f.DetectionClass != "no-validation" {
		t.Errorf("detection class %q", f.DetectionClass)
	}
}

// Every system on one machine: the failure no service redundancy survives.
func TestASingleHostIsFound(t *testing.T) {
	f := by(t, Assess(cottageShaped()), "The host stops")
	if f == nil {
		t.Fatal("a cloud on one host was not reported")
	}
	if f.EffectClass != "total-loss" || f.DetectionClass != "unobservable-from-within" {
		t.Errorf("classes %q / %q", f.EffectClass, f.DetectionClass)
	}

	// Two hosts, no finding: this is an exposure, not a rule.
	c := cottageShaped()
	c.Hosts = map[string][]string{"pi-a": {"meteorologue"}, "pi-b": {"beekeeper", "ethermostat"}}
	if by(t, Assess(c), "The host stops") != nil {
		t.Error("a cloud spread over two hosts was still reported as single-hosted")
	}
}

// Findings are ordered and identified stably, because the useful question about
// this month's assessment is what changed since last month's.
func TestTheSameCloudAssessesIdentically(t *testing.T) {
	first, second := Assess(cottageShaped()), Assess(cottageShaped())
	if len(first) != len(second) {
		t.Fatalf("%d findings then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].FailureMode != second[i].FailureMode {
			t.Errorf("row %d differs between runs: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
}

// Nothing is invented: every finding names the three classes the valuation file
// rates, or it cannot be scored at all.
func TestEveryFindingNamesItsClasses(t *testing.T) {
	for _, f := range Assess(cottageShaped()) {
		if f.EffectClass == "" || f.CauseClass == "" || f.DetectionClass == "" {
			t.Errorf("%s (%s) leaves a class empty: %q / %q / %q",
				f.ID, f.FailureMode, f.EffectClass, f.CauseClass, f.DetectionClass)
		}
		if f.Evidence == "" {
			t.Errorf("%s states a finding with nothing to check it against", f.ID)
		}
	}
}

// Two checks that must not fire, because a row with no action behind it is
// noise, and an FMEA nobody trusts is one nobody reads.
func TestWhatIsNotAFinding(t *testing.T) {
	findings := Assess(cottageShaped())

	// A boolean command has no values between its two, so it cannot be written
	// "outside a permitted range".
	for _, f := range findings {
		if strings.Contains(f.Item, "OnOff") && strings.Contains(f.FailureMode, "outside") {
			t.Errorf("a boolean command was reported as needing a range: %s", f.FailureMode)
		}
	}

	// A consumer in "set" mode drives that service rather than believing it. An
	// instruction cannot be stale the way a reading can.
	for _, f := range findings {
		if strings.Contains(f.Item, "OnOff") && strings.Contains(f.FailureMode, "stale") {
			t.Errorf("a written service was reported as serving a stale value: %s", f.Item)
		}
	}
}
