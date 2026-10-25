/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

// The vigie — the sailor on watch at the masthead — gives a cloud a view all
// round: a Livox Mid-360 3D lidar, with the IMU inside it, as a unit asset.
//
// Like the guetteur it is the sensor and nothing more. It knows the lidar's
// network protocol and where on the vehicle the lidar is mounted, and it
// publishes what a consumer can act on: a horizontal scan in the form the
// cartographer already reads, the clearance ahead at any height the vehicle
// could hit, and the vehicle's rate of turn and tilt.
package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	probe := flag.Bool("probe", false, "find the lidar, print what it says about itself, and exit — changes nothing")
	lidarIP := flag.String("lidar", "", "with -probe: the lidar's address, instead of finding it by broadcast")
	record := flag.String("record", "", "write every datagram received to this file, for replay")
	flag.Parse()

	if *probe {
		os.Exit(runProbe(*lidarIP))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("vigie", ctx)
	usecases.WatchShutdown(&sys, cancel)

	sys.Husk = &components.Husk{
		Description: "a Livox Mid-360 3D lidar and its IMU: a scan all round, the clearance ahead, the rate of turn and the tilt",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30205, "http": 20205, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/vigie",
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
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys, *record)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(2 * time.Second)
}

// runProbe finds the lidar and prints what it says, without changing any of
// its settings. It is the first thing to run on a vehicle: it answers whether
// the lidar is there, on which address, where it is sending its data, and
// whether anything is wrong with it.
func runProbe(ip string) int {
	if ip == "" {
		conn, err := openCommand()
		if err != nil {
			fmt.Println(err)
			return 1
		}
		fmt.Println("Broadcasting a discovery request on every interface...")
		found, err := discover(conn, 3*time.Second)
		conn.Close()
		if err != nil {
			fmt.Println(err)
			return 1
		}
		if len(found) == 0 {
			fmt.Println("No Livox device answered. Is the lidar powered (9–27 V) and its Ethernet cable on a port of this machine?")
			fmt.Println("Its default address is 192.168.1.1XX (the last two digits of its serial number); this machine needs")
			fmt.Println("an address on 192.168.1.x on that port, 192.168.1.50 by Livox's instructions.")
			return 1
		}
		for _, f := range found {
			kind := fmt.Sprintf("Livox device type %d", f.devType)
			if f.devType == devTypeMid360 {
				kind = "Mid-360"
			}
			fmt.Printf("Found a %s, serial %s, at %v (command port %d)\n", kind, f.serial, f.ip, f.cmdPort)
		}
		ip = found[0].ip.String()
	}
	l, err := connect(ip)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer l.conn.Close()
	if host, err := hostAddressFor(l.addr.IP); err == nil {
		fmt.Printf("This machine reaches it from %v.\n", host)
	}
	fmt.Printf("The lidar at %v says:\n%s", l.addr.IP, l.describe())
	return 0
}
