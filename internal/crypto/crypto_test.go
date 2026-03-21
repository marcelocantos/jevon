// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package crypto

import (
	"bytes"
	"testing"
)

func TestKeyExchangeAndEncrypt(t *testing.T) {
	// Simulate client and server generating key pairs.
	client, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	server, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	// Derive session keys — client sends, server receives.
	clientSendKey, err := DeriveSessionKey(client.Private, server.Public, []byte("client-to-server"))
	if err != nil {
		t.Fatal(err)
	}
	serverRecvKey, err := DeriveSessionKey(server.Private, client.Public, []byte("client-to-server"))
	if err != nil {
		t.Fatal(err)
	}

	// Keys should match.
	if !bytes.Equal(clientSendKey, serverRecvKey) {
		t.Fatal("derived keys don't match")
	}

	// Reverse direction.
	clientRecvKey, err := DeriveSessionKey(client.Private, server.Public, []byte("server-to-client"))
	if err != nil {
		t.Fatal(err)
	}
	serverSendKey, err := DeriveSessionKey(server.Private, client.Public, []byte("server-to-client"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(clientRecvKey, serverSendKey) {
		t.Fatal("reverse derived keys don't match")
	}

	// Create channels.
	clientCh, err := NewChannel(clientSendKey, clientRecvKey)
	if err != nil {
		t.Fatal(err)
	}
	serverCh, err := NewChannel(serverSendKey, serverRecvKey)
	if err != nil {
		t.Fatal(err)
	}

	// Client → server.
	msg := []byte("hello from client")
	ct := clientCh.Encrypt(msg)
	pt, err := serverCh.Decrypt(ct)
	if err != nil {
		t.Fatal("server decrypt:", err)
	}
	if !bytes.Equal(pt, msg) {
		t.Fatalf("got %q, want %q", pt, msg)
	}

	// Server → client.
	msg2 := []byte("hello from server")
	ct2 := serverCh.Encrypt(msg2)
	pt2, err := clientCh.Decrypt(ct2)
	if err != nil {
		t.Fatal("client decrypt:", err)
	}
	if !bytes.Equal(pt2, msg2) {
		t.Fatalf("got %q, want %q", pt2, msg2)
	}
}

func TestSymmetricChannel(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}

	clientCh, err := NewSymmetricChannel(secret, false)
	if err != nil {
		t.Fatal(err)
	}
	serverCh, err := NewSymmetricChannel(secret, true)
	if err != nil {
		t.Fatal(err)
	}

	// Client → server.
	msg := []byte(`{"type":"auth","device":"abc123"}`)
	ct := clientCh.Encrypt(msg)
	pt, err := serverCh.Decrypt(ct)
	if err != nil {
		t.Fatal("server decrypt:", err)
	}
	if !bytes.Equal(pt, msg) {
		t.Fatalf("got %q, want %q", pt, msg)
	}

	// Server → client.
	msg2 := []byte(`{"type":"auth_ok"}`)
	ct2 := serverCh.Encrypt(msg2)
	pt2, err := clientCh.Decrypt(ct2)
	if err != nil {
		t.Fatal("client decrypt:", err)
	}
	if !bytes.Equal(pt2, msg2) {
		t.Fatalf("got %q, want %q", pt2, msg2)
	}

	// Multiple messages in sequence.
	for i := 0; i < 100; i++ {
		msg := []byte("message from client")
		ct := clientCh.Encrypt(msg)
		pt, err := serverCh.Decrypt(ct)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if !bytes.Equal(pt, msg) {
			t.Fatalf("round %d: mismatch", i)
		}
	}
}

func TestReplayRejected(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	clientCh, err := NewSymmetricChannel(secret, false)
	if err != nil {
		t.Fatal(err)
	}
	serverCh, err := NewSymmetricChannel(secret, true)
	if err != nil {
		t.Fatal(err)
	}

	ct := clientCh.Encrypt([]byte("first"))
	if _, err := serverCh.Decrypt(ct); err != nil {
		t.Fatal(err)
	}

	// Replay the same ciphertext — should fail.
	if _, err := serverCh.Decrypt(ct); err == nil {
		t.Fatal("expected replay to be rejected")
	}
}

func TestDeriveKeyFromSecret(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := GenerateNonce()
	if err != nil {
		t.Fatal(err)
	}

	key1, err := DeriveKeyFromSecret(secret, nonce)
	if err != nil {
		t.Fatal(err)
	}
	key2, err := DeriveKeyFromSecret(secret, nonce)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(key1, key2) {
		t.Fatal("same inputs should produce same key")
	}

	// Different nonce → different key.
	nonce2, _ := GenerateNonce()
	key3, err := DeriveKeyFromSecret(secret, nonce2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(key1, key3) {
		t.Fatal("different nonces should produce different keys")
	}
}

func TestDeriveConfirmationCode(t *testing.T) {
	client, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	server, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	// Both sides compute the same code.
	code1, err := DeriveConfirmationCode(server.Public, client.Public)
	if err != nil {
		t.Fatal(err)
	}
	code2, err := DeriveConfirmationCode(client.Public, server.Public)
	if err != nil {
		t.Fatal(err)
	}
	if code1 != code2 {
		t.Fatalf("codes should be order-independent: %s vs %s", code1, code2)
	}
	if len(code1) != 6 {
		t.Fatalf("expected 6-digit code, got %q", code1)
	}

	// MitM: adversary substitutes its own key — codes diverge.
	adversary, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	codeServer, err := DeriveConfirmationCode(server.Public, adversary.Public)
	if err != nil {
		t.Fatal(err)
	}
	codeClient, err := DeriveConfirmationCode(adversary.Public, client.Public)
	if err != nil {
		t.Fatal(err)
	}
	if codeServer == codeClient {
		t.Fatal("MitM codes should differ (collision — extremely unlikely)")
	}
	if codeServer == code1 {
		t.Fatal("MitM server code should differ from honest code")
	}
	if codeClient == code1 {
		t.Fatal("MitM client code should differ from honest code")
	}
}
