# Changelog

## v0.2.0 - 2026-11-04

- Account erasure (card #120): `DELETE /internal/accounts/{account_id}/data` with `Authorization: Bearer $ACCOUNT_ERASE_KEY` on the HTTP listener (`:20200`, container network; never on the console TLS listener). Unset key = 503, wrong or missing key = 401, non-UUID = 400. Answers `{"service":"pokemon-home","rows":N}` and logs one `account erased` line.
- Erased: boxes, limbo and parked Pokemon, Pokedex, the GTS link and (through global-link, see `HOME_GL_ERASE_URL`) the open or traded GTS listing, Wonder Box entries, rooms the account hosts (members and trades go with them), room trades the account is on either side of, and the storage row with its global-link trainer id. Other players' Wonder entries that were paired with a deleted Pokemon go back to waiting. Idempotent.
- Kept: nothing. Boxes are private storage, not published content: a Pokemon only leaves the account through a completed trade, and the copy the other player received is theirs.
- New env `HOME_GL_ERASE_URL` (default `http://openpak-pokemon-global-link:9099`).

## v0.1.4 - 2026-11-04

- Docs only (#111): README states that only the `/v1` API exists and the Switch HOME app's own protocol is unimplemented (501) until a console capture exists; tracked as its own capture-gated card.

## v0.1.3 - 2026-11-03

- rewards-emit v0.3.0: set `REWARDS_EMIT_DATABASE_URL` for a durable rewards outbox in Postgres. Player state was already in Postgres; the capture dir is research capture only.

## v0.1.2 - 2026-11-01

- Completed trades (GTS taker, Wonder Box collect, both sides of a room trade) are reported to the rewards service (`REWARDS_EVENTS_URL` / `REWARDS_EVENTS_KEY`; unset = off).

## v0.1.1 - 2026-11-01

- Port block 20200 (HTTP) and 21040 (console TLS): 20180 was already taken by rewards.

## v0.1.0 - 2026-11-01

- Account-bound box storage (200 x 30): deposit, move, withdraw (limbo until confirmed), restore, release;
  legality gate on every deposit, fail closed.
- Pokedex derived from deposits and trades, rebuildable from the boxes.
- GTS through `pokemon-global-link`'s internal API.
- Wonder Box (delayed random swap) and Room trade (codes, offers, atomic swaps).
- Auth: Nintendo Account id token proved by nx-baas (the HOME user is the core account), or the website's
  trusted proxy.
- HOME container (PKH) helper; capture mode for unmatched requests.
- Switch app protocol: not implemented, capture-gated.
