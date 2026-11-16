// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package executionhost is the execenv adapter behind the Attempt
// Environment's host port: it dials the configured execution hosts and
// translates the owner's calls and execenv's answers. No execenv type
// leaves it.
package executionhost

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/ignore"
	"github.com/sudosylabs/execenv/remote"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
)

const (
	// reportTTL is how long a usable host's readiness answer is trusted,
	// and unusableTTL how long another answer is.
	reportTTL   = 2 * time.Second
	unusableTTL = 200 * time.Millisecond
	// redialDelay is how long a host that failed to dial is left alone.
	redialDelay = 5 * time.Second
	// dialTimeout and readyTimeout bound connecting and one readiness call.
	dialTimeout  = 15 * time.Second
	readyTimeout = 5 * time.Second
)

// Dialer connects to one configured execution host.
type Dialer func(ctx context.Context, host config.ExecutionHost) (execenv.Host, error)

// Config is what New needs.
type Config struct {
	Hosts []config.ExecutionHost
	// Dial connects to a host. Nil dials it over TLS 1.3, checking its
	// certificate against the host's certificate authority and presenting
	// its token.
	Dial Dialer
	// AllowUnisolated accepts hosts that share the machine's kernel, for
	// tests and local development. Production leaves it false, so only an
	// isolated host is ever used.
	AllowUnisolated bool
}

// Hosts dials the configured hosts lazily, one client each, and keeps a
// short-lived cache of their readiness. A host that fails to dial is left
// alone for a moment and dialed again on the next use.
type Hosts struct {
	dial            Dialer
	allowUnisolated bool

	mu     sync.Mutex
	order  []string
	byName map[string]*host
	closed bool
}

type host struct {
	config     config.ExecutionHost
	client     execenv.Host
	failedAt   time.Time
	report     execenv.Report
	reportErr  error
	reportedAt time.Time
}

var _ environment.Hosts = (*Hosts)(nil)

// New returns the hosts; nothing is dialed until a host is used.
func New(cfg Config) (*Hosts, error) {
	hosts := &Hosts{dial: cfg.Dial, allowUnisolated: cfg.AllowUnisolated, byName: map[string]*host{}}
	if hosts.dial == nil {
		hosts.dial = dialRemote
	}
	for _, configured := range cfg.Hosts {
		if configured.Name == "" {
			return nil, errors.New("an execution host has no name")
		}
		if _, repeated := hosts.byName[configured.Name]; repeated {
			return nil, fmt.Errorf("execution host %q is configured twice", configured.Name)
		}
		hosts.order = append(hosts.order, configured.Name)
		hosts.byName[configured.Name] = &host{config: configured}
	}
	return hosts, nil
}

// dialRemote dials a host over TLS with its certificate authority and
// token.
func dialRemote(_ context.Context, configured config.ExecutionHost) (execenv.Host, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(configured.CAPEM)) {
		return nil, execenv.Error("dial", execenv.ErrInvalid)
	}
	return remote.New(remote.Config{
		Address:    configured.Address,
		ServerName: configured.ServerName,
		TLS:        &tls.Config{RootCAs: roots, ServerName: configured.ServerName, MinVersion: tls.VersionTLS13},
		Security:   remote.SecurityTLS,
		Token:      []byte(configured.Token),
		Timeout:    dialTimeout,
	})
}

// Close closes every dialed client. Machines are left running.
func (h *Hosts) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, target := range h.byName {
		closeClient(target.client)
		target.client = nil
	}
	return nil
}

// client returns the named host's client, dialing it when needed.
func (h *Hosts) client(ctx context.Context, name string) (execenv.Host, error) {
	h.mu.Lock()
	target := h.byName[name]
	switch {
	case h.closed || target == nil:
		h.mu.Unlock()
		return nil, hostError("dial", environment.ErrHostUnavailable)
	case target.client != nil:
		client := target.client
		h.mu.Unlock()
		return client, nil
	case !target.failedAt.IsZero() && time.Since(target.failedAt) < redialDelay:
		h.mu.Unlock()
		return nil, hostError("dial", environment.ErrHostUnavailable)
	}
	configured := target.config
	h.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	client, err := h.dial(dialCtx, configured)
	if err == nil && !h.allowUnisolated {
		if err = execenv.RequireIsolated(client); err != nil {
			closeClient(client)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		target.failedAt = time.Now()
		return nil, hostError("dial", environment.ErrHostUnavailable)
	}
	if target.client != nil || h.closed {
		closeClient(client)
		if h.closed {
			return nil, hostError("dial", environment.ErrHostUnavailable)
		}
		return target.client, nil
	}
	target.client, target.failedAt = client, time.Time{}
	return client, nil
}

// broken forgets a client whose connection failed, so the next use dials
// again. Machines on it are unaffected.
func (h *Hosts) broken(name string, client execenv.Host) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if target := h.byName[name]; target != nil && target.client == client {
		target.client, target.reportedAt = nil, time.Time{}
		closeClient(client)
	}
}

func closeClient(client execenv.Host) {
	if closer, ok := client.(io.Closer); ok && client != nil {
		_ = closer.Close()
	}
}

// Reports returns the readiness of every host that answers.
func (h *Hosts) Reports(ctx context.Context) []environment.HostReport {
	h.mu.Lock()
	names := append([]string(nil), h.order...)
	h.mu.Unlock()
	var reports []environment.HostReport
	for _, name := range names {
		report, ok := h.ready(ctx, name)
		if ok {
			reports = append(reports, toReport(name, report))
		}
	}
	return reports
}

func (h *Hosts) ready(ctx context.Context, name string) (execenv.Report, bool) {
	h.mu.Lock()
	target := h.byName[name]
	ttl := unusableTTL
	if target.reportErr == nil && target.report.Usable {
		ttl = reportTTL
	}
	if !target.reportedAt.IsZero() && time.Since(target.reportedAt) < ttl {
		report, err := target.report, target.reportErr
		h.mu.Unlock()
		return report, err == nil
	}
	h.mu.Unlock()
	client, err := h.client(ctx, name)
	if err != nil {
		return execenv.Report{}, false
	}
	readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
	report, err := client.Ready(readyCtx)
	cancel()
	if errors.Is(err, execenv.ErrConnection) {
		h.broken(name, client)
	}
	if err == nil && !h.allowUnisolated && report.Isolation == execenv.IsolationNone {
		report.Usable = false
	}
	h.mu.Lock()
	target.report, target.reportErr, target.reportedAt = report, err, time.Now()
	h.mu.Unlock()
	return report, err == nil
}

func toReport(name string, report execenv.Report) environment.HostReport {
	result := environment.HostReport{
		Name: name, Usable: report.Usable,
		Free: model.EnvironmentResources{CPUMillis: report.CPUMillis.Free, MemoryBytes: report.MemoryBytes.Free,
			DiskBytes: report.DiskBytes.Free},
		GrantMax: model.EnvironmentResources{CPUMillis: report.GrantMax.CPUMillis, MemoryBytes: report.GrantMax.MemoryBytes,
			DiskBytes: report.GrantMax.DiskBytes},
	}
	for _, image := range report.Images {
		result.Images = append(result.Images, string(image))
	}
	for _, network := range report.Networks {
		switch network {
		case execenv.NetworkNone:
			result.Networks = append(result.Networks, model.EnvironmentNetworkNone)
		case execenv.NetworkAllowlist:
			result.Networks = append(result.Networks, model.EnvironmentNetworkAllowlist)
		}
	}
	return result
}

// Ensure places the machine on the named host.
func (h *Hosts) Ensure(ctx context.Context, name string, spec environment.MachineSpec) (environment.Machine, error) {
	client, err := h.client(ctx, name)
	if err != nil {
		return nil, err
	}
	executionSpec := execenv.Spec{
		ID: execenv.ID(spec.GrantID), Image: execenv.Image(spec.Image), Network: toNetwork(spec.Network),
		Resources: execenv.Resources{CPUMillis: spec.Resources.CPUMillis, MemoryBytes: spec.Resources.MemoryBytes,
			DiskBytes: spec.Resources.DiskBytes},
	}
	if spec.IdleSuspend > 0 {
		executionSpec.IdleTimeout, executionSpec.IdleAction = spec.IdleSuspend, execenv.IdleSuspend
	}
	env, err := client.Ensure(ctx, executionSpec)
	if err != nil {
		if errors.Is(err, execenv.ErrConnection) {
			h.broken(name, client)
		}
		return nil, translate("ensure", err)
	}
	return &machine{env: env, hosts: h, host: name, client: client}, nil
}

func toNetwork(network model.EnvironmentNetwork) execenv.Network {
	if network == model.EnvironmentNetworkAllowlist {
		return execenv.NetworkAllowlist
	}
	return execenv.NetworkNone
}

// Revoke ends a machine, or acknowledges a lost one.
func (h *Hosts) Revoke(ctx context.Context, name, grantID string) error {
	client, err := h.client(ctx, name)
	if err != nil {
		return err
	}
	if err := client.Revoke(ctx, execenv.ID(grantID)); err != nil {
		if errors.Is(err, execenv.ErrConnection) {
			h.broken(name, client)
		}
		return translate("revoke", err)
	}
	return nil
}

// WatchGrant follows one machine.
func (h *Hosts) WatchGrant(ctx context.Context, name, grantID string) (environment.GrantWatch, error) {
	client, err := h.client(ctx, name)
	if err != nil {
		return nil, err
	}
	watch, err := client.WatchGrant(ctx, execenv.ID(grantID))
	if err != nil {
		return nil, translate("watch", err)
	}
	return grantWatch{watch: watch}, nil
}

type grantWatch struct{ watch execenv.GrantWatch }

func (w grantWatch) Next(ctx context.Context) (environment.GrantState, error) {
	info, err := w.watch.Next(ctx)
	if errors.Is(err, io.EOF) {
		return "", io.EOF
	}
	if err != nil {
		return "", translate("watch", err)
	}
	return environment.GrantState(info.State), nil
}

func (w grantWatch) Close() error { return w.watch.Close() }

// IgnoreRules compiles ignore rules with execenv's matcher, the one the
// machine applies.
func (h *Hosts) IgnoreRules(file string, fallback []string) (environment.IgnoreRules, error) {
	rules, err := ignore.New(file, fallback)
	if err != nil {
		return nil, fmt.Errorf("compile ignore rules: %w", err)
	}
	return ignoreRules{rules: rules}, nil
}

type ignoreRules struct{ rules *ignore.Rules }

func (r ignoreRules) Policy(body []byte, present bool) environment.IgnorePolicy {
	fallback := !present || len(body) > ignore.MaxFileBytes
	if !fallback {
		_, err := ignore.Parse(body)
		fallback = err != nil
	}
	return ignorePolicy{policy: r.rules.Policy(body, present), fallback: fallback}
}

// ignorePolicy is execenv's decision and whether it is the fallback's,
// which execenv's matcher decides by the same parse.
type ignorePolicy struct {
	policy   ignore.Policy
	fallback bool
}

func (p ignorePolicy) Fallback() bool { return p.fallback }

func (p ignorePolicy) Observed(name string, directory bool) bool {
	return p.policy.Observed(name, directory, false)
}

// translate maps an execenv failure to the port's, naming only the
// operation: execenv's text can carry host detail.
func translate(operation string, err error) error {
	for _, known := range []struct {
		execenv error
		port    error
	}{
		{execenv.ErrCapacity, environment.ErrHostCapacity},
		{execenv.ErrLost, environment.ErrMachineLost},
		{execenv.ErrRevoked, environment.ErrMachineRevoked},
		{execenv.ErrStale, environment.ErrMachineStale},
		{execenv.ErrLagged, environment.ErrMachineLagged},
		{execenv.ErrConflict, environment.ErrMachineConflict},
		{execenv.ErrFrozen, environment.ErrMachineFrozen},
		{execenv.ErrNotFound, environment.ErrMachineNotFound},
		{execenv.ErrTooLarge, environment.ErrMachineTooLarge},
		{execenv.ErrQuota, environment.ErrMachineTooLarge},
		{execenv.ErrMissingContent, environment.ErrMachineConflict},
		{execenv.ErrBusy, environment.ErrMachineBusy},
		{execenv.ErrClosed, environment.ErrProcessExited},
		{execenv.ErrDraining, environment.ErrHostUnavailable},
		{execenv.ErrUnavailable, environment.ErrHostUnavailable},
		{execenv.ErrUnknownImage, environment.ErrHostUnavailable},
		{execenv.ErrNetwork, environment.ErrHostUnavailable},
		{execenv.ErrFeature, environment.ErrHostUnavailable},
		{execenv.ErrConnection, environment.ErrHostUnavailable},
		{context.Canceled, context.Canceled},
		{context.DeadlineExceeded, environment.ErrHostUnavailable},
	} {
		if errors.Is(err, known.execenv) {
			return hostError(operation, known.port)
		}
	}
	return hostError(operation, errors.New("execution host call failed"))
}

func hostError(operation string, err error) error {
	return fmt.Errorf("execution host %s: %w", operation, err)
}
