package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

// GlobalLink is the client of common/pokemon-global-link. The GTS store lives there (so it is not
// duplicated and its own legality gate also applies); this service only moves Pokemon between a
// player's boxes and a listing.
type GlobalLink struct {
	URL, Key string
	Client   *http.Client
}

type GLListing struct {
	ID             int64     `json:"id"`
	Generation     int       `json:"generation"`
	Species        int       `json:"species"`
	Status         string    `json:"status"`
	Format         string    `json:"format"`
	Data           []byte    `json:"data"`
	ReturnedFormat string    `json:"returned_format"`
	Returned       []byte    `json:"returned"`
	Request        GLRequest `json:"request"`
}

type GLRequest struct {
	Species  int `json:"species"`
	Gender   int `json:"gender"`
	LevelMin int `json:"level_min"`
	LevelMax int `json:"level_max"`
}

type GLError struct {
	Status int
	Msg    string
}

func (e *GLError) Error() string { return fmt.Sprintf("global-link %d: %s", e.Status, e.Msg) }

func (g *GlobalLink) do(ctx context.Context, method, path string, in, out any) error {
	var rd io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.URL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", g.Key)
	cl := g.Client
	if cl == nil {
		cl = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return &GLError{503, "unreachable"}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return &GLError{resp.StatusCode, e.Error}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// Browse lists open listings (public, no files, no trainer ids).
func (a *App) GTSBrowse(ctx context.Context, q url.Values) (json.RawMessage, error) {
	var raw json.RawMessage
	err := a.GL.do(ctx, http.MethodGet, "/api/v1/gts/listings?"+q.Encode(), nil, &raw)
	return raw, err
}

// GTSList takes a boxed Pokemon out of the box and lists it. The Pokemon is parked as 'gts' while
// global-link answers; on any failure it goes back to its slot.
func (a *App) GTSList(ctx context.Context, acct string, pid, monID int64, req GLRequest) (*GLListing, error) {
	var data []byte
	var format string
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		var n int
		if err := t.QueryRow(ctx, `SELECT count(*) FROM home_gts WHERE account_id=$1`, acct).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrConflict
		}
		err := t.QueryRow(ctx, `UPDATE home_pokemon SET state='gts' WHERE id=$1 AND account_id=$2 AND state='box' RETURNING format, data`, monID, acct).Scan(&format, &data)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var l GLListing
	body := map[string]any{"pid": pid, "game": "pokemon-home", "request": req, "format": format, "data": data}
	if err := a.GL.do(ctx, http.MethodPost, "/internal/v1/gts/deposit", body, &l); err != nil {
		a.back(ctx, acct, monID)
		return nil, err
	}
	err = a.DB.tx(ctx, func(t pgx.Tx) error {
		if _, err := t.Exec(ctx, `INSERT INTO home_gts(account_id,listing_id,generation) VALUES ($1,$2,$3)`, acct, l.ID, l.Generation); err != nil {
			return err
		}
		_, err := t.Exec(ctx, `DELETE FROM home_pokemon WHERE id=$1`, monID)
		return err
	})
	l.Data = nil
	return &l, err
}

// back returns a parked Pokemon to the boxes.
func (a *App) back(ctx context.Context, acct string, id int64) {
	_ = a.DB.tx(context.WithoutCancel(ctx), func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		var m *Mon
		return a.restoreTx(ctx, t, acct, id, &m)
	})
}

// GTSTrade offers a boxed Pokemon to a listing. The listing's Pokemon lands in the caller's boxes;
// the offered one goes to the listing owner (who collects it).
func (a *App) GTSTrade(ctx context.Context, acct string, pid, listing, monID int64) (*Mon, error) {
	var data []byte
	var format string
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		err := t.QueryRow(ctx, `UPDATE home_pokemon SET state='gts' WHERE id=$1 AND account_id=$2 AND state='box' RETURNING format, data`, monID, acct).Scan(&format, &data)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var l GLListing
	if err := a.GL.do(ctx, http.MethodPost, fmt.Sprintf("/internal/v1/gts/%d/trade", listing), map[string]any{"pid": pid, "format": format, "data": data}, &l); err != nil {
		a.back(ctx, acct, monID)
		return nil, err
	}
	return a.receive(ctx, acct, monID, l.Format, l.Data, "pokemon-home-gts", &Verdict{Generation: l.Generation, Species: l.Species})
}

// receive stores a Pokemon that global-link just handed over, after the gate. A completed trade
// must never be lost, so if the gate refuses or is unreachable the file is kept in limbo as
// 'unverified' (it cannot be restored to a box until it passes the gate) and the error is returned.
func (a *App) receive(ctx context.Context, acct string, delPark int64, format string, data []byte, game string, fallback *Verdict) (*Mon, error) {
	v, gerr := a.gate(ctx, data, format)
	var m *Mon
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		if delPark != 0 {
			if _, err := t.Exec(ctx, `DELETE FROM home_pokemon WHERE id=$1 AND account_id=$2 AND state='gts'`, delPark, acct); err != nil {
				return err
			}
		}
		if gerr != nil {
			if v == nil {
				v = fallback
			}
			m2, err := a.insertMon(ctx, t, acct, format, data, v, game, "unverified", nil, nil)
			if err != nil {
				return err
			}
			_, err = t.Exec(ctx, `UPDATE home_pokemon SET state='limbo', box=NULL, slot=NULL WHERE id=$1`, m2.ID)
			return err
		}
		var e error
		m, e = a.insertMon(ctx, t, acct, format, data, v, game, "pokemon-legality", nil, nil)
		return e
	})
	if err == nil && gerr != nil {
		err = gerr
	}
	return m, err
}

// GTSMine reports the caller's listing.
func (a *App) GTSMine(ctx context.Context, acct string, pid int64) (*GLListing, error) {
	var gen int
	err := a.DB.pool.QueryRow(ctx, `SELECT generation FROM home_gts WHERE account_id=$1`, acct).Scan(&gen)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var l GLListing
	if err := a.GL.do(ctx, http.MethodGet, fmt.Sprintf("/internal/v1/gts/mine?pid=%d&generation=%d", pid, gen), nil, &l); err != nil {
		return nil, err
	}
	l.Data, l.Returned = nil, nil
	return &l, nil
}

// GTSFinish withdraws an open listing (the original comes back) or collects a traded one (the
// received Pokemon arrives).
func (a *App) GTSFinish(ctx context.Context, acct string, pid int64) (*Mon, string, error) {
	var gen int
	var id int64
	err := a.DB.pool.QueryRow(ctx, `SELECT generation, listing_id FROM home_gts WHERE account_id=$1`, acct).Scan(&gen, &id)
	if err == pgx.ErrNoRows {
		return nil, "", ErrNotFound
	} else if err != nil {
		return nil, "", err
	}
	var l GLListing
	if err := a.GL.do(ctx, http.MethodPost, fmt.Sprintf("/internal/v1/gts/%d/finish", id), map[string]any{"pid": pid}, &l); err != nil {
		return nil, "", err
	}
	format, data, kind := l.Format, l.Data, "withdrawn"
	if l.Status == "collected" {
		format, data, kind = l.ReturnedFormat, l.Returned, "collected"
	}
	if _, err := a.DB.pool.Exec(ctx, `DELETE FROM home_gts WHERE account_id=$1`, acct); err != nil {
		return nil, "", err
	}
	m, err := a.receive(ctx, acct, 0, format, data, "pokemon-home-gts", &Verdict{Generation: l.Generation, Species: l.Species})
	return m, kind, err
}
