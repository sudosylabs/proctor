// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// MaximumRetentionExportSubmissions bounds one records export; a larger
// Institution narrows it by paging the records listing instead.
const MaximumRetentionExportSubmissions = 50_000

// ListRetentionHoldsQuery pages the Institution's holds.
type ListRetentionHoldsQuery struct {
	ExamID          model.ExamID
	ReasonCode      string
	IncludeReleased bool
	After           model.RetentionHoldID
	Limit           int
}

// ListRetentionHolds needs retention_policy.view at the Institution, as the
// records listing does. It pages every hold of the Institution.
func (s *Service) ListRetentionHolds(
	ctx context.Context,
	inv invocation.Invocation,
	query ListRetentionHoldsQuery,
) (*store.RetentionHoldPage, error) {
	if _, _, err := s.authorizeRetentionPolicy(ctx, inv, model.ActionRetentionPolicyView); err != nil {
		return nil, err
	}
	if query.Limit < 1 || query.Limit > 200 ||
		!query.After.IsZero() && !query.After.IsValid() ||
		!query.ExamID.IsZero() && !query.ExamID.IsValid() ||
		query.ReasonCode != "" && !model.ValidRecordsReasonCode(query.ReasonCode) {
		return nil, apperror.New("retention.invalid")
	}
	page, err := s.retention.ListHolds(ctx, store.RetentionHoldListOptions(query))
	if err != nil {
		return nil, retentionError(err)
	}
	if page == nil || len(page.Holds) > query.Limit || page.HasMore && len(page.Holds) == 0 {
		return nil, apperror.New("retention.unavailable")
	}
	return page, nil
}

// SummarizeRetentionRecordsByExam needs retention_policy.view at the
// Institution. It pages the Exams that have records, with each Exam's counts.
func (s *Service) SummarizeRetentionRecordsByExam(
	ctx context.Context,
	inv invocation.Invocation,
	after model.ExamID,
	limit int,
) (*store.RetentionExamSummaryPage, error) {
	if _, _, err := s.authorizeRetentionPolicy(ctx, inv, model.ActionRetentionPolicyView); err != nil {
		return nil, err
	}
	if limit < 1 || limit > model.RetentionMaximumPageSize || !after.IsZero() && !after.IsValid() {
		return nil, apperror.New("retention.invalid")
	}
	page, err := s.retention.SummarizeRecordsByExam(ctx, store.RetentionExamSummaryOptions{AfterExamID: after, Limit: limit})
	if err != nil {
		return nil, retentionError(err)
	}
	if page == nil || page.PolicyRevision < 1 || len(page.Items) > limit || page.HasMore && len(page.Items) == 0 {
		return nil, apperror.New("retention.unavailable")
	}
	return page, nil
}

// ExportRetentionRecords writes every record as CSV, one row per record, with
// the eligibility each page read. It needs retention_policy.view, like the
// listing it walks, and refuses an Institution past
// MaximumRetentionExportSubmissions submissions. Records hold no student
// content, so the export carries none either.
func (s *Service) ExportRetentionRecords(ctx context.Context, inv invocation.Invocation, w io.Writer) error {
	institution, resource, err := s.authorizeRetentionPolicy(ctx, inv, model.ActionRetentionPolicyView)
	if err != nil {
		return err
	}
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionRetentionPolicyView,
		Resource:   resource,
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institution.ID.String(),
		Operation:  "export_records",
	})
	if err != nil {
		return apperror.New("retention.unavailable").Wrap(err)
	}
	submissions, err := s.writeRetentionRecords(ctx, w)
	if err != nil {
		auditErr := s.auditor.Fail(ctx, auditID, apperror.CodeOf(err, "retention.unavailable"))
		return errors.Join(err, auditErr)
	}
	if err := s.auditor.Complete(ctx, auditID, map[string]any{"submission_count": submissions}); err != nil {
		return apperror.New("retention.unavailable").Wrap(err)
	}
	return nil
}

func (s *Service) writeRetentionRecords(ctx context.Context, w io.Writer) (int, error) {
	out := csv.NewWriter(w)
	if err := out.Write([]string{
		"exam_id", "exam_title", "exam_sitting_id", "submission_id", "category", "blocker", "eligible_at",
		"held", "completion_current", "retired_at", "policy_revision", "as_of",
	}); err != nil {
		return 0, apperror.New("retention.unavailable").Wrap(err)
	}

	var after model.SubmissionID
	submissions := 0
	for {
		previous := after
		page, err := s.retention.ListRecords(ctx, store.RetentionRecordListOptions{AfterSubmissionID: after, Limit: model.RetentionMaximumPageSize})
		if err != nil {
			return 0, retentionError(err)
		}
		if page == nil || page.PolicyRevision < 1 || page.AsOf.IsZero() ||
			len(page.Items) > model.RetentionRecordsPerSubmission*model.RetentionMaximumPageSize ||
			page.HasMore && len(page.Items) == 0 {
			return 0, apperror.New("retention.unavailable")
		}
		for _, item := range page.Items {
			record := item.Record
			if record.Scope.SubmissionID < after || !record.Scope.SubmissionID.IsValid() {
				return 0, apperror.New("retention.unavailable")
			}
			if record.Scope.SubmissionID != after {
				after = record.Scope.SubmissionID
				submissions++
			}
			if submissions > MaximumRetentionExportSubmissions {
				return 0, apperror.New("retention.invalid").WithField("field", "records")
			}
			if err := out.Write([]string{
				record.Scope.ExamID.String(),
				csvSafe(page.ExamTitles[record.Scope.ExamID]),
				record.Scope.SittingID.String(),
				record.Scope.SubmissionID.String(),
				string(record.Category),
				string(item.Eligibility.Blocker),
				item.Eligibility.EligibleAt.FormatRFC3339(),
				strconv.FormatBool(record.Held),
				strconv.FormatBool(record.CompletionCurrent),
				record.RetiredAt.FormatRFC3339(),
				strconv.FormatInt(page.PolicyRevision, 10),
				model.FormatRFC3339(page.AsOf),
			}); err != nil {
				return 0, apperror.New("retention.unavailable").Wrap(err)
			}
		}
		if !page.HasMore {
			break
		}
		if after <= previous {
			return 0, apperror.New("retention.unavailable")
		}
	}
	out.Flush()
	if err := out.Error(); err != nil {
		return 0, apperror.New("retention.unavailable").Wrap(err)
	}
	return submissions, nil
}

// csvSafe keeps a title a spreadsheet would read as a formula from running
// as one.
func csvSafe(value string) string {
	if value != "" && (value[0] == '=' || value[0] == '+' || value[0] == '-' || value[0] == '@' || value[0] == '\t' || value[0] == '\r') {
		return "'" + value
	}
	return value
}
