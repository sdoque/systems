package main

import (
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

// Every host runs a maitreD named "maitreD" offering "attest", and a registrar
// named "serviceregistrar" offering "registry". Matching on name and definition
// alone made all four of each the same record, so they overwrote one another in
// turn and appeared to pop in and out of the cloud.
func TestTheSameSystemOnTwoHostsIsTwoRecords(t *testing.T) {
	onRPi4 := &forms.ServiceRecord_v1{
		SystemName: "maitreD", ServiceDefinition: "attest", SubPath: "attest",
		ServiceNode: "RPi4_maitreD_maitreD_attest", IPAddresses: []string{"192.168.1.5"},
	}
	onRPi5 := &forms.ServiceRecord_v1{
		SystemName: "maitreD", ServiceDefinition: "attest", SubPath: "attest",
		ServiceNode: "RPi5_maitreD_maitreD_attest", IPAddresses: []string{"192.168.1.7"},
	}
	if sameInstance(onRPi4, onRPi5) {
		t.Error("two maitreDs on different hosts were treated as one record; they would evict each other")
	}
	if !sameInstance(onRPi4, onRPi4) {
		t.Error("a system's own renewal was treated as a different instance; it would register twice")
	}
}

// The renewal path depends on this: a system re-registering must find its own
// record, or it makes a second one and the registry answers with both.
func TestARenewalFromTheSameInstanceMatches(t *testing.T) {
	held := &forms.ServiceRecord_v1{
		SystemName: "serviceregistrar", ServiceDefinition: "registry", SubPath: "registry",
		ServiceNode: "RPi3_serviceregistrar_registry_registry", IPAddresses: []string{"192.168.1.6"},
	}
	renewal := *held
	if !sameInstance(held, &renewal) {
		t.Error("a renewal did not match its own record")
	}
}

// A record without a ServiceNode falls back to the address, which is still
// enough to tell one host from another.
func TestFallsBackToTheAddressWithoutAServiceNode(t *testing.T) {
	a := &forms.ServiceRecord_v1{IPAddresses: []string{"192.168.1.5"}}
	b := &forms.ServiceRecord_v1{IPAddresses: []string{"192.168.1.7"}}
	if sameInstance(a, b) {
		t.Error("different addresses were treated as the same instance")
	}
	if !sameInstance(a, &forms.ServiceRecord_v1{IPAddresses: []string{"192.168.1.5"}}) {
		t.Error("the same address was treated as a different instance")
	}
	// With nothing to tell them apart the caller has already matched system,
	// definition and path, so they are treated as one record — the behaviour
	// before this change. A second record per renewal would be worse.
	if !sameInstance(&forms.ServiceRecord_v1{}, &forms.ServiceRecord_v1{}) {
		t.Error("a renewal carrying no identifying fields made a duplicate record")
	}
}
