package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// minTokenLen guards against trivially guessable bearer tokens.
const minTokenLen = 32

// newHTTPServer exposes the MCP server over Streamable HTTP at /mcp so several
// clients can share one WhatsApp session. Every request must carry
// "Authorization: Bearer <token>". Bind it to loopback only (e.g. publish the
// container port as 127.0.0.1:PORT); the token is defence in depth against
// other local processes and DNS-rebinding from browsers.
func newHTTPServer(srv *server.MCPServer, addr, token string, logger *slog.Logger) (*http.Server, error) {
	if len(token) < minTokenLen {
		return nil, fmt.Errorf("MCP_AUTH_TOKEN must be at least %d characters when MCP_HTTP_ADDR is set", minTokenLen)
	}

	mcpHandler := server.NewStreamableHTTPServer(srv, server.WithEndpointPath("/mcp"))
	want := []byte("Bearer " + token)

	mux := http.NewServeMux()
	mux.Handle("/mcp", requireBearer(want, mcpHandler, logger))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}, nil
}

func requireBearer(want []byte, next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			logger.Warn("rejected unauthenticated MCP request", "remote", r.RemoteAddr, "method", r.Method)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveHTTP runs hs until it is shut down; ErrServerClosed is not an error.
func serveHTTP(hs *http.Server) error {
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
