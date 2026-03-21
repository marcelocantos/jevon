// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestPairingCeremonyValidates(t *testing.T) {
	p := PairingCeremony()
	if err := p.Validate(); err != nil {
		t.Fatalf("PairingCeremony validation failed: %v", err)
	}
}

func noopActions(m *Machine, ids ...ActionID) {
	for _, id := range ids {
		m.RegisterAction(id, func(any) error { return nil })
	}
}

func TestMachineHappyPath(t *testing.T) {
	p := PairingCeremony()

	m, err := NewMachine(p, "jevond")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}

	m.RegisterGuard(GuardTokenValid, func(any) bool { return true })
	m.RegisterGuard(GuardTokenInvalid, func(any) bool { return false })
	m.RegisterGuard(GuardCodeCorrect, func(any) bool { return true })
	m.RegisterGuard(GuardCodeWrong, func(any) bool { return false })
	m.RegisterGuard(GuardDeviceKnown, func(any) bool { return true })
	m.RegisterGuard(GuardDeviceUnknown, func(any) bool { return false })
	m.RegisterGuard(GuardNonceFresh, func(any) bool { return true })

	noopActions(m,
		ActionGenerateToken, ActionRegisterRelay, ActionDeriveSecret,
		ActionStoreDevice, ActionVerifyDevice,
	)

	assertState := func(expected State) {
		t.Helper()
		if got := m.State(); got != expected {
			t.Fatalf("expected state %s, got %s", expected, got)
		}
	}

	assertState(JevondIdle)
	mustHandle(t, m, MsgPairBegin)
	assertState(JevondGenerateToken)
	mustStep(t, m) // -> RegisterRelay
	mustStep(t, m) // -> WaitingForClient
	mustHandle(t, m, MsgPairHello)
	assertState(JevondDeriveSecret)
	mustStep(t, m) // -> SendAck
	mustStep(t, m) // -> WaitingForCode
	mustHandle(t, m, MsgCodeSubmit)
	assertState(JevondValidateCode)
	mustStep(t, m) // -> StorePaired
	mustStep(t, m) // -> Paired
	assertState(JevondPaired)

	// Reconnection.
	mustHandle(t, m, MsgAuthRequest)
	mustStep(t, m) // -> SessionActive
	assertState(JevondSessionActive)
}

func TestMachineTokenRejection(t *testing.T) {
	p := PairingCeremony()
	m, err := NewMachine(p, "jevond")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}

	m.RegisterGuard(GuardTokenValid, func(any) bool { return false })
	m.RegisterGuard(GuardTokenInvalid, func(any) bool { return true })
	noopActions(m, ActionGenerateToken, ActionRegisterRelay)

	mustHandle(t, m, MsgPairBegin)
	mustStep(t, m) // -> RegisterRelay
	mustStep(t, m) // -> WaitingForClient
	mustHandle(t, m, MsgPairHello)

	if got := m.State(); got != JevondIdle {
		t.Fatalf("expected Idle after invalid token, got %s", got)
	}
}

func TestMachineCodeRejection(t *testing.T) {
	p := PairingCeremony()
	m, err := NewMachine(p, "jevond")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}

	m.RegisterGuard(GuardTokenValid, func(any) bool { return true })
	m.RegisterGuard(GuardTokenInvalid, func(any) bool { return false })
	m.RegisterGuard(GuardCodeCorrect, func(any) bool { return false })
	m.RegisterGuard(GuardCodeWrong, func(any) bool { return true })
	noopActions(m, ActionGenerateToken, ActionRegisterRelay, ActionDeriveSecret)

	mustHandle(t, m, MsgPairBegin)
	mustStep(t, m) // -> RegisterRelay
	mustStep(t, m) // -> WaitingForClient
	mustHandle(t, m, MsgPairHello)
	mustStep(t, m) // -> SendAck
	mustStep(t, m) // -> WaitingForCode
	mustHandle(t, m, MsgCodeSubmit)
	mustStep(t, m) // -> Idle (wrong code)

	if got := m.State(); got != JevondIdle {
		t.Fatalf("expected Idle after wrong code, got %s", got)
	}
}

func TestMachineRejectsInvalidMessage(t *testing.T) {
	p := PairingCeremony()
	m, err := NewMachine(p, "jevond")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}

	if _, err := m.HandleMessage(MsgAuthRequest, nil); err == nil {
		t.Fatal("expected error for invalid message in Idle state")
	}
}

func TestIOSMachineHappyPath(t *testing.T) {
	p := PairingCeremony()
	m, err := NewMachine(p, "ios")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}

	noopActions(m, ActionSendPairHello, ActionDeriveSecret, ActionStoreSecret)

	mustStep(t, m) // -> ScanQR
	mustStep(t, m) // -> ConnectRelay
	mustStep(t, m) // -> GenKeyPair
	mustStep(t, m) // -> WaitAck
	mustHandle(t, m, MsgPairHelloAck)
	mustHandle(t, m, MsgPairConfirm)

	if got := m.State(); got != AppShowCode {
		t.Fatalf("expected ShowCode, got %s", got)
	}

	mustStep(t, m) // -> WaitPairComplete
	mustHandle(t, m, MsgPairComplete)

	if got := m.State(); got != AppPaired {
		t.Fatalf("expected Paired, got %s", got)
	}

	mustStep(t, m) // -> Reconnect
	mustStep(t, m) // -> SendAuth
	mustHandle(t, m, MsgAuthOk)

	if got := m.State(); got != AppSessionActive {
		t.Fatalf("expected SessionActive, got %s", got)
	}
}

func TestCLIMachineHappyPath(t *testing.T) {
	p := PairingCeremony()
	m, err := NewMachine(p, "cli")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}

	mustStep(t, m)
	mustStep(t, m)
	mustHandle(t, m, MsgTokenResponse)
	mustHandle(t, m, MsgWaitingForCode)
	mustStep(t, m)
	mustHandle(t, m, MsgPairStatus)

	if got := m.State(); got != CLIDone {
		t.Fatalf("expected Done, got %s", got)
	}
}

func TestExportTLA(t *testing.T) {
	p := PairingCeremony()
	var buf bytes.Buffer
	if err := p.ExportTLA(&buf); err != nil {
		t.Fatalf("ExportTLA: %v", err)
	}

	spec := buf.String()

	checks := map[string][]string{
		"structural": {
			"MODULE PairingCeremony",
			"EXTENDS Integers",
			"jevond_state", "ios_state", "cli_state",
			"chan_cli_jevond", "chan_jevond_ios",
			"adversary_knowledge", "process Adversary", "recv_msg",
		},
		"variables": {
			"active_tokens = {}", "used_tokens = {}",
			"paired_devices = {}",
			`current_token = "none"`,
			`server_shared_key = "none"`, `client_shared_key = "none"`,
			`server_code = "none"`, `ios_code = "none"`,
			`device_secret = "none"`,
			"adversary_keys = {}",
			`adv_ecdh_pub = "adv_pub"`,
			"auth_nonces_used = {}",
			"code_attempts = 0",
		},
		"operators": {
			"DeriveKey(a, b)", "DeriveCode(a, b)",
		},
		"guards": {
			"token_valid ==", "token_invalid ==",
			"code_correct ==", "device_known ==", "nonce_fresh ==",
		},
		"sends": {
			"Append(chan_cli_jevond", "Append(chan_jevond_cli",
			"Append(chan_jevond_ios", "Append(chan_ios_jevond",
		},
		"updates": {
			`active_tokens := active_tokens \union`,
			`used_tokens := used_tokens \union`,
			`paired_devices := paired_devices \union`,
			"DeriveKey(", "DeriveCode(",
			"code_attempts := code_attempts + 1",
			`auth_nonces_used := auth_nonces_used \union`,
		},
		"adversary_actions": {
			"QR_shoulder_surf", "MitM_pair_hello", "MitM_pair_hello_ack",
			"MitM_reencrypt_secret", "concurrent_pair",
			"token_bruteforce", "code_guess", "session_replay",
		},
		"properties": {
			"NoTokenReuse", "MitMDetectedByCodeMismatch", "MitMPrevented",
			"AuthRequiresCompletedPairing", "NoNonceReuse",
			"WrongCodeDoesNotPair", "DeviceSecretSecrecy",
			"HonestPairingCompletes",
		},
	}

	for category, items := range checks {
		for _, check := range items {
			if !strings.Contains(spec, check) {
				t.Errorf("TLA+ spec missing %s: %q", category, check)
			}
		}
	}
}

func TestExportTLAKeyBoundCodes(t *testing.T) {
	p := PairingCeremony()
	var buf bytes.Buffer
	if err := p.ExportTLA(&buf); err != nil {
		t.Fatalf("ExportTLA: %v", err)
	}

	spec := buf.String()

	// Server computes code from its view of pubkeys.
	if !strings.Contains(spec, `server_code := DeriveCode("server_pub", recv_msg.pubkey)`) {
		t.Error("TLA+ spec missing server-side DeriveCode")
	}

	// iOS computes code from its view of pubkeys.
	if !strings.Contains(spec, `ios_code := DeriveCode(received_server_pub, "client_pub")`) {
		t.Error("TLA+ spec missing ios-side DeriveCode")
	}

	// CLI sends ios_code (what the user read from the phone).
	if !strings.Contains(spec, "code |-> ios_code") {
		t.Error("TLA+ spec missing CLI sending ios_code")
	}

	// pair_confirm should NOT carry a code or key (it's just a signal now).
	// Check that pair_confirm send has no code field.
	if strings.Contains(spec, `MSG_pair_confirm, code`) {
		t.Error("TLA+ spec: pair_confirm should not carry code (key-bound codes are computed independently)")
	}
}

func TestValidateDetectsDuplicateActor(t *testing.T) {
	p := &Protocol{
		Name: "Bad",
		Actors: []Actor{
			{Name: "a", Initial: "S0"},
			{Name: "a", Initial: "S1"},
		},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for duplicate actor")
	}
}

func TestValidateDetectsUndeclaredMessage(t *testing.T) {
	p := &Protocol{
		Name: "Bad",
		Actors: []Actor{
			{Name: "a", Initial: "S0", Transitions: []Transition{
				{From: "S0", To: "S1", On: Recv("nonexistent")},
			}},
		},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for undeclared message")
	}
}

func TestValidateDetectsUndefinedGuard(t *testing.T) {
	p := &Protocol{
		Name: "Bad",
		Actors: []Actor{
			{Name: "a", Initial: "S0", Transitions: []Transition{
				{From: "S0", To: "S1", On: Internal("x"), Guard: "missing_guard"},
			}},
		},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for undefined guard")
	}
}

func TestValidateDetectsWrongSender(t *testing.T) {
	p := &Protocol{
		Name: "Bad",
		Actors: []Actor{
			{Name: "a", Initial: "S0", Transitions: []Transition{
				{From: "S0", To: "S1", On: Internal("x"), Sends: []Send{
					{To: "b", Msg: "msg1"},
				}},
			}},
			{Name: "b", Initial: "S0"},
		},
		Messages: []Message{
			{Type: "msg1", From: "b", To: "a"},
		},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for wrong sender")
	}
}

// Helpers.

func mustHandle(t *testing.T, m *Machine, msg MsgType) {
	t.Helper()
	if _, err := m.HandleMessage(msg, nil); err != nil {
		t.Fatalf("HandleMessage(%s): %v", msg, err)
	}
}

func mustStep(t *testing.T, m *Machine) {
	t.Helper()
	if _, err := m.Step(nil); err != nil {
		t.Fatalf("Step from %s: %v", m.State(), err)
	}
}
