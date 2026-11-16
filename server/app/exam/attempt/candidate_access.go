// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CandidateAccess names the Attempt and Connection a candidate request acts
// through and carries the continuity credential that proves it; only the
// credential's hash reaches the Store.
type CandidateAccess struct {
	AttemptID            model.ExamAttemptID
	ConnectionID         model.AttemptConnectionID
	ContinuityCredential string
}

func candidateSelector(inv invocation.Invocation, candidateAccess CandidateAccess) (store.CandidateAttemptAccess, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return store.CandidateAttemptAccess{}, apperror.New("authentication.token_invalid")
	}
	if !principal.HasRegisteredDesktopKey() {
		return store.CandidateAttemptAccess{}, apperror.New("exam.attempt.registered_desktop_required")
	}
	if !candidateAccess.AttemptID.IsValid() || !candidateAccess.ConnectionID.IsValid() || !model.IsValidCredentialToken(candidateAccess.ContinuityCredential) {
		return store.CandidateAttemptAccess{}, apperror.Invalid("exam.attempt.invalid", "candidate_access")
	}

	return store.CandidateAttemptAccess{
		AttemptID:                candidateAccess.AttemptID,
		CandidateUserID:          principal.UserID,
		SessionID:                principal.SessionID,
		DesktopRegistrationID:    principal.DesktopRegistrationID,
		DPoPKeyThumbprint:        principal.DPoPKeyThumbprint,
		ConnectionID:             candidateAccess.ConnectionID,
		ContinuityCredentialHash: model.HashToken(candidateAccess.ContinuityCredential),
	}, nil
}

// VerifiedCandidateAccess is what VerifyCandidateAttemptAccess proved: the
// candidate's active Attempt, its Sitting and Class, and the current
// Participation generation the Connection belongs to. It carries no
// credential.
type VerifiedCandidateAccess struct {
	AttemptID       model.ExamAttemptID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

// VerifyCandidateAttemptAccess runs the checks every candidate Attempt read
// runs, and nothing else: a Desktop Session with its registered key, the
// Attempt's open Connection and continuity credential, an active
// Participation with an unexpired lease, an active Attempt and a Sitting
// that is open or paused before its end. It writes nothing. The candidate's
// terminals call it before they act, and again while a terminal socket
// stays open, so a sealed Attempt or an ended Participation ends them.
func (s *Service) VerifyCandidateAttemptAccess(
	ctx context.Context,
	inv invocation.Invocation,
	candidateAccess CandidateAccess,
) (_ VerifiedCandidateAccess, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := candidateSelector(inv, candidateAccess)
	if err != nil {
		return VerifiedCandidateAccess{}, err
	}

	target, err := s.attempts.ResolveCandidateAccess(ctx, selector)
	if err != nil {
		return VerifiedCandidateAccess{}, storeError(storeErrors, err)
	}
	if !target.ConsistentWith(selector) {
		return VerifiedCandidateAccess{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent candidate Attempt access"))
	}

	return VerifiedCandidateAccess{
		AttemptID:       target.AttemptID,
		SittingID:       target.SittingID,
		ClassID:         target.ClassID,
		CandidateUserID: target.CandidateUserID,
		ParticipationID: target.ParticipationID,
		Generation:      target.Generation,
	}, nil
}

// screenClipUploadSelector is candidateSelector for a clip upload, which may
// also continue without the Attempt credential and Connection once the
// Participation that took the clip has closed. The Store decides whether it
// may; omitting both never grants that by itself.
func screenClipUploadSelector(inv invocation.Invocation, candidateAccess CandidateAccess) (store.CandidateAttemptAccess, error) {
	if candidateAccess.ConnectionID != "" || candidateAccess.ContinuityCredential != "" {
		return candidateSelector(inv, candidateAccess)
	}
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return store.CandidateAttemptAccess{}, apperror.New("authentication.token_invalid")
	}
	if !principal.HasRegisteredDesktopKey() {
		return store.CandidateAttemptAccess{}, apperror.New("exam.attempt.registered_desktop_required")
	}
	if !candidateAccess.AttemptID.IsValid() {
		return store.CandidateAttemptAccess{}, apperror.Invalid("exam.attempt.invalid", "candidate_access")
	}
	return store.CandidateAttemptAccess{
		AttemptID: candidateAccess.AttemptID, CandidateUserID: principal.UserID, SessionID: principal.SessionID,
		DesktopRegistrationID: principal.DesktopRegistrationID, DPoPKeyThumbprint: principal.DPoPKeyThumbprint,
	}, nil
}
