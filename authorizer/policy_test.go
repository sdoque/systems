package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

// record builds a registration record as the service registrar would hold it.
func record(system, asset, service, mission string, details map[string][]string) forms.ServiceRecord_v1 {
	var r forms.ServiceRecord_v1
	r.NewForm()
	r.SystemName = system
	r.SubPath = asset + "/" + service
	r.ServiceDefinition = service
	r.Mission = mission
	r.Details = details
	return r
}

func at(location string) map[string][]string {
	return map[string][]string{"FunctionalLocation": {location}}
}

// The scenario worked through in POLICY.md, expressed as the engine sees it.
var (
	bathroomSensor   = record("ds18b20", "bathroom-sensor", "temperature", "measurement", at("Bathroom"))
	bathroomHeater   = record("ethermostat", "bathroom-heater", "plug-state", "actuation", at("Bathroom"))
	cloudAggregator  = record("collector", "cloud-aggregator", "mquery", "aggregation", nil)
	thermostatPolicy = Policies{
		Rules: []Rule{
			{
				Subject:            "thermostat-*",
				Missions:           []string{"measurement", "actuation"},
				Actions:            []string{"read", "write"},
				MustMatchAttribute: "FunctionalLocation",
			},
			{
				Subject:  "collector",
				Missions: []string{"measurement", "actuation", "aggregation"},
				Actions:  []string{"read"},
			},
		},
	}
)

// Every row of POLICY.md's resolution table must come out as the document says.
// If the engine and the specification disagree, one of them is wrong and this is
// where it shows.
func TestDecideWorkedExamples(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		attrs   map[string][]string
		action  string
		rec     forms.ServiceRecord_v1
		want    bool
	}{
		{"thermostat-bathroom reads bathroom-sensor", "thermostat-bathroom", at("Bathroom"), ActionRead, bathroomSensor, true},
		{"thermostat-bathroom writes bathroom-heater", "thermostat-bathroom", at("Bathroom"), ActionWrite, bathroomHeater, true},
		{"thermostat-kitchen writes bathroom-heater", "thermostat-kitchen", at("Kitchen"), ActionWrite, bathroomHeater, false},
		{"thermostat-bathroom reads cloud-aggregator", "thermostat-bathroom", at("Bathroom"), ActionRead, cloudAggregator, false},
		{"collector reads bathroom-sensor", "collector", nil, ActionRead, bathroomSensor, true},
		{"collector writes bathroom-heater", "collector", nil, ActionWrite, bathroomHeater, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(thermostatPolicy, Request{
				Subject:           tc.subject,
				SubjectAttributes: tc.attrs,
				Action:            tc.action,
				Record:            tc.rec,
			})
			if got.Allowed != tc.want {
				t.Errorf("Allowed = %v; want %v (%s)", got.Allowed, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Error("a decision must explain itself")
			}
		})
	}
}

// An empty policy set leaves every authenticated system inert. That is the
// fail-closed property the whole design rests on, so it is asserted directly
// rather than left implied by the absence of a matching rule.
func TestDecideDeniesByDefault(t *testing.T) {
	got := Decide(Policies{}, Request{
		Subject: "thermostat-bathroom",
		Action:  ActionRead,
		Record:  bathroomSensor,
	})
	if got.Allowed {
		t.Errorf("an empty policy set authorized a request: %s", got.Reason)
	}
}

// A subject the certificate did not establish is not a subject.
func TestDecideRefusesAnonymousRequests(t *testing.T) {
	permissive := Policies{Rules: []Rule{{
		Subject:  Wildcard,
		Missions: []string{Wildcard},
		Actions:  []string{Wildcard},
	}}}

	got := Decide(permissive, Request{Action: ActionRead, Record: bathroomSensor})
	if got.Allowed {
		t.Error("a request with no subject was authorized even though nothing identified the caller")
	}
	if !strings.Contains(got.Reason, "no subject") {
		t.Errorf("Reason = %q; want it to name the missing subject", got.Reason)
	}
}

// Pairing has four distinct outcomes and each is a deliberate decision in
// POLICY.md rather than a consequence of the others.
func TestAttributePairing(t *testing.T) {
	rule := Policies{Rules: []Rule{{
		Subject:            Wildcard,
		Missions:           []string{Wildcard},
		Actions:            []string{Wildcard},
		MustMatchAttribute: "FunctionalLocation",
	}}}

	tests := []struct {
		name  string
		attrs map[string][]string
		rec   forms.ServiceRecord_v1
		want  bool
	}{
		{"locations agree", at("Bathroom"), bathroomSensor, true},
		{"locations differ", at("Kitchen"), bathroomSensor, false},
		{"an unpaired asset is universally reachable", nil, cloudAggregator, true},
		{"a located asset refuses an unlocated subject", nil, bathroomSensor, false},
		{"one shared value out of several is enough",
			map[string][]string{"FunctionalLocation": {"Kitchen", "Bathroom"}}, bathroomSensor, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(rule, Request{
				Subject:           "someone",
				SubjectAttributes: tc.attrs,
				Action:            ActionRead,
				Record:            tc.rec,
			})
			if got.Allowed != tc.want {
				t.Errorf("Allowed = %v; want %v (%s)", got.Allowed, tc.want, got.Reason)
			}
		})
	}
}

// The service selector is what separates two services of one asset that share a
// mission and an action — a PLC's signal register from its firmware endpoint.
func TestServiceSelector(t *testing.T) {
	signal := record("modboss", "Slider1_Motor_Forward", "signal", "actuation", at("A2307"))
	firmware := record("modboss", "Slider1_Motor_Forward", "firmware", "actuation", at("A2307"))

	scoped := Policies{Rules: []Rule{{
		Subject:  "thermostat",
		Missions: []string{"actuation"},
		Services: []string{"signal"},
		Actions:  []string{"write"},
	}}}

	if got := Decide(scoped, Request{Subject: "thermostat", Action: ActionWrite, Record: signal}); !got.Allowed {
		t.Errorf("the named service was refused: %s", got.Reason)
	}
	if got := Decide(scoped, Request{Subject: "thermostat", Action: ActionWrite, Record: firmware}); got.Allowed {
		t.Error("a service outside the selector was authorized; the selector does not narrow the rule")
	}

	// Omitting the selector must not narrow anything.
	unscoped := Policies{Rules: []Rule{{
		Subject:  "thermostat",
		Missions: []string{"actuation"},
		Actions:  []string{"write"},
	}}}
	if got := Decide(unscoped, Request{Subject: "thermostat", Action: ActionWrite, Record: firmware}); !got.Allowed {
		t.Errorf("an absent service selector narrowed the rule: %s", got.Reason)
	}
}

// A denial overrides any rule that would otherwise allow the request, and it
// applies to one asset rather than to the subject as a whole.
func TestDenialsOverrideRules(t *testing.T) {
	p := thermostatPolicy
	p.Denials = []Denial{{Subject: "thermostat-bathroom", Asset: "ethermostat/bathroom-heater"}}

	blocked := Decide(p, Request{
		Subject: "thermostat-bathroom", SubjectAttributes: at("Bathroom"),
		Action: ActionWrite, Record: bathroomHeater,
	})
	if blocked.Allowed {
		t.Error("a denial did not override a matching policy")
	}

	stillAllowed := Decide(p, Request{
		Subject: "thermostat-bathroom", SubjectAttributes: at("Bathroom"),
		Action: ActionRead, Record: bathroomSensor,
	})
	if !stillAllowed.Allowed {
		t.Errorf("a denial on one asset blocked another: %s", stillAllowed.Reason)
	}
}

// Where several rules authorize the same request, the most cautious lifetime
// applies: revocation latency should not depend on the order rules were written.
func TestShortestMatchingTTLWins(t *testing.T) {
	p := Policies{Rules: []Rule{
		{Subject: Wildcard, Missions: []string{Wildcard}, Actions: []string{Wildcard}, TTL: "30m"},
		{Subject: "thermostat-*", Missions: []string{"measurement"}, Actions: []string{"read"}, TTL: "1m"},
	}}

	got := Decide(p, Request{Subject: "thermostat-bathroom", Action: ActionRead, Record: bathroomSensor})
	if !got.Allowed {
		t.Fatalf("request refused: %s", got.Reason)
	}
	if got.TTL != time.Minute {
		t.Errorf("TTL = %v; want the shortest matching rule's %v", got.TTL, time.Minute)
	}
}

func TestDefaultTTLApplies(t *testing.T) {
	p := Policies{Rules: []Rule{{Subject: Wildcard, Missions: []string{Wildcard}, Actions: []string{Wildcard}}}}
	got := Decide(p, Request{Subject: "anyone", Action: ActionRead, Record: bathroomSensor})
	if got.TTL != DefaultTTL {
		t.Errorf("TTL = %v; want the default %v", got.TTL, DefaultTTL)
	}
}

func TestAssetOf(t *testing.T) {
	if got := AssetOf(bathroomHeater); got != "ethermostat/bathroom-heater" {
		t.Errorf("AssetOf = %q; want %q", got, "ethermostat/bathroom-heater")
	}
	// A record whose SubPath carries no service still identifies its asset.
	bare := forms.ServiceRecord_v1{SystemName: "esr", SubPath: "registry"}
	if got := AssetOf(bare); got != "esr/registry" {
		t.Errorf("AssetOf = %q; want %q", got, "esr/registry")
	}
}

// A misspelt mission or action would silently never match at runtime, which
// reads as a wrong rule rather than a mistyped one. Loading has to catch it.
func TestLoadPoliciesRejectsInvalidFiles(t *testing.T) {
	tests := []struct {
		name        string
		doc         string
		wantInError string
	}{
		{"unknown mission", `{"policies":[{"subject":"a","missions":["not-a-mission"],"actions":["read"]}]}`, "unknown mission"},
		{"unknown action", `{"policies":[{"subject":"a","missions":["measurement"],"actions":["delete"]}]}`, "unknown action"},
		{"unparsable ttl", `{"policies":[{"subject":"a","missions":["measurement"],"actions":["read"],"ttl":"soon"}]}`, "bad ttl"},
		{"no subject", `{"policies":[{"missions":["measurement"],"actions":["read"]}]}`, "no subject"},
		{"no missions", `{"policies":[{"subject":"a","actions":["read"]}]}`, "no missions"},
		{"no actions", `{"policies":[{"subject":"a","missions":["measurement"]}]}`, "no actions"},
		{"denial without an asset", `{"denials":[{"subject":"a"}]}`, "both subject and asset"},
		{"not json", `{`, "parsing policies"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadPolicies([]byte(tc.doc))
			if err == nil {
				t.Fatal("LoadPolicies accepted an invalid document")
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("error %q does not describe the problem (%q)", err, tc.wantInError)
			}
		})
	}
}

func TestLoadPoliciesAcceptsTheWorkedExample(t *testing.T) {
	doc := `{
	  "policies": [
	    {
	      "subject": "thermostat-*",
	      "missions": ["measurement", "actuation"],
	      "actions": ["read", "write"],
	      "must_match_attribute": "FunctionalLocation",
	      "ttl": "10m"
	    },
	    {
	      "subject": "collector",
	      "missions": ["measurement", "actuation", "aggregation"],
	      "actions": ["read"]
	    }
	  ],
	  "denials": [
	    {"subject": "thermostat", "asset": "parallax/basement-servo"}
	  ]
	}`

	p, err := LoadPolicies([]byte(doc))
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	if len(p.Rules) != 2 || len(p.Denials) != 1 {
		t.Fatalf("loaded %d rules and %d denials; want 2 and 1", len(p.Rules), len(p.Denials))
	}

	got := Decide(p, Request{
		Subject: "thermostat-bathroom", SubjectAttributes: at("Bathroom"),
		Action: ActionWrite, Record: bathroomHeater,
	})
	if !got.Allowed {
		t.Errorf("the documented policy refused a request the document allows: %s", got.Reason)
	}
	if got.TTL != 10*time.Minute {
		t.Errorf("TTL = %v; want the rule's 10m", got.TTL)
	}
}

// An empty document is a valid lockdown, distinct from an invalid one.
func TestLoadPoliciesAcceptsAnEmptyDocument(t *testing.T) {
	p, err := LoadPolicies([]byte(`{}`))
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	if got := Decide(p, Request{Subject: "anyone", Action: ActionRead, Record: bathroomSensor}); got.Allowed {
		t.Error("an empty document authorized a request")
	}
}

// readmePolicies extracts the worked example from the README, so the
// documentation is executed rather than merely asserted. A policy file that
// looks right and decides otherwise is worse than none.
func readmePolicies(t *testing.T) Policies {
	t.Helper()
	doc, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(string(doc), -1)
	for _, b := range blocks {
		if !strings.Contains(b[1], `"policies"`) {
			continue
		}
		p, err := LoadPolicies([]byte(b[1]))
		if err != nil {
			t.Fatalf("the README's policy file does not load: %v", err)
		}
		return p
	}
	t.Fatal("no policy example found in README.md")
	return Policies{}
}

// Every row of the README's "What this refuses" table, executed.
func TestReadmeClimateControlExample(t *testing.T) {
	policies := readmePolicies(t)

	kitchen := map[string][]string{"FunctionalLocation": {"Kitchen"}}
	bathroom := map[string][]string{"FunctionalLocation": {"Bathroom"}}

	sensor := record("ds18b20", "sensor_Id", "temperature", "measurement", kitchen)
	valve := record("parallax", "Servo_1", "rotation", "actuation", kitchen)
	newValve := record("parallax", "Servo_2", "rotation", "actuation", kitchen)
	setpoint := record("thermostat", "controller_1", "setpoint", "state", kitchen)

	tests := []struct {
		name    string
		subject string
		attrs   map[string][]string
		action  string
		rec     forms.ServiceRecord_v1
		want    bool
	}{
		{"thermostat reads the sensor", "thermostat", kitchen, ActionRead, sensor, true},
		{"thermostat writes the valve", "thermostat", kitchen, ActionWrite, valve, true},
		{"thermostat writes the sensor", "thermostat", kitchen, ActionWrite, sensor, false},
		{"thermostat writes the uncommissioned valve", "thermostat", kitchen, ActionWrite, newValve, false},
		{"a bathroom thermostat writes the kitchen valve", "thermostat", bathroom, ActionWrite, valve, false},
		{"collector reads the valve", "collector", nil, ActionRead, valve, true},
		{"collector writes the valve", "collector", nil, ActionWrite, valve, false},
		{"collector reads the setpoint", "collector", nil, ActionRead, setpoint, true},
		{"an unlisted system reads the sensor", "kgrapher", nil, ActionRead, sensor, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(policies, Request{
				Subject:           tc.subject,
				SubjectAttributes: tc.attrs,
				Action:            tc.action,
				Record:            tc.rec,
			})
			if got.Allowed != tc.want {
				t.Errorf("Allowed = %v; want %v (%s)", got.Allowed, tc.want, got.Reason)
			}
		})
	}
}

// The documented TTLs are part of the example's meaning: five minutes bounds how
// long a stale permission can drive a valve, fifteen is for a read-only historian.
func TestReadmeExampleTTLs(t *testing.T) {
	policies := readmePolicies(t)
	kitchen := map[string][]string{"FunctionalLocation": {"Kitchen"}}

	control := Decide(policies, Request{
		Subject: "thermostat", SubjectAttributes: kitchen, Action: ActionWrite,
		Record: record("parallax", "Servo_1", "rotation", "actuation", kitchen),
	})
	if control.TTL != 5*time.Minute {
		t.Errorf("the controller's TTL is %v; the README says 5m", control.TTL)
	}

	logging := Decide(policies, Request{
		Subject: "collector", Action: ActionRead,
		Record: record("ds18b20", "sensor_Id", "temperature", "measurement", kitchen),
	})
	if logging.TTL != 15*time.Minute {
		t.Errorf("the collector's TTL is %v; the README says 15m", logging.TTL)
	}
}
