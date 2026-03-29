package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 0
	serverIdleTimeout       = 120 * time.Second
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	blockPrivateTargets := parseBlockPrivateTargets(os.Getenv("BLOCK_PRIVATE_TARGETS"))

	handler := NewProxyHandler(blockPrivateTargets)

	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			log.Printf("[ERROR] write /health response failed: %v", err)
		}
	})

	// All other routes go to proxy handler
	mux.Handle("/", handler)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}

	log.Printf("[SERVER] listening on %s", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("[SERVER] fatal: %v", err)
	}
}
