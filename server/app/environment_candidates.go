// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
)

// environmentCandidates is the terminals' candidate verification over the
// Attempt service's candidate access check.
type environmentCandidates struct {
	attempts *attempt.Service
}

func (c environmentCandidates) VerifyCandidate(
	ctx context.Context,
	inv invocation.Invocation,
	access environment.CandidateAccess,
) (environment.CandidateAttempt, error) {
	verified, err := c.attempts.VerifyCandidateAttemptAccess(ctx, inv, attempt.CandidateAccess{
		AttemptID: access.AttemptID, ConnectionID: access.ConnectionID, ContinuityCredential: access.ContinuityCredential,
	})
	if err != nil {
		return environment.CandidateAttempt{}, err
	}
	return environment.CandidateAttempt{AttemptID: verified.AttemptID, CandidateUserID: verified.CandidateUserID}, nil
}

// newTerminals builds the candidates' terminals, or nil when the
// installation runs no execution environments.
func newTerminals(deps Dependencies, attempts *attempt.Service) (*environment.Terminals, error) {
	if deps.EnvironmentHosts == nil || deps.Store.AttemptEnvironment() == nil || attempts == nil {
		return nil, nil
	}
	return environment.NewTerminals(environment.TerminalsDependencies{
		Environments: deps.Store.AttemptEnvironment(),
		Evidence:     deps.Store.ExamAttemptWorkspace(),
		Hosts:        deps.EnvironmentHosts,
		Candidates:   environmentCandidates{attempts: attempts},
		Report:       deps.EnvironmentDiagnostics,
	})
}
