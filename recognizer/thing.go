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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// ------------------------------------- Define the unit asset

// Traits holds the configurable parameters for the YOLO unit asset.
type Traits struct {
	FunctionalLocation string `json:"functionalLocation"` // optional: filter photograph service by location
	YOLOServiceURL     string `json:"yoloServiceURL"`     // URL of the YOLO microservice (default: http://localhost:5000)
	YOLOModel          string `json:"yoloModel"`          // model name forwarded to the service (default: yolov8n.pt)

	owner *components.System
	ua    *components.UnitAsset
}

// yoloResponse is the JSON body returned by POST /detect on the YOLO microservice.
type yoloResponse struct {
	Labels    []string `json:"labels"`
	Annotated string   `json:"annotated"` // base64-encoded JPEG
	Error     string   `json:"error,omitempty"`
}

// ------------------------------------- Instantiate a unit asset template

// initTemplate returns a template UnitAsset that seeds systemconfig.json on first run.
func initTemplate() *components.UnitAsset {
	recognizeSvc := components.Service{
		Definition:  "recognize",
		SubPath:     "recognize",
		Details:     map[string][]string{"Forms": {"FileForm_v1"}},
		RegPeriod:   30,
		Description: "triggers a photograph, runs YOLOv8 detection, and returns the annotated image URL (GET)",
	}

	return &components.UnitAsset{
		Name:    "YOLOv8",
		Mission: "object_detection",
		Details: map[string][]string{"Model": {"Ultralytics_YOLOv8"}},
		ServicesMap: components.Services{
			recognizeSvc.SubPath: &recognizeSvc,
		},
		Traits: &Traits{
			YOLOServiceURL: "http://localhost:5000",
			YOLOModel:      "yolov8n.pt",
		},
	}
}

// ------------------------------------- Instantiate a unit asset based on configuration

// newResource creates the runtime unit asset from the configuration file.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		YOLOServiceURL: "http://localhost:5000",
		YOLOModel:      "yolov8n.pt",
		owner:          sys,
	}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], t); err != nil {
			log.Println("recognizer: could not unmarshal traits:", err)
		}
	}

	// Cervice for the photograph service, optionally filtered by FunctionalLocation.
	photographDetails := map[string][]string{}
	if t.FunctionalLocation != "" {
		photographDetails["FunctionalLocation"] = []string{t.FunctionalLocation}
	}
	photographCer := &components.Cervice{
		Definition: "photograph",
		Protos:     components.SProtocols(sys.Husk.ProtoPort),
		Details:    photographDetails,
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "get",
	}

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: components.Cervices{"photograph": photographCer},
		Traits:      t,
	}
	t.ua = ua
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua, func() {}
}

// ------------------------------------- Pipeline

// runPipeline orchestrates the full recognition cycle:
//  1. GET the photograph service → FileForm_v1 with the source JPEG URL
//  2. Download the JPEG bytes
//  3. POST to the YOLO microservice → labels + annotated JPEG
//  4. Save the annotated JPEG to files/
//  5. Return a FileForm_v1 pointing to the annotated image
func (t *Traits) runPipeline() (*forms.FileForm_v1, error) {
	// Step 1: get a fresh photograph via the Arrowhead orchestrator.
	cer := t.ua.CervicesMap["photograph"]
	photoForm, err := usecases.GetState(cer, t.owner)
	if err != nil {
		return nil, fmt.Errorf("getting photograph: %w", err)
	}
	ff, ok := photoForm.(*forms.FileForm_v1)
	if !ok {
		return nil, fmt.Errorf("unexpected form type from photograph service: %T", photoForm)
	}
	log.Printf("recognizer: source image: %s\n", ff.FileURL)

	// Step 2: download the JPEG as bytes.
	imageData, err := downloadImage(ff.FileURL)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}

	// Step 3: send to the YOLO microservice.
	labels, annotatedJPEG, err := t.callYOLOService(imageData)
	if err != nil {
		return nil, fmt.Errorf("YOLO service: %w", err)
	}

	if len(labels) > 0 {
		log.Printf("recognizer: detected objects: %s\n", strings.Join(labels, ", "))
	} else {
		log.Println("recognizer: no objects detected")
	}

	// Step 4: save the annotated image to files/.
	if err := os.MkdirAll("files", 0o755); err != nil {
		return nil, fmt.Errorf("creating files directory: %w", err)
	}
	outFilename := fmt.Sprintf("annotated_%s.jpg", time.Now().Format("20060102-150405"))
	outPath := filepath.Join("files", outFilename)
	if err := os.WriteFile(outPath, annotatedJPEG, 0o644); err != nil {
		return nil, fmt.Errorf("saving annotated image: %w", err)
	}

	// Step 5: build the response form.
	host := t.owner.Husk.Host.IPAddresses[0]
	port := t.owner.Husk.ProtoPort["http"]
	annotatedURL := fmt.Sprintf("http://%s:%d/recognizer/%s/files/%s", host, port, t.ua.Name, outFilename)

	var result forms.FileForm_v1
	result.NewForm()
	result.FileURL = annotatedURL
	result.Timestamp = time.Now()
	return &result, nil
}

// callYOLOService posts the image to the YOLO microservice and returns the detected
// labels and the annotated JPEG bytes.
func (t *Traits) callYOLOService(imageData []byte) (labels []string, annotatedJPEG []byte, err error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	fw, err := mw.CreateFormFile("image", "image.jpg")
	if err != nil {
		return nil, nil, fmt.Errorf("building multipart request: %w", err)
	}
	if _, err := fw.Write(imageData); err != nil {
		return nil, nil, fmt.Errorf("writing image to request: %w", err)
	}
	if t.YOLOModel != "" {
		_ = mw.WriteField("model", t.YOLOModel)
	}
	mw.Close()

	resp, err := http.Post(t.YOLOServiceURL+"/detect", mw.FormDataContentType(), &body)
	if err != nil {
		return nil, nil, fmt.Errorf("YOLO service unreachable at %s: %w", t.YOLOServiceURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("YOLO service returned %s", resp.Status)
	}

	var result yoloResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, fmt.Errorf("decoding YOLO service response: %w", err)
	}
	if result.Error != "" {
		return nil, nil, fmt.Errorf("YOLO service error: %s", result.Error)
	}

	annotated, err := base64.StdEncoding.DecodeString(result.Annotated)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding annotated image: %w", err)
	}

	return result.Labels, annotated, nil
}

// downloadImage fetches a URL and returns the response body as bytes.
func downloadImage(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
