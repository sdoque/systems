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
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// PoliciesFile is the operator-edited rule set, read from the authorizer's
// working directory.
const PoliciesFile = "policies.json"

//-------------------------------------Define the unit asset

// Traits holds the loaded policy set and what is needed to resolve a subject's
// attributes from the service registrar.
type Traits struct {
	owner *components.System

	// orchestrator is the common name the orchestrator enrolls under, taken from
	// this asset's configured details. Empty means the framework's default.
	// Written once at construction and read on the request path.
	orchestrator string

	mu       sync.RWMutex
	policies Policies
	loadedAt time.Time // modification time of the file the policies came from
	// lastPlaintextNote rate-limits the report that quests arrive with no
	// verifiable asker. Guarded by mu.
	lastPlaintextNote time.Time

	// Read and written on the request path — Adjudicate resolves a subject's
	// attributes through the registrar — and net/http gives every request its
	// own goroutine, so this cannot be a plain string.
	leadingRegistrar components.CachedURL

	// attributesOf resolves a subject's attributes. It is a field rather than a
	// direct call so a decision can be exercised without a registrar: the
	// adjudication logic is worth testing on its own, and reaching the network
	// from it would make that impossible.
	attributesOf func(subject string) map[string][]string
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	authorize := components.Service{
		Definition:  "authorize",
		SubPath:     "authorize",
		Details:     map[string][]string{"Forms": {"AuthorizationQuest_v1"}},
		RegPeriod:   30,
		Description: "decides (POST) which of a set of candidate providers a subject may use",
	}

	return &components.UnitAsset{
		Name:    "authorization",
		Mission: components.MissionCore,
		Details: map[string][]string{"Platform": {"Independent"}},
		ServicesMap: components.Services{
			authorize.SubPath: &authorize,
		},
		Traits: &Traits{},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the unit asset and loads the policy file.
//
// A missing policy file is not a startup failure: an authorizer with no policies
// denies everything, which is the correct state for a cloud that has not been
// commissioned yet. It is logged loudly, because "nothing is authorized" and
// "the authorizer is misconfigured" look identical from the outside.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{owner: sys}
	t.attributesOf = t.subjectAttributes
	// From the asset's details in systemconfig.json, which is a file an operator
	// can actually write. It used to be read from Husk.Details, a map nothing
	// sets from configuration and which no documentation mentions — so the name
	// it was meant to make configurable stayed hardcoded in practice.
	if names := configuredAsset.Details["OrchestratorName"]; len(names) > 0 && names[0] != "" {
		t.orchestrator = names[0]
	}

	if err := t.reloadPolicies(); err != nil {
		log.Fatalf("authorizer: %v\n", err)
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
		log.Println("authorizer: closing")
	}
}

//-------------------------------------Policy loading

// reloadPolicies reads the policy file if it has changed since the last read.
//
// Re-reading on change rather than only at startup is what makes POLICY.md's
// revocation semantics true: an edit takes effect for every subsequent decision,
// and tokens already issued stay valid only until their TTL expires. A file that
// exists but cannot be parsed is an error and leaves the previous rules in
// place — reverting to "deny everything" on a typo would take a plant down.
func (t *Traits) reloadPolicies() error {
	info, err := os.Stat(PoliciesFile)
	if os.IsNotExist(err) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if !t.loadedAt.IsZero() || len(t.policies.Rules) == 0 {
			log.Printf("authorizer: no %s — every request will be denied until one is written\n", PoliciesFile)
		}
		t.policies = Policies{}
		t.loadedAt = time.Time{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", PoliciesFile, err)
	}

	t.mu.RLock()
	unchanged := info.ModTime().Equal(t.loadedAt)
	t.mu.RUnlock()
	if unchanged {
		return nil
	}

	data, err := os.ReadFile(PoliciesFile)
	if err != nil {
		return fmt.Errorf("reading %s: %w", PoliciesFile, err)
	}
	loaded, err := LoadPolicies(data)
	if err != nil {
		return fmt.Errorf("%s: %w", PoliciesFile, err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.policies = loaded
	t.loadedAt = info.ModTime()
	log.Printf("authorizer: loaded %d policies and %d denials from %s\n",
		len(loaded.Rules), len(loaded.Denials), PoliciesFile)
	return nil
}

// currentPolicies returns the rule set to decide against, refreshing it first if
// the operator has edited the file.
func (t *Traits) currentPolicies() Policies {
	if err := t.reloadPolicies(); err != nil {
		// Keep deciding on the rules already in memory: a broken edit must not
		// silently widen or narrow what is permitted.
		log.Printf("authorizer: keeping the previous policies: %v\n", err)
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.policies
}

//-------------------------------------Service handlers

// serving handles the resource's services.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "authorize":
		t.authorize(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// authorize answers one authorization quest.
func (t *Traits) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		log.Printf("authorizer: parsing media type: %v\n", err)
		http.Error(w, "unreadable content type", http.StatusBadRequest)
		return
	}

	defer r.Body.Close()
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("authorizer: reading request body: %v\n", err)
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}

	questForm, err := usecases.Unpack(bodyBytes, mediaType)
	if err != nil {
		log.Printf("authorizer: unpacking the authorization quest: %v\n", err)
		http.Error(w, "unreadable authorization quest", http.StatusBadRequest)
		return
	}
	quest, ok := questForm.(*forms.AuthorizationQuest_v1)
	if !ok {
		http.Error(w, "unexpected form: an AuthorizationQuest_v1 is required", http.StatusBadRequest)
		return
	}

	// Who is asking, when the connection can say. The orchestrator is the only
	// system that calls this service, and it asks on another system's behalf, so
	// the quest names a subject the peer is not. What the peer certificate
	// establishes is that the *asker* is the orchestrator.
	//
	// Over plain HTTP there is no certificate to check, and what to do about
	// that depends on whether this authorizer is listening on TLS at all.
	//
	// The earlier reasoning here was wrong. It said refusing "would break every
	// default deployment to close a hole that only exists in deployments which
	// have not adopted TLS anyway". The premise does not hold: SetoutServers
	// binds the HTTP port unconditionally and never withdraws it, so a fully
	// enrolled cloud still answers on 20104. The hole was therefore open in
	// exactly the deployments that believe they are protected, and what was
	// reachable through it is an unauthenticated signing endpoint — anyone with
	// network reach could post any subject and any candidate and receive a
	// signed token plus the policy's reasons for every refusal.
	//
	// So: once this authorizer is serving on TLS, a quest that did not come
	// over it is refused. Before that — during enrollment, or in a cloud with
	// no HTTPS port — it is accepted and reported, because there is no better
	// channel for the orchestrator to have used and refusing would stop
	// orchestration cloud-wide for a gap the cloud has not yet had the means to
	// close.
	asker, verified := usecases.PeerCN(r)
	if verified && asker != t.owner.Name && asker != t.orchestratorCN() {
		log.Printf("authorizer: refusing an authorization quest from %q: only the orchestrator may ask\n", asker)
		http.Error(w, "only the orchestrator may request authorization", http.StatusForbidden)
		return
	}
	if !verified {
		if tlsPort, expected := t.expectsTLS(); expected {
			log.Printf("authorizer: refusing an authorization quest that arrived over plain HTTP while %d is for TLS\n", tlsPort)
			http.Error(w, fmt.Sprintf(
				"this authorizer requires TLS: reach it at https://<host>:%d/%s/ and give that URL "+
					"as the authorizer's coreSystems entry", tlsPort, t.owner.Name),
				http.StatusForbidden)
			return
		}
		t.notePlaintextQuest()
	}

	grants := t.Adjudicate(*quest)

	payload, err := usecases.Pack(&grants, mediaType)
	if err != nil {
		log.Printf("authorizer: packing the answer: %v\n", err)
		http.Error(w, "could not encode the answer", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		log.Printf("authorizer: writing the answer: %v\n", err)
	}
}

// orchestratorCN reports the common name the orchestrator enrolls under, which
// this authorizer accepts as the one legitimate asker.
//
// Configurable, because sys.Name is: a system renamed in its configuration file
// enrolls under that name, and two clouds sharing a host is a reason to rename
// one. Hardcoding it meant such a cloud got 403 on every quest, and since
// authorized() fails closed against a reachable-but-refusing authorizer, that
// stopped orchestration cloud-wide.
func (t *Traits) orchestratorCN() string {
	if t.orchestrator != "" {
		return t.orchestrator
	}
	return usecases.OrchestratorName
}

// expectsTLS reports whether this authorizer should be reached over TLS, and on
// which port.
//
// Latched rather than current, which is the difference between a security
// control and a coincidence.
//
// Reading the current port meant the refusal could switch itself off. The
// HTTPS server releases its entry when it returns, so an authorizer that had
// been refusing plaintext for weeks would start accepting it again after a cert
// rotation or a cancelled context, while continuing to serve on the plain port.
// EverBound is the latch: once TLS has been served, it stays served as far as
// this decision is concerned.
// The window before TLS first binds is deliberately still open: there is no
// better channel for the orchestrator to have used, and refusing would stop
// orchestration cloud-wide for a gap the cloud has not yet had the means to
// close. It is reported instead, by notePlaintextQuest.
func (t *Traits) expectsTLS() (int, bool) {
	if port, ok := t.owner.Husk.Bound.EverBound("https"); ok && port != 0 {
		return port, true
	}
	return 0, false
}

// notePlaintextQuest reports, at most once a minute, that quests are arriving
// with no verifiable asker.
//
// Rate-limited rather than silent: one line per orchestration would drown the
// log of a working cloud, and no line at all is how an operator comes to believe
// the gate is checking something it is not.
func (t *Traits) notePlaintextQuest() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.lastPlaintextNote) < time.Minute {
		return
	}
	t.lastPlaintextNote = time.Now()
	log.Printf("authorizer: authorization quests are arriving without a client certificate, so the asking system is unverified — this cloud reaches the authorizer over plain HTTP. Once this system is serving TLS, such quests are refused.\n")
}

//-------------------------------------Unit asset's functionalities

// Adjudicate decides every candidate in a quest and reports both what was
// permitted and what was refused.
//
// The subject's attributes are resolved once for the whole quest, from the
// registry rather than from anything in the request: the caller states which
// providers it is asking about, never who it is or where it sits.
func (t *Traits) Adjudicate(quest forms.AuthorizationQuest_v1) forms.AuthorizationGrantList_v1 {
	var answer forms.AuthorizationGrantList_v1
	answer.NewForm()

	now := time.Now()
	policies := t.currentPolicies()

	resolve := t.attributesOf
	if resolve == nil {
		resolve = t.subjectAttributes
	}
	attributes := resolve(quest.Subject)

	for _, candidate := range quest.Candidates {
		decision := Decide(policies, Request{
			Subject:           quest.Subject,
			SubjectAttributes: attributes,
			Action:            quest.Action,
			Record:            candidate,
		})

		if !decision.Allowed {
			answer.Refusals = append(answer.Refusals, forms.AuthorizationRefusal_v1{
				ProviderName: candidate.SystemName,
				ServiceNode:  candidate.ServiceNode,
				Reason:       decision.Reason,
			})
			continue
		}

		token, err := t.mint(quest, candidate, decision.TTL, now)
		if err != nil {
			// An authorizer that cannot sign cannot authorize. Refusing is the
			// only honest answer: a grant without a token would be a permission
			// no provider can check.
			answer.Refusals = append(answer.Refusals, forms.AuthorizationRefusal_v1{
				ProviderName: candidate.SystemName,
				ServiceNode:  candidate.ServiceNode,
				Reason:       err.Error(),
			})
			continue
		}

		answer.Grants = append(answer.Grants, forms.AuthorizationGrant_v1{
			Record: candidate,
			Token:  token,
			TTL:    decision.TTL.String(),
			Reason: decision.Reason,
		})
	}

	log.Printf("authorizer: %q may use %d of %d candidates for %s\n",
		quest.Subject, len(answer.Grants), len(quest.Candidates), quest.Action)
	return answer
}

// mint signs the permission just granted.
//
// Every claim narrows the token to one thing: this subject, reaching this
// provider's asset, through this service, with this action, until it expires. A
// token that named fewer of them would be a credential rather than a permission.
//
// The asset is taken from the record's SubPath, which is "<asset>/<subpath>", so
// the claim names the unit asset the provider will dispatch to.
func (t *Traits) mint(quest forms.AuthorizationQuest_v1, candidate forms.ServiceRecord_v1, ttl time.Duration, now time.Time) (string, error) {
	key := t.signingKey()
	if key == nil {
		return "", fmt.Errorf("the authorizer has no signing key yet: it is still enrolling with the CA")
	}

	assetName := AssetOf(candidate)
	if i := strings.Index(assetName, "/"); i >= 0 {
		assetName = assetName[i+1:]
	}

	claims := forms.AccessToken_v1{
		Subject:  quest.Subject,
		Provider: candidate.SystemName,
		Asset:    assetName,
		Service:  candidate.ServiceDefinition,
		Action:   quest.Action,
		IssuedAt: now,
		Expires:  now.Add(ttl),
		Issuer:   t.issuer(),
	}
	return usecases.MintToken(key, claims)
}

// signingKey is the authorizer's own private key, generated in memory when it
// enrolled with the CA. It is nil until enrollment completes, which is why mint
// can fail.
func (t *Traits) signingKey() *ecdsa.PrivateKey {
	if t.owner == nil || t.owner.Husk == nil {
		return nil
	}
	return t.owner.Husk.Pkey
}

func (t *Traits) issuer() string {
	if t.owner == nil {
		return usecases.AuthorizerName
	}
	return t.owner.Name
}

// subjectAttributes reads the consumer's own details out of the service
// registrar, by asking what that system provides.
//
// Attributes live on unit assets and reach the registry only through service
// records, so "where is the thermostat" is answered by looking at the records
// the thermostat registered. This is what the framework invariant — every system
// provides at least one service — exists to guarantee. A subject with no records
// yields no attributes, and the pairing rule then refuses it any located asset,
// which is the correct reading of a system that never registered.
func (t *Traits) subjectAttributes(subject string) map[string][]string {
	if subject == "" {
		return nil
	}

	records, err := t.recordsOf(subject)
	if err != nil {
		log.Printf("authorizer: could not resolve %q's attributes: %v\n", subject, err)
		return nil
	}

	attributes := make(map[string][]string)
	for _, rec := range records {
		for key, values := range rec.Details {
			for _, v := range values {
				if !contains(attributes[key], v) {
					attributes[key] = append(attributes[key], v)
				}
			}
		}
	}
	return attributes
}

// recordsOf asks the leading service registrar for everything one system
// provides.
func (t *Traits) recordsOf(systemName string) ([]forms.ServiceRecord_v1, error) {
	if t.owner == nil {
		return nil, fmt.Errorf("the authorizer has no owning system to find a registrar from")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	registrar, err := t.leadingRegistrar.Resolve(func() (string, error) {
		return components.GetRunningCoreSystemURL(t.owner, components.ServiceRegistrarName)
	})
	if err != nil {
		return nil, err
	}

	quest := forms.ServiceQuest_v1{
		RequesterName: t.owner.Name,
		ProviderName:  systemName,
		Version:       "ServiceQuest_v1",
	}
	mediaType := "application/json"
	body, err := usecases.Pack(&quest, mediaType)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrar+"/query", bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mediaType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.leadingRegistrar.Forget() // find the leading registrar again next time
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	listForm, err := usecases.Unpack(respBytes, mediaType)
	if err != nil {
		return nil, err
	}
	list, ok := listForm.(*forms.ServiceRecordList_v1)
	if !ok {
		return nil, fmt.Errorf("the registrar answered with %T, not a service record list", listForm)
	}
	return list.List, nil
}

// writePoliciesTemplate is used by the tests and by an operator wanting a
// starting point; it is never called at startup, because generating a policy
// file would mean inventing permissions nobody granted.
func writePoliciesTemplate(path string) error {
	starter := Policies{
		Rules: []Rule{{
			Subject:            "thermostat-*",
			Missions:           []string{components.MissionMeasurement.String()},
			Actions:            []string{ActionRead},
			MustMatchAttribute: "FunctionalLocation",
			TTL:                "5m",
		}},
	}
	data, err := json.MarshalIndent(starter, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
