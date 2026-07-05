package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/barronDEV/btunnel/signaling-server/handlers"
	"github.com/barronDEV/btunnel/signaling-server/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Configure zerolog
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Initialize store (Redis if configured, otherwise fallback to in-memory)
	var sessionStore store.Store
	redisAddr := os.Getenv("BTUNNEL_REDIS_ADDR")

	// Graceful shutdown context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if redisAddr != "" {
		redisPassword := os.Getenv("BTUNNEL_REDIS_PASSWORD")
		log.Info().Str("redis_addr", redisAddr).Msg("Initializing Redis session store")
		sessionStore = store.NewRedisStore(redisAddr, redisPassword, 0)
	} else {
		log.Info().Msg("Initializing in-memory session store")
		memStore := store.NewMemoryStore()
		sessionStore = memStore
		// Start session cleanup goroutine for memory store
		go memStore.StartCleanup(ctx, 30*time.Second)
	}

	// Create handler with store
	handler := handlers.NewHandler(sessionStore)

	// Set up HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handler.HandleWebSocket)
	mux.HandleFunc("/health", handler.HandleHealth)
	mux.Handle("/metrics", promhttp.Handler())

	// Serve static web frontend files in development if the web folder exists
	if _, err := os.Stat("web"); err == nil {
		log.Info().Msg("Web frontend folder detected; serving client pages on HTTP router")
		
		// Helper function to set cache-prevention headers and serve file
		serveNoCache := func(w http.ResponseWriter, r *http.Request, filePath string) {
			w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			http.ServeFile(w, r, filePath)
		}

		mux.HandleFunc("/share/", func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path[len("/share/"):]
			if path == "" || !strings.Contains(path, ".") {
				serveNoCache(w, r, "web/index.html")
				return
			}
			serveNoCache(w, r, filepath.Join("web", path))
		})

		mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
			serveNoCache(w, r, "web/sw.js")
		})
		mux.HandleFunc("/webrtc-client.js", func(w http.ResponseWriter, r *http.Request) {
			serveNoCache(w, r, "web/webrtc-client.js")
		})
	}

	// Server configuration
	addr := os.Getenv("BTUNNEL_SIGNAL_ADDR")
	if addr == "" {
		addr = ":9090"
	}

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	certFile := os.Getenv("BTUNNEL_TLS_CERT")
	keyFile := os.Getenv("BTUNNEL_TLS_KEY")

	// Start server
	go func() {
		log.Info().Str("addr", addr).Msg("Signaling server starting")
		var err error
		if certFile != "" && keyFile != "" {
			log.Info().Str("cert", certFile).Str("key", keyFile).Msg("TLS enabled")
			err = server.ListenAndServeTLS(certFile, keyFile)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("Server failed to start")
		}
	}()

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info().Msg("Shutting down signaling server...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("Server forced to shutdown")
	}

	log.Info().Msg("Signaling server stopped")
}
