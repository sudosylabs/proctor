// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestStageResourceContentReservesBeforeDeterministicWriteAndReturnsAuthoritativeMetadata(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	sha := strings.Repeat("a", 64)
	got, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: sha, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.False(t, got.StageID.IsZero())
	require.False(t, got.ResourceID.IsZero())
	require.Equal(t, model.ExamResourceMediaText, got.MediaType)
	require.Equal(t, int64(5), got.Size)
	require.Equal(t, sha, got.SHA256)
	require.False(t, got.ExpiresAt.IsZero())
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable reservation, and bytes are stored only inside the
	// reservation before it becomes ready.
	require.Equal(t, []string{"access", "membership", "authorize", "audit.begin", "reserve", "content.store", "ready"}, f.order)
	require.NotEmpty(t, f.persistence.reservation.AuditEventID)
	require.False(t, f.persistence.reservation.AuditAt.IsZero())
	require.Equal(t, model.ActionExamSittingManage, f.authorizer.action, "authorization")
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: f.sittingID.String()}, f.authorizer.resource, "authorization")
	require.False(t, f.persistence.reservation.ManagerOverride, "authorization")
	for _, forbidden := range []string{"media_type", "size", "sha256", "stage_id", "private_reason", "display_name"} {
		_, ok := f.auditor.value[forbidden]
		require.False(t, ok, "unsafe audit field %q in %#v", forbidden, f.auditor.value)
	}
	wantIdempotency, prepareErr := prepareStageIdempotency(f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Size: 5, ExpectedSHA256: sha, IdempotencyKey: "test-key",
	})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestStageResourceContentReadyReplaySkipsContentWrite(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.persistence.returnReady = true
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, 0, f.content.calls, "content")
	require.Equal(t, 0, f.persistence.readyCalls, "content")
}

type correctionCapacityError struct{}

func (correctionCapacityError) Error() string         { return "content capacity is unavailable" }
func (correctionCapacityError) WorkCapacityExceeded() {}

func TestStageResourceContentCapacityRefusalLeavesOnlyPendingReservation(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.content.err = correctionCapacityError{}
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "service.busy", fault.Code())
	require.ErrorIs(t, err, f.content.err)
	require.NotNil(t, f.persistence.reservation, "refused content became ready or published: store=%#v effects", f.persistence)
	require.Equal(t, 0, f.persistence.readyCalls, "refused content became ready or published: store=%#v effects", f.persistence)
	require.Nil(t, f.persistence.application, "refused content became ready or published: store=%#v effects", f.persistence)
	require.Empty(t, f.publisher.Calls(), "refused content became ready or published: store=%#v effects", f.persistence)
	// Contract (guardrail 8): the audit begins before the durable reservation that
	// the refused bytes were stored under.
	require.Equal(t, []string{"access", "membership", "authorize", "audit.begin", "reserve", "content.store"}, f.order)
}

func TestStageResourceContentUsesReservedTimeForConcurrentReplay(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.persistence.stageCreatedAt = f.at.Add(-time.Minute)
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.True(t, f.content.createdAt.Equal(f.persistence.stageCreatedAt), "rendition created_at=%s want reserved", f.content.createdAt)
}

func TestStageResourceContentUsesExplicitOverrideWithoutCurrentUnitMembership(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.memberships.empty = true
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, model.ActionExamSittingManageOverride, f.authorizer.action, "action=%s reservation=%#v", f.authorizer.action, f.persistence.reservation)
	require.True(t, f.persistence.reservation.ManagerOverride, "action=%s reservation=%#v", f.authorizer.action, f.persistence.reservation)
}

func TestStageResourceContentStopsWhenCurrentManagerMembershipCannotBeRead(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	failure := errors.New("membership unavailable")
	f.memberships.err = failure
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.sitting.correction.unavailable", fault.Code())
	require.ErrorIs(t, err, failure)
	// Without current membership nothing is authorized or reserved.
	require.Empty(t, f.authorizer.action, "authorized without current membership")
	require.Nil(t, f.persistence.reservation, "reserved without current membership")
}

func TestStageReplacementPinsFileEntryFromExactBaseRevision(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	resourceID, entryID := model.NewExamResourceID(), model.NewFileEntryID()
	f.revisions.snapshot = &model.ExamRevision{ID: f.baseRevisionID, ExamID: f.examID, Resources: []model.ExamRevisionResource{{ResourceID: resourceID, FileEntryID: entryID}}}
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceReplacement, ResourceID: resourceID, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Nil(t, f.persistence.reservation.Entry)
	require.Equal(t, entryID, f.persistence.reservation.FileEntryID)
	require.Equal(t, resourceID, f.persistence.reservation.ResourceID)
}

func TestApplyUsesOneAtomicStoreCommandAndSuppressesReplayEffects(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.persistence.applyReplayed = true
	result, err := f.service.ApplyExamSittingCorrection(context.Background(), f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID, ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID, Instructions: OptionalInstructions{Present: true, Markdown: "Updated"}, Resources: []ResourceManifestItem{}, AffectedCapabilities: []model.CandidateCapability{model.CandidateCapabilityBrowser, model.CandidateCapabilitySubmission, model.CandidateCapabilityWorkspace}, CandidateSummary: "The instructions were corrected.", AcknowledgementRequired: true, PrivateReason: "Correct a discovered ambiguity", IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, f.baseRevisionID, result.PreviousRevisionID)
	require.False(t, result.RevisionID.IsZero())
	require.Equal(t, int64(4), result.SittingRevision)
	require.True(t, result.EffectiveAt.Equal(f.at.Add(-time.Minute)))
	require.Empty(t, f.publisher.Calls())
	require.NotNil(t, f.persistence.application)
	require.NotNil(t, f.persistence.application.InstructionsMarkdown)
	require.Equal(t, "Updated", *f.persistence.application.InstructionsMarkdown)
	require.NotEmpty(t, f.persistence.application.PrivateReason)
	require.Empty(t, f.publisher.Calls(), "replay effects")
	for _, forbidden := range []string{"instructions_markdown", "resources", "private_reason", "stage_id", "sha256"} {
		_, ok := f.auditor.value[forbidden]
		require.False(t, ok, "unsafe audit field %q", forbidden)
	}
	wantIdempotency, prepareErr := prepareApplyIdempotency(f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID,
		ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID,
		Instructions: OptionalInstructions{Present: true, Markdown: "Updated"}, Resources: []ResourceManifestItem{},
		AffectedCapabilities: []model.CandidateCapability{model.CandidateCapabilityBrowser, model.CandidateCapabilitySubmission, model.CandidateCapabilityWorkspace}, CandidateSummary: "The instructions were corrected.", AcknowledgementRequired: true,
		PrivateReason: "Correct a discovered ambiguity", IdempotencyKey: "test-key"})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestApplyPublishesOnlyAfterCommitAndReportsTransientFailure(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	unavailable := errors.New("realtime unavailable")
	f.publisher.Fail = func(realtimetest.Call) error { return unavailable }
	candidateID := model.NewUserID()
	f.sittings.candidateIDs = []model.UserID{candidateID}
	result, err := f.service.ApplyExamSittingCorrection(context.Background(), f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID, ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID, Instructions: OptionalInstructions{Present: true, Markdown: "Updated"}, Resources: []ResourceManifestItem{}, AffectedCapabilities: []model.CandidateCapability{model.CandidateCapabilityBrowser, model.CandidateCapabilitySubmission, model.CandidateCapabilityWorkspace}, CandidateSummary: "The instructions were corrected.", PrivateReason: "Correct a discovered ambiguity", IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.NotNil(t, f.persistence.application)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effects;
	// the candidates are read only after the three Sitting-wide publications.
	require.Equal(t, []string{"access", "membership", "authorize", "audit.begin", "apply", "effect", "effect", "effect", "candidates", "effect"}, f.order)
	managerEvent, err := realtime.NewExamSittingContentCorrectedEvent(f.examID, f.sittingID, f.baseRevisionID, result.RevisionID, 4, result.EffectiveAt)
	require.NoError(t, err)
	candidateEvent, err := realtime.NewCandidateExamSittingContentCorrectedEvent(f.examID, f.sittingID, f.baseRevisionID, result.RevisionID, 4, result.EffectiveAt)
	require.NoError(t, err)
	boardEvent, err := realtime.NewManagerSittingBoardChangedEvent(f.examID, f.sittingID)
	require.NoError(t, err)
	activityEvent, err := realtime.NewCandidateExamActivityChangedEvent(candidateID)
	require.NoError(t, err)
	// Contract: managers learn of the correction before candidates, the board
	// and candidate activity invalidations follow, every publication is
	// attempted despite the failures, and the joined failure is reported once.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.Publish, Event: managerEvent},
		{Method: realtimetest.Publish, Event: candidateEvent},
		{Method: realtimetest.Publish, Event: boardEvent},
		{Method: realtimetest.Publish, Event: activityEvent},
		{Method: realtimetest.Report, Operation: "exam_sitting_content_corrected", Err: errors.Join(unavailable, unavailable, unavailable, errors.Join(unavailable))},
	}, f.publisher.Calls())
}

func TestApplyRejectsNonCanonicalPrivateReasonBeforeAuthorization(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	_, err := f.service.ApplyExamSittingCorrection(context.Background(), f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID, ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID, PrivateReason: " padded ", IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.sitting.correction.invalid", fault.Code())
	require.Empty(t, f.order)
}

type correctionFixture struct {
	service        *Service
	invocation     invocation.Invocation
	examID         model.ExamID
	sittingID      model.ExamSittingID
	baseRevisionID model.ExamRevisionID
	unitID         model.AcademicUnitID
	userID         model.UserID
	at             time.Time
	order          []string
	persistence    *correctionStoreFake
	access         *correctionAccessFake
	memberships    *correctionMembershipFake
	authorizer     *correctionAuthorizerFake
	auditor        *correctionAuditFake
	publisher      *realtimetest.Publisher
	sittings       *correctionSittingsFake
	content        *correctionContentFake
	revisions      *correctionRevisionsFake
}

func newCorrectionFixture(t *testing.T) *correctionFixture {
	t.Helper()
	f := &correctionFixture{examID: model.NewExamID(), sittingID: model.NewExamSittingID(), baseRevisionID: model.NewExamRevisionID(), unitID: model.NewAcademicUnitID(), userID: model.NewUserID(), at: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	f.persistence = &correctionStoreFake{f: f}
	f.access = &correctionAccessFake{f: f}
	f.memberships = &correctionMembershipFake{f: f}
	f.authorizer = &correctionAuthorizerFake{f: f}
	f.auditor = &correctionAuditFake{f: f}
	f.publisher = &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		if call.Method == realtimetest.Publish {
			f.order = append(f.order, "effect")
		}
	}}
	f.sittings = &correctionSittingsFake{f: f}
	f.content = &correctionContentFake{f: f}
	f.revisions = &correctionRevisionsFake{}
	service, err := New(Dependencies{
		Persistence:       f.persistence,
		Revisions:         f.revisions,
		Sittings:          f.sittings,
		Access:            f.access,
		Memberships:       f.memberships,
		Authorizer:        f.authorizer,
		Auditor:           f.auditor,
		Publisher:         f.publisher,
		Content:           f.content,
		Now:               func() time.Time { return f.at },
		NewStageID:        model.NewExamCorrectionResourceStageID,
		NewResourceID:     model.NewExamResourceID,
		NewEntryID:        model.NewFileEntryID,
		NewFileRevisionID: model.NewFileRevisionID,
		NewLeaseID:        model.NewUploadLeaseID,
		NewRenditionID:    model.NewFileRenditionID,
		NewExamRevisionID: model.NewExamRevisionID,
		InstitutionOrigin: "https://institution.example",
	})
	require.NoError(t, err)
	f.service = service
	f.invocation = invocation.New(model.Principal{UserID: f.userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: f.at}, model.RequestMetadata{})
	return f
}

type correctionStoreFake struct {
	f              *correctionFixture
	reservation    *store.ExamCorrectionResourceStageReservation
	stageCreatedAt time.Time
	readyCalls     int
	returnReady    bool
	application    *store.ExamCorrectionApplication
	applyReplayed  bool
	idempotency    *store.CommandIdempotency
}

func (s *correctionStoreFake) ReserveResourceStage(_ context.Context, in *store.ExamCorrectionResourceStageReservation, command *store.CommandIdempotency) (*store.ExamCorrectionResourceStage, error) {
	s.f.order = append(s.f.order, "reserve")
	s.reservation, s.idempotency = in, command
	stage := &store.ExamCorrectionResourceStage{ID: in.StageID, ExamID: in.ExamID, SittingID: in.SittingID, BaseRevisionID: in.BaseRevisionID, Target: in.Target, ResourceID: in.ResourceID, FileEntryID: in.FileEntryID, FileRevisionID: in.Revision.ID, UploadLeaseID: in.Lease.ID, RenditionID: in.RenditionID, CreatedByUserID: in.ActorUserID, State: store.ExamCorrectionResourceStagePending, CreatedAt: in.CreatedAt, ExpiresAt: in.Lease.ExpiresAt}
	if !s.stageCreatedAt.IsZero() {
		stage.CreatedAt = s.stageCreatedAt
	}
	if s.returnReady {
		stage.State = store.ExamCorrectionResourceStageReady
		stage.Rendition = correctionRendition(in.RenditionID, in.Revision.ID, in.CreatedAt)
	}
	return stage, nil
}
func (s *correctionStoreFake) MarkResourceStageReady(_ context.Context, in *store.ExamCorrectionResourceStageReadiness) (*store.ExamCorrectionResourceStage, error) {
	s.f.order = append(s.f.order, "ready")
	s.readyCalls++
	r := in.Rendition
	return &store.ExamCorrectionResourceStage{ID: s.reservation.StageID, ExamID: s.reservation.ExamID, SittingID: s.reservation.SittingID, BaseRevisionID: s.reservation.BaseRevisionID, Target: s.reservation.Target, ResourceID: s.reservation.ResourceID, FileEntryID: s.reservation.FileEntryID, FileRevisionID: s.reservation.Revision.ID, UploadLeaseID: s.reservation.Lease.ID, RenditionID: s.reservation.RenditionID, CreatedByUserID: s.reservation.ActorUserID, State: store.ExamCorrectionResourceStageReady, ExpiresAt: s.reservation.Lease.ExpiresAt, Rendition: r}, nil
}
func (s *correctionStoreFake) Apply(_ context.Context, in *store.ExamCorrectionApplication, command *store.CommandIdempotency) (*store.ExamCorrectionResult, error) {
	s.f.order = append(s.f.order, "apply")
	s.application, s.idempotency = in, command
	sitting := &model.ExamSitting{ID: in.SittingID, ExamID: in.ExamID, ExamRevisionID: in.RevisionID, State: model.ExamSittingOpen, Revision: in.ExpectedSittingRevision + 1, UpdatedAt: in.AppliedAt}
	effectiveAt := in.AppliedAt.Add(-time.Minute)
	return &store.ExamCorrectionResult{Revision: &store.ExamRevisionSummary{ID: in.RevisionID, ExamID: in.ExamID, Number: 2, Kind: model.ExamRevisionPublicationLiveCorrection, PublishedAt: effectiveAt}, Sitting: &store.ExamSittingSnapshot{Sitting: sitting}, PreviousRevisionID: in.CurrentRevisionID, EffectiveAt: effectiveAt, Replayed: s.applyReplayed}, nil
}
func correctionRendition(id model.FileRenditionID, revisionID model.FileRevisionID, at time.Time) *model.FileRendition {
	r, _ := model.NewFileRendition(id, revisionID, "original", string(model.ExamResourceMediaText), 5, 0, 0, strings.Repeat("a", 64), at)
	return r
}

type correctionRevisionsFake struct {
	store.ExamRevisionStore
	snapshot *model.ExamRevision
}

func (r *correctionRevisionsFake) GetSnapshot(context.Context, model.ExamID, model.ExamRevisionID) (*model.ExamRevision, error) {
	if r.snapshot == nil {
		return nil, store.NewErrNotFound("exam_revision", "")
	}
	return r.snapshot, nil
}

type correctionAccessFake struct {
	store.ExamAuthoringStore
	f *correctionFixture
}

func (a *correctionAccessFake) Access(context.Context, model.ExamID, model.UserID) (*store.ExamAccessSnapshot, error) {
	a.f.order = append(a.f.order, "access")
	exam, _ := model.NewExam(a.f.examID, a.f.unitID, a.f.userID, a.f.at)
	return &store.ExamAccessSnapshot{Exam: exam, ActorIsManager: true}, nil
}

type correctionMembershipFake struct {
	store.AcademicUnitMemberStore
	f     *correctionFixture
	empty bool
	err   error
}

func (m *correctionMembershipFake) ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.AcademicUnitMember, error) {
	m.f.order = append(m.f.order, "membership")
	if m.empty {
		return nil, m.err
	}
	return []*model.AcademicUnitMember{{AcademicUnitID: m.f.unitID, UserID: m.f.userID}}, m.err
}

type correctionAuthorizerFake struct {
	f        *correctionFixture
	action   model.Action
	resource model.Resource
}

func (a *correctionAuthorizerFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource) error {
	a.f.order = append(a.f.order, "authorize")
	a.action, a.resource = action, resource
	return nil
}

type correctionAuditFake struct {
	f     *correctionFixture
	value map[string]any
}

func (a *correctionAuditFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	a.f.order = append(a.f.order, "audit.begin")
	a.value = attempt.Value
	return model.NewAuditEventID(), nil
}
func (a *correctionAuditFake) Fail(context.Context, model.AuditEventID, string) error {
	a.f.order = append(a.f.order, "audit.fail")
	return nil
}

// correctionSittingsFake answers the candidates of the corrected Sitting in
// one page and places the read in the fixture's order log.
type correctionSittingsFake struct {
	store.ExamSittingStore
	f            *correctionFixture
	candidateIDs []model.UserID
}

func (s *correctionSittingsFake) ListCandidateInvalidationTargetsBySitting(
	context.Context,
	model.ExamSittingID,
	model.UserID,
	int,
) ([]model.UserID, error) {
	s.f.order = append(s.f.order, "candidates")
	return s.candidateIDs, nil
}

type correctionContentFake struct {
	f         *correctionFixture
	calls     int
	createdAt time.Time
	err       error
}

func (c *correctionContentFake) StoreExamResourceRendition(_ context.Context, revisionID model.FileRevisionID, renditionID model.FileRenditionID, _ model.ExamResourceMediaType, _ io.Reader, _ int64, at time.Time) (model.FileRendition, error) {
	c.f.order = append(c.f.order, "content.store")
	c.calls++
	c.createdAt = at
	if c.err != nil {
		return model.FileRendition{}, c.err
	}
	return *correctionRendition(renditionID, revisionID, at), nil
}
