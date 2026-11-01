package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

type ctxKey struct{}

type authed func(w http.ResponseWriter, r *http.Request, id *Identity)

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) { reply(w, code, map[string]any{"error": msg}) }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		fail(w, 400, "invalid JSON")
		return false
	}
	return true
}

func ctxOf(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 20*time.Second)
}

// httpErr maps a domain error to a status.
func httpErr(w http.ResponseWriter, err error) {
	var ill *IllegalError
	var gl *GLError
	var ae *AuthError
	switch {
	case errors.As(err, &ill):
		fail(w, 422, ill.Msg)
	case errors.Is(err, ErrChecker):
		fail(w, 503, "legality service unavailable")
	case errors.As(err, &ae):
		fail(w, ae.Status, ae.Msg)
	case errors.As(err, &gl):
		st := gl.Status
		if st < 400 || st > 599 {
			st = 502
		}
		if st == 401 || st == 403 {
			st = 502 // our credentials to global-link, not the player's
		}
		fail(w, st, "gts: "+gl.Msg)
	case errors.Is(err, ErrNotFound):
		fail(w, 404, "not found")
	case errors.Is(err, ErrConflict):
		fail(w, 409, "conflict")
	case errors.Is(err, ErrFull):
		fail(w, 409, "full")
	case errors.Is(err, ErrBadSlot):
		fail(w, 400, "box or slot out of range")
	default:
		logf("internal: %v", err)
		fail(w, 500, "internal")
	}
}

func (a *App) with(h authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, c := ctxOf(r)
		defer c()
		id, err := a.Auth.Authenticate(ctx, r)
		if err != nil {
			httpErr(w, err)
			return
		}
		if _, err := a.DB.ensureAccount(ctx, id.AccountID); err != nil {
			httpErr(w, err)
			return
		}
		h(w, r.WithContext(ctx), id)
	}
}

func pathID(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n
}

func (a *App) pid(ctx context.Context, id *Identity) (int64, error) {
	return a.DB.ensureAccount(ctx, id.AccountID)
}

func (a *App) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	m.HandleFunc("GET /v1/me", a.with(a.me))
	m.HandleFunc("GET /v1/boxes", a.with(a.boxes))
	m.HandleFunc("GET /v1/boxes/{n}", a.with(a.boxContents))
	m.HandleFunc("POST /v1/pokemon", a.with(a.deposit))
	m.HandleFunc("GET /v1/pokemon/{id}", a.with(a.getMon))
	m.HandleFunc("POST /v1/pokemon/{id}/move", a.with(a.move))
	m.HandleFunc("POST /v1/pokemon/{id}/withdraw", a.with(a.withdraw))
	m.HandleFunc("POST /v1/pokemon/{id}/confirm", a.with(a.confirm))
	m.HandleFunc("POST /v1/pokemon/{id}/restore", a.with(a.restore))
	m.HandleFunc("GET /v1/limbo", a.with(a.limbo))
	m.HandleFunc("POST /v1/release", a.with(a.release))
	m.HandleFunc("GET /v1/pokedex", a.with(a.pokedex))
	m.HandleFunc("POST /v1/pokedex/rebuild", a.with(a.rebuildDex))

	m.HandleFunc("GET /v1/gts/listings", a.with(a.gtsBrowse))
	m.HandleFunc("POST /v1/gts/listings", a.with(a.gtsList))
	m.HandleFunc("POST /v1/gts/listings/{id}/trade", a.with(a.gtsTrade))
	m.HandleFunc("GET /v1/gts/mine", a.with(a.gtsMine))
	m.HandleFunc("POST /v1/gts/mine/finish", a.with(a.gtsFinish))

	m.HandleFunc("GET /v1/wonder", a.with(a.wonder))
	m.HandleFunc("POST /v1/wonder", a.with(a.wonderSend))
	m.HandleFunc("POST /v1/wonder/{id}/collect", a.with(a.wonderCollect))

	m.HandleFunc("POST /v1/rooms", a.with(a.roomCreate))
	m.HandleFunc("POST /v1/rooms/{code}/join", a.with(a.roomJoin))
	m.HandleFunc("GET /v1/rooms/{code}", a.with(a.roomGet))
	m.HandleFunc("POST /v1/rooms/{code}/offer", a.with(a.roomOffer))
	m.HandleFunc("POST /v1/rooms/{code}/trades", a.with(a.roomPropose))
	m.HandleFunc("POST /v1/rooms/{code}/trades/{id}/accept", a.with(a.roomAccept))
	m.HandleFunc("POST /v1/rooms/{code}/trades/{id}/decline", a.with(a.roomDecline))

	// TODO(capture): the Switch HOME app's own protocol (hosts, session, sync, transfers) goes here once
	// a capture names it. Until then every unmatched request is recorded and answered 501.
	m.HandleFunc("/", a.capture)
	return m
}

func (a *App) me(w http.ResponseWriter, r *http.Request, id *Identity) {
	_, used, err := a.Boxes(r.Context(), id.AccountID)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"account_id": id.AccountID, "nickname": id.Nickname, "via": id.Via,
		"boxes": a.NumBoxes, "slots_per_box": a.SlotsPer, "used": used, "capacity": a.NumBoxes * a.SlotsPer})
}

func (a *App) boxes(w http.ResponseWriter, r *http.Request, id *Identity) {
	b, used, err := a.Boxes(r.Context(), id.AccountID)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"boxes": a.NumBoxes, "slots_per_box": a.SlotsPer, "used": used, "non_empty": b})
}

func (a *App) boxContents(w http.ResponseWriter, r *http.Request, id *Identity) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= a.NumBoxes {
		fail(w, 404, "no such box")
		return
	}
	ms, err := a.BoxContents(r.Context(), id.AccountID, n)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"box": n, "slots": a.SlotsPer, "pokemon": ms})
}

type pokemonIn struct {
	Format     string `json:"format"` // pk8, pk9, pa8, pb8, pb7, pkh, ...
	Data       []byte `json:"data"`   // base64
	Box        *int   `json:"box"`
	Slot       *int   `json:"slot"`
	SourceGame string `json:"source_game"`
}

func (a *App) deposit(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in pokemonIn
	if !decode(w, r, &in) {
		return
	}
	if (in.Box == nil) != (in.Slot == nil) {
		fail(w, 400, "box and slot go together")
		return
	}
	m, v, err := a.Deposit(r.Context(), id.AccountID, in.Format, in.Data, in.SourceGame, in.Box, in.Slot)
	if err != nil {
		var ill *IllegalError
		if errors.As(err, &ill) {
			reply(w, 422, map[string]any{"error": ill.Msg, "verdict": v})
			return
		}
		if errors.Is(err, ErrConflict) {
			fail(w, 409, "that slot is taken, or this exact Pokemon is already in your HOME")
			return
		}
		httpErr(w, err)
		return
	}
	reply(w, 201, m)
}

func (a *App) getMon(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, err := a.GetMon(r.Context(), id.AccountID, pathID(r, "id"), r.URL.Query().Get("data") == "1")
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, m)
}

func (a *App) move(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in struct{ Box, Slot int }
	if !decode(w, r, &in) {
		return
	}
	m, err := a.Move(r.Context(), id.AccountID, pathID(r, "id"), in.Box, in.Slot)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, m)
}

func (a *App) withdraw(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, err := a.Withdraw(r.Context(), id.AccountID, pathID(r, "id"))
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, m)
}

func (a *App) confirm(w http.ResponseWriter, r *http.Request, id *Identity) {
	if err := a.Confirm(r.Context(), id.AccountID, pathID(r, "id")); err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"ok": true})
}

func (a *App) restore(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, err := a.RestoreChecked(r.Context(), id.AccountID, pathID(r, "id"))
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, m)
}

func (a *App) limbo(w http.ResponseWriter, r *http.Request, id *Identity) {
	ms, err := a.Limbo(r.Context(), id.AccountID)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"pokemon": ms})
}

func (a *App) release(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !decode(w, r, &in) || len(in.IDs) == 0 || len(in.IDs) > 6000 {
		if len(in.IDs) == 0 {
			fail(w, 400, "ids required")
		}
		return
	}
	n, err := a.Release(r.Context(), id.AccountID, in.IDs)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"released": n})
}

func (a *App) pokedex(w http.ResponseWriter, r *http.Request, id *Identity) {
	es, err := a.Pokedex(r.Context(), id.AccountID)
	if err != nil {
		httpErr(w, err)
		return
	}
	species := map[int]bool{}
	shiny := 0
	for _, e := range es {
		species[e.Species] = true
		if e.Shiny {
			shiny++
		}
	}
	reply(w, 200, map[string]any{"species": len(species), "forms": len(es), "shiny": shiny, "entries": es})
}

func (a *App) rebuildDex(w http.ResponseWriter, r *http.Request, id *Identity) {
	if err := a.RebuildDex(r.Context(), id.AccountID); err != nil {
		httpErr(w, err)
		return
	}
	a.pokedex(w, r, id)
}

// --- GTS ---

func (a *App) gtsBrowse(w http.ResponseWriter, r *http.Request, id *Identity) {
	raw, err := a.GTSBrowse(r.Context(), r.URL.Query())
	if err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (a *App) gtsList(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in struct {
		PokemonID int64     `json:"pokemon_id"`
		Request   GLRequest `json:"request"`
	}
	if !decode(w, r, &in) {
		return
	}
	pid, err := a.pid(r.Context(), id)
	if err == nil {
		var l *GLListing
		if l, err = a.GTSList(r.Context(), id.AccountID, pid, in.PokemonID, in.Request); err == nil {
			reply(w, 201, l)
			return
		}
	}
	httpErr(w, err)
}

func (a *App) gtsTrade(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in struct {
		PokemonID int64 `json:"pokemon_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	pid, err := a.pid(r.Context(), id)
	if err == nil {
		var m *Mon
		if m, err = a.GTSTrade(r.Context(), id.AccountID, pid, pathID(r, "id"), in.PokemonID); err == nil {
			reply(w, 200, map[string]any{"received": m})
			return
		}
	}
	httpErr(w, err)
}

func (a *App) gtsMine(w http.ResponseWriter, r *http.Request, id *Identity) {
	pid, err := a.pid(r.Context(), id)
	if err == nil {
		var l *GLListing
		if l, err = a.GTSMine(r.Context(), id.AccountID, pid); err == nil {
			reply(w, 200, l)
			return
		}
	}
	httpErr(w, err)
}

func (a *App) gtsFinish(w http.ResponseWriter, r *http.Request, id *Identity) {
	pid, err := a.pid(r.Context(), id)
	if err == nil {
		var m *Mon
		var kind string
		if m, kind, err = a.GTSFinish(r.Context(), id.AccountID, pid); err == nil {
			reply(w, 200, map[string]any{"result": kind, "pokemon": m})
			return
		}
	}
	httpErr(w, err)
}

// --- Wonder Box ---

func (a *App) wonder(w http.ResponseWriter, r *http.Request, id *Identity) {
	es, err := a.Wonder(r.Context(), id.AccountID)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"slots": a.WonderSlots, "delay_seconds": int(a.WonderDelay.Seconds()), "entries": es})
}

func (a *App) wonderSend(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in struct {
		PokemonID int64 `json:"pokemon_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := a.WonderSend(r.Context(), id.AccountID, in.PokemonID)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 201, e)
}

func (a *App) wonderCollect(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, err := a.WonderCollect(r.Context(), id.AccountID, pathID(r, "id"))
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"received": m})
}

// --- Rooms ---

func nick(id *Identity) string {
	if id.Nickname != "" {
		return id.Nickname
	}
	return "Trainer"
}

func (a *App) roomCreate(w http.ResponseWriter, r *http.Request, id *Identity) {
	rm, err := a.RoomCreate(r.Context(), id.AccountID, nick(id))
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 201, rm)
}

func (a *App) roomJoin(w http.ResponseWriter, r *http.Request, id *Identity) {
	rm, err := a.RoomJoin(r.Context(), id.AccountID, nick(id), r.PathValue("code"))
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, rm)
}

func (a *App) roomGet(w http.ResponseWriter, r *http.Request, id *Identity) {
	rm, err := a.RoomGet(r.Context(), id.AccountID, r.PathValue("code"))
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, rm)
}

func (a *App) roomOffer(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in struct {
		PokemonID int64 `json:"pokemon_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.RoomOffer(r.Context(), id.AccountID, r.PathValue("code"), in.PokemonID); err != nil {
		httpErr(w, err)
		return
	}
	a.roomGet(w, r, id)
}

func (a *App) roomPropose(w http.ResponseWriter, r *http.Request, id *Identity) {
	var in struct {
		To int `json:"to_member"`
	}
	if !decode(w, r, &in) {
		return
	}
	tid, err := a.RoomPropose(r.Context(), id.AccountID, r.PathValue("code"), in.To)
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 201, map[string]any{"trade": tid})
}

func (a *App) roomAccept(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, err := a.RoomAccept(r.Context(), id.AccountID, r.PathValue("code"), pathID(r, "id"))
	if err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"received": m})
}

func (a *App) roomDecline(w http.ResponseWriter, r *http.Request, id *Identity) {
	if err := a.RoomDecline(r.Context(), id.AccountID, r.PathValue("code"), pathID(r, "id")); err != nil {
		httpErr(w, err)
		return
	}
	reply(w, 200, map[string]any{"ok": true})
}
