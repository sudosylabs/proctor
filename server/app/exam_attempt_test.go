// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamAttemptSubmissionAuthorizationResolvesCanonicalExamBeforeCurrentManagerCheck(t *testing.T) {
	t.Parallel()

	submissionID, examID := model.NewSubmissionID(), model.NewExamID()
	sittings := &submissionAuthorizationSittingFake{}
	adapter := examAttemptManagerAuthorizationAdapter{
		sittings: sittings,
		audit:    &submissionAuthorizationAuditFake{},
		submissions: &submissionAuthorizationStoreFake{authorization: &store.ExamSubmissionAuthorization{
			SubmissionID: submissionID, ExamID: examID, SittingID: model.NewExamSittingID(),
			AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(), AcademicUnitID: model.NewAcademicUnitID(),
		}},
	}
	principal := examAttemptPrincipal()
	call := invocation.New(principal, model.RequestMetadata{RequestID: "submission-view"})
	require.NoError(t, adapter.AuthorizeSubmissionView(context.Background(), call, submissionID))
	require.Equal(t, examID, sittings.examID, "authorization call = exam")
	require.Equal(t, submissionID, sittings.submissionID, "authorization call = exam")
	require.Equal(t, principal.UserID, sittings.invocation.Principal().UserID, "authorization call = exam")
}

type submissionAuthorizationAuditFake struct {
	principal model.Principal
	action    model.Action
	resource  model.Resource
	scopeType model.RoleScopeType
	scopeID   string
	metadata  model.RequestMetadata
	allowed   bool
	err       error
}

func (f *submissionAuthorizationAuditFake) RecordAuthorizationDecision(_ context.Context, principal model.Principal,
	action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID string,
	metadata model.RequestMetadata, allowed bool,
) error {
	f.principal, f.action, f.resource = principal, action, resource
	f.scopeType, f.scopeID, f.metadata, f.allowed = scopeType, scopeID, metadata, allowed
	return f.err
}

func TestExamAttemptSubmissionAuthorizationAuditsSelfDenialFailClosed(t *testing.T) {
	t.Parallel()
	principal := examAttemptPrincipal()
	submissionID, unitID := model.NewSubmissionID(), model.NewAcademicUnitID()
	authorization := &store.ExamSubmissionAuthorization{
		SubmissionID: submissionID, ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(),
		AttemptID: model.NewExamAttemptID(), CandidateUserID: principal.UserID, AcademicUnitID: unitID,
	}
	metadata := model.RequestMetadata{RequestID: "self-submission-view"}
	t.Run("records denial before concealment", func(t *testing.T) {
		t.Parallel()
		audit := &submissionAuthorizationAuditFake{}
		adapter := examAttemptManagerAuthorizationAdapter{sittings: &submissionAuthorizationSittingFake{},
			submissions: &submissionAuthorizationStoreFake{authorization: authorization}, audit: audit}
		err := adapter.AuthorizeSubmissionView(context.Background(), invocation.New(principal, metadata), submissionID)
		var fault *apperror.Error
		require.ErrorAs(t, err, &fault, "AuthorizeSubmissionView() error")
		require.Equal(t, "exam.attempt.not_found", fault.Code(), "AuthorizeSubmissionView() error")
		require.Equal(t, principal.UserID, audit.principal.UserID)
		require.Equal(t, model.ActionSubmissionView, audit.action)
		require.Equal(t, model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()}, audit.resource)
		require.Equal(t, model.RoleScopeAcademicUnit, audit.scopeType)
		require.Equal(t, unitID.String(), audit.scopeID)
		require.Equal(t, metadata.RequestID, audit.metadata.RequestID)
		require.False(t, audit.allowed)
	})
	t.Run("audit failure fails closed", func(t *testing.T) {
		t.Parallel()
		auditErr := errors.New("audit unavailable")
		adapter := examAttemptManagerAuthorizationAdapter{sittings: &submissionAuthorizationSittingFake{},
			submissions: &submissionAuthorizationStoreFake{authorization: authorization},
			audit:       &submissionAuthorizationAuditFake{err: auditErr}}
		require.ErrorIs(t, adapter.AuthorizeSubmissionView(context.Background(), invocation.New(principal, metadata), submissionID), auditErr, "AuthorizeSubmissionView() error")
	})
}

type submissionAuthorizationStoreFake struct {
	store.ExamSubmissionStore
	authorization *store.ExamSubmissionAuthorization
}

func (f *submissionAuthorizationStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return f.authorization, nil
}

type submissionAuthorizationSittingFake struct {
	examAttemptSittingAuthorizer
	invocation   invocation.Invocation
	examID       model.ExamID
	submissionID model.SubmissionID
}

func (f *submissionAuthorizationSittingFake) AuthorizeSubmissionView(_ context.Context, invocation invocation.Invocation,
	examID model.ExamID, submissionID model.SubmissionID,
) error {
	f.invocation, f.examID, f.submissionID = invocation, examID, submissionID
	return nil
}

func examAttemptPrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)}
}
