// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AssistantTranscriptQuery pages the assistant transcript sealed with a
// submission, after a record sequence.
type AssistantTranscriptQuery struct {
	SubmissionID  model.SubmissionID
	AfterSequence int64
	Limit         int
}

// ListAssistantTranscript returns one page of a sealed submission's
// assistant transcript, authorized as attempt evidence is. It is shown as a
// timeline beside the candidate's work; nothing in it raises a flag.
func (s *Service) ListAssistantTranscript(ctx context.Context, inv invocation.Invocation,
	query AssistantTranscriptQuery) (_ *store.AssistantTranscriptPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if query.Limit < 1 || query.Limit > 200 || query.AfterSequence < 0 {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "assistant_transcript_list")
	}
	if err := s.authorizeAssistantTranscript(ctx, inv, query.SubmissionID); err != nil {
		return nil, err
	}
	return s.readAssistantTranscriptPage(ctx, query)
}

func (s *Service) authorizeAssistantTranscript(ctx context.Context, inv invocation.Invocation, id model.SubmissionID) error {
	if !id.IsValid() {
		return apperror.Invalid("exam.integrity_review.invalid", "assistant_transcript_list")
	}
	if s.transcripts == nil {
		return apperror.New("exam.integrity_review.unavailable")
	}
	return s.authorizer.AuthorizeSubmissionView(ctx, inv, id)
}

func (s *Service) readAssistantTranscriptPage(ctx context.Context, query AssistantTranscriptQuery) (*store.AssistantTranscriptPage, error) {
	page, err := s.transcripts.ListForAttempt(ctx, store.AssistantTranscriptQuery{ReviewSubmissionID: query.SubmissionID,
		AfterSequence: query.AfterSequence, Limit: query.Limit})
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if page == nil || len(page.Records) > query.Limit ||
		(page.NextSequence != 0 && (page.NextSequence <= query.AfterSequence || page.NextSequence > model.AssistantTranscriptMaximumRecords)) {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid assistant transcript page"))
	}
	return page, nil
}

// SummarizeAssistantUsage reads a sealed transcript whole and describes how
// the assistant was used. The summary is descriptive: a person judges it.
func (s *Service) SummarizeAssistantUsage(ctx context.Context, inv invocation.Invocation,
	submissionID model.SubmissionID) (_ model.AssistantUsageSummary, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := s.authorizeAssistantTranscript(ctx, inv, submissionID); err != nil {
		return model.AssistantUsageSummary{}, err
	}
	var records []model.AssistantRecord
	var inputCount int64
	query := AssistantTranscriptQuery{SubmissionID: submissionID, Limit: 200}
	for {
		page, err := s.readAssistantTranscriptPage(ctx, query)
		if err != nil {
			return model.AssistantUsageSummary{}, err
		}
		records = append(records, page.Records...)
		if page.NextSequence == 0 {
			inputCount = page.Inventory.SessionInputs
			break
		}
		query.AfterSequence = page.NextSequence
	}
	summary := model.SummarizeAssistantTranscript(records)
	// A host write may have succeeded even if its output could not be read.
	summary.SessionInputs = inputCount
	return summary, nil
}
