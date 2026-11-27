package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
)

func TestIsNonLoopbackListenAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":8080", true},
		{"0.0.0.0:8080", true},
		{"[::]:8080", true},
		{"127.0.0.1:8080", false},
		{"[::1]:8080", false},
		{"monitor.internal.test:8080", true},
	}
	for _, tc := range cases {
		if got := isNonLoopbackListenAddress(tc.addr); got != tc.want {
			t.Fatalf("isNonLoopbackListenAddress(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestServeHTTPRejectsPartialTLS(t *testing.T) {
	for _, keyOnly := range []bool{false, true} {
		cfg := &config.Config{}
		if keyOnly {
			cfg.Server.TLSKey = "key.pem"
		} else {
			cfg.Server.TLSCert = "cert.pem"
		}
		server := &http.Server{Addr: "invalid address"}
		if err := serveHTTP(server, cfg); err == nil || !strings.Contains(err.Error(), "must be configured together") {
			t.Fatalf("serveHTTP error = %v", err)
		}
	}
}

func TestServeHTTPInvalidCertificateDoesNotFallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.TLSCert = filepath.Join(t.TempDir(), "missing.pem")
	cfg.Server.TLSKey = filepath.Join(t.TempDir(), "missing.key")
	server := &http.Server{Addr: "127.0.0.1:0"}
	defer server.Close()
	if err := serveHTTP(server, cfg); err == nil {
		t.Fatal("invalid certificate was accepted")
	}
}

func TestServeHTTPTransport(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := fixture.TLS.Certificates[0]
	client := fixture.Client()
	client.Timeout = 3 * time.Second
	fixture.Close()
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for path, data := range map[string][]byte{
		certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}),
		keyPath:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, useTLS := range []bool{false, true} {
		cfg := &config.Config{}
		if useTLS {
			cfg.Server.TLSCert, cfg.Server.TLSKey = " "+certPath+" ", " "+keyPath+" "
		}
		t.Run(cfg.ServerScheme(), func(t *testing.T) {
			addresses := make(chan string, 1)
			done := make(chan error, 1)
			server := &http.Server{
				Addr: "127.0.0.1:0",
				Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if (request.TLS != nil) != useTLS {
						t.Error("unexpected request transport")
					}
					_, _ = io.WriteString(writer, "ok")
				}),
				BaseContext: func(listener net.Listener) context.Context {
					addresses <- listener.Addr().String()
					return context.Background()
				},
			}
			go func() { done <- serveHTTP(server, cfg) }()
			t.Cleanup(func() {
				_ = server.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("server did not stop")
				}
			})
			var address string
			select {
			case address = <-addresses:
			case <-time.After(3 * time.Second):
				t.Fatal("server did not start")
			}
			response, err := client.Get(cfg.ServerScheme() + address)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "ok" {
				t.Fatalf("body=%q, error=%v", body, err)
			}
		})
	}
}

func TestWarnInsecureListenersTLS(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cfg := &config.Config{}
	cfg.Server.Port, cfg.Monitor.Port = "8080", "8081"
	warnInsecureListeners(cfg)
	if strings.Count(output.String(), "level=WARN") != 2 {
		t.Fatalf("warnings = %s", output.String())
	}
	output.Reset()
	cfg.Server.TLSCert, cfg.Server.TLSKey = "cert.pem", "key.pem"
	warnInsecureListeners(cfg)
	if output.Len() != 0 {
		t.Fatalf("TLS warnings = %s", output.String())
	}
}
