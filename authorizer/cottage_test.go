package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The cottage policy file, decided against the cloud running on 192.168.1.109.
//
// A second deployment rather than a variation on the first, and the differences
// are the point. AlphaCloud pairs a thermostat to a room with
// must_match_attribute; this cloud cannot, because its three heaters live in one
// system under one Common Name — the constraint POLICY.md records under "one
// functional location per system". Writing the rule anyway would look stricter
// and enforce nothing, so it is absent and this file says why.
//
// Captured from the registry: meteorologue observes, beekeeper's plugs act,
// ethermostat controls with per-service missions, flattener advises setpoints.
const cottagePolicies = `{
    "policies": [
        {"subject": "ethermostat", "missions": ["measurement"], "actions": ["read"], "ttl": "5m"},
        {"subject": "ethermostat", "missions": ["actuation"], "actions": ["read", "write"], "ttl": "5m"},
        {"subject": "flattener", "missions": ["state"], "actions": ["read", "write"], "ttl": "5m"},
        {"subject": "beehive", "missions": ["actuation"], "actions": ["read", "write"], "ttl": "5m"},
        {"subject": "collector", "missions": ["measurement", "state", "actuation", "aggregation", "control"], "actions": ["read"], "ttl": "15m"},
        {"subject": "envoy", "missions": ["measurement", "state", "actuation", "aggregation", "control"], "actions": ["read"], "ttl": "5m"},
        {"subject": "envoy", "missions": ["core"], "services": ["syslist"], "actions": ["read"], "ttl": "5m"},
        {"subject": "kgrapher", "missions": ["core"], "services": ["syslist"], "actions": ["read"], "ttl": "15m"},
        {"subject": "modeler",  "missions": ["core"], "services": ["syslist"], "actions": ["read"], "ttl": "15m"},
        {"subject": "painter",  "missions": ["core"], "services": ["syslist"], "actions": ["read"], "ttl": "15m"},
        {"subject": "modeler",  "missions": ["aggregation"], "actions": ["read"], "ttl": "15m"},
        {"subject": "painter",  "missions": ["aggregation"], "actions": ["read"], "ttl": "15m"}
    ],
    "denials": []
}`

func cottage(t *testing.T) Policies {
	t.Helper()
	var p Policies
	if err := json.Unmarshal([]byte(cottagePolicies), &p); err != nil {
		t.Fatalf("the policy file does not parse: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("the policy file does not load: %v", err)
	}
	return p
}

// The records below are the ones this cloud actually registered, so a rule that
// reads correctly and denies a running control loop fails here and not at the
// cottage.
var (
	indoorTemp  = record("meteorologue", "IndoorModule", "temperature", "measurement", nil)
	kitchenPlug = record("beekeeper", "KitchenHeater", "OnOff", "actuation", at("Kitchen"))
	kitchenSet  = record("ethermostat", "KitchenHeater", "setpoint", "state", at("Kitchen"))
	deviation   = record("ethermostat", "KitchenHeater", "deviation", "measurement", at("Kitchen"))
	sysList     = record("serviceregistrar", "registry", "syslist", "core", nil)
	registryQ   = record("serviceregistrar", "registry", "query", "core", nil)
	cloudGraph  = record("kgrapher", "assembler", "cloudgraph", "aggregation", nil)
	// price declares no mission of its own, so it inherits the ComfortController's
	// "control" — which is why the historian's rule has to name that mission to
	// record the electricity price driving the whole cloud.
	price = record("flattener", "ComfortController", "price", "control", nil)
)

func TestCottageDecisions(t *testing.T) {
	p := cottage(t)

	cases := []struct {
		name    string
		req     Request
		allowed bool
		ttlMin  float64
	}{{
		"the thermostat reads the room temperature it controls on",
		Request{Subject: "ethermostat", Action: ActionRead, Record: indoorTemp}, true, 5,
	}, {
		"the thermostat switches the heater plug",
		Request{Subject: "ethermostat", Action: ActionWrite, Record: kitchenPlug}, true, 5,
	}, {
		"the thermostat reads the plug back",
		Request{Subject: "ethermostat", Action: ActionRead, Record: kitchenPlug}, true, 5,
	}, {
		// The split into two rules is what makes this false. One rule naming
		// both missions and both actions would allow a controller to write to
		// the sensor it reads.
		"the thermostat cannot write to the sensor",
		Request{Subject: "ethermostat", Action: ActionWrite, Record: indoorTemp}, false, 0,
	}, {
		"the advisor pushes a setpoint",
		Request{Subject: "flattener", Action: ActionWrite, Record: kitchenSet}, true, 5,
	}, {
		// The whole point of an advisor: it moves the target and lets the
		// controller decide how to reach it. Reaching past ethermostat to the
		// plug would make the control loop unaccountable.
		"the advisor cannot drive the plug itself",
		Request{Subject: "flattener", Action: ActionWrite, Record: kitchenPlug}, false, 0,
	}, {
		"the dashboard toggles a plug",
		Request{Subject: "beehive", Action: ActionWrite, Record: kitchenPlug}, true, 5,
	}, {
		"the dashboard cannot move a setpoint",
		Request{Subject: "beehive", Action: ActionWrite, Record: kitchenSet}, false, 0,
	}, {
		"the historian records a measurement",
		Request{Subject: "collector", Action: ActionRead, Record: indoorTemp}, true, 15,
	}, {
		// A setpoint and a control deviation are worth recording precisely
		// because nothing else watches them; the assessor reports that gap.
		"the historian records a setpoint and a deviation",
		Request{Subject: "collector", Action: ActionRead, Record: kitchenSet}, true, 15,
	}, {
		"the historian reads a deviation nobody else watches",
		Request{Subject: "collector", Action: ActionRead, Record: deviation}, true, 15,
	}, {
		"the historian records the electricity price",
		Request{Subject: "collector", Action: ActionRead, Record: price}, true, 15,
	}, {
		"the historian drives nothing",
		Request{Subject: "collector", Action: ActionWrite, Record: kitchenPlug}, false, 0,
	}, {
		// The operator's hand in the cloud. It reads what a person would read
		// and drives nothing — which is the whole reason it can be trusted with
		// a certificate a person could not be given.
		"the envoy reads the assembled graph on an operator's behalf",
		Request{Subject: "envoy", Action: ActionRead, Record: cloudGraph}, true, 5,
	}, {
		"the envoy reads a room temperature",
		Request{Subject: "envoy", Action: ActionRead, Record: indoorTemp}, true, 5,
	}, {
		// The line that makes delegation safe rather than a hole in the model.
		// A read-only tool that could also write would be a way to drive the
		// heating from a laptop with no certificate of its own.
		"the envoy cannot switch a plug",
		Request{Subject: "envoy", Action: ActionWrite, Record: kitchenPlug}, false, 0,
	}, {
		"the envoy cannot move a setpoint",
		Request{Subject: "envoy", Action: ActionWrite, Record: kitchenSet}, false, 0,
	}, {
		"the envoy reads the system list",
		Request{Subject: "envoy", Action: ActionRead, Record: sysList}, true, 5,
	}, {
		// Narrowed like every other aggregator: reading the list of systems is
		// not a reason to be handed the registrar's whole surface.
		"the envoy may not query the registry",
		Request{Subject: "envoy", Action: ActionRead, Record: registryQ}, false, 0,
	}, {
		"the graph builder reads the system list",
		Request{Subject: "kgrapher", Action: ActionRead, Record: sysList}, true, 15,
	}, {
		"the graph builder may not query the registry",
		Request{Subject: "kgrapher", Action: ActionRead, Record: registryQ}, false, 0,
	}, {
		"the painter reads the assembled graph",
		Request{Subject: "painter", Action: ActionRead, Record: cloudGraph}, true, 15,
	}, {
		// Case matters: the subject is a certificate Common Name compared
		// exactly, and a capitalised one matches no rule while looking right.
		"a capitalised subject matches nothing",
		Request{Subject: "Ethermostat", Action: ActionRead, Record: indoorTemp}, false, 0,
	}, {
		"an unenrolled system gets nothing",
		Request{Subject: "intruder", Action: ActionRead, Record: indoorTemp}, false, 0,
	}}

	for _, tc := range cases {
		got := Decide(p, tc.req)
		if got.Allowed != tc.allowed {
			t.Errorf("%s: allowed=%t, want %t (%s)", tc.name, got.Allowed, tc.allowed, got.Reason)
			continue
		}
		if got.Allowed && got.TTL.Minutes() != tc.ttlMin {
			t.Errorf("%s: ttl=%v, want %v minutes", tc.name, got.TTL, tc.ttlMin)
		}
	}
}

// The file deployed to the Pi and the file under test must be the same file.
func TestTheShippedCottageExampleIsWhatIsTested(t *testing.T) {
	onDisk, err := os.ReadFile("policies.cottage.example.json")
	if err != nil {
		t.Fatalf("the shipped example is missing: %v", err)
	}
	var fromDisk, fromTest Policies
	if err := json.Unmarshal(onDisk, &fromDisk); err != nil {
		t.Fatalf("the shipped example does not parse: %v", err)
	}
	if err := json.Unmarshal([]byte(cottagePolicies), &fromTest); err != nil {
		t.Fatalf("the tested policy does not parse: %v", err)
	}
	a, _ := json.Marshal(fromDisk)
	b, _ := json.Marshal(fromTest)
	if string(a) != string(b) {
		t.Errorf("the shipped example has drifted from the tested policy:\n on disk: %s\n tested:  %s",
			strings.TrimSpace(string(a)), strings.TrimSpace(string(b)))
	}
}
