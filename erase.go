package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Erase removes everything the account has in HOME (card #120): boxes, limbo, Pokedex, GTS link and the
// open GTS listing on global-link, Wonder Box entries, hosted rooms, room trades. Idempotent: a second
// run finds nothing and returns 0. Boxes are the player's private storage, so nothing is kept.
// Global-link goes first: if it fails nothing local is touched and the finaliser retries.
func (a *App) Erase(ctx context.Context, acct string) (int, error) {
	if !uuidRE.MatchString(acct) {
		return 0, ErrNotFound
	}
	rows := 0
	var pid int64
	err := a.DB.pool.QueryRow(ctx, `SELECT gl_pid FROM home_accounts WHERE account_id=$1`, acct).Scan(&pid)
	if err == nil {
		n, err := a.GL.ErasePID(ctx, acct, pid)
		if err != nil {
			return 0, err
		}
		rows += n
	} else if err != pgx.ErrNoRows {
		return 0, err
	}
	err = a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct); err != nil {
			return err
		}
		// Wonder entries of other players that were paired with this account's Pokemon go back to waiting.
		if _, err := t.Exec(ctx, `UPDATE home_wonder SET recv_id=NULL, matched_at=NULL
		  WHERE account_id<>$1 AND recv_id IN (SELECT id FROM home_pokemon WHERE account_id=$1)`, acct); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM home_room_trades WHERE from_account=$1 OR to_account=$1`,
			`DELETE FROM home_rooms WHERE host=$1`, // members and trades of that room cascade
			`DELETE FROM home_room_members WHERE account_id=$1`,
			`DELETE FROM home_wonder WHERE account_id=$1`,
			`DELETE FROM home_gts WHERE account_id=$1`,
			`DELETE FROM home_dex WHERE account_id=$1`,
			`DELETE FROM home_pokemon WHERE account_id=$1`,
			`DELETE FROM home_accounts WHERE account_id=$1`,
		} {
			tag, err := t.Exec(ctx, q, acct)
			if err != nil {
				return err
			}
			rows += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	log.Printf("account erased account=%s rows=%d service=pokemon-home", acct, rows)
	return rows, nil
}

// ErasePID asks global-link to delete the GTS listings and battle records of one trainer id.
func (g *GlobalLink) ErasePID(ctx context.Context, acct string, pid int64) (int, error) {
	var out struct {
		Rows int `json:"rows"`
	}
	err := g.doAt(ctx, g.EraseURL, http.MethodDelete, "/internal/accounts/"+acct+"/data?pid="+strconv.FormatInt(pid, 10), nil, &out)
	return out.Rows, err
}

// WithErase serves DELETE /internal/accounts/{account_id}/data (Bearer ACCOUNT_ERASE_KEY) in front of h.
// It is wired on the plain HTTP listener only, never on the console TLS one. Unset key = 503.
func (a *App) WithErase(h http.Handler) http.Handler {
	m := http.NewServeMux()
	m.Handle("/", h)
	m.HandleFunc("DELETE /internal/accounts/{account_id}/data", func(w http.ResponseWriter, r *http.Request) {
		key := os.Getenv("ACCOUNT_ERASE_KEY")
		if key == "" {
			http.Error(w, `{"error":"erase disabled"}`, http.StatusServiceUnavailable)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		id := r.PathValue("account_id")
		if !uuidRE.MatchString(id) {
			http.Error(w, `{"error":"bad account id"}`, http.StatusBadRequest)
			return
		}
		n, err := a.Erase(r.Context(), strings.ToLower(id))
		if err != nil {
			http.Error(w, `{"error":"erase failed"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"service": "pokemon-home", "rows": n})
	})
	return m
}
