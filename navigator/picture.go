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
	"bufio"
	"fmt"
	"math"
	"os"

	"github.com/sdoque/mbaigo/forms"
)

// writePicture draws the map with the route on it, as a colour image a person
// can open in any viewer: black is occupied, white free, grey never seen; the
// route is red, where the vehicle is green and where the route ends blue.
//
// PPM because it needs no library: a header and the bytes. Row zero of an
// image is the top, and the map's y grows upwards, so the rows are written from
// the top of the map down.
//
// Cropped to what has been seen, the vehicle and the route, with a margin: the
// cartographer's grid is sixty meters square, and a corridor in the middle of
// it is a thin line in a grey field.
func writePicture(path string, m *forms.MapA_v1a, p *Plan, at here) error {
	type rgb [3]byte
	img := make([]rgb, m.Width*m.Height)
	for i, c := range m.Cells {
		switch c {
		case forms.MapUnknown:
			img[i] = rgb{160, 160, 160}
		case forms.MapFree:
			img[i] = rgb{255, 255, 255}
		default:
			img[i] = rgb{0, 0, 0}
		}
	}
	dot := func(x, y float64, r int, colour rgb) {
		ci := int(math.Round((x - m.OriginX) / m.Resolution))
		cj := int(math.Round((y - m.OriginY) / m.Resolution))
		for j := cj - r; j <= cj+r; j++ {
			for i := ci - r; i <= ci+r; i++ {
				if i >= 0 && j >= 0 && i < m.Width && j < m.Height {
					img[j*m.Width+i] = colour
				}
			}
		}
	}
	for i := range p.X {
		dot(p.X[i], p.Y[i], 0, rgb{220, 0, 0})
	}
	dot(at.x, at.y, 3, rgb{0, 170, 0})
	dot(p.GoalX, p.GoalY, 3, rgb{0, 60, 220})

	i0, j0, i1, j1 := m.Width, m.Height, -1, -1
	grow := func(i, j int) {
		i0, j0 = min(i0, i), min(j0, j)
		i1, j1 = max(i1, i), max(j1, j)
	}
	for j := 0; j < m.Height; j++ {
		for i := 0; i < m.Width; i++ {
			if m.Cells[j*m.Width+i] != forms.MapUnknown {
				grow(i, j)
			}
		}
	}
	cellOf := func(x, y float64) (int, int) {
		return int(math.Round((x - m.OriginX) / m.Resolution)), int(math.Round((y - m.OriginY) / m.Resolution))
	}
	for k := range p.X {
		grow(cellOf(p.X[k], p.Y[k]))
	}
	grow(cellOf(at.x, at.y))
	margin := int(math.Ceil(1 / m.Resolution)) // a meter
	i0, j0 = max(0, i0-margin), max(0, j0-margin)
	i1, j1 = min(m.Width-1, i1+margin), min(m.Height-1, j1+margin)
	if i1 < i0 || j1 < j0 {
		i0, j0, i1, j1 = 0, 0, m.Width-1, m.Height-1
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "P6\n%d %d\n255\n", i1-i0+1, j1-j0+1)
	for j := j1; j >= j0; j-- {
		for i := i0; i <= i1; i++ {
			px := img[j*m.Width+i]
			w.Write(px[:])
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
