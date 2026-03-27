package main

import (
	"bytes"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func rewriteResponseHeaders(resp *http.Response, baseURL string) {
	if loc := resp.Header.Get("Location"); loc != "" {
		resp.Header.Set("Location", rewriteSingleURL(loc, baseURL))
	}
	if cl := resp.Header.Get("Content-Location"); cl != "" {
		resp.Header.Set("Content-Location", rewriteSingleURL(cl, baseURL))
	}
}

var rewritableTypes = []string{
	"application/json",
	"text/html",
	"text/xml",
	"text/plain",
	"application/xml",
	"application/xhtml",
	"text/javascript",
	"application/javascript",
}

func shouldRewriteBody(contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, t := range rewritableTypes {
		if strings.Contains(ct, t) {
			return true
		}
	}
	return false
}

// rewriteBody replaces upstream origin URLs with proxy URLs using simple
// byte replacement — no regex. Handles both with-port and without-port forms.
func rewriteBody(body []byte, baseURL string, t *target) []byte {
	proxyPrefix := []byte(baseURL + "/" + t.Scheme + "/" + t.Domain + "/" + strconv.Itoa(t.Port))

	// Replace explicit port form: https://domain:443
	withPort := []byte(t.Scheme + "://" + t.Domain + ":" + strconv.Itoa(t.Port))
	body = bytes.ReplaceAll(body, withPort, proxyPrefix)

	// Replace implicit port form: https://domain (only for default ports)
	if isDefaultPort(t.Scheme, t.Port) {
		withoutPort := []byte(t.Scheme + "://" + t.Domain)
		body = bytes.ReplaceAll(body, withoutPort, proxyPrefix)
	}

	return body
}

func rewriteSingleURL(rawURL, baseURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	scheme := parsed.Scheme
	if scheme != "http" && scheme != "https" {
		return rawURL
	}
	host := parsed.Hostname()
	portStr := parsed.Port()
	port := 0
	if portStr != "" {
		port, _ = strconv.Atoi(portStr)
	} else if scheme == "https" {
		port = 443
	} else {
		port = 80
	}
	path := parsed.RequestURI()
	return baseURL + "/" + scheme + "/" + host + "/" + strconv.Itoa(port) + path
}
