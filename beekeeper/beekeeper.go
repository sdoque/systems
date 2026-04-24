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
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("beekeeper", ctx)

	sys.Husk = &components.Husk{
		Description: "exposes ZigBee devices paired to a RaspBee II / deCONZ gateway as Arrowhead services",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20185, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/beekeeper",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
		RegistrarChan: make(chan *components.CoreSystem, 1),
		Messengers:    make(map[string]int),
	}

	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset)

	if len(rawResources) == 0 {
		log.Fatal("beekeeper: no unit asset configuration found in systemconfig.json")
	}

	var uac usecases.ConfigurableAsset
	if err := json.Unmarshal(rawResources[0], &uac); err != nil {
		log.Fatalf("resource configuration error: %v\n", err)
	}
	assets, cleanup := newResources(uac, &sys)
	defer cleanup()
	for _, ua := range assets {
		sys.UAssets[ua.GetName()] = ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Sigs
	fmt.Println("\nshutting down system", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

// serving handles incoming HTTP requests for a ZigBee device unit asset.
// GET returns the cached measurement. PUT on on_off forwards the command to deCONZ.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch r.Method {
	case http.MethodGet:
		m := t.cache.get(t.assetName, servicePath)
		if m == nil {
			http.Error(w, "measurement not yet available", http.StatusServiceUnavailable)
			return
		}
		if m.IsBool {
			var f forms.SignalB_v1a
			f.NewForm()
			f.Value = m.BoolValue
			f.Timestamp = m.Timestamp
			usecases.HTTPProcessGetRequest(w, r, &f)
		} else {
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = m.Value
			f.Unit = serviceUnit(servicePath)
			f.Timestamp = m.Timestamp
			usecases.HTTPProcessGetRequest(w, r, &f)
		}

	case http.MethodPut:
		if servicePath != "on_off" {
			http.Error(w, "PUT is only supported for on_off", http.StatusMethodNotAllowed)
			return
		}
		if t.lightID == "" {
			http.Error(w, "no controllable light for this asset", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
			return
		}
		f, err := usecases.Unpack(body, r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "unpack: "+err.Error(), http.StatusBadRequest)
			return
		}
		sig, ok := f.(*forms.SignalB_v1a)
		if !ok {
			http.Error(w, "expected SignalB_v1a body", http.StatusBadRequest)
			return
		}
		if err := setLightState(t.cfg, t.lightID, sig.Value); err != nil {
			log.Printf("beekeeper: setLightState %s: %v\n", t.assetName, err)
			http.Error(w, "deCONZ error: "+err.Error(), http.StatusBadGateway)
			return
		}
		// Update the local cache immediately.
		v := 0.0
		if sig.Value {
			v = 1.0
		}
		t.cache.update(t.assetName, map[string]float64{"on_off": v}, time.Now())

		// Echo back the acknowledged state so callers using SetState get a valid response.
		sig.Timestamp = time.Now()
		usecases.HTTPProcessGetRequest(w, r, sig)

	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

// setLightState sends an on/off command to deCONZ for the given light ID.
func setLightState(cfg DeconzConfig, lightID string, on bool) error {
	url := fmt.Sprintf("%s/lights/%s/state", cfg.apiBase(), lightID)
	payload, _ := json.Marshal(map[string]bool{"on": on})
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deCONZ returned %s", resp.Status)
	}
	return nil
}

// serviceUnit returns the physical unit for a given service subpath.
func serviceUnit(s string) string {
	if spec, ok := serviceSpecs[s]; ok {
		return spec.unit
	}
	return ""
}
