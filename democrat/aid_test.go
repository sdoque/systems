package main

import (
	"strings"
	"testing"
)

// find returns the element at a slash-separated path of idShorts, or nil.
func find(elems []SubmodelElement, path string) *SubmodelElement {
	parts := strings.Split(path, "/")
	for i := range elems {
		if elems[i].IDShort != parts[0] {
			continue
		}
		if len(parts) == 1 {
			return &elems[i]
		}
		kids, ok := elems[i].Value.([]SubmodelElement)
		if !ok {
			return nil
		}
		return find(kids, strings.Join(parts[1:], "/"))
	}
	return nil
}

func aidOf(t *testing.T, s *SystemInfo) Submodel {
	t.Helper()
	env := buildAASEnv(map[string]*SystemInfo{s.SystemURI: s})
	for _, sm := range env.Submodels {
		if sm.IDShort == "AssetInterfacesDescription" {
			return sm
		}
	}
	t.Fatal("no Asset Interfaces Description was built")
	return Submodel{}
}

func thermostat() *SystemInfo {
	return &SystemInfo{
		SystemURI: "alc:thermostat", SystemName: "thermostat",
		HostName: "pi-office", IPs: []string{"192.168.1.10"},
		Services: []ServiceInfo{{
			ServiceName: "controller/setpoint", ServiceDef: "setpoint",
			URLs: []string{
				"http://192.168.1.10:20185/thermostat/controller/setpoint",
				"https://192.168.1.10:30185/thermostat/controller/setpoint",
			},
			URL:          "http://192.168.1.10:20185/thermostat/controller/setpoint",
			Unit:         "http://qudt.org/vocab/unit/DEG_C",
			QuantityKind: "http://qudt.org/vocab/quantitykind/ThermodynamicTemperature",
			Methods: []string{
				"http://www.w3.org/2011/http-methods#GET",
				"http://www.w3.org/2011/http-methods#PUT",
			},
			Subscribable: true, Form: "SignalA_v1a",
		}},
	}
}

// A husk that opens two ports is reachable two ways, and the two are not
// interchangeable: one is unauthenticated and the other is not. Describing them
// as one interface would leave a consumer to guess which security applies.
func TestOneInterfacePerProtocol(t *testing.T) {
	aid := aidOf(t, thermostat())

	if len(aid.SubmodelElements) != 2 {
		t.Fatalf("got %d interfaces, want one per protocol", len(aid.SubmodelElements))
	}
	for _, want := range []struct{ iface, base, security string }{
		{"InterfaceHTTP", "http://192.168.1.10:20185/thermostat/", "nosec_sc"},
		{"InterfaceHTTPS", "https://192.168.1.10:30185/thermostat/", "auto_sc"},
	} {
		base := find(aid.SubmodelElements, want.iface+"/EndpointMetadata/base")
		if base == nil {
			t.Errorf("%s has no base", want.iface)
			continue
		}
		if base.Value != want.base {
			t.Errorf("%s base = %v, want %q", want.iface, base.Value, want.base)
		}
		if find(aid.SubmodelElements, want.iface+"/EndpointMetadata/securityDefinitions/"+want.security) == nil {
			t.Errorf("%s does not declare %s", want.iface, want.security)
		}
	}
}

// The three mappings that are exact rather than approximate. If any of them
// drifts, this submodel goes back to being a shape with a name on it.
func TestTheExactMappings(t *testing.T) {
	aid := aidOf(t, thermostat())
	const prop = "InterfaceHTTPS/InteractionMetadata/properties/controller_setpoint"

	// A subscribable service is one a consumer may follow rather than poll,
	// which is what the Web of Things means by observable.
	if el := find(aid.SubmodelElements, prop+"/observable"); el == nil || el.Value != true {
		t.Errorf("observable = %v, want the service's subscribability", el)
	}
	// schema.org/unitCode admits a URL, so the QUDT IRI goes in whole and keeps
	// the conversion factors behind it.
	if el := find(aid.SubmodelElements, prop+"/unit"); el == nil ||
		el.Value != "http://qudt.org/vocab/unit/DEG_C" {
		t.Errorf("unit = %v, want the QUDT unit IRI", el)
	}
	// valueSemantics exists to point at what the value means, and a quantity
	// kind is precisely that: this is a temperature, not a number with °C after it.
	el := find(aid.SubmodelElements, prop+"/valueSemantics")
	if el == nil {
		t.Fatal("no valueSemantics")
	}
	if el.ModelType != "ReferenceElement" {
		t.Errorf("valueSemantics is a %s; a concept elsewhere needs a reference", el.ModelType)
	}
	ref, ok := el.Value.(*Reference)
	if !ok || len(ref.Keys) != 1 ||
		ref.Keys[0].Value != "http://qudt.org/vocab/quantitykind/ThermodynamicTemperature" {
		t.Errorf("valueSemantics = %v, want the QUDT quantity kind", el.Value)
	}
}

// A form's href is relative to the interface's base. Repeating the host in every
// property would be a second place for it to be wrong, and it is the part that
// changes when a system moves.
func TestHrefIsRelativeToTheBase(t *testing.T) {
	aid := aidOf(t, thermostat())
	el := find(aid.SubmodelElements,
		"InterfaceHTTP/InteractionMetadata/properties/controller_setpoint/forms/href")
	if el == nil {
		t.Fatal("no href")
	}
	if el.Value != "controller/setpoint" {
		t.Errorf("href = %v, want it relative to the base", el.Value)
	}
}

// AID 1.0 gives a property one form and that form one method name, so a service
// answering GET and PUT can only state the read here. What it cannot state must
// still appear somewhere: the Services submodel carries the whole list.
func TestAWriteIsNotSilentlyDropped(t *testing.T) {
	s := thermostat()
	env := buildAASEnv(map[string]*SystemInfo{s.SystemURI: s})

	var aid, services Submodel
	for _, sm := range env.Submodels {
		switch sm.IDShort {
		case "AssetInterfacesDescription":
			aid = sm
		case "Services":
			services = sm
		}
	}

	method := find(aid.SubmodelElements,
		"InterfaceHTTPS/InteractionMetadata/properties/controller_setpoint/forms/htv_methodName")
	if method == nil || method.Value != "GET" {
		t.Errorf("htv_methodName = %v, want the method a consumer reads with", method)
	}

	el := find(services.SubmodelElements, "Methods_controller_setpoint")
	if el == nil {
		t.Fatal("the Services submodel does not say the setpoint can be written")
	}
	if el.Value != "GET PUT" {
		t.Errorf("Methods = %v, want the complete list", el.Value)
	}
	if el.SemanticID == nil || el.SemanticID.Keys[0].Value != alc+"hasMethods" {
		t.Errorf("the method list means %v, want the predicate the graph used", el.SemanticID)
	}
}

// A service that never reads states the method it does answer. Calling beehive's
// toggle a GET would be worse than saying nothing, because a consumer would
// believe it.
func TestAServiceThatOnlyWritesSaysSo(t *testing.T) {
	aid := aidOf(t, &SystemInfo{
		SystemURI: "alc:beehive", SystemName: "beehive",
		Services: []ServiceInfo{{
			ServiceName: "hive/toggle", ServiceDef: "Toggle",
			URLs:    []string{"http://192.168.1.10:20190/beehive/hive/toggle"},
			Methods: []string{"http://www.w3.org/2011/http-methods#PUT"},
			Form:    "SignalB_v1a",
		}},
	})

	el := find(aid.SubmodelElements,
		"InterfaceHTTP/InteractionMetadata/properties/hive_toggle/forms/htv_methodName")
	if el == nil || el.Value != "PUT" {
		t.Errorf("htv_methodName = %v, want PUT", el)
	}
	// And a SignalB carries a boolean, not a number.
	if el := find(aid.SubmodelElements,
		"InterfaceHTTP/InteractionMetadata/properties/hive_toggle/type"); el == nil ||
		el.Value != "boolean" {
		t.Errorf("type = %v, want boolean", el)
	}
}

// A service that said nothing about methods gets no claim made on its behalf.
// A consumer assumes a read of a service it has been told nothing about, so
// stating GET would add a claim rather than a fact.
func TestSilenceAboutMethodsStaysSilent(t *testing.T) {
	aid := aidOf(t, &SystemInfo{
		SystemURI: "alc:ds18b20", SystemName: "ds18b20",
		Services: []ServiceInfo{{
			ServiceName: "probe/temperature", ServiceDef: "temperature",
			URLs: []string{"http://192.168.1.10:20150/ds18b20/probe/temperature"},
		}},
	})
	if el := find(aid.SubmodelElements,
		"InterfaceHTTP/InteractionMetadata/properties/probe_temperature/forms/htv_methodName"); el != nil {
		t.Errorf("htv_methodName = %v; the service never said", el.Value)
	}
}

// A system whose services have no address has no interface to describe, and an
// empty interface description is worse than none: it says the system was looked
// at and found to offer nothing.
func TestNoAddressNoInterfaceDescription(t *testing.T) {
	env := buildAASEnv(map[string]*SystemInfo{
		"alc:ghost": {SystemURI: "alc:ghost", SystemName: "ghost"},
	})
	for _, sm := range env.Submodels {
		if sm.IDShort == "AssetInterfacesDescription" {
			t.Error("a system with no addresses was given an interface description")
		}
	}
	for _, ref := range env.AssetAdministrationShells[0].Submodels {
		if strings.Contains(ref.Keys[0].Value, "AssetInterfacesDescription") {
			t.Error("the shell references an interface description that was not built")
		}
	}
}
