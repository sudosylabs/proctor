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

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// AdministratorMFAResetCommand names the administrator whose MFA the host
// operator is resetting. It deliberately carries no proof, password or policy
// change.
type AdministratorMFAResetCommand struct {
	InstitutionID string
	UserID        string
}

type AdministratorMFAResetResult struct {
	RecoveryGeneration   int64
	ReenrollmentRequired bool
}

// ResetAdministratorMFA builds an inert node, runs the reset and closes it
// without starting workers or transports. The store refuses the reset while a
// serving node is live.
func ResetAdministratorMFA(
	ctx context.Context,
	configPath string,
	command AdministratorMFAResetCommand,
) (_ *AdministratorMFAResetResult, resultErr error) {
	options := []Option{withCurrentSchemaRequired()}
	if configPath != "" {
		options = append(options, WithConfigPath(configPath))
	}

	node, err := New(ctx, options...)
	if err != nil {
		return nil, err
	}

	defer func() { resultErr = errors.Join(resultErr, node.Close()) }()
	return node.ResetAdministratorMFA(ctx, command)
}

// ResetAdministratorMFA works only on an inert Server and is gone for good
// once Run starts. It runs as a task, never as an impersonated principal.
func (s *Server) ResetAdministratorMFA(ctx context.Context, command AdministratorMFAResetCommand) (*AdministratorMFAResetResult, error) {
	if s == nil {
		return nil, errors.New("server is nil")
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.state != nodeInert {
		return nil, errors.New("administrator MFA reset requires an inert server")
	}
	if s.components.administratorRecovery == nil {
		return nil, errors.New("administrator MFA reset is unavailable")
	}

	result, err := s.components.administratorRecovery.ResetAdministratorMFA(
		ctx,
		invocation.Task("administrator-mfa-reset"),
		model.InstitutionID(command.InstitutionID),
		model.UserID(command.UserID),
	)
	if err != nil {
		return nil, err
	}
	if result == nil || !result.ReenrollmentRequired || result.RecoveryGeneration < 1 {
		return nil, errors.New("administrator MFA reset returned no recovery restriction")
	}

	return &AdministratorMFAResetResult{RecoveryGeneration: result.RecoveryGeneration, ReenrollmentRequired: true}, nil
}
