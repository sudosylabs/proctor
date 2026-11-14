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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type capabilityReviewStore struct {
	store.ExamIntegrityReviewStore
	page  *store.CapabilityEvidencePage
	calls int
}

func (s *capabilityReviewStore) ListCapabilityEvidence(context.Context, store.CapabilityEvidenceListOptions) (*store.CapabilityEvidencePage, error) {
	s.calls++
	return s.page, nil
}

func testCapabilityEvidence(t *testing.T) model.CapabilityEvidence {
	t.Helper()
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	value := model.CapabilityEvidence{ID: model.NewIntegrityEvidenceID(), AttemptID: model.NewExamAttemptID(),
		ParticipationID: model.NewAttemptParticipationID(), Generation: 1, BindingID: model.NewAttemptEnvironmentBindingID(),
		Kind: "execution.process_log", Sequence: 1, Detail: json.RawMessage(`{"command":"make"}`), ObservedAt: at, RecordedAt: at}
	require.NoError(t, value.Validate())
	return value
}

func TestCapabilityEvidenceRequiresCurrentDetailedReviewAuthority(t *testing.T) {
	f := newReviewFixture(t)
	denied := errors.New("review denied")
	auth := &nativeReviewAuthorizer{Authorizer: f.authorizer, err: denied}
	record := testCapabilityEvidence(t)
	p := &capabilityReviewStore{ExamIntegrityReviewStore: f.persistence,
		page: &store.CapabilityEvidencePage{Items: []model.CapabilityEvidence{record}}}
	f.service.authorizer = auth
	f.service.reviews = p
	query := store.CapabilityEvidenceListOptions{SubmissionID: f.submissionID, Limit: 100}

	_, err := f.service.ListCapabilityEvidence(context.Background(), f.inv, query)
	require.ErrorIs(t, err, denied, "private evidence read before authorization")
	require.Equal(t, 0, p.calls, "private evidence read before authorization")

	auth.err = nil
	page, err := f.service.ListCapabilityEvidence(context.Background(), f.inv, query)
	require.NoError(t, err)
	require.Equal(t, []model.CapabilityEvidence{record}, page.Items)
	page.Items[0].Detail[2] = 'X'
	require.JSONEq(t, `{"command":"make"}`, string(record.Detail), "the page is a copy")

	query.Limit = store.CapabilityEvidenceReviewMaximum + 1
	_, err = f.service.ListCapabilityEvidence(context.Background(), f.inv, query)
	require.Error(t, err, "unbounded review read")
	require.Equal(t, 1, p.calls, "unbounded review read")
}

func TestCapabilityEvidenceRejectsAnInvalidPage(t *testing.T) {
	f := newReviewFixture(t)
	record := testCapabilityEvidence(t)
	native := record
	native.Kind = "native.display_topology"
	for name, page := range map[string]*store.CapabilityEvidencePage{
		"missing":      nil,
		"native kind":  {Items: []model.CapabilityEvidence{native}},
		"out of order": {Items: []model.CapabilityEvidence{record, record}},
		"short more":   {Items: []model.CapabilityEvidence{record}, HasMore: true},
	} {
		t.Run(name, func(t *testing.T) {
			f.service.reviews = &capabilityReviewStore{ExamIntegrityReviewStore: f.persistence, page: page}
			_, err := f.service.ListCapabilityEvidence(context.Background(), f.inv,
				store.CapabilityEvidenceListOptions{SubmissionID: f.submissionID, Limit: 2})
			require.Error(t, err)
		})
	}
}
