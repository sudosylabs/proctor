// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

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

type serviceFixture struct {
	service     *Service
	invocation  invocation.Invocation
	examID      model.ExamID
	persistence *fakeWorkspaceStore
	authorizer  *fakeWorkspaceAuthorizer
	auditor     *fakeWorkspaceAuditor
	publisher   *realtimetest.Publisher
	content     *fakeWorkspaceContent
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	at := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	examID := model.ExamID("eeeeeeeeeeeeeeeeeeeeeeeeee")
	userID := model.UserID("uuuuuuuuuuuuuuuuuuuuuuuuuu")
	exam, _ := model.NewExam(examID, model.AcademicUnitID("aaaaaaaaaaaaaaaaaaaaaaaaaa"), userID, at)
	persistence := &fakeWorkspaceStore{access: &store.ExamAccessSnapshot{Exam: exam, ActorIsManager: true}}
	authorizer := &fakeWorkspaceAuthorizer{}
	auditor := &fakeWorkspaceAuditor{}
	publisher := &realtimetest.Publisher{}
	content := &fakeWorkspaceContent{persistence: persistence}
	service, err := New(Dependencies{
		Persistence: persistence,
		Access:      fakeWorkspaceAccess{persistence: persistence},
		Memberships: fakeMemberships{unitID: exam.AcademicUnitID},
		Authorizer:  authorizer,
		Auditor:     auditor,
		Content:     content,
		Publisher:   publisher,
		Now:         func() time.Time { return at },
		NewEntryID:  model.NewStarterWorkspaceEntryID,
		NewObjectID: model.NewStarterWorkspaceObjectID,
		NewVersion:  model.NewWorkspaceContentVersion,
	})
	require.NoError(t, err)
	principal := model.Principal{UserID: userID, SessionID: model.SessionID("ssssssssssssssssssssssssss"),
		CredentialID: model.PrincipalCredentialID("cccccccccccccccccccccccccc"), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb,
		AuthenticatedAt: at}
	return &serviceFixture{service: service, invocation: invocation.New(principal, model.RequestMetadata{}),
		examID: examID, persistence: persistence, authorizer: authorizer, auditor: auditor, publisher: publisher, content: content}
}

type fakeWorkspaceStore struct {
	access        *store.ExamAccessSnapshot
	reservation   *store.ExamStarterWorkspaceReservation
	reservations  []model.StarterWorkspaceObjectID
	mutation      *store.ExamStarterWorkspaceMutation
	finalizeErr   error
	reclaimed     bool
	reclaimedIDs  []model.StarterWorkspaceObjectID
	committed     *store.ExamStarterWorkspaceMutationResult
	finalizeCalls int
	idempotency   *store.CommandIdempotency
}

// fakeWorkspaceAccess serves the Exam access snapshot of a fakeWorkspaceStore.
type fakeWorkspaceAccess struct {
	store.ExamAuthoringStore
	persistence *fakeWorkspaceStore
}

func (f fakeWorkspaceAccess) Access(ctx context.Context, examID model.ExamID, userID model.UserID) (*store.ExamAccessSnapshot, error) {
	return f.persistence.Access(ctx, examID, userID)
}

func (f *fakeWorkspaceStore) Access(context.Context, model.ExamID, model.UserID) (*store.ExamAccessSnapshot, error) {
	return f.access, nil
}
func (f *fakeWorkspaceStore) List(context.Context, model.ExamID) ([]store.ExamStarterWorkspaceItem, error) {
	return []store.ExamStarterWorkspaceItem{}, nil
}
func (f *fakeWorkspaceStore) GetFile(context.Context, model.ExamID, model.StarterWorkspaceEntryID) (*store.ExamStarterWorkspaceItem, error) {
	return nil, store.NewErrNotFound("entry", "")
}
func (f *fakeWorkspaceStore) ReserveObject(_ context.Context, input *store.ExamStarterWorkspaceReservation) (*model.StarterWorkspaceObject, error) {
	f.reservation = input
	f.reservations = append(f.reservations, input.Object.ID)
	return input.Object, nil
}
func (f *fakeWorkspaceStore) finish(input *store.ExamStarterWorkspaceMutation) (*store.ExamStarterWorkspaceMutationResult, error) {
	f.mutation = input
	f.finalizeCalls++
	if f.finalizeErr != nil {
		return nil, f.finalizeErr
	}
	if f.committed != nil {
		replayed := *f.committed
		replayed.Replayed = true
		return &replayed, nil
	}
	object, _ := model.NewStagedStarterWorkspaceObject(input.ObjectID, input.ExamID, input.ActorUserID, input.ChangedAt.Add(-time.Second), input.ChangedAt.Add(time.Hour))
	_ = object.MarkCurrent(input.ContentVersion, input.MediaType, input.SizeBytes, input.SHA256, input.ChangedAt)
	path := input.Path
	if path == "" {
		path = "main.go"
	}
	entry, _ := model.NewStarterWorkspaceFile(input.EntryID, input.ExamID, path, input.ObjectID, input.ChangedAt)
	result := &store.ExamStarterWorkspaceMutationResult{Entry: entry, Object: object, DraftRevision: input.ExpectedDraftRevision + 1}
	f.committed = result
	return result, nil
}
func (f *fakeWorkspaceStore) CreateDirectory(_ context.Context, input *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	f.mutation, f.idempotency = input, command
	entry, _ := model.NewStarterWorkspaceDirectory(input.EntryID, input.ExamID, input.Path, input.ChangedAt)
	return &store.ExamStarterWorkspaceMutationResult{Entry: entry, DraftRevision: input.ExpectedDraftRevision + 1}, nil
}
func (f *fakeWorkspaceStore) CreateFile(_ context.Context, input *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	f.idempotency = command
	return f.finish(input)
}
func (f *fakeWorkspaceStore) MoveEntry(_ context.Context, input *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	f.mutation, f.idempotency = input, command
	entry, _ := model.NewStarterWorkspaceDirectory(input.EntryID, input.ExamID, input.Path, input.ChangedAt)
	return &store.ExamStarterWorkspaceMutationResult{Entry: entry, DraftRevision: input.ExpectedDraftRevision + 1}, nil
}
func (f *fakeWorkspaceStore) ReplaceFile(_ context.Context, input *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	f.idempotency = command
	return f.finish(input)
}
func (f *fakeWorkspaceStore) RemoveEntry(_ context.Context, input *store.ExamStarterWorkspaceMutation, command *store.CommandIdempotency) (*store.ExamStarterWorkspaceMutationResult, error) {
	f.mutation, f.idempotency = input, command
	entry, _ := model.NewStarterWorkspaceDirectory(input.EntryID, input.ExamID, "removed", input.ChangedAt)
	return &store.ExamStarterWorkspaceMutationResult{Entry: entry, DraftRevision: input.ExpectedDraftRevision + 1}, nil
}
func (f *fakeWorkspaceStore) MarkObjectReclaimable(_ context.Context, input *store.StarterWorkspaceObjectReclaim) error {
	id := input.ObjectID
	f.reclaimed = true
	f.reclaimedIDs = append(f.reclaimedIDs, id)
	return nil
}
func (f *fakeWorkspaceStore) ClaimObjectsForCleanup(ctx context.Context, input *store.StarterWorkspaceCleanupClaim) ([]model.StarterWorkspaceObject, error) {
	return nil, nil
}
func (f *fakeWorkspaceStore) CompleteObjectCleanup(ctx context.Context, input *store.StarterWorkspaceCleanupCompletion) error {
	return nil
}
func (f *fakeWorkspaceStore) ReleaseObjectCleanup(ctx context.Context, input *store.StarterWorkspaceCleanupRelease) error {
	return nil
}

type fakeMemberships struct {
	store.AcademicUnitMemberStore
	unitID model.AcademicUnitID
	err    error
}

func (f fakeMemberships) ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.AcademicUnitMember, error) {
	return []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}, f.err
}

type fakeWorkspaceAuthorizer struct{ called bool }

func (f *fakeWorkspaceAuthorizer) Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error {
	f.called = true
	return nil
}

type fakeWorkspaceAuditor struct{ values map[string]any }

func (f *fakeWorkspaceAuditor) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	f.values = attempt.Value
	return "iiiiiiiiiiiiiiiiiiiiiiiiii", nil
}
func (f *fakeWorkspaceAuditor) Fail(context.Context, model.AuditEventID, string) error { return nil }

// starterWorkspaceChanged is the call a committed Starter Workspace mutation
// publishes.
func starterWorkspaceChanged(
	t *testing.T,
	examID model.ExamID,
	entryID model.StarterWorkspaceEntryID,
	draftRevision int64,
	operation ChangeOperation,
	changedAt time.Time,
) realtimetest.Call {
	t.Helper()
	event, err := realtime.NewExamStarterWorkspaceChangedEvent(examID, entryID, draftRevision, string(operation), changedAt)
	require.NoError(t, err)
	return realtimetest.Call{Method: realtimetest.Publish, Event: event}
}

type fakeWorkspaceContent struct {
	persistence          *fakeWorkspaceStore
	stagedBeforeFinalize bool
	stagedIDs            []model.StarterWorkspaceObjectID
	stageErr             error
}

func (f *fakeWorkspaceContent) StageStarterWorkspaceObject(_ context.Context, id model.StarterWorkspaceObjectID, body io.Reader, size int64, media string) (*model.StarterWorkspaceContent, error) {
	if f.stageErr != nil {
		return nil, f.stageErr
	}
	data, _ := io.ReadAll(body)
	f.stagedIDs = append(f.stagedIDs, id)
	f.stagedBeforeFinalize = f.persistence.mutation == nil
	return &model.StarterWorkspaceContent{MediaType: media, SizeBytes: int64(len(data)), SHA256: strings.Repeat("a", 64)}, nil
}
func (*fakeWorkspaceContent) OpenStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID) (io.ReadCloser, error) {
	return nil, nil
}

func faultCode(err error) string {
	if fault, ok := apperror.As(err); ok {
		return fault.Code()
	}
	return ""
}
