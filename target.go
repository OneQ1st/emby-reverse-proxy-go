package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type target struct {
	Scheme string
	Domain string
	Port   int
	Path   string
	Query  string
}

type resolvedTarget struct {
	dialAddrs []string
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
	host := targetURLHost(t.Domain)
	var b strings.Builder
	b.Grow(len(t.Scheme) + 3 + len(host) + 6 + 1 + len(t.Path) + 1 + len(t.Query))
	b.WriteString(t.Scheme)
	b.WriteString("://")
	b.WriteString(host)
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(t.Port))
	b.WriteString(targetRequestPath(t))
	if t.Query != "" {
		b.WriteByte('?')
		b.WriteString(t.Query)
	}
	return b.String()
}

func trimIPv6LiteralBrackets(host string) string {
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		inner := host[1 : len(host)-1]
		if ip := net.ParseIP(inner); ip != nil && ip.To4() == nil {
			return ip.String()
		}
	}
	return host
}

func targetURLHost(host string) string {
	host = trimIPv6LiteralBrackets(host)
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return "[" + ip.String() + "]"
	}
	return host
}

func targetHostPort(t *target) string {
	if isDefaultPort(t.Scheme, t.Port) {
		return targetURLHost(t.Domain)
	}
	return net.JoinHostPort(trimIPv6LiteralBrackets(t.Domain), strconv.Itoa(t.Port))
}

func targetRequestPath(t *target) string {
	if t.Path == "" {
		return "/"
	}
	return "/" + t.Path
}

func inferBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); proto == "http" || proto == "https" {
		scheme = proto
	}
	host := firstHeaderValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" || strings.ContainsAny(host, "/\\ 	\r\n") {
		host = r.Host
	}
	return scheme + "://" + host
}

func isDefaultPort(scheme string, port int) bool {
	return (scheme == "https" && port == 443) || (scheme == "http" && port == 80)
}

func firstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	if idx := strings.IndexByte(raw, ','); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(strings.ToLower(raw))
}

func unproxyURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	t, err := parseTarget(parsed.Path, parsed.RawQuery)
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
	b.WriteString(targetRequestPath(t))
	if t.Query != "" {
		b.WriteByte('?')
		b.WriteString(t.Query)
	}
	return b.String()
}

func normalizeTargetHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	return strings.TrimSuffix(host, ".")
}

func (rt *resolvedTarget) dialAddresses() []string {
	return rt.dialAddrs
}

func resolveSafeTarget(ctx context.Context, resolver *net.Resolver, t *target) (*resolvedTarget, error) {
	ips, err := resolveSafeHostIPs(ctx, resolver, t.Domain)
	if err != nil {
		return nil, err
	}
	return &resolvedTarget{dialAddrs: buildDialAddresses(ips, t.Port)}, nil
}

func buildDialAddresses(ips []net.IP, port int) []string {
	addrs := make([]string, 0, len(ips))
	for _, ip := range ips {
		addrs = append(addrs, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	}
	return addrs
}

func resolveSafeHostIPs(ctx context.Context, resolver *net.Resolver, host string) ([]net.IP, error) {
	normalized := normalizeTargetHost(host)
	if normalized == "" {
		return nil, fmt.Errorf("domain is required")
	}
	if normalized == "localhost" || normalized == "host.docker.internal" {
		return nil, fmt.Errorf("blocked target host: %s", host)
	}
	ips, err := resolveTargetIPs(ctx, resolver, normalized)
	if err != nil {
		return nil, err
	}
	unsafeIPs := false
	safeIPs := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if isDangerousIP(ip) {
			unsafeIPs = true
			continue
		}
		safeIPs = append(safeIPs, ip)
	}
	if unsafeIPs || len(safeIPs) == 0 {
		return nil, fmt.Errorf("blocked target host: %s", host)
	}
	return safeIPs, nil
}

func validateTargetSafety(ctx context.Context, resolver *net.Resolver, t *target) error {
	_, err := resolveSafeTarget(ctx, resolver, t)
	return err
}

func validateHostSafety(ctx context.Context, resolver *net.Resolver, host string) error {
	_, err := resolveSafeHostIPs(ctx, resolver, host)
	return err
}

func resolveTargetIPs(ctx context.Context, resolver *net.Resolver, host string) ([]net.IP, error) {
	normalized := normalizeTargetHost(host)
	if normalized == "" {
		return nil, fmt.Errorf("domain is required")
	}
	if ip := net.ParseIP(strings.Trim(normalized, "[]")); ip != nil {
		return []net.IP{ip}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupIPAddr(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("resolve target host %s: %w", host, err)
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve target host %s: no addresses", host)
	}
	return ips, nil
}

func isDangerousIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if mapped := ip.To4(); mapped != nil {
		ip = mapped
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
