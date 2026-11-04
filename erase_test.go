package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	uA = "11111111-1111-4111-8111-111111111111"
	uB = "22222222-2222-4222-8222-222222222222"
)

func count(t *testing.T, app *App, q string, args ...any) int {
	t.Helper()
	var n int
	if err := app.DB.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEraseAccount(t *testing.T) {
	t.Setenv("ACCOUNT_ERASE_KEY", "erase-key-0123456789")
	app, _, srv := newTestApp(t)
	defer srv.Close()
	_, fake := newFakeGL()
	defer fake.Close()
	var mu sync.Mutex
	var erased []string
	target, _ := url.Parse(fake.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			erased = append(erased, r.URL.Path+"?"+r.URL.RawQuery+" auth="+r.Header.Get("Authorization"))
			mu.Unlock()
			w.Write([]byte(`{"service":"pokemon-global-link","rows":2}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer gl.Close()
	app.GL = &GlobalLink{URL: gl.URL, Key: "glkey", EraseURL: gl.URL}
	app.WonderDelay = time.Second
	srv.Config.Handler = app.WithErase(app.Routes())

	a, b := client{t, srv.URL, uA}, client{t, srv.URL, uB}
	pa := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(25, 30, 0, 9, 1)}))
	pa2 := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(26, 30, 0, 9, 5)}))
	pa3 := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(27, 30, 0, 9, 6)}))
	pb := id(b.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(133, 20, 0, 9, 2)}))
	pb2 := id(b.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(134, 20, 0, 9, 3)}))
	a.must(201, "POST", "/v1/gts/listings", map[string]any{"pokemon_id": pa3, "request": map[string]any{"species": 0, "gender": -1}})
	a.must(201, "POST", "/v1/wonder", map[string]any{"pokemon_id": pa})
	b.must(201, "POST", "/v1/wonder", map[string]any{"pokemon_id": pb})
	time.Sleep(1200 * time.Millisecond)
	if n, err := app.WonderPair(context.Background()); err != nil || n != 1 {
		t.Fatalf("pair %d %v", n, err)
	}
	room := a.must(201, "POST", "/v1/rooms", nil)["code"].(string)
	b.must(200, "POST", "/v1/rooms/"+room+"/join", nil)
	a.must(200, "POST", "/v1/rooms/"+room+"/offer", map[string]any{"pokemon_id": pa2})
	b.must(200, "POST", "/v1/rooms/"+room+"/offer", map[string]any{"pokemon_id": pb2})
	a.must(201, "POST", "/v1/rooms/"+room+"/trades", map[string]any{"to_member": 2})
	bRoom := b.must(201, "POST", "/v1/rooms", nil)["code"].(string)
	a.must(200, "POST", "/v1/rooms/"+bRoom+"/join", nil)

	bBefore := count(t, app, `SELECT (SELECT count(*) FROM home_pokemon WHERE account_id=$1)+(SELECT count(*) FROM home_dex WHERE account_id=$1)`, uB)

	del := func(key, path string) (int, string) {
		req, _ := http.NewRequest("DELETE", srv.URL+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		sb.Write(buf[:n])
		return resp.StatusCode, sb.String()
	}
	p := "/internal/accounts/" + uA + "/data"
	if c, _ := del("", p); c != 401 {
		t.Fatalf("no key: %d", c)
	}
	if c, _ := del("wrong", p); c != 401 {
		t.Fatalf("wrong key: %d", c)
	}
	if c, _ := del("erase-key-0123456789", "/internal/accounts/not-a-uuid/data"); c != 400 {
		t.Fatalf("bad uuid: %d", c)
	}
	c, body := del("erase-key-0123456789", p)
	if c != 200 || !strings.Contains(body, `"service":"pokemon-home"`) || strings.Contains(body, `"rows":0`) {
		t.Fatalf("erase: %d %s", c, body)
	}
	if len(erased) != 1 || !strings.Contains(erased[0], "/internal/accounts/"+uA+"/data?pid=") || !strings.Contains(erased[0], "Bearer erase-key") {
		t.Fatalf("global-link call: %v", erased)
	}
	for _, q := range []string{
		`SELECT count(*) FROM home_accounts WHERE account_id=$1`, `SELECT count(*) FROM home_pokemon WHERE account_id=$1`,
		`SELECT count(*) FROM home_dex WHERE account_id=$1`, `SELECT count(*) FROM home_gts WHERE account_id=$1`,
		`SELECT count(*) FROM home_wonder WHERE account_id=$1`, `SELECT count(*) FROM home_room_members WHERE account_id=$1`,
		`SELECT count(*) FROM home_rooms WHERE host=$1`,
		`SELECT count(*) FROM home_room_trades WHERE from_account=$1 OR to_account=$1`,
	} {
		if n := count(t, app, q, uA); n != 0 {
			t.Fatalf("left over (%d): %s", n, q)
		}
	}
	if n := count(t, app, `SELECT count(*) FROM home_rooms WHERE code=$1`, room); n != 0 {
		t.Fatal("hosted room kept")
	}
	// the other player is untouched; their Wonder entry waits again
	if n := count(t, app, `SELECT (SELECT count(*) FROM home_pokemon WHERE account_id=$1)+(SELECT count(*) FROM home_dex WHERE account_id=$1)`, uB); n != bBefore {
		t.Fatalf("b changed: %d != %d", n, bBefore)
	}
	if n := count(t, app, `SELECT count(*) FROM home_wonder WHERE account_id=$1 AND recv_id IS NULL`, uB); n != 1 {
		t.Fatal("b wonder entry not reset")
	}
	if n := count(t, app, `SELECT count(*) FROM home_rooms WHERE code=$1`, bRoom); n != 1 {
		t.Fatal("b's room lost")
	}
	if n := count(t, app, `SELECT count(*) FROM home_room_members WHERE account_id=$1`, uB); n != 1 {
		t.Fatalf("b membership: %d", n)
	}
	// second run: nothing left, no error, global-link not asked again
	n, err := app.Erase(context.Background(), uA)
	if err != nil || n != 0 {
		t.Fatalf("second run: %d %v", n, err)
	}
	if len(erased) != 1 {
		t.Fatal("global-link asked for an account with no HOME row")
	}
}

func TestEraseDisabledWithoutKey(t *testing.T) {
	t.Setenv("ACCOUNT_ERASE_KEY", "")
	app, _, srv := newTestApp(t)
	defer srv.Close()
	srv.Config.Handler = app.WithErase(app.Routes())
	req, _ := http.NewRequest("DELETE", srv.URL+"/internal/accounts/"+uA+"/data", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
