// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAML parse types — mirrors the YAML schema.

type yamlProtocol struct {
	Name       string                     `yaml:"name"`
	Messages   yaml.Node                  `yaml:"messages"`
	Actors     yaml.Node                  `yaml:"actors"`
	Vars       yaml.Node                  `yaml:"vars"`
	Guards     yaml.Node                  `yaml:"guards"`
	Operators  yaml.Node                  `yaml:"operators"`
	Adversary  []yamlAdvAction            `yaml:"adversary"`
	Properties []yamlProperty             `yaml:"properties"`
}

type yamlMessage struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	Desc string `yaml:"desc"`
}

type yamlActor struct {
	Initial     string           `yaml:"initial"`
	Transitions []yamlTransition `yaml:"transitions"`
}

type yamlTransition struct {
	From    string              `yaml:"from"`
	To      string              `yaml:"to"`
	On      string              `yaml:"on"`
	Guard   string              `yaml:"guard"`
	Do      string              `yaml:"do"`
	Sends   []yamlSend          `yaml:"sends"`
	Updates yaml.Node           `yaml:"updates"`
}

type yamlSend struct {
	To     string            `yaml:"to"`
	Msg    string            `yaml:"msg"`
	Fields map[string]string `yaml:"fields"`
}

type yamlVar struct {
	Initial string `yaml:"initial"`
	Desc    string `yaml:"desc"`
}

type yamlOperator struct {
	Params string `yaml:"params"`
	Expr   string `yaml:"expr"`
	Desc   string `yaml:"desc"`
}

type yamlAdvAction struct {
	Name string `yaml:"name"`
	Desc string `yaml:"desc"`
	Code string `yaml:"code"`
}

type yamlProperty struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
	Expr string `yaml:"expr"`
	Desc string `yaml:"desc"`
}

// LoadYAML reads a protocol definition from a YAML file and returns
// a Protocol struct suitable for use with the runtime executor and
// all code generators.
func LoadYAML(path string) (*Protocol, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseYAML(data)
}

// ParseYAML parses a YAML protocol definition.
func ParseYAML(data []byte) (*Protocol, error) {
	var yp yamlProtocol
	if err := yaml.Unmarshal(data, &yp); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	p := &Protocol{Name: yp.Name}

	// Messages — preserve YAML key order.
	msgs, err := parseOrderedMap[yamlMessage](&yp.Messages)
	if err != nil {
		return nil, fmt.Errorf("messages: %w", err)
	}
	for _, kv := range msgs {
		p.Messages = append(p.Messages, Message{
			Type: MsgType(kv.key),
			From: kv.val.From,
			To:   kv.val.To,
			Desc: kv.val.Desc,
		})
	}

	// Actors — preserve YAML key order.
	actors, err := parseOrderedMap[yamlActor](&yp.Actors)
	if err != nil {
		return nil, fmt.Errorf("actors: %w", err)
	}
	for _, kv := range actors {
		actor := Actor{
			Name:    kv.key,
			Initial: State(kv.val.Initial),
		}
		for i, yt := range kv.val.Transitions {
			t, err := convertTransition(yt)
			if err != nil {
				return nil, fmt.Errorf("actor %s transition %d: %w", kv.key, i, err)
			}
			actor.Transitions = append(actor.Transitions, t)
		}
		p.Actors = append(p.Actors, actor)
	}

	// Vars — preserve YAML key order.
	vars, err := parseOrderedMap[yamlVar](&yp.Vars)
	if err != nil {
		return nil, fmt.Errorf("vars: %w", err)
	}
	for _, kv := range vars {
		p.Vars = append(p.Vars, VarDef{
			Name:    kv.key,
			Initial: kv.val.Initial,
			Desc:    kv.val.Desc,
		})
	}

	// Guards — preserve YAML key order.
	guards, err := parseOrderedMapString(&yp.Guards)
	if err != nil {
		return nil, fmt.Errorf("guards: %w", err)
	}
	for _, kv := range guards {
		p.Guards = append(p.Guards, GuardDef{
			ID:   GuardID(kv.key),
			Expr: kv.val,
		})
	}

	// Operators — preserve YAML key order.
	ops, err := parseOrderedMap[yamlOperator](&yp.Operators)
	if err != nil {
		return nil, fmt.Errorf("operators: %w", err)
	}
	for _, kv := range ops {
		p.Operators = append(p.Operators, Operator{
			Name:   kv.key,
			Params: kv.val.Params,
			Expr:   kv.val.Expr,
			Desc:   kv.val.Desc,
		})
	}

	// Adversary actions.
	for _, ya := range yp.Adversary {
		code := strings.TrimRight(ya.Code, "\n")
		// Indent code lines for PlusCal either/or block.
		var indented []string
		for _, line := range strings.Split(code, "\n") {
			indented = append(indented, "      "+line)
		}
		p.AdvActions = append(p.AdvActions, AdvAction{
			Name: ya.Name,
			Desc: ya.Desc,
			Code: strings.Join(indented, "\n"),
		})
	}

	// Properties.
	for _, ypr := range yp.Properties {
		kind := Invariant
		if ypr.Kind == "liveness" {
			kind = Liveness
		}
		p.Properties = append(p.Properties, Property{
			Name: ypr.Name,
			Kind: kind,
			Expr: ypr.Expr,
			Desc: ypr.Desc,
		})
	}

	return p, nil
}

func convertTransition(yt yamlTransition) (Transition, error) {
	t := Transition{
		From:  State(yt.From),
		To:    State(yt.To),
		Guard: GuardID(yt.Guard),
		Do:    ActionID(yt.Do),
	}

	// Parse trigger: "recv <msg>" or free-form internal description.
	if strings.HasPrefix(yt.On, "recv ") {
		t.On = Recv(MsgType(strings.TrimPrefix(yt.On, "recv ")))
	} else {
		t.On = Internal(yt.On)
	}

	// Sends.
	for _, ys := range yt.Sends {
		t.Sends = append(t.Sends, Send{
			To:     ys.To,
			Msg:    MsgType(ys.Msg),
			Fields: ys.Fields,
		})
	}

	// Updates — preserve YAML key order.
	if yt.Updates.Kind != 0 {
		updates, err := parseOrderedMapString(&yt.Updates)
		if err != nil {
			return t, fmt.Errorf("updates: %w", err)
		}
		for _, kv := range updates {
			t.Updates = append(t.Updates, VarUpdate{
				Var:  kv.key,
				Expr: kv.val,
			})
		}
	}

	return t, nil
}

// Ordered map parsing — YAML maps don't guarantee order, but
// yaml.Node preserves it.

type kv[V any] struct {
	key string
	val V
}

func parseOrderedMap[V any](node *yaml.Node) ([]kv[V], error) {
	if node.Kind == 0 {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping, got %d", node.Kind)
	}
	var result []kv[V]
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		var val V
		if err := node.Content[i+1].Decode(&val); err != nil {
			return nil, fmt.Errorf("key %s: %w", key, err)
		}
		result = append(result, kv[V]{key: key, val: val})
	}
	return result, nil
}

func parseOrderedMapString(node *yaml.Node) ([]kv[string], error) {
	if node.Kind == 0 {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping, got %d", node.Kind)
	}
	var result []kv[string]
	for i := 0; i < len(node.Content); i += 2 {
		result = append(result, kv[string]{
			key: node.Content[i].Value,
			val: node.Content[i+1].Value,
		})
	}
	return result, nil
}
