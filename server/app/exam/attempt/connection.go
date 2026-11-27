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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ConnectCommand asks to admit the caller's registered desktop to a sitting.
// It is idempotent by IdempotencyKey.
type ConnectCommand struct {
	Security                         model.ConnectSecurity
	SittingID                        model.ExamSittingID
	ContinuityCredential             string
	ConfigurationManifestFingerprint string
	InitialConfiguration             *model.AttemptConfigurationCandidate
	IdempotencyKey                   string
}

// ConnectionResult is the admitted attempt and what the desktop applies; it
// also reports which sessions the admission revoked.
type ConnectionResult struct {
	Access                    CandidateConnectionAccess `json:"-"`
	Security                  model.AdmittedSecurity
	Attempt                   model.ExamAttempt
	Workspace                 model.ExamAttemptWorkspace
	Participation             store.ExamAttemptParticipationView
	Connection                model.AttemptConnection
	ClassID                   model.ClassID
	Configuration             model.AttemptConfiguration
	RuntimeCapabilities       store.CandidateRuntimeCapabilities
	BrowserPolicy             *store.CandidateBrowserPolicy
	LiveCorrections           []model.CandidateLiveCorrection
	FirstAdmission            bool
	ConnectionOpened          bool
	RevokedSessionIDs         []model.SessionID
	RevokedAccessTokenDigests []string
	Replayed                  bool
}

// ConnectExamAttempt idempotently admits the caller's registered, compatible
// desktop, creating the attempt on first admission or resuming it, and opens
// a new participation and connection. It checks only what the request
// carries; the store re-decides everything that depends on current rows,
// including the security preflight, in one locking transaction against
// database time.
func (s *Service) ConnectExamAttempt(
	ctx context.Context,
	inv invocation.Invocation,
	command ConnectCommand,
) (_ ConnectionResult, resultErr error) {
	defer func() { s.recordOperational("connect", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if command.Security.Validate() != nil {
		return ConnectionResult{}, apperror.Invalid("exam.attempt.invalid", "security")
	}

	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return ConnectionResult{}, apperror.New("authentication.token_invalid")
	}
	if !principal.HasRegisteredDesktopKey() {
		return ConnectionResult{}, apperror.New("exam.attempt.registered_desktop_required")
	}
	if !command.SittingID.IsValid() {
		return ConnectionResult{}, apperror.Invalid("exam.attempt.invalid", "exam_sitting_id")
	}
	if !model.IsValidCredentialToken(command.ContinuityCredential) {
		return ConnectionResult{}, apperror.Invalid("exam.attempt.invalid", "continuity_credential")
	}

	build, err := s.desktopBuilds.ResolveAttemptDesktopBuild(ctx, principal)
	if err != nil || build.Build.Validate() != nil || build.CompatibilityPolicyRevision < 1 {
		return ConnectionResult{}, apperror.New("exam.attempt.desktop_incompatible").Wrap(err)
	}
	if err = validateConnectConfiguration(command, build.Build); err != nil {
		return ConnectionResult{}, err
	}

	commandIdempotency, err := prepareConnectIdempotency(inv, command)
	if err != nil {
		return ConnectionResult{}, err
	}

	snapshot, err := s.sittings.Resolve(ctx, command.SittingID)
	if err != nil {
		return ConnectionResult{}, storeError(storeErrors, err)
	}

	if snapshot == nil ||
		snapshot.Sitting == nil ||
		snapshot.Sitting.ID != command.SittingID ||
		!snapshot.Sitting.ExamID.IsValid() ||
		!snapshot.Sitting.ClassID.IsValid() {
		return ConnectionResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("incomplete Sitting ownership projection"))
	}

	sitting := snapshot.Sitting

	attemptID := s.newAttemptID()
	at := model.TimeUTC(s.now())
	workspaceID := s.newWorkspaceID()
	credentialHash := model.HashToken(command.ContinuityCredential)
	participationID, connectionID := s.newParticipationID(), s.newConnectionID()

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    sitting.ClassID.String(),
		Operation:  "exam_attempt_connect",
		Value:      map[string]any{"exam_sitting_id": command.SittingID.String()},
	})
	if err != nil {
		return ConnectionResult{}, err
	}

	stored, err := s.attempts.Connect(ctx, &store.ExamAttemptConnect{
		Security:                           command.Security,
		SittingID:                          command.SittingID,
		CandidateUserID:                    principal.UserID,
		SessionID:                          principal.SessionID,
		DesktopRegistrationID:              principal.DesktopRegistrationID,
		DPoPKeyThumbprint:                  principal.DPoPKeyThumbprint,
		AttemptID:                          attemptID,
		WorkspaceID:                        workspaceID,
		ParticipationID:                    participationID,
		ConnectionID:                       connectionID,
		ContinuityCredentialHash:           credentialHash,
		ConfigurationManifestFingerprint:   command.ConfigurationManifestFingerprint,
		InitialConfiguration:               cloneAttemptConfigurationPointer(command.InitialConfiguration),
		DesktopBuild:                       build.Build,
		DesktopCompatibilityPolicyRevision: build.CompatibilityPolicyRevision,
		AuditEventID:                       auditID,
		AuditAt:                            at,
	}, commandIdempotency)
	if err != nil {
		return ConnectionResult{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectConnection(stored, sitting.ClassID)
	if err != nil {
		return ConnectionResult{}, err
	}

	result.Access = CandidateConnectionAccess{selector: candidateConnectionSelector(principal, result.Attempt.ID, result.Connection.ID, credentialHash)}
	if result.ConnectionOpened && !result.Replayed {
		if effectErr := s.publishConnectionOpened(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_connection_opened", effectErr)
		}
	}

	return result, nil
}

func validateConnectConfiguration(command ConnectCommand, build model.DesktopBuildTuple) error {
	if !model.IsValidSHA256Fingerprint(command.ConfigurationManifestFingerprint) ||
		command.ConfigurationManifestFingerprint != build.AttemptConfigurationManifestFingerprint {
		return apperror.New("exam.attempt.configuration_unsupported")
	}

	if command.InitialConfiguration != nil &&
		(command.InitialConfiguration.Validate() != nil ||
			command.InitialConfiguration.ManifestFingerprint != build.AttemptConfigurationManifestFingerprint ||
			build.ConfigurationManifest.ValidateSelection(
				command.InitialConfiguration.ApprovedCommands,
				command.InitialConfiguration.ApprovedKeybindings,
			) != nil) {
		return apperror.Invalid("exam.attempt.invalid", "initial_configuration")
	}

	return nil
}

func cloneAttemptConfigurationPointer(value *model.AttemptConfigurationCandidate) *model.AttemptConfigurationCandidate {
	if value == nil {
		return nil
	}
	clone := value.Clone()
	return &clone
}

// CloseConnectionCommand names one of the caller's connections and why it is
// closing.
type CloseConnectionCommand struct {
	AttemptID    model.ExamAttemptID
	SittingID    model.ExamSittingID
	ClassID      model.ClassID
	ConnectionID model.AttemptConnectionID
	Reason       model.AttemptConnectionCloseReason
}

// ConnectionClosedResult reports in Changed whether this close changed state
// rather than finding the connection already closed.
type ConnectionClosedResult struct {
	AttemptID       model.ExamAttemptID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
	Connection      model.AttemptConnection
	Changed         bool
}

// CloseExamAttemptConnection publishes post-commit effects only when the
// close changed state.
func (s *Service) CloseExamAttemptConnection(
	ctx context.Context,
	inv invocation.Invocation,
	command CloseConnectionCommand,
) (_ ConnectionClosedResult, resultErr error) {
	defer func() { s.recordOperational("close", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if inv.Principal().Validate() != nil ||
		!command.AttemptID.IsValid() ||
		!command.SittingID.IsValid() ||
		!command.ClassID.IsValid() ||
		!command.ConnectionID.IsValid() ||
		!command.Reason.IsValid() {
		return ConnectionClosedResult{}, apperror.Invalid("exam.attempt.invalid", "connection")
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    command.ClassID.String(),
		Operation:  "exam_attempt_connection_close",
		Value:      map[string]any{"exam_attempt_id": command.AttemptID.String(), "attempt_connection_id": command.ConnectionID.String()},
	})
	if err != nil {
		return ConnectionClosedResult{}, err
	}

	stored, err := s.attempts.CloseConnection(ctx, &store.ExamAttemptConnectionClose{
		ConnectionID:    command.ConnectionID,
		CandidateUserID: inv.Principal().UserID,
		SessionID:       inv.Principal().SessionID,
		Reason:          command.Reason,
		AuditEventID:    auditID,
		AuditAt:         model.TimeUTC(s.now()),
	})
	if err != nil {
		return ConnectionClosedResult{}, s.failAudit(ctx, auditID, err)
	}

	result := ConnectionClosedResult{
		AttemptID:       stored.AttemptID,
		ExamID:          stored.ExamID,
		SittingID:       stored.SittingID,
		CandidateUserID: stored.CandidateUserID,
		Connection:      *stored.Connection,
		Changed:         stored.Changed,
	}
	if result.Changed {
		if effectErr := s.publishConnectionClosed(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_connection_closed", effectErr)
		}
	}

	return result, nil
}

func projectConnection(stored *store.ExamAttemptConnectResult, classID model.ClassID) (ConnectionResult, error) {
	if !stored.Connected() {
		return ConnectionResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Exam Attempt connection outcome"))
	}

	if stored.Attempt.State != model.ExamAttemptActive ||
		stored.Participation.State != model.AttemptParticipationActive ||
		stored.Connection.State != model.AttemptConnectionOpen {
		return ConnectionResult{}, apperror.New("exam.attempt.connection_closed")
	}

	if !stored.ConfigurationConsistent() {
		return ConnectionResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Exam Attempt configuration outcome"))
	}

	revokedSessionIDs := append([]model.SessionID(nil), stored.RevokedSessionIDs...)
	return ConnectionResult{
		Security:                  stored.Security,
		Attempt:                   *stored.Attempt,
		Workspace:                 *stored.Workspace,
		Participation:             *stored.Participation,
		Connection:                *stored.Connection,
		ClassID:                   classID,
		FirstAdmission:            stored.FirstAdmission,
		Configuration:             stored.Configuration.Clone(),
		RuntimeCapabilities:       stored.RuntimeCapabilities,
		BrowserPolicy:             cloneCandidateBrowserPolicy(stored.BrowserPolicy),
		LiveCorrections:           model.CloneCandidateLiveCorrections(stored.LiveCorrections),
		ConnectionOpened:          stored.ConnectionOpened,
		RevokedSessionIDs:         revokedSessionIDs,
		RevokedAccessTokenDigests: append([]string(nil), stored.RevokedAccessTokenDigests...),
		Replayed:                  stored.Replayed,
	}, nil
}
