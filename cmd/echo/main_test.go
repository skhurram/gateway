package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandlerDescribesRequest(t *testing.T) {
	h := handler("users", "pod-1")
	req := httptest.NewRequest(http.MethodGet, "/api/users/7", nil)
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got reply
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Service != "users" || got.Pod != "pod-1" || got.Path != "/api/users/7" || got.ForwardedFor != "10.1.2.3" {
		t.Errorf("reply = %+v", got)
	}
}

func TestHandlerStatusAndDelay(t *testing.T) {
	h := handler("x", "p")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?status=503", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?status=999", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("out-of-range status = %d, want 200", rec.Code)
	}

	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?delay=50ms", nil))
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("delay = %v, want >= 50ms", d)
	}
}
