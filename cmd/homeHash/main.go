// homeHash — Lightweight local-only monitoring dashboard for home Bitcoin miners.
//
// Usage:
//
//	go run cmd/homeHash/main.go
//	./homeHash -port 8080
//
// Environment variables:
//
//	HOMEHASH_PORT     HTTP server port (default: 8080)
//	HOMEHASH_DB       SQLite database path (default: ./homehash.db)
//	HOMEHASH_INTERVAL Polling interval, seconds or duration (default: 60s)
//	HOMEHASH_MINER    Seed miner IPs (or ip:port), comma-separated
//	HOMEHASH_DISCOVERY  Enable network discovery (default: true)
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gregweir/homeHash/internal/alerts"
	"github.com/gregweir/homeHash/internal/api"
	"github.com/gregweir/homeHash/internal/config"
	"github.com/gregweir/homeHash/internal/discovery"
	"github.com/gregweir/homeHash/internal/drivers"
	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/poller"
	"github.com/gregweir/homeHash/internal/store"
	"github.com/gregweir/homeHash/web"
)

// The dashboard ships inside the binary: local-only, single file, no CDN.
var webFS = web.UIFS()

// version is injected at build time by the release workflow:
// go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

// parseArgs handles the command line (--version). It returns true when the
// caller should print the version and exit; startup output goes to stdout.
func parseArgs(args []string, out *bytes.Buffer) (bool, error) {
	fs := flag.NewFlagSet("homeHash", flag.ContinueOnError)
	fs.SetOutput(out)
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	return *showVersion, nil
}

func main() {
	var flagBuf bytes.Buffer
	showVersion, err := parseArgs(os.Args[1:], &flagBuf)
	switch {
	case errors.Is(err, flag.ErrHelp): // -h: usage text is in flagBuf
		fmt.Print(flagBuf.String())
		return
	case err != nil:
		os.Exit(2)
	}
	if showVersion {
		fmt.Println(version)
		return
	}

	// Load configuration
	cfg := config.Default()
	log.Printf("homeHash %s starting on port %d", version, cfg.Port)
	log.Printf("Database: %s", cfg.DatabasePath)
	log.Printf("Poll interval: %v", cfg.PollInterval)

	// Initialize driver registry
	registry := drivers.New()
	for _, d := range registry.All() {
		log.Printf("Registered driver %s: %v", d.Name(), d.SupportedModels())
	}

	// Initialize store
	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer st.Close()

	// Optional: seed miners by IP. Discovery adds the rest automatically.
	// HOMEHASH_MINER=192.168.1.45,192.168.1.50 (comma-separated)
	for _, ip := range cfg.Miners {
		miner := base.Miner{
			ID:     "bitaxe-" + strings.NewReplacer(".", "-", ":", "-").Replace(ip),
			IP:     ip,
			Model:  "Bitaxe (auto)",
			Driver: "bitaxe",
		}
		if err := st.UpsertMiner(miner); err != nil {
			log.Fatalf("Failed to seed miner: %v", err)
		}
		log.Printf("Seeded miner at %s", ip)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// Start polling
	go poller.New(st, registry, cfg.PollInterval).Run(ctx)

	// Periodic network discovery of new miners
	if cfg.DiscoveryEnabled {
		go runDiscoveryLoop(ctx, st, cfg.DiscoveryInterval)
	} else {
		log.Printf("Discovery disabled")
	}

	// Alerting loop (log by default, Discord webhook when configured)
	if cfg.AlertEnabled {
		var notifier alerts.Notifier = alerts.LogNotifier{}
		if cfg.AlertWebhookURL != "" {
			notifier = alerts.NewDiscordWebhook(cfg.AlertWebhookURL, 5*time.Second)
		}
		evaluator := alerts.NewEvaluator(alerts.Config{
			TempWarn:     cfg.TempWarningC,
			TempCritical: cfg.TempCriticalC,
			OfflineAfter: cfg.OfflineAfter,
			Cooldown:     cfg.AlertCooldown,
		})
		go alerts.Run(ctx, st, evaluator, notifier, cfg.AlertInterval)
	}

	// HTTP surface
	mux := http.NewServeMux()

	apiHandler := api.New(st)
	mux.Handle("/api/", apiHandler)

	webRoot, err := fs.Sub(webFS, ".")
	if err != nil {
		log.Fatalf("embedded web FS: %v", err)
	}
	static := http.FileServer(http.FS(webRoot))
	mux.Handle("/static/", static)
	// Chromium asks for /favicon.ico no matter what the page links; give it the
	// embedded SVG so the console stays clean.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		f, err := webRoot.Open("static/favicon.svg")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "image/svg+xml")
		if _, err := io.Copy(w, f); err != nil {
			log.Printf("serve favicon: %v", err)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		f, err := webRoot.Open("index.html")
		if err != nil {
			http.Error(w, "dashboard missing", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := io.Copy(w, f); err != nil {
			log.Printf("serve index: %v", err)
		}
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("HTTP server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// runDiscoveryLoop periodically scans for new miners on the local network and
// adds the unknown ones to the store. Known miners keep their poller-enriched
// identity — discovery never overwrites it.
func runDiscoveryLoop(ctx context.Context, st *store.Store, interval time.Duration) {
	disco := discovery.New()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	scanOnce := func() {
		mgs, err := disco.Discover(ctx)
		if err != nil {
			log.Printf("discovery: %v", err)
			return
		}
		for _, m := range mgs {
			known, err := st.GetMiner(m.ID)
			if err != nil {
				log.Printf("discovery: lookup %s: %v", m.ID, err)
				continue
			}
			if err := st.UpsertMinerIfNew(m); err != nil {
				log.Printf("discovery: add %s: %v", m.ID, err)
				continue
			}
			if known == nil && m.IP != "" {
				log.Printf("discovery: found new miner %s (%s) at %s", m.ID, m.Model, m.IP)
			}
		}
	}

	scanOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanOnce()
		}
	}
}
