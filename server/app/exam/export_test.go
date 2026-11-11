// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type exportAuthorizerFake struct {
	actions []model.Action
	deny    model.Action
	self    bool
	denyErr error
}

func (a *exportAuthorizerFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, _ model.Resource) error {
	a.actions = append(a.actions, action)
	if action == a.deny {
		return errors.New("current authority denied")
	}
	return nil
}
func (a *exportAuthorizerFake) RecordDenial(context.Context, invocation.Invocation, model.Action, model.Resource, model.AcademicUnitID) error {
	a.self = true
	return a.denyErr
}

func TestBrowserHistoryExportRejectsOverrideAndRechecksBeforeOpeningBytes(t *testing.T) {
	for _, removed := range []string{"manager"} {
		t.Run(removed, func(t *testing.T) {
			f, service, persistence, auth, content := newExportFixture(t)
			f.persistence.actorIsManager = true
			f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID, UserID: f.userID}}
			persistence.value.Categories = []model.RetentionCategory{model.RetentionCategoryBrowserActivity}
			query := ExportQuery{Scope: persistence.value.Scope, ExportID: persistence.value.ID}
			_, err := service.GetExamExport(context.Background(), f.inv, query)
			require.NoError(t, err)
			// Unit membership is part of ordinary export authorization;
			// the independently permitted export override leaves the exact
			// Browser Activity Manager predicate and permission intact.
			f.memberships.items = nil
			_, err = service.GetExamExport(context.Background(), f.inv, query)
			require.NoError(t, err)
			f.persistence.actorIsManager = false
			_, err = service.GetExamExport(context.Background(), f.inv, query)
			require.Error(t, err, "export metadata retained removed Browser Activity access")
			require.True(t, auth.self, "export metadata retained removed Browser Activity access")
			_, err = service.OpenExamExport(context.Background(), f.inv, query)
			require.Error(t, err, "unauthorized history reached archive bytes")
			require.Equal(t, 0, content.opens, "unauthorized history reached archive bytes")
			_, err = service.CreateExamExport(context.Background(), f.inv, CreateExportCommand{Scope: query.Scope, Categories: persistence.value.Categories, IdempotencyKey: "replay"})
			require.Error(t, err, "override created browser history export")
			require.Nil(t, persistence.create, "override created browser history export")
			auditErr := errors.New("denial audit unavailable")
			auth.denyErr = auditErr
			_, err = service.GetExamExport(context.Background(), f.inv, query)
			require.ErrorIs(t, err, auditErr)
		})
	}
}

type exportStoreFake struct {
	store.ExamExportStore
	selected        []store.ExamSubmissionAuthorization
	creationScope   []store.ExamSubmissionAuthorization
	creationCommand *store.CommandIdempotency
	value           *model.ExamExport
	readAt          func() time.Time
	getErr          error
	artifactID      model.JobAttemptID
	create          *store.ExamExportCreation
	createErr       error
	publishErr      error
	finished        int
	finishCanceled  bool
	published       int
}

func (s *exportStoreFake) ListScope(context.Context, model.RetentionHoldScope) ([]store.ExamSubmissionAuthorization, error) {
	if len(s.selected) > model.ExamExportMaximumSubmissions {
		return nil, store.NewErrConflict("exam_export", "limit", nil)
	}
	return s.selected, nil
}
func (s *exportStoreFake) ListCreationScope(_ context.Context, _ model.RetentionHoldScope, command *store.CommandIdempotency) ([]store.ExamSubmissionAuthorization, error) {
	s.creationCommand = command
	if s.creationScope != nil {
		return s.creationScope, nil
	}
	return s.selected, nil
}
func (s *exportStoreFake) Create(_ context.Context, c *store.ExamExportCreation, _ *store.CommandIdempotency) (*model.ExamExport, error) {
	s.create = c
	if s.createErr != nil {
		return nil, s.createErr
	}
	v := *s.value
	v.ID = c.ExportID
	v.Scope = c.Scope
	v.Categories = slices.Clone(c.Categories)
	return &v, nil
}
func (s *exportStoreFake) Get(context.Context, *store.ExamExportAccess) (*store.ExamExportDownload, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	value := *s.value
	artifact := store.ExamExportArtifact{ExportID: value.ID, AttemptID: s.artifactID}
	if !s.readAt().Before(value.ExpiresAt) {
		value.State = model.ExamExportExpired
		value.ArchiveSizeBytes, value.ArchiveSHA256, value.ReadyAt = 0, "", model.OptionalTime{}
		artifact = store.ExamExportArtifact{}
	}
	return &store.ExamExportDownload{Export: &value, Artifact: artifact, Submissions: s.selected}, nil
}
func (s *exportStoreFake) BeginBuild(context.Context, *store.ExamExportBuild) (*store.ExamExportSnapshot, error) {
	b, _ := json.Marshal(map[string]any{"export_id": s.value.ID})
	return &store.ExamExportSnapshot{Export: s.value, Records: b}, nil
}
func (s *exportStoreFake) Publish(context.Context, *store.ExamExportPublication) error {
	s.published++
	return s.publishErr
}
func (s *exportStoreFake) FinishWriter(ctx context.Context, _ store.ExamExportArtifact) error {
	s.finished++
	s.finishCanceled = ctx.Err() != nil
	return nil
}

type exportContentFake struct {
	opens, builds, purges int
	opened                func()
	body                  io.ReadCloser
	cancel                context.CancelFunc
	err                   error
	deadline              time.Time
}

func (c *exportContentFake) BuildExamExport(ctx context.Context, _ ExportArchiveInput) (*ExportArchiveContent, error) {
	c.builds++
	c.deadline, _ = ctx.Deadline()
	if c.cancel != nil {
		c.cancel()
	}
	if c.err != nil {
		return nil, c.err
	}
	return &ExportArchiveContent{SizeBytes: 42, SHA256: strings.Repeat("a", 64)}, nil
}
func (c *exportContentFake) OpenExamExport(context.Context, model.ExamExportID, model.JobAttemptID) (io.ReadCloser, error) {
	c.opens++
	if c.opened != nil {
		c.opened()
	}
	if c.body != nil {
		return c.body, c.err
	}
	return io.NopCloser(strings.NewReader("archive")), c.err
}
func (c *exportContentFake) PurgeExamExport(context.Context, model.ExamExportID, model.JobAttemptID) error {
	c.purges++
	return nil
}

func newExportFixture(t *testing.T) (authoringFixture, *Exports, *exportStoreFake, *exportAuthorizerFake, *exportContentFake) {
	t.Helper()
	f := newAuthoringFixture(t)
	now := model.NowUTC()
	scope := model.RetentionHoldScope{ExamID: f.examID, SittingID: model.NewExamSittingID(), SubmissionID: model.NewSubmissionID()}
	s := &exportStoreFake{value: &model.ExamExport{ID: model.NewExamExportID(), Scope: scope, RequesterUserID: f.userID, Categories: []model.RetentionCategory{model.RetentionCategoryWork}, State: model.ExamExportQueued, PolicyRevision: 1, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), SourceExpiresAt: now.Add(24 * time.Hour), SubmissionCount: 1}}
	s.readAt, s.artifactID = func() time.Time { return now }, model.NewJobAttemptID()
	s.selected = []store.ExamSubmissionAuthorization{{SubmissionID: scope.SubmissionID, ExamID: scope.ExamID, SittingID: scope.SittingID, AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}}
	auth, content := &exportAuthorizerFake{}, &exportContentFake{}
	service, err := NewExports(ExportsDependencies{
		Persistence: s,
		Access:      f.persistence,
		Memberships: f.memberships,
		Authorizer:  auth,
		Auditor:     f.auditor,
		Content:     content,
		Now:         func() time.Time { return now },
		NewID:       model.NewExamExportID,
		NewJobID:    model.NewJobID,
	})
	require.NoError(t, err)
	return f, service, s, auth, content
}
