package httpguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"localhost:5484":      "localhost",
		"127.0.0.1:5484":      "127.0.0.1",
		"[::1]:5484":          "::1",
		"[::1]":               "::1",
		"evil.example.com":    "evil.example.com",
		"":                    "",
		"::1":                 "", // bare unbracketed IPv6 with colons is ambiguous
	}
	for in, want := range cases {
		if got := HostOnly(in); got != want {
			t.Errorf("HostOnly(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	if !IsLoopbackHost("localhost") || !IsLoopbackHost("127.0.0.1") || !IsLoopbackHost("::1") {
		t.Error("loopback names should pass")
	}
	if !IsLoopbackHost("127.0.0.42") {
		t.Error("127.0.0.0/8 should pass")
	}
	if IsLoopbackHost("8.8.8.8") {
		t.Error("public IP should fail")
	}
	if IsLoopbackHost("evil.example.com") {
		t.Error("hostname should fail")
	}
}

func TestMiddlewareAllowsLoopback(t *testing.T) {
	h := Middleware(map[string]struct{}{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, host := range []string{"localhost:5484", "127.0.0.1:5484", "[::1]:5484"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("Host %q -> %d, want 200", host, w.Code)
		}
	}
}

func TestMiddlewareBlocksUnknown(t *testing.T) {
	h := Middleware(map[string]struct{}{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "evil.example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("code=%d, want 403", w.Code)
	}
}

func TestMiddlewareAllowsConfigured(t *testing.T) {
	allowed := BuildAllowedHosts("ocr.internal", "")
	h := Middleware(allowed, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "ocr.internal:5484"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("code=%d, want 200", w.Code)
	}
}
