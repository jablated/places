// Command places serves a personal wiki of NYC places: a JSON HTTP API over a
// pure-Go SQLite database, plus the SvelteKit frontend embedded into the same
// binary. No CGO, so it builds static and ships on distroless.
//
// There is no auth — this is a personal, LAN-or-tailnet service. Do not put it
// on the open internet without something in front of it.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Config is the runtime configuration, sourced entirely from environment
// variables so the same binary works locally and in a container.
type Config struct {
	DataDir string // DATA_DIR — where places.db lives
	Addr    string // ADDR — listen address
	// CORSOrigin (CORS_ORIGIN) enables cross-origin API access for the
	// SvelteKit dev server, which runs on its own port. Comma-separated list of
	// origins, or "*". Empty (the default) disables CORS entirely, which is
	// what the single-binary deployment wants — there the frontend is
	// same-origin.
	CORSOrigin string
	// Seed (SEED) controls whether an empty database gets the starter places and
	// trips. On by default so a fresh run is immediately useful. The two seeds
	// are independent: an existing database with places but no trips still gets
	// the starter itineraries, reusing any of their places it already holds.
	Seed bool
}

func loadConfig() Config {
	return Config{
		DataDir:    envStr("DATA_DIR", "./data"),
		Addr:       envStr("ADDR", ":8080"),
		CORSOrigin: envStr("CORS_ORIGIN", ""),
		Seed:       envBool("SEED", true),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "":
		return def
	case "1", "true", "TRUE", "yes", "on":
		return true
	case "0", "false", "FALSE", "no", "off":
		return false
	default:
		log.Printf("places: invalid %s, using default %v", key, def)
		return def
	}
}

func main() {
	cfg := loadConfig()

	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("places: open store: %v", err)
	}
	defer store.Close()

	if cfg.Seed {
		n, err := store.SeedIfEmpty()
		if err != nil {
			log.Fatalf("places: seed: %v", err)
		}
		if n > 0 {
			log.Printf("places: seeded %d starter places", n)
		}
		t, err := store.SeedTripsIfEmpty()
		if err != nil {
			log.Fatalf("places: seed trips: %v", err)
		}
		if t > 0 {
			log.Printf("places: seeded %d starter trips", t)
		}
	}

	srv := NewServer(cfg, store)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM so the SQLite writer closes cleanly
	// and doesn't leave a stale WAL behind.
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		log.Printf("places: shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	log.Printf("places: listening on %s (db %s)", cfg.Addr, store.Path())
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("places: serve: %v", err)
	}
}
