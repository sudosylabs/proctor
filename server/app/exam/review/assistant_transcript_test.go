// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// sealedTranscript pages a fixed transcript as the store would, recording
// which submission each read named.
type sealedTranscript struct {
	store.AssistantTranscriptStore
	records []model.AssistantRecord
	queries []store.AssistantTranscriptQuery
}

func (s *sealedTranscript) ListForAttempt(_ context.Context, query store.AssistantTranscriptQuery) (*store.AssistantTranscriptPage, error) {
	s.queries = append(s.queries, query)
	page := &store.AssistantTranscriptPage{Inventory: model.NewAssistantInventory()}
	for _, record := range s.records {
		if record.Sequence <= query.AfterSequence {
			continue
		}
		if len(page.Records) == query.Limit {
			page.NextSequence = page.Records[len(page.Records)-1].Sequence
			break
		}
		page.Records = append(page.Records, record)
	}
	return page, nil
}

func TestAssistantTranscriptRequiresCurrentReviewAuthorityAndPagesWholeForTheSummary(t *testing.T) {
	f := newReviewFixture(t)
	conversation, turn := model.NewAssistantConversationID(), model.NewAssistantTurnID()
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	transcript := &sealedTranscript{}
	for index := range 250 {
		record := model.AssistantRecord{Sequence: int64(index + 1), CreatedAt: at, Call: &model.AssistantCall{ConversationID: conversation,
			TurnID: turn, CallID: model.NewAssistantCallID(), Tool: "read_file", Phase: "requested", Arguments: json.RawMessage(`{}`)}}
		transcript.records = append(transcript.records, record)
	}
	f.service.transcripts = transcript

	_, err := f.service.ListAssistantTranscript(context.Background(), outsiderInvocation(f),
		AssistantTranscriptQuery{SubmissionID: f.submissionID, Limit: 50})
	require.True(t, apperror.Is(err, "resource.not_found"), "a refused caller cannot tell the Submission exists: %v", err)
	require.Empty(t, transcript.queries, "the transcript was read before authorization")

	_, err = f.service.ListAssistantTranscript(context.Background(), f.inv, AssistantTranscriptQuery{SubmissionID: f.submissionID, Limit: 201})
	require.True(t, apperror.Is(err, "exam.integrity_review.invalid"))

	decisionsBefore := len(f.decisions(model.ActionSubmissionView))
	summary, err := f.service.SummarizeAssistantUsage(context.Background(), f.inv, f.submissionID)
	require.NoError(t, err)
	require.Len(t, f.decisions(model.ActionSubmissionView), decisionsBefore+1)
	require.Equal(t, int64(250), summary.Tools["read_file"])
	require.Len(t, transcript.queries, 2, "the summary pages the whole transcript")
	require.Equal(t, int64(200), transcript.queries[1].AfterSequence)
	for _, query := range transcript.queries {
		require.Equal(t, f.submissionID, query.ReviewSubmissionID)
		require.Nil(t, query.Owner)
	}
}
