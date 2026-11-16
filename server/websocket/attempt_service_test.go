// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// attemptFixture is the real attempt.Service a connection runs candidate
// commands on, over in-memory stores. The stores record what each command
// turned into and answer as a consistent database would, so a test asserts
// what reached the Store rather than what a fake application received.
type attemptFixture struct {
	t       testing.TB
	at      time.Time
	sitting *model.ExamSitting
	build   model.DesktopBuildTuple
	control *accesstest.Fixture
	store   *attemptStore
	service *attempt.Service
}

func newAttemptFixture(t testing.TB) *attemptFixture {
	t.Helper()
	at := time.Date(2026, time.August, 17, 10, 0, 0, 0, time.UTC)
	f := &attemptFixture{t: t, at: at, sitting: &model.ExamSitting{ID: model.NewExamSittingID(), ExamID: model.NewExamID(),
		ExamRevisionID: model.NewExamRevisionID(), ClassID: model.NewClassID(), State: model.ExamSittingOpen,
		ScheduledStartAt: at.Add(-time.Hour), ScheduledEndAt: at.Add(time.Hour), CreatedAt: at.Add(-2 * time.Hour),
		UpdatedAt: at, Revision: 1}}
	f.build = model.DesktopBuildTuple{DesktopRelease: "1.0.0", DesktopBuildID: "test-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16), DesktopTarget: "darwin-arm64",
		ConfigurationManifest: model.EmptyAttemptConfigurationManifest(), CapabilityMatrixIdentity: "websocket-test-matrix"}
	f.build.NativeAgreement = nativetest.NativeAgreement(t, f.build)
	f.store = &attemptStore{f: f, generation: 1, databaseTime: at, focusOutcome: model.IntegrityOutcomeFlagAndSuspend,
		admissions: map[[sha256.Size]byte]*store.ExamAttemptConnectResult{}, attempts: map[model.ExamAttemptID]*admittedAttempt{}}
	f.control = accesstest.New(t, accesstest.Options{Now: func() time.Time { return f.at }})
	publisher := &realtimetest.Publisher{}
	managers, err := sitting.New(sitting.Dependencies{
		Persistence: unusedSittings{}, Access: unusedExams{}, Memberships: unusedMemberships{},
		Authorizer: f.control.Authorizer, Auditor: f.control.Auditor, Publisher: publisher,
		Jobs: unusedLifecycleJobs{}, Mail: sitting.ScheduleMail{Composer: &mail.SittingComposer{},
			Revisions: unusedRevisions{}, Classes: unusedClasses{}},
		Now: func() time.Time { return f.at }, NewID: model.NewExamSittingID,
	})
	require.NoError(t, err)
	service, err := attempt.New(attempt.Dependencies{
		Persistence: f.store, FocusLoss: attemptFocusLoss{s: f.store}, SecurityPreflight: unusedSecurityPreflight{},
		NativeDelivery: unusedNativeDelivery{}, BrowserDelivery: attemptBrowserDelivery{s: f.store},
		DeliveryBudget: attemptDeliveryBudget{s: f.store}, Workspace: unusedWorkspaces{}, Submissions: unusedSubmissions{},
		Sittings: attemptSittings{f: f},
		Managers: attempt.SittingManagerAuthorizer{Sittings: managers, Submissions: unusedSubmissions{}, Audit: f.control.Auditor},
		Auditor:  f.control.Auditor, Publisher: publisher, Content: unusedContent{},
		// No candidate command a connection runs seals a Submission, so the
		// receipt mail is never composed.
		Mail:          attempt.SubmissionReceiptMail{Composer: &mail.Composer{}, Users: unusedUsers{}, Sittings: unusedSittings{}, Revisions: unusedRevisions{}},
		DesktopBuilds: desktopBuilds{f: f},
		Now:           func() time.Time { return f.at }, NewAttemptID: model.NewExamAttemptID, NewWorkspaceID: model.NewExamAttemptWorkspaceID,
		NewParticipation: model.NewAttemptParticipationID, NewConnection: model.NewAttemptConnectionID,
		NewEvidence: model.NewIntegrityEvidenceID, NewFlag: model.NewIntegrityFlagID, NewSuspension: model.NewAttemptSuspensionID,
		NewFocusLossSignal: model.NewFocusLossSignalID, NewDiscrepancy: model.NewIntegrityDiscrepancyID,
		NewWorkspaceEntry: model.NewAttemptWorkspaceEntryID, NewWorkspaceObject: model.NewAttemptWorkspaceObjectID,
		NewWorkspaceVersion: model.NewWorkspaceContentVersion, NewSubmission: model.NewSubmissionID,
		NewReceiptCode: model.NewSubmissionReceiptCode, NewDomainEvent: model.NewDomainEventID, NewJob: model.NewJobID,
	})
	require.NoError(t, err)
	f.service = service
	return f
}

// candidate is a fresh Desktop Session principal with a registered key: the
// only principal the Attempt service admits.
func (f *attemptFixture) candidate() model.Principal {
	principal := testCandidatePrincipal()
	principal.AuthenticatedAt = f.at
	return principal
}

// testCandidatePrincipal is a Desktop Session principal of the fixture's
// build, as Accept would be handed for a candidate.
func testCandidatePrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, DesktopRegistrationID: model.NewDesktopRegistrationID(),
		DPoPKeyThumbprint: strings.Repeat("A", 43), RegisteredDesktopKey: true,
		DesktopRelease: "1.0.0", DesktopBuildID: "test-build", DesktopPlatform: model.DesktopPlatformDarwin,
		DesktopArchitecture: model.DesktopArchitectureARM64, DesktopRealtimeProtocol: 1, AuthenticatedAt: time.Now()}
}

// admit stores an Attempt the principal already joined at generation, as an
// earlier connect would have, and binds runtime to it.
func (f *attemptFixture) admit(runtime *connectionRuntime, generation int64) *examAttemptBinding {
	f.t.Helper()
	admitted := &admittedAttempt{attemptID: model.NewExamAttemptID(), participationID: model.NewAttemptParticipationID(),
		connectionID: model.NewAttemptConnectionID(), candidate: runtime.principal.UserID, session: runtime.principal.SessionID,
		generation: generation}
	f.store.mu.Lock()
	f.store.attempts[admitted.attemptID] = admitted
	f.store.mu.Unlock()
	binding := &examAttemptBinding{attemptID: admitted.attemptID, sittingID: f.sitting.ID, classID: f.sitting.ClassID,
		connectionID: admitted.connectionID, participationID: admitted.participationID, generation: generation}
	runtime.mu.Lock()
	runtime.attempt = binding
	runtime.mu.Unlock()
	return binding
}

// attemptStore is the Exam Attempt Store. Connect admits once per
// idempotency key, so an exact replay finds the first admission again.
type attemptStore struct {
	store.ExamAttemptStore
	f *attemptFixture

	mu sync.Mutex
	// generation is the Participation generation the next admission grants;
	// runtimeCapabilities, when set, is the capability snapshot it reports.
	generation          int64
	runtimeCapabilities *store.CandidateRuntimeCapabilities
	// databaseTime is the database clock a renewal or Focus Loss reads.
	databaseTime time.Time
	renewErr     error
	// focusOutcome is the frozen Focus Loss policy a qualifying claim crosses.
	focusOutcome model.IntegrityOutcome
	// appendErr fails Browser Activity appends; appendGate, when set, holds
	// each append until the request's context ends.
	appendErr   error
	appendGate  *appendGate
	browserSeen int64
	// closeGate, when set, holds each Connection close until it returns.
	closeGate func(context.Context) error

	admissions map[[sha256.Size]byte]*store.ExamAttemptConnectResult
	attempts   map[model.ExamAttemptID]*admittedAttempt

	connects        []*store.ExamAttemptConnect
	connectCommands []*store.CommandIdempotency
	renewals        []*store.ExamAttemptParticipationRenewal
	focusSignals    []*store.ExamAttemptFocusLossSignal
	browserStarts   []*store.BrowserActivitySourceStart
	browserAppends  []*store.BrowserActivityAppend
	closes          []*store.ExamAttemptConnectionClose
	// closeContextErrs is each close's context state when it arrived.
	closeContextErrs []error
}

type admittedAttempt struct {
	attemptID       model.ExamAttemptID
	participationID model.AttemptParticipationID
	connectionID    model.AttemptConnectionID
	candidate       model.UserID
	session         model.SessionID
	generation      int64
}

// appendGate holds an append until its context ends; entered closes when
// one arrives and finished once it returned.
type appendGate struct {
	entered  chan struct{}
	finished chan struct{}
}

func newAppendGate() *appendGate {
	return &appendGate{entered: make(chan struct{}), finished: make(chan struct{})}
}

func (s *attemptStore) Connect(_ context.Context, input *store.ExamAttemptConnect, command *store.CommandIdempotency) (*store.ExamAttemptConnectResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connects = append(s.connects, input)
	s.connectCommands = append(s.connectCommands, command)
	if admitted, replay := s.admissions[command.KeyDigest]; replay {
		replayed := *admitted
		replayed.Replayed, replayed.FirstAdmission, replayed.ConnectionOpened = true, false, false
		return &replayed, nil
	}
	f := s.f
	examAttempt, err := model.NewExamAttempt(input.AttemptID, f.sitting.ExamID, input.SittingID, input.CandidateUserID, f.sitting.ExamRevisionID, f.at)
	if err != nil {
		return nil, err
	}
	workspace, err := model.NewExamAttemptWorkspace(input.WorkspaceID, input.AttemptID, f.at)
	if err != nil {
		return nil, err
	}
	participation := &store.ExamAttemptParticipationView{ID: input.ParticipationID, AttemptID: input.AttemptID,
		State: model.AttemptParticipationActive, Generation: s.generation, StartedAt: f.at, UpdatedAt: f.at,
		LeaseExpiresAt: f.at.Add(model.AttemptParticipationInitialLease)}
	connection, err := model.NewAttemptConnection(input.ConnectionID, input.AttemptID, input.ParticipationID, input.SessionID, f.at)
	if err != nil {
		return nil, err
	}
	resolved, err := model.ResolveNativePolicy(model.NativePolicyResolution{PolicyID: "fixture-policy",
		Revision: f.sitting.ExamRevisionID.String(), Ordinal: s.generation, InstitutionID: f.control.Institution.ID,
		ExamRevisionID: f.sitting.ExamRevisionID, SittingID: f.sitting.ID,
		Scope:    model.SecurityPolicyScope{Kind: "admission", AdmissionScopeID: input.Security.PreflightID},
		IssuedAt: f.at, ActiveFrom: f.at, ActivationTime: f.at, Selections: model.DefaultExamPolicySet(), Build: f.build})
	if err != nil {
		return nil, err
	}
	policy, err := resolved.Policy.RebindAttempt(input.AttemptID, f.at)
	if err != nil {
		return nil, err
	}
	if input.InitialConfiguration == nil {
		return nil, store.NewErrConflict("exam_attempt", "attempt_configuration_required", nil)
	}
	configuration, err := input.InitialConfiguration.Freeze(model.NewID())
	if err != nil {
		return nil, err
	}
	capabilities := s.capabilities(configuration)
	result := &store.ExamAttemptConnectResult{
		Security: model.AdmittedSecurity{Policy: policy, PolicyContentDigest: resolved.PolicyContentDigest,
			PreflightID: input.Security.PreflightID, PreflightReportDigest: input.Security.ReportDigest,
			PreflightPolicyDigest: resolved.Policy.Digest, ParticipationID: input.ParticipationID, Generation: s.generation,
			SecuritySessionID: "fixture-security-session", DeliveryStreamID: "fixture-delivery", RenewalIntervalSeconds: 5},
		Attempt: examAttempt, Workspace: workspace, Participation: participation, Connection: connection,
		ClassID: f.sitting.ClassID, Configuration: configuration, RuntimeCapabilities: capabilities,
		BrowserPolicy: disabledBrowserPolicy(f.sitting.ExamRevisionID), FirstAdmission: true, ConnectionOpened: true,
	}
	s.admissions[command.KeyDigest] = result
	s.attempts[input.AttemptID] = &admittedAttempt{attemptID: input.AttemptID, participationID: input.ParticipationID,
		connectionID: input.ConnectionID, candidate: input.CandidateUserID, session: input.SessionID, generation: s.generation}
	return result, nil
}

func (s *attemptStore) capabilities(configuration model.AttemptConfiguration) store.CandidateRuntimeCapabilities {
	if s.runtimeCapabilities != nil {
		return *s.runtimeCapabilities
	}
	return store.CandidateRuntimeCapabilities{SchemaVersion: 1, ServerTime: s.f.at,
		InteractionState: store.CandidateInteractionInteractive,
		AttemptConfiguration: store.CandidateAttemptConfiguration{Revision: configuration.Revision,
			Presentation: configuration.Presentation, ApprovedCommands: append([]string{}, configuration.ApprovedCommands...),
			ApprovedKeybindings: append([]string{}, configuration.ApprovedKeybindings...), Digest: configuration.Digest},
		FocusLossCollectionEnabled: true, WorkspaceMutationAllowed: true, SubmissionAllowed: true,
		Browser: store.CandidateBrowserCapability{State: store.CandidateBrowserDisabled},
		ExamRevision: store.CandidateExamRevisionCapability{AdmissionRevisionID: s.f.sitting.ExamRevisionID,
			CurrentRevisionID: s.f.sitting.ExamRevisionID},
		Departure: store.CandidateDepartureCapability{Allowed: false, Reason: "attempt_in_progress"}}
}

func disabledBrowserPolicy(revisionID model.ExamRevisionID) *store.CandidateBrowserPolicy {
	policy := model.DisabledBrowserPolicy()
	digest, _ := model.BrowserPolicyDigest(policy)
	return &store.CandidateBrowserPolicy{Policy: policy, PolicyRevisionID: revisionID, PolicyRevisionNumber: 1, PolicyDigest: digest,
		BrowserActivityDisclosure: model.BrowserActivityDisclosure{NoticeID: "integrated_browser_activity",
			Audience: "exam_managers_with_browser_activity_permission", RetentionPolicyRevision: 1,
			RetentionAnchor: "eligible_closed_sitting_records_completion"}}
}

func (s *attemptStore) RenewParticipation(_ context.Context, input *store.ExamAttemptParticipationRenewal) (*store.ExamAttemptParticipationRenewalResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewals = append(s.renewals, input)
	if s.renewErr != nil {
		return nil, s.renewErr
	}
	return &store.ExamAttemptParticipationRenewalResult{Security: securityControlResultFixture(), AttemptID: input.AttemptID,
		ExamID: s.f.sitting.ExamID, SittingID: s.f.sitting.ID, CandidateUserID: input.CandidateUserID,
		ParticipationID: input.ParticipationID, Generation: input.Generation, AcceptedSequence: input.Sequence,
		DatabaseTime: s.databaseTime, LeaseExpiresAt: s.databaseTime.Add(model.AttemptParticipationInitialLease)}, nil
}

// ResolveParticipationExpiry finds no generation still due: another path
// already ended it.
func (s *attemptStore) ResolveParticipationExpiry(_ context.Context, attemptID model.ExamAttemptID, _ model.AttemptParticipationID, _ int64) (*store.ExamAttemptParticipationExpiryDue, error) {
	return nil, store.NewErrNotFound("attempt_participation", attemptID.String())
}

func (s *attemptStore) CloseConnection(ctx context.Context, input *store.ExamAttemptConnectionClose) (*store.ExamAttemptConnectionCloseResult, error) {
	s.mu.Lock()
	s.closes = append(s.closes, input)
	s.closeContextErrs = append(s.closeContextErrs, ctx.Err())
	gate := s.closeGate
	s.mu.Unlock()
	if gate != nil {
		if err := gate(ctx); err != nil {
			return nil, err
		}
	}
	admitted, err := s.attempt(input.CandidateUserID, input.SessionID, input.ConnectionID)
	if err != nil {
		return nil, err
	}
	connection, err := model.NewAttemptConnection(admitted.connectionID, admitted.attemptID, admitted.participationID, admitted.session, s.f.at)
	if err != nil {
		return nil, err
	}
	if err = connection.Close(input.Reason, input.AuditAt); err != nil {
		return nil, err
	}
	return &store.ExamAttemptConnectionCloseResult{AttemptID: admitted.attemptID, ExamID: s.f.sitting.ExamID,
		SittingID: s.f.sitting.ID, CandidateUserID: admitted.candidate, Connection: connection, Changed: true}, nil
}

// attempt finds the caller's admitted Attempt by its Connection, as the
// candidate access predicates do.
func (s *attemptStore) attempt(candidate model.UserID, session model.SessionID, connectionID model.AttemptConnectionID) (*admittedAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, admitted := range s.attempts {
		if admitted.connectionID == connectionID && admitted.candidate == candidate && admitted.session == session {
			return admitted, nil
		}
	}
	return nil, store.NewErrNotFound("attempt_connection", connectionID.String())
}

// attemptFocusLoss is the Focus Loss Store over the admitted Attempts.
type attemptFocusLoss struct {
	store.ExamFocusLossStore
	s *attemptStore
}

func (a attemptFocusLoss) ResolveFocusLossTarget(_ context.Context, selector store.ExamAttemptFocusLossAccess) (*store.ExamAttemptFocusLossTarget, error) {
	if _, err := a.s.attempt(selector.CandidateUserID, selector.SessionID, selector.ConnectionID); err != nil {
		return nil, err
	}
	f := a.s.f
	return &store.ExamAttemptFocusLossTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: selector.CandidateUserID, AttemptID: selector.AttemptID, ParticipationID: selector.ParticipationID,
		Generation: selector.Generation}, nil
}

// RecordFocusLoss accepts the claim as qualifying and crossing the frozen
// policy's threshold, which raises a Flag and either warns the candidate or
// suspends the Attempt and closes its Connection.
func (a attemptFocusLoss) RecordFocusLoss(_ context.Context, input *store.ExamAttemptFocusLossSignal) (*store.ExamAttemptFocusLossResult, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	a.s.focusSignals = append(a.s.focusSignals, input)
	f, selector, receivedAt := a.s.f, input.Access, a.s.databaseTime
	signal, err := model.NewFocusLossSignal(input.SignalID, selector.AttemptID, selector.ParticipationID, selector.Generation,
		input.Sequence, input.DurationMilliseconds, input.Source, receivedAt)
	if err != nil {
		return nil, err
	}
	flag, err := model.NewIntegrityFlag(input.FlagID, selector.AttemptID, selector.Generation, model.IntegrityPolicyFocusLoss, receivedAt)
	if err != nil {
		return nil, err
	}
	result := &store.ExamAttemptFocusLossResult{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: selector.CandidateUserID, AttemptID: selector.AttemptID, ParticipationID: selector.ParticipationID,
		Generation: selector.Generation, Signal: signal, AcceptedSequence: input.Sequence, DatabaseTime: receivedAt,
		CollectionEnabled: true, Qualified: true, ThresholdCrossed: true, PolicyOutcome: a.s.focusOutcome,
		RetainedEvidenceCount: 1, Flag: flag, FlagCreated: true, ManagerNotificationRequired: true}
	if a.s.focusOutcome == model.IntegrityOutcomeFlagAndWarn {
		result.CandidateWarningCreated = true
		return result, nil
	}
	suspended, err := model.NewExamAttempt(selector.AttemptID, f.sitting.ExamID, f.sitting.ID, selector.CandidateUserID,
		f.sitting.ExamRevisionID, f.at.Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	if err = suspended.Suspend(receivedAt); err != nil {
		return nil, err
	}
	result.Attempt = suspended
	result.Participation = &store.ExamAttemptParticipationView{ID: selector.ParticipationID, AttemptID: selector.AttemptID,
		State: model.AttemptParticipationEnded, Generation: selector.Generation, RenewalSequence: input.Sequence,
		StartedAt: f.at.Add(-time.Minute), UpdatedAt: receivedAt, LeaseExpiresAt: f.at.Add(model.AttemptParticipationInitialLease),
		EndedAt: model.OptionalTimeFrom(receivedAt), EndReason: model.AttemptParticipationEndPolicySuspended}
	result.Connection = &store.ExamAttemptManagerConnection{ID: selector.ConnectionID, State: model.AttemptConnectionClosed,
		OpenedAt: f.at.Add(-time.Minute), ClosedAt: model.OptionalTimeFrom(receivedAt),
		CloseReason: model.AttemptConnectionClosePolicySuspended}
	result.ConnectionClosed = true
	result.Suspension = &store.ExamAttemptSuspensionView{ID: input.SuspensionID, AttemptID: selector.AttemptID,
		ParticipationID: selector.ParticipationID, FlagID: flag.ID, Generation: selector.Generation,
		State: model.AttemptSuspensionActive, Source: model.AttemptSuspensionSourcePolicy,
		CandidateReason: model.AttemptSuspensionCandidateReasonFocusLossPolicy, StartedAt: receivedAt}
	return result, nil
}

// attemptDeliveryBudget resolves the Sitting and Class of the caller's live
// Attempt.
type attemptDeliveryBudget struct {
	store.DeliveryBudgetStore
	s *attemptStore
}

func (a attemptDeliveryBudget) ResolveLiveDeliveryTarget(_ context.Context, access store.CandidateAttemptAccess) (*store.DeliveryOwnerTarget, error) {
	if _, err := a.s.attempt(access.CandidateUserID, access.SessionID, access.ConnectionID); err != nil {
		return nil, err
	}
	return &store.DeliveryOwnerTarget{SittingID: a.s.f.sitting.ID, ClassID: a.s.f.sitting.ClassID}, nil
}

// attemptBrowserDelivery starts Browser Activity sources and acknowledges
// appends with one receipt per event.
type attemptBrowserDelivery struct {
	store.BrowserDeliveryStore
	s *attemptStore
}

func (a attemptBrowserDelivery) StartBrowserActivity(_ context.Context, input *store.BrowserActivitySourceStart) (*model.BrowserSourceStatus, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	a.s.browserStarts = append(a.s.browserStarts, input)
	return &model.BrowserSourceStatus{SourceSessionID: input.SourceSessionID, AttemptID: input.Access.AttemptID,
		ParticipationID: input.ParticipationID, Generation: input.Generation, PolicyRevisionID: input.PolicyRevisionID,
		PolicyDigest: input.PolicyDigest, StartTransition: input.Transition.Kind, StartedAt: a.s.f.at, DetailMode: "collecting",
		BrowserDeliveryProgress: model.BrowserDeliveryProgress{MissingRanges: []model.SequenceRange{}}, ServerTime: a.s.f.at}, nil
}

// AppendBrowserActivity acknowledges the events. A sequence the source saw
// earlier, browserSeen, stays the highest seen, and what lies between it and
// the contiguous prefix is reported missing.
func (a attemptBrowserDelivery) AppendBrowserActivity(ctx context.Context, input *store.BrowserActivityAppend) (*model.BrowserActivityAcknowledgement, error) {
	a.s.mu.Lock()
	a.s.browserAppends = append(a.s.browserAppends, input)
	appendErr, gate := a.s.appendErr, a.s.appendGate
	a.s.mu.Unlock()
	if gate != nil {
		close(gate.entered)
		<-ctx.Done()
		close(gate.finished)
		return nil, ctx.Err()
	}
	if appendErr != nil {
		return nil, appendErr
	}
	serverTime := a.s.f.at.Add(time.Second)
	a.s.mu.Lock()
	seen := a.s.browserSeen
	a.s.mu.Unlock()
	acknowledgement := &model.BrowserActivityAcknowledgement{SourceSessionID: input.SourceSessionID, HighestSeen: seen,
		ExamID: a.s.f.sitting.ExamID, SittingID: a.s.f.sitting.ID, MissingRanges: []model.BrowserActivityMissingRange{}, ServerTime: serverTime}
	for _, event := range input.Events {
		digest, err := event.Fingerprint()
		if err != nil {
			return nil, err
		}
		acknowledgement.Receipts = append(acknowledgement.Receipts, model.BrowserEventReceipt{Sequence: event.Sequence,
			EventDigest: digest, ReceivedAt: serverTime})
		if event.Sequence == 1 {
			acknowledgement.HighestContiguous = 1
		}
		acknowledgement.HighestSeen = max(acknowledgement.HighestSeen, event.Sequence)
	}
	if acknowledgement.HighestSeen > acknowledgement.HighestContiguous+1 {
		acknowledgement.MissingRanges = append(acknowledgement.MissingRanges, model.BrowserActivityMissingRange{
			First: acknowledgement.HighestContiguous + 1, Last: acknowledgement.HighestSeen - 1})
	}
	acknowledgement.SettledThrough, acknowledgement.AllocatedThrough = acknowledgement.HighestContiguous, acknowledgement.HighestSeen
	return acknowledgement, nil
}

// BrowserSourceStatus finds no source, so a refused append carries no
// recovery state.
func (a attemptBrowserDelivery) BrowserSourceStatus(_ context.Context, access store.BrowserDeliveryAccess) (*model.BrowserSourceStatus, error) {
	return nil, store.NewErrNotFound("browser_source", string(access.SourceSessionID))
}

type attemptSittings struct {
	store.ExamSittingStore
	f *attemptFixture
}

func (s attemptSittings) Resolve(_ context.Context, sittingID model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if sittingID != s.f.sitting.ID {
		return nil, store.NewErrNotFound("exam_sitting", sittingID.String())
	}
	return &store.ExamSittingSnapshot{Sitting: s.f.sitting}, nil
}

// desktopBuilds resolves every Desktop to the fixture's released build.
type desktopBuilds struct{ f *attemptFixture }

func (d desktopBuilds) ResolveAttemptDesktopBuild(context.Context, model.Principal) (attempt.DesktopBuildResolution, error) {
	return attempt.DesktopBuildResolution{Build: d.f.build, CompatibilityPolicyRevision: 1}, nil
}

func (d desktopBuilds) ResolveNativeDeliveryBuild(context.Context, attempt.NativeDeliveryBuildIdentity) (model.DesktopBuildTuple, error) {
	return d.f.build, nil
}

// The collaborators below are what a connection's candidate commands never
// reach: preflight, native delivery, the Workspace, Submissions, resources,
// manager decisions and mail.
type (
	unusedSecurityPreflight struct{ store.SecurityPreflightStore }
	unusedNativeDelivery    struct{ store.NativeDeliveryStore }
	unusedWorkspaces        struct {
		store.ExamAttemptWorkspaceStore
	}
	unusedSubmissions   struct{ store.ExamSubmissionStore }
	unusedSittings      struct{ store.ExamSittingStore }
	unusedExams         struct{ store.ExamAuthoringStore }
	unusedMemberships   struct{ store.AcademicUnitMemberStore }
	unusedRevisions     struct{ store.ExamRevisionStore }
	unusedClasses       struct{ store.ClassStore }
	unusedUsers         struct{ store.UserStore }
	unusedLifecycleJobs struct{ sitting.LifecycleJobFactory }
	unusedContent       struct{ attempt.Content }
)

// audited returns the one mutation attempt recorded for operation, with the
// value it was recorded with.
func (f *attemptFixture) audited(t testing.TB, operation string) (*model.AuditEvent, map[string]any) {
	t.Helper()
	var found *model.AuditEvent
	var value map[string]any
	for _, event := range f.control.Audits.Attempts() {
		var parameters struct {
			Operation string         `json:"operation"`
			Value     map[string]any `json:"value"`
		}
		require.NoError(t, json.Unmarshal(event.Parameters, &parameters))
		if parameters.Operation == operation {
			require.Nil(t, found, "more than one %s attempt", operation)
			found, value = event, parameters.Value
		}
	}
	require.NotNil(t, found, "no %s attempt was recorded", operation)
	return found, value
}

// attemptStoreLog is what reached the stores, in arrival order.
type attemptStoreLog struct {
	connects         []*store.ExamAttemptConnect
	connectCommands  []*store.CommandIdempotency
	renewals         []*store.ExamAttemptParticipationRenewal
	focusSignals     []*store.ExamAttemptFocusLossSignal
	browserStarts    []*store.BrowserActivitySourceStart
	browserAppends   []*store.BrowserActivityAppend
	closes           []*store.ExamAttemptConnectionClose
	closeContextErrs []error
}

func (s *attemptStore) log() attemptStoreLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return attemptStoreLog{connects: slices.Clone(s.connects), connectCommands: slices.Clone(s.connectCommands),
		renewals: slices.Clone(s.renewals), focusSignals: slices.Clone(s.focusSignals),
		browserStarts: slices.Clone(s.browserStarts), browserAppends: slices.Clone(s.browserAppends),
		closes: slices.Clone(s.closes), closeContextErrs: slices.Clone(s.closeContextErrs)}
}
