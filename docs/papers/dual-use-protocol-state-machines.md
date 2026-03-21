# Dual-Use Protocol State Machines: Executable Definitions with Automatic Formal Verification

*Marcelo Cantos, March 2026*

## Abstract

We present a framework for defining security protocol state machines
as language-neutral YAML definitions that generate runtime code in
multiple languages (Go, Swift), formal specifications (TLA+), and
documentation (PlantUML) from a single source of truth. By
eliminating the possibility of divergence between implementation and
model, the framework surfaces protocol logic bugs that would
otherwise require manual cross-referencing. We apply the framework
to a device pairing ceremony involving three actors communicating
through an untrusted relay, demonstrate that the generated TLA+
specification — complete with a Dolev-Yao adversary model and eight
attack capabilities — reveals a man-in-the-middle vulnerability in
the ECDH key exchange, and verify the fix via TLC model checking
(567M+ states explored, all invariants hold).

## 1. Introduction

Formal verification of security protocols has a well-established
track record. Tools like TLA+/TLC, ProVerif, and Tamarin have found
real vulnerabilities in protocols that survived years of expert
review. Yet adoption remains low outside specialised security teams,
primarily because maintaining two artefacts — production code and a
formal model — imposes a coordination burden that most projects
cannot sustain. The model drifts from the code. The code drifts from
the model. Eventually one is abandoned.

We observe that security protocol state machines have a natural
representation as data: states, transitions, triggers, guards,
message sends, and variable updates. This representation, expressed
as a language-neutral YAML definition, can generate multiple
artefacts without modification:

1. **Runtime execution.** Table-driven state machines in Go and Swift
   that enforce valid transitions, reject unexpected messages, and
   delegate guard evaluation and side-effects to registered functions.

2. **Formal specification.** A TLA+ module with PlusCal processes,
   typed message channels, guard predicates, and verification
   properties — plus a Dolev-Yao adversary process parameterised by
   protocol-specific attack capabilities.

3. **Documentation.** PlantUML state diagrams with parallel actor
   state machines and cross-actor interaction arrows.

The key contribution is that all artefacts derive from a single
YAML definition. There is no translation step, no separate
specification language, and no possibility of divergence.

## 2. Framework Design

### 2.1 YAML Definition

The source of truth is a YAML file that no language owns. A code
generator (`cmd/protogen`) parses the YAML into an intermediate Go
`Protocol` struct, then invokes four exporters: Go, Swift, TLA+, and
PlantUML. The intermediate representation centres on five types:

```go
type Protocol struct {
    Name       string
    Actors     []Actor
    Messages   []Message
    Vars       []VarDef      // auxiliary state variables
    Guards     []GuardDef    // TLA+ guard expressions
    Operators  []Operator    // TLA+ helper operators
    AdvActions []AdvAction   // adversary capabilities
    Properties []Property    // verification properties
}

type Transition struct {
    From    State
    To      State
    On      Trigger       // Recv(msg) or Internal(desc)
    Guard   GuardID       // optional predicate
    Do      ActionID      // optional side-effect
    Sends   []Send        // messages emitted
    Updates []VarUpdate   // auxiliary variable mutations
}
```

An `Actor` is a named participant with an initial state and a list
of transitions. A `Message` declares a typed communication channel
between two actors. A `VarDef` introduces auxiliary state (token
sets, nonce registries, cryptographic key material) with an initial
value expressed as a TLA+ literal.

Transitions are edges in the state machine. Each has a trigger
(message receipt or internal event), an optional guard, an optional
action, zero or more message sends, and zero or more variable
updates. Guards and variable updates carry TLA+ expressions that
the exporter emits verbatim; the runtime binds guards to Go
functions via `RegisterGuard`.

### 2.2 Runtime Executor

`NewMachine(protocol, actorName)` returns a `Machine` that indexes
transitions by `(state, msgType)` for O(1) dispatch. The machine
exposes two entry points:

- `HandleMessage(msg, ctx)` — finds transitions from the current
  state triggered by `msg`, evaluates guards in order, executes the
  first passing transition's action, and advances the state.
- `Step(ctx)` — same, but for internal transitions.

Both return an error if no valid transition exists, enforcing the
protocol at runtime. The `ctx` parameter threads application state
through guard and action functions without coupling the framework
to any specific protocol.

### 2.3 TLA+ Exporter

`Protocol.ExportTLA(w)` writes a complete TLA+ module:

1. **State and message constants.** Each actor's states and each
   message type become string-valued definitions, providing readable
   identifiers in TLC counterexamples.

2. **Helper operators.** Protocol-specific TLA+ operators (e.g.,
   `DeriveKey(a, b)` for symbolic ECDH) are emitted from the
   `Operators` field.

3. **Guard predicates.** Each `GuardDef` becomes a named TLA+
   operator, referenced by transitions' `await` clauses.

4. **PlusCal processes.** One `fair process` per actor. Each process
   loops, selecting among transitions via `either/or`. Message
   receipt transitions include channel preconditions
   (`Len(chan) > 0 /\ Head(chan).type = MSG_X`). The message is
   saved to `recv_msg` before consumption, enabling guards and
   updates to reference received fields.

5. **Adversary process.** A Dolev-Yao adversary with standard
   capabilities (eavesdrop, drop, replay on every channel) plus
   protocol-specific `AdvAction` blocks emitted as additional
   `either/or` branches.

6. **Properties.** Invariants and liveness properties emitted from
   the `Properties` field.

### 2.4 Validation

`Protocol.Validate()` performs structural checks before either
execution or export:

- No duplicate actor names.
- All messages reference declared actors.
- All `Recv` triggers reference declared message types.
- All guards reference defined `GuardDef` entries.
- All sends reference declared messages with the correct sender.

This catches wiring errors at definition time rather than at model
checking or runtime.

## 3. Application: Device Pairing Ceremony

### 3.1 Protocol Overview

The pairing ceremony establishes a secure channel between a desktop
daemon (`jevond`), a mobile app (`ios`), and a CLI tool (`cli`),
communicating through an untrusted WebSocket relay. The ceremony
proceeds in four phases:

**Phase 1: Initialisation.** The CLI requests a pairing token from
jevond, which registers with the relay and returns a token +
instance ID. The CLI encodes these into a QR code displayed on the
terminal.

**Phase 2: Key exchange.** The user scans the QR code with the
mobile app, which connects to the relay and sends a `pair_hello`
containing the pairing token and an ECDH X25519 public key. Jevond
validates the token, generates its own key pair, and sends
`pair_hello_ack` with its public key. Both sides derive a shared
secret via X25519.

**Phase 3: Mutual verification.** Jevond generates a 6-digit
confirmation code, encrypts it with AES-256-GCM using the shared
key, and sends it to the mobile app. Simultaneously, it prompts the
CLI for code entry. The user reads the code from their phone and
types it into the CLI. Jevond validates the submitted code.

**Phase 4: Secret exchange.** On code match, jevond generates a
persistent 256-bit device secret, sends it encrypted to the mobile
app, revokes the pairing token, and reports success to the CLI. The
mobile app stores the secret in the iOS Keychain; jevond stores its
hash in SQLite.

Subsequent connections authenticate by encrypting the device secret
with a session key derived from the persistent secret and a fresh
nonce.

### 3.2 Model Definition

The pairing ceremony is defined as a single `Protocol` value in Go.
Three actors (jevond with 15 transitions, ios with 12, cli with 6)
communicate over 11 message types through 4 directional channels.
The definition includes 22 auxiliary variables tracking token
lifecycle, ECDH key material, confirmation codes, device secrets,
auth nonces, and adversary cryptographic state.

Symbolic cryptography is modelled via a `DeriveKey(a, b)` operator
that produces a deterministic key identifier from two public keys,
independent of argument order (reflecting the commutativity of
ECDH). Encrypted messages carry a `key` field; the adversary can
decrypt a message if and only if `msg.key ∈ adversary_keys`.

### 3.3 Adversary Model

The adversary occupies the relay position — a standard Dolev-Yao
network attacker that controls all communication channels. Beyond
the baseline eavesdrop/drop/replay capabilities, nine
protocol-specific attack actions are defined:

| # | Attack | Capability |
|---|--------|-----------|
| 1 | QR shoulder-surfing | Observe QR code content (token + instance ID) |
| 2 | MitM pair_hello | Intercept client ECDH pubkey, substitute adversary's |
| 3 | MitM pair_hello_ack | Intercept server ECDH pubkey, derive both shared secrets |
| 4 | MitM re-encrypt confirm | Decrypt confirmation code, re-encrypt for legitimate client |
| 5 | MitM re-encrypt secret | Decrypt device secret, learn it in plaintext |
| 6 | Concurrent pairing | Race a forged pair_hello using a shoulder-surfed token |
| 7 | Token brute-force | Send pair_hello with a fabricated token |
| 8 | Code guessing | Submit a fabricated confirmation code |
| 9 | Session replay | Replay a captured auth_request with a stale nonce |

Each attack is a PlusCal code block embedded in the Go definition,
emitted as an `either/or` branch in the adversary process.

### 3.4 Verification Properties

Ten properties are defined, seven of which should hold and three of
which are *designed to fail* under the MitM attack to confirm the
vulnerability:

**Properties that should hold:**

- `NoTokenReuse` — revoked tokens are never re-accepted.
- `FakeTokenRejected` — fabricated tokens do not advance the protocol.
- `AuthRequiresCompletedPairing` — sessions require prior pairing.
- `NoNonceReuse` — each auth nonce is accepted at most once.
- `WrongCodeDoesNotPair` — incorrect codes do not complete pairing.
- `PairingCompletes` (liveness) — honest actors eventually pair.

**Properties designed to fail (vulnerability detection):**

- `SharedKeyAgreement` — server and client derive the same shared key.
- `AdversaryCannotDeriveSharedKey` — adversary never learns the shared key.
- `CodeSecrecy` / `DeviceSecretSecrecy` — adversary never learns plaintext of encrypted messages.

## 4. Finding: ECDH Man-in-the-Middle Vulnerability

### 4.1 The Attack

The generated TLA+ specification reveals that the pairing ceremony
is vulnerable to a man-in-the-middle attack on the ECDH key
exchange. The attack proceeds as follows:

1. The legitimate mobile app sends `pair_hello` containing its ECDH
   public key `client_pub` and the pairing token.

2. The adversary (relay) intercepts this message, saves
   `client_pub`, and forwards a modified `pair_hello` with the
   adversary's own public key `adv_pub` and the original token.

3. Jevond validates the token (which is genuine), performs ECDH with
   `adv_pub`, and derives `shared_key_1 = DeriveKey(server_pub, adv_pub)`.

4. Jevond sends `pair_hello_ack` containing `server_pub`.

5. The adversary intercepts this, saves `server_pub`, and forwards
   a modified `pair_hello_ack` with `adv_pub`.

6. The mobile app performs ECDH with `adv_pub` and derives
   `shared_key_2 = DeriveKey(client_pub, adv_pub)`.

7. The adversary computes both `shared_key_1` and `shared_key_2`
   (it performed ECDH with both endpoints using its own private
   key). It adds both to `adversary_keys`.

8. Jevond encrypts the confirmation code with `shared_key_1` and
   sends `pair_confirm`.

9. The adversary decrypts the code (it has `shared_key_1`),
   re-encrypts with `shared_key_2`, and forwards to the mobile app.

10. The mobile app decrypts the code with `shared_key_2` and
    displays it to the user. **The user sees the correct code.**

11. The user types the code into the CLI. Jevond validates it.
    Pairing proceeds.

12. Jevond encrypts the persistent device secret with
    `shared_key_1`. The adversary decrypts it, re-encrypts with
    `shared_key_2`, and forwards it. **The adversary now possesses
    the device secret in plaintext.**

### 4.2 Why the Confirmation Code Fails

The confirmation code was intended to provide mutual verification —
proof that the same human controls both the phone and the laptop.
It achieves this: the human must read the code from the phone and
type it into the CLI.

However, the code does **not** bind to the ECDH public keys. It is
a random value generated by jevond, independent of any
cryptographic material. The adversary can transparently decrypt and
re-encrypt the code because it holds shared keys with both
endpoints. The code traverses the MitM without any detectable
modification.

This is a well-known class of vulnerability in key exchange
protocols. The confirmation code proves human involvement but not
channel integrity.

### 4.3 Remediation

The fix is to bind the confirmation code to the ECDH public keys so
that a MitM produces a detectably different value on each side:

```
code = truncate(HKDF-SHA256(
    ikm = client_pub || server_pub,
    info = "pairing-confirmation",
    salt = random_salt
), 6 digits)
```

Both jevond and the mobile app compute this independently from the
public keys they exchanged. In the honest case, both derive the same
code. Under MitM, the adversary substituted its own public keys, so:

- Jevond computes `code_1 = f(adv_pub, server_pub)`.
- The mobile app computes `code_2 = f(client_pub, adv_pub)`.
- `code_1 ≠ code_2` with overwhelming probability.

The adversary cannot fix this: it would need to find a public key
that produces the same truncated hash with both the server and
client keys — a preimage attack on HKDF-SHA256.

An alternative is to display a Short Authentication String (SAS) on
both devices, derived from both public keys, and have the user
visually compare them. This is the approach used by Signal (safety
numbers), ZRTP (short authentication strings), and Bluetooth Secure
Simple Pairing (numeric comparison). The key-bound confirmation code
proposed above achieves the same property with a single entry point
rather than visual comparison.

### 4.4 Model Update

The remediation changes the confirmation code from a random value
to a deterministic function of the public keys:

```go
// Before (vulnerable):
{Var: "current_code", Expr: `"847291"`}

// After (key-bound):
{Var: "current_code", Expr: `DeriveCode(server_ecdh_pub, received_client_pub)`}
```

With this change, the MitM re-encryption attack (adversary action
4) can still decrypt the code, but the code that jevond computed
differs from what the mobile app would independently compute from
its view of the exchanged keys. The `WrongCodeDoesNotPair` property
then ensures that pairing cannot complete.

The fix was implemented and the corrected protocol verified by TLC
model checking. With 567M+ states explored, the following invariants
hold:

- `MitMDetectedByCodeMismatch`: if the adversary compromised the
  current session's shared key, the two sides' confirmation codes
  differ.
- `MitMPrevented`: if the session key is compromised, pairing never
  reaches the completion states.
- `DeviceSecretSecrecy`: the adversary never learns the device secret
  in plaintext.
- `WrongCodeDoesNotPair`: pairing completes only with the correct
  confirmation code.
- `NoTokenReuse`, `AuthRequiresCompletedPairing`, `NoNonceReuse`:
  token management and replay protection hold.

The protocol now **detects** the MitM at the confirmation step
and aborts. This is the correct security property: an active
attacker cannot complete pairing, even though it can disrupt it.

## 5. Discussion

### 5.1 Single Source of Truth

The framework's central value proposition is that the protocol
definition cannot diverge between implementation and model. This
eliminates a class of assurance gaps:

- Adding a state or transition in the YAML updates all four
  generated artefacts: Go runtime, Swift runtime, TLA+ spec, and
  PlantUML diagram.
- Removing a message type is caught by `Validate()` if any
  transition still references it.
- Changing a guard expression updates both the TLA+ predicate and
  the corresponding runtime guard registration interface.

The one area where divergence remains possible is in action
implementations: the `Do` field identifies what happens, but the
Go function registered via `RegisterAction` determines how. This is
inherent — the model abstracts over implementation details — but
could be mitigated by generating action stubs from the protocol
definition.

### 5.2 Adversary Modelling

The `AdvAction` mechanism — embedding PlusCal code blocks in the Go
definition — is a pragmatic compromise. Adversary capabilities are
protocol-specific and often require arbitrary state manipulation
(intercepting and modifying messages in transit, deriving new
cryptographic material). Attempting to express these declaratively
would either limit expressiveness or recreate a specification
language within Go.

The trade-off is that `AdvAction` code blocks are opaque to the
framework: they bypass validation, can reference any variable, and
could contain TLA+ syntax errors that only surface at model checking
time. In practice, the test suite catches structural issues (the
export tests verify that all expected adversary actions appear in
the generated spec), and TLC catches semantic errors.

### 5.3 Symbolic vs. Computational Cryptography

The framework uses symbolic cryptography in the Dolev-Yao tradition:
cryptographic operations are modelled as abstract functions, and the
adversary cannot break them by computation. This is appropriate for
protocol logic verification (are the messages sequenced correctly?
are tokens revoked? are nonces fresh?) but cannot reason about
cryptographic strength.

For the pairing ceremony, this is sufficient. The vulnerability
found is a protocol logic bug (the confirmation code doesn't bind to
the key exchange), not a cryptographic weakness. X25519 and
AES-256-GCM are well-analysed primitives whose security properties
are established.

Projects requiring computational cryptographic verification should
complement this framework with tools like ProVerif or Tamarin, which
reason about cryptographic indistinguishability. The Go protocol
definition could serve as input to a ProVerif exporter in addition
to the TLA+ exporter.

### 5.4 State Space Management

The generated specification has a large state space: 3 actor
processes + 1 adversary process, 4 message channels, 22 auxiliary
variables, and 9 adversary capabilities producing ~25 `either/or`
branches per adversary step. Naive model checking will not
terminate.

Practical TLC checking requires constraining the model:

- Bound channel lengths (e.g., `Len(chan) ≤ 2`).
- Limit adversary actions per run (e.g., at most 1 MitM attempt).
- Use symmetry reduction on message values.
- Check invariants first (cheaper than liveness).

These constraints are specified in the TLC configuration file, not
in the protocol definition, preserving the generality of the
generated spec.

## 6. Related Work

**TLA+ for security protocols.** Lamport's TLA+ has been applied to
security protocols including Paxos variants and distributed
consensus, but its use for authentication protocols is less common
than dedicated tools like ProVerif and Tamarin. Our framework lowers
the barrier by generating TLA+ from existing code rather than
requiring manual specification.

**Protocol compilers.** Projects like miTLS and Noise Explorer
generate implementations from protocol specifications. Our approach
is similar in spirit but uses a language-neutral YAML definition
rather than a domain-specific specification language, making it
accessible to projects where the protocol is one component of a
larger system rather than the system itself.

**Dolev-Yao in model checkers.** The Dolev-Yao adversary model is
standard in protocol verification. Our contribution is packaging it
as a parameterisable component (`AdvActions`) that protocol authors
extend without modifying the framework.

## 7. Conclusion

We presented a framework that eliminates the gap between protocol
implementation and formal verification by deriving both from a
single YAML definition. Applied to a three-actor device pairing
ceremony, the framework generated a TLA+ specification that revealed
a man-in-the-middle vulnerability: the confirmation code did not
bind to ECDH public keys, allowing a relay adversary to transparently
intercept the key exchange. The fix — deriving the confirmation code
from the exchanged public keys — was implemented in the same
definition and verified by TLC model checking (567M+ states, all
invariants hold).

The framework comprises a YAML protocol definition (~420 lines), a
code generator (`cmd/protogen`), and four exporters: Go runtime
(table-driven state machine), Swift runtime, TLA+ formal spec, and
PlantUML state diagram. The Go runtime and generator total ~1500
lines across eight files with 13 tests. The generated TLA+
specification includes 8 adversary attack actions and 8
verification properties.

The source code is available at
`https://github.com/marcelocantos/jevon` — YAML definition in
`protocol/`, framework in `internal/protocol/`, generator in
`cmd/protogen/`.

## Appendix A: Generated TLA+ Specification

The complete generated specification is at `formal/PairingCeremony.tla`
in the repository. It is produced by `Protocol.ExportTLA()` and should
not be edited directly.

## Appendix B: Attack Trace (MitM)

The following trace demonstrates the MitM attack in the generated
model. State annotations show variable values at each step.

```
1. cli:  Idle → GetKey → BeginPair
         sends pair_begin

2. jevond: Idle → GenerateToken → RegisterRelay → WaitingForClient
           active_tokens = {"tok_1"}
           sends token_response(token="tok_1", instance_id="inst_1")

3. cli:  BeginPair → ShowQR (displays QR with tok_1)

4. ios:  Idle → ScanQR → ConnectRelay → GenKeyPair → WaitAck
         sends pair_hello(token="tok_1", pubkey="client_pub")

5. ADVERSARY: MitM_pair_hello
              saves adv_saved_client_pub = "client_pub"
              replaces pair_hello pubkey with "adv_pub"

6. jevond: WaitingForClient → DeriveSecret
           received_client_pub = "adv_pub"  ← adversary's key!
           server_shared_key = DeriveKey("server_pub", "adv_pub")
           → DeriveSecret → SendAck
           sends pair_hello_ack(pubkey="server_pub")

7. ADVERSARY: MitM_pair_hello_ack
              saves adv_saved_server_pub = "server_pub"
              adversary_keys += {DeriveKey("adv_pub", "server_pub"),
                                 DeriveKey("adv_pub", "client_pub")}
              replaces pair_hello_ack pubkey with "adv_pub"

8. ios:  WaitAck → E2EReady
         received_server_pub = "adv_pub"  ← adversary's key!
         client_shared_key = DeriveKey("client_pub", "adv_pub")

   NOTE: server_shared_key ≠ client_shared_key
         but adversary has BOTH in adversary_keys

9. jevond: SendAck → WaitingForCode
           sends pair_confirm(key=server_shared_key, code="847291")
           sends waiting_for_code

10. ADVERSARY: MitM_reencrypt_confirm
               key ∈ adversary_keys → can decrypt!
               learns plaintext code "847291"
               re-encrypts with DeriveKey("adv_pub", "client_pub")

11. ios:  E2EReady → ShowCode → WaitPairComplete
          decrypts pair_confirm with client_shared_key ← matches!
          displays "847291" to user

12. cli:  ShowQR → PromptCode
          user reads "847291" from phone, types it in
          → SubmitCode
          sends code_submit(code="847291")

13. jevond: WaitingForCode → ValidateCode → StorePaired
            code matches!
            sends pair_complete(key=server_shared_key, secret="dev_secret_1")

14. ADVERSARY: MitM_reencrypt_secret
               key ∈ adversary_keys → decrypts device secret!
               adversary_knowledge += {plaintext_secret: "dev_secret_1"}

    *** CodeSecrecy VIOLATED ***
    *** DeviceSecretSecrecy VIOLATED ***
    *** SharedKeyAgreement VIOLATED ***
    *** AdversaryCannotDeriveSharedKey VIOLATED ***

15. Pairing completes. Adversary holds the device secret.
    All subsequent "authenticated" sessions are compromised.
```
