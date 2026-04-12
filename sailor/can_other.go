/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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

//go:build !linux

// can_other.go provides stub implementations of the SocketCAN functions for
// non-Linux platforms (macOS, Windows).  This allows the package to compile
// and its pure-function tests (PGN decode, serving dispatcher, assetLoop)
// to run on a development machine without a Raspberry Pi + PiCAN-M.
//
// The real implementation is in can_linux.go.

package main

import (
	"context"
	"fmt"
)

func openCAN(ifname string) (int, error) {
	return 0, fmt.Errorf("SocketCAN is only supported on Linux (requested interface: %q)", ifname)
}

func closeCAN(_ int) {}

// canPoller is a no-op stub: it blocks until the context is cancelled so the
// goroutine started by newResource exits cleanly on shutdown.
func canPoller(ctx context.Context, _ int, _ []pgnSubscription) {
	<-ctx.Done()
}
