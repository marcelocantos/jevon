// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package cli provides shared values for jevon binaries.
package cli

import _ "embed"

// Version is set at build time via -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

//go:embed help_agent.md
var AgentGuide string
