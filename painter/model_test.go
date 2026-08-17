package main

import (
	"strings"
	"testing"
)

// The graphs below are what the framework emits, in its shape and its
// punctuation. They are written out rather than generated so that a change to
// the emitter shows up here as a failing test rather than as a picture that
// quietly loses a line — the painter reads another program's output, and the
// only honest way to test a reader is against what the writer actually writes.
const beekeeperGraph = `@prefix alc: <http://www.synecdoque.com/lcloud/> .
@prefix afo: <http://www.synecdoque.com/2025/afo#> .

alc:home_beekeeper a afo:System ;
    afo:hasName "beekeeper" ;
    afo:hasHusk alc:home_beekeeper_Husk ;
    afo:hasUnitAsset alc:home_beekeeper_KitchenHeater ;
    afo:hasSecurityPosture alc:home_beekeeper_Security ;
    afo:isContainedIn alc:AlphaCloud .

alc:home_beekeeper_Husk a afo:Husk ;
    afo:runsOnHost alc:home .

alc:home a afo:Host ;
    afo:hasName "home" ;
    afo:hasIPAddress "192.168.1.109" .

alc:home_beekeeper_Security a afo:SecurityPosture ;
    afo:hasSecurityLevel "identified" ;
    afo:namesCertificateAuthority "true"^^xsd:boolean ;
    afo:namesAuthorizer "false"^^xsd:boolean ;
    afo:offersTLS "true"^^xsd:boolean ;
    afo:acceptsPlaintext "true"^^xsd:boolean .

alc:home_beekeeper_KitchenHeater a afo:UnitAsset ;
    afo:hasName "KitchenHeater" ;
    afo:hasMission "actuation" ;
    afo:providesService alc:home_beekeeper_KitchenHeater_OnOff .

alc:home_beekeeper_KitchenHeater_OnOff a afo:Service ;
    afo:hasServiceDefinition "OnOff" ;
    afo:hasMission "actuation" ;
    afo:hasUrl <https://192.168.1.109:30185/beekeeper/KitchenHeater/on_off> .
`

const ethermostatGraph = `@prefix alc: <http://www.synecdoque.com/lcloud/> .
@prefix afo: <http://www.synecdoque.com/2025/afo#> .

alc:home_ethermostat a afo:System ;
    afo:hasName "ethermostat" ;
    afo:hasHusk alc:home_ethermostat_Husk ;
    afo:hasUnitAsset alc:home_ethermostat_KitchenHeater ;
    afo:hasSecurityPosture alc:home_ethermostat_Security .

alc:home_ethermostat_Husk a afo:Husk ;
    afo:runsOnHost alc:home .

alc:home_ethermostat_Security a afo:SecurityPosture ;
    afo:hasSecurityLevel "authorized" ;
    afo:verifiesTokens "true"^^xsd:boolean .

alc:home_ethermostat_KitchenHeater a afo:UnitAsset ;
    afo:hasName "KitchenHeater" ;
    afo:hasMission "control" ;
    afo:consumesService alc:home_ethermostat_KitchenHeater_OnOff ;
    afo:consumesService alc:home_ethermostat_KitchenHeater_temperature .

alc:home_ethermostat_KitchenHeater_OnOff a afo:ConsumedService ;
    afo:consumes "OnOff" ;
    afo:consumes alc:home_beekeeper ;
    alc:hasMode "set" ;
    alc:fromUrl <https://192.168.1.109:30185/beekeeper/KitchenHeater/on_off> .

alc:home_ethermostat_KitchenHeater_temperature a afo:ConsumedService ;
    afo:consumes "temperature" ;
    alc:hasMode "get" .
`

func cloudOfTwo(t *testing.T) *Cloud {
	t.Helper()
	return build("AlphaCloud", map[string]string{
		"beekeeper":   beekeeperGraph,
		"ethermostat": ethermostatGraph,
	})
}

// The shape an operator sees: one host, the systems on it, the assets in them.
func TestTheCloudIsDrawnFromWhatEachSystemSaysAboutItself(t *testing.T) {
	cloud := cloudOfTwo(t)

	if len(cloud.Hosts) != 1 {
		t.Fatalf("got %d hosts; two systems on one machine are one host", len(cloud.Hosts))
	}
	host := cloud.Hosts[0]
	if host.Name != "home" {
		t.Errorf("host name %q; the systems name their host through their husk", host.Name)
	}
	if len(host.Systems) != 2 {
		t.Fatalf("got %d systems on the host, want 2", len(host.Systems))
	}
	// Sorted, so the same cloud draws the same way every time. An operator
	// learning a plant should not have to find things afresh on each load.
	if host.Systems[0].Name != "beekeeper" || host.Systems[1].Name != "ethermostat" {
		t.Errorf("systems are in %q, %q; the order must not depend on which answered first",
			host.Systems[0].Name, host.Systems[1].Name)
	}
}

// Colour carries the posture, so the posture has to survive the reading.
func TestEachSystemKeepsItsSecurityPosture(t *testing.T) {
	cloud := cloudOfTwo(t)
	levels := map[string]string{}
	for _, s := range cloud.Hosts[0].Systems {
		levels[s.Name] = s.Level
	}
	if levels["beekeeper"] != "identified" {
		t.Errorf("beekeeper is %q, want identified", levels["beekeeper"])
	}
	if levels["ethermostat"] != "authorized" {
		t.Errorf("ethermostat is %q, want authorized", levels["ethermostat"])
	}

	for _, s := range cloud.Hosts[0].Systems {
		if s.Name != "beekeeper" {
			continue
		}
		if !contains(s.Posture, "acceptsPlaintext") {
			t.Error("a system still answering plaintext does not say so, which is the " +
				"one thing an operator most needs to see about a cloud that looks secure")
		}
	}
}

// The line the whole picture is for: who depends on whom.
func TestAConsumedServiceBecomesALineBetweenTwoAssets(t *testing.T) {
	cloud := cloudOfTwo(t)

	if len(cloud.Links) != 1 {
		t.Fatalf("got %d links, want 1: the thermostat drives the plug and nothing else "+
			"is connected", len(cloud.Links))
	}
	link := cloud.Links[0]
	if link.From != "home/ethermostat/KitchenHeater" {
		t.Errorf("the line starts at %q; it should start at the consumer", link.From)
	}
	if link.To != "home/beekeeper/KitchenHeater" {
		t.Errorf("the line ends at %q; the provider is found by matching the URL the "+
			"consumer was bound to against the URL the provider published", link.To)
	}
	if link.Definition != "OnOff" {
		t.Errorf("the line is labelled %q, want OnOff", link.Definition)
	}
	// Mission decides how the line is drawn: driving a plug must not look like
	// reading a sensor.
	if link.Mission != "actuation" {
		t.Errorf("the line's mission is %q; it is taken from the service at the "+
			"providing end, which is what says whether this line acts or observes",
			link.Mission)
	}
}

// The flashing dot: an asset reaching for something nobody provides.
func TestAnUnsatisfiedRequestIsVisible(t *testing.T) {
	cloud := cloudOfTwo(t)

	var wants []*Want
	for _, s := range cloud.Hosts[0].Systems {
		if s.Name == "ethermostat" {
			wants = s.Assets[0].Wants
		}
	}
	if len(wants) != 2 {
		t.Fatalf("got %d wants, want 2", len(wants))
	}

	byDefinition := map[string]*Want{}
	for _, w := range wants {
		byDefinition[w.Definition] = w
	}
	if w := byDefinition["OnOff"]; w == nil || !w.Satisfied {
		t.Error("the plug it found is shown as unsatisfied")
	}
	// This is the state that looks healthy from every other angle: every system
	// running, and one controller quietly reaching for a sensor nobody provides.
	if w := byDefinition["temperature"]; w == nil || w.Satisfied {
		t.Error("a service nothing provides is shown as satisfied, so the cloud looks " +
			"complete while a control loop has no input")
	}
}

// A system whose graph cannot be read is still on the picture. An operator
// looking for a system needs to see that it is there far more than they need
// its details.
func TestASystemThatSaysSomethingUnreadableIsStillDrawn(t *testing.T) {
	cloud := build("AlphaCloud", map[string]string{
		"beekeeper": beekeeperGraph,
		"mystery": `alc:home_mystery a afo:System ;
    afo:hasName "mystery" ;
    afo:hasHusk alc:home_mystery_Husk ;
    afo:somethingThisPainterHasNeverHeardOf "wat" .

alc:home_mystery_Husk a afo:Husk ;
    afo:runsOnHost alc:home .
`,
	})

	var names []string
	for _, host := range cloud.Hosts {
		for _, s := range host.Systems {
			names = append(names, s.Name)
		}
	}
	if !contains(names, "mystery") {
		t.Errorf("systems drawn: %v; one that described itself partly is missing "+
			"entirely, which is worse than drawing it with what it did say", names)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// The reader must not invent statements from indentation or punctuation it did
// not expect, because everything above rests on it.
func TestTheReaderKeepsSubjectsApart(t *testing.T) {
	facts := readTurtle(beekeeperGraph)

	if got := object(facts, "alc:home", "afo:hasName"); got != "home" {
		t.Errorf("the host's name read as %q; a new block must start a new subject", got)
	}
	if got := object(facts, "alc:home_beekeeper", "afo:hasName"); got != "beekeeper" {
		t.Errorf("the system's name read as %q", got)
	}
	// A literal keeps its value and loses its punctuation and datatype.
	if got := object(facts, "alc:home_beekeeper_Security", "afo:offersTLS"); got != "true" {
		t.Errorf("a typed boolean read as %q, want true", got)
	}
	// An IRI loses its brackets so it can be compared with a consumer's URL.
	url := object(facts, "alc:home_beekeeper_KitchenHeater_OnOff", "afo:hasUrl")
	if strings.ContainsAny(url, "<>") {
		t.Errorf("a URL read as %q; the brackets must go or no line can be joined", url)
	}
}
