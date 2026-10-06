package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
)

// TestProxyHTTPServerHasNoDefaultWriteDeadline pins the fix for responses that
// were cut off mid-body.
//
// `http.Server.WriteTimeout` covers the whole response write rather than the gap
// between writes, so the five minutes this server used to carry truncated every
// proxied completion, every local Responses SSE stream and every long reasoning
// turn that ran past it. The deadline has to be absent by default and only
// present when an operator asks for it.
func TestProxyHTTPServerHasNoDefaultWriteDeadline(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Port = "18080"

	srv := newProxyHTTPServer(cfg, http.NewServeMux())
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0 (no write deadline)", srv.WriteTimeout)
	}
	if srv.ReadTimeout != 5*time.Minute {
		t.Fatalf("ReadTimeout = %v, want the 5m default", srv.ReadTimeout)
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout = %v, want 2m", srv.IdleTimeout)
	}
	if srv.Addr != ":18080" {
		t.Fatalf("Addr = %q, want :18080", srv.Addr)
	}
}

func TestProxyHTTPServerUsesConfiguredTimeouts(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Port = "18080"
	cfg.Server.ReadTimeout = 45 * time.Second
	cfg.Server.WriteTimeout = 90 * time.Second

	srv := newProxyHTTPServer(cfg, http.NewServeMux())
	if srv.ReadTimeout != 45*time.Second {
		t.Fatalf("ReadTimeout = %v, want the configured 45s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 90*time.Second {
		t.Fatalf("WriteTimeout = %v, want the configured 90s", srv.WriteTimeout)
	}
}

// TestManagementHTTPServerHasNoWriteDeadline pins that the Monitor server does
// not carry a deadline either: `GET /api/events/stream` is a long-lived SSE
// response, and the two minutes this server used to carry closed every event
// stream on schedule.
func TestManagementHTTPServerHasNoWriteDeadline(t *testing.T) {
	cfg := &config.Config{}
	cfg.Monitor.Port = "18081"

	srv := newManagementHTTPServer(cfg, http.NewServeMux())
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0 (no write deadline)", srv.WriteTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout = %v, want 30s", srv.ReadTimeout)
	}
	if srv.Addr != ":18081" {
		t.Fatalf("Addr = %q, want :18081", srv.Addr)
	}
}
