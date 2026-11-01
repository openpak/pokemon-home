package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	emit "github.com/openpak/rewards-emit"
)

func TestCompletedTradesReachRewards(t *testing.T) {
	app, _, srv := newTestApp(t)
	defer srv.Close()
	var mu sync.Mutex
	var got []map[string]any
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in struct{ Events []map[string]any }
		json.Unmarshal(b, &in)
		mu.Lock()
		got = append(got, in.Events...)
		mu.Unlock()
		w.Write([]byte(`{"results":[]}`))
	}))
	defer sink.Close()
	rw := emit.New(emit.Config{URL: sink.URL, Key: "k", Source: "pokemon-home", Flush: 10 * time.Millisecond})
	app.Rewards = rw

	a, b := client{t, srv.URL, "acct-a"}, client{t, srv.URL, "acct-b"}
	pa := id(a.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(25, 30, 0, 9, 1)}))
	pb := id(b.must(201, "POST", "/v1/pokemon", map[string]any{"format": "pk9", "data": mon(133, 20, 0, 9, 2)}))
	code := a.must(201, "POST", "/v1/rooms", nil)["code"].(string)
	b.must(200, "POST", "/v1/rooms/"+code+"/join", nil)
	a.must(200, "POST", "/v1/rooms/"+code+"/offer", map[string]any{"pokemon_id": pa})
	b.must(200, "POST", "/v1/rooms/"+code+"/offer", map[string]any{"pokemon_id": pb})
	tr := id(map[string]any{"id": a.must(201, "POST", "/v1/rooms/"+code+"/trades", map[string]any{"to_member": 2})["trade"]})
	// A proposal alone is not a trade.
	rw.Close(context.Background())
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("a proposal reported a trade: %v", got)
	}
	mu.Unlock()
	app.Rewards = emit.New(emit.Config{URL: sink.URL, Key: "k", Source: "pokemon-home", Flush: 10 * time.Millisecond})
	b.must(200, "POST", fmt.Sprintf("/v1/rooms/%s/trades/%d/accept", code, tr), nil)
	b.must(409, "POST", fmt.Sprintf("/v1/rooms/%s/trades/%d/accept", code, tr), nil) // already done: no new events
	app.Rewards.Close(context.Background())
	mu.Lock()
	defer mu.Unlock()
	accts := map[string]bool{}
	for _, e := range got {
		if e["metric"] != "trades_completed" {
			t.Fatalf("%v", e)
		}
		accts[e["account_id"].(string)] = true
	}
	if len(got) != 2 || !accts["acct-a"] || !accts["acct-b"] {
		t.Fatalf("a completed room trade counts for both sides: %v", got)
	}
}
