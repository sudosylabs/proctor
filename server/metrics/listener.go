// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/sudosylabs/proctor/server/internal/tlscert"
)

// profilingPrefix is the path under which the opt-in Go profiling handlers
// are served. They exist only on this authenticated, node-local listener.
const profilingPrefix = "/debug/pprof/"

// routes maps the listener's closed path set. The scrape endpoint is always
// present; the Go runtime profiles are added only when profiling is enabled.
// The command-line handler is never registered because process arguments can
// carry operator secrets.
func (m *Module) routes(scrape http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/metrics" && request.Method == http.MethodGet {
			scrape.ServeHTTP(writer, request)
			return
		}

		if !m.settings.ProfilingEnabled || !strings.HasPrefix(request.URL.Path, profilingPrefix) {
			http.NotFound(writer, request)
			return
		}

		name := strings.TrimPrefix(request.URL.Path, profilingPrefix)
		if request.Method != http.MethodGet && (name != "symbol" || request.Method != http.MethodPost) {
			http.NotFound(writer, request)
			return
		}

		switch name {
		case "cmdline":
			http.NotFound(writer, request)
		case "profile":
			pprof.Profile(writer, request)
		case "symbol":
			pprof.Symbol(writer, request)
		case "trace":
			pprof.Trace(writer, request)
		default:
			// Index serves the listing for the bare prefix and every named
			// runtime profile (heap, goroutine, allocs, block, mutex,
			// threadcreate) beneath it.
			pprof.Index(writer, request)
		}
	})
}

// authorize applies the listener-wide response headers and bearer
// authentication before any route, so profiling shares the scrape credential.
func (m *Module) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if m.settings.BearerToken != "" {
			got := request.Header.Get("Authorization")
			want := "Bearer " + m.settings.BearerToken
			gotDigest := sha256.Sum256([]byte(got))
			wantDigest := sha256.Sum256([]byte(want))
			if subtle.ConstantTimeCompare(gotDigest[:], wantDigest[:]) != 1 {
				writer.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func (m *Module) Start(ctx context.Context) error {
	if m == nil || !m.settings.Enabled {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("metrics module is closed")
	}
	if m.started {
		return errors.New("metrics module is already started")
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", m.settings.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", m.settings.ListenAddress, err)
	}

	if err := ctx.Err(); err != nil {
		_ = listener.Close()
		return err
	}

	if m.settings.TLS.CertificateFile != "" {
		certificate := tlscert.New(m.settings.TLS.CertificateFile, m.settings.TLS.PrivateKeyFile, func(err error) {
			if m.errorLog != nil {
				m.errorLog.Printf("metrics TLS certificate reload failed; keeping the previous certificate: %v", err)
			}
		})
		if loadErr := certificate.Reload(); loadErr != nil {
			_ = listener.Close()
			return fmt.Errorf("load metrics TLS identity: %w", loadErr)
		}

		m.certificate = certificate
		listener = tls.NewListener(listener, &tls.Config{GetCertificate: certificate.GetCertificate, MinVersion: tls.VersionTLS12})
	}

	server := &http.Server{
		Handler:           m.handler,
		ErrorLog:          m.errorLog,
		ReadHeaderTimeout: m.settings.ReadHeaderTimeout.Duration,
		ReadTimeout:       m.settings.ReadTimeout.Duration,
		WriteTimeout:      m.settings.WriteTimeout.Duration,
		IdleTimeout:       m.settings.IdleTimeout.Duration,
		MaxHeaderBytes:    16 << 10,
	}
	done := make(chan struct{})
	m.listener, m.server, m.done, m.started = listener, server, done, true
	go func() {
		defer close(done)
		serveErr := server.Serve(listener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
			select {
			case m.failures <- serveErr:
			default:
			}
		}
	}()
	return nil
}

// ReloadCertificates reloads the metrics listener's operator-managed TLS key
// pair, as the main listener's does on SIGHUP. On failure the previous pair
// stays in service. It is a no-op before Start and without TLS.
func (m *Module) ReloadCertificates() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	certificate := m.certificate
	m.mu.Unlock()
	if certificate == nil {
		return nil
	}
	return certificate.Reload()
}

func (m *Module) Failures() <-chan error {
	if m == nil {
		return nil
	}
	return m.failures
}

func (m *Module) SetReady(ready bool) {
	if m != nil {
		m.ready.Store(ready)
	}
}

// Close stops the metrics listener. The graceful drain ends when ctx, the
// caller's shutdown budget, ends or after the configured shutdown timeout,
// whichever comes first; connections that remain are then closed. Close is
// idempotent, and every caller shares the first result.
func (m *Module) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.closeErr = m.close(ctx)
		close(m.closeDone)
	})
	<-m.closeDone
	return m.closeErr
}

func (m *Module) close(budget context.Context) error {
	m.mu.Lock()
	m.closed = true
	server, listener, done := m.server, m.listener, m.done
	m.mu.Unlock()
	m.ready.Store(false)
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(budget, m.settings.ShutdownTimeout.Duration)
	defer cancel()
	shutdownErr := server.Shutdown(ctx)
	var forceErr error
	if shutdownErr != nil {
		forceErr = server.Close()
	}
	var closeErr error
	if listener != nil {
		closeErr = listener.Close()
	}
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	if done != nil {
		<-done
	}
	return errors.Join(shutdownErr, forceErr, closeErr)
}
