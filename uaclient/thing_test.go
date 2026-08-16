/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gopcua/opcua/ua"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ------------------------------------- join

func TestJoin(t *testing.T) {
	tests := []struct {
		a, b, want string
	}{
		{"Objects", "PLC", "Objects.PLC"},
		{"", "Root", "Root"},
		{"a", "b", "a.b"},
		{"", "", ""},
	}
	for _, tc := range tests {
		got := join(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("join(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// ------------------------------------- NodeDef.Records

func TestNodeDefRecords(t *testing.T) {
	nodeID, _ := ua.ParseNodeID("ns=2;i=1001")
	def := NodeDef{
		NodeID:      nodeID,
		BrowseName:  "Temperature",
		DataType:    "float64",
		Writable:    true,
		Description: "process temperature",
		Unit:        "°C",
		Scale:       "1.0",
		Min:         "0",
		Max:         "200",
	}

	records := def.Records()
	if len(records) != 9 {
		t.Fatalf("Records() length = %d, want 9", len(records))
	}
	// Order: BrowseName, DataType, NodeID, Unit, Scale, Min, Max, Writable, Description
	if records[0] != "Temperature" {
		t.Errorf("records[0] = %q, want BrowseName", records[0])
	}
	if records[1] != "float64" {
		t.Errorf("records[1] = %q, want DataType", records[1])
	}
	if records[7] != "true" {
		t.Errorf("records[7] = %q, want Writable=true", records[7])
	}
	if records[8] != "process temperature" {
		t.Errorf("records[8] = %q, want Description", records[8])
	}
}

// ------------------------------------- initTemplate

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() == "" {
		t.Error("template name should not be empty")
	}

	svcs := ua.GetServices()
	if _, ok := svcs["browse"]; !ok {
		t.Error("ServicesMap should contain a 'browse' service")
	}
	if _, ok := svcs["access"]; !ok {
		t.Error("ServicesMap should contain an 'access' service")
	}

	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.ServerAddress == "" {
		t.Error("ServerAddress default should not be empty")
	}
}

// ------------------------------------- serving dispatcher

func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/uaclient/PLC/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A node's mission follows the access level the OPC UA server reports, so the
// classification cannot disagree with what the server will actually permit.
func TestMissionForAccess(t *testing.T) {
	if got := missionForAccess(true); got.String() != "actuation" {
		t.Errorf("missionForAccess(true) = %q; want %q", got, "actuation")
	}
	if got := missionForAccess(false); got.String() != "measurement" {
		t.Errorf("missionForAccess(false) = %q; want %q", got, "measurement")
	}
}

// Browsing only enumerates the address space, so it stays measurement even on a
// writable node. Were it to inherit the node's mission, a policy granting writes
// to actuation would also grant enumeration of everything behind that node.
func TestApplyNodeMissionsLeavesBrowseReadOnly(t *testing.T) {
	ua := initTemplate()
	services := usecases.MakeServiceMap(getServicesList(*ua))
	applyNodeMissions(services)

	browseServ, ok := services["browse"]
	if !ok {
		t.Fatal("template has no browse service")
	}
	if browseServ.Mission.String() != "measurement" {
		t.Errorf("browse mission = %q; want %q", browseServ.Mission, "measurement")
	}

	// access carries no mission of its own: it inherits the node asset's, which
	// is what missionForAccess decided.
	accessServ, ok := services["access"]
	if !ok {
		t.Fatal("template has no access service")
	}
	if accessServ.Mission.String() != "" {
		t.Errorf("access mission = %q; want it to inherit the asset's", accessServ.Mission)
	}

	writable := &components.UnitAsset{Mission: missionForAccess(true), ServicesMap: services}
	if got := components.EffectiveMission(writable, accessServ); got.String() != "actuation" {
		t.Errorf("access effective mission on a writable node = %q; want %q", got, "actuation")
	}
	if got := components.EffectiveMission(writable, browseServ); got.String() != "measurement" {
		t.Errorf("browse effective mission on a writable node = %q; want %q", got, "measurement")
	}
}

// getServicesList mirrors the framework's own template-to-config conversion.
func getServicesList(ua components.UnitAsset) []components.Service {
	var list []components.Service
	for _, s := range ua.GetServices() {
		list = append(list, *s)
	}
	return list
}
