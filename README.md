# Offline Mesh Chat

[![CI](https://github.com/satyams018/offline-mesh-chat/actions/workflows/ci.yml/badge.svg)](https://github.com/satyams018/offline-mesh-chat/actions/workflows/ci.yml)

End-to-end encrypted chat for places with **no internet**: college fests, treks, disaster
zones. Laptops form a peer-to-peer mesh with no router and no server. Messages hop from device
to device and are **stored and carried** until a path to the recipient appears
(delay-tolerant networking, DTN).

> **Status:** milestone 0 (project setup). Nothing runs yet. See the [roadmap](#roadmap).

<!-- Screenshots / demo GIF go here from milestone 5. -->

## Features (v1 target)

- Multi-hop store-and-forward over TCP (Wi-Fi or a phone hotspot with mobile data off)
- Deduplication, hop limit (16) and age-based lifetime (24 h), no clock sync needed
- End-to-end encryption: X25519 + HKDF + XChaCha20-Poly1305, Ed25519 signatures
- Pluggable routing: epidemic and binary spray-and-wait
- Discrete-event simulator (100–1,000 nodes) running the **same** routing code, with plots
- Web UI over a local WebSocket API

## Architecture

A **sans-IO core** (`Handle(now, event) []Action`) holds all protocol logic and does no I/O.
The laptop node (`meshd`) and the simulator (`meshsim`) are thin drivers around it, so tests
are deterministic and simulator numbers describe the real code. Full details: [SPEC.md](SPEC.md).
Why each choice was made: [docs/decisions.md](docs/decisions.md).

## Tech stack

Go · Protocol Buffers · SQLite (`modernc.org/sqlite`) · React + TypeScript + Vite ·
Python + uv (pandas, matplotlib) · GitHub Actions · golangci-lint

## Setup

Requirements: Go 1.27+, golangci-lint v2. Later milestones add `buf`, Node and `uv`.

```bash
make test     # unit tests
make lint     # golangci-lint
make build    # builds bin/meshd
./bin/meshd -version
```

## Roadmap

One milestone per week, each tagged as a release.

| # | Week of | Milestone | Done when | Tag |
|---|---|---|---|---|
| 0 | 2026-11-22 | Setup: spec, decisions, CI | CI green | — |
| 1 | 2026-11-23 | Direct chat over TCP | Two laptops chat over a hotspot | v0.1.0 |
| 2 | 2026-11-30 | Store-and-forward | A→B→C delivers while A and C never connect; survives restart | v0.2.0 |
| 3 | 2026-12-07 | E2E encryption | Relays can't read; tamper and replay tests pass | v0.3.0 |
| 4 | 2026-12-14 | Simulator | 1,000 nodes, epidemic vs spray-and-wait plots in README | v0.4.0 |
| 5 | 2026-12-21 | Web UI + polish | Demo GIF, 3-laptop field test | **v1.0.0** |

**v2+ backlog** (tracked as GitHub issues): PRoPHET routing · Bloom-filter summaries ·
Android app (gomobile + Nearby Connections) bridging Wi-Fi ⇄ Bluetooth · delivery receipts ·
Noise-encrypted links · Double Ratchet forward secrecy · group chat (CRDT) · QR contact cards.

## License

MIT
