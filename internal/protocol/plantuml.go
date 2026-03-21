// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"fmt"
	"io"
	"strings"
)

// ExportPlantUML writes a PlantUML state diagram with parallel state
// machines and cross-actor interaction arrows.
func (p *Protocol) ExportPlantUML(w io.Writer) error {
	var b strings.Builder

	b.WriteString("@startuml ")
	b.WriteString(p.Name)
	b.WriteString("\n!theme plain\n\n")
	fmt.Fprintf(&b, "title %s — Parallel State Machines\n\n", p.Name)

	// Assign alias prefixes and colours for cross-actor arrows.
	colors := []string{"#blue", "#green", "#orange", "#purple", "#gray", "#red"}
	actorAlias := make(map[string]string)
	aliases := []string{"D", "A", "C", "E", "F", "G"}
	for i, a := range p.Actors {
		alias := aliases[i%len(aliases)]
		actorAlias[a.Name] = alias

		fmt.Fprintf(&b, "state \"%s\" as %s {\n", a.Name, alias)
		fmt.Fprintf(&b, "  [*] --> %s_%s\n", alias, sanitisePUML(string(a.Initial)))

		for _, t := range a.Transitions {
			from := fmt.Sprintf("%s_%s", alias, sanitisePUML(string(t.From)))
			to := fmt.Sprintf("%s_%s", alias, sanitisePUML(string(t.To)))
			label := transitionLabel(t)
			fmt.Fprintf(&b, "  %s --> %s : %s\n", from, to, label)
		}
		b.WriteString("}\n\n")
	}

	// Cross-actor interaction arrows from sends.
	b.WriteString("' === Cross-actor interactions ===\n\n")
	colorIdx := 0
	for _, a := range p.Actors {
		srcAlias := actorAlias[a.Name]
		for _, t := range a.Transitions {
			for _, s := range t.Sends {
				dstAlias := actorAlias[s.To]
				from := fmt.Sprintf("%s_%s", srcAlias, sanitisePUML(string(t.From)))
				// Find the receiving transition's target state.
				to := findRecvState(p, s.To, s.Msg, dstAlias)
				color := colors[colorIdx%len(colors)]
				label := string(s.Msg)
				if len(s.Fields) > 0 {
					var fields []string
					for k := range s.Fields {
						fields = append(fields, k)
					}
					label += "\\n{" + strings.Join(fields, ", ") + "}"
				}
				fmt.Fprintf(&b, "%s -[%s,dashed]-> %s : %s\n", from, color, to, label)
				colorIdx++
			}
		}
	}

	b.WriteString("\n@enduml\n")

	_, err := io.WriteString(w, b.String())
	return err
}

func transitionLabel(t Transition) string {
	var parts []string
	if t.On.Kind == TriggerRecv {
		parts = append(parts, "recv "+string(t.On.Msg))
	} else if t.On.Desc != "" {
		parts = append(parts, t.On.Desc)
	}
	if t.Guard != "" {
		parts = append(parts, "["+string(t.Guard)+"]")
	}
	if t.Do != "" {
		parts = append(parts, string(t.Do))
	}
	return strings.Join(parts, "\\n")
}

func findRecvState(p *Protocol, actorName string, msg MsgType, alias string) string {
	for _, a := range p.Actors {
		if a.Name != actorName {
			continue
		}
		for _, t := range a.Transitions {
			if t.On.Kind == TriggerRecv && t.On.Msg == msg {
				return fmt.Sprintf("%s_%s", alias, sanitisePUML(string(t.From)))
			}
		}
	}
	// Fallback: use actor initial state.
	return fmt.Sprintf("%s_%s", alias, "unknown")
}

func sanitisePUML(s string) string {
	return strings.ReplaceAll(s, " ", "_")
}
