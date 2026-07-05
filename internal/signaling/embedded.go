package signaling

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/barronDEV/btunnel/signaling-server/handlers"
	"github.com/barronDEV/btunnel/signaling-server/store"
	"github.com/barronDEV/btunnel/web"
	"github.com/rs/zerolog/log"
)

// EmbeddedServer is a lightweight signaling server that runs inside the CLI process.
// This eliminates the need for a separate signaling-server process — the host's
// `btunnel share` command automatically starts this in the background.
type EmbeddedServer struct {
	server   *http.Server
	store    store.Store
	handler  *handlers.Handler
	port     int
	listener net.Listener
}

// StartEmbeddedServer starts a signaling server as a background goroutine within
// the current process. It returns an EmbeddedServer handle and an error.
//
// The server is ready to accept connections when this function returns without error.
// Call Stop() to gracefully shut down.
func StartEmbeddedServer(ctx context.Context, port int) (*EmbeddedServer, error) {
	// Initialize in-memory store (no Redis needed for embedded mode)
	memStore := store.NewMemoryStore()
	go memStore.StartCleanup(ctx, 30*time.Second)

	// Create handler
	handler := handlers.NewHandler(memStore)

	// Set up HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handler.HandleWebSocket)
	mux.HandleFunc("/health", handler.HandleHealth)

	// Serve embedded web frontend files
	embeddedFS, err := fs.Sub(web.StaticFiles, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to create sub filesystem for web assets: %w", err)
	}

	mux.HandleFunc("/share/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[len("/share/"):]
		if path == "" || !strings.Contains(path, ".") {
			// Serve index.html for SPA routes
			data, err := fs.ReadFile(web.StaticFiles, "index.html")
			if err != nil {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		http.StripPrefix("/share/", http.FileServer(http.FS(embeddedFS))).ServeHTTP(w, r)
	})

	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(web.StaticFiles, "sw.js")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Write(data)
	})

	mux.HandleFunc("/webrtc-client.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(web.StaticFiles, "webrtc-client.js")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(data)
	})

	addr := fmt.Sprintf(":%d", port)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Create listener first to confirm the port is available
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	embedded := &EmbeddedServer{
		server:   server,
		store:    memStore,
		handler:  handler,
		port:     port,
		listener: listener,
	}

	// Start serving in background
	go func() {
		log.Info().Int("port", port).Msg("Embedded signaling server started")
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("Embedded signaling server error")
		}
	}()

	// Register shutdown on context cancellation
	go func() {
		<-ctx.Done()
		embedded.Stop()
	}()

	return embedded, nil
}

// Stop gracefully shuts down the embedded signaling server.
func (e *EmbeddedServer) Stop() {
	if e.server == nil {
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := e.server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("Embedded signaling server forced shutdown")
	}

	log.Info().Msg("Embedded signaling server stopped")
}

// Port returns the port the server is listening on.
func (e *EmbeddedServer) Port() int {
	return e.port
}

// WebSocketURL returns the WebSocket URL clients should connect to.
func (e *EmbeddedServer) WebSocketURL() string {
	return fmt.Sprintf("ws://localhost:%d/ws", e.port)
}

// WebURL returns the base HTTP URL for web clients, given a host IP.
func (e *EmbeddedServer) WebURL(hostIP string) string {
	return fmt.Sprintf("http://%s:%d/share/", hostIP, e.port)
}

// FindAvailablePort finds the first available port starting from the preferred port.
// If the preferred port is taken, it tries up to 10 subsequent ports.
func FindAvailablePort(preferred int) (int, error) {
	for i := 0; i < 10; i++ {
		port := preferred + i
		addr := fmt.Sprintf(":%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found near %d", preferred)
}

// ExtractFilePath extracts a clean file path from a request URL for embedded serving.
func ExtractFilePath(urlPath, prefix string) string {
	path := strings.TrimPrefix(urlPath, prefix)
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return "index.html"
	}
	return filepath.Clean(path)
}
