// Command echo is a tiny demo upstream: it reports who served the request and
// which trace it belongs to. It exists to exercise the gateway end to end.
//
//	GET /anything          JSON description of the request
//	GET /anything?status=503   respond with that status instead of 200
//	GET /anything?delay=250ms  sleep first (capped at 5s)
//	GET /health            200, used by the gateway's health checker
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"gateway/internal/telemetry"
)

const maxDelay = 5 * time.Second

type reply struct {
	Service      string `json:"service"`
	Pod          string `json:"pod"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	TraceID      string `json:"trace_id,omitempty"`
	Traceparent  string `json:"traceparent,omitempty"`
	ForwardedFor string `json:"x_forwarded_for,omitempty"`
}

func main() { os.Exit(run()) }

func run() int {
	listen := flag.String("listen", ":8080", "listen address")
	flag.Parse()

	pod, _ := os.Hostname()
	name := os.Getenv("ECHO_NAME")
	if name == "" {
		name = "echo"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry.Setup(ctx, "echo-"+name)
	if err != nil {
		log.Printf("telemetry: %v", err)
		return 1
	}
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(tctx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", otelhttp.NewHandler(handler(name, pod), "echo"))

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("echo %q listening on %s", name, *listen)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
			return 1
		}
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			log.Printf("shutdown: %v", err)
			return 1
		}
	}
	return 0
}

func handler(name, pod string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d, err := time.ParseDuration(r.URL.Query().Get("delay")); err == nil && d > 0 {
			select {
			case <-time.After(min(d, maxDelay)):
			case <-r.Context().Done():
				return
			}
		}

		status := http.StatusOK
		if s, err := strconv.Atoi(r.URL.Query().Get("status")); err == nil && s >= 200 && s <= 599 {
			status = s
		}

		rep := reply{
			Service:      name,
			Pod:          pod,
			Method:       r.Method,
			Path:         r.URL.Path,
			Traceparent:  r.Header.Get("Traceparent"),
			ForwardedFor: r.Header.Get("X-Forwarded-For"),
		}
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			rep.TraceID = sc.TraceID().String()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(rep)
	})
}
