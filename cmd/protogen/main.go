// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// protogen generates Go, Swift, TLA+, and PlantUML from a YAML
// protocol definition.
//
// Usage:
//
//	protogen protocol/pairing.yaml
//
// Outputs (relative to working directory):
//
//	internal/protocol/<name>_gen.go
//	formal/<Name>.tla
//	docs/<name>.puml
//	ios/Jevon/Protocol/<Name>Machine.swift
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marcelocantos/jevon/internal/protocol"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: protogen <protocol.yaml>")
		os.Exit(1)
	}

	p, err := protocol.LoadYAML(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(1)
	}

	if err := p.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "validate: %v\n", err)
		os.Exit(1)
	}

	lowerName := strings.ToLower(p.Name)

	generators := []struct {
		path string
		gen  func() error
	}{
		{
			path: filepath.Join("internal", "protocol", lowerName+"_gen.go"),
			gen: func() error {
				return writeFile(
					filepath.Join("internal", "protocol", lowerName+"_gen.go"),
					func(f *os.File) error {
						return p.ExportGo(f, "protocol", p.Name)
					},
				)
			},
		},
		{
			path: filepath.Join("formal", p.Name+".tla"),
			gen: func() error {
				return writeFile(
					filepath.Join("formal", p.Name+".tla"),
					func(f *os.File) error { return p.ExportTLA(f) },
				)
			},
		},
		{
			path: filepath.Join("docs", lowerName+".puml"),
			gen: func() error {
				return writeFile(
					filepath.Join("docs", lowerName+".puml"),
					func(f *os.File) error { return p.ExportPlantUML(f) },
				)
			},
		},
		{
			path: filepath.Join("ios", "Jevon", "Protocol", p.Name+"Machine.swift"),
			gen: func() error {
				return writeFile(
					filepath.Join("ios", "Jevon", "Protocol", p.Name+"Machine.swift"),
					func(f *os.File) error { return p.ExportSwift(f) },
				)
			},
		},
	}

	for _, g := range generators {
		if err := g.gen(); err != nil {
			fmt.Fprintf(os.Stderr, "generate %s: %v\n", g.path, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", g.path)
	}
}

func writeFile(path string, fn func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return fn(f)
}
