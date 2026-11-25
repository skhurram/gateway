package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"gateway/internal/config"
	"gateway/internal/proxy"
	"gateway/internal/telemetry"
)

const (
	shutdownTimeout  = 30 * time.Second
	telemetryTimeout = 5 * time.Second
)

func main() { os.Exit(run()) }

func run() int {
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
		log.Printf("config: %v", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Tracing and metrics export only when OTEL_EXPORTER_OTLP_* is set.
	telemetryShutdown, err := telemetry.Setup(ctx, "gateway")
	if err != nil {
		log.Printf("telemetry: %v", err)
		return 1
	}
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), telemetryTimeout)
		defer cancel()
		if err := telemetryShutdown(tctx); err != nil {
			log.Printf("telemetry shutdown: %v", err)
		}
	}()

	manager := newRuntimeManager(ctx, otelhttp.NewTransport(proxy.NewTransport()))
	if err := manager.initialize(cfg); err != nil {
		log.Printf("runtime: %v", err)
		return 1
	}
	if err := proxy.RegisterHealthyUpstreamGauge(manager.router); err != nil {
		log.Printf("metrics: %v", err)
		return 1
	}

	// draining flips /readyz to 503 as soon as shutdown starts, so load
	// balancers stop sending new traffic while in-flight requests finish.
	var draining atomic.Bool
	ready := func() bool { return !draining.Load() && manager.ready() }

	// Probes sit outside the tracing handler so they don't create spans.
	traced := otelhttp.NewHandler(http.HandlerFunc(manager.serveHTTP), "gateway",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }))
	srv := proxy.NewServer(cfg.Listen, proxy.WithProbes(traced, ready))

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	watcherDone := make(chan error, 1)
	watching := *configPath != ""
	if watching {
		go func() {
			watcherDone <- config.Watch(ctx, *configPath, func() {
				if err := manager.reload(*configPath); err != nil {
					log.Printf("config reload failed: %v", err)
					return
				}
				cfg := manager.config()
				log.Printf("config reload succeeded: %d route(s)", len(cfg.Routes))
			})
		}()
	}
	active := manager.config()
	log.Printf("gateway listening on %s with %d route(s)", active.Listen, len(active.Routes))

	select {
	case err := <-errCh:
		stop()
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
			if watching {
				if watchErr := <-watcherDone; watchErr != nil {
					log.Printf("config watcher: %v", watchErr)
				}
			}
			manager.wait()
			return 1
		}
	case err := <-watcherDone:
		if err != nil {
			log.Printf("config watcher: %v", err)
			stop()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
				log.Printf("shutdown: %v", shutdownErr)
			}
			cancel()
			manager.wait()
			return 1
		}
	case <-ctx.Done():
		log.Print("shutting down")
		draining.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
			if watching {
				if watchErr := <-watcherDone; watchErr != nil {
					log.Printf("config watcher: %v", watchErr)
				}
			}
			manager.wait()
			return 1
		}
	}
	if watching {
		if watchErr := <-watcherDone; watchErr != nil {
			log.Printf("config watcher: %v", watchErr)
		}
	}
	manager.wait()
	return 0
}
