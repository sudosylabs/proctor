// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
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

type testCapacityError struct{}

func (testCapacityError) Error() string         { return "content capacity is unavailable" }
func (testCapacityError) WorkCapacityExceeded() {}

func TestMetadataAndOrderNoOpsDoNotBeginAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	record := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
	f.persistence.items = []store.ExamResourceRecord{record}
	_, err := f.service.EditExamResourceMetadata(context.Background(), f.invocation, EditMetadataCommand{ExamID: f.examID, ResourceID: record.Resource.ID, ExpectedDraftRevision: 1, DisplayName: examResourceString(record.Resource.DisplayName), DescriptionMarkdown: examResourceString(record.Resource.DescriptionMarkdown), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "metadata no-op error")
	require.Equal(t, "exam.resource.no_changes", fault.Code(), "metadata no-op error")
	require.False(t, f.auditor.began, "metadata no-op error")
	_, err = f.service.ReorderExamResources(context.Background(), f.invocation, ReorderCommand{ExamID: f.examID, ExpectedDraftRevision: 1, ResourceIDs: []model.ExamResourceID{record.Resource.ID}, IdempotencyKey: "test-key"})
	require.ErrorAs(t, err, &fault, "reorder no-op error")
	require.Equal(t, "exam.resource.no_changes", fault.Code(), "reorder no-op error")
	require.False(t, f.auditor.began, "reorder no-op error")
}

type fixture struct {
	service     *Service
	invocation  invocation.Invocation
	examID      model.ExamID
	unitID      model.AcademicUnitID
	userID      model.UserID
	order       []string
	persistence *storeFake
	access      *accessFake
	memberships *membershipFake
	authorizer  *authFake
	auditor     *auditFake
	publisher   *realtimetest.Publisher
	content     *contentFake
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{examID: model.NewExamID(), unitID: model.NewAcademicUnitID(), userID: model.NewUserID()}
	f.persistence = &storeFake{f: f}
	f.access = &accessFake{f: f, draftRevision: 1}
	f.memberships = &membershipFake{f: f}
	f.auditor = &auditFake{f: f}
	f.publisher = &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		if call.Method == realtimetest.Publish {
			f.order = append(f.order, "effect")
		}
	}}
	f.content = &contentFake{f: f}
	f.authorizer = &authFake{f: f}
	at := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service, err := New(Dependencies{
		Persistence:   f.persistence,
		Access:        f.access,
		Memberships:   f.memberships,
		Authorizer:    f.authorizer,
		Auditor:       f.auditor,
		Publisher:     f.publisher,
		Content:       f.content,
		Now:           func() time.Time { return at },
		NewResourceID: model.NewExamResourceID,
		NewEntryID:    model.NewFileEntryID,
		NewRevisionID: model.NewFileRevisionID,
		NewLeaseID:    model.NewUploadLeaseID,
	})
	require.NoError(t, err)
	f.service = service
	f.invocation = invocation.New(model.Principal{UserID: f.userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: at}, model.RequestMetadata{})
	return f
}

type storeFake struct {
	f                *fixture
	items            []store.ExamResourceRecord
	finalization     *store.ExamResourceUploadFinalization
	reservation      *store.ExamResourceUploadReservation
	replayed         bool
	updateCalls      int
	reorderCalls     int
	updateErr        error
	reorderErr       error
	reorderResult    *store.ExamResourceCommandResult
	metadataUpdate   *store.ExamResourceMetadataUpdate
	updateResult     *store.ExamResourceCommandResult
	reserveCalls     int
	finalizeCalls    int
	replayAfterFirst bool
	committed        *store.ExamResourceRecord
	idempotency      *store.CommandIdempotency
}

func (s *storeFake) List(context.Context, model.ExamID) ([]store.ExamResourceRecord, error) {
	s.f.order = append(s.f.order, "list")
	return append([]store.ExamResourceRecord(nil), s.items...), nil
}
func (s *storeFake) Get(_ context.Context, _ model.ExamID, id model.ExamResourceID) (*store.ExamResourceRecord, error) {
	for i := range s.items {
		if s.items[i].Resource.ID == id {
			return &s.items[i], nil
		}
	}
	return nil, store.NewErrNotFound("exam_resource", id.String())
}
func (s *storeFake) ReserveUpload(_ context.Context, in *store.ExamResourceUploadReservation) (*store.FileUpload, error) {
	s.f.order = append(s.f.order, "reserve")
	s.reserveCalls++
	s.reservation = in
	return &store.FileUpload{Entry: in.Entry, Revision: in.Revision, Lease: in.Lease}, nil
}
func (s *storeFake) FinalizeUpload(_ context.Context, in *store.ExamResourceUploadFinalization, command *store.CommandIdempotency) (*store.ExamResourceCommandResult, error) {
	s.f.order = append(s.f.order, "finalize")
	s.finalizeCalls++
	s.finalization, s.idempotency = in, command
	if s.replayAfterFirst && s.committed != nil {
		return &store.ExamResourceCommandResult{Value: s.committed, Replayed: true}, nil
	}
	record := store.ExamResourceRecord{Resource: in.Resource, Rendition: in.Rendition, DraftRevision: in.ExpectedDraftRevision + 1}
	if s.replayAfterFirst {
		s.committed = &record
	}
	return &store.ExamResourceCommandResult{Value: &record, Replayed: s.replayed}, nil
}
func (s *storeFake) UpdateMetadata(_ context.Context, input *store.ExamResourceMetadataUpdate, command *store.CommandIdempotency) (*store.ExamResourceCommandResult, error) {
	s.updateCalls++
	s.metadataUpdate, s.idempotency = input, command
	return s.updateResult, s.updateErr
}
func (s *storeFake) Reorder(_ context.Context, _ *store.ExamResourceReorder, command *store.CommandIdempotency) (*store.ExamResourceCommandResult, error) {
	s.reorderCalls++
	s.idempotency = command
	return s.reorderResult, s.reorderErr
}

type accessFake struct {
	store.ExamAuthoringStore
	f             *fixture
	draftRevision int64
	archived      bool
}

func (a *accessFake) Access(context.Context, model.ExamID, model.UserID) (*store.ExamAccessSnapshot, error) {
	a.f.order = append(a.f.order, "access")
	exam, _ := model.NewExam(a.f.examID, a.f.unitID, a.f.userID, time.Now().UTC())
	if a.archived {
		exam.ArchivedAt = model.OptionalTimeFrom(exam.UpdatedAt)
	}
	return &store.ExamAccessSnapshot{Exam: exam, ActorIsManager: true}, nil
}

func (a *accessFake) Get(context.Context, model.ExamID, model.UserID) (*store.ExamAuthoringSnapshot, error) {
	at := time.Now().UTC()
	exam, _ := model.NewExam(a.f.examID, a.f.unitID, a.f.userID, at)
	if a.archived {
		exam.ArchivedAt = model.OptionalTimeFrom(at)
	}
	draft, _ := model.NewExamDraft(a.f.examID, "Exam", "", model.DefaultExamPolicySet(), at)
	draft.Revision = a.draftRevision
	return &store.ExamAuthoringSnapshot{Exam: exam, Draft: draft, ActorIsManager: true}, nil
}
func (s *storeFake) Remove(_ context.Context, input *store.ExamResourceRemoval, command *store.CommandIdempotency) (*store.ExamResourceCommandResult, error) {
	s.idempotency = command
	record := testRecord(input.ExamID, input.ResourceID, model.NewFileEntryID(), model.NewFileRevisionID(), input.ExpectedDraftRevision+1)
	return &store.ExamResourceCommandResult{Value: &record, DraftRevision: input.ExpectedDraftRevision + 1}, nil
}

type membershipFake struct {
	store.AcademicUnitMemberStore
	f     *fixture
	items []*model.AcademicUnitMember
	err   error
}

func (m *membershipFake) ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.AcademicUnitMember, error) {
	m.f.order = append(m.f.order, "membership")
	if m.items == nil {
		return []*model.AcademicUnitMember{{AcademicUnitID: m.f.unitID, UserID: m.f.userID}}, m.err
	}
	return m.items, m.err
}

type authFake struct {
	f      *fixture
	action model.Action
}

func (a *authFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, _ model.Resource) error {
	a.f.order = append(a.f.order, "authorize")
	a.action = action
	return nil
}

type auditFake struct {
	f     *fixture
	began bool
}

func (a *auditFake) Begin(context.Context, access.Mutation) (model.AuditEventID, error) {
	a.f.order = append(a.f.order, "audit.begin")
	a.began = true
	return model.NewAuditEventID(), nil
}
func (a *auditFake) Fail(context.Context, model.AuditEventID, string) error {
	a.f.order = append(a.f.order, "audit.fail")
	return nil
}

// draftUpdated is the call a committed Exam Resource change publishes: the
// Draft update of its Exam at the committed revision.
func draftUpdated(t *testing.T, examID model.ExamID, draftRevision int64) realtimetest.Call {
	t.Helper()
	event, err := realtime.NewExamDraftUpdatedEvent(examID, draftRevision)
	require.NoError(t, err)
	return realtimetest.Call{Method: realtimetest.Publish, Event: event}
}

type contentFake struct {
	f               *fixture
	storeCalls      int
	storeErr        error
	openedRevision  model.FileRevisionID
	openedRendition model.FileRenditionID
}

func (c *contentFake) StoreExamResource(_ context.Context, revision model.FileRevisionID, media model.ExamResourceMediaType, _ io.Reader, size int64, at time.Time) (model.FileRendition, error) {
	c.f.order = append(c.f.order, "content.store")
	c.storeCalls++
	if c.storeErr != nil {
		return model.FileRendition{}, c.storeErr
	}
	r, _ := model.NewFileRendition(model.NewFileRenditionID(), revision, "original", string(media), size, 0, 0, strings.Repeat("a", 64), at)
	return *r, nil
}
func (c *contentFake) OpenExamResource(_ context.Context, revision model.FileRevisionID, rendition model.FileRenditionID) (io.ReadCloser, error) {
	c.openedRevision, c.openedRendition = revision, rendition
	return io.NopCloser(strings.NewReader("protected")), nil
}
func (c *contentFake) RemoveExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) error {
	return nil
}
func testRecord(examID model.ExamID, resourceID model.ExamResourceID, entryID model.FileEntryID, revisionID model.FileRevisionID, draftRevision int64) store.ExamResourceRecord {
	at := time.Now().UTC()
	resource, _ := model.NewExamResource(resourceID, examID, entryID, revisionID, "Reference", "", 0, at)
	rendition, _ := model.NewFileRendition(model.NewFileRenditionID(), revisionID, "original", "text/plain", 9, 0, 0, strings.Repeat("a", 64), at)
	return store.ExamResourceRecord{Resource: resource, Rendition: rendition, DraftRevision: draftRevision}
}

func examResourceString(value string) *string { return &value }

type testInvalidContentError struct{}

func (testInvalidContentError) Error() string               { return "invalid content" }
func (testInvalidContentError) InvalidExamResourceContent() {}
