package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Mon is a stored Pokemon. Data is only filled when a file leaves the service.
type Mon struct {
	ID         int64     `json:"id"`
	State      string    `json:"state"`
	Box        *int      `json:"box"`
	Slot       *int      `json:"slot"`
	Format     string    `json:"format"`
	Generation int       `json:"generation"`
	Species    int       `json:"species"`
	Form       int       `json:"form"`
	Level      int       `json:"level"`
	Gender     int       `json:"gender"`
	Shiny      bool      `json:"shiny"`
	Nickname   string    `json:"nickname"`
	OT         string    `json:"ot"`
	Nature     int       `json:"nature"`
	Ability    int       `json:"ability"`
	HeldItem   int       `json:"held_item"`
	Moves      []int32   `json:"moves"`
	SourceGame string    `json:"source_game"`
	CreatedAt  time.Time `json:"created_at"`
	Data       []byte    `json:"data,omitempty"`
}

const monCols = `id,state,box,slot,format,generation,species,form,level,gender,shiny,nickname,ot,nature,ability,held_item,moves,source_game,created_at`

func scanMon(r pgx.Row) (*Mon, error) {
	var m Mon
	err := r.Scan(&m.ID, &m.State, &m.Box, &m.Slot, &m.Format, &m.Generation, &m.Species, &m.Form, &m.Level, &m.Gender,
		&m.Shiny, &m.Nickname, &m.OT, &m.Nature, &m.Ability, &m.HeldItem, &m.Moves, &m.SourceGame, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if m.Moves == nil {
		m.Moves = []int32{}
	}
	return &m, err
}

func (a *App) geometryOK(box, slot int) bool {
	return box >= 0 && box < a.NumBoxes && slot >= 0 && slot < a.SlotsPer
}

// freeSlot finds the first empty slot for the account. Callers hold the account lock.
func (a *App) freeSlot(ctx context.Context, t pgx.Tx, acct string) (box, slot int, err error) {
	rows, err := t.Query(ctx, `SELECT box, slot FROM home_pokemon WHERE account_id=$1 AND state='box'`, acct)
	if err != nil {
		return
	}
	used := map[int]bool{}
	for rows.Next() {
		var b, s int
		if err = rows.Scan(&b, &s); err != nil {
			return
		}
		used[b*a.SlotsPer+s] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return
	}
	for i := 0; i < a.NumBoxes*a.SlotsPer; i++ {
		if !used[i] {
			return i / a.SlotsPer, i % a.SlotsPer, nil
		}
	}
	return 0, 0, ErrFull
}

// insertMon puts a new, vetted Pokemon into the account's boxes (a chosen slot or the first free one),
// and registers it in the Pokedex. The caller holds the account lock inside tx t.
func (a *App) insertMon(ctx context.Context, t pgx.Tx, acct, format string, data []byte, v *Verdict, game, checkedBy string, box, slot *int) (*Mon, error) {
	var b, s int
	if box != nil && slot != nil {
		if !a.geometryOK(*box, *slot) {
			return nil, ErrBadSlot
		}
		b, s = *box, *slot
		var n int
		if err := t.QueryRow(ctx, `SELECT count(*) FROM home_pokemon WHERE account_id=$1 AND state='box' AND box=$2 AND slot=$3`, acct, b, s).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, ErrConflict
		}
	} else {
		var err error
		if b, s, err = a.freeSlot(ctx, t, acct); err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256(data)
	moves := make([]int32, 0, len(v.Moves))
	for _, m := range v.Moves {
		moves = append(moves, int32(m))
	}
	m, err := scanMon(t.QueryRow(ctx, `INSERT INTO home_pokemon
	 (account_id,state,box,slot,format,data,sha,generation,species,form,level,gender,shiny,nickname,ot,nature,ability,held_item,moves,source_game,checked_by)
	 VALUES ($1,'box',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) RETURNING `+monCols,
		acct, b, s, format, data, sum[:], v.Generation, v.Species, v.Form, v.Level, v.Gender, v.Shiny, v.Nickname, v.OT,
		v.Nature, v.Ability, v.HeldItem, moves, game, checkedBy))
	if err != nil {
		if isUnique(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return m, a.registerDex(ctx, t, acct, m.Species, m.Form, m.Shiny)
}

func (a *App) registerDex(ctx context.Context, t pgx.Tx, acct string, species, form int, shiny bool) error {
	_, err := t.Exec(ctx, `INSERT INTO home_dex(account_id,species,form,shiny) VALUES ($1,$2,$3,$4)
	 ON CONFLICT (account_id,species,form) DO UPDATE SET count=home_dex.count+1, last_at=now(), shiny=home_dex.shiny OR EXCLUDED.shiny`,
		acct, species, form, shiny)
	return err
}

// Deposit runs the legality gate and stores the Pokemon. Illegal gives ErrIllegal (422), an
// unreachable checker gives ErrChecker (503); nothing is stored in either case.
func (a *App) Deposit(ctx context.Context, acct, format string, data []byte, game string, box, slot *int) (*Mon, *Verdict, error) {
	v, err := a.gate(ctx, data, format)
	if err != nil {
		return nil, v, err
	}
	var m *Mon
	err = a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		var e error
		m, e = a.insertMon(ctx, t, acct, format, data, v, game, "pokemon-legality", box, slot)
		return e
	})
	return m, v, err
}

func (a *App) GetMon(ctx context.Context, acct string, id int64, withData bool) (*Mon, error) {
	m, err := scanMon(a.DB.pool.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1 AND account_id=$2`, id, acct))
	if err != nil || !withData {
		return m, err
	}
	err = a.DB.pool.QueryRow(ctx, `SELECT data FROM home_pokemon WHERE id=$1`, id).Scan(&m.Data)
	return m, err
}

type BoxSummary struct {
	Box  int `json:"box"`
	Used int `json:"used"`
}

func (a *App) Boxes(ctx context.Context, acct string) ([]BoxSummary, int, error) {
	rows, err := a.DB.pool.Query(ctx, `SELECT box, count(*) FROM home_pokemon WHERE account_id=$1 AND state='box' GROUP BY box ORDER BY box`, acct)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []BoxSummary{}
	total := 0
	for rows.Next() {
		var s BoxSummary
		if err := rows.Scan(&s.Box, &s.Used); err != nil {
			return nil, 0, err
		}
		total += s.Used
		out = append(out, s)
	}
	return out, total, rows.Err()
}

func (a *App) BoxContents(ctx context.Context, acct string, box int) ([]*Mon, error) {
	rows, err := a.DB.pool.Query(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE account_id=$1 AND state='box' AND box=$2 ORDER BY slot`, acct, box)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Mon{}
	for rows.Next() {
		m, err := scanMon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Move relocates a boxed Pokemon; an occupied target swaps places.
func (a *App) Move(ctx context.Context, acct string, id int64, box, slot int) (*Mon, error) {
	if !a.geometryOK(box, slot) {
		return nil, ErrBadSlot
	}
	var out *Mon
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		var ob, os_ *int
		var other *int64
		if err := t.QueryRow(ctx, `SELECT box, slot FROM home_pokemon WHERE id=$1 AND account_id=$2 AND state='box'`, id, acct).Scan(&ob, &os_); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		_ = t.QueryRow(ctx, `SELECT id FROM home_pokemon WHERE account_id=$1 AND state='box' AND box=$2 AND slot=$3`, acct, box, slot).Scan(&other)
		if other != nil && *other != id {
			// park, move, then place the other where this one was
			if _, err := t.Exec(ctx, `UPDATE home_pokemon SET box=-1, slot=-1 WHERE id=$1`, *other); err != nil {
				return err
			}
		}
		if _, err := t.Exec(ctx, `UPDATE home_pokemon SET box=$2, slot=$3 WHERE id=$1`, id, box, slot); err != nil {
			return err
		}
		if other != nil && *other != id {
			if _, err := t.Exec(ctx, `UPDATE home_pokemon SET box=$2, slot=$3 WHERE id=$1`, *other, *ob, *os_); err != nil {
				return err
			}
		}
		var e error
		out, e = scanMon(t.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1`, id))
		return e
	})
	return out, err
}

// Withdraw hands the file to the client and parks the Pokemon in limbo until the client confirms
// receipt; a lost response then never destroys a Pokemon.
func (a *App) Withdraw(ctx context.Context, acct string, id int64) (*Mon, error) {
	var m *Mon
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		var data []byte
		tag, err := t.Exec(ctx, `UPDATE home_pokemon SET state='limbo', box=NULL, slot=NULL WHERE id=$1 AND account_id=$2 AND state='box'`, id, acct)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if m, err = scanMon(t.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1`, id)); err != nil {
			return err
		}
		if err = t.QueryRow(ctx, `SELECT data FROM home_pokemon WHERE id=$1`, id).Scan(&data); err != nil {
			return err
		}
		m.Data = data
		return nil
	})
	return m, err
}

// Confirm deletes a withdrawn Pokemon for good (the client has it).
func (a *App) Confirm(ctx context.Context, acct string, id int64) error {
	tag, err := a.DB.pool.Exec(ctx, `DELETE FROM home_pokemon WHERE id=$1 AND account_id=$2 AND state='limbo'`, id, acct)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Restore puts a limbo Pokemon back in the first free slot.
func (a *App) Restore(ctx context.Context, acct string, id int64) (*Mon, error) {
	var m *Mon
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		return a.restoreTx(ctx, t, acct, id, &m)
	})
	return m, err
}

// RestoreChecked is Restore for the API: an 'unverified' file passes the gate first.
func (a *App) RestoreChecked(ctx context.Context, acct string, id int64) (*Mon, error) {
	var by, format string
	var data []byte
	err := a.DB.pool.QueryRow(ctx, `SELECT checked_by, format, data FROM home_pokemon WHERE id=$1 AND account_id=$2`, id, acct).Scan(&by, &format, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if by == "unverified" {
		if _, err := a.gate(ctx, data, format); err != nil {
			return nil, err
		}
		if _, err := a.DB.pool.Exec(ctx, `UPDATE home_pokemon SET checked_by='pokemon-legality' WHERE id=$1`, id); err != nil {
			return nil, err
		}
	}
	return a.Restore(ctx, acct, id)
}

func (a *App) restoreTx(ctx context.Context, t pgx.Tx, acct string, id int64, out **Mon) error {
	var n int
	if err := t.QueryRow(ctx, `SELECT count(*) FROM home_pokemon WHERE id=$1 AND account_id=$2 AND state<>'box'`, id, acct).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	b, s, err := a.freeSlot(ctx, t, acct)
	if err != nil {
		return err
	}
	m, err := scanMon(t.QueryRow(ctx, `UPDATE home_pokemon SET state='box', box=$3, slot=$4 WHERE id=$1 AND account_id=$2 RETURNING `+monCols, id, acct, b, s))
	*out = m
	return err
}

func (a *App) Limbo(ctx context.Context, acct string) ([]*Mon, error) {
	rows, err := a.DB.pool.Query(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE account_id=$1 AND state='limbo' ORDER BY id`, acct)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Mon{}
	for rows.Next() {
		m, err := scanMon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Release deletes boxed Pokemon (mass release). The Pokedex keeps them registered, as in HOME.
func (a *App) Release(ctx context.Context, acct string, ids []int64) (int64, error) {
	tag, err := a.DB.pool.Exec(ctx, `DELETE FROM home_pokemon WHERE account_id=$1 AND state='box' AND id = ANY($2)`, acct, ids)
	return tag.RowsAffected(), err
}
