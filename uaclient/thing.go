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
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/pkg/errors"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters
type Traits struct {
	ServerAdrress string              `json:"serverAddress"`
	NodeList      map[string][]string `json:"NodeList"`
	Server        *opcua.Client
	NodeID        *ua.NodeID
	NodeClass     ua.NodeClass
	NodeName      string
	BrowseName    string
	Description   string
	AccessLevel   ua.AccessLevelType
	Path          string
	DataType      string
	Writable      bool
	Unit          string
	Scale         string
	Min           string
	Max           string
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	// Asset-specific parameters
	Traits
}

// GetName returns the name of the Resource.
func (ua *UnitAsset) GetName() string {
	return ua.Name
}

// GetServices returns the services of the Resource.
func (ua *UnitAsset) GetServices() components.Services {
	return ua.ServicesMap
}

// GetCervices returns the list of consumed services by the Resource.
func (ua *UnitAsset) GetCervices() components.Cervices {
	return ua.CervicesMap
}

// GetDetails returns the details of the Resource.
func (ua *UnitAsset) GetDetails() map[string][]string {
	return ua.Details
}

// GetTraits returns the traits of the Resource.
func (ua *UnitAsset) GetTraits() any {
	return ua.Traits
}

// ensure UnitAsset implements components.UnitAsset (this check is done at during the compilation)
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	browse := components.Service{
		Definition:  "browse",
		SubPath:     "browse",
		Details:     map[string][]string{"Protocol": {"opc.tcp"}},
		RegPeriod:   61,
		Description: "provides the human readable (HTML) list (GET) of the nodes the OPC UA server holds, ",
	}

	access := components.Service{
		Definition:  "access",
		SubPath:     "access",
		Details:     map[string][]string{"Protocol": {"opc.tcp"}},
		RegPeriod:   30,
		Description: "accesses the OPC UA node to read (GET) the information or if possible to write (PUT)[but not yet], ",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "PLC with OPC UA server",
		Details: map[string][]string{"PLC": {"Prosys_Simulation_Server"}, "Location": {"Line_1"}, "KKS": {"YLLCP001"}},
		Traits: Traits{
			ServerAdrress: "opc.tcp://192.168.1.2:53530/OPCUA/SimulationServer",
		},
		ServicesMap: components.Services{
			browse.SubPath: &browse,
			access.SubPath: &access,
		},
	}
	return uat
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration using the uaConfig structs
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) ([]components.UnitAsset, func()) {
	var plcConfig Traits
	ctx := sys.Ctx
	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Fatalln("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		plcConfig = traits[0] // or handle multiple traits if needed
	}

	endpoint := plcConfig.ServerAdrress
	opcuaClient, err := opcua.NewClient(endpoint)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Trying to connect to OPC UA server @ %s\n", endpoint)
	if err := opcuaClient.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Connected")
	nodelist := []components.UnitAsset{}

	// Setting up the default node (Objects folder)
	uasset := &UnitAsset{}
	uasset.Name = "ObjectsFolder"
	uasset.NodeName = "ns=0;i=85"
	uasset.Server = opcuaClient
	uasset.NodeID, err = ua.ParseNodeID(uasset.NodeName)
	if err != nil {
		log.Fatalf("invalid node id: %s", err)
	}
	// uasset.Details = uac.Details
	// Create a new instance of components.Services since each resources has its own set of services
	// uasset.ServicesMap = components.CloneServices(servs)
	uasset.Owner = sys
	nodelist = append(nodelist, uasset)

	// Check if "Node_Id" key exists to avoid a potential panic
	if nodeIds, ok := plcConfig.NodeList["Node_Id"]; ok {
		for _, nodeId := range nodeIds {
			newUA := &UnitAsset{} // Create a pointer to UnitAsset
			newUA.Server = opcuaClient
			newUA.NodeID, err = ua.ParseNodeID(nodeId)
			if err != nil {
				log.Printf("invalid node id: %s", err)
				break
			}
			nodeList, err := browse(ctx, uasset.Server.Node(newUA.NodeID), "", 0)
			if err != nil {
				fmt.Printf("Node %s browsing errror %s", nodeId, err)
			}
			newUA.Name = nodeList[0].BrowseName
			// newUA.Details = uac.Details
			// Create a new instance of components.Services since each resources has its own set of services
			// newUA.ServicesMap = components.CloneServices(servs)

			newUA.Owner = sys
			nodelist = append(nodelist, newUA)
		}
	} else {
		fmt.Println("Node_Id key not found in map")
	}

	// Return the unit asset(s) and a cleanup function to close any connection
	return nodelist, func() {
		fmt.Println("Closing the OPC UA server connection")
		if err := opcuaClient.Close(ctx); err != nil {
			log.Printf("Error closing OPC UA connection: %v", err)
		}
	}
}

// UnmarshalTraits unmarshals a slice of json.RawMessage into a slice of Traits.
func UnmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {
	var traitsList []Traits
	for _, raw := range rawTraits {
		var t Traits
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("failed to unmarshal trait: %w", err)
		}
		traitsList = append(traitsList, t)
	}
	return traitsList, nil
}

// -------------------------------------Unit asset's function methods

// browseNode list the node(s)
func (node *UnitAsset) browseNode(w http.ResponseWriter) {

	nodeList, err := browse(node.Owner.Ctx, node.Server.Node(node.NodeID), "", 0)
	if err != nil {
		log.Fatal(err)
	}

	// Generate HTML output instead of CSV
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<table border='1'>")
	fmt.Fprintf(w, "<tr><th>Name</th><th>Type</th><th>Addr</th><th>Unit (SI)</th><th>Scale</th><th>Min</th><th>Max</th><th>Writable</th><th>Description</th></tr>")
	for _, s := range nodeList {
		// Assume s.Records() returns an array or slice that can be indexed into.
		fmt.Fprintf(w, "<tr>")
		for _, field := range s.Records() { // Replace with your actual function to retrieve records
			fmt.Fprintf(w, "<td>%s</td>", field)
		}
		fmt.Fprintf(w, "</tr>")
	}
	fmt.Fprintf(w, "</table>")

}

func (node *UnitAsset) read() (f forms.SignalA_v1a) {
	req := &ua.ReadRequest{
		MaxAge: 2000,
		NodesToRead: []*ua.ReadValueID{
			{NodeID: node.NodeID},
		},
		TimestampsToReturn: ua.TimestampsToReturnBoth,
	}

	var resp *ua.ReadResponse
	var err error
	for {
		resp, err = node.Server.Read(node.Owner.Ctx, req)
		if err == nil {
			break
		}

		// Following switch contains known errors that can be retried by the user.
		// Best practice is to do it on read operations.
		switch {
		case err == io.EOF && node.Server.State() != opcua.Closed:
			// has to be retried unless user closed the connection
			time.After(1 * time.Second)
			continue

		case errors.Is(err, ua.StatusBadSessionIDInvalid):
			// Session is not activated has to be retried. Session will be recreated internally.
			time.After(1 * time.Second)
			continue

		case errors.Is(err, ua.StatusBadSessionNotActivated):
			// Session is invalid has to be retried. Session will be recreated internally.
			time.After(1 * time.Second)
			continue

		case errors.Is(err, ua.StatusBadSecureChannelIDInvalid):
			// secure channel will be recreated internally.
			time.After(1 * time.Second)
			continue

		default:
			log.Printf("Read failed: %s", err)
			return f
		}
	}

	if resp != nil && resp.Results[0].Status != ua.StatusOK {
		log.Printf("Status not OK: %v", resp.Results[0].Status)
		return f
	}

	var cValue float64
	if resp != nil && len(resp.Results) > 0 && resp.Results[0].Status == ua.StatusOK {
		value := resp.Results[0].Value.Value()

		switch v := value.(type) {
		case float64:
			cValue = v
		case float32:
			// Convert float32 to float64
			cValue = float64(v)
		case int:
			// Convert int to float64
			cValue = float64(v)
		case int64:
			// Convert int64 to float64
			cValue = float64(v)
		case int32:
			// Convert int32 to float64
			cValue = float64(v)
		case uint:
			// Convert uint to float64
			cValue = float64(v)
		case uint64:
			// Convert uint64 to float64
			cValue = float64(v)
		case uint32:
			// Convert uint32 to float64
			cValue = float64(v)
		default:
			// Handle the case where the value is not a recognized number type
			log.Printf("Value is not a recognized number type: %#v", value)
		}
	} else if resp != nil && len(resp.Results) > 0 {
		log.Printf("Status not OK: %v\n", resp.Results[0].Status)
		return f
	} else {
		log.Printf("No response received\n")
		return f
	}

	f.NewForm()
	f.Value = cValue
	f.Unit = "undefined"     // should get it from the server
	f.Timestamp = time.Now() // should get it from the server
	return f
}

type NodeDef struct {
	NodeID      *ua.NodeID
	NodeClass   ua.NodeClass
	BrowseName  string
	Description string
	AccessLevel ua.AccessLevelType
	Path        string
	DataType    string
	Writable    bool
	Unit        string
	Scale       string
	Min         string
	Max         string
}

func (n NodeDef) Records() []string {
	return []string{n.BrowseName, n.DataType, n.NodeID.String(), n.Unit, n.Scale, n.Min, n.Max, strconv.FormatBool(n.Writable), n.Description}
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

func browse(ctx context.Context, n *opcua.Node, path string, level int) ([]NodeDef, error) {
	// fmt.Printf("node:%s path:%q level:%d\n", n, path, level)
	if level > 10 {
		return nil, nil
	}

	attrs, err := n.Attributes(ctx, ua.AttributeIDNodeClass, ua.AttributeIDBrowseName, ua.AttributeIDDescription, ua.AttributeIDAccessLevel, ua.AttributeIDDataType)
	if err != nil {
		return nil, err
	}

	var def = NodeDef{
		NodeID: n.ID,
	}

	switch err := attrs[0].Status; err {
	case ua.StatusOK:
		def.NodeClass = ua.NodeClass(attrs[0].Value.Int())
	default:
		return nil, err
	}

	switch err := attrs[1].Status; err {
	case ua.StatusOK:
		def.BrowseName = attrs[1].Value.String()
	default:
		return nil, err
	}

	switch err := attrs[2].Status; err {
	case ua.StatusOK:
		def.Description = attrs[2].Value.String()
	case ua.StatusBadAttributeIDInvalid:
		// ignore
	default:
		return nil, err
	}

	switch err := attrs[3].Status; err {
	case ua.StatusOK:
		def.AccessLevel = ua.AccessLevelType(attrs[3].Value.Int())
		def.Writable = def.AccessLevel&ua.AccessLevelTypeCurrentWrite == ua.AccessLevelTypeCurrentWrite
	case ua.StatusBadAttributeIDInvalid:
		// ignore
	default:
		return nil, err
	}

	switch err := attrs[4].Status; err {
	case ua.StatusOK:
		switch v := attrs[4].Value.NodeID().IntID(); v {
		case id.DateTime:
			def.DataType = "time.Time"
		case id.Boolean:
			def.DataType = "bool"
		case id.SByte:
			def.DataType = "int8"
		case id.Int16:
			def.DataType = "int16"
		case id.Int32:
			def.DataType = "int32"
		case id.Byte:
			def.DataType = "byte"
		case id.UInt16:
			def.DataType = "uint16"
		case id.UInt32:
			def.DataType = "uint32"
		case id.UtcTime:
			def.DataType = "time.Time"
		case id.String:
			def.DataType = "string"
		case id.Float:
			def.DataType = "float32"
		case id.Double:
			def.DataType = "float64"
		default:
			def.DataType = attrs[4].Value.NodeID().String()
		}
	case ua.StatusBadAttributeIDInvalid:
		// ignore
	default:
		return nil, err
	}

	def.Path = join(path, def.BrowseName)
	// fmt.Printf("%d: def.Path:%s def.NodeClass:%s\n", level, def.Path, def.NodeClass)

	var nodes []NodeDef
	if def.NodeClass == ua.NodeClassVariable {
		nodes = append(nodes, def)
	}

	browseChildren := func(refType uint32) error {
		refs, err := n.ReferencedNodes(ctx, refType, ua.BrowseDirectionForward, ua.NodeClassAll, true)
		if err != nil {
			return errors.Errorf("References: %d: %s", refType, err)
		}
		// fmt.Printf("found %d child refs\n", len(refs))
		for _, rn := range refs {
			children, err := browse(ctx, rn, def.Path, level+1)
			if err != nil {
				return errors.Errorf("browse children: %s", err)
			}
			nodes = append(nodes, children...)
		}
		return nil
	}

	if err := browseChildren(id.HasComponent); err != nil {
		return nil, err
	}
	if err := browseChildren(id.Organizes); err != nil {
		return nil, err
	}
	if err := browseChildren(id.HasProperty); err != nil {
		return nil, err
	}
	return nodes, nil
}
