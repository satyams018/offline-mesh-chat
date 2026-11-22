# Design decisions

One entry per key choice: context, decision, alternatives, tradeoffs. Newest at the bottom.
Loosely follows ADRs (Architecture Decision Records).

---

## D1. Go for the node and the simulator (2026-11-22)

**Context.** One core must run on laptops, in a 1,000-node simulator, and later inside an
Android app, built on an 8 GB M1.

**Decision.** Go.

**Alternatives.** *Python*: known best, but 10–50× slower for the simulator and impractical
to embed in Android. *Rust*: fastest and safest, but the learning curve would blow the schedule.
*Node/TS*: fine for networking, weaker for a CPU-bound simulator and mobile embedding.

**Tradeoffs.** Goroutines fit "many peers, many connections"; the standard library has the
crypto and networking we need; one static binary per OS; `gomobile bind` gives Android a
`.aar` later. Cost: learning a new language.

## D2. Sans-IO core (2026-11-22)

**Decision.** The core is `Handle(now, event) []Action` and does no I/O.

**Alternative.** Routing code that calls sockets and `time.Now()` directly.

**Tradeoffs.** Deterministic, fast tests with no sleeps. The simulator runs the *exact*
production routing code, which makes its numbers credible. Cost: every side effect must be
modelled as an `Action`.

## D3. Protocol Buffers for the wire format (2026-11-22)

**Alternatives.** JSON (large; binary needs base64), CBOR/msgpack (no schema), hand-rolled
binary (error-prone).

**Tradeoffs.** Typed schema, Go and Kotlin codegen (Android in v2), clear schema-evolution
rules, which matter because nobody updates at a fest. Cost: a codegen step, and encoding is not
canonical, so `bundle_id` uses its own byte layout (D7).

## D4. TCP over Wi-Fi/hotspot, discovery via mDNS + manual peers (2026-11-22)

**Decision.** TCP links; mDNS `_meshchat._tcp`; `--peer host:port` always available.

**Alternatives.** UDP (needs our own reliability), raw BLE (low throughput, painful on laptops),
Wi-Fi Direct (complex, OS-specific).

**Tradeoffs.** TCP gives per-link ordering and retries, and DTN handles the end-to-end path.
A phone hotspot with mobile data off is a real offline LAN. Campus Wi-Fi often blocks multicast,
hence manual peers.

## D5. Asynchronous sealed-box E2E encryption (2026-11-22)

**Context.** The recipient is usually offline when a message is sent.

**Decision.** Ephemeral X25519 per message + HKDF-SHA256 + XChaCha20-Poly1305; inner message
Ed25519-signed, binding recipient and ephemeral key.

**Alternatives.** *Noise/TLS*: interactive handshakes, which suit links but not messages that
wait in the network. *Signal (X3DH + Double Ratchet)*: forward secrecy, but needs prekeys and
loss-tolerant session state, a large scope (v2+). *Static-key encryption*: one leaked key exposes all.

**Tradeoffs.** Simple, audited primitives, works offline. Cost: **no forward secrecy**.

## D6. Age-based lifetime (2026-11-22)

**Decision.** Bundles carry `age_ms`; each node adds its holding time; expire at
`age_ms ≥ lifetime_ms` (RFC 9171's Bundle Age idea).

**Tradeoffs.** No dependence on unsynchronized clocks. A lying relay can only affect bundles
it could drop anyway.

## D7. bundle_id = SHA-256 over a canonical byte layout (2026-11-22)

**Decision.** Hash immutable fields in fixed order with fixed-width integers and a
domain-separation prefix; exclude age, hops, copies.

**Tradeoffs.** Content-addressed IDs make dedup trivial and tampering visible. Hashing protobuf
bytes would differ between encoders.

## D8. Routing: epidemic + spray-and-wait in v1, PRoPHET later (2026-11-22)

**Decision.** A `router.Strategy` interface. v1 ships epidemic (upper bound on delivery, worst
overhead) and binary spray-and-wait (bounded copies). PRoPHET (meeting-history based) is v2.

**Why.** Two strategies already show the core DTN tradeoff (delivery vs overhead) in the plots.
PRoPHET needs router gossip; the interface reserves room for it so adding it is one package.

## D9. SQLite via pure-Go driver (2026-11-22)

**Alternatives.** In-memory only (loses bundles on restart, defeating store-and-forward),
bbolt (key-value only), BadgerDB (heavier).

**Tradeoffs.** SQL makes expiry/eviction queries easy and is inspectable with `sqlite3`.
`modernc.org/sqlite` needs no C toolchain (helps cross-compiling and gomobile later). The
simulator uses an in-memory implementation of the same store interface.

## D10. Discrete-event simulator, not N real processes (2026-11-22)

**Alternatives.** N `meshd` processes with `tc netem` (1,000 won't fit in 8 GB, not
reproducible); the ONE simulator in Java (doesn't run *our* code).

**Tradeoffs.** Fast, seeded, reproducible, runs the real core. Cost: simplified link model,
checked against a small real-laptop test.

## D11. Web UI in React + TS, embedded in meshd (2026-11-22)

**Alternatives.** Terminal UI only (weak demo), native desktop UI (more code).

**Tradeoffs.** Reuses JS skills, one UI that a future Android WebView can reuse. The local API
needs CSRF/DNS-rebinding protection (token + Origin/Host checks).

## D12. No libp2p (2026-11-22)

libp2p would hide exactly what this project demonstrates (discovery, transports, gossip), and
its gossip assumes mostly-connected networks, not DTN. Named as the production alternative.

## D13. Analysis in Python with uv (2026-11-22)

The simulator writes CSV; pandas + matplotlib make the plots. Standard tooling, no Go plotting.

## D14. Laptop-only v1, everything else as v2+ (2026-11-22)

**Context.** The original plan (Android, gomobile, Nearby, PRoPHET, Bloom filters) was ~9 weeks.

**Decision.** Ship a complete laptop-only v1 in ~5 weekly milestones, each tagged as a release.
v2+ items live as GitHub issues and are added as ordinary feature branches.

**Tradeoffs.** A finished, demoable, measured system beats a half-built bigger one on a resume.
The sans-IO core and protobuf schema keep Android a pure addition later, not a rewrite.
Cost: no cross-transport (Wi-Fi ⇄ Bluetooth) bridging in v1.
