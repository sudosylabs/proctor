// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// sittingFixture is the real Sitting and correction services the Sitting
// handlers call, over the Exam fixture's stores plus a Sitting Store holding
// one Scheduled Sitting of the fixture's Exam. The caller manages that Exam
// and may schedule, manage and correct its Sittings. A handler test asserts
// what the request turned into at the Store.
type sittingFixture struct {
	*examFixture
	// sitting is the seeded Sitting as the Sitting Store holds it now.
	sitting *model.ExamSitting

	sittingStore      *sittingLedger
	correctionStore   *correctionLedger
	correctionContent *correctionRenditions

	sittingService    *sitting.Service
	correctionService *correction.Service
}

// sittingActions are every action the Sitting handlers' use cases ask about.
var sittingActions = append(slices.Clone(examActions),
	model.ActionExamSittingCreate, model.ActionExamSittingCreateOverride,
	model.ActionExamSittingManage, model.ActionExamSittingManageOverride,
)

// sittingFixtureTime is when the seeded Sitting was scheduled; it starts an
// hour later and ends two hours after that.
var sittingFixtureTime = time.Date(2026, time.August, 15, 9, 30, 0, 123456789, time.UTC)

func newSittingFixture(t *testing.T, caller model.Principal) *sittingFixture {
	t.Helper()
	return newSittingFixtureWith(t, caller, sittingActions...)
}

// newSittingFixtureWith builds the fixture with the caller holding actions
// in the Exam's Academic Unit instead of sittingActions.
func newSittingFixtureWith(t *testing.T, caller model.Principal, actions ...model.Action) *sittingFixture {
	t.Helper()
	f := &sittingFixture{examFixture: newExamFixture(t, caller, actions...)}
	record, err := model.NewExamSitting(model.NewExamSittingID(), f.examID, model.NewExamRevisionID(), model.NewClassID(),
		sittingFixtureTime.Add(time.Hour), sittingFixtureTime.Add(3*time.Hour), model.ExamSittingJoinWindow{}, sittingFixtureTime)
	require.NoError(t, err)
	f.sitting = record
	f.sittingStore = &sittingLedger{byID: map[model.ExamSittingID]*model.ExamSitting{record.ID: record}}
	f.correctionStore = &correctionLedger{sittings: f.sittingStore}
	f.correctionContent = &correctionRenditions{}

	composer, err := mail.NewSittingComposer(mail.SittingComposerDependencies{
		Renderer: httpMailRenderer(t), Sender: &identityMailSender{}, Now: time.Now,
	})
	require.NoError(t, err)
	f.sittingService, err = sitting.New(sitting.Dependencies{
		Persistence: f.sittingStore, Access: f.exams, Memberships: f.members,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor, Publisher: f.publisher,
		Jobs: jobs.NewExamSittingLifecycleJobFactory(time.Now, model.NewJobID),
		Mail: sitting.ScheduleMail{Composer: composer, Revisions: sittingMailRevisions{}, Classes: sittingMailClasses{}},
		Now:  time.Now, NewID: model.NewExamSittingID,
	})
	require.NoError(t, err)
	f.correctionService, err = correction.New(correction.Dependencies{
		Persistence: f.correctionStore, Revisions: sittingMailRevisions{}, Sittings: f.sittingStore,
		Access: f.exams, Memberships: f.members,
		Authorizer: f.access.Authorizer, Auditor: f.access.Auditor, Publisher: f.publisher,
		Content: f.correctionContent, Now: time.Now,
		NewStageID: model.NewExamCorrectionResourceStageID, NewResourceID: model.NewExamResourceID,
		NewEntryID: model.NewFileEntryID, NewFileRevisionID: model.NewFileRevisionID,
		NewLeaseID: model.NewUploadLeaseID, NewRenditionID: model.NewFileRenditionID,
		NewExamRevisionID: model.NewExamRevisionID, InstitutionOrigin: "https://institution.example",
	})
	require.NoError(t, err)
	return f
}

// moveSittingTo puts the seeded Sitting into state through the lifecycle the
// SQL Store applies, then sets its revision, so a test can send the expected
// revision its request names.
func (f *sittingFixture) moveSittingTo(t *testing.T, state model.ExamSittingState, revision int64) {
	t.Helper()
	at := sittingFixtureTime.Add(time.Hour)
	steps := map[model.ExamSittingState][]func(*model.ExamSitting) error{
		model.ExamSittingScheduled: nil,
		model.ExamSittingOpen:      {func(s *model.ExamSitting) error { return s.Open(at) }},
		model.ExamSittingPaused: {
			func(s *model.ExamSitting) error { return s.Open(at) },
			func(s *model.ExamSitting) error { return s.Pause(at.Add(time.Minute)) },
		},
		model.ExamSittingClosed: {
			func(s *model.ExamSitting) error { return s.Open(at) },
			func(s *model.ExamSitting) error {
				return s.EnterClosing(model.ExamSittingReasonManagerClosed, at.Add(time.Minute))
			},
			func(s *model.ExamSitting) error { return s.Close(at.Add(2 * time.Minute)) },
		},
	}
	path, ok := steps[state]
	require.True(t, ok, "the fixture cannot reach Sitting state %s", state)
	for _, step := range path {
		require.NoError(t, step(f.sitting))
	}
	f.sitting.Revision = revision
}

// sittingMailRevisions names every published Revision for schedule notices
// and holds no Revision snapshots, so a correction can only add Resources.
type sittingMailRevisions struct{ store.ExamRevisionStore }

func (sittingMailRevisions) GetSummary(_ context.Context, examID model.ExamID, id model.ExamRevisionID) (*store.ExamRevisionSummary, error) {
	return &store.ExamRevisionSummary{ID: id, ExamID: examID, Number: 1, Title: "Systems"}, nil
}

// sittingMailClasses names every Class a schedule notice mentions.
type sittingMailClasses struct{ store.ClassStore }

func (sittingMailClasses) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	return &model.Class{ID: id, DisplayName: "Algorithms A"}, nil
}

// sittingLedger holds Sittings by identity and applies each manager command
// under the revision fence and lifecycle rules the SQL Store applies,
// recording every request it is asked.
type sittingLedger struct {
	store.ExamSittingStore
	byID map[model.ExamSittingID]*model.ExamSitting
	// unitID, when set, is the Academic Unit Resolve places every Sitting in.
	unitID   model.AcademicUnitID
	resolves []model.ExamSittingID
	// listed and noShows are what List and ListNoShows answer.
	listed  []store.ExamSittingSnapshot
	noShows []store.ExamSittingNoShow

	schedules   []*store.ExamSittingSchedule
	gets        []sittingIdentity
	lists       []store.ExamSittingListOptions
	updates     []*store.ExamSittingScheduleUpdate
	cancels     []*store.ExamSittingCancellation
	pauses      []*store.ExamSittingManagerTransition
	resumes     []*store.ExamSittingManagerTransition
	extensions  []*store.ExamSittingExtension
	closes      []*store.ExamSittingManagerTransition
	noShowLists []store.ExamSittingNoShowListOptions
	commands    []*store.CommandIdempotency
}

// sittingIdentity is the pair of identities a Sitting read names.
type sittingIdentity struct {
	ExamID    model.ExamID
	SittingID model.ExamSittingID
}

// lastCommand is the idempotency of the most recent mutation.
func (s *sittingLedger) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no mutation reached the Sitting Store")
	return s.commands[len(s.commands)-1]
}

func (s *sittingLedger) snapshot(record *model.ExamSitting) *store.ExamSittingSnapshot {
	copied := *record
	return &store.ExamSittingSnapshot{Sitting: &copied}
}

func (s *sittingLedger) current(examID model.ExamID, id model.ExamSittingID) (*model.ExamSitting, error) {
	record, ok := s.byID[id]
	if !ok || record.ExamID != examID {
		return nil, store.NewErrNotFound("exam_sitting", id.String())
	}
	return record, nil
}

// fenced is the Sitting a command changes, when the command expects its
// current revision.
func (s *sittingLedger) fenced(examID model.ExamID, id model.ExamSittingID, expected int64) (*model.ExamSitting, error) {
	record, err := s.current(examID, id)
	if err != nil {
		return nil, err
	}
	if record.Revision != expected {
		return nil, store.NewErrConflict("exam_sitting", "exam_sitting_revision", nil)
	}
	return record, nil
}

func (s *sittingLedger) Schedule(_ context.Context, input *store.ExamSittingSchedule, command *store.CommandIdempotency) (*store.ExamSittingCommandResult, error) {
	s.schedules, s.commands = append(s.schedules, input), append(s.commands, command)
	record := *input.Sitting
	s.byID[record.ID] = &record
	return &store.ExamSittingCommandResult{Value: s.snapshot(&record)}, nil
}

func (s *sittingLedger) Get(_ context.Context, examID model.ExamID, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	s.gets = append(s.gets, sittingIdentity{ExamID: examID, SittingID: id})
	record, err := s.current(examID, id)
	if err != nil {
		return nil, err
	}
	return s.snapshot(record), nil
}

// Resolve is the Sitting with its Exam's Academic Unit, as the Attempt
// service reads it.
func (s *sittingLedger) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	s.resolves = append(s.resolves, id)
	record, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("exam_sitting", id.String())
	}
	snapshot := s.snapshot(record)
	snapshot.AcademicUnitID = s.unitID
	return snapshot, nil
}

func (s *sittingLedger) List(_ context.Context, options store.ExamSittingListOptions) ([]store.ExamSittingSnapshot, error) {
	s.lists = append(s.lists, options)
	return slices.Clone(s.listed), nil
}

func (s *sittingLedger) UpdateSchedule(_ context.Context, input *store.ExamSittingScheduleUpdate, command *store.CommandIdempotency) (*store.ExamSittingCommandResult, error) {
	s.updates, s.commands = append(s.updates, input), append(s.commands, command)
	record, err := s.fenced(input.ExamID, input.SittingID, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if _, err := record.ApplySchedule(input.ExamRevisionID, input.ClassID, input.ScheduledStartAt, input.ScheduledEndAt, input.JoinWindow, input.AuditAt); err != nil {
		return nil, store.NewErrConflict("exam_sitting", "exam_sitting_state", err)
	}
	return &store.ExamSittingCommandResult{Value: s.snapshot(record)}, nil
}

func (s *sittingLedger) Cancel(_ context.Context, input *store.ExamSittingCancellation, command *store.CommandIdempotency) (*store.ExamSittingCommandResult, error) {
	s.cancels, s.commands = append(s.cancels, input), append(s.commands, command)
	record, err := s.fenced(input.ExamID, input.SittingID, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if err := record.Cancel(model.ExamSittingReasonManagerCanceled, input.AuditAt); err != nil {
		return nil, store.NewErrConflict("exam_sitting", "exam_sitting_state", err)
	}
	return &store.ExamSittingCommandResult{Value: s.snapshot(record)}, nil
}

// transition applies one manager lifecycle change to the fenced Sitting.
func (s *sittingLedger) transition(
	examID model.ExamID, id model.ExamSittingID, expected int64,
	code store.ExamSittingLifecycleTransitionCode, apply func(*model.ExamSitting) error,
) (*store.ExamSittingLifecycleResult, error) {
	record, err := s.fenced(examID, id, expected)
	if err != nil {
		return nil, err
	}
	if err := apply(record); err != nil {
		return nil, store.NewErrConflict("exam_sitting", "exam_sitting_state", err)
	}
	return &store.ExamSittingLifecycleResult{Value: s.snapshot(record), Transition: code, Changed: true}, nil
}

func (s *sittingLedger) Pause(_ context.Context, input *store.ExamSittingManagerTransition, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	s.pauses, s.commands = append(s.pauses, input), append(s.commands, command)
	return s.transition(input.ExamID, input.SittingID, input.ExpectedRevision, store.ExamSittingTransitionManagerPaused,
		func(record *model.ExamSitting) error { return record.Pause(input.ChangedAt) })
}

func (s *sittingLedger) Resume(_ context.Context, input *store.ExamSittingManagerTransition, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	s.resumes, s.commands = append(s.resumes, input), append(s.commands, command)
	return s.transition(input.ExamID, input.SittingID, input.ExpectedRevision, store.ExamSittingTransitionManagerResumed,
		func(record *model.ExamSitting) error { return record.Resume(input.ChangedAt) })
}

func (s *sittingLedger) Extend(_ context.Context, input *store.ExamSittingExtension, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	s.extensions, s.commands = append(s.extensions, input), append(s.commands, command)
	return s.transition(input.ExamID, input.SittingID, input.ExpectedRevision, store.ExamSittingTransitionManagerExtended,
		func(record *model.ExamSitting) error { return record.ExtendEnd(input.ScheduledEndAt, input.ChangedAt) })
}

func (s *sittingLedger) EarlyClose(_ context.Context, input *store.ExamSittingManagerTransition, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	s.closes, s.commands = append(s.closes, input), append(s.commands, command)
	return s.transition(input.ExamID, input.SittingID, input.ExpectedRevision, store.ExamSittingTransitionManagerClosed,
		func(record *model.ExamSitting) error {
			return record.EnterClosing(model.ExamSittingReasonManagerClosed, input.ChangedAt)
		})
}

func (s *sittingLedger) ListNoShows(_ context.Context, options store.ExamSittingNoShowListOptions) ([]store.ExamSittingNoShow, error) {
	s.noShowLists = append(s.noShowLists, options)
	return slices.Clone(s.noShows), nil
}

// ListCandidateInvalidationTargetsBySitting finds no candidate whose Exam
// activity a change refreshes.
func (s *sittingLedger) ListCandidateInvalidationTargetsBySitting(context.Context, model.ExamSittingID, model.UserID, int) ([]model.UserID, error) {
	return nil, nil
}

// correctionLedger keeps the correction resource stages a test reserved and
// applies corrections to the Sitting Store's Sittings under the revision
// fences the SQL Store applies, recording every request it is asked.
type correctionLedger struct {
	store.ExamCorrectionStore
	sittings *sittingLedger
	stages   map[model.ExamCorrectionResourceStageID]*store.ExamCorrectionResourceStage

	reservations []*store.ExamCorrectionResourceStageReservation
	readiness    []*store.ExamCorrectionResourceStageReadiness
	applications []*store.ExamCorrectionApplication
	commands     []*store.CommandIdempotency
}

// lastCommand is the idempotency of the most recent command.
func (s *correctionLedger) lastCommand(t *testing.T) *store.CommandIdempotency {
	t.Helper()
	require.NotEmpty(t, s.commands, "no command reached the correction Store")
	return s.commands[len(s.commands)-1]
}

func (s *correctionLedger) ReserveResourceStage(_ context.Context, input *store.ExamCorrectionResourceStageReservation, command *store.CommandIdempotency) (*store.ExamCorrectionResourceStage, error) {
	s.reservations, s.commands = append(s.reservations, input), append(s.commands, command)
	stage := &store.ExamCorrectionResourceStage{
		ID: input.StageID, ExamID: input.ExamID, SittingID: input.SittingID, BaseRevisionID: input.BaseRevisionID,
		Target: input.Target, ResourceID: input.ResourceID, FileEntryID: input.FileEntryID,
		FileRevisionID: input.Revision.ID, UploadLeaseID: input.Lease.ID, RenditionID: input.RenditionID,
		CreatedByUserID: input.ActorUserID, State: store.ExamCorrectionResourceStagePending,
		CreatedAt: input.CreatedAt, ExpiresAt: input.CreatedAt.Add(time.Hour),
	}
	if s.stages == nil {
		s.stages = map[model.ExamCorrectionResourceStageID]*store.ExamCorrectionResourceStage{}
	}
	s.stages[stage.ID] = stage
	copied := *stage
	return &copied, nil
}

func (s *correctionLedger) MarkResourceStageReady(_ context.Context, input *store.ExamCorrectionResourceStageReadiness) (*store.ExamCorrectionResourceStage, error) {
	s.readiness = append(s.readiness, input)
	stage, ok := s.stages[input.StageID]
	if !ok {
		return nil, store.NewErrNotFound("exam_correction_resource_stage", input.StageID.String())
	}
	stage.State, stage.Rendition, stage.ReadyAt = store.ExamCorrectionResourceStageReady, input.Rendition, input.ReadyAt
	copied := *stage
	return &copied, nil
}

// Apply seals the correction Revision as the next one and retargets the
// Sitting to it.
func (s *correctionLedger) Apply(_ context.Context, input *store.ExamCorrectionApplication, command *store.CommandIdempotency) (*store.ExamCorrectionResult, error) {
	s.applications, s.commands = append(s.applications, input), append(s.commands, command)
	record, err := s.sittings.fenced(input.ExamID, input.SittingID, input.ExpectedSittingRevision)
	if err != nil {
		return nil, err
	}
	if record.ExamRevisionID != input.CurrentRevisionID {
		return nil, store.NewErrConflict("exam_correction", "exam_correction_base_revision", nil)
	}
	if err := record.RetargetRevision(input.RevisionID, input.AppliedAt); err != nil {
		return nil, store.NewErrConflict("exam_sitting", "exam_sitting_state", err)
	}
	return &store.ExamCorrectionResult{
		Revision: &store.ExamRevisionSummary{
			ID: input.RevisionID, ExamID: input.ExamID, Number: 4,
			Kind: model.ExamRevisionPublicationLiveCorrection, PublishedAt: input.AppliedAt,
		},
		Sitting:            s.sittings.snapshot(record),
		PreviousRevisionID: input.CurrentRevisionID,
		EffectiveAt:        input.AppliedAt,
	}, nil
}

// correctionRenditions stores staged correction content the way the file
// store does: it reads every byte, digests what it read, and reports the
// media type it detected, which for any text is text/plain.
type correctionRenditions struct {
	stored [][]byte
}

func (c *correctionRenditions) StoreExamResourceRendition(
	_ context.Context, revisionID model.FileRevisionID, renditionID model.FileRenditionID,
	_ model.ExamResourceMediaType, body io.Reader, _ int64, at time.Time,
) (model.FileRendition, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return model.FileRendition{}, err
	}
	c.stored = append(c.stored, raw)
	digest := sha256.Sum256(raw)
	rendition, err := model.NewFileRendition(renditionID, revisionID, "original", string(model.ExamResourceMediaText),
		int64(len(raw)), 0, 0, hex.EncodeToString(digest[:]), at)
	if err != nil {
		return model.FileRendition{}, err
	}
	return *rendition, nil
}
