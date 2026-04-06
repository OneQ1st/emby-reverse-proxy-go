package main

import (
	"context"
	"net"
	"net/http"
	"reflect"
	"testing"
)

func TestInferBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		headers map[string]string
		want    string
	}{
		{
			name: "trusted forwarded headers",
			host: "local.proxy:8080",
			headers: map[string]string{
				"X-Forwarded-Proto": "https",
				"X-Forwarded-Host":  "proxy.example.com",
			},
			want: "https://proxy.example.com",
		},
		{
			name: "fallback on invalid proto",
			host: "local.proxy:8080",
			headers: map[string]string{
				"X-Forwarded-Proto": "javascript",
				"X-Forwarded-Host":  "proxy.example.com",
			},
			want: "http://proxy.example.com",
		},
		{
			name: "fallback on invalid host",
			host: "local.proxy:8080",
			headers: map[string]string{
				"X-Forwarded-Proto": "https",
				"X-Forwarded-Host":  "bad/host",
			},
			want: "https://local.proxy:8080",
		},
		{
			name: "take first forwarded value",
			host: "local.proxy:8080",
			headers: map[string]string{
				"X-Forwarded-Proto": "HTTPS, http",
				"X-Forwarded-Host":  "Proxy.Example.com, ignored.example.com",
			},
			want: "https://proxy.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{Header: make(http.Header), Host: tt.host}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if got := inferBaseURL(req); got != tt.want {
				t.Fatalf("inferBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnproxyURLPreservesQuery(t *testing.T) {
	raw := "https://proxy.example.com/https/emby.example.com/443/web/index.html?api_key=abc123&userId=42"
	want := "https://emby.example.com/web/index.html?api_key=abc123&userId=42"

	if got := unproxyURL(raw); got != want {
		t.Fatalf("unproxyURL() = %q, want %q", got, want)
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		query   string
		want    *target
		wantErr bool
	}{
		{
			name:  "https with path and query",
			path:  "/https/emby.example.com/443/web/index.html",
			query: "api_key=abc",
			want: &target{
				Scheme: "https",
				Domain: "emby.example.com",
				Port:   443,
				Path:   "web/index.html",
				Query:  "api_key=abc",
			},
		},
		{
			name:  "http without trailing path",
			path:  "/http/emby.example.com/8096",
			query: "",
			want: &target{
				Scheme: "http",
				Domain: "emby.example.com",
				Port:   8096,
				Path:   "",
				Query:  "",
			},
		},
		{name: "invalid scheme", path: "/ftp/emby.example.com/21/file", wantErr: true},
		{name: "invalid port", path: "/https/emby.example.com/not-a-port/file", wantErr: true},
		{name: "missing domain", path: "/https//443/file", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTarget(tt.path, tt.query)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTarget(%q, %q) expected error", tt.path, tt.query)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTarget(%q, %q) unexpected error: %v", tt.path, tt.query, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseTarget(%q, %q) = %#v, want %#v", tt.path, tt.query, got, tt.want)
			}
		})
	}
}

func TestBuildTargetURL(t *testing.T) {
	tests := []struct {
		name string
		in   *target
		want string
	}{
		{
			name: "with query",
			in: &target{
				Scheme: "https",
				Domain: "emby.example.com",
				Port:   443,
				Path:   "web/index.html",
				Query:  "api_key=abc",
			},
			want: "https://emby.example.com:443/web/index.html?api_key=abc",
		},
		{
			name: "root path",
			in: &target{
				Scheme: "http",
				Domain: "emby.example.com",
				Port:   8096,
				Path:   "",
				Query:  "",
			},
			want: "http://emby.example.com:8096/",
		},
		{
			name: "ipv6 literal keeps single brackets",
			in: &target{
				Scheme: "https",
				Domain: "[2001:db8::1]",
				Port:   8096,
				Path:   "web/index.html",
			},
			want: "https://[2001:db8::1]:8096/web/index.html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildTargetURL(tt.in); got != tt.want {
				t.Fatalf("buildTargetURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTargetHostPort(t *testing.T) {
	tests := []struct {
		name string
		in   *target
		want string
	}{
		{
			name: "hostname default https port",
			in:   &target{Scheme: "https", Domain: "emby.example.com", Port: 443},
			want: "emby.example.com",
		},
		{
			name: "ipv6 default https port keeps brackets",
			in:   &target{Scheme: "https", Domain: "[2001:db8::1]", Port: 443},
			want: "[2001:db8::1]",
		},
		{
			name: "ipv6 literal with custom port keeps single brackets",
			in:   &target{Scheme: "https", Domain: "[2001:db8::1]", Port: 8096},
			want: "[2001:db8::1]:8096",
		},
		{
			name: "bare ipv6 with custom port gets brackets",
			in:   &target{Scheme: "https", Domain: "2001:db8::1", Port: 8096},
			want: "[2001:db8::1]:8096",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := targetHostPort(tt.in); got != tt.want {
				t.Fatalf("targetHostPort() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsDangerousIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{ip: "127.0.0.1", want: true},
		{ip: "10.0.0.1", want: true},
		{ip: "172.16.0.1", want: true},
		{ip: "192.168.1.10", want: true},
		{ip: "169.254.10.20", want: true},
		{ip: "0.0.0.0", want: true},
		{ip: "::1", want: true},
		{ip: "::", want: true},
		{ip: "fe80::1", want: true},
		{ip: "fc00::1", want: true},
		{ip: "8.8.8.8", want: false},
		{ip: "2606:4700:4700::1111", want: false},
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if got := isDangerousIP(ip); got != tt.want {
			t.Fatalf("isDangerousIP(%q) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

func TestValidateTargetSafety(t *testing.T) {
	ctx := context.Background()
	resolver := net.DefaultResolver

	blocked := []*target{
		{Scheme: "http", Domain: "localhost", Port: 8096},
		{Scheme: "http", Domain: "LOCALHOST.", Port: 8096},
		{Scheme: "http", Domain: "host.docker.internal", Port: 8096},
		{Scheme: "http", Domain: "127.0.0.1", Port: 8096},
		{Scheme: "http", Domain: "192.168.1.10", Port: 8096},
		{Scheme: "http", Domain: "::1", Port: 8096},
	}
	for _, tt := range blocked {
		if err := validateTargetSafety(ctx, resolver, tt); err == nil {
			t.Fatalf("validateTargetSafety(%q) expected blocked error", tt.Domain)
		}
	}

	allowed := &target{Scheme: "https", Domain: "8.8.8.8", Port: 443}
	if err := validateTargetSafety(ctx, resolver, allowed); err != nil {
		t.Fatalf("validateTargetSafety(%q) unexpected error: %v", allowed.Domain, err)
	}
}

func TestResolveSafeTarget(t *testing.T) {
	ctx := context.Background()
	target := &target{Scheme: "https", Domain: "8.8.8.8", Port: 443}

	rt, err := resolveSafeTarget(ctx, net.DefaultResolver, target)
	if err != nil {
		t.Fatalf("resolveSafeTarget(%q) unexpected error: %v", target.Domain, err)
	}
	addrs := rt.dialAddresses()
	if len(addrs) != 1 {
		t.Fatalf("resolved address count = %d, want 1", len(addrs))
	}
	if addrs[0] != "8.8.8.8:443" {
		t.Fatalf("dialAddresses()[0] = %q, want %q", addrs[0], "8.8.8.8:443")
	}
}

func TestParseBlockPrivateTargets(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "default empty true", raw: "", want: true},
		{name: "explicit true", raw: "true", want: true},
		{name: "numeric true", raw: "1", want: true},
		{name: "explicit false", raw: "false", want: false},
		{name: "numeric false", raw: "0", want: false},
		{name: "invalid keeps safe default", raw: "maybe", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseBlockPrivateTargets(tt.raw); got != tt.want {
				t.Fatalf("parseBlockPrivateTargets(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNewProxyHandlerBlockPrivateTargets(t *testing.T) {
	if handler := NewProxyHandler(true); handler.allowUnsafeDNS {
		t.Fatal("NewProxyHandler(true) should keep private target blocking enabled")
	}
	if handler := NewProxyHandler(false); !handler.allowUnsafeDNS {
		t.Fatal("NewProxyHandler(false) should disable private target blocking")
	}
}
