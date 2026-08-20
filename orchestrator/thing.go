/*******************************************************************************
 * Copyright (c) 2023 Jan van Deventer
 *
 * All rights reserved. This program and the accompanying materials
 * are made available under the terms of the Eclipse Public License v2.0
 * which accompanies this distribution, and is available at
 * http://www.eclipse.org/legal/epl-2.0/
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the Thing's resource

// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	// Both are read and written on the request path, and net/http gives every
	// request its own goroutine, so neither can be a plain string. A torn read
	// while a concurrent failure clears one builds a request against "/query"
	// with no host at all.
	leadingRegistrar  usecases.CachedURL
	leadingAuthorizer usecases.CachedURL
	// unchecked reports an unauthorized cloud once rather than per request, and
	// unidentified does the same for consumers the connection cannot name.
	unchecked    sync.Once
	unidentified sync.Once
	owner        *components.System `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	squest := components.Service{
		Definition:  "squest",
		SubPath:     "squest",
		Details:     map[string][]string{"DefaultForm": {"ServiceRecord_v1"}, "Methods": components.HTTPMethods("POST")},
		Description: "looks for the desired service described in a quest form (POST)",
	}

	return &components.UnitAsset{
		Name:    "orchestration",
		Mission: components.MissionCore,
		Details: map[string][]string{"Platform": {"Independent"}, "Mobility": {components.MobilityMovable}},
		Traits:  &Traits{},
		ServicesMap: components.Services{
			squest.SubPath: &squest,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the template
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner: sys,
	}

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	return ua, func() {
		log.Println("Ending orchestration services")
	}
}

//-------------------------------------Service handlers

// orchestrate receives a service discovery request and responds with the selected service location if found
func (t *Traits) orchestrate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			log.Println("Error parsing media type:", err)
			return
		}

		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error reading discovery request body: %v\n", err)
			return
		}

		questForm, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("error extracting the discovery request %v\n", err)
		}
		qf, ok := questForm.(*forms.ServiceQuest_v1)
		if !ok {
			log.Println("Problem unpacking the service discovery request form")
			return
		}

		subject, identified := usecases.PeerCN(r)
		if !identified {
			// Not a refusal: a cloud with no authorizer needs no certificate to
			// orchestrate, and this is the path it uses. Recorded so that when
			// the authorizer then refuses every candidate, the reason is in the
			// log next to it.
			t.noteUnidentified()
		}
		servLocation, err := t.getServiceURL(*qf, subject)
		if err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(servLocation)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

func (t *Traits) orchestrateMultiple(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			log.Println("Error parsing media type:", err)
			return
		}

		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error reading discovery request body: %v\n", err)
			return
		}

		questForm, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("error extracting the discovery request %v\n", err)
		}
		qf, ok := questForm.(*forms.ServiceQuest_v1)
		if !ok {
			log.Println("Problem unpacking the service discovery request form")
			return
		}

		subject, identified := usecases.PeerCN(r)
		if !identified {
			// As on the single-provider path: not a refusal, but recorded so the
			// authorizer's later refusals have their reason beside them.
			t.noteUnidentified()
		}
		servLocation, err := t.getServicesURL(*qf, subject)
		if err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(servLocation)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

//-------------------------------------Thing's resource functions

// getServiceURL retrieves the service URL for a given ServiceQuest_v1.
func (t *Traits) getServiceURL(newQuest forms.ServiceQuest_v1, subject string) (servLoc []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	registrar, err := t.leadingRegistrar.Resolve(func() (string, error) {
		return components.GetRunningCoreSystemURL(t.owner, "serviceregistrar")
	})
	if err != nil {
		return servLoc, err
	}

	mediaType := "application/json"
	jsonQF, err := usecases.Pack(&newQuest, mediaType)
	if err != nil {
		return servLoc, err
	}

	srURL := registrar + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", mediaType)
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.leadingRegistrar.Forget()
		return servLoc, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return servLoc, err
	}
	serviceListf, err := usecases.Unpack(respBytes, mediaType)
	if err != nil {
		return servLoc, err
	}

	serviceList, ok := serviceListf.(*forms.ServiceRecordList_v1)
	if !ok {
		return nil, fmt.Errorf("problem asserting the type of the service list form")
	}

	if len(serviceList.List) == 0 {
		return nil, fmt.Errorf("unable to locate any such service: %s", newQuest.ServiceDefinition)
	}

	permitted, tokens, err := t.authorized(subject, newQuest.Action, *serviceList)
	if err != nil {
		return nil, err
	}
	if len(permitted.List) == 0 {
		return nil, refusal(subject, newQuest.ServiceDefinition)
	}

	serviceLocation := selectService(permitted)
	// The token belongs to the provider that was chosen, so it is attached after
	// the choice rather than before it.
	serviceLocation.Token = tokens[serviceLocation.ServNode]
	payload, err := json.MarshalIndent(serviceLocation, "", "  ")
	return payload, err
}

// selectService picks a provider from the registrar's answer. The conversion to a
// service point is mbaigo's, so this path and the multi-service discovery path
// agree on the endpoint URL — in particular on reaching a provider over HTTPS
// when it has bound it. This used to build the URL here with "http://" and the
// http port hardcoded, which sent every consumer to the plain-HTTP endpoint and
// stripped its identity at the provider.
func selectService(serviceList forms.ServiceRecordList_v1) forms.ServicePoint_v1 {
	return usecases.ConvertToServicePoint(serviceList.List[0])
}

func (t *Traits) getServicesURL(newQuest forms.ServiceQuest_v1, subject string) (servLoc []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	registrar, err := t.leadingRegistrar.Resolve(func() (string, error) {
		return components.GetRunningCoreSystemURL(t.owner, "serviceregistrar")
	})
	if err != nil {
		return servLoc, err
	}

	mediaType := "application/json"
	jsonQF, err := usecases.Pack(&newQuest, mediaType)
	if err != nil {
		return servLoc, err
	}

	srURL := registrar + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", mediaType)
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.leadingRegistrar.Forget()
		return servLoc, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return servLoc, err
	}
	serviceListf, err := usecases.Unpack(respBytes, mediaType)
	if err != nil {
		return servLoc, err
	}

	serviceList, ok := serviceListf.(*forms.ServiceRecordList_v1)
	if !ok {
		return nil, fmt.Errorf("problem asserting the type of the service list form")
	}

	if len(serviceList.List) == 0 {
		return nil, fmt.Errorf("unable to locate any such service: %s", newQuest.ServiceDefinition)
	}

	// Asked of the authorizer, like the single-provider path beside it. This
	// path used to answer with the registrar's list untouched: it consulted no
	// policy, so a consumer was told about providers it may not use, and it
	// minted no tokens, so every request it then made was refused by any
	// provider in an authorized cloud. A consumer polling two temperature
	// sensors paid a full orchestration and two refusals on every cycle.
	permitted, tokens, err := t.authorized(subject, newQuest.Action, *serviceList)
	if err != nil {
		return nil, err
	}
	if len(permitted.List) == 0 {
		return nil, refusal(subject, newQuest.ServiceDefinition)
	}

	// Service points rather than records, because a record has nowhere to carry
	// the token that was just minted for it.
	var points forms.ServicePointList_v1
	points.NewForm()
	points.List = make([]forms.ServicePoint_v1, 0, len(permitted.List))
	for _, rec := range permitted.List {
		sp := usecases.ConvertToServicePoint(rec)
		sp.Token = tokens[sp.ServNode]
		points.List = append(points.List, sp)
	}

	payload, err := json.MarshalIndent(points, "", "  ")
	return payload, err
}

//-------------------------------------Authorization

// noteUnidentified reports, at most once a minute, that quests are arriving from
// consumers the connection cannot name.
func (t *Traits) noteUnidentified() {
	t.unidentified.Do(func() {
		log.Printf("orchestrator: service quests are arriving without a client certificate, so the consumer is unverified — an authorized cloud will refuse them\n")
	})
}

// refusal explains to the consumer why it got nothing, in the consumer's own
// log rather than only in the orchestrator's.
//
// An empty subject is not a policy decision, it is a misconfiguration, and the
// two look identical from the consumer's end: both arrive as "may not use any
// provider of X". The difference matters, because one is fixed by editing
// policies.json and the other by editing the consumer's own coreSystems entry —
// and an operator who reads "may not use" goes looking in the policy file,
// which is the one place the answer is not.
//
// A quest carries a subject only when it arrives over a connection that
// presented a client certificate, so a consumer whose orchestrator URL is http
// can never be named by any policy however the policy is written.
func refusal(subject, definition string) error {
	if subject == "" {
		return fmt.Errorf("this quest arrived without a client certificate, so no policy can name the "+
			"consumer: reach the orchestrator over https (its coreSystems entry) rather than http, "+
			"and no provider of %q can be granted until then", definition)
	}
	return fmt.Errorf("%q may not use any provider of %q", subject, definition)
}

// errNoAuthorizer says this local cloud declares no authorizer. It is a
// condition, not a fault: orchestration predates authorization and a cloud that
// has not adopted it still orchestrates.
var errNoAuthorizer = errors.New("no authorizer in this local cloud")

// authorized asks the authorizer which of the registrar's candidates the subject
// may use, and returns the survivors.
//
// **A cloud with no authorizer configured is not filtered.** Orchestration
// predates authorization, and making the authorizer a hard dependency of every
// deployment would break clouds that have not adopted it. The absence is
// reported once so it cannot pass for a working control: a cloud that expects to
// be authorized and quietly is not is the worst of the three states.
//
// A cloud that *does* declare an authorizer fails closed when it cannot be
// reached. Having named the gate, running without it is a fault, not a fallback.
func (t *Traits) authorized(subject, action string, candidates forms.ServiceRecordList_v1) (forms.ServiceRecordList_v1, map[string]string, error) {
	// An absent authorizer is not an error here — see the note above — so the
	// lookup reports it by returning no URL rather than by failing.
	authorizer, err := t.leadingAuthorizer.Resolve(func() (string, error) {
		url, err := components.GetRunningCoreSystemURL(t.owner, "authorizer")
		if err != nil || url == "" {
			return "", errNoAuthorizer
		}
		return url, nil
	})
	if err != nil {
		t.unchecked.Do(func() {
			log.Printf("orchestrator: no authorizer in this local cloud — orchestration is unfiltered\n")
		})
		return candidates, nil, nil
	}

	var quest forms.AuthorizationQuest_v1
	quest.NewForm()
	quest.RequesterName = t.owner.Name
	quest.Subject = subject
	quest.Action = action
	quest.Candidates = candidates.List

	mediaType := "application/json"
	body, err := usecases.Pack(&quest, mediaType)
	if err != nil {
		return forms.ServiceRecordList_v1{}, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authorizer+"/authorize", bytes.NewBuffer(body))
	if err != nil {
		return forms.ServiceRecordList_v1{}, nil, err
	}
	req.Header.Set("Content-Type", mediaType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.leadingAuthorizer.Forget() // look it up again next time
		return forms.ServiceRecordList_v1{}, nil, fmt.Errorf("the authorizer is unreachable: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return forms.ServiceRecordList_v1{}, nil, err
	}
	answerForm, err := usecases.Unpack(respBytes, mediaType)
	if err != nil {
		return forms.ServiceRecordList_v1{}, nil, err
	}
	answer, ok := answerForm.(*forms.AuthorizationGrantList_v1)
	if !ok {
		return forms.ServiceRecordList_v1{}, nil, fmt.Errorf("the authorizer answered with %T, not a grant list", answerForm)
	}

	for _, refusal := range answer.Refusals {
		log.Printf("orchestrator: %q refused %s: %s\n", subject, refusal.ServiceNode, refusal.Reason)
	}

	// An unnamed subject is refused everything by Decide's empty-subject check,
	// and the refusal then reads as "no policy permits this" — which sends an
	// operator to the policy file for a problem that is not in it.
	if subject == "" && len(answer.Grants) == 0 && len(answer.Refusals) > 0 {
		log.Printf("orchestrator: the consumer presented no verified certificate, so no policy can name it — this quest reached the authorizer over a connection with no client certificate\n")
	}

	var permitted forms.ServiceRecordList_v1
	permitted.NewForm()
	tokens := make(map[string]string, len(answer.Grants))
	for _, grant := range answer.Grants {
		permitted.List = append(permitted.List, grant.Record)
		tokens[grant.Record.ServiceNode] = grant.Token
	}
	return permitted, tokens, nil
}
