package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Short server timeouts so the tests run quickly; the behavior is the same
// at the real 30s read and 60s write timeouts.
var testTimeouts = serverTimeouts{
	readHeader: time.Second,
	read:       200 * time.Millisecond,
	write:      200 * time.Millisecond,
	idle:       time.Second,
}

func startGateway(t *testing.T, upstream http.Handler) string {
	t.Helper()
	return startGatewayWrapped(t, upstream, func(h http.Handler) http.Handler { return h })
}

func startGatewayWrapped(t *testing.T, upstream http.Handler, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	target, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer("", wrap(New(target, nil)), testTimeouts)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// A server-sent event stream outlives the server's read and write timeouts,
// also behind the otelhttp handler the gateway uses in production.
func TestEventStreamOutlivesServerTimeouts(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		testEventStream(t, func(h http.Handler) http.Handler { return h })
	})
	t.Run("otelhttp", func(t *testing.T) {
		testEventStream(t, func(h http.Handler) http.Handler { return otelhttp.NewHandler(h, "gateway") })
	})
}

func testEventStream(t *testing.T, wrap func(http.Handler) http.Handler) {
	const events = 12 // 12 * 50ms = 600ms, three times the timeouts
	addr := startGatewayWrapped(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range events {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			_ = http.NewResponseController(w).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}), wrap)
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	got := strings.Count(string(body), "data: ")
	if err != nil || got != events {
		t.Fatalf("received %d of %d events (err %v)", got, events, err)
	}
}

// An upgraded connection (WebSocket) stays usable past the server timeouts.
func TestUpgradedConnectionOutlivesServerTimeouts(t *testing.T) {
	addr := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = brw.Flush()
		for {
			line, err := brw.ReadString('\n')
			if err != nil {
				return
			}
			_, _ = brw.WriteString(line)
			_ = brw.Flush()
		}
	}))

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: gw\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	for i := range 3 {
		time.Sleep(300 * time.Millisecond) // longer than both server timeouts
		msg := fmt.Sprintf("ping %d\n", i)
		if _, err := io.WriteString(conn, msg); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, err := br.ReadString('\n')
		if err != nil || got != msg {
			t.Fatalf("echo %d = %q, %v; want %q", i, got, err, msg)
		}
	}
}

// Ordinary requests keep the write timeout: a response that takes longer
// than it is cut off.
func TestOrdinaryRequestKeepsWriteTimeout(t *testing.T) {
	addr := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "start\n")
		_ = http.NewResponseController(w).Flush()
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, "end\n")
	}))
	resp, err := http.Get("http://" + addr + "/slow")
	if err != nil {
		return // cut off before headers: also acceptable
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err == nil && strings.Contains(string(body), "end") {
		t.Fatalf("slow ordinary response completed (%q); the write timeout no longer applies", body)
	}
}
