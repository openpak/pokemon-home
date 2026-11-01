// Command pokemon-home is OpenPak's Pokemon HOME: account-bound box storage, Pokedex, GTS (on
// pokemon-global-link), Wonder Box and Room trade, with a legality gate on every deposit. The HOME
// user is the OpenPak core account; there is no separate login.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func logf(f string, a ...any) { log.Printf(f, a...) }

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return d
}

func envDur(k string, d time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return d
}

func main() {
	key := os.Getenv("HOME_INTERNAL_KEY")
	if len(key) < 16 {
		log.Fatal("HOME_INTERNAL_KEY is required (16+ bytes)")
	}
	ctx := context.Background()
	db, err := OpenDB(ctx, env("HOME_DATABASE_URL", "postgres://home@localhost/home?sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	ids := map[string]bool{}
	for _, s := range strings.Split(os.Getenv("HOME_APPLICATION_IDS"), ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			ids[s] = true
		}
	}
	app := &App{
		DB:    db,
		Check: &HTTPChecker{URL: env("HOME_LEGALITY_URL", "http://openpak-pokemon-legality:20170"), Key: os.Getenv("HOME_LEGALITY_KEY")},
		GL:    &GlobalLink{URL: env("HOME_GL_URL", "http://openpak-pokemon-global-link:20160"), Key: os.Getenv("HOME_GL_KEY")},
		Auth: &Authenticator{InternalKey: key, NXURL: env("HOME_NX_URL", "http://openpak-nx-baas:20070"),
			NXKey: os.Getenv("HOME_NX_KEY"), AppIDs: ids},
		NumBoxes: envInt("HOME_BOXES", 200), SlotsPer: envInt("HOME_BOX_SLOTS", 30),
		WonderDelay: envDur("HOME_WONDER_DELAY", 5*time.Minute), WonderSlots: envInt("HOME_WONDER_SLOTS", 3),
		RoomMax: envInt("HOME_ROOM_MAX", 20), RoomTTL: envDur("HOME_ROOM_TTL", 24*time.Hour),
		CaptureDir: os.Getenv("HOME_CAPTURE_DIR"),
	}
	go func() { // wonder pairing and room expiry
		for range time.Tick(15 * time.Second) {
			if _, err := app.WonderPair(ctx); err != nil {
				logf("wonder pair: %v", err)
			}
			if err := app.RoomSweep(ctx); err != nil {
				logf("room sweep: %v", err)
			}
		}
	}()
	srv := &http.Server{Addr: env("HOME_HTTP_ADDR", ":20200"), Handler: app.Routes(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	// The console-facing listener: TLS with an OpenPak-CA leaf (served with the CA), reached by Traefik
	// passthrough on the HOME SNIs. Off until the hosts are known (docs/protocol.md).
	if cert, k := os.Getenv("HOME_TLS_CERT"), os.Getenv("HOME_TLS_KEY"); cert != "" && k != "" {
		tsrv := &http.Server{Addr: env("HOME_TLS_ADDR", ":21040"), Handler: srv.Handler,
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
		go func() { log.Fatal(tsrv.ListenAndServeTLS(cert, k)) }()
		log.Printf("pokemon-home TLS listening on %s", tsrv.Addr)
	}
	log.Printf("pokemon-home listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}
