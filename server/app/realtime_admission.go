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
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/model"
)

// RealtimeAdmission admits long-lived WebSocket clients. It lives in app
// rather than app/realtime because it needs identity and exam services that
// realtime cannot import without a cycle. Subscriptions are authorized
// against current state, concealing exams and sittings the caller may not
// see.
type RealtimeAdmission struct {
	principals    *identity.Service
	exams         *exam.Authoring
	sittings      *sitting.Service
	authorization *access.Authorizer
	operational   operational.Recorder
}

// RealtimeAdmissionDependencies may leave Exams and Sittings nil when the
// caller never subscribes to those resources.
type RealtimeAdmissionDependencies struct {
	Principals  *identity.Service
	Exams       *exam.Authoring
	Sittings    *sitting.Service
	Authorizer  *access.Authorizer
	Operational operational.Recorder
}

// NewRealtimeAdmission returns the admission over deps.
func NewRealtimeAdmission(deps RealtimeAdmissionDependencies) *RealtimeAdmission {
	return &RealtimeAdmission{
		principals:    deps.Principals,
		exams:         deps.Exams,
		sittings:      deps.Sittings,
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
)

// AuthorizeWebSocketSubscription uses the relationship gates for exams and
// sittings and the audited access decision for every other resource.
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
