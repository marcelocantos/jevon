# Taming State Space Explosion in Protocol Model Checking

*Marcelo Cantos, March 2026*

Our pairing ceremony protocol has three actors and an adversary
communicating over four WebSocket channels. When we first ran TLC on
the generated TLA+ spec, it didn't terminate. It couldn't — the
reachable state space was infinite.

## The explosion

The first run explored 212 states before hitting a type error (strings
compared to tuples — a separate modelling issue). After fixing the
type system, subsequent runs showed exponential growth:

| Run | States generated | Distinct states | Time | Outcome |
|-----|-----------------|-----------------|------|---------|
| 1   | 212             | 116             | <1s  | Type error |
| 2   | 45,846          | 10,729          | <1s  | Type error |
| 3   | 256,888,539     | 52,841,399      | 1.5min | Type error |
| 4   | 567,480,749     | 112,980,717     | 3.7min | Invariant violation |
| 5   | 1,845,833,940   | 369,640,475     | 12.6min | Disk full |

Run 5 generated 1.85 billion states before filling the disk, with 283
million states still queued. The queue was *growing* — each depth
level produced more unexplored states than it consumed. TLC would
never terminate.

## Why it exploded

Two features of the spec combined to create an infinite state space:

**1. Actor loops.** Each PlusCal process ran in a `while TRUE` loop,
modelling the possibility that pairing could be attempted multiple
times. After completing a pairing, actors would cycle back to their
initial states and start again. Each iteration created a fresh
combination of variable values, and the adversary's accumulated
knowledge from prior iterations multiplied the state space.

**2. Unbounded channels.** The adversary could inject messages into
any channel at any time — replaying captured messages, forging new
ones, or guessing codes. With no bound on channel length, the
adversary could queue arbitrarily many messages, and each distinct
queue contents constituted a distinct state.

Together, these created a doubly-infinite space: infinite iterations
× infinite channel growth.

## The fix: matching the model to reality

The insight was that neither feature matched the real protocol:

**Pairing is one-shot.** A device pairs once. The ceremony runs,
succeeds or fails, and terminates. There is no loop — a second
pairing attempt would be a fresh `jevon --init` invocation with a new
token, new keys, and no shared state with the first attempt. Modelling
it as a loop overstated what the protocol needed to guarantee.

**Channels have bounded occupancy.** The WebSocket channels carry a
fixed number of messages per protocol run. Each channel sees at most
2–4 legitimate messages total, and messages are consumed immediately
on receipt. Even with an adversary injecting extras, a bound of 3
messages per channel (one real + adversary slack) is generous — in
practice, the adversary's injected messages are either consumed
(and rejected by token/code validation) or dropped.

We added two fields to the protocol definition:

```yaml
name: PairingCeremony
one_shot: true      # actors run once, don't loop
channel_bound: 3    # max messages per channel
```

The `one_shot` flag causes the TLA+ exporter to generate processes
without `while TRUE` loops — each actor executes its transition
sequence once and halts. The `channel_bound` adds
`await Len(chan) < 3` guards to all adversary inject and replay
actions, preventing unbounded queue growth.

## The result

| Metric | Unbounded | Bounded |
|--------|-----------|---------|
| States generated | 1,845,833,940+ | 6,112,201 |
| Distinct states | 369,640,475+ | 420,488 |
| States remaining | 283,159,939+ | 0 |
| Time | 12.6min (disk full) | 3 seconds |
| Coverage | Partial | **Exhaustive** |
| Invariants checked | 7 (no violations found) | **7 (all hold, proven)** |

The bounded model explores the complete reachable state space — 420K
distinct states, zero remaining — in 3 seconds. All seven safety
invariants hold unconditionally:

- `NoTokenReuse` — revoked tokens are never re-accepted
- `MitMDetectedByCodeMismatch` — compromised sessions produce
  mismatching confirmation codes
- `MitMPrevented` — compromised sessions never reach pairing
  completion
- `AuthRequiresCompletedPairing` — sessions require prior pairing
- `NoNonceReuse` — auth nonces are accepted at most once
- `WrongCodeDoesNotPair` — incorrect codes don't complete pairing
- `DeviceSecretSecrecy` — the adversary never learns the device
  secret

The liveness property (`HonestPairingCompletes`) correctly fails:
the adversary can always drop messages to prevent progress. This is
expected — liveness under an active adversary requires fairness
assumptions that exclude permanent message suppression.

## The lesson

The state space didn't explode because the protocol was complex. It
exploded because the model was more general than the protocol. The
actors looped when the protocol doesn't; the channels grew when the
protocol bounds them. Once the model matched reality, the state space
collapsed from billions to thousands.

This is a general principle: model what you're building, not what you
could theoretically build. Unbounded models are useful for finding
bugs in general-purpose systems (databases, consensus protocols) where
the number of operations is genuinely open-ended. For bounded
protocols — authentication ceremonies, handshakes, provisioning flows
— the model should reflect that boundedness. The result is exhaustive
verification in seconds instead of partial exploration that never
terminates.
