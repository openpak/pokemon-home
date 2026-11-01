package main

import (
	"context"
	"time"
)

type DexEntry struct {
	Species int       `json:"species"`
	Form    int       `json:"form"`
	Shiny   bool      `json:"shiny"`
	Count   int       `json:"count"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

func (a *App) Pokedex(ctx context.Context, acct string) ([]DexEntry, error) {
	rows, err := a.DB.pool.Query(ctx, `SELECT species,form,shiny,count,first_at,last_at FROM home_dex WHERE account_id=$1 ORDER BY species,form`, acct)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DexEntry{}
	for rows.Next() {
		var e DexEntry
		if err := rows.Scan(&e.Species, &e.Form, &e.Shiny, &e.Count, &e.FirstAt, &e.LastAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RebuildDex adds whatever the boxes hold that the Pokedex lacks. It never removes an entry: HOME
// keeps a species registered after it is traded or released.
func (a *App) RebuildDex(ctx context.Context, acct string) error {
	_, err := a.DB.pool.Exec(ctx, `INSERT INTO home_dex(account_id,species,form,shiny,count)
	  SELECT account_id,species,form,bool_or(shiny),count(*) FROM home_pokemon WHERE account_id=$1 AND state IN ('box','limbo')
	  GROUP BY account_id,species,form
	  ON CONFLICT (account_id,species,form) DO UPDATE SET shiny=home_dex.shiny OR EXCLUDED.shiny, count=GREATEST(home_dex.count,EXCLUDED.count)`, acct)
	return err
}
