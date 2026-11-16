// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package commands

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sudosylabs/proctor/server"
)

type serveExecutor func(context.Context, string) error

func newServeCommand(execute serveExecutor, text commandText) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: text.value("cli.serve.short", "Run the Proctor server", nil),
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			path, err := configPath(command, text)
			if err != nil {
				return err
			}
			return execute(command.Context(), path)
		},
	}
}

func serve(ctx context.Context, configPath string) error {
	var options []server.Option
	if configPath != "" {
		options = append(options, server.WithConfigPath(configPath))
	}
	supervisor := newSystemdSupervisor(os.Getenv, os.Getpid())
	if supervisor != nil {
		options = append(options,
			server.WithReadyNotifier(supervisor.ready),
			server.WithStoppingNotifier(supervisor.beginStopping),
		)
	}
	return serveNode(ctx, supervisor, func(ctx context.Context) (servingNode, error) {
		node, err := server.New(ctx, options...)
		if err != nil {
			return nil, err
		}
		return node, nil
	})
}

// servingNode is the part of *server.Server that serve drives.
type servingNode interface {
	Run(context.Context) error
	Reload(context.Context) error
	ForceStop()
	Ready() bool
}

// serveNode registers both signal handlers before construction, because the
// default SIGHUP action terminates the process and construction applies
// database migrations. A SIGHUP received before the node exists is held and
// runs once it does.
func serveNode(ctx context.Context, supervisor *systemdSupervisor, construct func(context.Context) (servingNode, error)) error {
	reloadSignals := make(chan os.Signal, 1)
	signal.Notify(reloadSignals, syscall.SIGHUP)
	defer signal.Stop(reloadSignals)
	stopSignals := make(chan os.Signal, 3)
	signal.Notify(stopSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopSignals)

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var nodeMu sync.Mutex
	var node servingNode
	escalationDone := make(chan struct{})
	served := make(chan struct{})
	go func() {
		defer close(escalationDone)
		escalateStopSignals(served, stopSignals, stopEscalation{
			stop: stopRun,
			force: func() {
				nodeMu.Lock()
				defer nodeMu.Unlock()
				if node != nil {
					node.ForceStop()
				}
			},
			exit: func() { os.Exit(immediateExitCode) },
		})
	}()
	defer func() {
		close(served)
		<-escalationDone
	}()

	constructed, err := construct(runCtx)
	if err != nil {
		return err
	}
	nodeMu.Lock()
	node = constructed
	nodeMu.Unlock()
	reloadCtx, stopReloads := context.WithCancel(runCtx)
	reloadsDone := make(chan struct{})
	go func() {
		defer close(reloadsDone)
		forwardReloadSignals(reloadCtx, reloadSignals, func(ctx context.Context) error {
			return supervisor.reload(ctx, constructed.Reload)
		})
	}()
	defer func() {
		stopReloads()
		<-reloadsDone
	}()
	keepAliveDone := make(chan struct{})
	keepAliveStopped := make(chan struct{})
	go func() {
		defer close(keepAliveStopped)
		supervisor.keepAlive(keepAliveDone, constructed.Ready)
	}()
	defer func() {
		close(keepAliveDone)
		<-keepAliveStopped
	}()
	return constructed.Run(runCtx)
}

// immediateExitCode is the status of a process ended by a third stop signal.
const immediateExitCode = 1

type stopEscalation struct {
	stop func()
	// force ends a graceful drain at once; Run then reports the forced stop.
	force func()
	exit  func()
}

// escalateStopSignals: the first SIGINT or SIGTERM drains gracefully, a second
// forces a bounded close, and a third exits at once.
func escalateStopSignals(done <-chan struct{}, signals <-chan os.Signal, escalation stopEscalation) {
	received := 0
	for {
		select {
		case <-done:
			return
		case <-signals:
			received++
			switch received {
			case 1:
				escalation.stop()
			case 2:
				escalation.force()
			default:
				escalation.exit()
				return
			}
		}
	}
}

// reloadSignalTimeout bounds one SIGHUP-triggered reload.
const reloadSignalTimeout = 30 * time.Second

// forwardReloadSignals drops reload errors: the server logs each outcome, and
// a failed reload keeps the running configuration and certificates in force.
func forwardReloadSignals(ctx context.Context, signals <-chan os.Signal, reload func(context.Context) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			reloadCtx, cancel := context.WithTimeout(ctx, reloadSignalTimeout)
			_ = reload(reloadCtx)
			cancel()
		}
	}
}
