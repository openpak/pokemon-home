# Pokemon HOME

OpenPak's Pokemon HOME: account-bound box storage, a Pokedex, the GTS, a Wonder Box and Room trade, with
a legality gate on every Pokemon that enters.

| | |
| --- | --- |
| Network | Switch (the HOME app); the JSON API is for the website and app |
| Role | Box storage (200 boxes x 30), Pokedex, GTS on `pokemon-global-link`, Wonder Box, Room trade |
| User | The OpenPak core account. There is no HOME login |
| Depends on | Postgres, [`pokemon-legality`](https://github.com/openpak/pokemon-legality), [`pokemon-global-link`](https://github.com/openpak/pokemon-global-link), [`nx-baas`](https://github.com/openpak/nx-baas) (id token check) |
| Image | `ghcr.io/openpak/pokemon-home` |
| Licence | AGPL-3.0-only |

## Behaviour

The Switch HOME app's own wire protocol (hosts, session, sync, transfers) is capture-gated, see
[protocol](docs/protocol.md). Unmatched requests are recorded (capture mode) and answered 501.
Console DNS and certificates for HOME hosts depend on the hosts being known, see [deploy](docs/deploy.md).

## Run

```sh
cp example.env .env   # set HOME_INTERNAL_KEY, HOME_DATABASE_URL
set -a; . ./.env; set +a
GOWORK=off go run .
HOME_TEST_DSN='postgres://...' GOWORK=off go test ./...   # tests need Postgres and are skipped without it
```

## Docs

- [API for the website and app](docs/api.md)
- [Protocol and capture plan](docs/protocol.md)
- [Deploy](docs/deploy.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
