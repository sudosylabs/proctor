// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCallClonesCredentialScopes(t *testing.T) {
	t.Parallel()
	principal := testPrincipal(model.NewUserID())
	principal.CredentialType = model.CredentialPersonalAccessToken
	principal.SessionID = ""
	principal.CredentialScopes = []string{string(model.ActionExamCreate)}
	principal.AuthenticationStrength = ""
	principal.AuthenticatedAt = time.Time{}
	principal.ClientType = model.SessionClientCLI
	inv := invocation.New(principal, model.RequestMetadata{RequestID: "request"})
	principal.CredentialScopes[0] = string(model.ActionAuditView)
	got := inv.Principal()
	require.Equal(t, string(model.ActionExamCreate), got.CredentialScopes[0])
}

type authoringFixture struct {
	service     *Authoring
	inv         invocation.Invocation
	unitID      model.AcademicUnitID
	examID      model.ExamID
	userID      model.UserID
	order       *[]string
	authorizer  *authorizerFake
	memberships *membershipsFake
	users       *usersFake
	mail        *managerMailPreparerFake
	auditor     *auditorFake
	outcomes    *commandOutcomesFake
	persistence *authoringStoreFake
	publisher   *realtimetest.Publisher
	sittings    *sittingsFake
}

func newAuthoringFixture(t *testing.T) authoringFixture {
	t.Helper()
	order := []string{}
	unitID, examID, userID := model.NewAcademicUnitID(), model.NewExamID(), model.NewUserID()
	authorizer := &authorizerFake{order: &order}
	memberships := &membershipsFake{order: &order}
	users := &usersFake{order: &order, user: activeTestUser(userID)}
	mail := &managerMailPreparerFake{order: &order}
	auditor := &auditorFake{order: &order}
	outcomes := &commandOutcomesFake{}
	persistence := &authoringStoreFake{order: &order, examID: examID, unitID: unitID, actorID: userID}
	publisher := publisherIn(&order)
	sittings := &sittingsFake{order: &order}
	service, err := NewAuthoring(AuthoringDependencies{
		Persistence: persistence,
		Memberships: memberships,
		Users:       users,
		Sittings:    sittings,
		Mail:        mail,
		Authorizer:  authorizer,
		Auditor:     auditor,
		Outcomes:    outcomes,
		Publisher:   publisher,
		Now: func() time.Time {
			return time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
		},
		NewID:             func() model.ExamID { return examID },
		InstitutionOrigin: "https://institution.example",
	})
	require.NoError(t, err)
	return authoringFixture{service: service, inv: invocation.New(testPrincipal(userID), model.RequestMetadata{}), unitID: unitID, examID: examID, userID: userID, order: &order, authorizer: authorizer, memberships: memberships, users: users, mail: mail, auditor: auditor, outcomes: outcomes, persistence: persistence, publisher: publisher, sittings: sittings}
}

type commandOutcomesFake struct {
	found bool
	err   error
	calls int
}

func (f *commandOutcomesFake) Has(context.Context, *store.CommandIdempotency) (bool, error) {
	f.calls++
	return f.found, f.err
}

func testPrincipal(userID model.UserID) model.Principal {
	now := time.Now().UTC()
	return model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: now}
}

type authorizerFake struct {
	order          *[]string
	action         model.Action
	resource       model.Resource
	err            error
	listVisibility store.ExamListVisibility
}

func (f *authorizerFake) AuthorizeExamList(context.Context, invocation.Invocation) (store.ExamListVisibility, error) {
	*f.order = append(*f.order, "authorize.list")
	return f.listVisibility, f.err
}

func (f *authorizerFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource) error {
	*f.order = append(*f.order, "authorize")
	f.action, f.resource = action, resource
	return f.err
}

type membershipsFake struct {
	store.AcademicUnitMemberStore
	order       *[]string
	items       []*model.AcademicUnitMember
	itemsByUser map[string][]*model.AcademicUnitMember
	lookup      func(string, time.Time) ([]*model.AcademicUnitMember, error)
	err         error
}

func (f *membershipsFake) ListActiveByUser(_ context.Context, userID model.UserID, at time.Time) ([]*model.AcademicUnitMember, error) {
	*f.order = append(*f.order, "membership")
	if f.lookup != nil {
		return f.lookup(userID.String(), at)
	}
	if items, ok := f.itemsByUser[userID.String()]; ok {
		return items, f.err
	}
	return f.items, f.err
}

type usersFake struct {
	store.UserStore
	order *[]string
	user  *model.User
	users map[string]*model.User
	err   error
}

func (f *usersFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	*f.order = append(*f.order, "user.get")
	if user, ok := f.users[id.String()]; ok {
		return user, f.err
	}
	return f.user, f.err
}

type managerMailPreparerFake struct {
	order    *[]string
	requests []ManagerMailPreparation
	err      error
}

func (f *managerMailPreparerFake) PrepareManagerMail(request ManagerMailPreparation) (*store.ExamManagerMail, error) {
	*f.order = append(*f.order, "mail.prepare")
	f.requests = append(f.requests, request)
	if f.err != nil {
		return nil, f.err
	}
	occurrence := &model.MailOccurrence{ID: request.OccurrenceID, Kind: model.MailOccurrenceExamManagement,
		TemplateKey: request.TemplateKey, ActorUserID: request.Recipient.ID, CreatedAt: request.ActionAt}
	delivery := &model.MailDelivery{TargetUserID: request.Recipient.ID, TemplateKey: request.TemplateKey}
	return &store.ExamManagerMail{Occurrence: occurrence, Delivery: delivery, Job: &model.Job{}}, nil
}

type auditorFake struct {
	order      *[]string
	failedCode string
	value      map[string]any
	scopeType  model.RoleScopeType
	scopeID    string
	err        error
	failErr    error
}

func (f *auditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	*f.order = append(*f.order, "audit.begin")
	f.value = attempt.Value
	f.scopeType, f.scopeID = attempt.ScopeType, attempt.ScopeID
	return model.NewAuditEventID(), f.err
}
func (f *auditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	*f.order = append(*f.order, "audit.fail")
	f.failedCode = code
	if f.failErr != nil {
		return f.failErr
	}
	return f.err
}

type authoringStoreFake struct {
	order              *[]string
	examID             model.ExamID
	unitID             model.AcademicUnitID
	actorID            model.UserID
	actorIsManager     bool
	archived           bool
	replayed           bool
	creation           *store.ExamAuthoringCreation
	textUpdate         *store.ExamDraftTextUpdate
	focusLossUpdate    *store.ExamDraftFocusLossUpdate
	nativePolicyUpdate *store.ExamDraftNativePolicyUpdate
	archive            *store.ExamArchive
	listOptions        store.ExamListOptions
	summaries          []store.ExamSummary
	managerSummaries   []store.ExamManagerSummary
	managerListOptions store.ExamManagerListOptions
	managerMutation    *store.ExamManagerMutation
	idempotency        *store.CommandIdempotency
	err                error
	managerErr         error
}

func (f *authoringStoreFake) ListManagers(_ context.Context, options store.ExamManagerListOptions) ([]store.ExamManagerSummary, error) {
	f.managerListOptions = options
	return append([]store.ExamManagerSummary(nil), f.managerSummaries...), f.err
}
func (f *authoringStoreFake) AddManager(_ context.Context, input *store.ExamManagerMutation, command *store.CommandIdempotency) (*store.ExamManagerCommandResult, error) {
	return f.managerResult(input, command, true, false)
}
func (f *authoringStoreFake) RemoveManager(_ context.Context, input *store.ExamManagerMutation, command *store.CommandIdempotency) (*store.ExamManagerCommandResult, error) {
	return f.managerResult(input, command, false, false)
}
func (f *authoringStoreFake) TransferOwner(_ context.Context, input *store.ExamManagerMutation, command *store.CommandIdempotency) (*store.ExamManagerCommandResult, error) {
	return f.managerResult(input, command, true, true)
}
func (f *authoringStoreFake) managerResult(input *store.ExamManagerMutation, command *store.CommandIdempotency, present, transfer bool) (*store.ExamManagerCommandResult, error) {
	f.managerMutation, f.idempotency = input, command
	if f.managerErr != nil {
		return nil, f.managerErr
	}
	if f.err != nil {
		return nil, f.err
	}
	exam, _ := model.NewExam(input.ExamID, f.unitID, f.actorID, model.TimeUTC(input.ChangedAt).Add(-time.Minute))
	if transfer {
		exam.OwnerUserID = input.TargetUserID
	}
	exam.Revision = input.ExpectedRevision + 1
	manager, _ := model.NewExamManager(input.ExamID, input.TargetUserID, input.ActorUserID, model.TimeUTC(input.ChangedAt))
	if !present && manager == nil {
		return nil, errors.New("manager result")
	}
	return &store.ExamManagerCommandResult{Exam: exam, Manager: manager, Replayed: f.replayed}, nil
}

func (f *authoringStoreFake) List(_ context.Context, options store.ExamListOptions) ([]store.ExamSummary, error) {
	*f.order = append(*f.order, "store.list")
	f.listOptions = options
	return append([]store.ExamSummary(nil), f.summaries...), f.err
}
func (f *authoringStoreFake) Archive(_ context.Context, input *store.ExamArchive, command *store.CommandIdempotency) (*store.ExamArchiveCommandResult, error) {
	*f.order = append(*f.order, "store.archive")
	f.archive, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	exam, _ := model.NewExam(input.ExamID, f.unitID, f.actorID, model.TimeUTC(input.ArchivedAt).Add(-time.Minute))
	if f.replayed {
		_ = exam.Archive(model.TimeUTC(input.ArchivedAt))
		return &store.ExamArchiveCommandResult{Value: exam, Replayed: true}, nil
	}
	if !f.actorIsManager && !input.ManagerOverride {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if f.archived {
		return nil, store.NewErrConflict("exam", "exam_archived", nil)
	}
	if input.ExpectedRevision != 1 {
		return nil, store.NewErrConflict("exam", "exam_revision", nil)
	}
	_ = exam.Archive(model.TimeUTC(input.ArchivedAt))
	return &store.ExamArchiveCommandResult{Value: exam}, nil
}

func (f *authoringStoreFake) Create(_ context.Context, input *store.ExamAuthoringCreation, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.create")
	f.creation, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	return &store.ExamAuthoringCommandResult{Value: snapshotFromCreation(input, true), Replayed: f.replayed}, nil
}
func (f *authoringStoreFake) UpdateDraftText(_ context.Context, input *store.ExamDraftTextUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.update_text")
	f.textUpdate, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	snapshot, err := f.Get(context.Background(), input.ExamID, input.ActorUserID)
	*f.order = (*f.order)[:len(*f.order)-1]
	if err != nil {
		return nil, err
	}
	if f.replayed {
		return &store.ExamAuthoringCommandResult{Value: snapshot, Replayed: true}, nil
	}
	if !snapshot.ActorIsManager && !input.ManagerOverride {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if snapshot.Exam.IsArchived() {
		return nil, store.NewErrConflict("exam", "exam_archived", nil)
	}
	if snapshot.Draft.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}
	if _, err := snapshot.Draft.ApplyTextPatch(input.Title, input.InstructionsMarkdown, model.TimeUTC(input.UpdatedAt)); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: snapshot, Replayed: f.replayed}, nil
}
func (f *authoringStoreFake) UpdateDraftFocusLoss(_ context.Context, input *store.ExamDraftFocusLossUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.update_focus_loss")
	f.focusLossUpdate, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	snapshot, err := f.Get(context.Background(), input.ExamID, input.ActorUserID)
	*f.order = (*f.order)[:len(*f.order)-1]
	if err != nil {
		return nil, err
	}
	if f.replayed {
		return &store.ExamAuthoringCommandResult{Value: snapshot, Replayed: true}, nil
	}
	if !snapshot.ActorIsManager && !input.ManagerOverride {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if snapshot.Exam.IsArchived() {
		return nil, store.NewErrConflict("exam", "exam_archived", nil)
	}
	if snapshot.Draft.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}
	if _, err := snapshot.Draft.ApplyFocusLossPolicy(input.FocusLoss, model.TimeUTC(input.UpdatedAt)); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: snapshot}, nil
}
func (f *authoringStoreFake) UpdateDraftBrowserPolicy(_ context.Context, input *store.ExamDraftBrowserPolicyUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.update_browser_policy")
	f.idempotency = command
	if f.err != nil {
		return nil, f.err
	}
	snapshot, err := f.Get(context.Background(), input.ExamID, input.ActorUserID)
	*f.order = (*f.order)[:len(*f.order)-1]
	if err != nil {
		return nil, err
	}
	if f.replayed {
		return &store.ExamAuthoringCommandResult{Value: snapshot, Replayed: true}, nil
	}
	if _, err = snapshot.Draft.ApplyBrowserPolicy(input.Policy, model.TimeUTC(input.UpdatedAt)); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: snapshot}, nil
}
func (f *authoringStoreFake) Access(_ context.Context, examID model.ExamID, _ model.UserID) (*store.ExamAccessSnapshot, error) {
	*f.order = append(*f.order, "store.access")
	if f.err != nil {
		return nil, f.err
	}
	exam, _ := model.NewExam(examID, f.unitID, f.actorID, time.Now().UTC())
	if f.archived {
		exam.ArchivedAt = model.OptionalTimeFrom(time.Now().UTC())
	}
	return &store.ExamAccessSnapshot{Exam: exam, ActorIsManager: f.actorIsManager}, nil
}
func (f *authoringStoreFake) Get(_ context.Context, examID model.ExamID, _ model.UserID) (*store.ExamAuthoringSnapshot, error) {
	*f.order = append(*f.order, "store.get")
	if f.err != nil {
		return nil, f.err
	}
	at := time.Now().UTC()
	exam, _ := model.NewExam(examID, f.unitID, f.actorID, at)
	if f.archived {
		exam.ArchivedAt = model.OptionalTimeFrom(at)
	}
	draft, _ := model.NewExamDraft(examID, "Test", "", model.DefaultExamPolicySet(), at)
	return &store.ExamAuthoringSnapshot{Exam: exam, Draft: draft, OwnerUserID: f.actorID, ManagerCount: 1, ActorIsManager: f.actorIsManager}, nil
}
func (f *authoringStoreFake) Resolve(context.Context, model.ExamID) (*model.Exam, error) {
	return nil, nil
}
func snapshotFromCreation(input *store.ExamAuthoringCreation, actor bool) *store.ExamAuthoringSnapshot {
	return &store.ExamAuthoringSnapshot{Exam: input.Exam, Draft: input.Draft, OwnerUserID: input.Exam.OwnerUserID, ManagerCount: 1, ActorIsManager: actor}
}

// publisherIn returns a recording Publisher that also places the created,
// Draft-updated and archived facts in the log a test shares with its other
// fakes, as "effect.created", "effect.updated" and "effect.archived".
func publisherIn(order *[]string) *realtimetest.Publisher {
	entries := map[string]string{
		"exam_created":       "effect.created",
		"exam_draft_updated": "effect.updated",
		"exam_archived":      "effect.archived",
	}
	return &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		if entry, ok := entries[call.Event.Name]; ok && call.Method == realtimetest.Publish {
			*order = append(*order, entry)
		}
	}}
}

// draftUpdated is the call a committed Draft edit publishes.
func draftUpdated(t *testing.T, examID model.ExamID, revision int64) realtimetest.Call {
	t.Helper()
	event, err := realtime.NewExamDraftUpdatedEvent(examID, revision)
	require.NoError(t, err)
	return realtimetest.Call{Method: realtimetest.Publish, Event: event}
}

// sittingsFake answers the Sittings of an archived Exam and their candidates,
// and places the Sitting read in the shared log.
type sittingsFake struct {
	store.ExamSittingStore
	order        *[]string
	targets      []store.ExamSittingInvalidationTarget
	candidateIDs []model.UserID
}

func (f *sittingsFake) ListInvalidationTargetsByExam(
	context.Context,
	model.ExamID,
	model.ExamSittingID,
	int,
) ([]store.ExamSittingInvalidationTarget, error) {
	*f.order = append(*f.order, "store.archived_sittings")
	return f.targets, nil
}

func (f *sittingsFake) ListCandidateInvalidationTargetsBySitting(
	context.Context,
	model.ExamSittingID,
	model.UserID,
	int,
) ([]model.UserID, error) {
	return f.candidateIDs, nil
}

func (f *authoringStoreFake) UpdateDraftNativePolicy(_ context.Context, input *store.ExamDraftNativePolicyUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.update_native_policy")
	f.nativePolicyUpdate, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	snapshot, err := f.Get(context.Background(), input.ExamID, input.ActorUserID)
	*f.order = (*f.order)[:len(*f.order)-1]
	if err != nil {
		return nil, err
	}
	if f.replayed {
		return &store.ExamAuthoringCommandResult{Value: snapshot, Replayed: true}, nil
	}
	if !snapshot.ActorIsManager && !input.ManagerOverride {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if snapshot.Exam.IsArchived() {
		return nil, store.NewErrConflict("exam", "exam_archived", nil)
	}
	if snapshot.Draft.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}
	if _, err := snapshot.Draft.ApplyNativePolicy(input.NativePolicy, model.TimeUTC(input.UpdatedAt)); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: snapshot}, nil
}
