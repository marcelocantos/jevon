// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"fmt"
	"sync"
)

// GuardFunc evaluates whether a transition's guard condition is met.
// The context parameter carries protocol-specific state.
type GuardFunc func(ctx any) bool

// ActionFunc executes a side-effect when a transition fires.
type ActionFunc func(ctx any) error

// Machine is a runtime state machine executor for one actor in a protocol.
// It enforces the transition table — any message or event not matching a
// valid transition from the current state is rejected.
type Machine struct {
	mu      sync.Mutex
	actor   Actor
	state   State
	guards  map[GuardID]GuardFunc
	actions map[ActionID]ActionFunc

	// index: (state, msgType) → applicable transitions
	recvIndex     map[stateMsg][]Transition
	internalIndex map[State][]Transition
}

type stateMsg struct {
	state State
	msg   MsgType
}

// NewMachine creates a runtime state machine for the named actor.
// Guards and actions are registered separately via the returned Machine.
func NewMachine(p *Protocol, actorName string) (*Machine, error) {
	var actor *Actor
	for i := range p.Actors {
		if p.Actors[i].Name == actorName {
			actor = &p.Actors[i]
			break
		}
	}
	if actor == nil {
		return nil, fmt.Errorf("actor %q not found in protocol %q", actorName, p.Name)
	}

	m := &Machine{
		actor:         *actor,
		state:         actor.Initial,
		guards:        make(map[GuardID]GuardFunc),
		actions:       make(map[ActionID]ActionFunc),
		recvIndex:     make(map[stateMsg][]Transition),
		internalIndex: make(map[State][]Transition),
	}

	for _, t := range actor.Transitions {
		switch t.On.Kind {
		case TriggerRecv:
			key := stateMsg{t.From, t.On.Msg}
			m.recvIndex[key] = append(m.recvIndex[key], t)
		case TriggerInternal:
			m.internalIndex[t.From] = append(m.internalIndex[t.From], t)
		}
	}

	return m, nil
}

// RegisterGuard binds a guard function to a GuardID.
func (m *Machine) RegisterGuard(id GuardID, fn GuardFunc) {
	m.guards[id] = fn
}

// RegisterAction binds an action function to an ActionID.
func (m *Machine) RegisterAction(id ActionID, fn ActionFunc) {
	m.actions[id] = fn
}

// State returns the current state.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// HandleMessage processes a received message. Returns the new state
// or an error if no valid transition exists.
func (m *Machine) HandleMessage(msg MsgType, ctx any) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := stateMsg{m.state, msg}
	transitions := m.recvIndex[key]
	if len(transitions) == 0 {
		return m.state, fmt.Errorf("no transition from %s on message %s", m.state, msg)
	}

	return m.tryTransitions(transitions, ctx)
}

// Step attempts an internal transition from the current state.
// Returns the new state or an error if no valid transition exists.
func (m *Machine) Step(ctx any) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	transitions := m.internalIndex[m.state]
	if len(transitions) == 0 {
		return m.state, fmt.Errorf("no internal transition from %s", m.state)
	}

	return m.tryTransitions(transitions, ctx)
}

func (m *Machine) tryTransitions(transitions []Transition, ctx any) (State, error) {
	for _, t := range transitions {
		if t.Guard != "" {
			guard, ok := m.guards[t.Guard]
			if !ok {
				return m.state, fmt.Errorf("unregistered guard: %s", t.Guard)
			}
			if !guard(ctx) {
				continue
			}
		}

		if t.Do != "" {
			action, ok := m.actions[t.Do]
			if !ok {
				return m.state, fmt.Errorf("unregistered action: %s", t.Do)
			}
			if err := action(ctx); err != nil {
				return m.state, fmt.Errorf("action %s failed: %w", t.Do, err)
			}
		}

		m.state = t.To
		return m.state, nil
	}

	return m.state, fmt.Errorf("all guards failed for transitions from %s", m.state)
}
