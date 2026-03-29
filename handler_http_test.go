package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newUnsafeTestProxyHandler() *ProxyHandler {
	h := NewProxyHandler()
	h.allowUnsafeDNS = true
	return h
}

func TestLooksLikeMedia(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/Videos/123/stream.mp4", want: true},
		{path: "/Items/Images/Primary", want: true},
		{path: "/audio/track.flac", want: true},
		{path: "/web/index.html", want: false},
		{path: "/emby/Items/1", want: false},
	}

	for _, tt := range tests {
		if got := looksLikeMedia(tt.path); got != tt.want {
			t.Fatalf("looksLikeMedia(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestNormalizeContentEncoding(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "gzip", want: "gzip"},
		{raw: "GZIP, br", want: "gzip"},
		{raw: " identity ", want: "identity"},
		{raw: "", want: ""},
	}

	for _, tt := range tests {
		if got := normalizeContentEncoding(tt.raw); got != tt.want {
			t.Fatalf("normalizeContentEncoding(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestServeHTTPBadTarget(t *testing.T) {
	handler := newUnsafeTestProxyHandler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestServeHTTPRewriteBodyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "identity" {
			t.Fatalf("Accept-Encoding = %q, want identity", got)
		}
		if got := r.Header.Get("Referer"); got != "https://upstream.example.com/app" {
			t.Fatalf("Referer = %q, want https://upstream.example.com/app", got)
		}
		if got := r.Header.Get("Origin"); got != "https://upstream.example.com/app" {
			t.Fatalf("Origin = %q, want https://upstream.example.com/app", got)
		}
		if got := r.Header.Get("X-Forwarded-For"); got != "" {
			t.Fatalf("X-Forwarded-For = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"url":"https://upstream.example.com/Items/1"}`))
	}))
	defer upstream.Close()

	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	handler := newUnsafeTestProxyHandler()
	req := httptest.NewRequest(http.MethodGet, "/http/127.0.0.1/"+strconv.Itoa(port)+"/Items", nil)
	req.Host = "proxy.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Referer", "https://proxy.example.com/https/upstream.example.com/443/app")
	req.Header.Set("Origin", "https://proxy.example.com/https/upstream.example.com/443/app")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	want := `{"url":"https://proxy.example.com/https/upstream.example.com/443/Items/1"}`
	if got := strings.TrimSpace(rr.Body.String()); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestServeHTTPStreamPathAndRange(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=0-15" {
			t.Fatalf("Range = %q, want bytes=0-15", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	handler := newUnsafeTestProxyHandler()
	req := httptest.NewRequest(http.MethodGet, "/http/127.0.0.1/"+strconv.Itoa(port)+"/Videos/1/stream.mp4", nil)
	req.Header.Set("Range", "bytes=0-15")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusPartialContent)
	}
	if !bytes.Equal(rr.Body.Bytes(), payload) {
		t.Fatal("stream body mismatch")
	}
}

func TestServeHTTPRewriteRedirectHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://upstream.example.com/web/index.html")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	handler := newUnsafeTestProxyHandler()
	req := httptest.NewRequest(http.MethodGet, "/http/127.0.0.1/"+strconv.Itoa(port)+"/redirect", nil)
	req.Host = "proxy.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	want := "https://proxy.example.com/https/upstream.example.com/443/web/index.html"
	if got := rr.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestServeHTTPCompressedResponseFallsBackToStream(t *testing.T) {
	payload := []byte("gzip-body")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	handler := newUnsafeTestProxyHandler()
	req := httptest.NewRequest(http.MethodGet, "/http/127.0.0.1/"+strconv.Itoa(port)+"/Items", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !bytes.Equal(rr.Body.Bytes(), payload) {
		t.Fatalf("body = %q, want %q", rr.Body.Bytes(), payload)
	}
}

func TestServeHTTPRemovesSensitiveResponseHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "upstream")
		w.Header().Set("X-Powered-By", "go")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	handler := newUnsafeTestProxyHandler()
	req := httptest.NewRequest(http.MethodGet, "/http/127.0.0.1/"+strconv.Itoa(port)+"/blob.bin", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	for _, name := range []string{"Server", "X-Powered-By", "X-Frame-Options", "X-Content-Type-Options"} {
		if got := rr.Header().Get(name); got != "" {
			t.Fatalf("%s = %q, want empty", name, got)
		}
	}
}

func TestServeHTTPBlocksDangerousTarget(t *testing.T) {
	handler := NewProxyHandler()
	req := httptest.NewRequest(http.MethodGet, "/http/127.0.0.1/8096/Items", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "blocked target host") {
		t.Fatalf("body = %q, want blocked target host message", rr.Body.String())
	}
}
