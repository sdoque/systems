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
// gitignored, like every deployment's. This is the copy under test.
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
        }
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
		// Nothing in this file mentions the aggregation systems, and the default
		// is deny. They are unaffected today only because the registrar does not
		// verify tokens; the moment it does, this is what they get.
		"the painter cannot read the registry",
		Request{Subject: "painter", SubjectAttributes: map[string][]string{}, Action: ActionRead,
			Record: record("serviceregistrar", "registry", "syslist", "core", nil)},
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
		if got.Allowed && got.TTL.Minutes() != 5 {
			t.Errorf("%s: ttl=%v, want the 5 minutes that bounds a stale permission on a valve",
				tc.name, got.TTL)
		}
	}
}

// The file on the Pi and the file under test must be the same file. Kept beside
// the tests rather than in the authorizer's working directory, because that one
// is gitignored and a deployment's policy is not this repository's business —
// but a copy nothing checks is a copy that drifts.
func TestTheShippedExampleIsWhatIsTested(t *testing.T) {
	onDisk, err := os.ReadFile("policies.alphacloud.json")
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
