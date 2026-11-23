// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// examFixture is the real Exam authoring, publication, records and export
// services the Exam handlers call, over in-memory stores holding one Exam in
// one Academic Unit. The caller owns and manages that Exam and is a current
// member of its unit, so every use case takes its ordinary action. A handler
// test asserts what the request turned into at the Store.
type examFixture struct {
	caller model.Principal
	unitID model.AcademicUnitID
	// examID is the seeded Exam. Sittings and Submissions with no recorded
	// Exam belong to it.
	examID model.ExamID

	access      *accesstest.Fixture
	exams       *examAuthoringRecords
	members     *examUnitMembers
	users       *directoryUserStore
	sittings    *examSittingRecords
	submissions *examSubmissionRecords
	revisions   *examRevisionRecords
	ledger      *examRecordsLedger
	exportLog   *examExportRecords
	archives    *examExportArchives
	mail        *examManagerMail
	publisher   *realtimetest.Publisher
	people      *people.Resolver

	authoring   *exam.Authoring
	publication *exam.Publication
	records     *exam.Records
	exports     *exam.Exports
}

// examActions are every action the Exam handlers' use cases ask about.
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

// newExamFixture builds the fixture for caller, who holds actions in the
// Exam's Academic Unit, or every examAction when none are named.
func newExamFixture(t *testing.T, caller model.Principal, actions ...model.Action) *examFixture {
	t.Helper()
	at := model.NowUTC()
	f := &examFixture{
		caller: caller, unitID: model.NewAcademicUnitID(), examID: model.NewExamID(),
		exams:       &examAuthoringRecords{byID: map[model.ExamID]*examAuthoringEntry{}},
		members:     &examUnitMembers{units: map[model.UserID][]model.AcademicUnitID{}},
		users:       &directoryUserStore{byID: map[model.UserID]*model.User{}},
		submissions: &examSubmissionRecords{candidateID: model.NewUserID()},
		revisions:   &examRevisionRecords{},
		archives:    &examExportArchives{},
		mail:        &examManagerMail{},
		publisher:   &realtimetest.Publisher{},
	}
	f.sittings = &examSittingRecords{fixture: f}
	f.submissions.fixture = f
	f.ledger = &examRecordsLedger{fixture: f}
	f.exportLog = &examExportRecords{fixture: f}

	examRecord, err := model.NewExam(f.examID, f.unitID, caller.UserID, at)
	require.NoError(t, err)
	draft, err := model.NewExamDraft(f.examID, "Systems", "Use **Go**.", model.DefaultExamPolicySet(), caller.UserID, at)
	require.NoError(t, err)
	f.exams.byID[f.examID] = &examAuthoringEntry{exam: examRecord, draft: draft, managers: []model.UserID{caller.UserID}}
	f.members.join(caller.UserID, f.unitID)
	f.users.add(activeExamUser(caller.UserID, "caller"))

	if len(actions) == 0 {
		actions = examActions
	}
	f.access = accesstest.New(t, accesstest.Options{
		Grants:      []accesstest.Grant{inAcademicUnit(caller.UserID, f.unitID, actions...)},
		Users:       f.users,
		Exams:       f.exams,
		Sittings:    f.sittings,
		Submissions: f.submissions,
	})

	f.authoring, err = exam.NewAuthoring(exam.AuthoringDependencies{
		Persistence: f.exams, Memberships: f.members, Users: f.users, Sittings: f.sittings, Mail: f.mail,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor, Publisher: f.publisher,
		Now: time.Now, NewID: model.NewExamID, InstitutionOrigin: "https://institution.example",
	})
	require.NoError(t, err)
	f.publication, err = exam.NewPublication(exam.PublicationDependencies{
		Persistence: f.revisions, Access: f.exams, Memberships: f.members,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor, Publisher: f.publisher,
		Now: time.Now, NewID: model.NewExamRevisionID, InstitutionOrigin: "https://institution.example",
	})
	require.NoError(t, err)
	f.records, err = exam.NewRecords(exam.RecordsDependencies{
		Persistence: f.ledger, Access: f.exams, Memberships: f.members,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor,
		RecentTTL: 5 * time.Minute, Now: time.Now, NewHoldID: model.NewRetentionHoldID,
	})
	require.NoError(t, err)
	f.exports, err = exam.NewExports(exam.ExportsDependencies{
		Persistence: f.exportLog, Access: f.exams, Memberships: f.members,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor, Content: f.archives,
		Now: time.Now, NewID: model.NewExamExportID, NewJobID: model.NewJobID,
	})
	require.NoError(t, err)
	f.people = testPeople(f.users, f.access.Authorizer)
	return f
}

// seeded is the fixture's Exam as the Store holds it now.
func (f *examFixture) seeded() *examAuthoringEntry { return f.exams.byID[f.examID] }

// sittingScope is a records scope of one Sitting of the fixture's Exam.
func (f *examFixture) sittingScope() model.RetentionHoldScope {
	return model.RetentionHoldScope{ExamID: f.examID, SittingID: model.NewExamSittingID()}
}

// activeExamUser is an active User the Exam services may name.
func activeExamUser(id model.UserID, username string) *model.User {
	user := &model.User{Username: username, Email: username + "@example.edu", DisplayName: username}
	user.PrepareCreate(id, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	return user
}

// testExamHTTPPrincipal is a signed-in web caller; the real authorizer
// refuses a zero Principal.
func testExamHTTPPrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now().UTC()}
}

// examAuthoringEntry is one Exam with its Draft and Exam Managers.
type examAuthoringEntry struct {
	exam     *model.Exam
	draft    *model.ExamDraft
	managers []model.UserID
}

func (e *examAuthoringEntry) snapshot(actorID model.UserID) *store.ExamAuthoringSnapshot {
	examRecord, draft := *e.exam, *e.draft
	return &store.ExamAuthoringSnapshot{
		Exam: &examRecord, Draft: &draft, OwnerUserID: e.exam.OwnerUserID,
		ManagerCount: len(e.managers), ActorIsManager: slices.Contains(e.managers, actorID),
	}
}

// examAuthoringRecords holds Exams by identity and applies Draft edits,
// archival and Manager changes under the same manager, archive and revision
// fences as the SQL Store. It records every request it is asked.
type examAuthoringRecords struct {
	store.ExamAuthoringStore
	byID map[model.ExamID]*examAuthoringEntry
	// listed and managersListed are what List and ListManagers answer.
	listed         []store.ExamSummary
	managersListed []store.ExamManagerSummary

	accesses            []model.ExamID
	gets                []model.ExamID
	creations           []*store.ExamAuthoringCreation
	textUpdates         []*store.ExamDraftTextUpdate
	focusLossUpdates    []*store.ExamDraftFocusLossUpdate
	assistantUpdates    []*store.ExamDraftAssistantUpdate
	nativePolicyUpdates []*store.ExamDraftNativePolicyUpdate
	lists               []store.ExamListOptions
	archives            []*store.ExamArchive
	managerLists        []store.ExamManagerListOptions
	managerChanges      []*store.ExamManagerMutation
	commands            []*store.CommandIdempotency
}

// writes is how many mutations reached the Store.
func (s *examAuthoringRecords) writes() int {
	return len(s.creations) + len(s.textUpdates) + len(s.focusLossUpdates) + len(s.nativePolicyUpdates) +
		len(s.archives) + len(s.managerChanges)
}

// lastCommand is the idempotency of the most recent mutation.
func (s *examAuthoringRecords) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no mutation reached the Exam Store")
	return s.commands[len(s.commands)-1]
}

func (s *examAuthoringRecords) entry(id model.ExamID) (*examAuthoringEntry, error) {
	entry, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("exam", id.String())
	}
	return entry, nil
}

// editable applies the fences every Draft edit shares.
func (s *examAuthoringRecords) editable(id model.ExamID, actor model.UserID, override bool, expected int64) (*examAuthoringEntry, error) {
	entry, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	if !override && !slices.Contains(entry.managers, actor) {
		return nil, store.NewErrNotFound("exam_manager", actor.String())
	}
	if entry.exam.IsArchived() {
		return nil, store.NewErrConflict("exam", "exam_archived", nil)
	}
	if entry.draft.Revision != expected {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}
	return entry, nil
}

func (s *examAuthoringRecords) Access(_ context.Context, id model.ExamID, actorID model.UserID) (*store.ExamAccessSnapshot, error) {
	s.accesses = append(s.accesses, id)
	entry, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	snapshot := entry.snapshot(actorID)
	return &store.ExamAccessSnapshot{Exam: snapshot.Exam, ActorIsManager: snapshot.ActorIsManager}, nil
}

func (s *examAuthoringRecords) Get(_ context.Context, id model.ExamID, actorID model.UserID) (*store.ExamAuthoringSnapshot, error) {
	s.gets = append(s.gets, id)
	entry, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	return entry.snapshot(actorID), nil
}

// Resolve gives the authorizer an Exam's owning Academic Unit.
func (s *examAuthoringRecords) Resolve(_ context.Context, id model.ExamID) (*model.Exam, error) {
	entry, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	examRecord := *entry.exam
	return &examRecord, nil
}

func (s *examAuthoringRecords) Create(_ context.Context, input *store.ExamAuthoringCreation, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	s.creations, s.commands = append(s.creations, input), append(s.commands, command)
	examRecord, draft := *input.Exam, *input.Draft
	entry := &examAuthoringEntry{exam: &examRecord, draft: &draft, managers: []model.UserID{input.Manager.UserID}}
	s.byID[input.Exam.ID] = entry
	return &store.ExamAuthoringCommandResult{Value: entry.snapshot(input.Manager.UserID)}, nil
}

func (s *examAuthoringRecords) UpdateDraftText(_ context.Context, input *store.ExamDraftTextUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	s.textUpdates, s.commands = append(s.textUpdates, input), append(s.commands, command)
	entry, err := s.editable(input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if _, err = entry.draft.ApplyTextPatch(input.Title, input.InstructionsMarkdown, input.UpdatedAt); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: entry.snapshot(input.ActorUserID)}, nil
}

func (s *examAuthoringRecords) UpdateDraftFocusLoss(_ context.Context, input *store.ExamDraftFocusLossUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	s.focusLossUpdates, s.commands = append(s.focusLossUpdates, input), append(s.commands, command)
	entry, err := s.editable(input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if _, err = entry.draft.ApplyFocusLossPolicy(input.FocusLoss, input.UpdatedAt); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: entry.snapshot(input.ActorUserID)}, nil
}

func (s *examAuthoringRecords) UpdateDraftNativePolicy(_ context.Context, input *store.ExamDraftNativePolicyUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	s.nativePolicyUpdates, s.commands = append(s.nativePolicyUpdates, input), append(s.commands, command)
	entry, err := s.editable(input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if _, err = entry.draft.ApplyNativePolicy(input.NativePolicy, input.UpdatedAt); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: entry.snapshot(input.ActorUserID)}, nil
}

func (s *examAuthoringRecords) UpdateDraftAssistant(_ context.Context, input *store.ExamDraftAssistantUpdate, command *store.CommandIdempotency) (*store.ExamAuthoringCommandResult, error) {
	s.assistantUpdates, s.commands = append(s.assistantUpdates, input), append(s.commands, command)
	entry, err := s.editable(input.ExamID, input.ActorUserID, input.ManagerOverride, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if _, err = entry.draft.ApplyAssistantPolicy(input.Assistant, input.UpdatedAt); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: entry.snapshot(input.ActorUserID)}, nil
}

func (s *examAuthoringRecords) List(_ context.Context, options store.ExamListOptions) ([]store.ExamSummary, error) {
	s.lists = append(s.lists, options)
	return slices.Clone(s.listed), nil
}

func (s *examAuthoringRecords) Archive(_ context.Context, input *store.ExamArchive, command *store.CommandIdempotency) (*store.ExamArchiveCommandResult, error) {
	s.archives, s.commands = append(s.archives, input), append(s.commands, command)
	entry, err := s.entry(input.ExamID)
	if err != nil {
		return nil, err
	}
	if !input.ManagerOverride && !slices.Contains(entry.managers, input.ActorUserID) {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if entry.exam.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam", "exam_revision", nil)
	}
	if err = entry.exam.Archive(input.ArchivedAt); err != nil {
		return nil, store.NewErrConflict("exam", "exam_archived", err)
	}
	examRecord := *entry.exam
	return &store.ExamArchiveCommandResult{Value: &examRecord}, nil
}

func (s *examAuthoringRecords) ListManagers(_ context.Context, options store.ExamManagerListOptions) ([]store.ExamManagerSummary, error) {
	s.managerLists = append(s.managerLists, options)
	return slices.Clone(s.managersListed), nil
}

func (s *examAuthoringRecords) AddManager(_ context.Context, input *store.ExamManagerMutation, command *store.CommandIdempotency) (*store.ExamManagerCommandResult, error) {
	return s.changeManager(input, command, func(entry *examAuthoringEntry) {
		entry.managers = append(entry.managers, input.TargetUserID)
	})
}

func (s *examAuthoringRecords) RemoveManager(_ context.Context, input *store.ExamManagerMutation, command *store.CommandIdempotency) (*store.ExamManagerCommandResult, error) {
	return s.changeManager(input, command, func(entry *examAuthoringEntry) {
		entry.managers = slices.DeleteFunc(entry.managers, func(id model.UserID) bool { return id == input.TargetUserID })
	})
}

func (s *examAuthoringRecords) TransferOwner(_ context.Context, input *store.ExamManagerMutation, command *store.CommandIdempotency) (*store.ExamManagerCommandResult, error) {
	return s.changeManager(input, command, func(entry *examAuthoringEntry) {
		entry.exam.OwnerUserID = input.TargetUserID
	})
}

func (s *examAuthoringRecords) changeManager(input *store.ExamManagerMutation, command *store.CommandIdempotency, change func(*examAuthoringEntry)) (*store.ExamManagerCommandResult, error) {
	s.managerChanges, s.commands = append(s.managerChanges, input), append(s.commands, command)
	entry, err := s.entry(input.ExamID)
	if err != nil {
		return nil, err
	}
	if !input.ManagerOverride && !slices.Contains(entry.managers, input.ActorUserID) {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if entry.exam.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam", "exam_revision", nil)
	}
	change(entry)
	entry.exam.Revision++
	manager, err := model.NewExamManager(input.ExamID, input.TargetUserID, input.ActorUserID, input.ChangedAt)
	if err != nil {
		return nil, err
	}
	examRecord := *entry.exam
	return &store.ExamManagerCommandResult{Exam: &examRecord, Manager: manager}, nil
}

// examUnitMembers holds each User's current Academic Unit memberships.
type examUnitMembers struct {
	store.AcademicUnitMemberStore
	units map[model.UserID][]model.AcademicUnitID
}

func (s *examUnitMembers) join(userID model.UserID, unitID model.AcademicUnitID) {
	s.units[userID] = append(s.units[userID], unitID)
}

func (s *examUnitMembers) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.AcademicUnitMember, error) {
	members := make([]*model.AcademicUnitMember, 0, len(s.units[userID]))
	for _, unitID := range s.units[userID] {
		members = append(members, &model.AcademicUnitMember{AcademicUnitID: unitID, UserID: userID})
	}
	return members, nil
}

// examSittingRecords resolves every Sitting to the fixture's Exam, and an
// archived Exam has no Sittings to invalidate.
type examSittingRecords struct {
	store.ExamSittingStore
	fixture *examFixture
}

func (s *examSittingRecords) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	return &store.ExamSittingSnapshot{Sitting: &model.ExamSitting{ID: id, ExamID: s.fixture.examID}}, nil
}

func (s *examSittingRecords) ListInvalidationTargetsByExam(context.Context, model.ExamID, model.ExamSittingID, int) ([]store.ExamSittingInvalidationTarget, error) {
	return nil, nil
}

// examSubmissionRecords resolves every Submission to a Sitting of the
// fixture's Exam, submitted by a candidate other than the caller.
type examSubmissionRecords struct {
	store.ExamSubmissionStore
	fixture     *examFixture
	candidateID model.UserID
}

func (s *examSubmissionRecords) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return &store.ExamSubmissionAuthorization{
		SubmissionID: id, ExamID: s.fixture.examID, SittingID: model.NewExamSittingID(),
		AttemptID: model.NewExamAttemptID(), CandidateUserID: s.candidateID, AcademicUnitID: s.fixture.unitID,
	}, nil
}

// examRevisionRecords publishes summary and answers listed, recording each
// request.
type examRevisionRecords struct {
	store.ExamRevisionStore
	summary store.ExamRevisionSummary
	listed  []store.ExamRevisionSummary

	publications []*store.ExamRevisionPublication
	commands     []*store.CommandIdempotency
	gets         []model.ExamRevisionID
	lists        []store.ExamRevisionListOptions
}

func (s *examRevisionRecords) Publish(_ context.Context, input *store.ExamRevisionPublication, command *store.CommandIdempotency) (*store.ExamRevisionPublicationResult, error) {
	s.publications, s.commands = append(s.publications, input), append(s.commands, command)
	summary := s.summary
	return &store.ExamRevisionPublicationResult{Revision: &summary}, nil
}

func (s *examRevisionRecords) GetSummary(_ context.Context, examID model.ExamID, id model.ExamRevisionID) (*store.ExamRevisionSummary, error) {
	s.gets = append(s.gets, id)
	if s.summary.ExamID != examID || s.summary.ID != id {
		return nil, store.NewErrNotFound("exam_revision", id.String())
	}
	summary := s.summary
	return &summary, nil
}

func (s *examRevisionRecords) List(_ context.Context, options store.ExamRevisionListOptions) ([]store.ExamRevisionSummary, error) {
	s.lists = append(s.lists, options)
	return slices.Clone(s.listed), nil
}

// examRecordsLedger resolves records scopes to the fixture's Exam and keeps
// the completions and Retention Holds a test asks for, recording each
// request.
type examRecordsLedger struct {
	store.ExamRecordsStore
	fixture *examFixture
	// holds is what ListHolds answers; more says another page follows.
	holds []model.RetentionHold
	more  bool
	// workRetired and integrityRetired are what a created hold reports.
	workRetired, integrityRetired int64

	resolves    []model.RetentionHoldScope
	completions []*store.ExamRecordsCompletion
	creations   []*store.ExamRecordsHoldCreation
	holdLists   []store.ExamRecordsHoldListOptions
	releases    []*store.ExamRecordsHoldRelease
	commands    []*store.CommandIdempotency
}

// requests is how many requests reached the records Store.
func (s *examRecordsLedger) requests() int {
	return len(s.resolves) + len(s.completions) + len(s.creations) + len(s.holdLists) + len(s.releases)
}

func (s *examRecordsLedger) Resolve(_ context.Context, scope model.RetentionHoldScope) (*store.ExamRecordsScope, error) {
	s.resolves = append(s.resolves, scope)
	if scope.ExamID != s.fixture.examID {
		return nil, store.NewErrNotFound("exam", scope.ExamID.String())
	}
	return &store.ExamRecordsScope{
		Scope: scope, InstitutionID: s.fixture.access.Institution.ID, AcademicUnitID: s.fixture.unitID,
		CandidateUserID: s.fixture.submissions.candidateID, AttemptID: model.NewExamAttemptID(),
	}, nil
}

func (s *examRecordsLedger) CompleteRecords(_ context.Context, input *store.ExamRecordsCompletion, command *store.CommandIdempotency) (*store.ExamRecordsCompletionResult, error) {
	s.completions, s.commands = append(s.completions, input), append(s.commands, command)
	completion := model.NewExamSittingRecordsCompletion(input.Scope.SittingID)
	if _, err := completion.Complete(input.ExpectedRevision, input.AcknowledgedEvidenceRevision, input.Principal.UserID, input.AuditAt); err != nil {
		return nil, err
	}
	return &store.ExamRecordsCompletionResult{Completion: completion}, nil
}

func (s *examRecordsLedger) CreateHold(_ context.Context, input *store.ExamRecordsHoldCreation, command *store.CommandIdempotency) (*store.ExamRecordsHoldResult, error) {
	s.creations, s.commands = append(s.creations, input), append(s.commands, command)
	return &store.ExamRecordsHoldResult{Hold: &model.RetentionHold{
		ID: input.HoldID, Scope: input.Scope, Revision: 1, CreatedAt: input.AuditAt, CreatedByUserID: input.Principal.UserID,
		ReasonCode: input.ReasonCode, PrivateReason: input.PrivateReason,
		WorkRetiredSubmissionCount: s.workRetired, IntegrityRetiredSubmissionCount: s.integrityRetired,
	}}, nil
}

func (s *examRecordsLedger) ListHolds(_ context.Context, options store.ExamRecordsHoldListOptions) (*store.ExamRecordsHoldPage, error) {
	s.holdLists = append(s.holdLists, options)
	return &store.ExamRecordsHoldPage{Holds: slices.Clone(s.holds), HasMore: s.more}, nil
}

func (s *examRecordsLedger) ReleaseHold(_ context.Context, input *store.ExamRecordsHoldRelease, command *store.CommandIdempotency) (*store.ExamRecordsHoldResult, error) {
	s.releases, s.commands = append(s.releases, input), append(s.commands, command)
	return nil, errors.New("the fixture releases no Retention Hold")
}

// examExportRecords keeps the Examination Export a test created or placed,
// selecting one Submission of the requested scope as its source, and records
// each request.
type examExportRecords struct {
	store.ExamExportStore
	fixture *examFixture
	// current is the export Get answers, with artifact as its archive.
	current  *model.ExamExport
	artifact model.JobAttemptID

	creationScopes []model.RetentionHoldScope
	creations      []*store.ExamExportCreation
	commands       []*store.CommandIdempotency
	gets           []*store.ExamExportAccess
}

// requests is how many requests reached the export Store.
func (s *examExportRecords) requests() int {
	return len(s.creationScopes) + len(s.creations) + len(s.gets)
}

// sources is the one Submission an export of scope reads.
func (s *examExportRecords) sources(scope model.RetentionHoldScope) []store.ExamSubmissionAuthorization {
	submissionID := scope.SubmissionID
	if submissionID.IsZero() {
		submissionID = model.NewSubmissionID()
	}
	return []store.ExamSubmissionAuthorization{{
		SubmissionID: submissionID, ExamID: scope.ExamID, SittingID: scope.SittingID, AttemptID: model.NewExamAttemptID(),
		CandidateUserID: s.fixture.submissions.candidateID, AcademicUnitID: s.fixture.unitID,
	}}
}

func (s *examExportRecords) ListCreationScope(_ context.Context, scope model.RetentionHoldScope, _ *store.CommandIdempotency) ([]store.ExamSubmissionAuthorization, error) {
	s.creationScopes = append(s.creationScopes, scope)
	return s.sources(scope), nil
}

func (s *examExportRecords) Create(_ context.Context, input *store.ExamExportCreation, command *store.CommandIdempotency) (*model.ExamExport, error) {
	s.creations, s.commands = append(s.creations, input), append(s.commands, command)
	at := input.AuditAt
	created := &model.ExamExport{
		ID: input.ExportID, Scope: input.Scope, RequesterUserID: input.Principal.UserID,
		Categories: slices.Clone(input.Categories), State: model.ExamExportQueued, PolicyRevision: 1,
		CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour), SourceExpiresAt: at.Add(24 * time.Hour),
		SubmissionCount: len(input.SubmissionIDs),
	}
	s.current = created
	return created, nil
}

func (s *examExportRecords) Get(_ context.Context, input *store.ExamExportAccess) (*store.ExamExportDownload, error) {
	s.gets = append(s.gets, input)
	if s.current == nil || s.current.ID != input.ExportID || s.current.Scope != input.Scope {
		return nil, store.NewErrNotFound("exam_export", input.ExportID.String())
	}
	current := *s.current
	download := &store.ExamExportDownload{Export: &current, Submissions: s.sources(input.Scope)}
	if current.State == model.ExamExportReady {
		download.Artifact = store.ExamExportArtifact{ExportID: current.ID, AttemptID: s.artifact}
	}
	return download, nil
}

// examExportArchives serves every export archive as the bytes "archive".
type examExportArchives struct {
	opens int
}

func (a *examExportArchives) BuildExamExport(context.Context, exam.ExportArchiveInput) (*exam.ExportArchiveContent, error) {
	return nil, errors.New("the fixture builds no export archive")
}

func (a *examExportArchives) OpenExamExport(context.Context, model.ExamExportID, model.JobAttemptID) (io.ReadCloser, error) {
	a.opens++
	return io.NopCloser(strings.NewReader("archive")), nil
}

func (a *examExportArchives) PurgeExamExport(context.Context, model.ExamExportID, model.JobAttemptID) error {
	return nil
}

// examManagerMail prepares every Exam Manager notice it is asked for and
// records the request.
type examManagerMail struct {
	requests []exam.ManagerMailPreparation
}

func (m *examManagerMail) PrepareManagerMail(request exam.ManagerMailPreparation) (*store.ExamManagerMail, error) {
	m.requests = append(m.requests, request)
	occurrence := &model.MailOccurrence{ID: request.OccurrenceID, Kind: model.MailOccurrenceExamManagement,
		TemplateKey: request.TemplateKey, ActorUserID: request.Recipient.ID, CreatedAt: request.ActionAt}
	delivery := &model.MailDelivery{TargetUserID: request.Recipient.ID, TemplateKey: request.TemplateKey}
	return &store.ExamManagerMail{Occurrence: occurrence, Delivery: delivery, Job: &model.Job{}}, nil
}
