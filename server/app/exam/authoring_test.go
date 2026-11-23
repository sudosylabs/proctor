// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
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
	access      *accesstest.Fixture
	memberships *membershipsFake
	users       *usersFake
	mail        *managerMailPreparerFake
	persistence *authoringStoreFake
	publisher   *realtimetest.Publisher
	sittings    *sittingsFake
	submissions *submissionsFake
}

// examActions are every action the Exam use cases ask about. The fixture's
// caller holds them all in the Exam's Academic Unit unless a test grants
// fewer.
var examActions = []model.Action{
	model.ActionExamCreate, model.ActionExamCreateOverride,
	model.ActionExamView, model.ActionExamViewOverride,
	model.ActionExamManage, model.ActionExamManageOverride,
	model.ActionExamPublish, model.ActionExamPublishOverride,
	model.ActionExamRecordsComplete, model.ActionExamRecordsCompleteOverride,
	model.ActionExamRecordsHold, model.ActionExamRecordsHoldOverride,
	model.ActionExamRecordsExport, model.ActionExamRecordsExportOverride,
	model.ActionExamSittingView, model.ActionExamSittingViewOverride,
	model.ActionSubmissionView, model.ActionSubmissionViewOverride,
	model.ActionExamAttemptBrowserActivityView, model.ActionRetentionHoldRelease,
}

// authoringNow is the clock of the Authoring service and of the authorizer.
func authoringNow() time.Time { return time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC) }

// newAuthoringFixture builds Authoring over the real authorizer and auditor.
// The caller holds actions in the Exam's Academic Unit, or every examAction
// when none are named.
func newAuthoringFixture(t *testing.T, actions ...model.Action) authoringFixture {
	t.Helper()
	order := []string{}
	unitID, examID, userID := model.NewAcademicUnitID(), model.NewExamID(), model.NewUserID()
	f := authoringFixture{
		unitID: unitID, examID: examID, userID: userID, order: &order,
		inv:         invocation.New(testPrincipal(userID), model.RequestMetadata{}),
		memberships: &membershipsFake{order: &order},
		users:       &usersFake{order: &order, user: activeTestUser(userID)},
		mail:        &managerMailPreparerFake{order: &order},
		persistence: &authoringStoreFake{order: &order, examID: examID, unitID: unitID, actorID: userID},
		publisher:   publisherIn(&order),
		sittings:    &sittingsFake{order: &order, examID: examID},
		submissions: &submissionsFake{examID: examID, unitID: unitID},
	}
	if len(actions) == 0 {
		actions = examActions
	}
	f.access = f.accessGranting(t, actions...)
	service, err := NewAuthoring(AuthoringDependencies{
		Persistence:       f.persistence,
		Memberships:       f.memberships,
		Users:             f.users,
		Sittings:          f.sittings,
		Mail:              f.mail,
		Authorizer:        f.access.Authorizer,
		Auditor:           f.access.Auditor,
		Publisher:         f.publisher,
		Now:               authoringNow,
		NewID:             func() model.ExamID { return examID },
		InstitutionOrigin: "https://institution.example",
	})
	require.NoError(t, err)
	f.service = service
	return f
}

// accessGranting builds the real authorizer and auditor over the fixture's
// Exam, Sittings and Submissions, with the caller holding actions in the
// Exam's Academic Unit. What they do joins the fixture's order log as
// "grants", "decision:<action>:<status>", "audit-begin:<action>" and
// "audit-<status>".
func (f authoringFixture) accessGranting(t *testing.T, actions ...model.Action) *accesstest.Fixture {
	t.Helper()
	return accesstest.New(t, accesstest.Options{
		Grants: []accesstest.Grant{{
			UserID: f.userID, Actions: actions,
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: f.unitID.String(),
		}},
		Exams:       f.persistence,
		Sittings:    f.sittings,
		Submissions: f.submissions,
		Now:         authoringNow,
		Observe:     func(event string) { *f.order = append(*f.order, event) },
	})
}

// authorizerWithout is the real authorizer after the caller lost the
// withdrawn actions, for a use case that must recheck its authority.
func (f authoringFixture) authorizerWithout(t *testing.T, withdrawn ...model.Action) *access.Authorizer {
	t.Helper()
	return f.accessGranting(t, examActionsWithout(withdrawn...)...).Authorizer
}

// examActionsWithout is examActions less the withheld actions.
func examActionsWithout(withheld ...model.Action) []model.Action {
	return slices.DeleteFunc(slices.Clone(examActions), func(action model.Action) bool {
		return slices.Contains(withheld, action)
	})
}

// decisions describes each recorded authorization decision as
// "<action> <resource type>:<resource id> <status>".
func (f authoringFixture) decisions() []string {
	var described []string
	for _, event := range f.access.Audits.Decisions() {
		described = append(described, event.Action+" "+string(event.Resource.Type)+":"+event.Resource.ID+" "+string(event.Status))
	}
	return described
}

// decided describes one decision the way decisions does.
func decided(action model.Action, resource model.Resource, status model.AuditStatus) string {
	return string(action) + " " + string(resource.Type) + ":" + resource.ID + " " + string(status)
}

// examResource is the fixture's Exam as an authorization resource.
func (f authoringFixture) examResource() model.Resource {
	return model.Resource{Type: model.ResourceExam, ID: f.examID.String()}
}

// onlyAttempt returns the one mutation attempt the auditor recorded.
func (f authoringFixture) onlyAttempt(t *testing.T) *model.AuditEvent {
	t.Helper()
	attempts := f.access.Audits.Attempts()
	require.Len(t, attempts, 1, "mutation attempts")
	return attempts[0]
}

// attemptValue decodes the value the one recorded attempt stores, with
// integral numbers as int64 the way the use case built them.
func (f authoringFixture) attemptValue(t *testing.T) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(f.onlyAttempt(t).Parameters))
	decoder.UseNumber()
	var parameters struct {
		Value map[string]any `json:"value"`
	}
	require.NoError(t, decoder.Decode(&parameters))
	for key, field := range parameters.Value {
		if number, ok := field.(json.Number); ok {
			if integral, err := number.Int64(); err == nil {
				parameters.Value[key] = integral
			}
		}
	}
	return parameters.Value
}

// failedCode is the error code the one recorded attempt failed with, or empty
// when it did not fail.
func (f authoringFixture) failedCode(t *testing.T) string {
	t.Helper()
	attempt := f.onlyAttempt(t)
	if attempt.Status != model.AuditStatusFail {
		return ""
	}
	return attempt.ErrorCode
}

func testPrincipal(userID model.UserID) model.Principal {
	now := time.Now().UTC()
	return model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: now}
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
	executionUpdate    *store.ExamDraftExecutionUpdate
	assistantUpdate    *store.ExamDraftAssistantUpdate
	nativePolicyUpdate *store.ExamDraftNativePolicyUpdate
	draftRevision      int64
	archive            *store.ExamArchive
	listOptions        store.ExamListOptions
	countOptions       *store.ExamListOptions
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

// Count counts the listed summaries; it is a read, so it joins no order log.
func (f *authoringStoreFake) Count(_ context.Context, options store.ExamListOptions, maximum int) (int, error) {
	f.countOptions = &options
	return min(len(f.summaries), maximum), f.err
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
func (f *authoringStoreFake) UpdateDraftExecution(_ context.Context, input *store.ExamDraftExecutionUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.update_execution")
	f.executionUpdate, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	snapshot, err := f.Get(context.Background(), input.ExamID, input.ActorUserID)
	*f.order = (*f.order)[:len(*f.order)-1]
	if err != nil {
		return nil, err
	}
	if snapshot.Draft.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}
	if _, err := snapshot.Draft.ApplyExecutionPolicy(input.Execution, model.TimeUTC(input.UpdatedAt)); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: snapshot}, nil
}

func (f *authoringStoreFake) UpdateDraftAssistant(ctx context.Context, input *store.ExamDraftAssistantUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.update_assistant")
	f.assistantUpdate, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	snapshot, err := f.Get(ctx, input.ExamID, input.ActorUserID)
	*f.order = (*f.order)[:len(*f.order)-1]
	if err != nil {
		return nil, err
	}
	if snapshot.Draft.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}
	if _, err := snapshot.Draft.ApplyAssistantPolicy(input.Assistant, model.TimeUTC(input.UpdatedAt)); err != nil {
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
	draft, _ := model.NewExamDraft(examID, "Test", "", model.DefaultExamPolicySet(), f.actorID, at)
	if f.draftRevision > 0 {
		draft.Revision = f.draftRevision
	}
	return &store.ExamAuthoringSnapshot{Exam: exam, Draft: draft, OwnerUserID: f.actorID, ManagerCount: 1, ActorIsManager: f.actorIsManager}, nil
}

// Resolve gives the authorizer the Exam's owning Academic Unit.
func (f *authoringStoreFake) Resolve(_ context.Context, examID model.ExamID) (*model.Exam, error) {
	if examID != f.examID {
		return nil, store.NewErrNotFound("exam", examID.String())
	}
	return model.NewExam(examID, f.unitID, f.actorID, time.Now().UTC())
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
// and places the Sitting read in the shared log. Every Sitting resolves to the
// fixture's Exam.
type sittingsFake struct {
	store.ExamSittingStore
	order        *[]string
	examID       model.ExamID
	targets      []store.ExamSittingInvalidationTarget
	candidateIDs []model.UserID
}

func (f *sittingsFake) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	return &store.ExamSittingSnapshot{Sitting: &model.ExamSitting{ID: id, ExamID: f.examID}}, nil
}

// submissionsFake resolves every Submission to a Sitting of the fixture's
// Exam in its Academic Unit.
type submissionsFake struct {
	store.ExamSubmissionStore
	examID model.ExamID
	unitID model.AcademicUnitID
}

func (f *submissionsFake) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return &store.ExamSubmissionAuthorization{
		SubmissionID: id, ExamID: f.examID, SittingID: model.NewExamSittingID(),
		AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID,
	}, nil
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
