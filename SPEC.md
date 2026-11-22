# Offline Mesh Chat — Specification

Status: v1 draft (2026-11-22). Design rationale and rejected alternatives live in
[`docs/decisions.md`](docs/decisions.md). This file says *what* we build. Anything marked
**v2+** is planned but deliberately not part of v1 (see D15).

## 1. Problem

At college fests, treks and disaster zones the internet is overloaded or absent, but devices
are physically close to each other. Offline Mesh Chat lets laptops exchange end-to-end
encrypted messages with **no internet and no server**. Messages hop device to device and are
*stored and carried* until a path to the recipient appears (delay-tolerant networking, DTN).

## 2. Goals (v1)

1. **Real multi-laptop mesh** over TCP on a shared Wi-Fi or a phone hotspot with mobile data off.
2. **Multi-hop store-and-forward** with deduplication, a hop limit (default 16) and a lifetime
   limit (default 24 h). A→B→C works even if A and C are never connected at the same time.
3. **End-to-end encryption and signatures**: relays forward ciphertext they can't read or forge.
4. **Pluggable routing** behind one interface: **epidemic** and **binary spray-and-wait** in v1.
5. **Discrete-event simulator** with 100–1,000 virtual nodes, movement models and packet loss,
   running the *same* routing code as real nodes. It reports delivery ratio, latency
   (median/p95), overhead, hop count and buffer use.
6. **Reproducible experiments**: one command each, fixed seeds, plots committed to the repo.
7. A minimal **web UI** (React + TS) talking to the local node over a WebSocket API.

## 3. Non-goals

**Never in scope:** an internet bridge, accounts or servers, anonymity against a global
observer, guaranteed delivery, a professional security audit.

**v2+ (planned, not in v1):** PRoPHET routing · Bloom-filter summaries · Android app
(gomobile + Nearby Connections) and cross-transport bridging · delivery receipts · Noise-encrypted
links · Double Ratchet forward secrecy · CRDT group chat · BLE · iOS · files/voice.

v1 messages are text only, at most 16 KiB.

## 4. Architecture

```
┌──────── web UI (React + TS, served by meshd on 127.0.0.1) ────────┐
└───────────────────────────────┬───────────────────────────────────┘
┌───────────────────────────────┴───────────────────────────────────┐
│ runtime (I/O side): goroutines, sockets, timers, SQLite, WS API   │
│   ┌──────────────────────────────────────────────────────────┐    │
│   │ core (sans-IO, deterministic)                            │    │
│   │   bundle · store/dedup · router (epidemic|spray) · crypto│    │
│   │   · link (contact protocol)                              │    │
│   └──────────────────────────────────────────────────────────┘    │
│ transport: memnet (tests) · tcp       discovery: mDNS · --peer    │
└───────────────────────────────────────────────────────────────────┘
        meshsim drives the same core with a virtual clock
```

### 4.1 Sans-IO core

The core never touches sockets, clocks or goroutines:

```go
// Handle consumes one input event and returns the actions the runtime must perform.
func (n *Node) Handle(now time.Time, ev Event) []Action
// Events:  PeerUp, PeerDown, FrameReceived, Tick, AppSend
// Actions: SendFrame, DisconnectPeer, DeliverToApp, PersistBundle, DropBundle
```

`meshd` and `meshsim` are thin drivers around `Handle`. Tests feed events and assert on
actions, so they are deterministic.

### 4.2 Repository layout

| Path | What | Milestone |
|---|---|---|
| `cmd/meshd/` | laptop node binary | M1 |
| `proto/mesh/v1/` | protobuf wire schema | M1 |
| `core/bundle`, `core/link` | bundle type, canonical ID, framing, contact protocol | M1–M2 |
| `core/store`, `core/router` | store + dedup, `Strategy` interface, epidemic, spray-and-wait | M2, M4 |
| `core/crypto` | identity keys, seal/open, contact cards | M3 |
| `transport/memnet`, `transport/tcp` | in-memory (loss/delay) and TCP links | M1–M2 |
| `discovery/` | mDNS `_meshchat._tcp` + manual `--peer` | M2 |
| `runtime/` | goroutine driver, SQLite persistence, WebSocket API | M2, M5 |
| `cmd/meshsim/` | discrete-event simulator | M4 |
| `analysis/` | Python (uv) scripts: CSV → plots | M4 |
| `web/` | React + TS + Vite UI, embedded with `go:embed` | M5 |

### 4.3 Transport and framing

TCP over any shared IP network. Each frame is a 4-byte big-endian length followed by a
protobuf `Frame`, maximum 64 KiB. Oversized or malformed frames drop the link.

Discovery: mDNS `_meshchat._tcp`, plus `--peer host:port` always (campus Wi-Fi often blocks
multicast). `--topology <file>` restricts which peers a node accepts, so multi-hop can be
demonstrated with every laptop on the same network. (UDP broadcast fallback: v2+.)

## 5. Identity

- On first start each node generates an **Ed25519** identity key and an **X25519** encryption
  key, stored in the data directory with mode 0600.
- **Node ID** = the 32-byte Ed25519 public key, shown as a short fingerprint (first 10 bytes,
  base32, grouped).
- A **contact card** `{node_id, x25519_pub, display_name}` is signed by the identity key and
  shared as text (QR in v2+). Trust on first use; comparing fingerprints in person marks it *verified*.

## 6. Wire format

Defined in `proto/mesh/v1/*.proto`:

```
Frame = oneof { Hello, SummaryVector, BundleRequest, Bundle }

Hello          protocol_version, node_id, x25519_pub, display_name,
               challenge_nonce, signature(peer's nonce)
SummaryVector  list of bundle_id
BundleRequest  list of bundle_id wanted

Bundle
  immutable (covered by bundle_id):
    version u32 · destination 32 B · lifetime_ms u64 · eph_pub 32 B · nonce 24 B · ciphertext
  mutable routing block (NOT covered by bundle_id):
    age_ms u64 · hop_count u32 · copies u32

InnerMessage (plaintext, visible only to the recipient)
  sender_id, sent_at_ms, message_id, body (UTF-8, ≤ 16 KiB),
  signature = Ed25519(sender, "mesh-msg-v1" ‖ destination ‖ eph_pub ‖ body fields)
```

**bundle_id** = `SHA-256("mesh-bundle-v1" ‖ version ‖ destination ‖ lifetime_ms ‖ eph_pub ‖
nonce ‖ ciphertext)` with fixed-width big-endian integers. It is computed over this hand-defined
layout, not over protobuf bytes, because protobuf encoding is not canonical.

**Lifetime**: each node adds the time it held a bundle to `age_ms` before forwarding. A bundle
expires when `age_ms ≥ lifetime_ms`. No synchronized clocks needed (same idea as RFC 9171).

> M1–M2 send plaintext bodies in the `ciphertext` field (no `eph_pub`/`nonce` yet);
> M3 switches to sealed messages and bumps `version`.

## 7. Routing

### 7.1 Contact protocol

1. Exchange `Hello`; each side signs the other's nonce (proves it owns the node ID). *(M3; M1–M2 skip the signature.)*
2. Exchange `SummaryVector`s of held bundle IDs.
3. For each bundle the peer lacks, the router decides whether to send it and with how many
   copies. Bundles go out newest first, then fewest hops.
4. On receive: check size and format, not expired, `hop_count < max_hops`, not already seen.
   Then deliver (if `destination == self`) or store.

**Dedup**: a bounded "seen" set of bundle IDs kept until each bundle's lifetime ends.
It also blocks replays at the recipient.

### 7.2 Strategies (`router.Strategy`)

| Strategy | Rule |
|---|---|
| **Epidemic** | Give the peer every bundle it lacks. Highest delivery, highest overhead. |
| **Binary spray-and-wait (L=8)** | Source starts with L copies. A node with n > 1 copies gives ⌊n/2⌋ to a peer that lacks the bundle. With 1 copy left, forward only to the destination. |
| PRoPHET | **v2+.** The interface must allow router gossip (a `RouterState` frame) without changes to the core. |

### 7.3 Buffers

At most 5,000 bundles and 50 MB per node. Eviction order: expired, then most hops, then
oldest received. A node's own outgoing messages are never evicted for relayed ones.

## 8. Threat model

Primitives (Go `crypto/*`, `golang.org/x/crypto`): Ed25519, X25519, HKDF-SHA256,
XChaCha20-Poly1305, SHA-256. **No custom primitives.**

**Sealing**: fresh ephemeral X25519 key per message →
`k = HKDF(X25519(eph_priv, recipient_pub), info = "mesh-seal-v1" ‖ eph_pub ‖ recipient_pub)` →
XChaCha20-Poly1305 over the signed `InnerMessage`, with the immutable header as associated data.
Works while the recipient is offline. **No forward secrecy in v1.**

| Adversary | Can | Cannot / mitigation |
|---|---|---|
| Eavesdropper | See frames, destination ID, sizes, timing | Read content or learn the sender (sender ID is inside the ciphertext) |
| Malicious relay | Drop, delay, replay; lie about `age_ms`/`hop_count` | Read or modify content (AEAD + signature); replays hit the seen-set; lying only hurts delivery |
| Spammer | Inject many bundles | Per-peer rate limit, size limits, buffer caps (crowding-out is a known limit) |
| Impersonator | Copy a display name | Forge the node ID (Hello challenge); TOFU + fingerprint check |
| Malicious website on the laptop | Call the local API | API on 127.0.0.1 only, random per-start token, `Origin`/`Host` checks |
| Stolen device | Read keys and history | Out of scope for v1 |

Known metadata leaks: recipient ID in cleartext (needed for routing), sizes and timing visible.

## 9. Simulator (`meshsim`)

- Discrete-event loop: priority queue of timestamped events, virtual clock, no sockets or sleeps.
- Each virtual node is a real `core.Node`; contacts come from a movement model:
  **random waypoint** (W×H area, radio range r) and a **fest model** (nodes move between
  hotspots such as stage, food court, gates).
- Link model: bandwidth per contact, per-frame loss probability, latency.
- Traffic: Poisson message generation between random pairs. Seeded RNG.
- Output: `runs/<experiment>/<seed>.csv` plus the config used.

| Metric | Definition |
|---|---|
| Delivery ratio | unique bundles delivered ÷ bundles created |
| Latency | delivery − creation time (median, p95) |
| Overhead ratio | (transmissions − delivered) ÷ delivered |
| Hop count | mean hops of delivered bundles |
| Buffer occupancy | mean and peak bundles per node |

Experiments sweep node count, buffer size, loss and TTL per strategy, ≥ 5 seeds per point.

## 10. Local UI API

`meshd` serves the UI and a JSON-over-WebSocket API on `127.0.0.1:7878`:
`contacts.list/add`, `messages.list/send`, `peers.list`, events `message.received`,
`peer.up/down`, `bundle.status`.

## 11. Testing

| Level | What |
|---|---|
| Unit | Table-driven, injected clock, per core package |
| Property | bundle_id stability, dedup idempotence, seal/open round trip, spray copies never increase |
| Fuzz | Go fuzzing of the frame and bundle decoders |
| Integration | N nodes over `memnet` with loss; real TCP on localhost |
| Simulator | Same seed ⇒ same CSV; epidemic delivery ≥ spray delivery |
| Manual | 3 laptops on a hotspot, A→B→C with `--topology` |

CI: golangci-lint, `go vet`, `go test -race`, build; later a short fuzz run, `buf lint`, web
build, analysis lint.

## 12. Milestones

See [README § Roadmap](README.md#roadmap).
