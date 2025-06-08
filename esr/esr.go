/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("serviceregistrar", ctx)

	// Instatiate the Capusle
	sys.Husk = &components.Husk{
		Description: "is an Arrowhead mandatory core system that keeps track of the currently available services.",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20102, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/esr",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
	}

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	assetName := assetTemplate.GetName()
	sys.UAssets[assetName] = &assetTemplate

	// Configure the system
	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("Configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = &ua
	}

	// Generate PKI keys and CSR to obtain a authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// start the http handler and server
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal, and gracefully close properly goroutines with context
	<-sys.Sigs // wait for a SIGINT (Ctrl+C) signal
	fmt.Println("\nshuting down system", sys.Name)
	cancel()                    // cancel the context, signaling the goroutines to stop
	time.Sleep(3 * time.Second) // allow the go routines to be executed, which might take more time than the main routine to end
}

// ---------------------------------------------------------------------------- end of main()

// Serving handles the resources services. NOTE: it expects those names from the request URL path
func (ua *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "register":
		ua.updateDB(w, r)
	case "query":
		ua.queryDB(w, r)
	case "unregister":
		ua.cleanDB(w, r)
	case "status":
		ua.roleStatus(w, r)
	case "syslist":
		ua.systemList(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// updateDB is used to add a new service record or to extend its registration life
func (ua *UnitAsset) updateDB(w http.ResponseWriter, r *http.Request) {
	if !ua.leading {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Service Unavailable"))
		return
	}
	switch r.Method {
	case "POST", "PUT":
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			fmt.Println("Error parsing media type:", err)
			return
		}

		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error reading registration request body: %v", err)
			return
		}
		record, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("error extracting the registration request %v\n", err)
			return
		}

		// Create a struct to send on a channel to handle the request
		addRecord := ServiceRegistryRequest{
			Action: "add",
			Record: record,
			Error:  make(chan error),
		}

		// Send request to add a record to the unit asset
		ua.requests <- addRecord
		// Check the error back from the unit asset
		err = <-addRecord.Error
		if err != nil {
			log.Printf("error adding the new service: %v", err)
			http.Error(w, "Error registering service", http.StatusInternalServerError)
			return
		}
		// fmt.Println(record)
		updatedRecordBytes, err := usecases.Pack(record, mediaType)
		if err != nil {
			log.Printf("error confirming new service: %s", err)
			http.Error(w, "Error registering service", http.StatusInternalServerError)
		}
		w.Header().Set("Content-Type", mediaType)
		w.WriteHeader(http.StatusOK)
		_, err = w.Write([]byte(updatedRecordBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

	default:
		fmt.Fprintf(w, "unsupported http request method")
	}
}

// queryDB looks for service records in the service registry
func (ua *UnitAsset) queryDB(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET": // from a web browser
		// Create a struct to send on a channel to handle the request
		recordsRequest := ServiceRegistryRequest{
			Action: "read",
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}

		// Send request to the `ua.requests` channel
		ua.requests <- recordsRequest

		// Use a select statement to wait for responses on either the Result or Error channel
		select {
		case err := <-recordsRequest.Error:
			if err != nil {
				log.Printf("Error retrieving service records: %v", err)
				http.Error(w, "Error retrieving service records", http.StatusInternalServerError)
			}
		case servvicesList := <-recordsRequest.Result:
			// Build the HTML response
			text := "<!DOCTYPE html><html><body>"
			w.Write([]byte(text))
			text = "<p>The local cloud's currently available services are:</p><ul>"
			w.Write([]byte(text))
			for _, serRec := range servvicesList {
				metaservice := ""
				for key, values := range serRec.Details {
					metaservice += key + ": " + fmt.Sprintf("%v", values) + " "
				}
				hyperlink := "http://" + serRec.IPAddresses[0] + ":" + strconv.Itoa(int(serRec.ProtoPort["http"])) + "/" + serRec.SystemName + "/" + serRec.SubPath
				parts := strings.Split(serRec.SubPath, "/")
				uaName := parts[0]
				sLine := "<p>Service ID: " + strconv.Itoa(int(serRec.Id)) + " with definition <b><a href=\"" + hyperlink + "\">" + serRec.ServiceDefinition + "</b></a> from the <b>" + serRec.SystemName + "/" + uaName + "</b> with details " + metaservice + " will expire at: " + serRec.EndOfValidity + "</p>"
				w.Write([]byte(fmt.Sprintf("<li>%s</li>", sLine)))
			}
			text = "</ul></body></html>"
			w.Write([]byte(text))
		case <-time.After(5 * time.Second): // Optional timeout
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Failure to process service listing request")
		}

	case "POST": // from the orchesrator
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			fmt.Println("Error parsing media type:", err)
			return
		}

		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error reading service discovery request body: %v", err)
			return
		}
		record, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("error extracting the service discovery request %v\n", err)
			return
		}

		// Create a struct to send on a channel to handle the request
		readRecord := ServiceRegistryRequest{
			Action: "read",
			Record: record,
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}

		// Send request to add a record to the unit asset
		ua.requests <- readRecord

		// Use a select statement to wait for responses on either the Result or Error channel
		select {
		case err := <-readRecord.Error:
			if err != nil {
				log.Printf("Error retrieving service records: %v", err)
				http.Error(w, "Error retrieving service records", http.StatusInternalServerError)
				return
			}
		case servvicesList := <-readRecord.Result:
			fmt.Println(servvicesList)
			var slForm forms.ServiceRecordList_v1
			slForm.NewForm()
			slForm.List = servvicesList
			updatedRecordBytes, err := usecases.Pack(&slForm, mediaType)
			if err != nil {
				log.Printf("error confirming new service: %s", err)
				http.Error(w, "Error registering service", http.StatusInternalServerError)
			}
			w.Header().Set("Content-Type", mediaType)
			w.WriteHeader(http.StatusOK)
			_, err = w.Write([]byte(updatedRecordBytes))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case <-time.After(5 * time.Second): // Optional timeout
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Failure to process service discovery request")
			return
		}
	default:
		http.Error(w, "Unsupported HTTP request method", http.StatusMethodNotAllowed)
	}
	// fmt.Println("Done querying the database")
}

// cleanDB deletes service records upon request (e.g., when a system shuts down)
func (ua *UnitAsset) cleanDB(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "DELETE":
		parts := strings.Split(r.URL.Path, "/")
		idStr := parts[len(parts)-1]   // the ID is the last part of the URL path
		id, err := strconv.Atoi(idStr) // convert the ID to an integer
		if err != nil {
			// handle the error
			http.Error(w, "Invalid record ID", http.StatusBadRequest)
			return
		}
		// Create a struct to send on a channel to handle the request
		addRecord := ServiceRegistryRequest{
			Action: "delete",
			Id:     int64(id),
			Error:  make(chan error),
		}

		// Send request to add a record to the unit asset
		ua.requests <- addRecord
		// Check the error back from the unit asset
		err = <-addRecord.Error
		if err != nil {
			log.Printf("error deleting the service with id: %d, %s\n", id, err)
			http.Error(w, "Error deleting service", http.StatusInternalServerError)
			return
		}
	default:
		fmt.Fprintf(w, "unsupported http request method")
	}
}

// roleStatus returns the current activity of a service registrar (i.e., leading or on stand by)
func (ua *UnitAsset) roleStatus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		if ua.leading {
			text := fmt.Sprintf("lead Service Registrar since %s", ua.leadingSince)
			fmt.Fprint(w, text)
			return
		}
		if ua.leadingRegistrar != nil {
			text := fmt.Sprintf("On standby, leading registrar is %s", ua.leadingRegistrar.Url)
			http.Error(w, text, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Service Unavailable"))
	default:
		fmt.Fprintf(w, "unsupported http request method")
	}
}

// Role repeatedly check which service registrar in the local cloud is the leading service registrar
func (ua *UnitAsset) Role() {
	peersList, err := peersList(ua.Owner)
	if err != nil {
		panic(err)
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		for {
			standby := false
		foundLead:
			for _, cSys := range peersList {
				resp, err := http.Get(cSys.Url + "/status")
				if err != nil {
					break // that system registrar is not up
				}
				defer resp.Body.Close()

				// Handle status codes
				switch resp.StatusCode {
				case http.StatusOK:
					standby = true
					ua.leading = false
					ua.leadingSince = time.Time{} // reset lead timer
					ua.leadingRegistrar = cSys
					break foundLead
				case http.StatusServiceUnavailable:
					// Service unavailable
				default:
					fmt.Printf("Received unexpected status code: %d\n", resp.StatusCode)
				}
			}
			if !standby && !ua.leading {
				ua.leading = true
				ua.leadingSince = time.Now()
				ua.leadingRegistrar = nil
				fmt.Printf("taking the service registry lead at %s\n", ua.leadingSince)
			}
			<-ticker.C
		}
	}()
}

// peerslist provides a list of the other service registrars in the local cloud
func peersList(sys *components.System) (peers []*components.CoreSystem, err error) {
	for _, cs := range sys.CoreS {
		if cs.Name != "serviceregistrar" {
			continue
		}
		u, err := url.Parse(cs.Url)
		if err != nil {
			return peers, err
		}
		uPort, err := strconv.Atoi(u.Port())
		if err != nil {
			fmt.Println(err)
		}
		if (u.Hostname() == sys.Host.IPAddresses[0] || u.Hostname() == "localhost") && uPort == sys.Husk.ProtoPort[u.Scheme] {
			continue
		}
		peers = append(peers, cs)
	}
	return peers, nil
}

// queryDB looks for service records in the service registry
func (ua *UnitAsset) systemList(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		systemsList, err := getUniqueSystems(ua)
		if err != nil {
			http.Error(w, fmt.Sprintf("system list error: %s", err), http.StatusInternalServerError)
			return
		}
		usecases.HTTPProcessGetRequest(w, r, systemsList)
	default:
		http.Error(w, "unsupported HTTP request method", http.StatusMethodNotAllowed)
	}
}
