// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package protocol provides a declarative state machine framework for
// security protocols. State machines are defined as data (transition
// tables) that serve as the single source of truth for both runtime
// execution and TLA+ model generation.
package protocol

import "fmt"

// State is a named state in an actor's state machine.
type State string

// MsgType identifies a message kind exchanged between actors.
type MsgType string

// GuardID identifies a named guard predicate.
type GuardID string

// ActionID identifies a named side-effect action.
type ActionID string

// PropertyKind classifies a verification property.
type PropertyKind int

const (
	Invariant PropertyKind = iota
	Liveness
)

// Transition defines a single edge in an actor's state machine.
type Transition struct {
	From    State
	To      State
	On      Trigger      // what causes this transition
	Guard   GuardID      // optional: must be true to take this edge
	Do      ActionID     // optional: side-effect on transition
	Sends   []Send       // messages emitted when this transition fires
	Updates []VarUpdate  // auxiliary variable updates
}

// Send describes a message emitted during a transition.
type Send struct {
	To     string            // receiver actor name
	Msg    MsgType           // message type
	Fields map[string]string // TLA+ expressions for message record fields
}

// VarUpdate sets an auxiliary variable to a TLA+ expression.
type VarUpdate struct {
	Var  string // variable name
	Expr string // TLA+ expression
}

// TriggerKind distinguishes message receipt from internal events.
type TriggerKind int

const (
	TriggerRecv    TriggerKind = iota // message received from another actor
	TriggerInternal                   // internal event (timeout, completion)
)

// Trigger describes what causes a transition.
type Trigger struct {
	Kind TriggerKind
	Msg  MsgType // for TriggerRecv: the message type
	Desc string  // human-readable label (used in TLA+ comments)
}

// Recv creates a trigger for receiving a message.
func Recv(msg MsgType) Trigger {
	return Trigger{Kind: TriggerRecv, Msg: msg}
}

// Internal creates a trigger for an internal event.
func Internal(desc string) Trigger {
	return Trigger{Kind: TriggerInternal, Desc: desc}
}

// Message defines a message type in the protocol, with its sender
// and receiver. This determines TLA+ channel topology.
type Message struct {
	Type MsgType
	From string // actor name
	To   string // actor name
	Desc string // human-readable description
}

// VarDef defines an auxiliary state variable for TLA+ model checking.
// These track protocol-level state that isn't part of any single actor.
type VarDef struct {
	Name    string // TLA+ variable name
	Initial string // TLA+ expression for initial value
	Desc    string // human-readable description
}

// GuardDef maps a GuardID to a TLA+ expression, binding the
// declarative guard used in transitions to a checkable predicate.
type GuardDef struct {
	ID   GuardID
	Expr string // TLA+ boolean expression
}

// Operator defines a TLA+ helper operator used in guard expressions,
// variable updates, or properties. These capture domain-specific
// logic (e.g., symbolic crypto operations).
type Operator struct {
	Name   string // operator name
	Params string // comma-separated parameter list (e.g., "a, b")
	Expr   string // TLA+ expression body
	Desc   string // human-readable description
}

// AdvAction defines an adversary capability as a PlusCal code block.
// Each action becomes an either/or branch in the adversary process,
// in addition to the standard Dolev-Yao eavesdrop/drop/replay.
type AdvAction struct {
	Name string // identifier (used in comments)
	Desc string // what attack this models
	Code string // PlusCal code (indented inside either/or branch)
}

// Property defines a verification property for TLA+ model checking.
type Property struct {
	Name string
	Kind PropertyKind
	Expr string // TLA+ expression
	Desc string // human-readable description
}

// Actor defines one participant in the protocol.
type Actor struct {
	Name        string
	Initial     State
	Transitions []Transition
}

// Protocol is the complete definition of a multi-actor protocol.
// This is the single source of truth for runtime and TLA+ generation.
type Protocol struct {
	Name       string
	Actors     []Actor
	Messages   []Message
	Vars       []VarDef    // auxiliary state variables
	Guards     []GuardDef  // guard TLA+ expressions
	Operators  []Operator  // TLA+ helper operators
	AdvActions []AdvAction // adversary capabilities beyond Dolev-Yao
	Properties []Property
}

// Validate checks the protocol definition for internal consistency:
// all states reachable, all message types declared, sends reference
// valid actors and message types, guards are defined.
func (p *Protocol) Validate() error {
	actorNames := map[string]bool{}
	for _, a := range p.Actors {
		if actorNames[a.Name] {
			return fmt.Errorf("duplicate actor name: %s", a.Name)
		}
		actorNames[a.Name] = true
	}

	msgTypes := map[MsgType]bool{}
	msgSenders := map[MsgType]string{}
	for _, m := range p.Messages {
		msgTypes[m.Type] = true
		msgSenders[m.Type] = m.From
		if !actorNames[m.From] {
			return fmt.Errorf("message %s: unknown sender %q", m.Type, m.From)
		}
		if !actorNames[m.To] {
			return fmt.Errorf("message %s: unknown receiver %q", m.Type, m.To)
		}
	}

	guardDefs := map[GuardID]bool{}
	for _, g := range p.Guards {
		guardDefs[g.ID] = true
	}

	for _, a := range p.Actors {
		for _, t := range a.Transitions {
			if t.On.Kind == TriggerRecv && !msgTypes[t.On.Msg] {
				return fmt.Errorf("actor %s: transition %s->%s triggers on undeclared message %s",
					a.Name, t.From, t.To, t.On.Msg)
			}
			if t.Guard != "" && !guardDefs[t.Guard] {
				return fmt.Errorf("actor %s: transition %s->%s uses undefined guard %s",
					a.Name, t.From, t.To, t.Guard)
			}
			for _, s := range t.Sends {
				if !actorNames[s.To] {
					return fmt.Errorf("actor %s: transition %s->%s sends to unknown actor %q",
						a.Name, t.From, t.To, s.To)
				}
				if !msgTypes[s.Msg] {
					return fmt.Errorf("actor %s: transition %s->%s sends undeclared message %s",
						a.Name, t.From, t.To, s.Msg)
				}
				if sender := msgSenders[s.Msg]; sender != a.Name {
					return fmt.Errorf("actor %s: transition %s->%s sends message %s but declared sender is %s",
						a.Name, t.From, t.To, s.Msg, sender)
				}
			}
		}
	}

	return nil
}
