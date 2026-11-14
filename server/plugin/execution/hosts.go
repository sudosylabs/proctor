// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/remote"
)

// Dialer connects to one configured execenv host. Production uses
// RemoteDialer; tests hand in memory hosts.
type Dialer func(ctx context.Context, host HostConfig) (execenv.Host, error)

// RemoteDialer dials a host over TLS with its certificate authority and
// token.
func RemoteDialer(_ context.Context, host HostConfig) (execenv.Host, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(host.CAPEM)) {
		return nil, execenv.Error("dial", execenv.ErrInvalid)
	}
	return remote.New(remote.Config{
		Address:    host.Address,
		ServerName: host.ServerName,
		TLS:        &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
		Security:   remote.SecurityTLS,
		Token:      []byte(host.Token),
		Timeout:    10 * time.Second,
	})
}

const (
	// readyTTL is how long a host's readiness report is trusted.
	readyTTL = 5 * time.Second
	// redialDelay is how long a host that failed to dial is left alone.
	redialDelay = 5 * time.Second
)

// hostPool dials the configured hosts lazily and keeps a short-lived cache
// of their readiness. A host that fails to dial or reports itself unusable
// is skipped for placement and retried later.
type hostPool struct {
	dial   Dialer
	logger *slog.Logger
	mu     sync.Mutex
	hosts  []*poolHost
}

type poolHost struct {
	config     HostConfig
	host       execenv.Host
	failedAt   time.Time
	report     execenv.Report
	reportedAt time.Time
	reportErr  error
}

// hostReport is one host's cached readiness.
type hostReport struct {
	name   string
	report execenv.Report
}

func (r hostReport) usable(image execenv.Image) bool {
	return r.report.Usable && slices.Contains(r.report.Images, image) &&
		slices.Contains(r.report.Networks, execenv.NetworkNone)
}

func newHostPool(configs []HostConfig, dial Dialer, logger *slog.Logger) *hostPool {
	pool := &hostPool{dial: dial, logger: logger}
	for _, config := range configs {
		pool.hosts = append(pool.hosts, &poolHost{config: config})
	}
	return pool
}

// get returns the named host, dialing it if needed.
func (p *hostPool) get(ctx context.Context, name string) (execenv.Host, error) {
	p.mu.Lock()
	var target *poolHost
	for _, candidate := range p.hosts {
		if candidate.config.Name == name {
			target = candidate
		}
	}
	p.mu.Unlock()
	if target == nil {
		return nil, execenv.Error("dial", execenv.ErrUnavailable)
	}
	return p.connect(ctx, target)
}

func (p *hostPool) connect(ctx context.Context, target *poolHost) (execenv.Host, error) {
	p.mu.Lock()
	if target.host != nil {
		host := target.host
		p.mu.Unlock()
		return host, nil
	}
	if !target.failedAt.IsZero() && time.Since(target.failedAt) < redialDelay {
		p.mu.Unlock()
		return nil, execenv.Error("dial", execenv.ErrConnection)
	}
	config := target.config
	p.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	host, err := p.dial(dialCtx, config)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		target.failedAt = time.Now()
		p.logger.Warn("execution host did not dial", slog.String("host", config.Name), slog.String("error", errorClass(err)))
		return nil, execenv.Error("dial", execenv.ErrConnection)
	}
	if target.host != nil {
		closeHost(host)
		return target.host, nil
	}
	target.host, target.failedAt = host, time.Time{}
	return host, nil
}

// broken forgets a host whose connection failed, so the next call dials
// again. Live grants on it are unaffected.
func (p *hostPool) broken(name string, host execenv.Host) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, candidate := range p.hosts {
		if candidate.config.Name == name && candidate.host == host {
			candidate.host, candidate.reportedAt = nil, time.Time{}
			closeHost(host)
		}
	}
}

// reports returns the readiness of every reachable host, refreshing reports
// older than readyTTL. With fresh, every report is refreshed now.
func (p *hostPool) reports(ctx context.Context, fresh bool) []hostReport {
	p.mu.Lock()
	hosts := slices.Clone(p.hosts)
	p.mu.Unlock()
	var result []hostReport
	for _, target := range hosts {
		p.mu.Lock()
		cached, at, cachedErr := target.report, target.reportedAt, target.reportErr
		p.mu.Unlock()
		if !fresh && !at.IsZero() && time.Since(at) < readyTTL {
			if cachedErr == nil {
				result = append(result, hostReport{name: target.config.Name, report: cached})
			}
			continue
		}
		host, err := p.connect(ctx, target)
		if err != nil {
			continue
		}
		readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		report, err := host.Ready(readyCtx)
		cancel()
		p.mu.Lock()
		target.report, target.reportedAt, target.reportErr = report, time.Now(), err
		p.mu.Unlock()
		if err != nil {
			if errors.Is(err, execenv.ErrConnection) {
				p.broken(target.config.Name, host)
			}
			continue
		}
		if !report.Usable {
			p.logger.Debug("execution host is not usable", slog.String("host", target.config.Name), slog.String("reason", report.Reason))
		}
		result = append(result, hostReport{name: target.config.Name, report: report})
	}
	return result
}

// place picks the usable host offering image with the most free slots, or
// "" when none has room.
func (p *hostPool) place(ctx context.Context, image execenv.Image, exclude map[string]bool) string {
	best, slots := "", 0
	for _, report := range p.reports(ctx, false) {
		if exclude[report.name] || !report.usable(image) || report.report.Slots <= slots {
			continue
		}
		best, slots = report.name, report.report.Slots
	}
	return best
}

// close closes every dialed host connection. Grants are not revoked.
func (p *hostPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, target := range p.hosts {
		if target.host != nil {
			closeHost(target.host)
			target.host = nil
		}
	}
}

func closeHost(host execenv.Host) {
	if closer, ok := host.(io.Closer); ok {
		_ = closer.Close()
	}
}

// errorClass names an execenv error without its text, which could carry
// host detail.
func errorClass(err error) string {
	for _, known := range []struct {
		err  error
		name string
	}{
		{execenv.ErrInvalid, "invalid"}, {execenv.ErrUnknownImage, "unknown_image"}, {execenv.ErrCapacity, "capacity"},
		{execenv.ErrDraining, "draining"}, {execenv.ErrConflict, "conflict"}, {execenv.ErrUnavailable, "unavailable"},
		{execenv.ErrFrozen, "frozen"}, {execenv.ErrRevoked, "revoked"}, {execenv.ErrLost, "lost"},
		{execenv.ErrBusy, "busy"}, {execenv.ErrSuperseded, "superseded"}, {execenv.ErrClosed, "closed"},
		{execenv.ErrNotFound, "not_found"}, {execenv.ErrLagged, "lagged"}, {execenv.ErrTooLarge, "too_large"},
		{execenv.ErrConnection, "connection"}, {execenv.ErrNetwork, "network"},
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline"},
	} {
		if errors.Is(err, known.err) {
			return known.name
		}
	}
	return "other"
}
