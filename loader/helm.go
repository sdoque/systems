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

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The helm decides who drives.
//
// Before it, the loader followed whichever system had spoken last, so a stop
// from one controller was followed by the next setpoint from another and meant
// nothing. The rules now are those of two pilots sharing an aircraft:
//
//   - One system has control at a time, and only its setpoints are obeyed.
//   - Control is taken and released explicitly. Nobody drives by merely
//     sending a number.
//   - A system with priority (the gamepad: a person) may take control from
//     anyone at any time.
//   - Anyone may stop the vehicle, whoever has control. A stop takes control
//     away from everyone, and only a system with priority can take it back.
//     A stop is therefore never undone by the software it was meant to stop.
//   - A pilot that goes silent is a stop, not a handover.
//   - The vehicle starts stopped, so the first system to drive it after
//     power-up is one with priority, which then hands over by releasing.
//
// Identity is the certificate the caller presented. In a cloud with an
// authorizer every request has one, because the framework refuses the rest
// before they arrive here. In a cloud with no certificates at all, nobody can be
// told apart, and every caller counts as one and the same pilot with priority:
// the rules still sequence a bench session, but they do not protect anything,
// and the loader says so. A loader that holds a certificate does not extend
// that to a caller who presents none; such a caller may stop the vehicle and do
// nothing else.

// caller is who is asking.
type caller struct {
	name  string
	known bool // the name came from a verified certificate
}

func (c caller) String() string {
	if !c.known {
		return "an unidentified caller"
	}
	return c.name
}

// refusal is a request the helm will not grant, and why.
type refusal struct{ reason string }

func (r *refusal) Error() string { return r.reason }

type helm struct {
	priority map[string]bool
	// anonymousPilot is true while this loader holds no certificate, which is
	// when nobody can be identified and a caller without a name must be allowed
	// to drive.
	anonymousPilot bool

	held    bool
	owner   caller
	stopped bool
	why     string // why it is stopped, for the refusal and the log
	heard   time.Time
}

func newHelm(priority []string) *helm {
	h := &helm{
		priority: make(map[string]bool),
		stopped:  true,
		why:      "the vehicle starts stopped",
	}
	for _, p := range priority {
		h.priority[p] = true
	}
	return h
}

func (h *helm) same(a, b caller) bool {
	if a.known != b.known {
		return false
	}
	return !a.known || a.name == b.name
}

func (h *helm) mayPilot(c caller) bool {
	return c.known || h.anonymousPilot
}

func (h *helm) outranks(c caller) bool {
	if !c.known {
		return h.anonymousPilot
	}
	return h.priority[c.name]
}

// take gives c control if the rules allow it. changed reports whether the
// pilot is now someone other than before, which is when the setpoints must be
// cleared: a new pilot starts from rest, not from its predecessor's command.
func (h *helm) take(c caller, now time.Time) (changed bool, err error) {
	if !h.mayPilot(c) {
		return false, &refusal{"an unidentified caller cannot take control of a vehicle whose loader holds a certificate"}
	}
	switch {
	case h.stopped:
		if !h.outranks(c) {
			return false, &refusal{fmt.Sprintf("the vehicle is stopped (%s); only %s can take control after a stop", h.why, h.priorityNames())}
		}
		h.stopped, h.why = false, ""
	case h.held && h.same(h.owner, c):
		h.heard = now
		return false, nil
	case h.held && !h.outranks(c):
		return false, &refusal{fmt.Sprintf("%s has control", h.owner)}
	}
	h.held, h.owner, h.heard = true, c, now
	return true, nil
}

// release hands control back. The vehicle is left free, not stopped, so a
// system without priority may take it next: that is the handover.
func (h *helm) release(c caller) (changed bool, err error) {
	if !h.held {
		return false, nil
	}
	if !h.same(h.owner, c) {
		return false, &refusal{fmt.Sprintf("%s has control, not %s", h.owner, c)}
	}
	h.held, h.owner = false, caller{}
	return true, nil
}

// stop is accepted from anyone.
func (h *helm) stop(c caller, reason string) {
	h.held, h.owner = false, caller{}
	h.stopped = true
	h.why = fmt.Sprintf("%s by %s", reason, c)
}

// command checks that c may move the vehicle now, and notes that it spoke.
func (h *helm) command(c caller, now time.Time) error {
	switch {
	case h.stopped:
		return &refusal{fmt.Sprintf("the vehicle is stopped (%s)", h.why)}
	case !h.held:
		return &refusal{"nobody has control; take control before commanding"}
	case !h.same(h.owner, c):
		return &refusal{fmt.Sprintf("%s has control", h.owner)}
	}
	h.heard = now
	return nil
}

// silent stops the vehicle if its pilot has said nothing for too long, and
// reports whether it did.
func (h *helm) silent(now time.Time, limit time.Duration) bool {
	if !h.held || now.Sub(h.heard) <= limit {
		return false
	}
	pilot := h.owner
	h.held, h.owner = false, caller{}
	h.stopped = true
	h.why = fmt.Sprintf("%s went silent for more than %d ms", pilot, limit.Milliseconds())
	return true
}

func (h *helm) holds(c caller) bool {
	return h.held && h.same(h.owner, c)
}

func (h *helm) priorityNames() string {
	if h.anonymousPilot {
		return "anyone, since nobody here can be identified"
	}
	if len(h.priority) == 0 {
		return "nobody (no system is configured with priority)"
	}
	names := make([]string, 0, len(h.priority))
	for name := range h.priority {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, " or ")
}
