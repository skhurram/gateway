package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gateway/internal/config"
	"gateway/internal/health"
	"gateway/internal/proxy"
)

const shutdownTimeout = 30 * time.Second

func main() {
	configPath := flag.String("config", "", "path to YAML config (overrides -listen and -upstream)")
	listen := flag.String("listen", ":8080", "listen address (when -config is not set)")
	upstream := flag.String("upstream", "http://localhost:9000", "single upstream URL (when -config is not set)")
	flag.Parse()

	var (
		cfg *config.Config
		err error
	)
	if *configPath != "" {
		cfg, err = config.Load(*configPath)
	} else {
		cfg, err = config.Single(*listen, *upstream)
	}
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	router, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		log.Fatalf("router: %v", err)
	}
	srv := proxy.NewServer(cfg.Listen, router)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		(&health.Checker{
			Upstreams: router.Upstreams(),
			Path:      cfg.Health.Path,
			Interval:  cfg.Health.Interval,
			Timeout:   cfg.Health.Timeout,
		}).Run(ctx)
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("gateway listening on %s with %d route(s)", cfg.Listen, len(cfg.Routes))

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			stop()
			bg.Wait()
			log.Fatal(err)
		}
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
			bg.Wait()
			os.Exit(1)
		}
	}
	bg.Wait()
}
