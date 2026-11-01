package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS home_accounts (
  account_id text PRIMARY KEY,
  gl_pid     bigint GENERATED ALWAYS AS IDENTITY (START WITH 1099511627776) UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS home_pokemon (
  id          bigserial PRIMARY KEY,
  account_id  text NOT NULL REFERENCES home_accounts(account_id),
  state       text NOT NULL DEFAULT 'box',   -- box | limbo | gts | wonder
  box         int,
  slot        int,
  format      text NOT NULL,
  data        bytea NOT NULL,
  sha         bytea NOT NULL,
  generation  int NOT NULL DEFAULT 0,
  species     int NOT NULL,
  form        int NOT NULL DEFAULT 0,
  level       int NOT NULL DEFAULT 0,
  gender      int NOT NULL DEFAULT 0,
  shiny       boolean NOT NULL DEFAULT false,
  nickname    text NOT NULL DEFAULT '',
  ot          text NOT NULL DEFAULT '',
  nature      int NOT NULL DEFAULT 0,
  ability     int NOT NULL DEFAULT 0,
  held_item   int NOT NULL DEFAULT 0,
  moves       int[] NOT NULL DEFAULT '{}',
  source_game text NOT NULL DEFAULT '',
  checked_by  text NOT NULL DEFAULT 'pokemon-legality',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS home_pokemon_slot ON home_pokemon(account_id, box, slot) WHERE state = 'box';
CREATE UNIQUE INDEX IF NOT EXISTS home_pokemon_sha ON home_pokemon(account_id, sha);
CREATE TABLE IF NOT EXISTS home_dex (
  account_id text NOT NULL REFERENCES home_accounts(account_id),
  species    int NOT NULL,
  form       int NOT NULL,
  shiny      boolean NOT NULL DEFAULT false,
  count      int NOT NULL DEFAULT 1,
  first_at   timestamptz NOT NULL DEFAULT now(),
  last_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, species, form)
);
CREATE TABLE IF NOT EXISTS home_gts (
  account_id text PRIMARY KEY REFERENCES home_accounts(account_id),
  listing_id bigint NOT NULL,
  generation int NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS home_wonder (
  id          bigserial PRIMARY KEY,
  account_id  text NOT NULL REFERENCES home_accounts(account_id),
  pokemon_id  bigint NOT NULL REFERENCES home_pokemon(id),
  sent_at     timestamptz NOT NULL DEFAULT now(),
  recv_id     bigint REFERENCES home_pokemon(id),   -- set when paired: the Pokemon this entry receives
  matched_at  timestamptz
);
CREATE TABLE IF NOT EXISTS home_rooms (
  code       text PRIMARY KEY,
  host       text NOT NULL REFERENCES home_accounts(account_id),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS home_room_members (
  code       text NOT NULL REFERENCES home_rooms(code) ON DELETE CASCADE,
  account_id text NOT NULL REFERENCES home_accounts(account_id),
  member_no  int NOT NULL,
  nickname   text NOT NULL DEFAULT '',
  offer_id   bigint,
  PRIMARY KEY (code, account_id),
  UNIQUE (code, member_no)
);
CREATE TABLE IF NOT EXISTS home_room_trades (
  id           bigserial PRIMARY KEY,
  code         text NOT NULL REFERENCES home_rooms(code) ON DELETE CASCADE,
  from_account text NOT NULL,
  to_account   text NOT NULL,
  from_pokemon bigint NOT NULL,
  status       text NOT NULL DEFAULT 'pending',   -- pending | done | declined | cancelled
  created_at   timestamptz NOT NULL DEFAULT now()
);
`

type DB struct{ pool *pgxpool.Pool }

func OpenDB(ctx context.Context, url string) (*DB, error) {
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	var last error
	for i := 0; i < 20; i++ { // Postgres may still be coming up after a reboot
		if last = p.Ping(ctx); last == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if last != nil {
		return nil, last
	}
	if _, err := p.Exec(ctx, schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &DB{pool: p}, nil
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrFull     = errors.New("storage full")
	ErrBadSlot  = errors.New("bad box or slot")
)

// tx runs fn in a transaction.
func (d *DB) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	t, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer t.Rollback(ctx)
	if err := fn(t); err != nil {
		return err
	}
	return t.Commit(ctx)
}

// lockAccounts takes per-account advisory locks in a fixed order, so two transactions touching the
// same pair of accounts cannot deadlock, and so slot allocation for one account is serial.
func lockAccounts(ctx context.Context, t pgx.Tx, ids ...string) error {
	if len(ids) == 2 && ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	for _, id := range ids {
		if _, err := t.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, id); err != nil {
			return err
		}
	}
	return nil
}

// ensureAccount creates the storage row lazily: the HOME user is the core account, nothing else.
func (d *DB) ensureAccount(ctx context.Context, id string) (pid int64, err error) {
	err = d.pool.QueryRow(ctx, `INSERT INTO home_accounts(account_id) VALUES ($1)
	  ON CONFLICT (account_id) DO UPDATE SET account_id = EXCLUDED.account_id RETURNING gl_pid`, id).Scan(&pid)
	return
}
