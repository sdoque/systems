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

import "errors"

// The buses are the Linux kernel's i2c-dev and spidev. Elsewhere the system
// builds, so its tests run on a development machine, and refuses to start.
var errNotLinux = errors.New("the I2C and SPI buses are read through Linux's i2c-dev and spidev; run the spiker on the Raspberry Pi")

func openI2C(device string, addr uint16) (bus, error) { return nil, errNotLinux }

func openSPI(device string, speedHz uint32) (bus, error) { return nil, errNotLinux }
