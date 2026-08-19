package main

import (
	"strings"
	"testing"
)

func twoUnits() map[string]*SystemInfo {
	return map[string]*SystemInfo{"u": {
		SystemURI: "u", SystemName: "thermostat", HostName: "pi", IPs: []string{"192.168.1.10"},
		Services: []ServiceInfo{
			{ServiceName: "controller/setpoint", ServiceDef: "setpoint",
				URLs:         []string{"https://192.168.1.10:30185/thermostat/controller/setpoint"},
				Unit:         "http://qudt.org/vocab/unit/DEG_C",
				QuantityKind: "http://qudt.org/vocab/quantitykind/ThermodynamicTemperature",
				Methods: []string{"http://www.w3.org/2011/http-methods#GET",
					"http://www.w3.org/2011/http-methods#PUT"},
				Subscribable: true, Form: "SignalA_v1a"},
			{ServiceName: "controller/jitter", ServiceDef: "jitter",
				URLs:         []string{"https://192.168.1.10:30185/thermostat/controller/jitter"},
				Unit:         "http://qudt.org/vocab/unit/MilliSEC",
				QuantityKind: "http://qudt.org/vocab/quantitykind/Time",
				Form:         "SignalA_v1a"},
		},
	}}
}

func concepts(t *testing.T) map[string]ConceptDescription {
	t.Helper()
	env := buildAASEnv(twoUnits())
	out := map[string]ConceptDescription{}
	for _, cd := range env.ConceptDescriptions {
		out[cd.ID] = cd
	}
	return out
}

// The rule: describe what only we can describe. An identifier this cloud minted
// exists nowhere else, so if democrat does not say what it means, nobody can.
// AFO, IDTA and Web of Things terms are published by people who define them
// properly, and copying those definitions into every shell would be the same
// duplication this system exists to remove — with copies that drift the first
// time the ontology is revised.
func TestOnlyWhatThisBridgeCanSpeakFor(t *testing.T) {
	for id := range concepts(t) {
		switch {
		case strings.HasPrefix(id, alc), strings.HasPrefix(id, "http://qudt.org/"):
			// ours to explain, or the QUDT translation only this bridge makes
		default:
			t.Errorf("%s is described here; it belongs to whoever published it", id)
		}
	}

	// And specifically: the AFO predicates carried as semanticIds get none.
	if _, described := concepts(t)[afo+"hasName"]; described {
		t.Error("an AFO term was redefined locally instead of left to its ontology")
	}
}

// Every identifier this cloud minted resolves inside the environment. A
// semanticId whose concept is missing is a promise that somebody wrote down
// what it means, broken.
func TestNoLocalIdentifierDangles(t *testing.T) {
	env := buildAASEnv(twoUnits())
	described := map[string]bool{}
	for _, cd := range env.ConceptDescriptions {
		described[cd.ID] = true
	}

	var check func(el SubmodelElement, path string)
	check = func(el SubmodelElement, path string) {
		path += "/" + el.IDShort
		if el.SemanticID != nil {
			if id := el.SemanticID.Keys[0].Value; strings.HasPrefix(id, alc) && !described[id] {
				t.Errorf("%s means %s, which nothing in this environment explains", path, id)
			}
		}
		if kids, ok := el.Value.([]SubmodelElement); ok {
			for _, kid := range kids {
				check(kid, path)
			}
		}
	}
	for _, sm := range env.Submodels {
		if id := sm.SemanticID.Keys[0].Value; strings.HasPrefix(id, alc) && !described[id] {
			t.Errorf("submodel %s means %s, which nothing explains", sm.IDShort, id)
		}
		for _, el := range sm.SubmodelElements {
			check(el, sm.IDShort)
		}
	}
}

// A unit's description carries the IEC 61360 translation, which is the whole
// reason for describing a QUDT unit at all: QUDT is authoritative and
// dereferences, but it does not publish "°C, and formally this IRI" in the shape
// the Asset Administration Shell world reads.
func TestAUnitCarriesItsSymbolAndItsIRI(t *testing.T) {
	cd, ok := concepts(t)["http://qudt.org/vocab/unit/DEG_C"]
	if !ok {
		t.Fatal("the degree Celsius is used and not described")
	}
	if len(cd.EmbeddedDataSpecifications) != 1 {
		t.Fatalf("got %d data specifications, want one", len(cd.EmbeddedDataSpecifications))
	}
	spec := cd.EmbeddedDataSpecifications[0]
	if spec.DataSpecification.Keys[0].Value != iec61360 {
		t.Errorf("content is shaped by %q, want the IEC 61360 template",
			spec.DataSpecification.Keys[0].Value)
	}
	c := spec.DataSpecificationContent
	if c.Unit != "°C" {
		t.Errorf("unit = %q, want the symbol the framework converts with", c.Unit)
	}
	if c.UnitID == nil || c.UnitID.Keys[0].Value != "http://qudt.org/vocab/unit/DEG_C" {
		t.Errorf("unitId = %v, want the QUDT IRI", c.UnitID)
	}
	// REAL_MEASURE is IEC 61360 for a real number with a unit. The specification
	// requires a unit or unitId when it is used, and both are here.
	if c.DataType != "REAL_MEASURE" {
		t.Errorf("dataType = %q, want REAL_MEASURE", c.DataType)
	}
	if len(c.Definition) == 0 || !strings.Contains(c.Definition[0].Text, "ThermodynamicTemperature") {
		t.Errorf("definition = %v, want it to say what the unit measures", c.Definition)
	}
	if len(c.PreferredName) == 0 || c.PreferredName[0].Language != "en" {
		t.Error("a name in no stated language leaves a consumer to guess")
	}
}

// The symbols come from the framework's own table — the one its conversions use
// — rather than from a second table here that could disagree with it. A unit the
// framework does not know is left undescribed rather than given an invented
// symbol.
func TestSymbolsComeFromTheTableTheConversionsUse(t *testing.T) {
	env := buildAASEnv(map[string]*SystemInfo{"u": {
		SystemURI: "u", SystemName: "odd",
		Services: []ServiceInfo{{
			ServiceName: "sensor/reading",
			URLs:        []string{"http://127.0.0.1:20100/odd/sensor/reading"},
			Unit:        "http://qudt.org/vocab/unit/FURLONG_PER_FORTNIGHT",
		}},
	}})
	for _, cd := range env.ConceptDescriptions {
		if strings.Contains(cd.ID, "FURLONG") {
			t.Error("a unit the framework cannot convert was given a description anyway")
		}
	}
}

// A quantity kind says what a value is, not what it is counted in. Filling in a
// unit or a data type would be describing the wrong thing.
func TestAQuantityKindCarriesNoUnit(t *testing.T) {
	cd, ok := concepts(t)["http://qudt.org/vocab/quantitykind/ThermodynamicTemperature"]
	if !ok {
		t.Fatal("the quantity kind valueSemantics points at is not described")
	}
	c := cd.EmbeddedDataSpecifications[0].DataSpecificationContent
	if c.Unit != "" || c.UnitID != nil {
		t.Errorf("a quantity kind was given a unit: %q %v", c.Unit, c.UnitID)
	}
	if c.DataType != "" {
		t.Errorf("dataType = %q; a quantity kind is not a value", c.DataType)
	}
}

// Two services measuring in the same unit produce one description of it, not
// one each.
func TestEachConceptIsDescribedOnce(t *testing.T) {
	env := buildAASEnv(map[string]*SystemInfo{"u": {
		SystemURI: "u", SystemName: "pair",
		Services: []ServiceInfo{
			{ServiceName: "a/temperature", URLs: []string{"http://127.0.0.1:20100/pair/a/temperature"},
				Unit: "http://qudt.org/vocab/unit/DEG_C"},
			{ServiceName: "b/temperature", URLs: []string{"http://127.0.0.1:20100/pair/b/temperature"},
				Unit: "http://qudt.org/vocab/unit/DEG_C"},
		},
	}})
	seen := map[string]int{}
	for _, cd := range env.ConceptDescriptions {
		seen[cd.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s is described %d times", id, n)
		}
	}
}
