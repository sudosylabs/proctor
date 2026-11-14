// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
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
