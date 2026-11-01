package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Needs Postgres: HOME_TEST_DSN=postgres://postgres:t@127.0.0.1:55471/home?sslmode=disable

// fakeChecker reads a Pokemon as bytes: [0..1]=species, [2]=level, [3]=1 means illegal, [4]=generation.
type fakeChecker struct{ down bool }

func (f *fakeChecker) Check(ctx context.Context, data []byte, format string) (*Verdict, error) {
	if f.down {
		return nil, fmt.Errorf("down")
	}
	if len(data) < 5 {
		return &Verdict{Valid: false, Error: "not a Pokemon"}, fmt.Errorf("not a Pokemon")
	}
	return &Verdict{Valid: data[3] == 0, Parsed: true, Generation: int(data[4]), Species: int(data[0]) | int(data[1])<<8,
		Level: int(data[2]), Moves: []int{33}, Issues: map[bool][]string{true: {"bad EVs"}}[data[3] != 0]}, nil
}

func mon(species, level, illegal, gen, uniq byte) []byte {
	return []byte{species, 0, level, illegal, gen, uniq}
}

type fakeGL struct {
	mu       sync.Mutex
	listings map[int64]*GLListing
	owner    map[int64]int64
	next     int64
}

func newFakeGL() (*fakeGL, *httptest.Server) {
	g := &fakeGL{listings: map[int64]*GLListing{}, owner: map[int64]int64{}, next: 1}
	mux := http.NewServeMux()
	chk := func(r *http.Request) bool { return r.Header.Get("X-Internal-Key") == "glkey" }
	mux.HandleFunc("POST /internal/v1/gts/deposit", func(w http.ResponseWriter, r *http.Request) {
		if !chk(r) {
			fail(w, 401, "key")
			return
		}
		var in struct {
			PID     int64
			Request GLRequest
			Format  string
			Data    []byte
		}
		json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		defer g.mu.Unlock()
		for id, o := range g.owner {
			if o == in.PID && g.listings[id].Status != "collected" && g.listings[id].Status != "withdrawn" {
				fail(w, 409, "already")
				return
			}
		}
		l := &GLListing{ID: g.next, Generation: int(in.Data[4]), Species: int(in.Data[0]), Status: "open", Format: in.Format, Data: in.Data, Request: in.Request}
		g.listings[g.next], g.owner[g.next] = l, in.PID
		g.next++
		reply(w, 201, l)
	})
	mux.HandleFunc("POST /internal/v1/gts/{id}/trade", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PID    int64
			Format string
			Data   []byte
		}
		json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		defer g.mu.Unlock()
		l := g.listings[pathID(r, "id")]
		if l == nil || l.Status != "open" || g.owner[l.ID] == in.PID {
			fail(w, 409, "gone")
			return
		}
		if l.Request.Species != 0 && l.Request.Species != int(in.Data[0]) {
			fail(w, 422, "mismatch")
			return
		}
		l.Status, l.ReturnedFormat, l.Returned = "traded", in.Format, in.Data
		reply(w, 200, l)
	})
	mux.HandleFunc("GET /internal/v1/gts/mine", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		for id, o := range g.owner {
			if fmt.Sprint(o) == r.URL.Query().Get("pid") && (g.listings[id].Status == "open" || g.listings[id].Status == "traded") {
				reply(w, 200, g.listings[id])
				return
			}
		}
		fail(w, 404, "none")
	})
	mux.HandleFunc("POST /internal/v1/gts/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		l := g.listings[pathID(r, "id")]
		if l == nil {
			fail(w, 404, "nf")
			return
		}
		cp := *l
		l.Status = map[string]string{"open": "withdrawn", "traded": "collected"}[l.Status]
		cp.Status = l.Status
		reply(w, 200, cp)
	})
	mux.HandleFunc("GET /api/v1/gts/listings", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"total": len(g.listings), "listings": []any{}})
	})
	return g, httptest.NewServer(mux)
}

func newTestApp(t *testing.T) (*App, *fakeChecker, *httptest.Server) {
	dsn := os.Getenv("HOME_TEST_DSN")
	if dsn == "" {
		t.Skip("HOME_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := OpenDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.pool.Exec(ctx, `TRUNCATE home_room_trades, home_room_members, home_rooms, home_wonder, home_gts, home_dex, home_pokemon, home_accounts RESTART IDENTITY CASCADE`)
	_, gls := newFakeGL()
	t.Cleanup(gls.Close)
	nx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDToken string `json:"id_token"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get("X-Internal-Key") != "nxkey" {
			w.WriteHeader(401)
			return
		}
		switch in.IDToken {
		case "tok-alice":
			reply(w, 200, map[string]any{"pid": 7, "nickname": "Alice", "account_id": "acct-alice", "application_id": "0100abcd"})
		case "tok-unlinked":
			reply(w, 200, map[string]any{"pid": 8, "nickname": "X", "account_id": ""})
		case "tok-unknown":
			w.WriteHeader(404)
		case "tok-banned":
			w.WriteHeader(403)
		default:
			w.WriteHeader(401)
		}
	}))
	t.Cleanup(nx.Close)
	fc := &fakeChecker{}
	app := &App{DB: db, Check: fc, GL: &GlobalLink{URL: gls.URL, Key: "glkey"},
		Auth:     &Authenticator{InternalKey: "0123456789abcdef", NXURL: nx.URL, NXKey: "nxkey"},
		NumBoxes: 4, SlotsPer: 3, WonderDelay: time.Second, WonderSlots: 2, RoomMax: 3, RoomTTL: time.Hour}
	return app, fc, httptest.NewServer(app.Routes())
}

type client struct {
	t    *testing.T
	base string
	acct string
}

func (c client) do(method, path string, in any) (int, map[string]any) {
	var rd *bytes.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("X-Internal-Key", "0123456789abcdef")
	req.Header.Set("X-OpenPak-Account", c.acct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c client) dep(species, level, illegal, gen, uniq byte) (int, map[string]any) {
	return c.do("POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(species, level, illegal, gen, uniq), "source_game": "sv"})
}

func (c client) must(code int, method, path string, in any) map[string]any {
	c.t.Helper()
	got, out := c.do(method, path, in)
	if got != code {
		c.t.Fatalf("%s %s: %d want %d: %v", method, path, got, code, out)
	}
	return out
}

func id(m map[string]any) int64 { return int64(m["id"].(float64)) }

func TestBoxesGateDexWithdraw(t *testing.T) {
	_, fc, srv := newTestApp(t)
	defer srv.Close()
	a := client{t, srv.URL, "acct-a"}

	m1 := a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(25, 30, 0, 9, 1)})
	if m1["box"].(float64) != 0 || m1["slot"].(float64) != 0 || m1["species"].(float64) != 25 {
		t.Fatalf("first mon placement: %v", m1)
	}
	// illegal: refused, nothing stored
	code, out := a.dep(150, 50, 1, 9, 2)
	if code != 422 || out["verdict"] == nil {
		t.Fatalf("illegal deposit: %d %v", code, out)
	}
	// checker down: refused, nothing stored (fail closed)
	fc.down = true
	if code, _ := a.dep(1, 5, 0, 9, 3); code != 503 {
		t.Fatalf("checker down: %d", code)
	}
	fc.down = false
	if r := a.must(200, "GET", "/v1/boxes", nil); r["used"].(float64) != 1 {
		t.Fatalf("only the legal one is stored: %v", r)
	}
	// same bytes twice = a clone
	if code, _ := a.dep(25, 30, 0, 9, 1); code != 409 {
		t.Fatalf("duplicate deposit: %d", code)
	}
	// fill: 4 boxes x 3 = 12
	for i := 0; i < 11; i++ {
		a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(10, 5, 0, 9, byte(10+i))})
	}
	if code, _ := a.dep(11, 5, 0, 9, 99); code != 409 {
		t.Fatalf("full: %d", code)
	}
	// dex: 25 and 10
	dx := a.must(200, "GET", "/v1/pokedex", nil)
	if dx["species"].(float64) != 2 {
		t.Fatalf("dex: %v", dx)
	}
	// move onto an occupied slot swaps
	m2 := a.must(200, "GET", "/v1/boxes/0", nil)["pokemon"].([]any)[1].(map[string]any)
	a.must(200, "POST", fmt.Sprintf("/v1/pokemon/%d/move", id(m1)), map[string]any{"box": 0, "slot": 1})
	got := a.must(200, "GET", fmt.Sprintf("/v1/pokemon/%d", id(m2)), nil)
	if got["slot"].(float64) != 0 {
		t.Fatalf("swap: %v", got)
	}
	if code, _ := a.do("POST", fmt.Sprintf("/v1/pokemon/%d/move", id(m1)), map[string]any{"box": 9, "slot": 0}); code != 400 {
		t.Fatalf("out of range: %d", code)
	}
	// withdraw -> limbo; the slot frees; restore; confirm deletes
	w := a.must(200, "POST", fmt.Sprintf("/v1/pokemon/%d/withdraw", id(m1)), nil)
	if w["data"] == nil || w["state"] != "limbo" {
		t.Fatalf("withdraw: %v", w)
	}
	a.must(404, "POST", fmt.Sprintf("/v1/pokemon/%d/withdraw", id(m1)), nil)
	a.must(200, "POST", fmt.Sprintf("/v1/pokemon/%d/restore", id(m1)), nil)
	a.must(200, "POST", fmt.Sprintf("/v1/pokemon/%d/withdraw", id(m1)), nil)
	a.must(200, "POST", fmt.Sprintf("/v1/pokemon/%d/confirm", id(m1)), nil)
	if r := a.must(200, "GET", "/v1/boxes", nil); r["used"].(float64) != 11 {
		t.Fatalf("after confirm: %v", r)
	}
	// the dex keeps Pikachu after it left
	if dx := a.must(200, "GET", "/v1/pokedex", nil); dx["species"].(float64) != 2 {
		t.Fatalf("dex keeps entry: %v", dx)
	}
	// another account cannot see or move it
	b := client{t, srv.URL, "acct-b"}
	b.must(404, "GET", fmt.Sprintf("/v1/pokemon/%d", id(m2)), nil)
	b.must(404, "POST", fmt.Sprintf("/v1/pokemon/%d/withdraw", id(m2)), nil)
}

func TestAuth(t *testing.T) {
	_, _, srv := newTestApp(t)
	defer srv.Close()
	get := func(hdr map[string]string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/v1/me", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	for name, c := range map[string]struct {
		h    map[string]string
		want int
	}{
		"nothing":        {nil, 401},
		"id token":       {map[string]string{"Authorization": "Bearer tok-alice"}, 200},
		"unproven":       {map[string]string{"Authorization": "Bearer nope"}, 401},
		"unlinked":       {map[string]string{"Authorization": "Bearer tok-unlinked"}, 403},
		"unknown":        {map[string]string{"Authorization": "Bearer tok-unknown"}, 403},
		"banned":         {map[string]string{"Authorization": "Bearer tok-banned"}, 403},
		"bad key":        {map[string]string{"X-Internal-Key": "wrong", "X-OpenPak-Account": "x"}, 401},
		"key no account": {map[string]string{"X-Internal-Key": "0123456789abcdef"}, 400},
		"proxy":          {map[string]string{"X-Internal-Key": "0123456789abcdef", "X-OpenPak-Account": "x"}, 200},
	} {
		if got := get(c.h); got != c.want {
			t.Errorf("%s: %d want %d", name, got, c.want)
		}
	}
	// the account the token proves is the core account
	req, _ := http.NewRequest("GET", srv.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	r, _ := http.DefaultClient.Do(req)
	var me map[string]any
	json.NewDecoder(r.Body).Decode(&me)
	if me["account_id"] != "acct-alice" || me["via"] != "id_token" {
		t.Fatalf("me: %v", me)
	}
}

func TestApplicationScope(t *testing.T) {
	app, _, srv := newTestApp(t)
	defer srv.Close()
	app.Auth.AppIDs = map[string]bool{"0100ffff": true}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 403 {
		t.Fatalf("wrong application: %d", r.StatusCode)
	}
}

func TestGTS(t *testing.T) {
	_, fc, srv := newTestApp(t)
	defer srv.Close()
	a, b := client{t, srv.URL, "acct-a"}, client{t, srv.URL, "acct-b"}
	pa := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(25, 30, 0, 9, 1)}))
	pb := id(b.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(150, 70, 0, 9, 2)}))
	pb2 := id(b.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(7, 70, 0, 9, 3)}))

	l := a.must(201, "POST", "/v1/gts/listings", map[string]any{"pokemon_id": pa, "request": map[string]any{"species": 150, "gender": -1}})
	lid := id(l)
	if r := a.must(200, "GET", "/v1/boxes", nil); r["used"].(float64) != 0 {
		t.Fatalf("listed Pokemon left the box: %v", r)
	}
	a.must(409, "POST", "/v1/gts/listings", map[string]any{"pokemon_id": pa}) // gone from boxes AND one listing each
	// wrong species: refused, offered Pokemon goes back
	b.must(422, "POST", fmt.Sprintf("/v1/gts/listings/%d/trade", lid), map[string]any{"pokemon_id": pb2})
	if r := b.must(200, "GET", "/v1/boxes", nil); r["used"].(float64) != 2 {
		t.Fatalf("failed trade restored: %v", r)
	}
	// checker down on the receiving side does not lose the completed trade
	fc.down = true
	b.must(503, "POST", fmt.Sprintf("/v1/gts/listings/%d/trade", lid), map[string]any{"pokemon_id": pb})
	fc.down = false
	if r := b.must(200, "GET", "/v1/limbo", nil)["pokemon"].([]any); len(r) != 1 {
		t.Fatalf("received Pokemon must wait in limbo: %v", r)
	}
	lim := b.must(200, "GET", "/v1/limbo", nil)["pokemon"].([]any)[0].(map[string]any)
	b.must(200, "POST", fmt.Sprintf("/v1/pokemon/%d/restore", id(lim)), nil) // passes the gate on restore
	// a collects the Mewtwo
	if mine := a.must(200, "GET", "/v1/gts/mine", nil); mine["status"] != "traded" {
		t.Fatalf("mine: %v", mine)
	}
	f := a.must(200, "POST", "/v1/gts/mine/finish", nil)
	if f["result"] != "collected" || f["pokemon"].(map[string]any)["species"].(float64) != 150 {
		t.Fatalf("finish: %v", f)
	}
	a.must(404, "GET", "/v1/gts/mine", nil)

	// withdraw an open listing: the original returns
	l2 := id(b.must(201, "POST", "/v1/gts/listings", map[string]any{"pokemon_id": pb2, "request": map[string]any{"species": 0, "gender": -1}}))
	_ = l2
	f = b.must(200, "POST", "/v1/gts/mine/finish", nil)
	if f["result"] != "withdrawn" || f["pokemon"].(map[string]any)["species"].(float64) != 7 {
		t.Fatalf("withdraw: %v", f)
	}
	a.must(200, "GET", "/v1/gts/listings", nil)
}

func TestWonderBox(t *testing.T) {
	app, _, srv := newTestApp(t)
	defer srv.Close()
	a, b := client{t, srv.URL, "acct-a"}, client{t, srv.URL, "acct-b"}
	pa := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(25, 30, 0, 9, 1)}))
	pb := id(b.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(133, 20, 0, 9, 2)}))
	ea := id(a.must(201, "POST", "/v1/wonder", map[string]any{"pokemon_id": pa}))
	// before the delay nothing pairs
	if n, _ := app.WonderPair(context.Background()); n != 0 {
		t.Fatalf("paired too early: %d", n)
	}
	a.must(404, "POST", fmt.Sprintf("/v1/wonder/%d/collect", ea), nil)
	// the same account can't pair with itself
	pa2 := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(26, 30, 0, 9, 5)}))
	a.must(201, "POST", "/v1/wonder", map[string]any{"pokemon_id": pa2})
	a.must(409, "POST", "/v1/wonder", map[string]any{"pokemon_id": pa}) // already in the Wonder Box / slots
	eb := id(b.must(201, "POST", "/v1/wonder", map[string]any{"pokemon_id": pb}))
	time.Sleep(1200 * time.Millisecond)
	n, err := app.WonderPair(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("pair: %d %v", n, err)
	}
	ma := a.must(200, "POST", fmt.Sprintf("/v1/wonder/%d/collect", ea), nil)["received"].(map[string]any)
	mb := b.must(200, "POST", fmt.Sprintf("/v1/wonder/%d/collect", eb), nil)["received"].(map[string]any)
	if ma["species"].(float64) != 133 || mb["species"].(float64) != 25 {
		t.Fatalf("swap: %v %v", ma, mb)
	}
	a.must(404, "POST", fmt.Sprintf("/v1/wonder/%d/collect", ea), nil)
	if dx := a.must(200, "GET", "/v1/pokedex", nil); dx["species"].(float64) != 3 { // 25, 26, 133
		t.Fatalf("dex after wonder: %v", dx)
	}
}

func TestRoomTrade(t *testing.T) {
	_, _, srv := newTestApp(t)
	defer srv.Close()
	a, b, c := client{t, srv.URL, "acct-a"}, client{t, srv.URL, "acct-b"}, client{t, srv.URL, "acct-c"}
	pa := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(25, 30, 0, 9, 1)}))
	pb := id(b.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(133, 20, 0, 9, 2)}))
	room := a.must(201, "POST", "/v1/rooms", nil)
	code := room["code"].(string)
	b.must(200, "POST", "/v1/rooms/"+code+"/join", nil)
	c.must(200, "POST", "/v1/rooms/"+code+"/join", nil)
	d := client{t, srv.URL, "acct-d"}
	d.must(409, "POST", "/v1/rooms/"+code+"/join", nil) // RoomMax 3
	d.must(404, "GET", "/v1/rooms/"+code, nil)          // not a member
	d.must(404, "POST", "/v1/rooms/NOSUCH11/join", nil)
	// no offer yet
	a.must(409, "POST", "/v1/rooms/"+code+"/trades", map[string]any{"to_member": 2})
	a.must(200, "POST", "/v1/rooms/"+code+"/offer", map[string]any{"pokemon_id": pa})
	b.must(404, "POST", "/v1/rooms/"+code+"/offer", map[string]any{"pokemon_id": pa}) // not b's
	r := b.must(200, "POST", "/v1/rooms/"+code+"/offer", map[string]any{"pokemon_id": pb})
	for _, m := range r["members"].([]any) { // no account ids leak
		if strings.Contains(fmt.Sprint(m), "acct-") {
			t.Fatalf("account id leaked: %v", m)
		}
	}
	tr := id(map[string]any{"id": a.must(201, "POST", "/v1/rooms/"+code+"/trades", map[string]any{"to_member": 2})["trade"]})
	a.must(404, "POST", fmt.Sprintf("/v1/rooms/%s/trades/%d/accept", code, tr), nil) // proposer can't accept
	b.must(200, "POST", fmt.Sprintf("/v1/rooms/%s/trades/%d/accept", code, tr), nil)
	b.must(409, "POST", fmt.Sprintf("/v1/rooms/%s/trades/%d/accept", code, tr), nil) // already done
	ga := a.must(200, "GET", fmt.Sprintf("/v1/pokemon/%d", pb), nil)
	gb := b.must(200, "GET", fmt.Sprintf("/v1/pokemon/%d", pa), nil)
	if ga["species"].(float64) != 133 || gb["species"].(float64) != 25 {
		t.Fatalf("swap: %v %v", ga, gb)
	}
	a.must(404, "GET", fmt.Sprintf("/v1/pokemon/%d", pa), nil)
	// decline path
	pc := id(c.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(1, 5, 0, 9, 7)}))
	c.must(200, "POST", "/v1/rooms/"+code+"/offer", map[string]any{"pokemon_id": pc})
	a.must(200, "POST", "/v1/rooms/"+code+"/offer", map[string]any{"pokemon_id": pb})
	t2 := a.must(201, "POST", "/v1/rooms/"+code+"/trades", map[string]any{"to_member": 3})["trade"].(float64)
	c.must(200, "POST", fmt.Sprintf("/v1/rooms/%s/trades/%d/decline", code, int64(t2)), nil)
	c.must(409, "POST", fmt.Sprintf("/v1/rooms/%s/trades/%d/accept", code, int64(t2)), nil)
}

func TestConcurrentWithdrawAndTrade(t *testing.T) {
	_, _, srv := newTestApp(t)
	defer srv.Close()
	a := client{t, srv.URL, "acct-a"}
	p := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(25, 30, 0, 9, 1)}))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, _ := a.do("POST", fmt.Sprintf("/v1/pokemon/%d/withdraw", p), nil); code == 200 {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one withdraw may win, got %d", wins)
	}
}

func TestCaptureMode(t *testing.T) {
	app, _, srv := newTestApp(t)
	defer srv.Close()
	app.CaptureDir = t.TempDir()
	req, _ := http.NewRequest("POST", srv.URL+"/some/home/path?x=1", strings.NewReader(`{"a":1}`))
	req.Header.Set("Authorization", "Bearer SECRETTOKEN")
	req.Header.Set("X-Thing", "kept")
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 501 {
		t.Fatalf("status %d", r.StatusCode)
	}
	files, _ := os.ReadDir(app.CaptureDir)
	if len(files) != 1 {
		t.Fatalf("capture files: %d", len(files))
	}
	b, _ := os.ReadFile(app.CaptureDir + "/" + files[0].Name())
	if strings.Contains(string(b), "SECRETTOKEN") || !strings.Contains(string(b), "/some/home/path?x=1") || !strings.Contains(string(b), "kept") {
		t.Fatalf("capture content: %s", b)
	}
}

func TestPKHRoundTrip(t *testing.T) {
	plain := make([]byte, pkhHeader+32)
	plain[0] = 4
	copy(plain[2:], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	for i := pkhHeader; i < len(plain); i++ {
		plain[i] = byte(i)
	}
	enc, err := PKHWrap(plain)
	if err != nil || bytes.Equal(enc[pkhHeader:], plain[pkhHeader:]) {
		t.Fatalf("wrap: %v", err)
	}
	dec, err := PKHPlain(enc)
	if err != nil || !bytes.Equal(dec, plain) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := PKHPlain(plain[:20]); err == nil {
		t.Fatal("short input must fail")
	}
	bad := append([]byte(nil), plain...)
	bad[0] = 9
	if _, err := PKHPlain(bad); err == nil {
		t.Fatal("unknown version must fail")
	}
}
