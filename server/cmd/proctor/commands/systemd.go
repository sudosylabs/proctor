// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (
	systemdReadyMessage        = "READY=1"
	systemdStoppingMessage     = "STOPPING=1"
	systemdWatchdogMessage     = "WATCHDOG=1"
	systemdNotificationTimeout = 5 * time.Second
	// systemdWatchdogSampleLimit is the longest gap between readiness samples
	// inside one watchdog interval.
	systemdWatchdogSampleLimit = time.Second
)

type systemdNotificationConnection interface {
	Write([]byte) (int, error)
	SetWriteDeadline(time.Time) error
	Close() error
}

// A nil systemdSupervisor means systemd does not supervise the process.
type systemdSupervisor struct {
	notify func(context.Context, string) error
	// watchdogInterval is how often systemd expects a keep-alive; zero when
	// the unit configures no watchdog for this process.
	watchdogInterval time.Duration
	monotonicNow     func() (time.Duration, error)
	stopping         atomic.Bool
}

// newSystemdSupervisor returns nil when NOTIFY_SOCKET is absent. As
// sd_watchdog_enabled(3) specifies, the watchdog applies only when
// WATCHDOG_PID, if set, names this process.
func newSystemdSupervisor(lookup func(string) string, pid int) *systemdSupervisor {
	socketPath := lookup("NOTIFY_SOCKET")
	if socketPath == "" {
		return nil
	}
	supervisor := &systemdSupervisor{
		notify:       systemdNotifier(socketPath),
		monotonicNow: monotonicClock,
	}
	if watchdogPID := lookup("WATCHDOG_PID"); watchdogPID != "" && watchdogPID != strconv.Itoa(pid) {
		return supervisor
	}
	if microseconds, err := strconv.ParseInt(lookup("WATCHDOG_USEC"), 10, 64); err == nil && microseconds > 0 {
		supervisor.watchdogInterval = time.Duration(microseconds) * time.Microsecond
	}
	return supervisor
}

func (s *systemdSupervisor) ready(ctx context.Context) error {
	return s.notify(ctx, systemdReadyMessage)
}

// beginStopping keeps the watchdog fed through the drain, so systemd's stop
// timeout, not the watchdog, limits shutdown.
func (s *systemdSupervisor) beginStopping(ctx context.Context) error {
	s.stopping.Store(true)
	return s.notify(ctx, systemdStoppingMessage)
}

// reload brackets the reload with RELOADING=1 and READY=1, as
// Type=notify-reload requires. READY=1 follows even a failed reload, since the
// running configuration stays in force. Notification failures never block the
// reload.
func (s *systemdSupervisor) reload(ctx context.Context, reload func(context.Context) error) error {
	if s == nil {
		return reload(ctx)
	}
	if message, err := s.reloadingMessage(); err == nil {
		_ = s.notify(ctx, message)
	}
	err := reload(ctx)
	_ = s.ready(ctx)
	return err
}

func (s *systemdSupervisor) reloadingMessage() (string, error) {
	now, err := s.monotonicNow()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("RELOADING=1\nMONOTONIC_USEC=%d", now.Microseconds()), nil
}

// keepAlive sends WATCHDOG=1 every half interval, but only if the node was
// ready at some sample since the previous keep-alive, or is stopping. Sampling
// several times per interval rides out a short dependency blip; a node that
// stays unready, or whose readiness check wedges, goes quiet and systemd
// restarts it.
func (s *systemdSupervisor) keepAlive(done <-chan struct{}, ready func() bool) {
	if s == nil || s.watchdogInterval <= 0 {
		return
	}
	petEvery := s.watchdogInterval / 2
	sampleEvery := min(systemdWatchdogSampleLimit, petEvery/4)
	if sampleEvery <= 0 {
		sampleEvery = petEvery
	}
	pets := time.NewTicker(petEvery)
	defer pets.Stop()
	samples := time.NewTicker(sampleEvery)
	defer samples.Stop()
	gate := watchdogGate{}
	gate.sample(ready)
	for {
		select {
		case <-done:
			return
		case <-samples.C:
			gate.sample(ready)
		case <-pets.C:
			if !gate.due(s.stopping.Load()) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), petEvery)
			_ = s.notify(ctx, systemdWatchdogMessage)
			cancel()
		}
	}
}

type watchdogGate struct {
	observedReady bool
}

func (g *watchdogGate) sample(ready func() bool) {
	if !g.observedReady && ready() {
		g.observedReady = true
	}
}

// due also resets the gate for the next interval.
func (g *watchdogGate) due(stopping bool) bool {
	due := g.observedReady || stopping
	g.observedReady = false
	return due
}

func monotonicClock() (time.Duration, error) {
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		return 0, fmt.Errorf("read monotonic clock: %w", err)
	}
	return time.Duration(now.Nano()), nil
}

func systemdNotifier(socketPath string) func(context.Context, string) error {
	return func(ctx context.Context, message string) error {
		connection, err := (&net.Dialer{}).DialContext(ctx, "unixgram", socketPath)
		if err != nil {
			return fmt.Errorf("connect to systemd notification socket: %w", err)
		}
		return sendSystemdNotification(ctx, connection, systemdNotificationTimeout, message)
	}
}

func sendSystemdNotification(
	ctx context.Context,
	connection systemdNotificationConnection,
	timeout time.Duration,
	message string,
) (resultErr error) {
	defer func() {
		if err := connection.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close systemd notification socket: %w", err))
		}
	}()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("send systemd notification: %w", err)
	}
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("bound systemd notification: %w", err)
	}

	written, err := connection.Write([]byte(message))
	if err != nil {
		return fmt.Errorf("send systemd notification: %w", err)
	}
	if written != len(message) {
		return fmt.Errorf("send systemd notification: %w", io.ErrShortWrite)
	}
	return nil
}
