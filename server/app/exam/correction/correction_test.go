// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type correctionFixture struct {
	service        *Service
	inv            invocation.Invocation
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
	// grants is the real authorizer and auditor; the caller manages Sittings
	// in the Exam's unit, ordinarily and by override.
	grants    *accesstest.Fixture
	publisher *realtimetest.Publisher
	sittings  *correctionSittingsFake
	content   *correctionContentFake
	revisions *correctionRevisionsFake
}

func newCorrectionFixture(t *testing.T) *correctionFixture {
	t.Helper()
	f := &correctionFixture{examID: model.NewExamID(), sittingID: model.NewExamSittingID(), baseRevisionID: model.NewExamRevisionID(), unitID: model.NewAcademicUnitID(), userID: model.NewUserID(), at: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	f.persistence = &correctionStoreFake{f: f}
	f.access = &correctionAccessFake{f: f}
	f.memberships = &correctionMembershipFake{f: f}
	f.publisher = &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		if call.Method == realtimetest.Publish {
			f.order = append(f.order, "effect")
		}
	}}
	f.sittings = &correctionSittingsFake{f: f}
	f.content = &correctionContentFake{f: f}
	f.revisions = &correctionRevisionsFake{}
	f.grants = accesstest.New(t, accesstest.Options{
		Grants: []accesstest.Grant{{
			UserID:    f.userID,
			Actions:   []model.Action{model.ActionExamSittingManage, model.ActionExamSittingManageOverride},
			ScopeType: model.RoleScopeAcademicUnit,
			ScopeID:   f.unitID.String(),
		}},
		Exams:    f.access,
		Sittings: f.sittings,
		Observe:  func(event string) { f.order = append(f.order, event) },
	})
	service, err := New(Dependencies{
		Persistence:       f.persistence,
		Revisions:         f.revisions,
		Sittings:          f.sittings,
		Access:            f.access,
		Memberships:       f.memberships,
		Authorizer:        f.grants.Authorizer,
		Auditor:           f.grants.Auditor,
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
	f.inv = invocation.New(model.Principal{UserID: f.userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: f.at}, model.RequestMetadata{})
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

// Resolve gives the real authorizer the Exam's owning unit.
func (a *correctionAccessFake) Resolve(context.Context, model.ExamID) (*model.Exam, error) {
	exam, _ := model.NewExam(a.f.examID, a.f.unitID, a.f.userID, a.f.at)
	return exam, nil
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

// decisions describes each recorded authorization decision as
// "<action> <resource type>:<resource id> <status>".
func (f *correctionFixture) decisions() []string {
	var described []string
	for _, event := range f.grants.Audits.Decisions() {
		described = append(described, event.Action+" "+string(event.Resource.Type)+":"+event.Resource.ID+" "+string(event.Status))
	}
	return described
}

// auditValue returns the value the one recorded mutation attempt stored, with
// whole numbers decoded as int64.
func (f *correctionFixture) auditValue(t *testing.T) map[string]any {
	t.Helper()
	attempts := f.grants.Audits.Attempts()
	require.Len(t, attempts, 1, "mutation attempts")
	var parameters struct {
		Value map[string]any `json:"value"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(attempts[0].Parameters)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&parameters))
	for key, value := range parameters.Value {
		if number, ok := value.(json.Number); ok {
			if whole, err := number.Int64(); err == nil {
				parameters.Value[key] = whole
			}
		}
	}
	return parameters.Value
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

// Resolve gives the real authorizer the Sitting's Exam.
func (s *correctionSittingsFake) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	return &store.ExamSittingSnapshot{Sitting: &model.ExamSitting{ID: id, ExamID: s.f.examID}}, nil
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
