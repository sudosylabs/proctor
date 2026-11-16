// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
)

// attemptCandidates checks a terminal's candidate through the attempt
// service; every terminal call repeats the check.
type attemptCandidates struct {
	attempts *attempt.Service
}

// AttemptCandidates returns the CandidateVerifier backed by the attempt service.
func AttemptCandidates(attempts *attempt.Service) CandidateVerifier {
	return attemptCandidates{attempts: attempts}
}

func (c attemptCandidates) VerifyCandidate(
	ctx context.Context,
	inv invocation.Invocation,
	access CandidateAccess,
) (CandidateAttempt, error) {
	verified, err := c.attempts.VerifyCandidateAttemptAccess(ctx, inv, attempt.CandidateAccess{
		AttemptID: access.AttemptID, ConnectionID: access.ConnectionID, ContinuityCredential: access.ContinuityCredential,
	})
	if err != nil {
		return CandidateAttempt{}, err
	}
	return CandidateAttempt{AttemptID: verified.AttemptID, CandidateUserID: verified.CandidateUserID}, nil
}
