// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// ExportTLA writes a TLA+ spec for the protocol to w. The spec uses
// PlusCal for readability, with one process per actor and message
// channels between them. A Dolev-Yao adversary process models an
// active network attacker with configurable capabilities.
func (p *Protocol) ExportTLA(w io.Writer) error {
	var b strings.Builder

	b.WriteString("---- MODULE ")
	b.WriteString(sanitiseTLA(p.Name))
	b.WriteString(" ----\n")
	b.WriteString("\\* Auto-generated from protocol definition. Do not edit.\n")
	b.WriteString("\\* Source of truth: internal/protocol/ Go definition.\n\n")
	b.WriteString("EXTENDS Integers, Sequences, FiniteSets, TLC\n\n")

	writeStateConstants(&b, p)
	writeMsgConstants(&b, p)
	// Operators go before PlusCal — they are pure functions.
	writeOperators(&b, p)

	// PlusCal algorithm.
	b.WriteString("(*--algorithm ")
	b.WriteString(sanitiseTLA(p.Name))
	b.WriteString("\n\n")

	writeVariables(&b, p)
	writeProcesses(&b, p)
	writeAdversary(&b, p)

	b.WriteString("end algorithm; *)\n")
	b.WriteString("\\* BEGIN TRANSLATION\n")
	b.WriteString("\\* END TRANSLATION\n\n")

	writeProperties(&b, p)

	b.WriteString("====\n")

	_, err := io.WriteString(w, b.String())
	return err
}

func writeStateConstants(b *strings.Builder, p *Protocol) {
	for _, a := range p.Actors {
		states := collectStates(a)
		b.WriteString("\\* States for ")
		b.WriteString(a.Name)
		b.WriteString("\n")
		for _, s := range states {
			fmt.Fprintf(b, "%s_%s == \"%s_%s\"\n",
				sanitiseTLA(a.Name), sanitiseTLA(string(s)),
				a.Name, s)
		}
		b.WriteString("\n")
	}
}

func writeMsgConstants(b *strings.Builder, p *Protocol) {
	if len(p.Messages) == 0 {
		return
	}
	b.WriteString("\\* Message types\n")
	for _, m := range p.Messages {
		fmt.Fprintf(b, "MSG_%s == \"%s\" \\* %s -> %s",
			sanitiseTLA(string(m.Type)), m.Type, m.From, m.To)
		if m.Desc != "" {
			fmt.Fprintf(b, " (%s)", m.Desc)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func writeOperators(b *strings.Builder, p *Protocol) {
	if len(p.Operators) == 0 {
		return
	}
	b.WriteString("\\* Helper operators\n")
	for _, op := range p.Operators {
		if op.Desc != "" {
			fmt.Fprintf(b, "\\* %s\n", op.Desc)
		}
		if op.Params != "" {
			fmt.Fprintf(b, "%s(%s) == %s\n", sanitiseTLA(op.Name), op.Params, op.Expr)
		} else {
			fmt.Fprintf(b, "%s == %s\n", sanitiseTLA(op.Name), op.Expr)
		}
	}
	b.WriteString("\n")
}

// Guard expressions are inlined into await clauses (see writeTransitionAwait)
// to avoid TLA+ operator ordering issues with TRANSLATION-block variables.

func writeVariables(b *strings.Builder, p *Protocol) {
	b.WriteString("variables\n")

	// Per-actor state variable.
	for _, a := range p.Actors {
		fmt.Fprintf(b, "    %s_state = %s_%s,\n",
			sanitiseTLA(a.Name),
			sanitiseTLA(a.Name), sanitiseTLA(string(a.Initial)))
	}

	// Channel per message route (from->to).
	channels := channelPairs(p)
	for _, ch := range channels {
		fmt.Fprintf(b, "    chan_%s_%s = <<>>,\n", ch.from, ch.to)
	}

	// Adversary knowledge.
	b.WriteString("    adversary_knowledge = {},\n")

	// Auxiliary variables from protocol definition.
	for _, v := range p.Vars {
		if v.Desc != "" {
			fmt.Fprintf(b, "    \\* %s\n", v.Desc)
		}
		fmt.Fprintf(b, "    %s = %s,\n", sanitiseTLA(v.Name), v.Initial)
	}

	// Remove trailing comma, add semicolon.
	s := b.String()
	if idx := strings.LastIndex(s, ",\n"); idx >= 0 {
		b.Reset()
		b.WriteString(s[:idx])
		b.WriteString(";\n\n")
	}
}

func writeProcesses(b *strings.Builder, p *Protocol) {
	for i, a := range p.Actors {
		fmt.Fprintf(b, "fair process %s = %d\n", sanitiseTLA(a.Name), i+1)
		b.WriteString("begin\n")
		fmt.Fprintf(b, "  %s_loop:\n", sanitiseTLA(a.Name))
		if !p.OneShot {
			b.WriteString("  while TRUE do\n")
		}

		b.WriteString("    either\n")

		first := true
		for _, t := range a.Transitions {
			if !first {
				b.WriteString("    or\n")
			}
			first = false

			writeTransitionComment(b, &t)
			writeTransitionAwait(b, p, &a, &t)
			writeTransitionBody(b, p, &a, &t)
		}

		b.WriteString("    end either;\n")
		if !p.OneShot {
			b.WriteString("  end while;\n")
		}
		b.WriteString("end process;\n\n")
	}
}

func writeTransitionComment(b *strings.Builder, t *Transition) {
	fmt.Fprintf(b, "      \\* %s -> %s", t.From, t.To)
	if t.On.Kind == TriggerRecv {
		fmt.Fprintf(b, " on %s", t.On.Msg)
	} else if t.On.Desc != "" {
		fmt.Fprintf(b, " (%s)", t.On.Desc)
	}
	b.WriteString("\n")
}

func writeTransitionAwait(b *strings.Builder, p *Protocol, a *Actor, t *Transition) {
	fmt.Fprintf(b, "      await %s_state = %s_%s",
		sanitiseTLA(a.Name),
		sanitiseTLA(a.Name), sanitiseTLA(string(t.From)))

	if t.On.Kind == TriggerRecv {
		fromActor := msgSender(p, t.On.Msg)
		chanName := channelName(fromActor, a.Name)
		fmt.Fprintf(b, " /\\ Len(%s) > 0 /\\ Head(%s).type = MSG_%s",
			chanName, chanName, sanitiseTLA(string(t.On.Msg)))
	}

	if t.Guard != "" {
		// Inline the guard expression. For recv transitions, substitute
		// "recv_msg" with "Head(channel)" since at await-time the
		// message hasn't been consumed into recv_msg yet.
		expr := guardExpr(p, t.Guard)
		if t.On.Kind == TriggerRecv {
			fromActor := msgSender(p, t.On.Msg)
			chanName := channelName(fromActor, a.Name)
			expr = strings.ReplaceAll(expr, "recv_msg", "Head("+chanName+")")
		}
		fmt.Fprintf(b, " /\\ (%s)", expr)
	}
	b.WriteString(";\n")
}

func guardExpr(p *Protocol, id GuardID) string {
	for _, g := range p.Guards {
		if g.ID == id {
			return g.Expr
		}
	}
	return string(id) // fallback
}

func writeTransitionBody(b *strings.Builder, p *Protocol, a *Actor, t *Transition) {
	// Consume message: save to recv_msg, then Tail.
	if t.On.Kind == TriggerRecv {
		fromActor := msgSender(p, t.On.Msg)
		chanName := channelName(fromActor, a.Name)
		fmt.Fprintf(b, "      recv_msg := Head(%s);\n", chanName)
		fmt.Fprintf(b, "      %s := Tail(%s);\n", chanName, chanName)
	}

	// Send messages.
	for _, s := range t.Sends {
		chanName := channelName(a.Name, s.To)
		fmt.Fprintf(b, "      %s := Append(%s, ", chanName, chanName)
		writeRecord(b, s.Msg, s.Fields)
		b.WriteString(");\n")
	}

	// Auxiliary variable updates.
	for _, u := range t.Updates {
		fmt.Fprintf(b, "      %s := %s;\n", sanitiseTLA(u.Var), u.Expr)
	}

	// State update.
	fmt.Fprintf(b, "      %s_state := %s_%s;\n",
		sanitiseTLA(a.Name),
		sanitiseTLA(a.Name), sanitiseTLA(string(t.To)))
}

func writeRecord(b *strings.Builder, msg MsgType, fields map[string]string) {
	b.WriteString("[type |-> MSG_")
	b.WriteString(sanitiseTLA(string(msg)))

	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(b, ", %s |-> %s", k, fields[k])
		}
	}

	b.WriteString("]")
}

func writeAdversary(b *strings.Builder, p *Protocol) {
	channels := channelPairs(p)
	if len(channels) == 0 && len(p.AdvActions) == 0 {
		return
	}

	b.WriteString("\\* Dolev-Yao adversary: controls the network.\n")
	b.WriteString("\\* Can read, drop, replay, and reorder messages on all channels.\n")
	b.WriteString("\\* Cannot forge messages or break cryptographic primitives.\n")
	b.WriteString("\\* Extended capabilities model specific attack scenarios.\n")
	fmt.Fprintf(b, "fair process Adversary = %d\n", len(p.Actors)+1)
	b.WriteString("begin\n")
	b.WriteString("  adv_loop:\n")
	b.WriteString("  while TRUE do\n")
	b.WriteString("    either\n")
	b.WriteString("      skip \\* no-op: honest relay\n")

	// Standard Dolev-Yao: eavesdrop, drop, replay per channel.
	for _, ch := range channels {
		chanName := fmt.Sprintf("chan_%s_%s", ch.from, ch.to)

		b.WriteString("    or\n")
		fmt.Fprintf(b, "      \\* Eavesdrop on %s -> %s\n", ch.from, ch.to)
		fmt.Fprintf(b, "      await Len(%s) > 0;\n", chanName)
		fmt.Fprintf(b, "      adversary_knowledge := adversary_knowledge \\union {Head(%s)};\n",
			chanName)

		b.WriteString("    or\n")
		fmt.Fprintf(b, "      \\* Drop from %s -> %s\n", ch.from, ch.to)
		fmt.Fprintf(b, "      await Len(%s) > 0;\n", chanName)
		fmt.Fprintf(b, "      %s := Tail(%s);\n", chanName, chanName)

		b.WriteString("    or\n")
		fmt.Fprintf(b, "      \\* Replay into %s -> %s\n", ch.from, ch.to)
		if p.ChannelBound > 0 {
			fmt.Fprintf(b, "      await adversary_knowledge /= {} /\\ Len(%s) < %d;\n", chanName, p.ChannelBound)
		} else {
			b.WriteString("      await adversary_knowledge /= {};\n")
		}
		fmt.Fprintf(b, "      with msg \\in adversary_knowledge do\n")
		fmt.Fprintf(b, "        %s := Append(%s, msg);\n", chanName, chanName)
		b.WriteString("      end with;\n")
	}

	// Protocol-specific adversary actions.
	for _, aa := range p.AdvActions {
		b.WriteString("    or\n")
		fmt.Fprintf(b, "      \\* %s: %s\n", aa.Name, aa.Desc)
		b.WriteString(aa.Code)
		b.WriteString("\n")
	}

	b.WriteString("    end either;\n")
	b.WriteString("  end while;\n")
	b.WriteString("end process;\n\n")
}

func writeProperties(b *strings.Builder, p *Protocol) {
	if len(p.Properties) == 0 {
		return
	}
	b.WriteString("\\* Verification properties\n")
	for _, prop := range p.Properties {
		if prop.Desc != "" {
			fmt.Fprintf(b, "\\* %s\n", prop.Desc)
		}
		switch prop.Kind {
		case Invariant:
			fmt.Fprintf(b, "%s == %s\n", sanitiseTLA(prop.Name), prop.Expr)
		case Liveness:
			fmt.Fprintf(b, "%s == <>(%s)\n", sanitiseTLA(prop.Name), prop.Expr)
		}
	}
	b.WriteString("\n")
}

// Helpers.

type channelPair struct{ from, to string }

func channelPairs(p *Protocol) []channelPair {
	seen := map[channelPair]bool{}
	var pairs []channelPair
	for _, m := range p.Messages {
		cp := channelPair{sanitiseTLA(m.From), sanitiseTLA(m.To)}
		if !seen[cp] {
			seen[cp] = true
			pairs = append(pairs, cp)
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].from != pairs[j].from {
			return pairs[i].from < pairs[j].from
		}
		return pairs[i].to < pairs[j].to
	})
	return pairs
}

func channelName(from, to string) string {
	return fmt.Sprintf("chan_%s_%s", sanitiseTLA(from), sanitiseTLA(to))
}

func msgSender(p *Protocol, mt MsgType) string {
	for _, m := range p.Messages {
		if m.Type == mt {
			return m.From
		}
	}
	return "unknown"
}

func collectStates(a Actor) []State {
	seen := map[State]bool{}
	var states []State
	add := func(s State) {
		if !seen[s] {
			seen[s] = true
			states = append(states, s)
		}
	}
	add(a.Initial)
	for _, t := range a.Transitions {
		add(t.From)
		add(t.To)
	}
	return states
}

func sanitiseTLA(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
