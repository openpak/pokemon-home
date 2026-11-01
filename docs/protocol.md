# The Switch HOME app's protocol

**Status: not implemented. Capture-gated.** No public source documents the Switch HOME app's hosts, paths or
payloads (searched 2026-11-01: PKHeX, switchbrew, community projects, general web). Nothing here was captured by
us either. See `research/pokemon/home-plan.md` (private research repo) for the sources and the licence of each.

## What is known

| Fact | Source |
| --- | --- |
| The HOME Pokemon container: 16-byte clear header (u16 version 1..4, u64 seed, u32 checksum, u16 body size), AES-128-CBC body, key and IV derived from the seed | PKHeX `HomeCrypto.cs` (GPL-3.0), facts only; implemented in `pkh.go`, unverified on a real file |
| Side-game data kinds: PB7, PK8, PA8, PB8, PK9, PC9, PA9 | PKHeX `HomeGameDataFormat` |
| The console's Nintendo Account id token is what proves identity; nx-baas mints it and proves it for title servers | nx-baas `docs/console-id-token.md` |

## What the service does with it

`Authorization: Bearer <id token>` is accepted on every route (the form a Switch title uses everywhere else);
nx-baas proves it, returns the core `account_id`, and that account is the HOME user. `HOME_APPLICATION_IDS`
restricts which `nintendo.ai` application id may use it; empty until a capture says what HOME's is.

## Capture mode

Any request that matches no route is written to `HOME_CAPTURE_DIR` (method, host, URI, headers with
`Authorization`/`Cookie` redacted to a length, body) and answered `501 {"error":"not_implemented","capture":"<id>"}`,
and logged as `unhandled METHOD host/path`. Once a HOME host is redirected here the first session documents itself.

## TODO(capture) list

Each item needs real traffic first (client RE playbook: measure before changing a server).

1. Hosts and TLS: which names, SNI/ALPN, pinning. Method: record DNS queries of a real console running HOME
   with the OpenPak resolver in log-only mode; do not redirect a name nobody saw asked for.
2. Session: how the id token is presented, whether HOME exchanges it, cookie/header names, NA client id.
3. Storage sync: list/diff of boxes, deposit and withdraw transactions, versioning and conflict handling.
4. Pokedex sync, Wonder Box, GTS, Room trade, Judge: request/response schema, error codes.
5. Game bridges (SwSh, BDSP, PLA, SV, LGPE <-> HOME): the transfer authorization round trip, per-game return rules.
6. Wire form of a Pokemon: PKH (encrypted with its seed) or per-game files; version number; checksum rule.
7. Plan/entitlement (Basic vs Premium) enforcement.

Capture tools: Harbor/Eden guest TLS trace (only when the owner is not using the machine and a HOME copy exists),
or a real Switch behind the OpenPak resolver and CA. When done, add handlers beside `/v1` in `api.go` that call
the same `App` methods, so the legality gate cannot be skipped by a new protocol.
