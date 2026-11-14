package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gateway/internal/proxy"
)

const shutdownTimeout = 30 * time.Second

func main() {
	listen := flag.String("listen", ":8080", "listen address")
	upstream := flag.String("upstream", "http://localhost:9000", "upstream URL")
	flag.Parse()

	target, err := url.Parse(*upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Fatalf("invalid -upstream %q", *upstream)
	}

	srv := proxy.NewServer(*listen, proxy.New(target, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("gateway listening on %s -> %s", *listen, target)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
			os.Exit(1)
		}
	}
}
