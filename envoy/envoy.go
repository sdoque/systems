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

// The envoy fetches the cloud's own descriptions on behalf of a person.
//
// A local cloud that enforces authorization has no place for a human being in
// it. The subject of every decision is the Common Name of a verified client
// certificate, and the CA issues one only to a binary whose hash the maitreD
// finds on the whitelist — which a browser, a curl, or an operator at a laptop
// can never be. So the moment a cloud adopts authorization, its owner is locked
// out of reading it: /cloudgraph and /cloudmodel answer 401 to the one person
// who is responsible for the whole thing.
//
// The tempting answers are both wrong. Declaring those services core-mission
// would make them bootstrap-exempt, but "core" is what the authorizer reasons
// about, and lying to it to gain access corrupts the vocabulary for everything
// else. Issuing a certificate to a person by hand would put a hole in the
// attestation chain the entire trust model rests on.
//
// This is the third answer: a binary that enrolls the ordinary way, is
// whitelisted like any other, is named in policy like any other, and can be
// refused like any other. It holds no privilege a person could not audit, and
// the operator reads what it wrote to disk rather than being let into the cloud.
// Access is delegated, not granted — which is what an envoy is.
package main

import (
	"context"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	dir := flag.String("dir", ".", "directory to write the captured files into")
	wait := flag.Duration("wait", 90*time.Second, "how long to wait for enrollment before giving up")
	stamp := flag.Bool("timestamp", true, "put a capture timestamp in each filename")
	serveMode := flag.Bool("serve", false, "stay up and serve the named services to a browser on this host")
	port := flag.Int("port", 8190, "loopback port to serve on with -serve")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `envoy — fetch the cloud's descriptions on behalf of a person

Usage:
  envoy [flags] <serviceDefinition>...

Examples:
  envoy cloudgraph cloudmodel
  envoy -dir /tmp/capture cloudgraph
  envoy -serve view cloudpicture

Every provider of each named service definition is discovered through the
orchestrator, read with the access token the orchestrator issues, and written
verbatim to one file per provider. Bodies are never parsed: what the provider
sent is what lands on disk.

With -serve the same reads are held open for a browser on this host instead of
written to disk: each service is published on the last segment of the provider's
own path, so a page fetching a relative URL finds it. The listener binds the
loopback interface, answers GET only, and forwards nothing that could actuate.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	definitions := flag.Args()
	if len(definitions) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("envoy", ctx)
	usecases.WatchShutdown(&sys, cancel)

	sys.Husk = &components.Husk{
		Description: "fetches the cloud's own descriptions on behalf of an operator",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		// A port is declared because the husk requires one, not because anything
		// is served on it: this process exits as soon as it has written its
		// files. See the note on registration below.
		ProtoPort: map[string]int{"https": 30190, "http": 20190, "coap": 0},
		InfoLink:  "https://github.com/sdoque/systems/tree/main/envoy",
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

	if _, err := usecases.Configure(&sys); err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	// Enrollment, and deliberately no registration.
	//
	// Every other system here registers what it provides, and the framework
	// treats that as an invariant — the authorizer resolves a subject's
	// attributes from its registry entry, and POLICY.md leans on "every system
	// provides at least one service" to guarantee they exist. This one provides
	// nothing: it is a person's hand reaching in, it runs for a few seconds, and
	// a service registered by a process that is about to exit is a stale record
	// somebody else will try to consume.
	//
	// That is safe only because no rule naming this subject may use
	// must_match_attribute — with no registry entry there are no attributes to
	// match, and such a rule would refuse everything. envoy's rule in
	// policies.json is unpaired for exactly this reason, and says so.
	usecases.RequestCertificate(&sys)

	// A capture gives up; a viewer waits.
	//
	// The timeout suits the one-shot shape: an operator ran this from a prompt
	// and is watching it, so failing after a minute and a half with a sentence
	// about the whitelist beats hanging. In -serve mode it is a system in
	// systems.txt like any other, and every other one retries until what it
	// needs appears — which matters most exactly when this fired: after a
	// redeploy, when every hash has changed and the maitreD is still serving a
	// cached whitelist for up to five minutes. Exiting there means the cloud
	// comes back without its viewer and nobody notices until they look.
	certReady := usecases.EnsureCertReady(&sys)
	if *serveMode {
		waiting := time.NewTicker(30 * time.Second)
		defer waiting.Stop()
		for ready := false; !ready; {
			select {
			case <-certReady:
				ready = true
			case <-waiting.C:
				log.Printf("envoy: still waiting for a certificate — is this binary on the whitelist the CA is serving?")
			case <-ctx.Done():
				return
			}
		}
	} else {
		select {
		case <-certReady:
		case <-time.After(*wait):
			log.Fatalf("no certificate after %v — is the CA running, and is this binary on the whitelist?", *wait)
		case <-ctx.Done():
			return
		}
	}

	if *serveMode {
		// Blocks until the process is stopped. Reaching the end of serve at all
		// means the listener failed, which is worth exiting non-zero for.
		log.Fatalf("envoy: %v", serve(&sys, definitions, *port))
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatalf("cannot write to %s: %v", *dir, err)
	}

	captured, failed := 0, 0
	for _, definition := range definitions {
		n, bad := capture(&sys, definition, *dir, *stamp)
		captured += n
		failed += bad
	}

	fmt.Printf("envoy: %d file(s) written to %s", captured, *dir)
	if failed > 0 {
		fmt.Printf(", %d provider(s) could not be read", failed)
	}
	fmt.Println()

	// Nothing captured is a failure, not a quiet success: a script that treats
	// an empty capture as a good one archives nothing and reports nothing.
	if captured == 0 {
		os.Exit(1)
	}
}
