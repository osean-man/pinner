// Command pinner-server runs the pinner sync server.
//
// It stores only encrypted blobs and should sit behind a TLS-terminating proxy (see deploy/).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/osean-man/pinner/internal/buildinfo"
	"github.com/osean-man/pinner/internal/server"
)

// Set by release builds with -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version string
	commit  string
	date    string
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string) int {
	n, _ := strconv.Atoi(os.Getenv(key))
	return n
}

func main() {
	addr := flag.String("addr", envOr("PINNER_ADDR", "127.0.0.1:8080"), "listen address (env PINNER_ADDR)")
	dbPath := flag.String("db", envOr("PINNER_DB", "pinner-server.db"), "SQLite database path (env PINNER_DB)")
	maxUsers := flag.Int("max-users", envInt("PINNER_MAX_USERS"), "maximum registered users, 0 for unlimited (env PINNER_MAX_USERS)")
	maxRecords := flag.Int("max-records", envInt("PINNER_MAX_RECORDS"), "maximum records per user (env PINNER_MAX_RECORDS)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	info := buildinfo.Resolve(version, commit, date)
	if *showVersion {
		fmt.Println("pinner-server " + info.String())
		return
	}

	store, err := server.OpenStore(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.NewHandler(store, server.Options{MaxUsers: *maxUsers, MaxRecords: *maxRecords}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("pinner-server %s listening on %s (db %s)", info.Version, *addr, *dbPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
