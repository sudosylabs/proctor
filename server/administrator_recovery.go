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

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// AdministratorRecoveryCommand is the narrow module-root contract consumed by
// the host CLI. Password must come from a private input channel, never argv.
type AdministratorRecoveryCommand struct {
	InstitutionID    string
	UserID           string
	EnableLocalLogin bool
	Password         string
}

type AdministratorRecoveryResult struct {
	LocalLoginEnabled bool
	PasswordRotated   bool
}

// RecoverAdministratorAccess constructs an inert server graph, invokes its
// host-only recovery capability, and closes every acquired resource without
// ever starting workers, listeners, or transports. It never migrates and
// refuses a schema that differs from this build.
func RecoverAdministratorAccess(
	ctx context.Context,
	configPath string,
	command AdministratorRecoveryCommand,
) (_ *AdministratorRecoveryResult, resultErr error) {
	options := []Option{withCurrentSchemaRequired()}
	if configPath != "" {
		options = append(options, WithConfigPath(configPath))
	}

	node, err := New(ctx, options...)
	if err != nil {
		return nil, err
	}

	defer func() { resultErr = errors.Join(resultErr, node.Close()) }()
	return node.RecoverAdministratorAccess(ctx, command)
}

// RecoverAdministratorAccess runs only while the constructed node remains
// inert. Running the node permanently closes this host-only capability.
func (s *Server) RecoverAdministratorAccess(
	ctx context.Context,
	command AdministratorRecoveryCommand,
) (*AdministratorRecoveryResult, error) {
	if s == nil {
		return nil, errors.New("server is nil")
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.state != nodeInert {
		return nil, errors.New("administrator recovery requires an inert server")
	}

	recovery := s.components.administratorRecovery
	if recovery == nil {
		return nil, errors.New("administrator recovery is unavailable")
	}

	result, err := recovery.RecoverAdministratorAccess(ctx, invocation.Task("administrator-recovery"), identity.AdministratorRecoveryCommand{
		InstitutionID:    model.InstitutionID(command.InstitutionID),
		UserID:           model.UserID(command.UserID),
		EnableLocalLogin: command.EnableLocalLogin,
		Password:         command.Password,
	})
	if err != nil {
		return nil, err
	}

	return &AdministratorRecoveryResult{
		LocalLoginEnabled: result.LocalLoginEnabled,
		PasswordRotated:   result.PasswordRotated,
	}, nil
}
