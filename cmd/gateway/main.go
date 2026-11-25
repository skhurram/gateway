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
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"gateway/internal/config"
	"gateway/internal/health"
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

	// The transport injects W3C trace context into upstream requests.
	router, err := proxy.NewRouter(cfg, otelhttp.NewTransport(proxy.NewTransport()))
	if err != nil {
		log.Printf("router: %v", err)
		return 1
	}

	checker := &health.Checker{
		Upstreams: router.Upstreams(),
		Path:      cfg.Health.Path,
		Interval:  cfg.Health.Interval,
		Timeout:   cfg.Health.Timeout,
	}
	// Probe once before serving so /readyz reflects real upstream state from
	// the first request instead of the optimistic initial value.
	checker.CheckOnce(ctx)

	// draining flips /readyz to 503 as soon as shutdown starts, so load
	// balancers stop sending new traffic while in-flight requests finish.
	var draining atomic.Bool
	ready := func() bool { return !draining.Load() && router.Ready() }

	// Probes sit outside the tracing handler so they don't create spans.
	traced := otelhttp.NewHandler(router, "gateway",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }))
	srv := proxy.NewServer(cfg.Listen, proxy.WithProbes(traced, ready))

	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		checker.Run(ctx)
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("gateway listening on %s with %d route(s)", cfg.Listen, len(cfg.Routes))

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
			stop()
			bg.Wait()
			return 1
		}
	case <-ctx.Done():
		log.Print("shutting down")
		draining.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
			bg.Wait()
			return 1
		}
	}
	bg.Wait()
	return 0
}
