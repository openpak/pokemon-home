package main

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Room trade: a room (a short code, up to RoomMax members, RoomTTL lifetime). Each member shows one
// offered Pokemon from their boxes. A member proposes a trade to another member; the other accepts
// with whatever they are offering at that moment, and the two Pokemon swap owners atomically. No
// account id is ever shown to other members: they see a member number and a nickname.

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 8)
	for i := range b {
		out[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(out)
}

type RoomMember struct {
	No       int    `json:"member"`
	Nickname string `json:"nickname"`
	Me       bool   `json:"me,omitempty"`
	Offer    *Mon   `json:"offer"`
}

type RoomTrade struct {
	ID         int64  `json:"id"`
	FromMember int    `json:"from_member"`
	ToMember   int    `json:"to_member"`
	Status     string `json:"status"`
	Offered    *Mon   `json:"offered"`
}

type Room struct {
	Code      string       `json:"code"`
	ExpiresAt time.Time    `json:"expires_at"`
	Members   []RoomMember `json:"members"`
	Trades    []RoomTrade  `json:"trades"`
}

func (a *App) RoomCreate(ctx context.Context, acct, nick string) (*Room, error) {
	code := newCode()
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if _, err := t.Exec(ctx, `INSERT INTO home_rooms(code,host,expires_at) VALUES ($1,$2,now()+make_interval(secs=>$3))`, code, acct, a.RoomTTL.Seconds()); err != nil {
			return err
		}
		_, err := t.Exec(ctx, `INSERT INTO home_room_members(code,account_id,member_no,nickname) VALUES ($1,$2,1,$3)`, code, acct, nick)
		return err
	})
	if err != nil {
		return nil, err
	}
	return a.RoomGet(ctx, acct, code)
}

func (a *App) RoomJoin(ctx context.Context, acct, nick, code string) (*Room, error) {
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if _, err := t.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, code); err != nil {
			return err
		}
		var n int
		if err := t.QueryRow(ctx, `SELECT count(*) FROM home_rooms WHERE code=$1 AND expires_at>now()`, code).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if err := t.QueryRow(ctx, `SELECT count(*) FROM home_room_members WHERE code=$1 AND account_id=$2`, code, acct).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		var cnt, next int
		if err := t.QueryRow(ctx, `SELECT count(*), coalesce(max(member_no),0)+1 FROM home_room_members WHERE code=$1`, code).Scan(&cnt, &next); err != nil {
			return err
		}
		if cnt >= a.RoomMax {
			return ErrFull
		}
		_, err := t.Exec(ctx, `INSERT INTO home_room_members(code,account_id,member_no,nickname) VALUES ($1,$2,$3,$4)`, code, acct, next, nick)
		return err
	})
	if err != nil {
		return nil, err
	}
	return a.RoomGet(ctx, acct, code)
}

func (a *App) memberNo(ctx context.Context, acct, code string) (int, error) {
	var no int
	err := a.DB.pool.QueryRow(ctx, `SELECT m.member_no FROM home_room_members m JOIN home_rooms r USING (code)
	  WHERE m.code=$1 AND m.account_id=$2 AND r.expires_at>now()`, code, acct).Scan(&no)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return no, err
}

// RoomGet shows the room to a member.
func (a *App) RoomGet(ctx context.Context, acct, code string) (*Room, error) {
	me, err := a.memberNo(ctx, acct, code)
	if err != nil {
		return nil, err
	}
	r := &Room{Code: code, Members: []RoomMember{}, Trades: []RoomTrade{}}
	if err := a.DB.pool.QueryRow(ctx, `SELECT expires_at FROM home_rooms WHERE code=$1`, code).Scan(&r.ExpiresAt); err != nil {
		return nil, err
	}
	rows, err := a.DB.pool.Query(ctx, `SELECT member_no, nickname, offer_id FROM home_room_members WHERE code=$1 ORDER BY member_no`, code)
	if err != nil {
		return nil, err
	}
	type mm struct {
		no    int
		nick  string
		offer *int64
	}
	var ms []mm
	for rows.Next() {
		var x mm
		if err := rows.Scan(&x.no, &x.nick, &x.offer); err != nil {
			return nil, err
		}
		ms = append(ms, x)
	}
	rows.Close()
	for _, x := range ms {
		m := RoomMember{No: x.no, Nickname: x.nick, Me: x.no == me}
		if x.offer != nil {
			if mon, err := scanMon(a.DB.pool.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1 AND state='box'`, *x.offer)); err == nil {
				mon.Box, mon.Slot, mon.SourceGame = nil, nil, "" // others see the Pokemon, not where it lives
				m.Offer = mon
			}
		}
		r.Members = append(r.Members, m)
	}
	trows, err := a.DB.pool.Query(ctx, `SELECT t.id, f.member_no, o.member_no, t.status, t.from_pokemon
	  FROM home_room_trades t
	  JOIN home_room_members f ON f.code=t.code AND f.account_id=t.from_account
	  JOIN home_room_members o ON o.code=t.code AND o.account_id=t.to_account
	  WHERE t.code=$1 AND (f.member_no=$2 OR o.member_no=$2) ORDER BY t.id`, code, me)
	if err != nil {
		return nil, err
	}
	type tt struct {
		RoomTrade
		pk int64
	}
	var ts []tt
	for trows.Next() {
		var x tt
		if err := trows.Scan(&x.ID, &x.FromMember, &x.ToMember, &x.Status, &x.pk); err != nil {
			return nil, err
		}
		ts = append(ts, x)
	}
	trows.Close()
	for _, x := range ts {
		if mon, err := scanMon(a.DB.pool.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1`, x.pk)); err == nil {
			mon.Box, mon.Slot, mon.SourceGame = nil, nil, ""
			x.Offered = mon
		}
		r.Trades = append(r.Trades, x.RoomTrade)
	}
	return r, nil
}

// RoomOffer sets (monID > 0) or clears (0) the member's offered Pokemon.
func (a *App) RoomOffer(ctx context.Context, acct, code string, monID int64) error {
	if _, err := a.memberNo(ctx, acct, code); err != nil {
		return err
	}
	if monID == 0 {
		_, err := a.DB.pool.Exec(ctx, `UPDATE home_room_members SET offer_id=NULL WHERE code=$1 AND account_id=$2`, code, acct)
		return err
	}
	var n int
	if err := a.DB.pool.QueryRow(ctx, `SELECT count(*) FROM home_pokemon WHERE id=$1 AND account_id=$2 AND state='box'`, monID, acct).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err := a.DB.pool.Exec(ctx, `UPDATE home_room_members SET offer_id=$3 WHERE code=$1 AND account_id=$2`, code, acct, monID)
	return err
}

// RoomPropose asks another member to trade the proposer's current offer.
func (a *App) RoomPropose(ctx context.Context, acct, code string, to int) (int64, error) {
	if _, err := a.memberNo(ctx, acct, code); err != nil {
		return 0, err
	}
	var id int64
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		var offer *int64
		if err := t.QueryRow(ctx, `SELECT offer_id FROM home_room_members WHERE code=$1 AND account_id=$2`, code, acct).Scan(&offer); err != nil {
			return err
		}
		if offer == nil {
			return ErrConflict
		}
		var toAcct string
		if err := t.QueryRow(ctx, `SELECT account_id FROM home_room_members WHERE code=$1 AND member_no=$2`, code, to).Scan(&toAcct); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if toAcct == acct {
			return ErrConflict
		}
		return t.QueryRow(ctx, `INSERT INTO home_room_trades(code,from_account,to_account,from_pokemon) VALUES ($1,$2,$3,$4) RETURNING id`, code, acct, toAcct, *offer).Scan(&id)
	})
	return id, err
}

// RoomAccept swaps the proposer's offered Pokemon for the acceptor's current offer.
func (a *App) RoomAccept(ctx context.Context, acct, code string, tradeID int64) (*Mon, error) {
	if _, err := a.memberNo(ctx, acct, code); err != nil {
		return nil, err
	}
	var from string
	if err := a.DB.pool.QueryRow(ctx, `SELECT from_account FROM home_room_trades WHERE id=$1 AND code=$2 AND to_account=$3`, tradeID, code, acct).Scan(&from); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var got *Mon
	err := a.DB.tx(ctx, func(t pgx.Tx) error {
		if err := lockAccounts(ctx, t, acct, from); err != nil {
			return err
		}
		var fromPk int64
		var status string
		if err := t.QueryRow(ctx, `SELECT from_pokemon, status FROM home_room_trades WHERE id=$1 FOR UPDATE`, tradeID).Scan(&fromPk, &status); err != nil {
			return err
		}
		if status != "pending" {
			return ErrConflict
		}
		var myOffer *int64
		if err := t.QueryRow(ctx, `SELECT offer_id FROM home_room_members WHERE code=$1 AND account_id=$2`, code, acct).Scan(&myOffer); err != nil {
			return err
		}
		if myOffer == nil {
			return ErrConflict
		}
		// both Pokemon must still sit in their owners' boxes
		var n int
		if err := t.QueryRow(ctx, `SELECT count(*) FROM home_pokemon WHERE (id=$1 AND account_id=$2 OR id=$3 AND account_id=$4) AND state='box'`, fromPk, from, *myOffer, acct).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return ErrConflict
		}
		var fb, fs, mb, ms int
		if err := t.QueryRow(ctx, `SELECT box, slot FROM home_pokemon WHERE id=$1`, fromPk).Scan(&fb, &fs); err != nil {
			return err
		}
		if err := t.QueryRow(ctx, `SELECT box, slot FROM home_pokemon WHERE id=$1`, *myOffer).Scan(&mb, &ms); err != nil {
			return err
		}
		// each Pokemon takes the other's slot; the sha index is per account, so park first
		for _, id := range []int64{fromPk, *myOffer} {
			if _, err := t.Exec(ctx, `UPDATE home_pokemon SET box=-1, slot=-1 WHERE id=$1`, id); err != nil {
				return err
			}
		}
		var e error
		if _, e = t.Exec(ctx, `UPDATE home_pokemon SET account_id=$2, box=$3, slot=$4, source_game='pokemon-home-room' WHERE id=$1`, fromPk, acct, mb, ms); e != nil {
			if isUnique(e) {
				return ErrConflict
			}
			return e
		}
		if _, e = t.Exec(ctx, `UPDATE home_pokemon SET account_id=$2, box=$3, slot=$4, source_game='pokemon-home-room' WHERE id=$1`, *myOffer, from, fb, fs); e != nil {
			if isUnique(e) {
				return ErrConflict
			}
			return e
		}
		for _, p := range []struct {
			id   int64
			acct string
		}{{fromPk, acct}, {*myOffer, from}} {
			var sp, form int
			var shiny bool
			if err := t.QueryRow(ctx, `SELECT species, form, shiny FROM home_pokemon WHERE id=$1`, p.id).Scan(&sp, &form, &shiny); err != nil {
				return err
			}
			if err := a.registerDex(ctx, t, p.acct, sp, form, shiny); err != nil {
				return err
			}
		}
		if _, err := t.Exec(ctx, `UPDATE home_room_members SET offer_id=NULL WHERE code=$1 AND account_id IN ($2,$3)`, code, acct, from); err != nil {
			return err
		}
		// any other pending proposal that involved either Pokemon is now void
		if _, err := t.Exec(ctx, `UPDATE home_room_trades SET status='cancelled' WHERE status='pending' AND id<>$1 AND from_pokemon IN ($2,$3)`, tradeID, fromPk, *myOffer); err != nil {
			return err
		}
		if _, err := t.Exec(ctx, `UPDATE home_room_trades SET status='done' WHERE id=$1`, tradeID); err != nil {
			return err
		}
		got, e = scanMon(t.QueryRow(ctx, `SELECT `+monCols+` FROM home_pokemon WHERE id=$1`, fromPk))
		return e
	})
	return got, err
}

// RoomDecline lets the recipient decline or the proposer cancel a pending proposal.
func (a *App) RoomDecline(ctx context.Context, acct, code string, tradeID int64) error {
	tag, err := a.DB.pool.Exec(ctx, `UPDATE home_room_trades SET status=CASE WHEN from_account=$3 THEN 'cancelled' ELSE 'declined' END
	  WHERE id=$1 AND code=$2 AND status='pending' AND (from_account=$3 OR to_account=$3)`, tradeID, code, acct)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// RoomSweep removes expired rooms (members and trades cascade).
func (a *App) RoomSweep(ctx context.Context) error {
	_, err := a.DB.pool.Exec(ctx, `DELETE FROM home_rooms WHERE expires_at < now()`)
	return err
}
