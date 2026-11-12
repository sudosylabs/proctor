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
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type attemptStoreFake struct {
	f                               *fixture
	screenClipRequest               *store.ExamAttemptScreenClipRequest
	screenClipErr                   error
	screenClipReplayed              bool
	screenClipUpload                *store.ExamAttemptScreenClipUploadStatus
	screenClipChunk                 *store.ExamAttemptScreenClipChunk
	connect                         *store.ExamAttemptConnect
	connectErr                      error
	getErr                          error
	replayed                        bool
	firstAdmission                  bool
	connectionOpened                bool
	connectClosed                   bool
	participationEnded              bool
	connectionSession               model.SessionID
	closeChanged                    bool
	attempt                         *model.ExamAttempt
	workspace                       *model.ExamAttemptWorkspace
	participation                   *store.ExamAttemptParticipationView
	connection                      *model.AttemptConnection
	presentation                    *store.CandidateExamPresentation
	candidateAccess                 store.CandidateAttemptAccess
	authorizedSession               model.SessionID
	managerOptions                  store.ExamAttemptManagerListOptions
	managerRows                     []store.ExamAttemptManagerSnapshot
	managerSuspension               *store.ExamAttemptSuspensionView
	latestParticipation             *store.ExamAttemptParticipationView
	renew                           *store.ExamAttemptParticipationRenewal
	renewResult                     *store.ExamAttemptParticipationRenewalResult
	renewErr                        error
	focusAccess                     store.ExamAttemptFocusLossAccess
	focusTarget                     *store.ExamAttemptFocusLossTarget
	focusSignal                     *store.ExamAttemptFocusLossSignal
	focusResult                     *store.ExamAttemptFocusLossResult
	focusErr                        error
	lateFocusTarget                 *store.ExamAttemptFocusLossDiscrepancyTarget
	lateFocusInput                  *store.ExamAttemptFocusLossDiscrepancy
	lateFocusResult                 *store.ExamAttemptFocusLossDiscrepancyResult
	lateFocusErr                    error
	expiryDue                       []store.ExamAttemptParticipationExpiryDue
	resolvedExpiry                  *store.ExamAttemptParticipationExpiryDue
	resolveExpiryErr                error
	expireInput                     *store.ExamAttemptParticipationExpiry
	expireResult                    *store.ExamAttemptParticipationExpiryResult
	expireErr                       error
	reallow                         *store.ExamAttemptReallow
	reallowResult                   *store.ExamAttemptReallowResult
	reallowErr                      error
	manualSuspension                *store.ExamAttemptManualSuspension
	manualSuspensionResult          *store.ExamAttemptManualSuspensionResult
	manualSuspensionErr             error
	correctionAcknowledgementTarget *store.ExamAttemptCorrectionAcknowledgementTarget
	correctionAcknowledgement       *store.ExamAttemptCorrectionAcknowledgement
	correctionAcknowledgementResult *store.ExamAttemptCorrectionAcknowledgementResult
	correctionAcknowledgementErr    error
	correctionAcknowledgementCalls  int
	idempotency                     *store.CommandIdempotency
}

func (f *attemptStoreFake) Connect(_ context.Context, input *store.ExamAttemptConnect, command *store.CommandIdempotency) (*store.ExamAttemptConnectResult, error) {
	f.f.order = append(f.f.order, "connect")
	f.connect, f.idempotency = input, command
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	var err error
	f.attempt, err = model.NewExamAttempt(input.AttemptID, f.f.sitting.ExamID, input.SittingID, input.CandidateUserID, f.f.revision.ID, f.f.at)
	if err != nil {
		return nil, err
	}
	f.workspace, err = model.NewExamAttemptWorkspace(input.WorkspaceID, input.AttemptID, f.f.at)
	if err != nil {
		return nil, err
	}
	f.participation = &store.ExamAttemptParticipationView{ID: input.ParticipationID, AttemptID: input.AttemptID,
		State: model.AttemptParticipationActive, Generation: 1, StartedAt: f.f.at, UpdatedAt: f.f.at,
		LeaseExpiresAt: f.f.at.Add(model.AttemptParticipationInitialLease)}
	if f.participationEnded {
		f.participation.State = model.AttemptParticipationEnded
		f.participation.UpdatedAt = f.f.at.Add(time.Second)
		f.participation.EndedAt = model.OptionalTimeFrom(f.f.at.Add(time.Second))
		f.participation.EndReason = model.AttemptParticipationEndInterrupted
	}
	connectionSession := input.SessionID
	if f.connectionSession.IsValid() {
		connectionSession = f.connectionSession
	}
	f.connection, err = model.NewAttemptConnection(input.ConnectionID, input.AttemptID, input.ParticipationID, connectionSession, f.f.at)
	if err != nil {
		return nil, err
	}
	if f.connectClosed {
		if err = f.connection.Close(model.AttemptConnectionCloseTransport, f.f.at.Add(time.Second)); err != nil {
			return nil, err
		}
	}
	resolved, err := model.ResolveNativePolicy(model.NativePolicyResolution{PolicyID: "fixture-policy", Revision: f.f.revision.ID.String(), Ordinal: 1, InstitutionID: model.NewInstitutionID(), ExamRevisionID: f.f.revision.ID, SittingID: f.f.sitting.ID, Scope: model.SecurityPolicyScope{Kind: "admission", AdmissionScopeID: input.Security.PreflightID}, IssuedAt: f.f.at, ActiveFrom: f.f.at, ActivationTime: f.f.at, Selections: model.DefaultExamPolicySet(), Build: f.f.desktopBuild})
	if err != nil {
		return nil, err
	}
	policy, err := resolved.Policy.RebindAttempt(input.AttemptID, f.f.at)
	if err != nil {
		return nil, err
	}
	security := model.AdmittedSecurity{Policy: policy, PolicyContentDigest: resolved.PolicyContentDigest, PreflightID: input.Security.PreflightID, PreflightReportDigest: input.Security.ReportDigest, PreflightPolicyDigest: resolved.Policy.Digest, ParticipationID: input.ParticipationID, Generation: 1, SecuritySessionID: "fixture-security-session", DeliveryStreamID: "fixture-delivery", RenewalIntervalSeconds: 5}
	configuration := f.f.configuration.Clone()
	return &store.ExamAttemptConnectResult{Security: security, Attempt: f.attempt, Workspace: f.workspace, Participation: f.participation,
		Connection: f.connection, ClassID: f.f.sitting.ClassID, FirstAdmission: f.firstAdmission,
		Configuration: configuration, RuntimeCapabilities: runtimeCapabilitiesFixture(f.f, f.f.sitting.ExamRevisionID), BrowserPolicy: disabledCandidateBrowserPolicyFixture(f.f.sitting.ExamRevisionID),
		ConnectionOpened: f.connectionOpened, Replayed: f.replayed}, nil
}

func (f *attemptStoreFake) RenewParticipation(_ context.Context, input *store.ExamAttemptParticipationRenewal) (*store.ExamAttemptParticipationRenewalResult, error) {
	f.f.order = append(f.f.order, "renew")
	f.renew = input
	return f.renewResult, f.renewErr
}

func (f *attemptStoreFake) ResolveFocusLossTarget(_ context.Context, focusLossAccess store.ExamAttemptFocusLossAccess) (*store.ExamAttemptFocusLossTarget, error) {
	f.f.order = append(f.f.order, "focus.resolve")
	f.focusAccess = focusLossAccess
	return f.focusTarget, f.focusErr
}

func (f *attemptStoreFake) RecordFocusLoss(_ context.Context, input *store.ExamAttemptFocusLossSignal) (*store.ExamAttemptFocusLossResult, error) {
	f.f.order = append(f.f.order, "focus.record")
	f.focusSignal = input
	return f.focusResult, f.focusErr
}

func (f *attemptStoreFake) ResolveEndedFocusLossTarget(_ context.Context, focusLossAccess store.ExamAttemptFocusLossAccess) (*store.ExamAttemptFocusLossDiscrepancyTarget, error) {
	f.f.order = append(f.f.order, "focus.late.resolve")
	f.focusAccess = focusLossAccess
	return f.lateFocusTarget, f.lateFocusErr
}

func (f *attemptStoreFake) RecordEndedFocusLoss(_ context.Context, input *store.ExamAttemptFocusLossDiscrepancy) (*store.ExamAttemptFocusLossDiscrepancyResult, error) {
	f.f.order = append(f.f.order, "focus.late.record")
	f.lateFocusInput = input
	return f.lateFocusResult, f.lateFocusErr
}

func (f *attemptStoreFake) ResolveParticipationExpiry(context.Context, model.ExamAttemptID, model.AttemptParticipationID, int64) (*store.ExamAttemptParticipationExpiryDue, error) {
	f.f.order = append(f.f.order, "expiry.resolve")
	return f.resolvedExpiry, f.resolveExpiryErr
}

func (f *attemptStoreFake) ListExpiredParticipations(_ context.Context, _ int) ([]store.ExamAttemptParticipationExpiryDue, error) {
	f.f.order = append(f.f.order, "expiry.list")
	return f.expiryDue, nil
}

func (f *attemptStoreFake) ExpireParticipation(_ context.Context, input *store.ExamAttemptParticipationExpiry) (*store.ExamAttemptParticipationExpiryResult, error) {
	f.f.order = append(f.f.order, "expiry.commit")
	f.expireInput = input
	return f.expireResult, f.expireErr
}

func (f *attemptStoreFake) ReallowAttempt(_ context.Context, input *store.ExamAttemptReallow, command *store.CommandIdempotency) (*store.ExamAttemptReallowResult, error) {
	f.f.order = append(f.f.order, "reallow")
	f.reallow, f.idempotency = input, command
	return f.reallowResult, f.reallowErr
}

func (f *attemptStoreFake) SuspendAttempt(_ context.Context, input *store.ExamAttemptManualSuspension, command *store.CommandIdempotency) (*store.ExamAttemptManualSuspensionResult, error) {
	f.f.order = append(f.f.order, "suspend")
	f.manualSuspension, f.idempotency = input, command
	return f.manualSuspensionResult, f.manualSuspensionErr
}

func (f *attemptStoreFake) RequestScreenClip(
	_ context.Context,
	input *store.ExamAttemptScreenClipRequest,
	command *store.CommandIdempotency,
) (*store.ExamAttemptScreenClipResult, error) {
	f.f.order = append(f.f.order, "screen_clip")
	f.screenClipRequest, f.idempotency = input, command
	if f.screenClipErr != nil {
		return nil, f.screenClipErr
	}
	return &store.ExamAttemptScreenClipResult{ExamID: input.ExamID, SittingID: input.SittingID, CandidateUserID: f.f.userID,
		Clip: store.NewExamAttemptScreenClipView(input.Clip), Replayed: f.screenClipReplayed}, nil
}

func (f *attemptStoreFake) ListPendingScreenClips(context.Context, store.CandidateAttemptAccess) ([]store.ExamAttemptScreenClipView, error) {
	return []store.ExamAttemptScreenClipView{}, nil
}

func (f *attemptStoreFake) BeginScreenClipUpload(context.Context, *store.ExamAttemptScreenClipUploadBegin) (*store.ExamAttemptScreenClipView, error) {
	return nil, store.NewErrNotFound("screen_clip", "")
}

func (f *attemptStoreFake) GetScreenClipUpload(
	_ context.Context,
	_ store.CandidateAttemptAccess,
	clipID model.ScreenClipID,
) (*store.ExamAttemptScreenClipUploadStatus, error) {
	if f.screenClipUpload == nil {
		return nil, store.NewErrNotFound("screen_clip", clipID.String())
	}
	return f.screenClipUpload, nil
}

func (f *attemptStoreFake) RecordScreenClipChunk(_ context.Context, input *store.ExamAttemptScreenClipChunk) error {
	f.screenClipChunk = input
	return nil
}

func (f *attemptStoreFake) CompleteScreenClipUpload(
	_ context.Context,
	_ store.CandidateAttemptAccess,
	clipID model.ScreenClipID,
) (*store.ExamAttemptScreenClipView, error) {
	return nil, store.NewErrNotFound("screen_clip", clipID.String())
}

func (f *attemptStoreFake) DeliverScreenClip(_ context.Context, input *store.ExamAttemptScreenClipDelivery) (*store.ExamAttemptScreenClipView, error) {
	return nil, store.NewErrNotFound("screen_clip", input.ClipID.String())
}

func (f *attemptStoreFake) CloseConnection(_ context.Context, input *store.ExamAttemptConnectionClose) (*store.ExamAttemptConnectionCloseResult, error) {
	f.f.order = append(f.f.order, "close")
	if input.CandidateUserID != f.connect.CandidateUserID || input.SessionID != f.connect.SessionID {
		return nil, store.NewErrNotFound("attempt_connection", input.ConnectionID.String())
	}
	connection := *f.connection
	if err := connection.Close(input.Reason, f.f.at.Add(time.Second)); err != nil {
		return nil, err
	}
	return &store.ExamAttemptConnectionCloseResult{AttemptID: f.attempt.ID, ExamID: f.attempt.ExamID, SittingID: f.connect.SittingID,
		CandidateUserID: f.connect.CandidateUserID, Connection: &connection, Changed: f.closeChanged}, nil
}

func (f *attemptStoreFake) Get(_ context.Context, examID model.ExamID, attemptID model.ExamAttemptID) (*store.ExamAttemptManagerSnapshot, error) {
	f.f.order = append(f.f.order, "manager.get")
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.attempt == nil || f.attempt.ExamID != examID || f.attempt.ID != attemptID {
		return nil, errors.New("not configured")
	}
	return &store.ExamAttemptManagerSnapshot{Attempt: f.attempt, ActiveSuspension: f.managerSuspension,
		LatestParticipation: f.latestParticipation}, nil
}

func (f *attemptStoreFake) List(_ context.Context, options store.ExamAttemptManagerListOptions) ([]store.ExamAttemptManagerSnapshot, error) {
	f.f.order = append(f.f.order, "manager.list")
	f.managerOptions = options
	return f.managerRows, nil
}

func (f *attemptStoreFake) ListCandidateActivity(context.Context, store.CandidateExamActivityListOptions) (*store.CandidateExamActivityPage, error) {
	return &store.CandidateExamActivityPage{ServerTime: model.NowUTC(), Items: []store.CandidateExamActivityItem{}}, nil
}

func (f *attemptStoreFake) ListSittingCandidateStatuses(context.Context, store.SittingCandidateStatusListOptions) (*store.SittingCandidateStatusPage, error) {
	return &store.SittingCandidateStatusPage{Items: []store.SittingCandidateStatusItem{}}, nil
}

func (*attemptStoreFake) ListSessionRevocationInvalidationTargets(
	context.Context,
	model.UserID,
	[]model.SessionID,
) ([]store.ExamAttemptInvalidationTarget, error) {
	return nil, nil
}

func (f *attemptStoreFake) GetCandidatePresentation(_ context.Context, candidateAccess store.CandidateAttemptAccess) (*store.CandidateExamPresentation, error) {
	f.candidateAccess = candidateAccess
	if f.authorizedSession.IsValid() && candidateAccess.SessionID != f.authorizedSession {
		return nil, store.NewErrNotFound("exam_attempt_access", candidateAccess.AttemptID.String())
	}
	return f.presentation, nil
}

func (f *attemptStoreFake) StartBrowserActivity(context.Context, *store.BrowserActivitySourceStart) (*model.BrowserSourceStatus, error) {
	return nil, nil
}

func (f *attemptStoreFake) AppendBrowserActivity(context.Context, *store.BrowserActivityAppend) (*model.BrowserActivityAcknowledgement, error) {
	return nil, nil
}

func (f *attemptStoreFake) ListBrowserActivity(context.Context, store.BrowserActivityListOptions) ([]store.BrowserActivityRecord, error) {
	return nil, nil
}

func (f *attemptStoreFake) ResolveCorrectionAcknowledgementTarget(_ context.Context, input store.ExamAttemptCorrectionAcknowledgement) (*store.ExamAttemptCorrectionAcknowledgementTarget, error) {
	f.f.order = append(f.f.order, "correction.acknowledgement.resolve")
	f.correctionAcknowledgement = &input
	return f.correctionAcknowledgementTarget, f.correctionAcknowledgementErr
}

func (f *attemptStoreFake) AcknowledgeCorrection(_ context.Context, input *store.ExamAttemptCorrectionAcknowledgement, command *store.CommandIdempotency) (*store.ExamAttemptCorrectionAcknowledgementResult, error) {
	f.f.order = append(f.f.order, "correction.acknowledgement.commit")
	f.correctionAcknowledgementCalls++
	f.correctionAcknowledgement, f.idempotency = input, command
	return f.correctionAcknowledgementResult, f.correctionAcknowledgementErr
}

func (*attemptStoreFake) ResolveCandidateResource(context.Context, store.CandidateAttemptAccess, model.ExamResourceID) (*store.CandidateResourceContent, error) {
	return nil, errors.New("not configured")
}

func (f *attemptStoreFake) PrepareSecurityPreflight(context.Context, *store.SecurityPreflightPrepare, *store.CommandIdempotency) (*store.SecurityPreflightPrepared, error) {
	return nil, errors.New("unexpected preflight preparation")
}

func (f *attemptStoreFake) ReportSecurityPreflight(context.Context, *store.SecurityPreflightReport, *store.CommandIdempotency) (*model.SecurityPreflightResult, error) {
	return nil, errors.New("unexpected preflight report")
}

func (f *attemptStoreFake) ResolveSecurityPreflightSitting(context.Context, string, model.UserID, model.SessionID) (model.ExamSittingID, error) {
	return "", errors.New("unexpected preflight lookup")
}

func (f *attemptStoreFake) RecoverSecurityPolicy(context.Context, store.SecurityPreflightAccess, model.ExamAttemptID) (*store.SecurityPolicyRecovery, error) {
	return nil, errors.New("unexpected security recovery")
}

func (f *attemptStoreFake) UpdateSecurityCoverage(context.Context, *store.ExamAttemptSecurityCoverageUpdate) (model.SecurityCoverageResult, error) {
	return model.SecurityCoverageResult{}, errors.New("unexpected security update")
}

func (f *attemptStoreFake) NativeDeliveryStatus(context.Context, store.NativeDeliveryAccess) (*model.NativeSecurityStreamStatus, error) {
	return nil, errors.New("unexpected NativeDeliveryStatus")
}

func (f *attemptStoreFake) NativeDeliveryReceipt(context.Context, store.NativeDeliveryAccess, int64) (*model.NativeBatchReceipt, error) {
	return nil, errors.New("unexpected NativeDeliveryReceipt")
}

func (f *attemptStoreFake) DeclareNativeDeliveryGaps(context.Context, *store.NativeDeliveryGapDeclaration, *store.CommandIdempotency) (*model.DeliveryGapReceipt, error) {
	return nil, errors.New("unexpected DeclareNativeDeliveryGaps")
}

func (f *attemptStoreFake) SealNativeDelivery(context.Context, *store.NativeDeliveryFinalDeclaration, *store.CommandIdempotency) (*model.NativeSecurityStreamStatus, error) {
	return nil, errors.New("unexpected SealNativeDelivery")
}

func (f *attemptStoreFake) UpdateNativeDeliverySummary(context.Context, *store.NativeDeliverySummaryUpdate, *store.CommandIdempotency) (*model.NativeSecurityStreamStatus, error) {
	return nil, errors.New("unexpected UpdateNativeDeliverySummary")
}

func (f *attemptStoreFake) ResolveNativeDeliveryTarget(context.Context, store.NativeDeliveryAccess) (*store.NativeDeliveryTarget, error) {
	return nil, errors.New("unexpected native delivery target")
}

func (f *attemptStoreFake) AppendNativeDelivery(context.Context, *store.NativeDeliveryAppend, *store.CommandIdempotency) (*model.NativeSecurityAcknowledgement, error) {
	return nil, errors.New("unexpected AppendNativeDelivery")
}

func (f *attemptStoreFake) BrowserSourceStatus(context.Context, store.BrowserDeliveryAccess) (*model.BrowserSourceStatus, error) {
	return nil, errors.New("unexpected browser source query")
}

func (f *attemptStoreFake) BrowserSourceList(context.Context, store.BrowserDeliveryAccess) ([]model.BrowserSourceStatus, error) {
	return nil, errors.New("unexpected browser source query")
}

func (f *attemptStoreFake) ResolveLiveDeliveryTarget(context.Context, store.CandidateAttemptAccess) (*store.DeliveryOwnerTarget, error) {
	return nil, errors.New("unexpected browser source target")
}

func (f *attemptStoreFake) AppendHistoricalBrowserDelivery(context.Context, *store.BrowserActivityAppend, *store.CommandIdempotency) (*model.BrowserActivityAcknowledgement, error) {
	return nil, errors.New("unexpected AppendHistoricalBrowserDelivery")
}

func (f *attemptStoreFake) ResolveBrowserDeliveryTarget(context.Context, store.BrowserDeliveryAccess) (*store.DeliveryOwnerTarget, error) {
	return nil, errors.New("unexpected ResolveBrowserDeliveryTarget")
}

func (f *attemptStoreFake) BrowserDeliveryReceipts(context.Context, store.BrowserDeliveryAccess, int64, int) (*model.BrowserReceiptPage, error) {
	return nil, errors.New("unexpected BrowserDeliveryReceipts")
}

func (f *attemptStoreFake) DeclareBrowserDeliveryGaps(context.Context, *store.BrowserDeliveryGapDeclaration, *store.CommandIdempotency) (*model.BrowserDeliveryGapResult, error) {
	return nil, errors.New("unexpected DeclareBrowserDeliveryGaps")
}

func (f *attemptStoreFake) SealBrowserDelivery(context.Context, *store.BrowserDeliveryFinalDeclaration, *store.CommandIdempotency) (*model.BrowserSourceStatus, error) {
	return nil, errors.New("unexpected SealBrowserDelivery")
}

func (f *attemptStoreFake) UpdateBrowserDeliverySummary(context.Context, *store.BrowserDeliverySummaryUpdate, *store.CommandIdempotency) (*model.BrowserDeliverySummaryResult, error) {
	return nil, errors.New("unexpected UpdateBrowserDeliverySummary")
}

func (f *attemptStoreFake) ListExpiredDeliveries(context.Context, int) ([]store.DeliveryExpiryDue, error) {
	return nil, nil
}

func (f *attemptStoreFake) ExpireDelivery(context.Context, *store.DeliveryExpiry) (bool, error) {
	return false, errors.New("unexpected ExpireDelivery")
}

func (f *attemptStoreFake) DeliveryBudget(context.Context, store.DeliveryBudgetAccess) (*model.DeliveryBudgetSnapshot, error) {
	return nil, errors.New("unexpected DeliveryBudget")
}

func (f *attemptStoreFake) StopDeliveryDetails(context.Context, *store.DeliveryDetailsStop, *store.CommandIdempotency) (*model.StopDeliveryDetailsResult, error) {
	return nil, errors.New("unexpected StopDeliveryDetails")
}
