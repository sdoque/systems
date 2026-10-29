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
 ***************************************************************************SDG*/

// Command mafgen writes mAF.sysml from AFO, the mapping table and the frame.
// Run from the modeler directory, as `go generate` does.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/sdoque/systems/modeler/mafgen"
)

func main() {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		return string(b)
	}
	out, err := mafgen.Generate(
		read(filepath.Join("..", "kgrapher", "ontologies", "afo.ttl")),
		read("afo-maf-mapping.tsv"),
		read("maf-frame.sysml"))
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("mAF.sysml", []byte(out), 0o644); err != nil {
		log.Fatal(err)
	}
}
