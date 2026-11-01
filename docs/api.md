# JSON API

Base URL inside the network: `http://openpak-pokemon-home:20180`. All bodies are JSON; Pokemon files are
base64 in `data`. Ids are PKHeX ids (species, moves, abilities, items, natures). Errors are
`{"error": "..."}`.

## Authentication: the HOME user is the OpenPak account

Every `/v1/*` call needs one of:

| Caller | Headers | Resolves to |
| --- | --- | --- |
| A Switch (the HOME app, or a test client) | `Authorization: Bearer <Nintendo Account id token minted by nx-baas>` | The core `account_id` that nx-baas returns from `POST /internal/switch/identity`. 401 = token not proven (the console refreshes and retries); 403 = banned, no account linked, or the token is for another application; 503 = identity service down |
| The website or app (server side only) | `X-Internal-Key: <HOME_INTERNAL_KEY>` and `X-OpenPak-Account: <core account id>` | That account. Never forward these from a browser: the website authenticates its own session, then calls with the key |

Storage is created lazily on the first call. Nothing here ever creates or checks a password.

## Account

`GET /v1/me` -> `{"account_id","nickname","via":"id_token|proxy","boxes":200,"slots_per_box":30,"used":N,"capacity":6000}`

## Boxes

- `GET /v1/boxes` -> `{"boxes":200,"slots_per_box":30,"used":N,"non_empty":[{"box":0,"used":12}]}`
- `GET /v1/boxes/{n}` -> `{"box":n,"slots":30,"pokemon":[Mon...]}` (no files)
- `POST /v1/pokemon` `{"format":"pk9","data":"<b64>","box":0,"slot":3,"source_game":"sv"}` -> 201 Mon.
  `box`/`slot` optional (first free slot otherwise). Formats: whatever `pokemon-legality` opens (`pk3`..`pk9`,
  `pb7`, `pa8`, `pb8`) and `pkh` (HOME container). **422** with `{"error","verdict"}` when illegal or not a
  Pokemon, **503** when the legality service is unreachable (nothing stored either way), **409** when the slot
  is taken, storage is full, or the exact same file is already in the account's HOME, **400** bad slot.
- `GET /v1/pokemon/{id}[?data=1]` -> Mon (`data` adds the base64 file, no state change)
- `POST /v1/pokemon/{id}/move` `{"box","slot"}` -> Mon; an occupied target swaps
- `POST /v1/pokemon/{id}/withdraw` -> Mon with `data`, state `limbo`. The Pokemon is held until the client
  confirms; a lost response never destroys it
- `POST /v1/pokemon/{id}/confirm` deletes a limbo Pokemon for good; `POST /v1/pokemon/{id}/restore` puts it back
  in the first free slot (a file marked `unverified`, see GTS, passes the gate again first)
- `GET /v1/limbo` -> `{"pokemon":[Mon...]}`
- `POST /v1/release` `{"ids":[1,2,3]}` -> `{"released":3}` (boxed Pokemon only; the Pokedex keeps them)

`Mon`: `{id,state(box|limbo|gts|wonder),box,slot,format,generation,species,form,level,gender,shiny,nickname,ot,
nature,ability,held_item,moves[],source_game,created_at}`.

## Pokedex

- `GET /v1/pokedex` -> `{"species":N,"forms":N,"shiny":N,"entries":[{species,form,shiny,count,first_at,last_at}]}`
  Entries are registered at deposit and by trades, and are never removed (as in HOME).
- `POST /v1/pokedex/rebuild` re-derives missing entries from the boxes, then returns the Pokedex.

## GTS (backed by `pokemon-global-link`)

- `GET /v1/gts/listings` (same filters as global-link's public API: `generation, species, gender, level_min,
  level_max, limit, offset`) -> global-link's listing list, no files, no trainer ids
- `POST /v1/gts/listings` `{"pokemon_id":7,"request":{"species":150,"gender":-1,"level_min":0,"level_max":100}}`
  -> 201 listing. One listing per account; the Pokemon leaves the boxes. 409 if you already have one
- `POST /v1/gts/listings/{id}/trade` `{"pokemon_id":9}` -> `{"received":Mon}`. 422 when the offer does not match
  the request, 409 when the listing is gone or yours. Failure puts your Pokemon back. global-link only trades
  within one generation
- `GET /v1/gts/mine` -> your listing and its status (`open` | `traded`)
- `POST /v1/gts/mine/finish` -> `{"result":"withdrawn|collected","pokemon":Mon}`: an open listing comes back, a
  traded one delivers what you received. If the gate refuses or is down at that moment the file waits in
  `/v1/limbo` as `unverified` and the call returns the gate's error: a completed trade is never lost

## Wonder Box

- `GET /v1/wonder` -> `{"slots":3,"delay_seconds":300,"entries":[{id,sent_at,ready,ready_at,pokemon,received?}]}`
- `POST /v1/wonder` `{"pokemon_id":7}` -> 201 entry (409 when all slots are used)
- `POST /v1/wonder/{id}/collect` -> `{"received":Mon}` once paired (404 until then)

Entries pair oldest first with another account's entry that is past the delay; the two swap.

## Room trade

Rooms live 24 h, hold up to 20 members. Members see each other as a member number and a nickname, never an
account id, and see each other's offered Pokemon (no box position).

- `POST /v1/rooms` -> 201 Room (`code`); `POST /v1/rooms/{code}/join` -> Room (409 full, 404 unknown or expired)
- `GET /v1/rooms/{code}` -> `{"code","expires_at","members":[{member,nickname,me?,offer}],"trades":[{id,from_member,to_member,status,offered}]}`
- `POST /v1/rooms/{code}/offer` `{"pokemon_id":7}` (0 clears) -> Room
- `POST /v1/rooms/{code}/trades` `{"to_member":2}` -> 201 `{"trade":id}` proposes your current offer
- `POST /v1/rooms/{code}/trades/{id}/accept` -> `{"received":Mon}`: swaps the proposer's offer for the
  acceptor's current offer, atomically
- `POST /v1/rooms/{code}/trades/{id}/decline` declines (recipient) or cancels (proposer)

## Not here

Judge (needs IVs in the legality verdict), Battle Data and points, Pokemon Bank import, per-game transfer
rules, plan tiers: see `docs/notes.md`.
