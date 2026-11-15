// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamAttemptSubmissionAuthorizationResolvesCanonicalExamBeforeCurrentManagerCheck(t *testing.T) {
	t.Parallel()

	submissionID, examID := model.NewSubmissionID(), model.NewExamID()
	sittings := &submissionAuthorizationSittingFake{}
	adapter := SittingManagerAuthorizer{
		Sittings: sittings,
		Audit:    &submissionAuthorizationAuditFake{},
		Submissions: &submissionAuthorizationStoreFake{authorization: &store.ExamSubmissionAuthorization{
			SubmissionID: submissionID, ExamID: examID, SittingID: model.NewExamSittingID(),
			AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(), AcademicUnitID: model.NewAcademicUnitID(),
		}},
	}
	principal := examAttemptPrincipal()
	call := invocation.New(principal, model.RequestMetadata{RequestID: "submission-view"})
	require.NoError(t, adapter.AuthorizeSubmissionView(context.Background(), call, submissionID))
	require.Equal(t, examID, sittings.examID, "authorization call = exam")
	require.Equal(t, submissionID, sittings.submissionID, "authorization call = exam")
	require.Equal(t, principal.UserID, sittings.inv.Principal().UserID, "authorization call = exam")
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
		adapter := SittingManagerAuthorizer{Sittings: &submissionAuthorizationSittingFake{},
			Submissions: &submissionAuthorizationStoreFake{authorization: authorization}, Audit: audit}
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
		adapter := SittingManagerAuthorizer{Sittings: &submissionAuthorizationSittingFake{},
			Submissions: &submissionAuthorizationStoreFake{authorization: authorization},
			Audit:       &submissionAuthorizationAuditFake{err: auditErr}}
		require.ErrorIs(t, adapter.AuthorizeSubmissionView(context.Background(), invocation.New(principal, metadata), submissionID), auditErr, "AuthorizeSubmissionView() error")
	})
	t.Run("projection of another Submission is refused before self-denial", func(t *testing.T) {
		t.Parallel()
		other := *authorization
		other.SubmissionID = model.NewSubmissionID()
		audit := &submissionAuthorizationAuditFake{}
		sittings := &submissionAuthorizationSittingFake{}
		adapter := SittingManagerAuthorizer{Sittings: sittings,
			Submissions: &submissionAuthorizationStoreFake{authorization: &other}, Audit: audit}
		err := adapter.AuthorizeSubmissionView(context.Background(), invocation.New(principal, metadata), submissionID)
		var fault *apperror.Error
		require.ErrorAs(t, err, &fault, "AuthorizeSubmissionView() error")
		require.Equal(t, "exam.attempt.unavailable", fault.Code(), "AuthorizeSubmissionView() error")
		require.Empty(t, audit.action, "a decision was recorded on another Submission's projection")
		require.True(t, sittings.submissionID.IsZero(), "the Manager decision was asked on another Submission's projection")
	})
}

type submissionAuthorizationStoreFake struct {
	store.ExamSubmissionStore
	authorization *store.ExamSubmissionAuthorization
	err           error
}

func (f *submissionAuthorizationStoreFake) ResolveAttempt(context.Context, model.ExamAttemptID) (*store.ExamSubmissionAuthorization, error) {
	return f.authorization, f.err
}

func (f *submissionAuthorizationStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return f.authorization, nil
}

type submissionAuthorizationSittingFake struct {
	SittingAuthorizer
	inv          invocation.Invocation
	examID       model.ExamID
	submissionID model.SubmissionID
	manageErr    error
	reviewErr    error
	reviewed     bool
}

func (f *submissionAuthorizationSittingFake) AuthorizeSittingManage(context.Context, invocation.Invocation, model.ExamSittingID) (access.ManagerAuthorization, error) {
	return access.ManagerAuthorization{}, f.manageErr
}

func (f *submissionAuthorizationSittingFake) AuthorizeSubmissionReview(_ context.Context, _ invocation.Invocation,
	examID model.ExamID, submissionID model.SubmissionID,
) (access.ManagerAuthorization, error) {
	f.reviewed, f.examID, f.submissionID = true, examID, submissionID
	return access.ManagerAuthorization{}, f.reviewErr
}

func (f *submissionAuthorizationSittingFake) AuthorizeSubmissionView(_ context.Context, inv invocation.Invocation,
	examID model.ExamID, submissionID model.SubmissionID,
) error {
	f.inv, f.examID, f.submissionID = inv, examID, submissionID
	return nil
}

func examAttemptPrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)}
}

func TestScreenClipsAreForTheSittingsManagersAndTheSubmissionsReviewers(t *testing.T) {
	t.Parallel()
	principal := examAttemptPrincipal()
	call := invocation.New(principal, model.RequestMetadata{RequestID: "clip-view"})
	sittingID, attemptID, unitID := model.NewExamSittingID(), model.NewExamAttemptID(), model.NewAcademicUnitID()
	submission := &store.ExamSubmissionAuthorization{SubmissionID: model.NewSubmissionID(), ExamID: model.NewExamID(),
		SittingID: sittingID, AttemptID: attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: unitID}
	denied := apperror.New("authorization.denied")

	manager := SittingManagerAuthorizer{Sittings: &submissionAuthorizationSittingFake{},
		Submissions: &submissionAuthorizationStoreFake{authorization: submission}}
	viewer, err := manager.AuthorizeScreenClipView(context.Background(), call, sittingID, attemptID)
	require.NoError(t, err)
	require.Equal(t, model.ActionExamSittingManage, viewer.Action)
	require.Equal(t, model.RoleScopeClass, viewer.ScopeType)

	sittings := &submissionAuthorizationSittingFake{manageErr: denied}
	reviewer := SittingManagerAuthorizer{Sittings: sittings, Submissions: &submissionAuthorizationStoreFake{authorization: submission}}
	viewer, err = reviewer.AuthorizeScreenClipView(context.Background(), call, sittingID, attemptID)
	require.NoError(t, err, "a reviewer of the Submission sees its clips")
	require.Equal(t, model.ActionSubmissionReview, viewer.Action)
	require.Equal(t, model.Resource{Type: model.ResourceSubmission, ID: submission.SubmissionID.String()}, viewer.Resource)
	require.Equal(t, unitID.String(), viewer.ScopeID)
	require.Equal(t, submission.SubmissionID, sittings.submissionID)

	for name, adapter := range map[string]SittingManagerAuthorizer{
		"not submitted": {Sittings: &submissionAuthorizationSittingFake{manageErr: denied},
			Submissions: &submissionAuthorizationStoreFake{err: store.NewErrNotFound("exam_submission", attemptID.String())}},
		"not a reviewer": {Sittings: &submissionAuthorizationSittingFake{manageErr: denied, reviewErr: denied},
			Submissions: &submissionAuthorizationStoreFake{authorization: submission}},
		"another Sitting's Submission": {Sittings: &submissionAuthorizationSittingFake{manageErr: denied},
			Submissions: &submissionAuthorizationStoreFake{authorization: &store.ExamSubmissionAuthorization{
				SubmissionID: submission.SubmissionID, SittingID: model.NewExamSittingID(), AttemptID: attemptID}}},
	} {
		_, err = adapter.AuthorizeScreenClipView(context.Background(), call, sittingID, attemptID)
		require.ErrorIs(t, err, denied, name)
	}

	own := *submission
	own.CandidateUserID = principal.UserID
	sittings = &submissionAuthorizationSittingFake{manageErr: denied}
	_, err = SittingManagerAuthorizer{Sittings: sittings, Submissions: &submissionAuthorizationStoreFake{authorization: &own}}.
		AuthorizeScreenClipView(context.Background(), call, sittingID, attemptID)
	require.ErrorIs(t, err, denied, "the candidate never sees their own clips")
	require.False(t, sittings.reviewed, "the candidate is refused before any review decision")
}
