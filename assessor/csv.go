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

// The FMEA as a file somebody can open.
//
// CSV rather than a workbook, because the audience for an FMEA opens it in a
// spreadsheet and the audience for a diff opens it in a text editor, and CSV
// serves both. A generated artifact that cannot be diffed is one nobody can
// review: the useful question about this month's assessment is not what it says
// but what changed since last month's, and that question is answerable here
// with `diff`.
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
)

// Columns are fixed and ordered as an FMEA is read: what it is, what goes
// wrong, what follows, how bad, how likely, how visible, and only then the
// number that ranks it.
var columns = []string{
	"ID", "Block", "Item", "Function",
	"Failure mode", "Local effect", "End effect",
	"S", "Severity means",
	"Cause", "O", "Occurrence means",
	"Detection", "D", "Detection means",
	"RPN", "Recommended action", "Graph evidence",
}

// WriteCSV renders the assessment.
//
// Rows are ordered by RPN descending, because the first question anyone asks of
// an FMEA is what to fix first — and an unscored row sorts to the top rather
// than the bottom. A finding whose consequence nobody has valued is not a
// finding of no consequence; it is one the owner has not yet looked at, and
// burying it under the scored rows is how it stays that way.
func WriteCSV(w io.Writer, cloud *Cloud, findings []*Finding, v *ValuationFile, now time.Time) error {
	out := csv.NewWriter(w)

	type scored struct {
		f       *Finding
		s, o, d Rating
		rpn     int
		full    bool
	}
	rows := make([]scored, 0, len(findings))
	for _, f := range findings {
		s, o, d, full := v.Score(f)
		r := scored{f: f, s: s, o: o, d: d, full: full}
		if full {
			r.rpn = s.Rating * o.Rating * d.Rating
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].full != rows[j].full {
			return !rows[i].full // unscored first: they are the ones needing a decision
		}
		return rows[i].rpn > rows[j].rpn
	})

	// A preamble, because ratings without their scale are unreadable and an
	// assessment without its date is unciteable.
	for _, line := range [][]string{
		{"FMEA", cloud.Name, "generated " + now.UTC().Format(time.RFC3339)},
		{"", "Derived from the knowledge graph; severity, occurrence and detection from valuation.json"},
		{"RPN", "S x O x D. An empty rating means the class is not in valuation.json"},
		{},
	} {
		if err := out.Write(line); err != nil {
			return err
		}
	}

	if err := out.Write(columns); err != nil {
		return err
	}
	for _, r := range rows {
		if err := out.Write([]string{
			r.f.ID, r.f.Block, r.f.Item, r.f.Function,
			r.f.FailureMode, r.f.LocalEffect, r.f.EndEffect,
			rating(r.s, r.full), r.s.Means,
			r.f.Cause, rating(r.o, r.full), r.o.Means,
			r.f.Detection, rating(r.d, r.full), r.d.Means,
			rating(Rating{Rating: r.rpn}, r.full), r.f.Action, r.f.Evidence,
		}); err != nil {
			return err
		}
	}

	// What the run needed and the file did not rate, named so the owner can act
	// on it rather than compare two documents to find out.
	if missing := v.Unrated(findings); len(missing) > 0 {
		if err := out.Write([]string{}); err != nil {
			return err
		}
		if err := out.Write([]string{"Unrated classes",
			"add these to valuation.json and the rows above will score"}); err != nil {
			return err
		}
		for _, m := range missing {
			if err := out.Write([]string{"", m}); err != nil {
				return err
			}
		}
	}

	// The scales, quoted so the numbers above mean something to a reader who
	// has never seen the file they came from.
	for name, scale := range map[string]map[string]string{
		"Severity": v.Scales.Severity, "Occurrence": v.Scales.Occurrence, "Detection": v.Scales.Detection,
	} {
		if len(scale) == 0 {
			continue
		}
		if err := out.Write([]string{}); err != nil {
			return err
		}
		if err := out.Write([]string{name + " scale"}); err != nil {
			return err
		}
		keys := make([]string, 0, len(scale))
		for k := range scale {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, _ := strconv.Atoi(keys[i])
			b, _ := strconv.Atoi(keys[j])
			return a > b
		})
		for _, k := range keys {
			if err := out.Write([]string{"", k, scale[k]}); err != nil {
				return err
			}
		}
	}

	out.Flush()
	return out.Error()
}

// rating renders a number, or nothing when the class was never valued. An
// empty cell is honest; a zero would sort and read as a judgment.
func rating(r Rating, scored bool) string {
	if !scored {
		return ""
	}
	return fmt.Sprint(r.Rating)
}
