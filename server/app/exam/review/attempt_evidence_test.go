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

type capabilityReviewStore struct {
	store.ExamIntegrityReviewStore
	page  *store.AttemptEvidencePage
	calls int
}

func (s *capabilityReviewStore) ListAttemptEvidence(context.Context, store.AttemptEvidenceListOptions) (*store.AttemptEvidencePage, error) {
	s.calls++
	return s.page, nil
}

func testAttemptEvidence(t *testing.T) model.AttemptEvidence {
	t.Helper()
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	value := model.AttemptEvidence{ID: model.NewIntegrityEvidenceID(), AttemptID: model.NewExamAttemptID(),
		Kind: model.AttemptEvidenceProcess, Author: model.NewEnvironmentWorkspaceAuthor(0, ""), Sequence: 1,
		Detail: json.RawMessage(`{"command":"make"}`), ObservedAt: at, RecordedAt: at}
	require.NoError(t, value.Validate())
	return value
}

func TestAttemptEvidenceRequiresCurrentDetailedReviewAuthority(t *testing.T) {
	f := newReviewFixture(t)
	record := testAttemptEvidence(t)
	p := &capabilityReviewStore{ExamIntegrityReviewStore: f.persistence,
		page: &store.AttemptEvidencePage{Items: []model.AttemptEvidence{record}}}
	f.service.reviews = p
	query := store.AttemptEvidenceListOptions{SubmissionID: f.submissionID, Limit: 100}

	_, err := f.service.ListAttemptEvidence(context.Background(), outsiderInvocation(f), query)
	// A refused caller cannot tell the Submission exists: the denial is
	// concealed as not found.
	require.True(t, apperror.Is(err, "resource.not_found"), "error = %v", err)
	require.Equal(t, 0, p.calls, "private evidence read before authorization")

	page, err := f.service.ListAttemptEvidence(context.Background(), f.inv, query)
	require.NoError(t, err)
	require.Equal(t, []model.AttemptEvidence{record}, page.Items)
	page.Items[0].Detail[2] = 'X'
	require.JSONEq(t, `{"command":"make"}`, string(record.Detail), "the page is a copy")

	query.Limit = store.AttemptEvidenceReviewMaximum + 1
	_, err = f.service.ListAttemptEvidence(context.Background(), f.inv, query)
	require.Error(t, err, "unbounded review read")
	require.Equal(t, 1, p.calls, "unbounded review read")
}

func TestAttemptEvidenceRejectsAnInvalidPage(t *testing.T) {
	f := newReviewFixture(t)
	record := testAttemptEvidence(t)
	native := record
	native.Kind = "native.display_topology"
	for name, page := range map[string]*store.AttemptEvidencePage{
		"missing":      nil,
		"native kind":  {Items: []model.AttemptEvidence{native}},
		"out of order": {Items: []model.AttemptEvidence{record, record}},
		"short more":   {Items: []model.AttemptEvidence{record}, HasMore: true},
	} {
		t.Run(name, func(t *testing.T) {
			f.service.reviews = &capabilityReviewStore{ExamIntegrityReviewStore: f.persistence, page: page}
			_, err := f.service.ListAttemptEvidence(context.Background(), f.inv,
				store.AttemptEvidenceListOptions{SubmissionID: f.submissionID, Limit: 2})
			require.Error(t, err)
		})
	}
}
