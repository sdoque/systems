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

// The half of an FMEA that is not in the graph.
//
// Which services exist, what consumes them, and what stops working when one
// stops answering — all of that the cloud knows about itself, and this system
// derives it. What the cloud cannot know is how much any of it matters. A
// heater failing in a Norrbotten cottage in January and the same heater failing
// in a summer house are the same failure and not remotely the same consequence,
// and no triple distinguishes them.
//
// So severity, occurrence and detection come from a file the owner writes. This
// is the same division the authorizer makes: policies.json holds the judgment,
// the engine derives the rest, and neither pretends to be the other.
//
// The ratings attach to *classes* rather than to rows. Attaching them to rows
// would mean re-judging all of them whenever a system was added, which is
// exactly the maintenance burden that leaves FMEA spreadsheets to rot. A class
// is a kind of consequence — "a heated zone runs open-loop" — and it keeps its
// rating while the cloud changes shape underneath it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ValuationFile is the content of valuation.json.
type ValuationFile struct {
	// Scales are the 1-10 definitions, quoted into the CSV so a reader knows
	// what an 8 meant to whoever wrote it. FMEA ratings are meaningless without
	// them: severity 8 against an automotive scale and against a cottage scale
	// are different claims.
	Scales struct {
		Severity   map[string]string `json:"severity"`
		Occurrence map[string]string `json:"occurrence"`
		Detection  map[string]string `json:"detection"`
	} `json:"scales"`

	Severity   map[string]Rating `json:"severity"`
	Occurrence map[string]Rating `json:"occurrence"`
	Detection  map[string]Rating `json:"detection"`
}

// Rating is one judgment: a number, and why it is that number.
//
// The prose is not decoration. An FMEA is read by people deciding where to
// spend, and "8" persuades nobody; "a heated zone runs open-loop in a climate
// where that freezes pipes" is the argument.
type Rating struct {
	Rating int    `json:"rating"`
	Means  string `json:"means"`
}

// LoadValuation reads and checks the file.
func LoadValuation(path string) (*ValuationFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v ValuationFile
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := v.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &v, nil
}

// Validate refuses a file that would produce meaningless arithmetic.
//
// A rating outside 1-10 is not a stricter opinion, it is a mistake — RPN is a
// product, so a severity of 50 does not make a row five times more urgent than
// a 10, it makes the whole ranking incomparable with every other row.
func (v *ValuationFile) Validate() error {
	for name, set := range map[string]map[string]Rating{
		"severity": v.Severity, "occurrence": v.Occurrence, "detection": v.Detection,
	} {
		if len(set) == 0 {
			return fmt.Errorf("no %s classes are rated, so nothing can be scored", name)
		}
		for class, r := range set {
			if r.Rating < 1 || r.Rating > 10 {
				return fmt.Errorf("%s class %q is rated %d; FMEA ratings run 1 to 10",
					name, class, r.Rating)
			}
			if r.Means == "" {
				return fmt.Errorf("%s class %q has a rating and no reason; the reason is "+
					"what a reader weighs, the number is only how it sorts", name, class)
			}
		}
	}
	return nil
}

// Score returns the three ratings for one finding, and whether every class it
// named was rated.
//
// An unrated class is not scored as zero and not silently dropped. It appears
// in the CSV with an empty rating and a note, because a finding nobody has
// valued is precisely the one worth putting in front of the owner: the analysis
// has found something whose consequence has never been considered.
func (v *ValuationFile) Score(f *Finding) (s, o, d Rating, complete bool) {
	s, sOK := v.Severity[f.EffectClass]
	o, oOK := v.Occurrence[f.CauseClass]
	d, dOK := v.Detection[f.DetectionClass]
	return s, o, d, sOK && oOK && dOK
}

// Unrated lists the classes a run needed and the file did not rate, so the
// owner can be told what to add rather than left to compare two documents.
func (v *ValuationFile) Unrated(findings []*Finding) []string {
	missing := map[string]bool{}
	for _, f := range findings {
		if _, ok := v.Severity[f.EffectClass]; !ok {
			missing["severity/"+f.EffectClass] = true
		}
		if _, ok := v.Occurrence[f.CauseClass]; !ok {
			missing["occurrence/"+f.CauseClass] = true
		}
		if _, ok := v.Detection[f.DetectionClass]; !ok {
			missing["detection/"+f.DetectionClass] = true
		}
	}
	out := make([]string, 0, len(missing))
	for m := range missing {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
