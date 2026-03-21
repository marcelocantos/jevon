// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

// ExportSwift writes a Swift source file with enums for states and
// message types, and a table-driven state machine for each actor.
func (p *Protocol) ExportSwift(w io.Writer) error {
	var b strings.Builder

	b.WriteString("// Auto-generated from protocol definition. Do not edit.\n")
	b.WriteString("// Source of truth: protocol/*.yaml\n\n")
	b.WriteString("import Foundation\n\n")

	// Message type enum.
	b.WriteString("enum MessageType: String, Sendable {\n")
	for _, m := range p.Messages {
		fmt.Fprintf(&b, "    case %s = \"%s\"\n", swiftCase(string(m.Type)), m.Type)
	}
	b.WriteString("}\n\n")

	// Per-actor state enum + machine.
	for _, a := range p.Actors {
		typeName := swiftTypeName(a.Name)
		states := collectStates(a)

		// State enum.
		fmt.Fprintf(&b, "enum %sState: String, Sendable {\n", typeName)
		for _, s := range states {
			fmt.Fprintf(&b, "    case %s = \"%s\"\n", swiftCase(string(s)), s)
		}
		b.WriteString("}\n\n")

		// Machine class.
		fmt.Fprintf(&b, "final class %sMachine: @unchecked Sendable {\n", typeName)
		fmt.Fprintf(&b, "    private(set) var state: %sState\n\n", typeName)
		fmt.Fprintf(&b, "    init() {\n")
		fmt.Fprintf(&b, "        self.state = .%s\n", swiftCase(string(a.Initial)))
		b.WriteString("    }\n\n")

		// handleMessage
		b.WriteString("    /// Process a received message. Returns the new state, or nil if rejected.\n")
		fmt.Fprintf(&b, "    func handleMessage(_ msg: MessageType, guard check: (String) -> Bool = { _ in true }) -> %sState? {\n", typeName)
		b.WriteString("        switch (state, msg) {\n")
		for _, t := range a.Transitions {
			if t.On.Kind != TriggerRecv {
				continue
			}
			guardClause := ""
			if t.Guard != "" {
				guardClause = fmt.Sprintf(" where check(\"%s\")", t.Guard)
			}
			fmt.Fprintf(&b, "        case (.%s, .%s)%s:\n",
				swiftCase(string(t.From)),
				swiftCase(string(t.On.Msg)),
				guardClause)
			fmt.Fprintf(&b, "            state = .%s\n", swiftCase(string(t.To)))
			b.WriteString("            return state\n")
		}
		b.WriteString("        default:\n")
		b.WriteString("            return nil\n")
		b.WriteString("        }\n")
		b.WriteString("    }\n\n")

		// step
		b.WriteString("    /// Attempt an internal transition. Returns the new state, or nil if none available.\n")
		fmt.Fprintf(&b, "    func step(guard check: (String) -> Bool = { _ in true }) -> %sState? {\n", typeName)
		b.WriteString("        switch state {\n")

		// Group internal transitions by from-state.
		byFrom := map[State][]Transition{}
		for _, t := range a.Transitions {
			if t.On.Kind == TriggerInternal {
				byFrom[t.From] = append(byFrom[t.From], t)
			}
		}
		for _, s := range states {
			ts := byFrom[s]
			if len(ts) == 0 {
				continue
			}
			fmt.Fprintf(&b, "        case .%s:\n", swiftCase(string(s)))
			for _, t := range ts {
				if t.Guard != "" {
					fmt.Fprintf(&b, "            if check(\"%s\") {\n", t.Guard)
					fmt.Fprintf(&b, "                state = .%s\n", swiftCase(string(t.To)))
					b.WriteString("                return state\n")
					b.WriteString("            }\n")
				} else {
					fmt.Fprintf(&b, "            state = .%s\n", swiftCase(string(t.To)))
					b.WriteString("            return state\n")
				}
			}
			if ts[len(ts)-1].Guard != "" {
				b.WriteString("            return nil\n")
			}
		}
		b.WriteString("        default:\n")
		b.WriteString("            return nil\n")
		b.WriteString("        }\n")
		b.WriteString("    }\n")

		b.WriteString("}\n\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func swiftTypeName(name string) string {
	// "jevond" -> "Jevond", "ios" -> "Ios", "cli" -> "Cli"
	if len(name) == 0 {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func swiftCase(s string) string {
	// Convert PascalCase/camelCase state names to lowerCamelCase for Swift enums.
	if len(s) == 0 {
		return s
	}
	// Replace non-alphanumeric with boundaries.
	var result []rune
	prevUpper := false
	for i, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			prevUpper = false
			// Next letter should be uppercase (boundary).
			continue
		}
		if i == 0 || (len(result) > 0 && !unicode.IsLetter(rune(s[i-1])) && !unicode.IsDigit(rune(s[i-1]))) {
			if len(result) == 0 {
				result = append(result, unicode.ToLower(r))
			} else {
				result = append(result, unicode.ToUpper(r))
			}
			prevUpper = unicode.IsUpper(r)
			continue
		}
		if unicode.IsUpper(r) && !prevUpper && len(result) > 0 {
			result = append(result, r)
			prevUpper = true
		} else {
			if len(result) == 0 {
				result = append(result, unicode.ToLower(r))
			} else {
				result = append(result, r)
			}
			prevUpper = unicode.IsUpper(r)
		}
	}
	return string(result)
}
