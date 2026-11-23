// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type retentionListings struct {
	store.RetentionStore
	pages     map[model.SubmissionID]*store.RetentionRecordPage
	cursors   []model.SubmissionID
	holds     *store.RetentionHoldPage
	summaries *store.RetentionExamSummaryPage
	due       []store.RetentionHoldDue
}

func (s *retentionListings) ListRecords(_ context.Context, opts store.RetentionRecordListOptions) (*store.RetentionRecordPage, error) {
	s.cursors = append(s.cursors, opts.AfterSubmissionID)
	return s.pages[opts.AfterSubmissionID], nil
}

func (s *retentionListings) ListHolds(context.Context, store.RetentionHoldListOptions) (*store.RetentionHoldPage, error) {
	return s.holds, nil
}

func (s *retentionListings) SummarizeRecordsByExam(context.Context, store.RetentionExamSummaryOptions) (*store.RetentionExamSummaryPage, error) {
	return s.summaries, nil
}

func (s *retentionListings) ClaimDueHoldNotifications(context.Context) ([]store.RetentionHoldDue, error) {
	claimed := s.due
	s.due = nil
	return claimed, nil
}

func TestRetentionRecordsExportPagesAndCompletesAudit(t *testing.T) {
	t.Parallel()
	fixture := newRetentionPolicyFixture(t)
	exam, sitting := model.NewExamID(), model.NewExamSittingID()
	first, second := model.NewSubmissionID(), model.NewSubmissionID()
	if second < first {
		first, second = second, first
	}
	item := func(submission model.SubmissionID, category model.RetentionCategory) store.RetentionRecordItem {
		return store.RetentionRecordItem{Record: model.RetentionRecord{Scope: model.RetentionHoldScope{
			ExamID: exam, SittingID: sitting, SubmissionID: submission,
		}, Category: category}}
	}
	listing := &retentionListings{pages: map[model.SubmissionID]*store.RetentionRecordPage{
		"": {PolicyRevision: 1, AsOf: fixture.at, HasMore: true,
			ExamTitles: map[model.ExamID]string{exam: "=HYPERLINK()"},
			Items:      []store.RetentionRecordItem{item(first, model.RetentionCategoryWork), item(first, model.RetentionCategoryIntegrity)}},
		first: {PolicyRevision: 1, AsOf: fixture.at, Items: []store.RetentionRecordItem{item(second, model.RetentionCategoryWork)}},
	}}
	fixture.service.retention = listing
	var out bytes.Buffer
	require.NoError(t, fixture.service.ExportRetentionRecords(t.Context(), fixture.inv, &out))
	require.Equal(t, []model.SubmissionID{"", first}, listing.cursors)
	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 4)
	require.Equal(t, "'=HYPERLINK()", rows[1][1])
	require.Equal(t, first.String(), rows[2][3])
	require.Equal(t, second.String(), rows[3][3])
	require.Equal(t, model.AuditStatusSuccess, onlyAttempt(t, fixture.access).Status)
	require.Equal(t, "export_records", onlyAttempt(t, fixture.access).Operation)
}

func TestRetentionRecordsExportStopsBeforeReadingWhenAuditFails(t *testing.T) {
	t.Parallel()
	fixture := newRetentionPolicyFixture(t)
	listing := &retentionListings{}
	fixture.service.retention = listing
	fixture.access.Audits.FailAttempts(errors.New("audit unavailable"))
	var out bytes.Buffer
	err := fixture.service.ExportRetentionRecords(t.Context(), fixture.inv, &out)
	require.True(t, apperror.Is(err, "retention.unavailable"), "%v", err)
	require.Empty(t, listing.cursors)
	require.Empty(t, out.Bytes())
}

func TestInstitutionRetentionListingsRequirePermissionBeforeReading(t *testing.T) {
	t.Parallel()
	fixture := newRetentionPolicyFixture(t)
	listing := &retentionListings{
		holds:     &store.RetentionHoldPage{Holds: []model.RetentionHold{}, ExamTitles: map[model.ExamID]string{}},
		summaries: &store.RetentionExamSummaryPage{PolicyRevision: 1, AsOf: fixture.at, Items: []store.RetentionExamSummary{}},
	}
	fixture.service.retention = listing
	holds, err := fixture.service.ListRetentionHolds(t.Context(), fixture.stranger, ListRetentionHoldsQuery{Limit: 1})
	require.True(t, apperror.Is(err, "authorization.denied"), "%v", err)
	require.Nil(t, holds)
	summaries, err := fixture.service.SummarizeRetentionRecordsByExam(t.Context(), fixture.stranger, "", 1)
	require.True(t, apperror.Is(err, "authorization.denied"), "%v", err)
	require.Nil(t, summaries)

	holds, err = fixture.service.ListRetentionHolds(t.Context(), fixture.inv, ListRetentionHoldsQuery{Limit: 1})
	require.NoError(t, err)
	require.Same(t, listing.holds, holds)
	summaries, err = fixture.service.SummarizeRetentionRecordsByExam(t.Context(), fixture.inv, "", 1)
	require.NoError(t, err)
	require.Same(t, listing.summaries, summaries)
}

func TestRetentionHoldReviewTaskPublishesOnlyClaimedHolds(t *testing.T) {
	t.Parallel()
	fixture := newRetentionPolicyFixture(t)
	due := store.RetentionHoldDue{ID: model.NewRetentionHoldID(), ExamID: model.NewExamID()}
	fixture.service.retention = &retentionListings{due: []store.RetentionHoldDue{due}}
	task := fixture.service.RetentionHoldReviewTask()
	require.NoError(t, task.Runner.Run(t.Context()))
	require.NoError(t, task.Runner.Run(t.Context()))
	event, err := realtime.NewRetentionHoldChangedEvent(fixture.institution.ID, due.ID, due.ExamID)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: event}}, fixture.service.publisher.(*realtimetest.Publisher).Calls())
}
