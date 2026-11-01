package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Wonder Box: send a boxed Pokemon, get another player's back after a delay. Pairing is a swap of
// two waiting entries from different accounts, oldest first; the Pokemon change owner only when
// each side collects, so nothing is lost if a player never comes back.

type WonderEntry struct {
	ID       int64     `json:"id"`
	SentAt   time.Time `json:"sent_at"`
	Pokemon  *Mon      `json:"pokemon"`
	Ready    bool      `json:"ready"`
	ReadyAt  time.Time `json:"ready_at"`
	Received *Mon      `json:"received,omitempty"`
}

func (a *App) WonderSend(ctx context.Context, acct string, monID int64) (*WonderEntry, error) {
	var id int64
	var sent time.Time
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		var n int
		if err := t.QueryRow(ctx, `SELECT count(*) FROM home_wonder WHERE account_id=$1`, acct).Scan(&n); err != nil {
			return err
		}
		if n >= a.WonderSlots {
			return ErrFull
		}
		tag, err := t.Exec(ctx, `UPDATE home_pokemon SET state='wonder', box=NULL, slot=NULL WHERE id=$1 AND account_id=$2 AND state='box'`, monID, acct)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return t.QueryRow(ctx, `INSERT INTO home_wonder(account_id,pokemon_id) VALUES ($1,$2) RETURNING id, sent_at`, acct, monID).Scan(&id, &sent)
	})
	if err != nil {
		return nil, err
	}
	return a.wonderView(ctx, acct, id)
}

func (a *App) wonderView(ctx context.Context, acct string, id int64) (*WonderEntry, error) {
	es, err := a.Wonder(ctx, acct)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, ErrNotFound
}

// Wonder lists the account's entries. A received Pokemon is shown (species, level, ...) but its
// file is only released by collecting.
func (a *App) Wonder(ctx context.Context, acct string) ([]*WonderEntry, error) {
	rows, err := a.DB.pool.Query(ctx, `SELECT id, sent_at, pokemon_id, recv_id FROM home_wonder WHERE account_id=$1 ORDER BY id`, acct)
	if err != nil {
		return nil, err
	}
	type r struct {
		id, pk int64
		sent   time.Time
		recv   *int64
	}
	var rs []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.id, &x.sent, &x.pk, &x.recv); err != nil {
			return nil, err
		}
		rs = append(rs, x)
	}
	rows.Close()
	out := []*WonderEntry{}
	for _, x := range rs {
		m, err := scanMon(a.DB.pool.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1`, x.pk))
		if err != nil {
			return nil, err
		}
		e := &WonderEntry{ID: x.id, SentAt: x.sent, Pokemon: m, ReadyAt: x.sent.Add(a.WonderDelay)}
		if x.recv != nil {
			e.Ready = true
			if e.Received, err = scanMon(a.DB.pool.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1`, *x.recv)); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// WonderPair swaps waiting entries older than the delay. Returns how many pairs were made.
func (a *App) WonderPair(ctx context.Context) (int, error) {
	pairs := 0
	for {
		made := false
		err := a.DB.tx(ctx, func(t pgx.Tx) error {
			rows, err := t.Query(ctx, `SELECT id, account_id, pokemon_id FROM home_wonder
			  WHERE recv_id IS NULL AND sent_at <= now() - make_interval(secs => $1)
			  ORDER BY sent_at, id FOR UPDATE SKIP LOCKED`, a.WonderDelay.Seconds())
			if err != nil {
				return err
			}
			type w struct {
				id, pk int64
				acct   string
			}
			var ws []w
			for rows.Next() {
				var x w
				if err := rows.Scan(&x.id, &x.acct, &x.pk); err != nil {
					return err
				}
				ws = append(ws, x)
			}
			rows.Close()
			for i := 0; i < len(ws); i++ {
				for j := i + 1; j < len(ws); j++ {
					if ws[i].acct == ws[j].acct {
						continue
					}
					if _, err := t.Exec(ctx, `UPDATE home_wonder SET recv_id=$2, matched_at=now() WHERE id=$1`, ws[i].id, ws[j].pk); err != nil {
						return err
					}
					if _, err := t.Exec(ctx, `UPDATE home_wonder SET recv_id=$2, matched_at=now() WHERE id=$1`, ws[j].id, ws[i].pk); err != nil {
						return err
					}
					made = true
					return nil
				}
			}
			return nil
		})
		if err != nil {
			return pairs, err
		}
		if !made {
			return pairs, nil
		}
		pairs++
	}
}

// WonderCollect completes a paired entry: the partner's Pokemon moves into the caller's boxes.
// The caller's own sent Pokemon was handed to the partner by *their* collect.
func (a *App) WonderCollect(ctx context.Context, acct string, id int64) (*Mon, error) {
	var out *Mon
	// two accounts are involved: find the partner first, then lock both in order
	var partner string
	var recv *int64
	if err := a.DB.pool.QueryRow(ctx, `SELECT w.recv_id, p.account_id FROM home_wonder w JOIN home_pokemon p ON p.id=w.recv_id
	  WHERE w.id=$1 AND w.account_id=$2`, id, acct).Scan(&recv, &partner); err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct, partner); err != nil {
			return err
		}
		var rid, mine int64
		if err := t.QueryRow(ctx, `SELECT recv_id, pokemon_id FROM home_wonder WHERE id=$1 AND account_id=$2 AND recv_id IS NOT NULL FOR UPDATE`, id, acct).Scan(&rid, &mine); err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		b, s, err := a.freeSlot(ctx, t, acct)
		if err != nil {
			return err
		}
		// Ownership of a Pokemon changes only here, so the partner's row is still theirs.
		m, err := scanMon(t.QueryRow(ctx, `UPDATE home_pokemon SET account_id=$2, state='box', box=$3, slot=$4, source_game='pokemon-home-wonder'
		  WHERE id=$1 RETURNING `+monCols, rid, acct, b, s))
		if err != nil {
			if isUnique(err) {
				return ErrConflict
			}
			return err
		}
		if err := a.registerDex(ctx, t, acct, m.Species, m.Form, m.Shiny); err != nil {
			return err
		}
		// my entry is done; my sent Pokemon stays 'wonder' until the partner collects it, then moves
		if _, err := t.Exec(ctx, `DELETE FROM home_wonder WHERE id=$1`, id); err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}
