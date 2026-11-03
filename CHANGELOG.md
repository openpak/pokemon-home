# Changelog

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
