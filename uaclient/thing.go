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
	"mime"
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
	ServerAddress string              `json:"serverAddress"`
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
	owner         *components.System
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
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

	return &components.UnitAsset{
		Name:    "PLC with OPC UA server",
		Details: map[string][]string{"PLC": {"Prosys_Simulation_Server"}, "FunctionalLocation": {"Line_1"}, "KKS": {"YLLCP001"}},
		ServicesMap: components.Services{
			browse.SubPath: &browse,
			access.SubPath: &access,
		},
		Traits: &Traits{
			ServerAddress: "opc.tcp://192.168.1.2:53530/OPCUA/SimulationServer",
		},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration using the uaConfig structs
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	var plcConfig Traits
	ctx := sys.Ctx
	if len(configuredAsset.Traits) > 0 {
		var traitsList []Traits
		for _, raw := range configuredAsset.Traits {
			var t Traits
			if err := json.Unmarshal(raw, &t); err != nil {
				log.Fatalln("Warning: could not unmarshal traits:", err)
			}
			traitsList = append(traitsList, t)
		}
		if len(traitsList) > 0 {
			plcConfig = traitsList[0]
		}
	}

	endpoint := plcConfig.ServerAddress
	opcuaClient, err := opcua.NewClient(endpoint)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Trying to connect to OPC UA server @ %s\n", endpoint)
	if err := opcuaClient.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Connected")

	var nodelist []*components.UnitAsset

	// Setting up the default node (Objects folder)
	rootTraits := &Traits{
		NodeName: "ns=0;i=85",
		Server:   opcuaClient,
		owner:    sys,
	}
	rootTraits.NodeID, err = ua.ParseNodeID(rootTraits.NodeName)
	if err != nil {
		log.Fatalf("invalid node id: %s", err)
	}
	rootUA := &components.UnitAsset{
		Name:        "ObjectsFolder",
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      rootTraits,
	}
	rootUA.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(rootTraits, w, r, servicePath)
	}
	nodelist = append(nodelist, rootUA)

	// Register one UnitAsset per configured Node_Id. A single bad entry must
	// not abort the whole loop — skip the offender and keep going.
	if nodeIds, ok := plcConfig.NodeList["Node_Id"]; ok {
		log.Printf("uaclient: found %d configured Node_Id entries", len(nodeIds))
		for _, nodeId := range nodeIds {
			t := &Traits{
				Server: opcuaClient,
				owner:  sys,
			}
			t.NodeID, err = ua.ParseNodeID(nodeId)
			if err != nil {
				log.Printf("uaclient: skipping %q — invalid node id: %v", nodeId, err)
				continue
			}
			nodeList, err := browse(ctx, rootTraits.Server.Node(t.NodeID), "", 0)
			if err != nil {
				log.Printf("uaclient: skipping %q — browse error: %v", nodeId, err)
				continue
			}
			if len(nodeList) == 0 {
				log.Printf("uaclient: skipping %q — browse returned no nodes", nodeId)
				continue
			}
			t.NodeName = nodeList[0].BrowseName
			// Remember the OPC UA data type so a PUT can coerce a numeric
			// SignalA value to the exact integer/float width the server
			// expects (writing a Double to an Int16 node is a type
			// mismatch error on Siemens).
			t.DataType = nodeList[0].DataType
			t.Writable = nodeList[0].Writable
			newUA := &components.UnitAsset{
				Name:        t.NodeName,
				Owner:       sys,
				Details:     configuredAsset.Details,
				ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
				Traits:      t,
			}
			newUA.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
				serving(t, w, r, servicePath)
			}
			nodelist = append(nodelist, newUA)
			log.Printf("uaclient: registered %q as UnitAsset %q", nodeId, t.NodeName)
		}
	} else {
		log.Println("uaclient: no Node_Id entries found in systemconfig.json traits")
	}

	// Return the unit asset(s) and a cleanup function to close any connection
	return nodelist, func() {
		fmt.Println("Closing the OPC UA server connection")
		if err := opcuaClient.Close(ctx); err != nil {
			log.Printf("Error closing OPC UA connection: %v", err)
		}
	}
}

// -------------------------------------Service handlers

func (t *Traits) browseHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		t.browseNode(w)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

func (t *Traits) access(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		valueForm := t.read()
		if valueForm == nil {
			http.Error(w, "unable to read value from OPC UA server", http.StatusBadGateway)
			return
		}
		usecases.HTTPProcessGetRequest(w, r, valueForm)
	case "PUT":
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "invalid Content-Type: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "could not read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		newState, err := usecases.Unpack(body, mediaType)
		if err != nil {
			http.Error(w, "could not unpack value form: "+err.Error(), http.StatusBadRequest)
			return
		}
		var val interface{}
		switch ns := newState.(type) {
		case *forms.SignalB_v1a:
			val = ns.Value // bool
		case *forms.SignalA_v1a:
			val = ns.Value // float64 — coerced in write() based on DataType
		default:
			http.Error(w, fmt.Sprintf("unsupported form type: %T", ns), http.StatusBadRequest)
			return
		}
		if err := t.write(val); err != nil {
			log.Printf("write failed for node %s: %v", t.NodeID, err)
			http.Error(w, "write failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// -------------------------------------Unit asset's function methods

// browseNode lists the node(s) under the configured NodeID as an HTML table.
// Retries once on transient session/channel errors (Siemens servers time out
// idle sessions after ~30 s; gopcua re-establishes on the next request).
// Returns an HTTP error rather than killing the process on any other failure.
func (t *Traits) browseNode(w http.ResponseWriter) {
	var nodeList []NodeDef
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		nodeList, err = browse(t.owner.Ctx, t.Server.Node(t.NodeID), "", 0)
		if err == nil {
			break
		}
		if errors.Is(err, ua.StatusBadSessionIDInvalid) ||
			errors.Is(err, ua.StatusBadSessionNotActivated) ||
			errors.Is(err, ua.StatusBadSecureChannelIDInvalid) {
			log.Printf("browse: transient session error, retrying: %v", err)
			continue
		}
		break
	}
	if err != nil {
		log.Printf("browse failed for node %s: %v", t.NodeID, err)
		http.Error(w, fmt.Sprintf("browse failed: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<table border='1'>")
	fmt.Fprintf(w, "<tr><th>Name</th><th>Type</th><th>Addr</th><th>Unit (SI)</th><th>Scale</th><th>Min</th><th>Max</th><th>Writable</th><th>Description</th></tr>")
	for _, s := range nodeList {
		fmt.Fprintf(w, "<tr>")
		for _, field := range s.Records() {
			fmt.Fprintf(w, "<td>%s</td>", field)
		}
		fmt.Fprintf(w, "</tr>")
	}
	fmt.Fprintf(w, "</table>")
}

// read fetches the current value of the configured OPC UA node and wraps it
// in the appropriate Arrowhead form: SignalB_v1a for booleans, SignalA_v1a
// for numeric scalars. Returns nil on a read error or unsupported type so
// the caller can surface a 5xx to the client.
func (t *Traits) read() forms.Form {
	req := &ua.ReadRequest{
		MaxAge: 2000,
		NodesToRead: []*ua.ReadValueID{
			{NodeID: t.NodeID},
		},
		TimestampsToReturn: ua.TimestampsToReturnBoth,
	}

	var resp *ua.ReadResponse
	var err error
	for {
		resp, err = t.Server.Read(t.owner.Ctx, req)
		if err == nil {
			break
		}

		// Following switch contains known errors that can be retried by the user.
		// Best practice is to do it on read operations.
		switch {
		case err == io.EOF && t.Server.State() != opcua.Closed:
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
			return nil
		}
	}

	if resp == nil || len(resp.Results) == 0 {
		log.Printf("No response received\n")
		return nil
	}
	if resp.Results[0].Status != ua.StatusOK {
		log.Printf("Status not OK: %v", resp.Results[0].Status)
		return nil
	}

	value := resp.Results[0].Value.Value()
	now := time.Now()

	// Boolean nodes (discrete inputs, coils, digital outputs) → SignalB.
	if b, ok := value.(bool); ok {
		sb := &forms.SignalB_v1a{}
		sb.NewForm()
		sb.Value = b
		sb.Timestamp = now
		return sb
	}

	// Numeric nodes → SignalA. Go through every integer/float width OPC UA
	// can return and promote to float64.
	var cValue float64
	switch v := value.(type) {
	case float64:
		cValue = v
	case float32:
		cValue = float64(v)
	case int:
		cValue = float64(v)
	case int64:
		cValue = float64(v)
	case int32:
		cValue = float64(v)
	case int16:
		cValue = float64(v)
	case int8:
		cValue = float64(v)
	case uint:
		cValue = float64(v)
	case uint64:
		cValue = float64(v)
	case uint32:
		cValue = float64(v)
	case uint16:
		cValue = float64(v)
	case uint8:
		cValue = float64(v)
	default:
		log.Printf("unsupported OPC UA value type for node %s: %#v", t.NodeID, value)
		return nil
	}

	sa := &forms.SignalA_v1a{}
	sa.NewForm()
	sa.Value = cValue
	sa.Unit = "undefined"
	sa.Timestamp = now
	return sa
}

// write pushes a new value to the configured OPC UA node. Booleans are sent
// verbatim (suitable for discrete inputs, coils, and BOOL PLC tags).
// SignalA numeric values arrive as float64 and are coerced to the node's
// actual DataType (captured at node registration) so an Int16 node doesn't
// reject a Double. Retries once on transient session/channel errors.
func (t *Traits) write(value interface{}) error {
	coerced, err := t.coerceForNode(value)
	if err != nil {
		return err
	}
	variant, err := ua.NewVariant(coerced)
	if err != nil {
		return fmt.Errorf("could not build OPC UA variant for %#v: %w", coerced, err)
	}

	req := &ua.WriteRequest{
		NodesToWrite: []*ua.WriteValue{
			{
				NodeID:      t.NodeID,
				AttributeID: ua.AttributeIDValue,
				Value: &ua.DataValue{
					EncodingMask: ua.DataValueValue,
					Value:        variant,
				},
			},
		},
	}

	var resp *ua.WriteResponse
	for attempt := 0; attempt < 2; attempt++ {
		resp, err = t.Server.Write(t.owner.Ctx, req)
		if err == nil {
			break
		}
		if errors.Is(err, ua.StatusBadSessionIDInvalid) ||
			errors.Is(err, ua.StatusBadSessionNotActivated) ||
			errors.Is(err, ua.StatusBadSecureChannelIDInvalid) {
			log.Printf("write: transient session error, retrying: %v", err)
			continue
		}
		break
	}
	if err != nil {
		return err
	}
	if resp == nil || len(resp.Results) == 0 {
		return fmt.Errorf("no write response")
	}
	if resp.Results[0] != ua.StatusOK {
		return fmt.Errorf("server rejected write: %v", resp.Results[0])
	}
	return nil
}

// coerceForNode converts the incoming Go value to the Go type that matches
// the OPC UA node's DataType string captured during registration. bool
// values pass through. Numeric values (always float64 on the SignalA path)
// are narrowed to the PLC's width. If DataType is unknown we pass the value
// through and let the server complain with a precise status code.
func (t *Traits) coerceForNode(value interface{}) (interface{}, error) {
	if b, ok := value.(bool); ok {
		return b, nil
	}
	f, ok := value.(float64)
	if !ok {
		return value, nil
	}
	switch t.DataType {
	case "int8":
		return int8(f), nil
	case "int16":
		return int16(f), nil
	case "int32":
		return int32(f), nil
	case "int64":
		return int64(f), nil
	case "byte", "uint8":
		return uint8(f), nil
	case "uint16":
		return uint16(f), nil
	case "uint32":
		return uint32(f), nil
	case "uint64":
		return uint64(f), nil
	case "float32":
		return float32(f), nil
	case "float64", "":
		return f, nil
	default:
		return f, nil
	}
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

	var nodes []NodeDef
	if def.NodeClass == ua.NodeClassVariable {
		nodes = append(nodes, def)
	}

	browseChildren := func(refType uint32) error {
		refs, err := n.ReferencedNodes(ctx, refType, ua.BrowseDirectionForward, ua.NodeClassAll, true)
		if err != nil {
			return errors.Errorf("References: %d: %s", refType, err)
		}
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
