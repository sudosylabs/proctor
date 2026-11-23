// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// attemptFixture is the real Attempt service the Attempt, delivery and
// preflight handlers call, over in-memory stores that hold one Attempt of
// the Sitting fixture's Sitting. The Sitting fixture's caller manages that
// Sitting; candidate is the Desktop Session the Attempt belongs to. The
// stores record every request and answer as a consistent database would, so
// a handler test asserts what the request turned into at the Store.
type attemptFixture struct {
	*sittingFixture
	// candidate signs in with the bearer token "candidate"; any other token
	// signs in the manager.
	candidate     model.Principal
	credential    string
	attempt       *model.ExamAttempt
	workspace     *model.ExamAttemptWorkspace
	participation *store.ExamAttemptParticipationView
	connection    *store.ExamAttemptManagerConnection
	// build is the candidate's Desktop build, whose native agreement the
	// admitted security policy was resolved for.
	build model.DesktopBuildTuple

	attemptStore     *attemptLedger
	workspaceStore   *attemptWorkspaceLedger
	submissionStore  *attemptSubmissionLedger
	deliveryStore    *attemptDeliveryLedger
	environmentStore *attemptEnvironmentLedger
	content          *attemptContent

	service *attempt.Service
}

// attemptFixtureTime is when the seeded Attempt started.
var attemptFixtureTime = time.Date(2026, time.August, 17, 9, 0, 0, 123000000, time.UTC)

// attemptReceiptCode is the receipt code every Submission the fixture seals
// is given, in its canonical form.
const attemptReceiptCode = model.SubmissionReceiptCode("MBY3KBMNEB83TFW1")

func newAttemptFixture(t *testing.T) *attemptFixture {
	t.Helper()
	f := &attemptFixture{sittingFixture: newSittingFixture(t, testExamHTTPPrincipal())}
	at := attemptFixtureTime
	f.candidate = attemptCandidatePrincipal()
	f.users.add(activeExamUser(f.candidate.UserID, "candidate"))
	f.credential = model.NewCredentialToken()
	var err error
	f.attempt, err = model.NewExamAttempt(model.NewExamAttemptID(), f.examID, f.sitting.ID, f.candidate.UserID, f.sitting.ExamRevisionID, at)
	require.NoError(t, err)
	f.workspace, err = model.NewExamAttemptWorkspace(model.NewExamAttemptWorkspaceID(), f.attempt.ID, at)
	require.NoError(t, err)
	f.participation = &store.ExamAttemptParticipationView{ID: model.NewAttemptParticipationID(), AttemptID: f.attempt.ID,
		State: model.AttemptParticipationActive, Generation: 1, StartedAt: at, UpdatedAt: at, LeaseExpiresAt: at.Add(20 * time.Second)}
	f.connection = &store.ExamAttemptManagerConnection{ID: model.NewAttemptConnectionID(), State: model.AttemptConnectionOpen, OpenedAt: at}
	f.build = model.DesktopBuildTuple{DesktopRelease: "1.0.0", DesktopBuildID: "test-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16), DesktopTarget: "darwin-arm64",
		ConfigurationManifest: model.EmptyAttemptConfigurationManifest(), CapabilityMatrixIdentity: "httpapi-test-matrix"}
	f.build.NativeAgreement = nativetest.NativeAgreement(t, f.build)

	f.attemptStore = newAttemptLedger(t, f)
	f.workspaceStore = newAttemptWorkspaceLedger(f)
	f.submissionStore = newAttemptSubmissionLedger(t, f)
	f.deliveryStore = newAttemptDeliveryLedger(t, f)
	f.environmentStore = &attemptEnvironmentLedger{}
	f.content = &attemptContent{chunks: map[int][]byte{}}
	f.sittingStore.unitID = f.unitID

	f.service, err = attempt.New(attempt.Dependencies{
		Persistence: f.attemptStore, FocusLoss: unusedAttemptStores{}, SecurityPreflight: unusedAttemptStores{},
		NativeDelivery: f.deliveryStore, BrowserDelivery: f.deliveryStore, DeliveryBudget: f.deliveryStore,
		Workspace: f.workspaceStore, Submissions: f.submissionStore, Sittings: f.sittingStore,
		Managers: attempt.SittingManagerAuthorizer{Sittings: f.sittingService, Submissions: f.submissionStore, Audit: f.access.Auditor},
		Auditor:  f.access.Auditor, Publisher: f.publisher, Content: f.content,
		Mail: attempt.SubmissionReceiptMail{Composer: administrationComposer(t, &identityMailSender{}), Users: f.users,
			Sittings: f.sittingStore, Revisions: sittingMailRevisions{}},
		DesktopBuilds: attemptDesktopBuilds{f: f}, Environments: f.environmentStore,
		Now: time.Now, NewAttemptID: model.NewExamAttemptID, NewWorkspaceID: model.NewExamAttemptWorkspaceID,
		NewParticipation: model.NewAttemptParticipationID, NewConnection: model.NewAttemptConnectionID,
		NewEvidence: model.NewIntegrityEvidenceID, NewFlag: model.NewIntegrityFlagID, NewSuspension: model.NewAttemptSuspensionID,
		NewFocusLossSignal: model.NewFocusLossSignalID, NewDiscrepancy: model.NewIntegrityDiscrepancyID,
		NewWorkspaceEntry: model.NewAttemptWorkspaceEntryID, NewWorkspaceObject: model.NewAttemptWorkspaceObjectID,
		NewWorkspaceVersion: model.NewWorkspaceContentVersion, NewSubmission: model.NewSubmissionID,
		NewReceiptCode: func() model.SubmissionReceiptCode { return attemptReceiptCode },
		NewDomainEvent: model.NewDomainEventID, NewJob: model.NewJobID,
	})
	require.NoError(t, err)
	return f
}

// attemptCandidatePrincipal is a Desktop Session with a registered key: the
// only principal the Attempt service admits as a candidate.
func attemptCandidatePrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, DesktopRegistrationID: model.NewDesktopRegistrationID(),
		DPoPKeyThumbprint: strings.Repeat("A", 43), RegisteredDesktopKey: true,
		DesktopRelease: "1.0.0", DesktopBuildID: "test-build", DesktopPlatform: model.DesktopPlatformDarwin,
		DesktopArchitecture: model.DesktopArchitectureARM64, DesktopRealtimeProtocol: 1, AuthenticatedAt: time.Now()}
}

func (f *attemptFixture) AuthenticateAccess(_ context.Context, token string) (*model.Principal, error) {
	principal := f.caller
	if token == "candidate" {
		principal = f.candidate
	}
	return &principal, nil
}

func (f *attemptFixture) AuthenticateBearer(ctx context.Context, token string) (*model.Principal, error) {
	return f.AuthenticateAccess(ctx, token)
}

func (f *attemptFixture) AuthenticateDPoP(ctx context.Context, token, _, _, _ string) (*model.Principal, error) {
	return f.AuthenticateAccess(ctx, token)
}

// api compiles resources over the fixture's service, authenticating each
// request as the candidate or the manager by its bearer token.
func (f *attemptFixture) api(t *testing.T, resources ...route.Resource) *route.API {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	return newFocusedResourceAPI(t, logger, f, resources...)
}

// candidateRequest is the candidate's request through the Attempt's open
// Connection and continuity credential.
func (f *attemptFixture) candidateRequest(t *testing.T, method, path string) *http.Request {
	request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	request.Header.Set("Authorization", "Bearer candidate")
	request.Header.Set(candidateAttemptCredentialHeader, f.credential)
	request.Header.Set(candidateAttemptConnectionHeader, f.connection.ID.String())
	return request
}

// managerRequest is the Sitting manager's request.
func (f *attemptFixture) managerRequest(t *testing.T, method, path string, body io.Reader) *http.Request {
	request := httptest.NewRequestWithContext(t.Context(), method, path, body)
	request.Header.Set("Authorization", "Bearer credential")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

// candidateAccess is the selector the candidate's requests prove at the
// Store: their Session and Desktop key, the Connection, and only the hash of
// the continuity credential.
func (f *attemptFixture) candidateAccess() store.CandidateAttemptAccess {
	return store.CandidateAttemptAccess{AttemptID: f.attempt.ID, CandidateUserID: f.candidate.UserID,
		SessionID: f.candidate.SessionID, DesktopRegistrationID: f.candidate.DesktopRegistrationID,
		DPoPKeyThumbprint: f.candidate.DPoPKeyThumbprint, ConnectionID: f.connection.ID,
		ContinuityCredentialHash: model.HashToken(f.credential)}
}

// editorAccess is the candidate's selector for a Workspace write, fenced to
// the live Participation generation.
func (f *attemptFixture) editorAccess() store.ExamAttemptWorkspaceEditorAccess {
	access := f.candidateAccess()
	return store.ExamAttemptWorkspaceEditorAccess{AttemptID: access.AttemptID, ParticipationID: f.participation.ID,
		Generation: f.participation.Generation, CandidateUserID: access.CandidateUserID, SessionID: access.SessionID,
		DesktopRegistrationID: access.DesktopRegistrationID, DPoPKeyThumbprint: access.DPoPKeyThumbprint,
		ConnectionID: access.ConnectionID, ContinuityCredentialHash: access.ContinuityCredentialHash}
}

// moveAttemptTo sets the Attempt's state and revision as the Store holds
// them, so a manager test can send the revision its request expects.
func (f *attemptFixture) moveAttemptTo(state model.ExamAttemptState, revision int64) {
	f.attempt.State, f.attempt.Revision = state, revision
}

// managerPath is the path of the seeded Attempt under its Exam and Sitting.
func (f *attemptFixture) managerPath(suffix string) string {
	return "/api/v1/exams/" + f.examID.String() + "/sittings/" + f.sitting.ID.String() + "/attempts/" + f.attempt.ID.String() + suffix
}

// candidatePath is the path of the seeded Attempt as the candidate names it.
func (f *attemptFixture) candidatePath(suffix string) string {
	return "/api/v1/exam-attempts/" + f.attempt.ID.String() + suffix
}

// admits reports whether access is the candidate's live Attempt access, as
// the Store proves it.
func (f *attemptFixture) admits(access store.CandidateAttemptAccess) error {
	if access != f.candidateAccess() {
		return store.NewErrNotFound("exam_attempt", access.AttemptID.String())
	}
	return nil
}

// managerSnapshot is the Attempt as a manager sees it now.
func (f *attemptFixture) managerSnapshot() store.ExamAttemptManagerSnapshot {
	examAttempt, workspace := *f.attempt, *f.workspace
	participation, connection := *f.participation, *f.connection
	snapshot := store.ExamAttemptManagerSnapshot{Attempt: &examAttempt, Workspace: &workspace,
		LatestParticipation: &participation, CurrentConnection: &connection}
	if active := f.attemptStore.activeSuspension; active != nil {
		suspension := *active
		snapshot.ActiveSuspension = &suspension
	}
	return snapshot
}

// attemptLedger is the Exam Attempt Store of the one seeded Attempt.
type attemptLedger struct {
	store.ExamAttemptStore
	f *attemptFixture

	presentation     *store.CandidateExamPresentation
	resource         store.CandidateResourceContent
	activeSuspension *store.ExamAttemptSuspensionView
	// others are further Attempts of the Sitting a manager list pages past.
	others []store.ExamAttemptManagerSnapshot
	// clip is the one screen clip the Attempt has, once requested.
	clip     *store.ExamAttemptScreenClipView
	received []int
	// activity is the candidate's exam activity page.
	activity      []store.CandidateExamActivityItem
	activityLists []store.CandidateExamActivityListOptions

	accesses         []store.CandidateAttemptAccess
	gets             []attemptRead
	lists            []store.ExamAttemptManagerListOptions
	acknowledgements []*store.ExamAttemptCorrectionAcknowledgement
	reallows         []*store.ExamAttemptReallow
	suspensions      []*store.ExamAttemptManualSuspension
	clipRequests     []*store.ExamAttemptScreenClipRequest
	clipUploads      []*store.ExamAttemptScreenClipUploadBegin
	clipChunks       []*store.ExamAttemptScreenClipChunk
	clipCompletions  []*store.ExamAttemptScreenClipCompletion
	clipReads        int
	commands         []*store.CommandIdempotency
}

func newAttemptLedger(t *testing.T, f *attemptFixture) *attemptLedger {
	t.Helper()
	resourceID := model.NewExamResourceID()
	resource := store.CandidateExamResource{ResourceID: resourceID, DisplayName: "Input", DescriptionMarkdown: "Read this.",
		MediaType: model.ExamResourceMediaText, SizeBytes: 9, SHA256: digestOf([]byte("protected"))}
	return &attemptLedger{f: f,
		presentation: &store.CandidateExamPresentation{AttemptID: f.attempt.ID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
			Title: "Algorithms", InstructionsMarkdown: "Solve safely.",
			Capacity:            model.ExamCapacityPolicy{ResourceMaximumCount: 7, ResourceMaximumBytes: 4096, WorkspaceMaximumEntries: 42, WorkspaceMaximumFileBytes: 1024, WorkspaceMaximumTotalBytes: 8192},
			RuntimeCapabilities: candidateTestRuntimeCapabilities(t, attemptFixtureTime, f.attempt),
			Resources:           []store.CandidateExamResource{resource}},
		resource: store.CandidateResourceContent{Resource: resource, FileRevisionID: model.NewFileRevisionID(), RenditionID: model.NewFileRenditionID()},
	}
}

// lastCommand is the idempotency of the most recent mutation.
func (s *attemptLedger) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no mutation reached the Attempt Store")
	return s.commands[len(s.commands)-1]
}

func (s *attemptLedger) prove(access store.CandidateAttemptAccess) error {
	s.accesses = append(s.accesses, access)
	return s.f.admits(access)
}

func (s *attemptLedger) GetCandidatePresentation(_ context.Context, access store.CandidateAttemptAccess) (*store.CandidateExamPresentation, error) {
	if err := s.prove(access); err != nil {
		return nil, err
	}
	presentation := *s.presentation
	return &presentation, nil
}

func (s *attemptLedger) ResolveCandidateAccess(_ context.Context, access store.CandidateAttemptAccess) (*store.CandidateAttemptAccessTarget, error) {
	if err := s.prove(access); err != nil {
		return nil, err
	}
	return &store.CandidateAttemptAccessTarget{AttemptID: s.f.attempt.ID, SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID,
		CandidateUserID: s.f.candidate.UserID, ParticipationID: s.f.participation.ID, Generation: s.f.participation.Generation}, nil
}

func (s *attemptLedger) ResolveCandidateResource(_ context.Context, access store.CandidateAttemptAccess, id model.ExamResourceID) (*store.CandidateResourceContent, error) {
	if err := s.prove(access); err != nil {
		return nil, err
	}
	if id != s.resource.Resource.ResourceID {
		return nil, store.NewErrNotFound("exam_resource", id.String())
	}
	resource := s.resource
	return &resource, nil
}

// attemptRead is the pair of identities a manager's Attempt read names.
type attemptRead struct {
	ExamID    model.ExamID
	AttemptID model.ExamAttemptID
}

func (s *attemptLedger) Get(_ context.Context, examID model.ExamID, id model.ExamAttemptID) (*store.ExamAttemptManagerSnapshot, error) {
	s.gets = append(s.gets, attemptRead{ExamID: examID, AttemptID: id})
	if examID != s.f.examID || id != s.f.attempt.ID {
		return nil, store.NewErrNotFound("exam_attempt", id.String())
	}
	snapshot := s.f.managerSnapshot()
	return &snapshot, nil
}

// List pages the seeded Attempt and then the others, newest first, after
// the keyset cursor.
func (s *attemptLedger) List(_ context.Context, options store.ExamAttemptManagerListOptions) ([]store.ExamAttemptManagerSnapshot, error) {
	s.lists = append(s.lists, options)
	rows := append([]store.ExamAttemptManagerSnapshot{s.f.managerSnapshot()}, s.others...)
	if options.BeforeAttemptID.IsValid() {
		after := slices.IndexFunc(rows, func(row store.ExamAttemptManagerSnapshot) bool { return row.Attempt.ID == options.BeforeAttemptID })
		rows = rows[after+1:]
	}
	return rows[:min(len(rows), options.Limit)], nil
}

func (s *attemptLedger) ListSittingCandidateStatuses(context.Context, store.SittingCandidateStatusListOptions) (*store.SittingCandidateStatusPage, error) {
	return &store.SittingCandidateStatusPage{}, nil
}

func (s *attemptLedger) currentRevision() model.ExamRevisionID {
	return s.presentation.RuntimeCapabilities.ExamRevision.CurrentRevisionID
}

func (s *attemptLedger) ResolveCorrectionAcknowledgementTarget(_ context.Context, input store.ExamAttemptCorrectionAcknowledgement) (*store.ExamAttemptCorrectionAcknowledgementTarget, error) {
	if err := s.prove(input.Access); err != nil {
		return nil, err
	}
	if input.ExpectedCurrentRevisionID != s.currentRevision() {
		return nil, store.NewErrConflict("exam_attempt", "exam_correction_acknowledgement_target", nil)
	}
	return &store.ExamAttemptCorrectionAcknowledgementTarget{AttemptID: s.f.attempt.ID, ExamID: s.f.examID, SittingID: s.f.sitting.ID,
		ClassID: s.f.sitting.ClassID, CandidateUserID: s.f.candidate.UserID, CorrectionRevisionID: input.CorrectionRevisionID,
		CurrentRevisionID: s.currentRevision()}, nil
}

func (s *attemptLedger) AcknowledgeCorrection(_ context.Context, input *store.ExamAttemptCorrectionAcknowledgement, command *store.CommandIdempotency) (*store.ExamAttemptCorrectionAcknowledgementResult, error) {
	s.acknowledgements, s.commands = append(s.acknowledgements, input), append(s.commands, command)
	return &store.ExamAttemptCorrectionAcknowledgementResult{AttemptID: s.f.attempt.ID, CorrectionRevisionID: input.CorrectionRevisionID,
		CurrentRevisionID: s.currentRevision(), AcknowledgedAt: input.AuditEvent.CreatedAt, MutationAuditEventID: input.AuditEvent.ID}, nil
}

// fenced is the seeded Attempt when a manager command expects its current
// revision.
func (s *attemptLedger) fenced(id model.ExamAttemptID, expected int64) error {
	if id != s.f.attempt.ID {
		return store.NewErrNotFound("exam_attempt", id.String())
	}
	if s.f.attempt.Revision != expected {
		return store.NewErrConflict("exam_attempt", "exam_attempt_revision", nil)
	}
	return nil
}

func (s *attemptLedger) ReallowAttempt(_ context.Context, input *store.ExamAttemptReallow, command *store.CommandIdempotency) (*store.ExamAttemptReallowResult, error) {
	s.reallows, s.commands = append(s.reallows, input), append(s.commands, command)
	if err := s.fenced(input.AttemptID, input.ExpectedRevision); err != nil {
		return nil, err
	}
	if s.activeSuspension == nil || s.activeSuspension.ID != input.SuspensionID {
		return nil, store.NewErrConflict("exam_attempt", "attempt_suspension_active", nil)
	}
	s.f.moveAttemptTo(model.ExamAttemptReady, input.ExpectedRevision+1)
	s.f.attempt.UpdatedAt = input.AuditAt
	closed := *s.activeSuspension
	closed.State, closed.EndedAt, closed.ReallowedByUserID = model.AttemptSuspensionClosed, model.OptionalTimeFrom(input.AuditAt), input.ActorUserID
	s.activeSuspension = nil
	examAttempt := *s.f.attempt
	return &store.ExamAttemptReallowResult{ExamID: input.ExamID, SittingID: input.SittingID, ClassID: s.f.sitting.ClassID,
		CandidateUserID: s.f.candidate.UserID, Attempt: &examAttempt, Suspension: &closed}, nil
}

func (s *attemptLedger) SuspendAttempt(_ context.Context, input *store.ExamAttemptManualSuspension, command *store.CommandIdempotency) (*store.ExamAttemptManualSuspensionResult, error) {
	s.suspensions, s.commands = append(s.suspensions, input), append(s.commands, command)
	if err := s.fenced(input.AttemptID, input.ExpectedRevision); err != nil {
		return nil, err
	}
	at := input.AuditAt
	s.f.moveAttemptTo(model.ExamAttemptSuspended, input.ExpectedRevision+1)
	s.f.attempt.UpdatedAt = at
	s.f.participation.State, s.f.participation.EndReason = model.AttemptParticipationEnded, model.AttemptParticipationEndKicked
	s.f.participation.UpdatedAt, s.f.participation.EndedAt = at, model.OptionalTimeFrom(at)
	s.f.connection.State, s.f.connection.CloseReason = model.AttemptConnectionClosed, model.AttemptConnectionCloseKicked
	s.f.connection.ClosedAt = model.OptionalTimeFrom(at)
	s.activeSuspension = &store.ExamAttemptSuspensionView{ID: input.SuspensionID, AttemptID: input.AttemptID,
		ParticipationID: s.f.participation.ID, Generation: s.f.participation.Generation, State: model.AttemptSuspensionActive,
		Source: model.AttemptSuspensionSourceManual, CandidateReason: model.AttemptSuspensionCandidateReasonManagerSuspended,
		StartedAt: at, SuspendedByUserID: input.ActorUserID}
	examAttempt, participation, connection, suspension := *s.f.attempt, *s.f.participation, *s.f.connection, *s.activeSuspension
	return &store.ExamAttemptManualSuspensionResult{ExamID: input.ExamID, SittingID: input.SittingID, ClassID: s.f.sitting.ClassID,
		CandidateUserID: s.f.candidate.UserID, Attempt: &examAttempt, Participation: &participation, Connection: &connection,
		ConnectionClosed: true, Suspension: &suspension}, nil
}

func (s *attemptLedger) RequestScreenClip(_ context.Context, input *store.ExamAttemptScreenClipRequest, command *store.CommandIdempotency) (*store.ExamAttemptScreenClipResult, error) {
	s.clipRequests, s.commands = append(s.clipRequests, input), append(s.commands, command)
	clip := input.Clip
	s.clip = &store.ExamAttemptScreenClipView{ID: clip.ID, AttemptID: clip.AttemptID, Source: clip.Source,
		RequestedByUserID: clip.RequestedByUserID, Range: clip.Range, State: clip.State, RequestedAt: clip.RequestedAt,
		UpdatedAt: clip.UpdatedAt, Revision: clip.Revision}
	return &store.ExamAttemptScreenClipResult{ExamID: input.ExamID, SittingID: input.SittingID, CandidateUserID: s.f.candidate.UserID,
		Clip: *s.clip}, nil
}

// deliverClip puts a clip the candidate's Desktop has taken into the Store.
func (s *attemptLedger) deliverClip(state model.ScreenClipState, upload *model.ScreenClipUpload) model.ScreenClipID {
	at := s.f.attempt.UpdatedAt
	s.clip = &store.ExamAttemptScreenClipView{ID: model.NewScreenClipID(), AttemptID: s.f.attempt.ID, Source: model.ScreenClipSourceManager,
		RequestedByUserID: s.f.caller.UserID, Range: model.ScreenClipRange{FromSessionStart: true, End: at}, State: state,
		DeliveredParticipationID: s.f.participation.ID, Upload: upload, RequestedAt: at, UpdatedAt: at, Revision: 2}
	return s.clip.ID
}

func (s *attemptLedger) candidateClip(access store.CandidateAttemptAccess, id model.ScreenClipID) (*store.ExamAttemptScreenClipView, error) {
	if err := s.prove(access); err != nil {
		return nil, err
	}
	if s.clip == nil || s.clip.ID != id {
		return nil, store.NewErrNotFound("screen_clip", id.String())
	}
	return s.clip, nil
}

func (s *attemptLedger) BeginScreenClipUpload(_ context.Context, input *store.ExamAttemptScreenClipUploadBegin) (*store.ExamAttemptScreenClipView, error) {
	s.clipUploads = append(s.clipUploads, input)
	clip, err := s.candidateClip(input.Access, input.ClipID)
	if err != nil {
		return nil, err
	}
	clip.State, clip.Upload, clip.Revision = model.ScreenClipUploading, input.Upload, clip.Revision+1
	value := *clip
	return &value, nil
}

func (s *attemptLedger) GetScreenClipUpload(_ context.Context, access store.CandidateAttemptAccess, id model.ScreenClipID) (*store.ExamAttemptScreenClipUploadStatus, error) {
	clip, err := s.candidateClip(access, id)
	if err != nil {
		return nil, err
	}
	return &store.ExamAttemptScreenClipUploadStatus{Clip: *clip, Received: slices.Clone(s.received)}, nil
}

func (s *attemptLedger) RecordScreenClipChunk(_ context.Context, input *store.ExamAttemptScreenClipChunk) error {
	s.clipChunks = append(s.clipChunks, input)
	if _, err := s.candidateClip(input.Access, input.ClipID); err != nil {
		return err
	}
	s.received = append(s.received, input.Index)
	return nil
}

func (s *attemptLedger) CompleteScreenClipUpload(_ context.Context, input *store.ExamAttemptScreenClipCompletion) (*store.ExamAttemptScreenClipView, error) {
	s.clipCompletions = append(s.clipCompletions, input)
	clip, err := s.candidateClip(input.Access, input.ClipID)
	if err != nil {
		return nil, err
	}
	clip.State, clip.Signature, clip.Revision = model.ScreenClipReady, input.Signature, clip.Revision+1
	value := *clip
	return &value, nil
}

func (s *attemptLedger) ListAttemptScreenClips(context.Context, store.ExamAttemptClips) ([]store.ExamAttemptScreenClipView, error) {
	if s.clip == nil {
		return []store.ExamAttemptScreenClipView{}, nil
	}
	return []store.ExamAttemptScreenClipView{*s.clip}, nil
}

func (s *attemptLedger) GetAttemptScreenClip(_ context.Context, clips store.ExamAttemptClips, id model.ScreenClipID) (*store.ExamAttemptScreenClipView, error) {
	s.clipReads++
	if clips.AttemptID != s.f.attempt.ID || s.clip == nil || s.clip.ID != id {
		return nil, store.NewErrNotFound("screen_clip", id.String())
	}
	value := *s.clip
	return &value, nil
}

// attemptWorkspaceLedger is the Attempt Workspace Store. It lists the
// Workspace and its journal, and applies a batch as the journal changes it
// records, each at the next cursor.
type attemptWorkspaceLedger struct {
	store.ExamAttemptWorkspaceStore
	f *attemptFixture

	cursor  int64
	items   []store.CandidateAttemptWorkspaceItem
	journal []model.AttemptWorkspaceJournalEntry
	// versions are the content versions of the objects marked ready.
	versions map[model.AttemptWorkspaceObjectID]model.WorkspaceContentVersion
	// batchErr, when set, refuses every batch; result, when set, is what
	// ApplyBatch answers instead of the changes it applied.
	batchErr error
	result   *store.ExamAttemptWorkspaceBatchResult

	lists        []store.CandidateWorkspaceListOptions
	files        []attemptWorkspaceFileRead
	journalReads []store.CandidateWorkspaceJournalOptions
	reservations []*store.ExamAttemptWorkspaceObjectReservation
	ready        []*store.ExamAttemptWorkspaceObjectsReady
	batches      []*store.ExamAttemptWorkspaceBatch
	reclaimed    [][]model.AttemptWorkspaceObjectID
	commands     []*store.CommandIdempotency
}

func newAttemptWorkspaceLedger(f *attemptFixture) *attemptWorkspaceLedger {
	file := func(path string) store.CandidateAttemptWorkspaceItem {
		return store.CandidateAttemptWorkspaceItem{EntryID: model.NewAttemptWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile,
			Path: path, ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: 9,
			SHA256: digestOf([]byte("protected"))}
	}
	return &attemptWorkspaceLedger{f: f, cursor: 4, items: []store.CandidateAttemptWorkspaceItem{file("cmd/main.go"), file("go.mod")},
		versions: map[model.AttemptWorkspaceObjectID]model.WorkspaceContentVersion{}}
}

// writes is how many requests to change the Workspace reached the Store.
func (s *attemptWorkspaceLedger) writes() int { return len(s.reservations) + len(s.batches) }

// lastBatch is the most recent batch the Store applied.
func (s *attemptWorkspaceLedger) lastBatch(t *testing.T) *store.ExamAttemptWorkspaceBatch {
	t.Helper()
	require.NotEmpty(t, s.batches, "no batch reached the Workspace Store")
	return s.batches[len(s.batches)-1]
}

func (s *attemptWorkspaceLedger) readable(access store.ExamAttemptWorkspaceReadAccess) error {
	candidate, ok := access.(store.CandidateAttemptAccess)
	if !ok {
		return errors.New("the Workspace was read without the candidate's access")
	}
	return s.f.admits(candidate)
}

func (s *attemptWorkspaceLedger) List(_ context.Context, options store.CandidateWorkspaceListOptions) (*store.CandidateAttemptWorkspacePage, error) {
	s.lists = append(s.lists, options)
	if err := s.readable(options.Access); err != nil {
		return nil, err
	}
	items := s.items
	if options.AfterEntryID.IsValid() {
		after := slices.IndexFunc(items, func(item store.CandidateAttemptWorkspaceItem) bool { return item.EntryID == options.AfterEntryID })
		items = items[after+1:]
	}
	page := &store.CandidateAttemptWorkspacePage{WorkspaceID: s.f.workspace.ID, Cursor: s.cursor, HasMore: len(items) > options.Limit,
		RefreshRequired: options.ExpectedCursor >= 0 && options.ExpectedCursor != s.cursor}
	page.Items = slices.Clone(items[:min(len(items), options.Limit)])
	return page, nil
}

// attemptWorkspaceFileRead is one Workspace file read and the access that
// proved it.
type attemptWorkspaceFileRead struct {
	Access  store.ExamAttemptWorkspaceReadAccess
	EntryID model.AttemptWorkspaceEntryID
}

func (s *attemptWorkspaceLedger) ResolveFile(_ context.Context, access store.ExamAttemptWorkspaceReadAccess, id model.AttemptWorkspaceEntryID) (*store.CandidateWorkspaceContent, error) {
	s.files = append(s.files, attemptWorkspaceFileRead{Access: access, EntryID: id})
	if err := s.readable(access); err != nil {
		return nil, err
	}
	for _, item := range s.items {
		if item.EntryID == id {
			return &store.CandidateWorkspaceContent{Entry: item, StorageOrigin: model.AttemptWorkspaceStorageStarter,
				StarterObjectID: model.NewStarterWorkspaceObjectID(), AttemptObjectID: model.NewAttemptWorkspaceObjectID(),
				ContentVersion: item.ContentVersion}, nil
		}
	}
	return nil, store.NewErrNotFound("attempt_workspace_entry", id.String())
}

func (s *attemptWorkspaceLedger) ListJournal(_ context.Context, options store.CandidateWorkspaceJournalOptions) (*store.CandidateWorkspaceJournalPage, error) {
	s.journalReads = append(s.journalReads, options)
	if err := s.readable(options.Access); err != nil {
		return nil, err
	}
	var entries []model.AttemptWorkspaceJournalEntry
	for _, entry := range s.journal {
		if entry.Cursor > options.AfterCursor {
			entries = append(entries, entry)
		}
	}
	return &store.CandidateWorkspaceJournalPage{WorkspaceID: s.f.workspace.ID, CurrentCursor: s.cursor,
		Entries: slices.Clone(entries[:min(len(entries), options.Limit)]), HasMore: len(entries) > options.Limit}, nil
}

func (s *attemptWorkspaceLedger) target() *store.ExamAttemptWorkspaceMutationTarget {
	return &store.ExamAttemptWorkspaceMutationTarget{ExamID: s.f.examID, SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID,
		CandidateUserID: s.f.candidate.UserID, WorkspaceID: s.f.workspace.ID}
}

func (s *attemptWorkspaceLedger) writable(access store.ExamAttemptWorkspaceWriteAccess) error {
	if access != store.ExamAttemptWorkspaceWriteAccess(s.f.editorAccess()) {
		return store.NewErrNotFound("exam_attempt", access.WorkspaceAttemptID().String())
	}
	return nil
}

func (s *attemptWorkspaceLedger) ReserveObjects(_ context.Context, input *store.ExamAttemptWorkspaceObjectReservation) (*store.ExamAttemptWorkspaceMutationTarget, error) {
	s.reservations = append(s.reservations, input)
	if err := s.writable(input.Access); err != nil {
		return nil, err
	}
	return s.target(), nil
}

func (s *attemptWorkspaceLedger) MarkObjectsReady(_ context.Context, input *store.ExamAttemptWorkspaceObjectsReady) error {
	s.ready = append(s.ready, input)
	for _, object := range input.Objects {
		s.versions[object.ObjectID] = object.ContentVersion
	}
	return nil
}

func (s *attemptWorkspaceLedger) MarkObjectsReclaimable(_ context.Context, ids []model.AttemptWorkspaceObjectID) error {
	s.reclaimed = append(s.reclaimed, ids)
	return nil
}

func (s *attemptWorkspaceLedger) entry(id model.AttemptWorkspaceEntryID) (store.CandidateAttemptWorkspaceItem, bool) {
	index := slices.IndexFunc(s.items, func(item store.CandidateAttemptWorkspaceItem) bool { return item.EntryID == id })
	if index < 0 {
		return store.CandidateAttemptWorkspaceItem{}, false
	}
	return s.items[index], true
}

func (s *attemptWorkspaceLedger) ApplyBatch(_ context.Context, batch *store.ExamAttemptWorkspaceBatch, command *store.CommandIdempotency) (*store.ExamAttemptWorkspaceBatchResult, error) {
	s.batches, s.commands = append(s.batches, batch), append(s.commands, command)
	if err := s.writable(batch.Access); err != nil {
		return nil, err
	}
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	if s.result != nil {
		return s.result, nil
	}
	target := s.target()
	result := &store.ExamAttemptWorkspaceBatchResult{SittingID: target.SittingID, ClassID: target.ClassID,
		CandidateUserID: target.CandidateUserID, WorkspaceID: target.WorkspaceID}
	for _, mutation := range batch.Mutations {
		s.cursor++
		change := model.AttemptWorkspaceJournalEntry{WorkspaceID: target.WorkspaceID, Cursor: s.cursor, EntryID: mutation.EntryID,
			EntryKind: model.StarterWorkspaceEntryFile, Operation: mutation.Operation, OldPath: mutation.ExpectedPath,
			NewPath: mutation.DestinationPath, Recursive: mutation.Recursive, ChangedAt: batch.AuditAt, Author: batch.Access.WorkspaceAuthor()}
		existing, known := s.entry(mutation.EntryID)
		switch mutation.Operation {
		case model.AttemptWorkspaceMutationCreateDirectory:
			change.EntryKind = model.StarterWorkspaceEntryDirectory
		case model.AttemptWorkspaceMutationCreateFile:
			change.ContentVersion = s.versions[mutation.ObjectID]
		case model.AttemptWorkspaceMutationReplaceFile:
			change.NewPath, change.ContentVersion = mutation.ExpectedPath, s.versions[mutation.ObjectID]
			s.replace(mutation.EntryID, func(item *store.CandidateAttemptWorkspaceItem) { item.ContentVersion = change.ContentVersion })
		case model.AttemptWorkspaceMutationMoveEntry:
			if known {
				change.EntryKind, change.ContentVersion = existing.Kind, existing.ContentVersion
			}
			s.replace(mutation.EntryID, func(item *store.CandidateAttemptWorkspaceItem) { item.Path = mutation.DestinationPath })
		case model.AttemptWorkspaceMutationDeleteEntry:
			if mutation.Recursive || known && existing.Kind == model.StarterWorkspaceEntryDirectory {
				change.EntryKind = model.StarterWorkspaceEntryDirectory
			}
		}
		result.Changes = append(result.Changes, change)
		s.journal = append(s.journal, change)
	}
	return result, nil
}

func (s *attemptWorkspaceLedger) replace(id model.AttemptWorkspaceEntryID, change func(*store.CandidateAttemptWorkspaceItem)) {
	for index := range s.items {
		if s.items[index].EntryID == id {
			change(&s.items[index])
		}
	}
}

// attemptSubmissionLedger is the Exam Submission Store. It seals the
// candidate's Submission or a manager's end of the Attempt, and serves the
// sealed Submission's manifest and files.
type attemptSubmissionLedger struct {
	store.ExamSubmissionStore
	f *attemptFixture

	submission *model.ExamSubmission
	manifest   []store.ExamSubmissionManifestItem

	sealTargets    []store.ExamSubmissionSealAccess
	seals          []*store.ExamSubmissionSeal
	endPreparation []store.ExamSubmissionManagerEndRequest
	ends           []*store.ExamSubmissionManagerEnd
	gets           []model.SubmissionID
	manifestReads  []store.ExamSubmissionManifestListOptions
	files          []model.AttemptWorkspaceEntryID
	commands       []*store.CommandIdempotency
}

func newAttemptSubmissionLedger(t *testing.T, f *attemptFixture) *attemptSubmissionLedger {
	t.Helper()
	item := func(path string) store.ExamSubmissionManifestItem {
		return store.ExamSubmissionManifestItem{EntryID: model.NewAttemptWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile,
			Path: path, ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: 9,
			SHA256: digestOf([]byte("protected"))}
	}
	items := []store.ExamSubmissionManifestItem{item("cmd/main.go"), item("go.mod")}
	slices.SortFunc(items, func(a, b store.ExamSubmissionManifestItem) int {
		return strings.Compare(a.EntryID.String(), b.EntryID.String())
	})
	entries := make([]model.ExamSubmissionManifestEntry, len(items))
	for index, value := range items {
		entries[index] = model.ExamSubmissionManifestEntry{EntryID: value.EntryID, Kind: value.Kind, Path: value.Path,
			ContentVersion: value.ContentVersion, MediaType: value.MediaType, SizeBytes: value.SizeBytes, SHA256: value.SHA256,
			StorageOrigin: model.AttemptWorkspaceStorageStarter, StarterObjectID: model.NewStarterWorkspaceObjectID()}
	}
	manifest, err := model.NewExamSubmissionManifest(4, entries)
	require.NoError(t, err)
	submission, err := model.NewExamSubmission(model.ExamSubmissionSpecification{ID: model.NewSubmissionID(), ReceiptCode: attemptReceiptCode,
		AttemptID: f.attempt.ID, ExamRevisionID: f.attempt.AdmissionRevisionID, WorkspaceID: f.workspace.ID, Manifest: manifest,
		BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
		Provenance:      model.ExamSubmissionCandidateSubmitted, SubmittedAt: attemptFixtureTime.Add(time.Minute)})
	require.NoError(t, err)
	return &attemptSubmissionLedger{f: f, submission: submission, manifest: items}
}

func (s *attemptSubmissionLedger) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no mutation reached the Submission Store")
	return s.commands[len(s.commands)-1]
}

func (s *attemptSubmissionLedger) authorization() *store.ExamSubmissionAuthorization {
	return &store.ExamSubmissionAuthorization{SubmissionID: s.submission.ID, ExamID: s.f.examID, SittingID: s.f.sitting.ID,
		AttemptID: s.f.attempt.ID, CandidateUserID: s.f.candidate.UserID, AcademicUnitID: s.f.unitID}
}

func (s *attemptSubmissionLedger) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	if id != s.submission.ID {
		return nil, store.NewErrNotFound("exam_submission", id.String())
	}
	return s.authorization(), nil
}

func (s *attemptSubmissionLedger) ResolveAttempt(_ context.Context, id model.ExamAttemptID) (*store.ExamSubmissionAuthorization, error) {
	if id != s.f.attempt.ID {
		return nil, store.NewErrNotFound("exam_submission", id.String())
	}
	return s.authorization(), nil
}

func (s *attemptSubmissionLedger) Get(_ context.Context, id model.SubmissionID) (*model.ExamSubmission, error) {
	s.gets = append(s.gets, id)
	if id != s.submission.ID {
		return nil, store.NewErrNotFound("exam_submission", id.String())
	}
	submission := *s.submission
	return &submission, nil
}

func (s *attemptSubmissionLedger) ListManifest(_ context.Context, options store.ExamSubmissionManifestListOptions) (*store.ExamSubmissionManifestPage, error) {
	s.manifestReads = append(s.manifestReads, options)
	items := s.manifest
	if options.AfterEntryID.IsValid() {
		after := slices.IndexFunc(items, func(item store.ExamSubmissionManifestItem) bool { return item.EntryID == options.AfterEntryID })
		items = items[after+1:]
	}
	return &store.ExamSubmissionManifestPage{SubmissionID: s.submission.ID, WorkspaceCursor: s.submission.WorkspaceCursor,
		ManifestDigest: s.submission.ManifestDigest, Items: slices.Clone(items[:min(len(items), options.Limit)]),
		HasMore: len(items) > options.Limit}, nil
}

func (s *attemptSubmissionLedger) ResolveFile(_ context.Context, id model.SubmissionID, entryID model.AttemptWorkspaceEntryID) (*store.ExamSubmissionFileSelector, error) {
	s.files = append(s.files, entryID)
	for _, item := range s.manifest {
		if id == s.submission.ID && item.EntryID == entryID {
			return &store.ExamSubmissionFileSelector{Entry: item, StorageOrigin: model.AttemptWorkspaceStorageStarter,
				StarterObjectID: model.NewStarterWorkspaceObjectID(), ContentVersion: item.ContentVersion}, nil
		}
	}
	return nil, store.NewErrNotFound("exam_submission_file", entryID.String())
}

func (s *attemptSubmissionLedger) ResolveSealTarget(_ context.Context, access store.ExamSubmissionSealAccess) (*store.ExamSubmissionSealTarget, error) {
	s.sealTargets = append(s.sealTargets, access)
	return &store.ExamSubmissionSealTarget{ExamID: s.f.examID, SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID,
		CandidateUserID: s.f.candidate.UserID, WorkspaceID: s.f.workspace.ID, CurrentRevisionID: s.f.attempt.AdmissionRevisionID,
		SealAt: attemptFixtureTime.Add(time.Minute)}, nil
}

func (s *attemptSubmissionLedger) receipt(id model.SubmissionID, code model.SubmissionReceiptCode, cursor int64, at time.Time) store.ExamSubmissionReceipt {
	return store.ExamSubmissionReceipt{AssistantTranscriptDigest: model.NewAssistantInventory().Digest, BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
		SubmissionID: id, ReceiptCode: code, AttemptID: s.f.attempt.ID, ExamRevisionID: s.f.attempt.AdmissionRevisionID,
		State: model.ExamAttemptSubmitted, WorkspaceCursor: cursor, ManifestDigest: s.submission.ManifestDigest, SubmittedAt: at}
}

func (s *attemptSubmissionLedger) Seal(_ context.Context, input *store.ExamSubmissionSeal, command *store.CommandIdempotency) (*store.ExamSubmissionSealResult, error) {
	s.seals, s.commands = append(s.seals, input), append(s.commands, command)
	return &store.ExamSubmissionSealResult{
		Receipt: s.receipt(input.SubmissionID, input.ReceiptCode, input.Access.ExpectedWorkspaceCursor, input.AuditAt),
		ExamID:  s.f.examID, SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID, CandidateUserID: s.f.candidate.UserID,
		ParticipationID: input.Access.ParticipationID, Generation: input.Access.Generation, ConnectionID: input.Access.ConnectionID,
	}, nil
}

func (s *attemptSubmissionLedger) endTarget() store.ExamSubmissionAutomaticSealTarget {
	return store.ExamSubmissionAutomaticSealTarget{ExamID: s.f.examID, SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID,
		AcademicUnitID: s.f.unitID, CandidateUserID: s.f.candidate.UserID, AttemptID: s.f.attempt.ID, WorkspaceID: s.f.workspace.ID,
		CurrentRevisionID: s.f.attempt.AdmissionRevisionID, ParticipationID: s.f.participation.ID,
		Generation: s.f.participation.Generation, ConnectionID: s.f.connection.ID}
}

func (s *attemptSubmissionLedger) PrepareManagerEnd(_ context.Context, request store.ExamSubmissionManagerEndRequest) (*store.ExamSubmissionManagerEndPreparation, error) {
	s.endPreparation = append(s.endPreparation, request)
	if err := s.f.attemptStore.fenced(request.AttemptID, request.ExpectedAttemptRevision); err != nil {
		return nil, err
	}
	return &store.ExamSubmissionManagerEndPreparation{Target: s.endTarget(), ExpectedAttemptRevision: request.ExpectedAttemptRevision,
		SealAt: attemptFixtureTime.Add(time.Minute)}, nil
}

func (s *attemptSubmissionLedger) EndByManager(_ context.Context, input *store.ExamSubmissionManagerEnd, command *store.CommandIdempotency) (*store.ExamSubmissionManagerEndResult, error) {
	s.ends, s.commands = append(s.ends, input), append(s.commands, command)
	return &store.ExamSubmissionManagerEndResult{ExamSubmissionSealResult: store.ExamSubmissionSealResult{
		Receipt: s.receipt(input.SubmissionID, input.ReceiptCode, s.submission.WorkspaceCursor, input.AuditAt),
		ExamID:  s.f.examID, SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID, CandidateUserID: s.f.candidate.UserID,
		ParticipationID: s.f.participation.ID, Generation: s.f.participation.Generation, ConnectionID: s.f.connection.ID,
	}, ConnectionClosed: true}, nil
}

// attemptDeliveryLedger is the native delivery, browser delivery and
// delivery budget Stores of the Attempt's live Participation.
type attemptDeliveryLedger struct {
	store.NativeDeliveryStore
	store.BrowserDeliveryStore
	store.DeliveryBudgetStore
	f *attemptFixture

	security model.AdmittedSecurity
	// browserActivity is what the Attempt's browser sources reported, oldest
	// first.
	browserActivity []store.BrowserActivityRecord
	// summaryErr, when set, refuses every native summary.
	summaryErr error

	nativeStatuses  []store.NativeDeliveryAccess
	nativeSummaries []*store.NativeDeliverySummaryUpdate
	nativeAppends   []*store.NativeDeliveryAppend
	browserLists    []store.BrowserActivityListOptions
	budgets         []store.DeliveryBudgetAccess
	stops           []*store.DeliveryDetailsStop
	commands        []*store.CommandIdempotency
}

func newAttemptDeliveryLedger(t *testing.T, f *attemptFixture) *attemptDeliveryLedger {
	t.Helper()
	at := attemptFixtureTime
	resolved, err := model.ResolveNativePolicy(model.NativePolicyResolution{PolicyID: "fixture-policy",
		Revision: f.sitting.ExamRevisionID.String(), Ordinal: 1, InstitutionID: f.access.Institution.ID,
		ExamRevisionID: f.sitting.ExamRevisionID, SittingID: f.sitting.ID,
		Scope:    model.SecurityPolicyScope{Kind: "admission", AdmissionScopeID: "fixture-preflight"},
		IssuedAt: at, ActiveFrom: at, ActivationTime: at, Selections: model.DefaultExamPolicySet(), Build: f.build})
	require.NoError(t, err)
	policy, err := resolved.Policy.RebindAttempt(f.attempt.ID, at)
	require.NoError(t, err)
	security := model.AdmittedSecurity{Policy: policy, PolicyContentDigest: resolved.PolicyContentDigest,
		PreflightID: "fixture-preflight", PreflightReportDigest: model.SHA256Fingerprint([]byte("report")),
		PreflightPolicyDigest: resolved.Policy.Digest, ParticipationID: f.participation.ID, Generation: 1,
		SecuritySessionID: "fixture-security-session", DeliveryStreamID: "fixture-delivery",
		RenewalIntervalSeconds: int64(model.AttemptParticipationRenewalInterval / time.Second)}
	require.NoError(t, security.Validate())
	return &attemptDeliveryLedger{f: f, security: security}
}

func (s *attemptDeliveryLedger) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no mutation reached the delivery Store")
	return s.commands[len(s.commands)-1]
}

// requests is how many requests reached the delivery Stores.
func (s *attemptDeliveryLedger) requests() int {
	return len(s.nativeStatuses) + len(s.nativeSummaries) + len(s.nativeAppends) + len(s.budgets) + len(s.stops)
}

// sameCandidate reports whether access is the candidate's, with or without
// the live Connection and credential a historical read leaves out.
func (s *attemptDeliveryLedger) sameCandidate(access store.CandidateAttemptAccess) error {
	live := s.f.candidateAccess()
	if access.ConnectionID == "" && access.ContinuityCredentialHash == "" {
		live.ConnectionID, live.ContinuityCredentialHash = "", ""
	}
	if access != live {
		return store.NewErrNotFound("exam_attempt", access.AttemptID.String())
	}
	return nil
}

func (s *attemptDeliveryLedger) ResolveNativeDeliveryTarget(_ context.Context, access store.NativeDeliveryAccess) (*store.NativeDeliveryTarget, error) {
	if err := s.sameCandidate(access.Access); err != nil {
		return nil, err
	}
	return &store.NativeDeliveryTarget{Security: s.security, MatrixDigest: model.SHA256Fingerprint([]byte("matrix")),
		ExamID: s.f.examID, SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID}, nil
}

func (s *attemptDeliveryLedger) NativeDeliveryStatus(_ context.Context, access store.NativeDeliveryAccess) (*model.NativeSecurityStreamStatus, error) {
	s.nativeStatuses = append(s.nativeStatuses, access)
	if err := s.sameCandidate(access.Access); err != nil {
		return nil, err
	}
	return &model.NativeSecurityStreamStatus{NativeDeliveryProgress: model.NativeDeliveryProgress{MissingBatchRanges: []model.SequenceRange{},
		ServerTime: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}, StreamID: access.StreamID, DetailMode: "collecting"}, nil
}

func (s *attemptDeliveryLedger) UpdateNativeDeliverySummary(_ context.Context, input *store.NativeDeliverySummaryUpdate, command *store.CommandIdempotency) (*model.NativeSecurityStreamStatus, error) {
	s.nativeSummaries, s.commands = append(s.nativeSummaries, input), append(s.commands, command)
	if s.summaryErr != nil {
		return nil, s.summaryErr
	}
	return s.NativeDeliveryStatus(context.Background(), input.Access)
}

func (s *attemptDeliveryLedger) AppendNativeDelivery(_ context.Context, input *store.NativeDeliveryAppend, command *store.CommandIdempotency) (*store.NativeDeliveryAppended, error) {
	s.nativeAppends, s.commands = append(s.nativeAppends, input), append(s.commands, command)
	raw, err := input.Batch.Canonical()
	if err != nil {
		return nil, err
	}
	at := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	return &store.NativeDeliveryAppended{Acknowledgement: &model.NativeSecurityAcknowledgement{
		Receipt: model.NativeBatchReceipt{StreamID: input.Batch.StreamID, BatchSequence: input.Batch.BatchSequence,
			RequestDigest: model.SHA256Fingerprint(raw), ReceivedAt: at},
		NativeDeliveryProgress: model.NativeDeliveryProgress{HighestContiguousBatchSequence: 1, SettledThroughBatchSequence: 1,
			HighestSeenBatchSequence: 1, MissingBatchRanges: []model.SequenceRange{}, ServerTime: at}}}, nil
}

func (s *attemptDeliveryLedger) ListBrowserActivity(_ context.Context, options store.BrowserActivityListOptions) ([]store.BrowserActivityRecord, error) {
	s.browserLists = append(s.browserLists, options)
	records := s.browserActivity
	if options.AfterSourceID.IsValid() {
		after := slices.IndexFunc(records, func(record store.BrowserActivityRecord) bool {
			return record.SourceSessionID == options.AfterSourceID && record.Event.Sequence == options.AfterSequence
		})
		records = records[after+1:]
	}
	return slices.Clone(records[:min(len(records), options.Limit)]), nil
}

func (s *attemptDeliveryLedger) DeliveryBudget(_ context.Context, access store.DeliveryBudgetAccess) (*model.DeliveryBudgetSnapshot, error) {
	s.budgets = append(s.budgets, access)
	if err := s.sameCandidate(access.Access); err != nil {
		return nil, err
	}
	return testHTTPDeliveryBudget(access.ParticipationID), nil
}

func (s *attemptDeliveryLedger) ResolveLiveDeliveryTarget(_ context.Context, access store.CandidateAttemptAccess) (*store.DeliveryOwnerTarget, error) {
	if err := s.f.admits(access); err != nil {
		return nil, err
	}
	return &store.DeliveryOwnerTarget{SittingID: s.f.sitting.ID, ClassID: s.f.sitting.ClassID}, nil
}

func (s *attemptDeliveryLedger) StopDeliveryDetails(_ context.Context, input *store.DeliveryDetailsStop, command *store.CommandIdempotency) (*model.StopDeliveryDetailsResult, error) {
	s.stops, s.commands = append(s.stops, input), append(s.commands, command)
	value := &model.StopDeliveryDetailsResult{Budget: *testHTTPDeliveryBudget(s.f.participation.ID), Browser: []model.BrowserSourceStatus{}}
	value.Budget.Browser.Participation.SummaryOnly = true
	reason := input.Request.Reason
	value.Budget.Browser.Participation.StopReason = &reason
	return value, nil
}

// testHTTPDeliveryBudget is the delivery budget of a Participation that has
// used none of it.
func testHTTPDeliveryBudget(part model.AttemptParticipationID) *model.DeliveryBudgetSnapshot {
	return &model.DeliveryBudgetSnapshot{ParticipationID: part, Generation: 1, Native: model.DeliveryFamilyBudget{Participation: model.NewDeliveryQuotaUsage(true, false), Attempt: model.NewDeliveryQuotaUsage(true, true), PendingByteLimit: model.DeliveryPendingByteLimit}, Browser: model.DeliveryFamilyBudget{Participation: model.NewDeliveryQuotaUsage(false, false), Attempt: model.NewDeliveryQuotaUsage(false, true), PendingByteLimit: model.DeliveryPendingByteLimit}, ServerTime: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}
}

// attemptEnvironmentLedger is the Attempt Environment Store; a replacement
// asks the owner to start the next incarnation.
type attemptEnvironmentLedger struct {
	store.AttemptEnvironmentStore

	replacements []*store.AttemptEnvironmentReplacement
	commands     []*store.CommandIdempotency
}

func (s *attemptEnvironmentLedger) Replace(_ context.Context, input *store.AttemptEnvironmentReplacement, command *store.CommandIdempotency) (*store.AttemptEnvironmentReplacementResult, error) {
	s.replacements, s.commands = append(s.replacements, input), append(s.commands, command)
	return &store.AttemptEnvironmentReplacementResult{Environment: &model.AttemptEnvironment{AttemptID: input.AttemptID,
		State: model.AttemptEnvironmentRunning, Desired: model.AttemptEnvironmentDesiredRunning, Incarnation: input.ExpectedIncarnation},
		ReplaceBefore: input.ExpectedIncarnation + 1}, nil
}

// attemptContent is the protected content the Attempt reads and writes:
// every Resource and Workspace object reads "protected", staged Workspace
// bodies are digested as the content store does, and screen clip chunks are
// checked against their declared digest.
type attemptContent struct {
	mu sync.Mutex
	// staged is every Workspace body staged, in order.
	staged [][]byte
	chunks map[int][]byte
}

// invalidWorkspaceContent is the content store's refusal of a body that
// cannot be read or is not its declared size.
type invalidWorkspaceContent struct{ cause error }

func (e invalidWorkspaceContent) Error() string                      { return "invalid Attempt Workspace content" }
func (e invalidWorkspaceContent) Unwrap() error                      { return e.cause }
func (invalidWorkspaceContent) InvalidAttemptWorkspaceContent() bool { return true }

func (*attemptContent) protected() io.ReadCloser { return io.NopCloser(strings.NewReader("protected")) }

func (c *attemptContent) OpenExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error) {
	return c.protected(), nil
}

func (c *attemptContent) OpenStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID) (io.ReadCloser, error) {
	return c.protected(), nil
}

func (c *attemptContent) OpenAttemptWorkspaceObject(context.Context, model.AttemptWorkspaceObjectID) (io.ReadCloser, error) {
	return c.protected(), nil
}

func (c *attemptContent) StageAttemptWorkspaceObject(_ context.Context, _ model.AttemptWorkspaceObjectID, body io.Reader, size int64, mediaType string) (*model.AttemptWorkspaceContent, error) {
	raw, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return nil, invalidWorkspaceContent{cause: err}
	}
	if int64(len(raw)) != size {
		return nil, invalidWorkspaceContent{cause: errors.New("the body is not its declared size")}
	}
	c.mu.Lock()
	c.staged = append(c.staged, raw)
	c.mu.Unlock()
	return &model.AttemptWorkspaceContent{MediaType: mediaType, SizeBytes: size, SHA256: digestOf(raw)}, nil
}

func (c *attemptContent) StageScreenClipChunk(_ context.Context, _ model.ScreenClipID, index int, body io.Reader, size int64, expected string) error {
	raw, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) != size || digestOf(raw) != expected {
		return model.ErrScreenClipChunkInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunks[index] = raw
	return nil
}

func (c *attemptContent) OpenScreenClipChunk(_ context.Context, _ model.ScreenClipID, index int, within, length int64) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	chunk, ok := c.chunks[index]
	if !ok || within+length > int64(len(chunk)) {
		return nil, errors.New("screen clip chunk is missing")
	}
	return io.NopCloser(bytes.NewReader(chunk[within : within+length])), nil
}

// attemptDesktopBuilds resolves every native source and Desktop to the
// fixture's build.
type attemptDesktopBuilds struct{ f *attemptFixture }

func (d attemptDesktopBuilds) ResolveAttemptDesktopBuild(context.Context, model.Principal) (attempt.DesktopBuildResolution, error) {
	return attempt.DesktopBuildResolution{Build: d.f.build, CompatibilityPolicyRevision: 1}, nil
}

func (d attemptDesktopBuilds) ResolveNativeDeliveryBuild(context.Context, attempt.NativeDeliveryBuildIdentity) (model.DesktopBuildTuple, error) {
	return d.f.build, nil
}

// unusedAttemptStores are the Attempt Stores no handler test reaches.
type unusedAttemptStores struct {
	store.ExamFocusLossStore
	store.SecurityPreflightStore
}

func candidateTestRuntimeCapabilities(t *testing.T, at time.Time, record *model.ExamAttempt) store.CandidateRuntimeCapabilities {
	t.Helper()
	manifest := model.CurrentAttemptConfigurationManifestFingerprint()
	configuration, err := (model.AttemptConfigurationCandidate{ManifestFingerprint: manifest, RegistryFingerprint: "fnv1a64:" + strings.Repeat("b", 16), UserSettingsRevision: model.NewUserSettingsRevision(), DesktopBuild: "test-build", DesktopTarget: "darwin-arm64", Presentation: model.AttemptConfigurationPresentation{ColorTheme: "dark", ZoomPercent: 100, EditorFontSizePX: 14, EditorLineHeightPX: 22, ScreenReaderMode: "auto", AnnouncementMode: "auto", CursorStyle: "line", CursorBlinking: "blink"}, ApprovedCommands: []string{}, ApprovedKeybindings: []string{}}).Freeze(model.NewID())
	require.NoError(t, err)
	return store.CandidateRuntimeCapabilities{SchemaVersion: 1, ServerTime: at,
		InteractionState: store.CandidateInteractionInteractive,
		AttemptConfiguration: store.CandidateAttemptConfiguration{Revision: configuration.Revision, Presentation: configuration.Presentation,
			ApprovedCommands: append([]string{}, configuration.ApprovedCommands...), ApprovedKeybindings: append([]string{}, configuration.ApprovedKeybindings...), Digest: configuration.Digest},
		FocusLossCollectionEnabled: true, WorkspaceMutationAllowed: true, SubmissionAllowed: true,
		Browser: store.CandidateBrowserCapability{State: store.CandidateBrowserDisabled},
		ExamRevision: store.CandidateExamRevisionCapability{AdmissionRevisionID: record.AdmissionRevisionID,
			CurrentRevisionID: model.NewExamRevisionID()},
		Departure: store.CandidateDepartureCapability{Allowed: false, Reason: "attempt_in_progress"}}
}

func (s *attemptLedger) ListCandidateStarterWorkspace(_ context.Context, access store.CandidateAttemptAccess, _ int) ([]store.CandidateStarterWorkspaceFile, error) {
	return []store.CandidateStarterWorkspaceFile{}, s.prove(access)
}
