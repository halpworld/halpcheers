package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/halpworld/halpcheers/server/internal/config"
	internalhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to TOML configuration file")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	// Start background SIGHUP listener for reloadable config
	cfg.ListenSIGHUP(ctx)

	// 2. Initialize storage layer (runs 0001_init.sql migrations, starts single writer)
	st, err := store.Open(ctx, cfg.Startup.DBPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer st.Close()

	// 3. Initialize observability (Prometheus registry & metrics)
	metrics := obs.NewMetrics()

	// -------------------------------------------------------------------------
	// Feature Service Wiring
	// Wire all Wave 1 implementations into RouterDeps and start background pumps.
	// -------------------------------------------------------------------------
	routerDeps, assembly, err := internalhttp.AssembleDependencies(ctx, cfg, st, metrics)
	if err != nil {
		log.Fatalf("failed to assemble dependencies: %v", err)
	}
	defer assembly.Close()
	assembly.Start(ctx)

	// 4. Construct router (router.go is frozen after Wave 0)
	router := internalhttp.NewRouter(routerDeps)

	// 5. Start HTTP server
	srv := &http.Server{
		Addr:    cfg.Startup.HTTPAddr,
		Handler: router,
	}

	go func() {
		log.Printf("halpd listening on %s (WAL SQLite at %s)", cfg.Startup.HTTPAddr, cfg.Startup.DBPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP listener failed: %v", err)
		}
	}()

	// 6. Graceful shutdown on SIGTERM / SIGINT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("halpd received signal %s, initiating graceful drain...", sig)

	drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.Startup.ShutdownDrainTimeout)
	defer drainCancel()

	if err := srv.Shutdown(drainCtx); err != nil {
		log.Printf("server drain error: %v", err)
	} else {
		log.Printf("server drained successfully within timeout")
	}

	log.Println("halpd stopped")
}
