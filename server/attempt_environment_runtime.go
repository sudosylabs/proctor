// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/executionhost"
	"github.com/sudosylabs/proctor/server/store"
)

// attemptEnvironments is the node's execution environment runtime: the
// cluster waker, the owner runtime and the execution host clients. It is
// inert until Start; Close stops the owners, then the waker, then the host
// clients. Machines keep running on their hosts for the next owner.
type attemptEnvironments struct {
	waker   *environmentWaker
	runtime *environment.Runtime
	hosts   *executionhost.Hosts
}

// attemptEnvironmentInputs is what the execution environment runtime takes
// from construction.
type attemptEnvironmentInputs struct {
	persistence store.Catalog
	cluster     borrowedCluster
	logger      *logging.Logger
	nodeID      string
	// allowUnisolated accepts hosts that share the machine's kernel, for
	// development; dial, when set, replaces dialing a host.
	allowUnisolated bool
	dial            executionhost.Dialer
}

// prepareAttemptEnvironments builds the waker and the execution host
// clients the application needs, or nil when the installation runs no
// execution environments. Nothing is dialed until a host is used.
func prepareAttemptEnvironments(snapshot config.Config, inputs attemptEnvironmentInputs) (*attemptEnvironments, error) {
	if !snapshot.Execution.Enabled || inputs.persistence.AttemptEnvironment() == nil {
		return nil, nil
	}
	logger := inputs.logger
	waker, err := newEnvironmentWaker(inputs.cluster, func(err error) {
		logger.Warn("could not broadcast an attempt environment wake", logging.Err(err))
	})
	if err != nil {
		return nil, fmt.Errorf("construct attempt environment waker: %w", err)
	}
	hosts, err := executionhost.New(executionhost.Config{Hosts: snapshot.Execution.Hosts, Dial: inputs.dial,
		AllowUnisolated: inputs.allowUnisolated || inputs.dial != nil})
	if err != nil {
		return nil, fmt.Errorf("construct execution hosts: %w", err)
	}
	return &attemptEnvironments{waker: waker, hosts: hosts}, nil
}

// attach gives the application the waker, its diagnostics and the hosts
// its terminals reach machines through.
func (e *attemptEnvironments) attach(deps *app.Dependencies, inputs attemptEnvironmentInputs) {
	if e == nil {
		return
	}
	deps.EnvironmentWaker = e.waker
	deps.EnvironmentDiagnostics = environmentReporter(inputs)
	deps.EnvironmentHosts = e.hosts
}

// complete builds the owners and their runtime over the application.
func (e *attemptEnvironments) complete(
	snapshot config.Config,
	inputs attemptEnvironmentInputs,
	application *app.App,
) error {
	if e == nil {
		return nil
	}
	hosts := e.hosts
	workspace, err := app.NewEnvironmentWorkspace(application.Attempts)
	if err != nil {
		return fmt.Errorf("construct attempt environment workspace: %w", err)
	}
	report := environmentReporter(inputs)
	owners, err := environment.NewOwners(environment.OwnerDependencies{
		Environments: inputs.persistence.AttemptEnvironment(),
		Evidence:     inputs.persistence.ExamAttemptWorkspace(),
		Workspace:    workspace,
		Hosts:        hosts,
		Report:       report,
	})
	if err != nil {
		return fmt.Errorf("construct attempt environment owners: %w", err)
	}
	runtime, err := environment.NewRuntime(environment.RuntimeConfig{
		Leases:   inputs.persistence.AttemptRuntimeLease(),
		NodeID:   inputs.nodeID,
		NewOwner: owners,
		Owners:   snapshot.Execution.Owners,
		Report:   report,
	})
	if err != nil {
		return fmt.Errorf("construct attempt environment runtime: %w", err)
	}
	e.waker.attach(runtime)
	e.runtime = runtime
	return nil
}

func environmentReporter(inputs attemptEnvironmentInputs) func(model.ExamAttemptID, error) {
	logger := inputs.logger
	return func(attemptID model.ExamAttemptID, err error) {
		logger.Warn("attempt environment failure",
			logging.String("exam_attempt_id", attemptID.String()), logging.Err(err))
	}
}

// Start begins broadcasting wakes and owning environments.
func (e *attemptEnvironments) Start(ctx context.Context) error {
	if err := e.waker.Start(ctx); err != nil {
		return err
	}
	return e.runtime.Start(ctx)
}

// Close stops the owners, the waker and the host clients.
func (e *attemptEnvironments) Close(ctx context.Context) error {
	return errors.Join(e.runtime.Close(ctx), e.waker.Close(ctx), e.hosts.Close())
}
