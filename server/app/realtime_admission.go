// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/model"
)

// RealtimeAdmission admits long-lived WebSocket clients. It lives in app
// rather than app/realtime because it needs identity and exam services that
// realtime cannot import without a cycle. Subscriptions are authorized
// against current state, concealing exams, sittings, and submissions outside
// the caller's visibility.
type RealtimeAdmission struct {
	principals    *identity.Service
	exams         *exam.Authoring
	sittings      *sitting.Service
	reviews       review.SittingManagerAuthorizer
	authorization *access.Authorizer
	operational   operational.Recorder
}

// RealtimeAdmissionDependencies may omit the exam services when the caller
// never subscribes to their resources.
type RealtimeAdmissionDependencies struct {
	Principals  *identity.Service
	Exams       *exam.Authoring
	Sittings    *sitting.Service
	Reviews     review.SittingManagerAuthorizer
	Authorizer  *access.Authorizer
	Operational operational.Recorder
}

// NewRealtimeAdmission returns the admission over deps.
func NewRealtimeAdmission(deps RealtimeAdmissionDependencies) *RealtimeAdmission {
	return &RealtimeAdmission{
		principals:    deps.Principals,
		exams:         deps.Exams,
		sittings:      deps.Sittings,
		reviews:       deps.Reviews,
		authorization: deps.Authorizer,
		operational:   deps.Operational,
	}
}

var (
	realtimeExamFailures = apperror.Concealment{
		Unavailable: "exam.unavailable",
		Concealed:   []apperror.Code{"exam.not_found"},
	}
	realtimeSittingFailures = apperror.Concealment{
		Unavailable: "exam.sitting.unavailable",
		Concealed:   []apperror.Code{"exam.sitting.not_found"},
	}
	realtimeSubmissionFailures = apperror.Concealment{
		Unavailable: "exam.integrity_review.unavailable",
		Concealed:   []apperror.Code{"exam.integrity_review.not_found"},
	}
)

// AuthorizeWebSocketSubscription uses the relationship gates for exams and
// sittings and submissions, plus the audited access decision for other resources.
func (s *RealtimeAdmission) AuthorizeWebSocketSubscription(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resource model.Resource,
) (resultErr error) {
	defer func() { operational.Record(s.operational, "realtime", "subscription_authorize", resultErr) }()
	definition, ok := model.DefinitionForAction(action)
	if !ok || resource.Validate() != nil || !definition.AcceptsResource(resource.Type) {
		return invalidRealtimeRequest("subscription")
	}
	if action == model.ActionExamView && resource.Type == model.ResourceExam {
		examID, err := model.ParseExamID(resource.ID)
		if err != nil {
			return invalidRealtimeRequest("subscription")
		}
		return realtimeExamFailures.Conceal(s.exams.AuthorizeExamView(ctx, inv, examID))
	}
	if action == model.ActionExamSittingView && resource.Type == model.ResourceExamSitting {
		sittingID, err := model.ParseExamSittingID(resource.ID)
		if err != nil {
			return invalidRealtimeRequest("subscription")
		}
		return realtimeSittingFailures.Conceal(s.sittings.AuthorizeSittingView(ctx, inv, sittingID))
	}
	if resource.Type == model.ResourceExamSitting && (action == model.ActionSubmissionView || action == model.ActionExamSittingManage) {
		sittingID, err := model.ParseExamSittingID(resource.ID)
		if err != nil {
			return invalidRealtimeRequest("subscription")
		}
		if action == model.ActionExamSittingManage {
			// Clip and evidence subscriptions need the same two permissions
			// as the reads they announce.
			if _, err = s.sittings.AuthorizeSittingManage(ctx, inv, sittingID); err != nil {
				return realtimeSittingFailures.Conceal(err)
			}
		}
		return realtimeSittingFailures.Conceal(s.sittings.AuthorizeSittingSubmissionView(ctx, inv, sittingID))
	}
	if action == model.ActionSubmissionView && resource.Type == model.ResourceSubmission {
		submissionID, err := model.ParseSubmissionID(resource.ID)
		if err != nil {
			return invalidRealtimeRequest("subscription")
		}
		return realtimeSubmissionFailures.Conceal(s.reviews.AuthorizeSubmissionView(ctx, inv, submissionID))
	}
	// A User is read through the caller's user visibility, which a
	// unit-scoped grant reaches only through relationships in its subtree.
	if action == model.ActionUserView && resource.Type == model.ResourceUser {
		userID, err := model.ParseUserID(resource.ID)
		if err != nil {
			return invalidRealtimeRequest("subscription")
		}
		err = s.authorization.AuthorizeUserRead(ctx, inv, userID)
		operational.Record(s.operational, "authorization", "decision", err)
		return err
	}
	err := s.authorization.Authorize(ctx, inv, action, resource)
	operational.Record(s.operational, "authorization", "decision", err)
	return err
}

// ValidateWebSocketPrincipal rechecks the session and user behind a
// long-lived connection's principal.
func (s *RealtimeAdmission) ValidateWebSocketPrincipal(ctx context.Context, principal model.Principal) error {
	err := s.principals.ValidatePrincipal(ctx, principal)
	operational.Record(s.operational, "realtime", "principal_validate", err)
	return err
}

func invalidRealtimeRequest(field string) error {
	return apperror.New("websocket.request.invalid").WithField("field", field)
}
