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

// CandidateAccess names the attempt and connection a candidate request acts
// through, with the continuity credential that proves it. Only the
// credential's hash reaches the store.
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

	return candidateConnectionSelector(principal, candidateAccess.AttemptID, candidateAccess.ConnectionID,
		model.HashToken(candidateAccess.ContinuityCredential)), nil
}

// CandidateConnectionAccess retains the verified connection identity and a credential digest
// so an admitted socket can recheck participation without retaining the Desktop credential.
type CandidateConnectionAccess struct {
	selector store.CandidateAttemptAccess
}

func candidateConnectionSelector(
	principal model.Principal,
	attemptID model.ExamAttemptID,
	connectionID model.AttemptConnectionID,
	digest string,
) store.CandidateAttemptAccess {
	return store.CandidateAttemptAccess{
		AttemptID:                attemptID,
		CandidateUserID:          principal.UserID,
		SessionID:                principal.SessionID,
		DesktopRegistrationID:    principal.DesktopRegistrationID,
		DPoPKeyThumbprint:        principal.DPoPKeyThumbprint,
		ConnectionID:             connectionID,
		ContinuityCredentialHash: digest,
	}
}

// VerifiedCandidateAccess is what VerifyCandidateAttemptAccess proved. It
// carries no credential.
type VerifiedCandidateAccess struct {
	ExamID          model.ExamID
	AttemptID       model.ExamAttemptID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

// VerifyCandidateAttemptAccess runs the checks every candidate attempt read
// runs, and writes nothing. Terminals call it again while a socket stays open,
// so a sealed attempt or an ended participation ends them.
func (s *Service) VerifyCandidateAttemptAccess(
	ctx context.Context,
	inv invocation.Invocation,
	candidateAccess CandidateAccess,
) (VerifiedCandidateAccess, error) {
	selector, err := candidateSelector(inv, candidateAccess)
	if err != nil {
		return VerifiedCandidateAccess{}, publicFailures.Conceal(err)
	}

	return s.RecheckCandidateConnection(ctx, CandidateConnectionAccess{selector: selector})
}

// RecheckCandidateConnection resolves the live relationship behind a previously admitted connection.
func (s *Service) RecheckCandidateConnection(ctx context.Context, connection CandidateConnectionAccess) (_ VerifiedCandidateAccess, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector := connection.selector
	target, err := s.attempts.ResolveCandidateAccess(ctx, selector)
	if err != nil {
		return VerifiedCandidateAccess{}, storeError(storeErrors, err)
	}
	if !target.ConsistentWith(selector) {
		return VerifiedCandidateAccess{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent candidate Attempt access"))
	}

	return VerifiedCandidateAccess{
		ExamID:          target.ExamID,
		AttemptID:       target.AttemptID,
		SittingID:       target.SittingID,
		ClassID:         target.ClassID,
		CandidateUserID: target.CandidateUserID,
		ParticipationID: target.ParticipationID,
		Generation:      target.Generation,
	}, nil
}

// screenClipUploadSelector is candidateSelector for a clip upload, which may
// continue without the attempt credential and connection once the
// participation that took the clip has closed. The store decides whether it
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
