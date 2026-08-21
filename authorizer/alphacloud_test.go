package main

import (
	"encoding/json"
	"os"
	"testing"
)

// The AlphaCloud policy file, decided against the cloud that is actually
// running on 192.168.1.10.
//
// The worked example in the README is written from the design; this is written
// from the registry. Its records carry the missions and functional locations
// those eleven systems registered on the day it was captured, so a rule that
// looks right on paper and denies the thermostat its own valve fails here
// rather than on the bench.
//
// The file itself lives in the authorizer's working directory on the Pi and is
// gitignored, like every deployment's. This is the copy under test — named
// .example.json so .gitignore's carve-out keeps it, which it did not before.
const alphaCloudPolicies = `{
    "policies": [
        {
            "subject": "thermostat",
            "missions": ["measurement"],
            "actions": ["read"],
            "must_match_attribute": "FunctionalLocation",
            "ttl": "5m"
        },
        {
            "subject": "thermostat",
            "missions": ["actuation"],
            "actions": ["read", "write"],
            "must_match_attribute": "FunctionalLocation",
            "ttl": "5m"
        },
        {"subject": "painter",  "missions": ["core"], "services": ["syslist"], "actions": ["read"], "ttl": "15m"},
        {"subject": "kgrapher", "missions": ["core"], "services": ["syslist"], "actions": ["read"], "ttl": "15m"},
        {"subject": "modeler",  "missions": ["core"], "services": ["syslist"], "actions": ["read"], "ttl": "15m"},
        {"subject": "collector", "missions": ["measurement", "actuation", "state"], "actions": ["read"], "ttl": "15m"}
    ],
    "denials": []
}`

func alphaCloud(t *testing.T) Policies {
	t.Helper()
	var p Policies
	if err := json.Unmarshal([]byte(alphaCloudPolicies), &p); err != nil {
		t.Fatalf("the policy file does not parse: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("the policy file does not load: %v", err)
	}
	return p
}

func TestAlphaCloudDecisions(t *testing.T) {
	p := alphaCloud(t)

	cases := []struct {
		name    string
		req     Request
		allowed bool
	}{{
		"the thermostat reads the temperature it controls on",
		Request{Subject: "thermostat", SubjectAttributes: at("Kitchen"), Action: ActionRead,
			Record: record("ds18b20", "28-00000f030344", "temperature", "measurement", at("Kitchen"))},
		true,
	}, {
		"the thermostat drives the valve",
		Request{Subject: "thermostat", SubjectAttributes: at("Kitchen"), Action: ActionWrite,
			Record: record("parallax", "Servo_1", "rotation", "actuation", at("Kitchen"))},
		true,
	}, {
		"the thermostat reads back the valve position",
		Request{Subject: "thermostat", SubjectAttributes: at("Kitchen"), Action: ActionRead,
			Record: record("parallax", "Servo_1", "rotation", "actuation", at("Kitchen"))},
		true,
	}, {
		// The distinction the two split rules exist to make. One rule listing
		// both missions and both actions would allow this.
		"the thermostat cannot write to the sensor",
		Request{Subject: "thermostat", SubjectAttributes: at("Kitchen"), Action: ActionWrite,
			Record: record("ds18b20", "28-00000f030344", "temperature", "measurement", at("Kitchen"))},
		false,
	}, {
		// The constraint comes from the registry, not the request: a thermostat
		// cannot assert its way into another room.
		"a bathroom thermostat cannot drive the kitchen valve",
		Request{Subject: "thermostat", SubjectAttributes: at("Bathroom"),
			Action: ActionWrite, Record: record("parallax", "Servo_1", "rotation", "actuation", at("Kitchen"))},
		false,
	}, {
		// The aggregation systems read the system list and nothing else. They
		// would work without this — syslist is core-mission and served without a
		// token — but the orchestrator still filters, so without a rule every
		// walk is refused and logged, and a log full of denials that are not
		// denials is how a real one gets missed.
		"the painter may read the system list",
		Request{Subject: "painter", SubjectAttributes: map[string][]string{}, Action: ActionRead,
			Record: record("serviceregistrar", "registry", "syslist", "core", nil)},
		true,
	}, {
		// Narrowed to that one service: reading the list of systems is not a
		// reason to be handed the registrar's whole surface.
		"the painter may not query the registry",
		Request{Subject: "painter", SubjectAttributes: map[string][]string{}, Action: ActionRead,
			Record: record("serviceregistrar", "registry", "query", "core", nil)},
		false,
	}, {
		// And reading is not writing: nothing here lets an aggregator register
		// or unregister anything.
		"the painter may not write to the registry",
		Request{Subject: "painter", SubjectAttributes: map[string][]string{}, Action: ActionWrite,
			Record: record("serviceregistrar", "registry", "syslist", "core", nil)},
		false,
	}, {
		// The historian reads everything and drives nothing. It is also the row
		// that shows why the pairing constraint is per-rule rather than global:
		// a cloud-wide collector is legitimately unpaired, so its rule omits
		// must_match_attribute and it reaches assets in every room.
		"the collector reads a measurement anywhere in the cloud",
		Request{Subject: "collector", SubjectAttributes: map[string][]string{}, Action: ActionRead,
			Record: record("ds18b20", "28-00000f030344", "temperature", "measurement", at("Kitchen"))},
		true,
	}, {
		"the collector may not drive an actuator it is allowed to read",
		Request{Subject: "collector", SubjectAttributes: map[string][]string{}, Action: ActionWrite,
			Record: record("parallax", "Servo_1", "rotation", "actuation", at("Kitchen"))},
		false,
	}, {
		// Case matters, and this is the trap the rename removed: the system used
		// to call itself "Collector", so this rule matched nothing at all while
		// looking exactly right.
		"a capitalised subject does not match a lowercase rule",
		Request{Subject: "Collector", SubjectAttributes: map[string][]string{}, Action: ActionRead,
			Record: record("ds18b20", "28-00000f030344", "temperature", "measurement", at("Kitchen"))},
		false,
	}, {
		"an unknown system gets nothing",
		Request{Subject: "intruder", SubjectAttributes: at("Kitchen"), Action: ActionRead,
			Record: record("ds18b20", "28-00000f030344", "temperature", "measurement", at("Kitchen"))},
		false,
	}}

	for _, tc := range cases {
		got := Decide(p, tc.req)
		if got.Allowed != tc.allowed {
			t.Errorf("%s: allowed=%t, want %t (%s)", tc.name, got.Allowed, tc.allowed, got.Reason)
		}
		// Five minutes bounds how long a stale permission can still drive a
		// valve; fifteen is fine for a system that only reads a list, and costs
		// the orchestrator less traffic.
		wantTTL := 5.0
		if tc.req.Subject != "thermostat" {
			wantTTL = 15
		}
		if got.Allowed && got.TTL.Minutes() != wantTTL {
			t.Errorf("%s: ttl=%v, want %v minutes", tc.name, got.TTL, wantTTL)
		}
	}
}

// The file on the Pi and the file under test must be the same file. Kept beside
// the tests rather than in the authorizer's working directory, because that one
// is gitignored and a deployment's policy is not this repository's business —
// but a copy nothing checks is a copy that drifts.
func TestTheShippedExampleIsWhatIsTested(t *testing.T) {
	onDisk, err := os.ReadFile("policies.alphacloud.example.json")
	if err != nil {
		t.Skipf("no example file to compare: %v", err)
	}
	var a, b Policies
	if err := json.Unmarshal(onDisk, &a); err != nil {
		t.Fatalf("the example file does not parse: %v", err)
	}
	if err := json.Unmarshal([]byte(alphaCloudPolicies), &b); err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Errorf("the example file and the tested policy have drifted apart:\n on disk: %s\n tested:  %s", x, y)
	}
}
