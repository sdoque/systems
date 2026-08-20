/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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

// How loaded this machine is.
//
// The maitreD is the only system with a per-host identity — exactly one runs on
// each machine, and the CA reaches it by source address rather than through the
// registry — so it is where a per-host question belongs. A separate monitor
// system would have to solve "exactly one of these per host, enrolled and
// whitelisted" all over again to gain nothing.
//
// The cost is worth naming rather than glossing: this puts an operational
// reading beside an attestation duty, and attestation is what the whole trust
// chain rests on. Reading a few files under /proc and serving a struct is a
// small addition to that surface, but it is an addition.
package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

// Where the kernel keeps the answers. Variables rather than constants so a test
// can point them at a directory it controls: the readings are the whole of what
// this file does, and a sampler that can only be tested on Linux is one that is
// tested on nobody's laptop.
var (
	procLoadAvg  = "/proc/loadavg"
	procMeminfo  = "/proc/meminfo"
	procPressure = "/proc/pressure"
	sysThermal   = "/sys/class/thermal/thermal_zone0/temp"
)

// sample reads the host's current state.
//
// Every source here is a virtual file: the kernel formats a few numbers on
// read, there is no disk and no syscall storm, and the whole sample costs
// microseconds. That is what makes it honest to call this monitoring "without
// loading the host" — but it is only half the claim. The other half is that
// nothing calls this per request; see the sampler loop below.
//
// The second return says whether the host could measure itself at all. A
// machine with no /proc — a developer's Mac, a container with it masked —
// otherwise reported every figure as zero, which headroom then read as
// completely idle. That is the most dangerous answer available: a balancer
// would move work onto the one host that cannot say how loaded it is. Answering
// nothing is honest; answering "idle" is not.
func sample(host string) (forms.HostLoad_v1, bool) {
	var f forms.HostLoad_v1
	f.NewForm()
	f.Host = host
	f.Cores = runtime.NumCPU()
	f.SampledAt = time.Now()
	f.Timestamp = f.SampledAt

	f.Load1, f.Load5, f.Load15 = loadAverages()
	if f.Cores > 0 {
		f.LoadNormalized = f.Load1 / float64(f.Cores)
	}
	f.MemAvailableMB, f.MemTotalMB = memory()
	f.CPUTempC = temperature()
	f.ThrottledNow, f.ThrottledSince = throttling()
	f.StallCPU = pressure("cpu")
	f.StallIO = pressure("io")
	f.StallMemory = pressure("memory")

	f.Headroom = headroom(&f)

	// Load average is the one reading every Linux host has. Without it, nothing
	// here was measured and the zeros are absence rather than idleness.
	_, err := os.Stat(procLoadAvg)
	return f, err == nil
}

// headroom reduces the reading to one number between 0 and 1, because a
// balancer comparing a Raspberry Pi with a server cannot compare load averages
// and only each host knows what its own figures mean.
//
// The worst of several constraints rather than an average of them: a machine
// with spare CPU and no memory is not half-available, it is unavailable, and
// averaging would report it as comfortable. Whichever resource is scarcest is
// the one that decides.
//
// Pressure is preferred over load when the kernel reports it. Stall says how
// much work was actually delayed; load average says how much work exists, which
// is a proxy for the same thing and a poor one across different core counts.
func headroom(f *forms.HostLoad_v1) float64 {
	worst := 1.0
	consider := func(free float64) {
		if free < worst {
			worst = free
		}
	}

	if f.StallCPU != nil {
		// Stall is a percentage of time delayed; what is left is what is free.
		consider(1 - *f.StallCPU/100)
	} else if f.Cores > 0 {
		// Load per core: 1.0 is as much runnable work as there are cores, and
		// beyond that the machine is oversubscribed rather than merely busy.
		consider(1 - f.LoadNormalized)
	}
	if f.StallMemory != nil {
		consider(1 - *f.StallMemory/100)
	}
	if f.MemTotalMB > 0 {
		consider(float64(f.MemAvailableMB) / float64(f.MemTotalMB))
	}

	// Thermal throttling is not a degree of busy-ness, it is a machine that has
	// stopped delivering what its load average implies. Reporting anything but
	// "nearly full" would invite a balancer to move work onto it.
	if f.ThrottledNow {
		consider(0.1)
	}

	if worst < 0 {
		return 0
	}
	if worst > 1 {
		return 1
	}
	return worst
}

// loadAverages reads the one-, five- and fifteen-minute averages.
func loadAverages() (one, five, fifteen float64) {
	data, err := os.ReadFile(procLoadAvg)
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	one, _ = strconv.ParseFloat(fields[0], 64)
	five, _ = strconv.ParseFloat(fields[1], 64)
	fifteen, _ = strconv.ParseFloat(fields[2], 64)
	return one, five, fifteen
}

// memory reports what a new process could obtain, and the total.
//
// MemAvailable rather than MemFree: free counts none of the reclaimable page
// cache, so a healthy Linux machine that has read a few files looks exhausted.
// The kernel computes MemAvailable precisely so that callers stop getting this
// wrong.
func memory() (availableMB, totalMB int) {
	data, err := os.ReadFile(procMeminfo)
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemAvailable:":
			availableMB = kb / 1024
		case "MemTotal:":
			totalMB = kb / 1024
		}
	}
	return availableMB, totalMB
}

// temperature reads the CPU thermal zone, in degrees Celsius.
func temperature() float64 {
	data, err := os.ReadFile(sysThermal)
	if err != nil {
		return 0
	}
	milli, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0
	}
	return milli / 1000
}

// pressure reads one of the kernel's pressure-stall figures, or nil when the
// kernel does not report them.
//
// nil rather than zero, and the form carries a pointer for the same reason: a
// stall of 0.0 means nothing was delayed, which is the opposite conclusion from
// "this kernel does not measure". A balancer reading zero from a machine that
// never looked would move work onto it believing it idle.
//
// The "avg10" figure is used — the share of the last ten seconds in which work
// was delayed — because a balancer decides now and a longer window would answer
// a question about the past.
func pressure(resource string) *float64 {
	data, err := os.ReadFile(procPressure + "/" + resource)
	if err != nil {
		return nil // CONFIG_PSI absent, which is stock Raspberry Pi OS
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some") {
			continue
		}
		for _, field := range strings.Fields(line) {
			name, value, found := strings.Cut(field, "=")
			if !found || name != "avg10" {
				continue
			}
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil
			}
			return &v
		}
	}
	return nil
}

// throttling reports whether the board is derating now, and whether it has done
// so since boot.
//
// Two answers because they inform different decisions: whether to avoid this
// host today, and whether it has a cooling problem somebody should fix. The
// firmware's bit 2 is "currently throttled" and bit 18 is the sticky "has been
// throttled since boot".
//
// Read from the sysfs interface rather than by running vcgencmd, because
// spawning a process to answer "am I busy?" is the one implementation that
// would make the question dishonest. A machine that is not a Raspberry Pi
// reports neither, which is correct rather than a failure.
func throttling() (now, since bool) {
	for _, path := range []string{
		"/sys/devices/platform/soc/soc:firmware/get_throttled",
		"/sys/class/thermal/cooling_device0/cur_state",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		text = strings.TrimPrefix(text, "0x")
		bits, err := strconv.ParseUint(text, 16, 64)
		if err != nil {
			if plain, errPlain := strconv.ParseUint(text, 10, 64); errPlain == nil {
				bits = plain
			} else {
				continue
			}
		}
		return bits&(1<<2) != 0, bits&(1<<18) != 0
	}
	return false, false
}
