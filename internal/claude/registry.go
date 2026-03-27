// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

// AgentDef is the persistent definition of an agent.
type AgentDef struct {
	Name      string `json:"name"`               // unique identifier
	WorkDir   string `json:"workdir"`             // working directory
	SessionID string `json:"session_id"`          // persistent Claude session ID
	Model     string `json:"model,omitempty"`     // model override
	AutoStart bool   `json:"auto_start"`          // start on jevond startup
	Parent    string `json:"parent,omitempty"`    // parent agent name (for tree display)
}

// Registry manages persistent agent definitions and their running processes.
type Registry struct {
	path string // path to agents.json

	mu     sync.Mutex
	agents map[string]*AgentDef
	procs  map[string]*Process
}

// NewRegistry loads or creates an agent registry at the given path.
func NewRegistry(path string) (*Registry, error) {
	r := &Registry{
		path:   path,
		agents: make(map[string]*AgentDef),
		procs:  make(map[string]*Process),
	}

	data, err := os.ReadFile(path)
	if err == nil {
		var defs []AgentDef
		if err := json.Unmarshal(data, &defs); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for i := range defs {
			r.agents[defs[i].Name] = &defs[i]
		}
	}

	return r, nil
}

// save writes the registry to disk.
func (r *Registry) save() error {
	defs := make([]AgentDef, 0, len(r.agents))
	for _, d := range r.agents {
		defs = append(defs, *d)
	}
	data, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o644)
}

// Register adds or updates an agent definition and saves to disk.
func (r *Registry) Register(def AgentDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if def.SessionID == "" {
		return fmt.Errorf("agent %q: session_id required", def.Name)
	}
	r.agents[def.Name] = &def
	return r.save()
}

// Remove removes an agent definition and stops it if running.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if proc, ok := r.procs[name]; ok {
		proc.Stop()
		delete(r.procs, name)
	}
	delete(r.agents, name)
	return r.save()
}

// Start starts a registered agent. No-op if already running.
func (r *Registry) Start(name string) (*Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if proc, ok := r.procs[name]; ok && proc.Alive() {
		return proc, nil
	}

	def, ok := r.agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q not registered", name)
	}

	mcpConfig := filepath.Join(def.WorkDir, ".mcp.json")
	if _, err := os.Stat(mcpConfig); err != nil {
		mcpConfig = "" // no MCP config in workdir
	}

	proc, err := Start(Config{
		WorkDir:   def.WorkDir,
		SessionID: def.SessionID,
		Model:     def.Model,
		MCPConfig: mcpConfig,
	})
	if err != nil {
		return nil, err
	}

	r.procs[name] = proc
	slog.Info("agent started", "name", name, "session", proc.SessionID())
	return proc, nil
}

// Stop stops a running agent.
func (r *Registry) Stop(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if proc, ok := r.procs[name]; ok {
		proc.Stop()
		delete(r.procs, name)
		slog.Info("agent stopped", "name", name)
	}
}

// Get returns the running process for an agent, or nil.
func (r *Registry) Get(name string) *Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.procs[name]
}

// Def returns the definition for an agent.
func (r *Registry) Def(name string) *AgentDef {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.agents[name]; ok {
		cp := *d
		return &cp
	}
	return nil
}

// List returns all registered agent definitions.
func (r *Registry) List() []AgentDef {
	r.mu.Lock()
	defer r.mu.Unlock()
	defs := make([]AgentDef, 0, len(r.agents))
	for _, d := range r.agents {
		cp := *d
		cp.Name = d.Name // ensure name is set
		defs = append(defs, cp)
	}
	return defs
}

// StartAll starts all agents marked with auto_start.
func (r *Registry) StartAll() {
	r.mu.Lock()
	names := make([]string, 0)
	for name, def := range r.agents {
		if def.AutoStart {
			names = append(names, name)
		}
	}
	r.mu.Unlock()

	for _, name := range names {
		if _, err := r.Start(name); err != nil {
			slog.Error("auto-start failed", "agent", name, "err", err)
		}
	}
}

// StopAll stops all running agents.
func (r *Registry) StopAll() {
	r.mu.Lock()
	names := make([]string, 0, len(r.procs))
	for name := range r.procs {
		names = append(names, name)
	}
	r.mu.Unlock()

	for _, name := range names {
		r.Stop(name)
	}
}

// EnsureAgent registers an agent if it doesn't exist, using the given
// defaults. If an agent with the same workdir exists under a different
// name, it is renamed. Returns the definition (existing or new).
func (r *Registry) EnsureAgent(name, workDir, model string, autoStart bool) (*AgentDef, error) {
	r.mu.Lock()
	if def, ok := r.agents[name]; ok {
		r.mu.Unlock()
		return def, nil
	}

	// Check if an agent with the same workdir exists under a different name.
	for oldName, def := range r.agents {
		if def.WorkDir == workDir {
			slog.Info("renaming agent", "from", oldName, "to", name)
			delete(r.agents, oldName)
			def.Name = name
			r.agents[name] = def
			// Migrate any running process to the new name.
			if proc, ok := r.procs[oldName]; ok {
				delete(r.procs, oldName)
				r.procs[name] = proc
			}
			err := r.save()
			r.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return def, nil
		}
	}
	r.mu.Unlock()

	def := AgentDef{
		Name:      name,
		WorkDir:   workDir,
		SessionID: newSessionID(),
		Model:     model,
		AutoStart: autoStart,
	}
	if err := r.Register(def); err != nil {
		return nil, err
	}
	return &def, nil
}

func newSessionID() string {
	return uuid.New().String()
}
