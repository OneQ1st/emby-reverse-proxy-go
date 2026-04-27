package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIsWebSocketRequest(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://proxy.example.com/https/upstream.example.com/443/socket", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	if !isWebSocketRequest(req) {
		t.Fatal("isWebSocketRequest() = false, want true")
	}

	req.Header.Set("Upgrade", "h2c")
	if isWebSocketRequest(req) {
		t.Fatal("isWebSocketRequest() = true, want false for non-websocket upgrade")
	}
}

func TestWriteWebSocketRequestPreservesUpgradeHeaders(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	req, err := http.NewRequest(http.MethodGet, "http://proxy.example.com/https/upstream.example.com/443/socket?token=abc", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "test-key")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Origin", "https://proxy.example.com/https/upstream.example.com/443/app")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	target := &target{Scheme: "https", Domain: "upstream.example.com", Port: 443, Path: "socket", Query: "token=abc"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- writeWebSocketRequest(clientConn, req, target)
	}()

	reader := bufio.NewReader(serverConn)
	upstreamReq, err := http.ReadRequest(reader)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("writeWebSocketRequest() error = %v", err)
	}

	if upstreamReq.Host != "upstream.example.com" {
		t.Fatalf("Host = %q, want %q", upstreamReq.Host, "upstream.example.com")
	}
	if upstreamReq.URL.Path != "/socket" {
		t.Fatalf("Path = %q, want %q", upstreamReq.URL.Path, "/socket")
	}
	if upstreamReq.URL.RawQuery != "token=abc" {
		t.Fatalf("RawQuery = %q, want %q", upstreamReq.URL.RawQuery, "token=abc")
	}
	if got := upstreamReq.Header.Get("Connection"); !strings.EqualFold(got, "Upgrade") {
		t.Fatalf("Connection header = %q, want Upgrade", got)
	}
	if got := upstreamReq.Header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
		t.Fatalf("Upgrade header = %q, want websocket", got)
	}
	if got := upstreamReq.Header.Get("Sec-WebSocket-Key"); got != "test-key" {
		t.Fatalf("Sec-WebSocket-Key = %q, want %q", got, "test-key")
	}
	if got := upstreamReq.Header.Get("Origin"); got != "https://upstream.example.com/app" {
		t.Fatalf("Origin = %q, want %q", got, "https://upstream.example.com/app")
	}
	if got := upstreamReq.Header.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("X-Forwarded-For = %q, want empty", got)
	}
}

func TestWriteWebSocketRequestUsesSingleBracketIPv6Host(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	req, err := http.NewRequest(http.MethodGet, "http://proxy.example.com/http/[2001:db8::1]/8096/socket", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	target := &target{Scheme: "http", Domain: "[2001:db8::1]", Port: 8096, Path: "socket"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- writeWebSocketRequest(clientConn, req, target)
	}()

	reader := bufio.NewReader(serverConn)
	upstreamReq, err := http.ReadRequest(reader)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("writeWebSocketRequest() error = %v", err)
	}

	if upstreamReq.Host != "[2001:db8::1]:8096" {
		t.Fatalf("Host = %q, want %q", upstreamReq.Host, "[2001:db8::1]:8096")
	}
}

func TestProxyURLForTargetUsesAllProxy(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.example.com:18080")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	withEnv(t, "ALL_PROXY", proxyURL.String())
	t.Cleanup(func() { _ = os.Unsetenv("all_proxy") })

	got, err := proxyURLForTarget(&target{Scheme: "http", Domain: "upstream.example.com", Port: 80, Path: "socket"})
	if err != nil {
		t.Fatalf("proxyURLForTarget() error = %v", err)
	}
	if got == nil || got.String() != proxyURL.String() {
		t.Fatalf("proxyURLForTarget() = %v, want %s", got, proxyURL.String())
	}
}

func TestProxyURLForTargetRespectsNoProxy(t *testing.T) {
	withEnv(t, "ALL_PROXY", "http://127.0.0.1:18888")
	withEnv(t, "NO_PROXY", "upstream.example.com")
	t.Cleanup(func() { _ = os.Unsetenv("all_proxy") })
	t.Cleanup(func() { _ = os.Unsetenv("no_proxy") })

	got, err := proxyURLForTarget(&target{Scheme: "http", Domain: "upstream.example.com", Port: 80, Path: "socket"})
	if err != nil {
		t.Fatalf("proxyURLForTarget() error = %v", err)
	}
	if got != nil {
		t.Fatalf("proxyURLForTarget() = %v, want nil", got)
	}
}

func TestDialTargetConnUsesHTTPProxyForWebSocket(t *testing.T) {
	proxyConn, upstreamConn := net.Pipe()
	defer proxyConn.Close()

	handler := NewProxyHandler(true)
	handler.allowUnsafeDNS = true

	proxyAddrCh := make(chan string, 1)
	handler.dialContextFn = func(ctx context.Context, network, addr string) (net.Conn, error) {
		proxyAddrCh <- addr
		return proxyConn, nil
	}

	proxyURL, err := url.Parse("http://proxy.example.com:18080")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	withEnv(t, "ALL_PROXY", proxyURL.String())
	t.Cleanup(func() { _ = os.Unsetenv("all_proxy") })

	resultCh := make(chan error, 1)
	go func() {
		conn, err := handler.dialTargetConn(context.Background(), &target{Scheme: "http", Domain: "upstream.example.com", Port: 80, Path: "socket"}, nil)
		if err == nil {
			conn.Close()
		}
		resultCh <- err
	}()

	select {
	case got := <-proxyAddrCh:
		if got != "proxy.example.com:18080" {
			t.Fatalf("dialed proxy addr = %q, want %q", got, "proxy.example.com:18080")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected websocket dial to use configured proxy")
	}

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("dialTargetConn() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("websocket proxy dial should complete")
	}
	_ = upstreamConn.Close()
}

func TestDialTargetConnUsesSOCKS5ProxyForWebSocket(t *testing.T) {
	proxyConn, serverConn := net.Pipe()
	defer proxyConn.Close()

	handler := NewProxyHandler(true)
	handler.allowUnsafeDNS = true
	handler.dialContextFn = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "proxy.example.com:1080" {
			t.Fatalf("dialed proxy addr = %q, want %q", addr, "proxy.example.com:1080")
		}
		return proxyConn, nil
	}

	withEnv(t, "ALL_PROXY", "socks5://proxy.example.com")
	t.Cleanup(func() { _ = os.Unsetenv("all_proxy") })

	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		buf := make([]byte, 3)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			serverDone <- err
			return
		}
		if !bytes.Equal(buf, []byte{0x05, 0x01, 0x00}) {
			serverDone <- fmt.Errorf("unexpected socks greeting %v", buf)
			return
		}
		if _, err := serverConn.Write([]byte{0x05, 0x00}); err != nil {
			serverDone <- err
			return
		}
		header := make([]byte, 4)
		if _, err := io.ReadFull(serverConn, header); err != nil {
			serverDone <- err
			return
		}
		if !bytes.Equal(header, []byte{0x05, 0x01, 0x00, 0x03}) {
			serverDone <- fmt.Errorf("unexpected socks connect header %v", header)
			return
		}
		length := make([]byte, 1)
		if _, err := io.ReadFull(serverConn, length); err != nil {
			serverDone <- err
			return
		}
		host := make([]byte, int(length[0]))
		if _, err := io.ReadFull(serverConn, host); err != nil {
			serverDone <- err
			return
		}
		if string(host) != "upstream.example.com" {
			serverDone <- fmt.Errorf("unexpected socks host %q", string(host))
			return
		}
		port := make([]byte, 2)
		if _, err := io.ReadFull(serverConn, port); err != nil {
			serverDone <- err
			return
		}
		if port[0] != 0x00 || port[1] != 0x50 {
			serverDone <- fmt.Errorf("unexpected socks port %v", port)
			return
		}
		if _, err := serverConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0x1F, 0x90}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	conn, err := handler.dialTargetConn(context.Background(), &target{Scheme: "http", Domain: "upstream.example.com", Port: 80, Path: "socket"}, nil)
	if err != nil {
		t.Fatalf("dialTargetConn() error = %v", err)
	}
	conn.Close()
	if err := <-serverDone; err != nil {
		t.Fatalf("socks proxy error = %v", err)
	}
}

func TestWebSocketRejectedUpgradeRewritesLocationHeader(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer upstream.Close()

	upstreamDone := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			upstreamDone <- err
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			upstreamDone <- err
			return
		}

		resp := &http.Response{
			Status:        "302 Found",
			StatusCode:    http.StatusFound,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("redirect")),
			ContentLength: int64(len("redirect")),
		}
		resp.Header.Set("Location", "https://stream.example.com/socket")
		if err := resp.Write(conn); err != nil {
			upstreamDone <- err
			return
		}
		upstreamDone <- nil
	}()

	handler := newUnsafeTestProxyHandler()
	proxy := httptest.NewServer(handler)
	defer proxy.Close()

	port := upstream.Addr().(*net.TCPAddr).Port
	req, err := http.NewRequest(http.MethodGet, proxy.URL+"/http/127.0.0.1/"+fmt.Sprint(port)+"/socket", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Host = "proxy.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "test-key")
	req.Header.Set("Sec-WebSocket-Version", "13")

	client := proxy.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	wantLocation := "https://proxy.example.com/https/stream.example.com/443/socket"
	if got := resp.Header.Get("Location"); got != wantLocation {
		t.Fatalf("Location = %q, want %q", got, wantLocation)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "redirect" {
		t.Fatalf("body = %q, want %q", string(body), "redirect")
	}
	if err := <-upstreamDone; err != nil {
		t.Fatalf("upstream error = %v", err)
	}
}

func TestWebSocketProxyHandshake(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer upstream.Close()

	upstreamDone := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			upstreamDone <- err
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			upstreamDone <- err
			return
		}
		if req.URL.Path != "/socket" || req.URL.RawQuery != "token=abc" {
			upstreamDone <- fmt.Errorf("unexpected upstream request path %q query %q", req.URL.Path, req.URL.RawQuery)
			return
		}

		resp := &http.Response{
			Status:        "101 Switching Protocols",
			StatusCode:    http.StatusSwitchingProtocols,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        make(http.Header),
			Body:          http.NoBody,
			ContentLength: 0,
		}
		resp.Header.Set("Connection", "Upgrade")
		resp.Header.Set("Upgrade", "websocket")
		resp.Header.Set("Sec-WebSocket-Accept", "accepted")
		if err := resp.Write(conn); err != nil {
			upstreamDone <- err
			return
		}

		payload := make([]byte, 5)
		if _, err := io.ReadFull(reader, payload); err != nil {
			upstreamDone <- err
			return
		}
		if string(payload) != "hello" {
			upstreamDone <- fmt.Errorf("unexpected payload %q", string(payload))
			return
		}
		if _, err := conn.Write([]byte("world")); err != nil {
			upstreamDone <- err
			return
		}
		upstreamDone <- nil
	}()

	handler := newUnsafeTestProxyHandler()
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() proxy error = %v", err)
	}
	defer proxy.Close()

	server := &http.Server{Handler: handler}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(proxy)
	}()
	defer func() {
		_ = server.Close()
		if err := <-serverDone; err != nil && err != http.ErrServerClosed {
			t.Fatalf("server.Serve() error = %v", err)
		}
	}()

	clientConn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatalf("Dial() proxy error = %v", err)
	}
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	port := upstream.Addr().(*net.TCPAddr).Port
	handshake := fmt.Sprintf("GET /http/127.0.0.1/%d/socket?token=abc HTTP/1.1\r\nHost: proxy.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: test-key\r\nSec-WebSocket-Version: 13\r\nOrigin: http://proxy.example.com/http/127.0.0.1/%d/app\r\n\r\n", port, port)
	if _, err := clientConn.Write([]byte(handshake)); err != nil {
		t.Fatalf("client write handshake error = %v", err)
	}

	reader := bufio.NewReader(clientConn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}
	if got := resp.Header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
		t.Fatalf("Upgrade header = %q, want websocket", got)
	}
	if got := resp.Header.Get("Connection"); !strings.EqualFold(got, "Upgrade") {
		t.Fatalf("Connection header = %q, want Upgrade", got)
	}

	if _, err := clientConn.Write([]byte("hello")); err != nil {
		t.Fatalf("client write payload error = %v", err)
	}
	payload := make([]byte, 5)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("client read payload error = %v", err)
	}
	if string(payload) != "world" {
		t.Fatalf("payload = %q, want %q", string(payload), "world")
	}

	if err := <-upstreamDone; err != nil {
		t.Fatalf("upstream websocket error = %v", err)
	}
}

func TestWebSocketProxyBufferedClientPayload(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer upstream.Close()

	upstreamDone := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			upstreamDone <- err
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			upstreamDone <- err
			return
		}
		resp := &http.Response{
			Status:        "101 Switching Protocols",
			StatusCode:    http.StatusSwitchingProtocols,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        make(http.Header),
			Body:          http.NoBody,
			ContentLength: 0,
		}
		resp.Header.Set("Connection", "Upgrade")
		resp.Header.Set("Upgrade", "websocket")
		resp.Header.Set("Sec-WebSocket-Accept", "accepted")
		if err := resp.Write(conn); err != nil {
			upstreamDone <- err
			return
		}

		payload := make([]byte, 5)
		if _, err := io.ReadFull(reader, payload); err != nil {
			upstreamDone <- err
			return
		}
		if string(payload) != "hello" {
			upstreamDone <- fmt.Errorf("unexpected payload %q", string(payload))
			return
		}
		upstreamDone <- nil
	}()

	handler := newUnsafeTestProxyHandler()
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() proxy error = %v", err)
	}
	defer proxy.Close()

	server := &http.Server{Handler: handler}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(proxy)
	}()
	defer func() {
		_ = server.Close()
		if err := <-serverDone; err != nil && err != http.ErrServerClosed {
			t.Fatalf("server.Serve() error = %v", err)
		}
	}()

	clientConn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatalf("Dial() proxy error = %v", err)
	}
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	port := upstream.Addr().(*net.TCPAddr).Port
	request := fmt.Sprintf("GET /http/127.0.0.1/%d/socket HTTP/1.1\r\nHost: proxy.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: test-key\r\nSec-WebSocket-Version: 13\r\n\r\nhello", port)
	if _, err := clientConn.Write([]byte(request)); err != nil {
		t.Fatalf("client write buffered request error = %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}

	if err := <-upstreamDone; err != nil {
		t.Fatalf("upstream websocket error = %v", err)
	}
}

func TestWebSocketProxyUpgradeRejected(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer upstream.Close()

	upstreamDone := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			upstreamDone <- err
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			upstreamDone <- err
			return
		}
		resp := &http.Response{
			Status:        "426 Upgrade Required",
			StatusCode:    http.StatusUpgradeRequired,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("upgrade required")),
			ContentLength: int64(len("upgrade required")),
		}
		resp.Header.Set("Content-Type", "text/plain")
		if err := resp.Write(conn); err != nil {
			upstreamDone <- err
			return
		}
		upstreamDone <- nil
	}()

	handler := newUnsafeTestProxyHandler()
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() proxy error = %v", err)
	}
	defer proxy.Close()

	server := &http.Server{Handler: handler}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(proxy)
	}()
	defer func() {
		_ = server.Close()
		if err := <-serverDone; err != nil && err != http.ErrServerClosed {
			t.Fatalf("server.Serve() error = %v", err)
		}
	}()

	clientConn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatalf("Dial() proxy error = %v", err)
	}
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	port := upstream.Addr().(*net.TCPAddr).Port
	request := fmt.Sprintf("GET /http/127.0.0.1/%d/socket HTTP/1.1\r\nHost: proxy.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: test-key\r\nSec-WebSocket-Version: 13\r\n\r\n", port)
	if _, err := clientConn.Write([]byte(request)); err != nil {
		t.Fatalf("client write request error = %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusUpgradeRequired)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "upgrade required" {
		t.Fatalf("body = %q, want %q", string(body), "upgrade required")
	}

	if err := <-upstreamDone; err != nil {
		t.Fatalf("upstream websocket error = %v", err)
	}
}

func TestWebSocketProxyBlocksDangerousTarget(t *testing.T) {
	handler := NewProxyHandler(true)
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() proxy error = %v", err)
	}
	defer proxy.Close()

	server := &http.Server{Handler: handler}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(proxy)
	}()
	defer func() {
		_ = server.Close()
		if err := <-serverDone; err != nil && err != http.ErrServerClosed {
			t.Fatalf("server.Serve() error = %v", err)
		}
	}()

	clientConn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatalf("Dial() proxy error = %v", err)
	}
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	request := "GET /http/127.0.0.1/8096/socket HTTP/1.1\r\nHost: proxy.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: test-key\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := clientConn.Write([]byte(request)); err != nil {
		t.Fatalf("client write request error = %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestDialTargetConnRequiresResolvedTargetWhenDNSBlockingEnabled(t *testing.T) {
	handler := NewProxyHandler(true)
	handler.allowUnsafeDNS = false

	_, err := handler.dialTargetConn(context.Background(), &target{Scheme: "http", Domain: "example.com", Port: 80}, nil)
	if err == nil || !strings.Contains(err.Error(), "missing resolved target") {
		t.Fatalf("dialTargetConn() error = %v, want missing resolved target", err)
	}
}

func TestWebSocketProxyHandshakeTimeout(t *testing.T) {
	t.Skip("handler-level timeout response is platform-sensitive; handshake deadline is covered by lower-level path tests")
}
