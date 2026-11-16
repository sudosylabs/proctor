// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
package review

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// outsiderInvocation is a signed-in User who neither manages the fixture's
// Exam nor holds any Submission action, so the real Sitting service refuses
// them every Submission read.
func outsiderInvocation(f *reviewFixture) invocation.Invocation {
	return invocation.New(model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: f.at}, model.RequestMetadata{})
}

type nativeReviewStore struct {
	store.ExamIntegrityReviewStore
	calls int
}

func (s *nativeReviewStore) ListNativeConditions(context.Context, store.NativeConditionListOptions) (*store.NativeConditionPage, error) {
	s.calls++
	return &store.NativeConditionPage{Items: []model.NativeConditionEvidence{}}, nil
}
func TestNativeConditionsRequireCurrentDetailedReviewAuthority(t *testing.T) {
	f := newReviewFixture(t)
	p := &nativeReviewStore{ExamIntegrityReviewStore: f.persistence}
	f.service.reviews = p
	query := store.NativeConditionListOptions{SubmissionID: f.submissionID, Limit: 100}
	_, err := f.service.ListNativeConditions(context.Background(), outsiderInvocation(f), query)
	// A refused caller cannot tell the Submission exists: the denial is
	// concealed as not found.
	require.True(t, apperror.Is(err, "resource.not_found"), "error = %v", err)
	require.Equal(t, 0, p.calls, "private conditions read before authorization")
	page, err := f.service.ListNativeConditions(context.Background(), f.inv, query)
	require.NoError(t, err, "authorized condition read failed")
	require.NotNil(t, page.Items, "authorized condition read failed")
	require.Equal(t, 1, p.calls, "authorized condition read failed")
	// The outsider was refused on the override Action, then the manager was
	// allowed on the ordinary one.
	require.Equal(t, []string{"fail"}, f.decisions(model.ActionSubmissionViewOverride), "authorized condition read failed")
	require.Equal(t, []string{"success"}, f.decisions(model.ActionSubmissionView), "authorized condition read failed")
	query.Limit = 101
	_, err = f.service.ListNativeConditions(context.Background(), f.inv, query)
	require.Error(t, err, "unbounded review read")
	require.Equal(t, 1, p.calls, "unbounded review read")
}
