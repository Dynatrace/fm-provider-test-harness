// Command mockserver is the shared mock backend for the Dynatrace FM OpenFeature provider test
// harness. It emulates the CDN config endpoint, the metrics ingest endpoint and an SSE stream, and
// exposes an HTTP control plane the provider acceptance suites drive. See README.md for the contract.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := ":" + envOr("PORT", "8080")

	// baseCtx is the parent of every request context. Cancelling it on shutdown unblocks
	// long-lived handlers (the SSE stream) immediately, so graceful shutdown doesn't stall for
	// the full grace window waiting on streams that never go idle on their own.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	srv := &http.Server{
		Addr:              addr,
		Handler:           NewServer().Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}

	go func() {
		log.Printf("mock backend listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutting down")
	cancelBase() // release the SSE streams so Shutdown can drain instead of waiting them out
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
