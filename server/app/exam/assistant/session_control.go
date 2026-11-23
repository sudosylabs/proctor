// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SessionCommand identifies a session within the candidate's attempt.
type SessionCommand struct {
	Access attempt.CandidateAccess
	Tag    string
}

func sessionEnvironmentAccess(access attempt.CandidateAccess) environment.CandidateAccess {
	return environment.CandidateAccess{AttemptID: access.AttemptID, ConnectionID: access.ConnectionID,
		ContinuityCredential: access.ContinuityCredential}
}

func sessionNotFound() error { return apperror.New("exam.attempt.assistant_session_not_found") }

// ListSessions reconciles the candidate's live sessions before returning them.
func (s *Service) ListSessions(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess) (
	_ []model.AssistantSession, resultErr error,
) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if s == nil {
		return nil, unavailable()
	}
	candidate, err := s.attempts.VerifyAssistantCandidate(ctx, inv, access)
	if err != nil {
		return nil, err
	}
	rows, err := s.reconcileSessions(ctx, inv, sessionEnvironmentAccess(access), candidate.ExamID, candidate.SittingID, candidate.Editor, "")
	if err != nil {
		return nil, storeFailure(err)
	}
	return rows, nil
}

// StopSession stops the program, or returns the first recorded outcome.
func (s *Service) StopSession(ctx context.Context, inv invocation.Invocation, command SessionCommand) (
	_ model.AssistantSession, resultErr error,
) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if s == nil {
		return model.AssistantSession{}, unavailable()
	}
	candidate, session, err := s.candidateSession(ctx, inv, command)
	if err != nil || session.State == "ended" {
		return session, err
	}
	auditID, err := s.beginControlAudit(ctx, inv, candidate, "assistant_session_stop")
	if err != nil {
		return model.AssistantSession{}, err
	}
	output, err := s.terminals.StopAssistantSession(ctx, inv, sessionEnvironmentAccess(command.Access),
		model.AttemptEnvironmentProcessTag(command.Tag), session.ReadOffset)
	outcome := "stopped"
	if err != nil {
		switch {
		case errors.Is(err, environment.ErrEnvironmentNotRunning), errors.Is(err, environment.ErrNoEnvironment),
			errors.Is(err, environment.ErrTerminalNotFound), errors.Is(err, environment.ErrTerminalEnded):
			outcome = "ended"
		default:
			return model.AssistantSession{}, s.failAudit(ctx, auditID, err)
		}
	}
	end := store.AssistantSessionEnd{Editor: candidate.Editor, Tag: session.Tag, Outcome: outcome,
		AuditEventID: auditID, AuditAt: model.TimeUTC(s.now()),
		DurationMilliseconds: max(0, s.now().Sub(session.StartedAt).Milliseconds())}
	if output.Exit != nil {
		end.Signal = output.Exit.Signal
		if end.Signal == "" {
			end.ExitCode = &output.Exit.Code
		}
	}
	ended, err := s.transcripts.EndSession(ctx, &end)
	if err != nil {
		return model.AssistantSession{}, s.failAudit(ctx, auditID, err)
	}
	if ended.Changed {
		s.publishSessionEnded(ctx, candidate.ExamID, candidate.SittingID, candidate.Editor.AttemptID, session.CandidateUserID, ended)
	}
	rows, err := s.transcripts.ListSessions(ctx, store.AssistantSessionQuery{Tag: command.Tag, Owner: sessionOwner(candidate.Editor)})
	if err != nil {
		return model.AssistantSession{}, storeFailure(err)
	}
	if len(rows) != 1 {
		return model.AssistantSession{}, sessionNotFound()
	}
	return rows[0], nil
}

// ViewSession returns the current screen through a read-only candidate call.
func (s *Service) ViewSession(ctx context.Context, inv invocation.Invocation, command SessionCommand) (
	_ []string, resultErr error,
) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if s == nil {
		return nil, unavailable()
	}
	_, session, err := s.candidateSession(ctx, inv, command)
	if err != nil {
		return nil, err
	}
	if session.State != "running" {
		return nil, sessionNotFound()
	}
	rows, err := s.terminals.ViewAssistantSession(ctx, inv, sessionEnvironmentAccess(command.Access), model.AttemptEnvironmentProcessTag(session.Tag))
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) candidateSession(ctx context.Context, inv invocation.Invocation, command SessionCommand) (
	attempt.AssistantCandidate, model.AssistantSession, error,
) {
	candidate, err := s.attempts.VerifyAssistantCandidate(ctx, inv, command.Access)
	if err != nil {
		return candidate, model.AssistantSession{}, err
	}
	if !model.ValidAssistantProcessTag(command.Tag) {
		return candidate, model.AssistantSession{}, sessionNotFound()
	}
	rows, err := s.reconcileSessions(ctx, inv, sessionEnvironmentAccess(command.Access), candidate.ExamID, candidate.SittingID, candidate.Editor, command.Tag)
	if err != nil {
		return candidate, model.AssistantSession{}, storeFailure(err)
	}
	if len(rows) != 1 || rows[0].Starting {
		return candidate, model.AssistantSession{}, sessionNotFound()
	}
	return candidate, rows[0], nil
}

func sessionOwner(editor store.ExamAttemptWorkspaceEditorAccess) store.AssistantTranscriptOwnerAccess {
	return store.AssistantTranscriptOwnerAccess{AttemptID: editor.AttemptID, CandidateUserID: editor.CandidateUserID, SessionID: editor.SessionID}
}
