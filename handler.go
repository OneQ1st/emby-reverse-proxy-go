package main

import (
	"compress/gzip"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// copyBufPool reuses 32 KB buffers for io.CopyBuffer, avoiding per-request allocation.
var copyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// gzipWriterPool reuses gzip writers to avoid heavy init cost per response.
var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
		return w
	},
}

type ProxyHandler struct {
	client *http.Client
}

type target struct {
	Scheme string
	Domain string
	Port   int
	Path   string
	Query  string
}

func NewProxyHandler() *ProxyHandler {
	transport := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 20,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     120 * time.Second,
		TLSClientConfig:     &tls.Config{},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Must be true for a proxy — prevents Go from silently
		// decompressing upstream gzip in memory then sending uncompressed.
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
		WriteBufferSize:    64 * 1024,
		ReadBufferSize:     64 * 1024,
	}

	return &ProxyHandler{
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Proxy-Connection":    true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

var stripRequestHeaders = []string{
	"X-Real-Ip",
	"X-Forwarded-For",
	"X-Forwarded-Proto",
	"X-Forwarded-Host",
	"X-Forwarded-Port",
	"Forwarded",
	"Via",
}

var stripResponseHeaders = []string{
	"Server",
	"X-Powered-By",
	"X-Frame-Options",
	"X-Content-Type-Options",
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t, err := parseTarget(r.URL.Path, r.URL.RawQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	start := time.Now()
	baseURL := inferBaseURL(r)
	targetURL := buildTargetURL(t)
	media := looksLikeMedia(t.Path)

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	// Copy headers, skip hop-by-hop
	for k, vs := range r.Header {
		if hopByHopHeaders[k] {
			continue
		}
		outReq.Header[k] = vs
	}

	// Masquerade Host + TLS SNI
	hostVal := t.Domain
	if !isDefaultPort(t.Scheme, t.Port) {
		hostVal = net.JoinHostPort(t.Domain, strconv.Itoa(t.Port))
	}
	outReq.Host = hostVal
	outReq.Header.Set("Host", hostVal)

	for _, name := range stripRequestHeaders {
		outReq.Header.Del(name)
	}

	// Rewrite Referer & Origin to bypass hotlink protection
	if ref := outReq.Header.Get("Referer"); ref != "" {
		outReq.Header.Set("Referer", unproxyURL(ref))
	}
	if origin := outReq.Header.Get("Origin"); origin != "" {
		outReq.Header.Set("Origin", unproxyURL(origin))
	}

	if !media {
		outReq.Header.Set("Accept-Encoding", "identity")
	}

	resp, err := h.client.Do(outReq)
	if err != nil {
		log.Printf("[ERROR] %s %s/%s : %v", r.Method, t.Domain, t.Path, err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	rewriteResponseHeaders(resp, baseURL)

	// Copy response headers, skip hop-by-hop, strip revealing ones
	outHeader := w.Header()
	for k, vs := range resp.Header {
		if hopByHopHeaders[k] {
			continue
		}
		outHeader[k] = vs
	}
	for _, name := range stripResponseHeaders {
		outHeader.Del(name)
	}

	ct := resp.Header.Get("Content-Type")
	if shouldRewriteBody(ct) {
		h.serveRewrittenBody(w, r, resp, baseURL, t)
		log.Printf("[API] %d %s %s/%s (%s)", resp.StatusCode, r.Method, t.Domain, t.Path, time.Since(start))
	} else {
		written := h.serveStreamBody(w, resp)
		elapsed := time.Since(start)
		if media {
			log.Printf("[STREAM] %d %s %s/%s | %s | %s",
				resp.StatusCode, r.Method, t.Domain, t.Path,
				formatBytes(written), elapsed)
		} else {
			log.Printf("[PROXY] %d %s %s/%s | %s | %s",
				resp.StatusCode, r.Method, t.Domain, t.Path,
				formatBytes(written), elapsed)
		}
	}
}

func (h *ProxyHandler) serveRewrittenBody(w http.ResponseWriter, r *http.Request, resp *http.Response, baseURL string, t *target) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[ERROR] read body failed: %v", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	rewritten := rewriteBody(body, baseURL, t)

	// Compress rewritten text responses if client supports gzip.
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		w.WriteHeader(resp.StatusCode)

		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w)
		gz.Write(rewritten)
		gz.Close()
		gzipWriterPool.Put(gz)
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
		w.Header().Del("Content-Encoding")
		w.WriteHeader(resp.StatusCode)
		w.Write(rewritten)
	}
}

// serveStreamBody pipes upstream to client via io.CopyBuffer with pooled buffer.
// No intermediate buffering — on Linux this can trigger splice(2).
func (h *ProxyHandler) serveStreamBody(w http.ResponseWriter, resp *http.Response) int64 {
	w.WriteHeader(resp.StatusCode)
	bufp := copyBufPool.Get().(*[]byte)
	written, _ := io.CopyBuffer(w, resp.Body, *bufp)
	copyBufPool.Put(bufp)
	return written
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

var mediaExtensions = map[string]bool{
	"mp4": true, "mkv": true, "avi": true, "ts": true, "m3u8": true,
	"mp3": true, "flac": true, "aac": true, "ogg": true, "wav": true,
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "ico": true,
	"bmp": true, "tiff": true, "svg": true,
	"woff": true, "woff2": true, "ttf": true, "eot": true,
	"zip": true, "gz": true, "br": true, "zst": true,
	"m4v": true, "m4a": true, "webm": true, "mov": true, "wmv": true,
	"srt": true, "ass": true, "ssa": true, "vtt": true, "sub": true,
}

func looksLikeMedia(path string) bool {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "/videos/") ||
		strings.Contains(lower, "/audio/") ||
		strings.Contains(lower, "/images/") ||
		strings.Contains(lower, "/items/images") ||
		strings.Contains(lower, "/stream") {
		return true
	}
	dot := strings.LastIndexByte(path, '.')
	if dot >= 0 && dot < len(path)-1 {
		ext := strings.ToLower(path[dot+1:])
		if q := strings.IndexByte(ext, '?'); q >= 0 {
			ext = ext[:q]
		}
		return mediaExtensions[ext]
	}
	return false
}

func parseTarget(path, query string) (*target, error) {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return nil, fmt.Errorf("usage: /{scheme}/{domain}/{port}/{path}")
	}
	parts := strings.SplitN(trimmed, "/", 4)
	if len(parts) < 3 {
		return nil, fmt.Errorf("usage: /{scheme}/{domain}/{port}/{path}")
	}
	scheme := strings.ToLower(parts[0])
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https, got: %s", scheme)
	}
	domain := parts[1]
	if domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	port, err := strconv.Atoi(parts[2])
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port: %s", parts[2])
	}
	remaining := ""
	if len(parts) == 4 {
		remaining = parts[3]
	}
	return &target{Scheme: scheme, Domain: domain, Port: port, Path: remaining, Query: query}, nil
}

func buildTargetURL(t *target) string {
	var b strings.Builder
	b.Grow(len(t.Scheme) + 3 + len(t.Domain) + 6 + 1 + len(t.Path) + 1 + len(t.Query))
	b.WriteString(t.Scheme)
	b.WriteString("://")
	b.WriteString(t.Domain)
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(t.Port))
	b.WriteByte('/')
	b.WriteString(t.Path)
	if t.Query != "" {
		b.WriteByte('?')
		b.WriteString(t.Query)
	}
	return b.String()
}

func inferBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func isDefaultPort(scheme string, port int) bool {
	return (scheme == "https" && port == 443) || (scheme == "http" && port == 80)
}

func unproxyURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	t, err := parseTarget(parsed.Path, "")
	if err != nil {
		return raw
	}
	var b strings.Builder
	b.WriteString(t.Scheme)
	b.WriteString("://")
	b.WriteString(t.Domain)
	if !isDefaultPort(t.Scheme, t.Port) {
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(t.Port))
	}
	if t.Path != "" {
		b.WriteByte('/')
		b.WriteString(t.Path)
	} else {
		b.WriteByte('/')
	}
	return b.String()
}
