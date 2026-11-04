# Deploy

Quadlet `deploy/openpak-pokemon-home.container`; image `ghcr.io/openpak/pokemon-home:latest`
(`AutoUpdate=registry`); port 20200 published on `127.0.0.1` only. Tags `v*.*.*` build the image.

## Environment (`~/openpak/deploy/pokemon-home.env`)

| Variable | Meaning |
| --- | --- |
| `HOME_INTERNAL_KEY` | Required, 16+ bytes. Trusted-proxy key for the website/app |
| `HOME_DATABASE_URL` | Postgres URL (own database `pokemonhome`, own role) |
| `HOME_LEGALITY_URL`, `HOME_LEGALITY_KEY` | `pokemon-legality` and its `PKHAX_INTERNAL_KEY` |
| `HOME_GL_URL`, `HOME_GL_KEY` | `pokemon-global-link` and its `PGL_INTERNAL_KEY` |
| `HOME_NX_URL`, `HOME_NX_KEY` | nx-baas game API (`:20070`) and the shared `NX_INTERNAL_KEY` |
| `HOME_APPLICATION_IDS` | Allowed `nintendo.ai` ids, empty = any proven token |
| `HOME_BOXES`, `HOME_BOX_SLOTS`, `HOME_WONDER_DELAY`, `HOME_WONDER_SLOTS`, `HOME_ROOM_MAX`, `HOME_ROOM_TTL` | Product parameters |
| `HOME_CAPTURE_DIR` | Capture mode directory |
| `HOME_TLS_CERT`, `HOME_TLS_KEY`, `HOME_TLS_ADDR` | Console-facing TLS listener, off unless set |

New quadlets need `~/openpak/deploy/pin-ips.sh --apply`; wrap prod changes in `flock ~/openpak-deploy.lock`.

## Console DNS and certificates (not applied)

How other Switch hosts are served: the console follows the Atmosphere `dns_mitm` hosts block written by the
nro from the signed network profile; Traefik routes the SNI on the `console` entrypoint; the leaf is minted
from the OpenPak CA, served with the CA, backdated to 2020; the host's family must be inside the signed
ceiling; nn-sssl-dns is restarted after a ceiling change. For HOME the host names are unknown, so:

1. Capture the names (docs/protocol.md).
2. `deploy/mint-leaf.sh <hosts...>` in `~/openpak/deploy` -> `keys/pokemon-home.{key,pem}`; mount them in the
   quadlet and set `HOME_TLS_CERT`/`HOME_TLS_KEY`, publish nothing (Traefik reaches 21040 on the network).
3. Put the hosts in `deploy/traefik-pokemon-home.yml` and merge it into `traefik/dynamic.yml`.
4. Add the family to `netprofile-platforms.yml` (patch, never overwrite) and the signed ceiling; restart nn-sssl-dns.
5. Verify off-box: `openssl s_client -connect <ip>:443 -servername <host> -alpn h2 -showcerts 2>&1` shows two
   certificates (leaf then CA).

A public `*.openpak.org` name for the JSON API is not needed (the website proxies); if wanted, point a tunnel
hostname at `http://localhost:20200`.

## Account erasure (card #120)

`DELETE /internal/accounts/{account_id}/data`, header `Authorization: Bearer <ACCOUNT_ERASE_KEY>` (one shared key; unset means 503), on the plain HTTP listener `http://openpak-pokemon-home:20200` only. The console TLS listener does not serve it. Calls pokemon-global-link's erase listener first (`HOME_GL_ERASE_URL`, default `http://openpak-pokemon-global-link:9099`); if that fails nothing local is touched and the call can be repeated.

Erased: boxes, limbo, Pokedex, GTS link and listing, Wonder Box entries, hosted rooms, room trades, the storage row. Kept: nothing (private storage; traded copies belong to the receiving player). Reply `{"service":"pokemon-home","rows":N}`; a second call returns 0.
