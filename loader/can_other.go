//go:build !linux

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

package main

import "fmt"

// canFrame is declared here too so the rest of the system compiles off Linux.
type canFrame struct {
	ID   uint32
	DLC  uint8
	Pad  uint8
	Res0 uint8
	Res1 uint8
	Data [8]byte
}

func openCAN(ifname string) (int, error) {
	return 0, fmt.Errorf("SocketCAN is only supported on Linux (requested interface: %q)", ifname)
}

func closeCAN(_ int) {}

func sendCAN(_ int, _ uint32, _ []byte) error {
	return fmt.Errorf("SocketCAN is only supported on Linux")
}
