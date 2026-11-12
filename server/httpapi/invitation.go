// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// InvitationApplication is the application port its handlers call; it issues
// and accepts Invitations of each purpose and lists, resends, revokes,
// replaces and batches them.
type InvitationApplication interface {
	IssueStudentClassInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.IssueStudentClassInvitationCommand,
	) (onboarding.InvitationView, error)
	AcceptStudentClassInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.AcceptStudentClassInvitationCommand,
	) (*onboarding.InvitationAcceptanceView, error)
	IssueTeacherAcademicUnitInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.IssueTeacherAcademicUnitInvitationCommand,
	) (onboarding.InvitationView, error)
	AcceptTeacherAcademicUnitInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.AcceptTeacherAcademicUnitInvitationCommand,
	) (*onboarding.InvitationAcceptanceView, error)
	IssueAcademicUnitRoleInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.IssueAcademicUnitRoleInvitationCommand,
	) (onboarding.InvitationView, error)
	AcceptAcademicUnitRoleInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.AcceptAcademicUnitRoleInvitationCommand,
	) (*onboarding.InvitationAcceptanceView, error)
	IssueInstitutionRoleInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.IssueInstitutionRoleInvitationCommand,
	) (onboarding.InvitationView, error)
	AcceptInstitutionRoleInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.AcceptInstitutionRoleInvitationCommand,
	) (*onboarding.InvitationAcceptanceView, error)
	ListInvitations(context.Context, invocation.Invocation, onboarding.ListInvitationsQuery) (onboarding.InvitationAdministrationPage, error)
	GetInvitation(context.Context, invocation.Invocation, model.InvitationID) (onboarding.InvitationAdministrationView, error)
	ResendInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.ResendInvitationCommand,
	) (onboarding.InvitationAdministrationView, error)
	RevokeInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.RevokeInvitationCommand,
	) (onboarding.InvitationAdministrationView, error)
	ReplaceInvitation(
		context.Context,
		invocation.Invocation,
		onboarding.ReplaceInvitationCommand,
	) (onboarding.InvitationAdministrationView, error)
	RunInvitationBatch(context.Context, invocation.Invocation, onboarding.RunInvitationBatchCommand) (onboarding.InvitationBatchResult, error)
}

type issueStudentClassInvitationRequest struct {
	Email                string `json:"email"`
	StartAt              string `json:"start_at,omitempty"`
	EndAt                string `json:"end_at,omitempty"`
	SuggestedUsername    string `json:"suggested_username,omitempty"`
	SuggestedDisplayName string `json:"suggested_display_name,omitempty"`
	SuggestedFirstName   string `json:"suggested_first_name,omitempty"`
	SuggestedLastName    string `json:"suggested_last_name,omitempty"`
	SuggestedLocale      string `json:"suggested_locale,omitempty"`
	SuggestedTimezone    string `json:"suggested_timezone,omitempty"`
	ExpiresInDays        *int   `json:"expires_in_days,omitempty"`
}

type acceptStudentClassInvitationRequest struct {
	Claim       string `json:"claim"`
	Password    string `json:"password"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Locale      string `json:"locale,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

type issueTeacherAcademicUnitInvitationRequest struct {
	Email                string `json:"email"`
	RoleID               string `json:"role_id"`
	StartAt              string `json:"start_at,omitempty"`
	EndAt                string `json:"end_at,omitempty"`
	SuggestedUsername    string `json:"suggested_username,omitempty"`
	SuggestedDisplayName string `json:"suggested_display_name,omitempty"`
	SuggestedFirstName   string `json:"suggested_first_name,omitempty"`
	SuggestedLastName    string `json:"suggested_last_name,omitempty"`
	SuggestedLocale      string `json:"suggested_locale,omitempty"`
	SuggestedTimezone    string `json:"suggested_timezone,omitempty"`
	ExpiresInDays        *int   `json:"expires_in_days,omitempty"`
}

type acceptTeacherAcademicUnitInvitationRequest acceptStudentClassInvitationRequest

type issueScopedRoleInvitationRequest struct {
	Email         string `json:"email"`
	RoleID        string `json:"role_id"`
	StartAt       string `json:"start_at,omitempty"`
	EndAt         string `json:"end_at,omitempty"`
	ExpiresInDays *int   `json:"expires_in_days,omitempty"`
}

type acceptScopedRoleInvitationRequest struct {
	Claim string `json:"claim"`
}

type invitationResponse struct {
	ID               string   `json:"id"`
	Purpose          string   `json:"purpose"`
	State            string   `json:"state"`
	ClassID          string   `json:"class_id,omitempty"`
	AcademicPeriodID string   `json:"academic_period_id,omitempty"`
	AcademicUnitID   string   `json:"academic_unit_id,omitempty"`
	RoleID           string   `json:"role_id,omitempty"`
	RoleActions      []string `json:"role_actions,omitempty"`
	StartAt          string   `json:"start_at"`
	EndAt            string   `json:"end_at,omitempty"`
	ExpiresAt        string   `json:"expires_at"`
}

type invitationAcceptanceResponse struct {
	UserID               string `json:"user_id"`
	InvitationID         string `json:"invitation_id"`
	AffiliationID        string `json:"affiliation_id,omitempty"`
	ClassMemberID        string `json:"class_member_id,omitempty"`
	AcademicUnitMemberID string `json:"academic_unit_member_id,omitempty"`
	RoleBindingID        string `json:"role_binding_id,omitempty"`
	Replayed             bool   `json:"replayed"`
}

type invitationMutationRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

type replaceInvitationRequest struct {
	ExpectedRevision     int64  `json:"expected_revision"`
	Purpose              string `json:"purpose"`
	Email                string `json:"email"`
	ClassID              string `json:"class_id,omitempty"`
	AcademicUnitID       string `json:"academic_unit_id,omitempty"`
	InstitutionID        string `json:"institution_id,omitempty"`
	RoleID               string `json:"role_id,omitempty"`
	StartAt              string `json:"start_at,omitempty"`
	EndAt                string `json:"end_at,omitempty"`
	SuggestedUsername    string `json:"suggested_username,omitempty"`
	SuggestedDisplayName string `json:"suggested_display_name,omitempty"`
	SuggestedFirstName   string `json:"suggested_first_name,omitempty"`
	SuggestedLastName    string `json:"suggested_last_name,omitempty"`
	SuggestedLocale      string `json:"suggested_locale,omitempty"`
	SuggestedTimezone    string `json:"suggested_timezone,omitempty"`
	ExpiresInDays        *int   `json:"expires_in_days,omitempty"`
}

type invitationBatchItemRequest struct {
	Key                  string `json:"key"`
	InvitationID         string `json:"invitation_id,omitempty"`
	ExpectedRevision     int64  `json:"expected_revision,omitempty"`
	Email                string `json:"email,omitempty"`
	RoleID               string `json:"role_id,omitempty"`
	StartAt              string `json:"start_at,omitempty"`
	EndAt                string `json:"end_at,omitempty"`
	SuggestedUsername    string `json:"suggested_username,omitempty"`
	SuggestedDisplayName string `json:"suggested_display_name,omitempty"`
	SuggestedFirstName   string `json:"suggested_first_name,omitempty"`
	SuggestedLastName    string `json:"suggested_last_name,omitempty"`
	SuggestedLocale      string `json:"suggested_locale,omitempty"`
	SuggestedTimezone    string `json:"suggested_timezone,omitempty"`
}

type invitationBatchRequest struct {
	Operation string                       `json:"operation"`
	ScopeType string                       `json:"scope_type"`
	ScopeID   string                       `json:"scope_id"`
	Items     []invitationBatchItemRequest `json:"items"`
}

type invitationBatchItemResponse struct {
	Index        int    `json:"index"`
	Status       string `json:"status"`
	InvitationID string `json:"invitation_id,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
}

type invitationBatchResponse struct {
	Operation string                        `json:"operation"`
	Items     []invitationBatchItemResponse `json:"items"`
	Succeeded int                           `json:"succeeded"`
	NoOp      int                           `json:"no_op"`
	Failed    int                           `json:"failed"`
}

type invitationDeliveryResponse struct {
	TemplateKey       string `json:"template_key"`
	State             string `json:"state"`
	MaskedRecipient   string `json:"masked_recipient"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	Deadline          string `json:"deadline"`
	AcceptedAt        string `json:"accepted_at,omitempty"`
	PublicFailureCode string `json:"public_failure_code,omitempty"`
}

type invitationAdministrationResponse struct {
	ID               string                      `json:"id"`
	Purpose          string                      `json:"purpose"`
	State            string                      `json:"state"`
	ClassID          string                      `json:"class_id,omitempty"`
	AcademicPeriodID string                      `json:"academic_period_id,omitempty"`
	AcademicUnitID   string                      `json:"academic_unit_id,omitempty"`
	RoleID           string                      `json:"role_id,omitempty"`
	RoleActions      []string                    `json:"role_actions,omitempty"`
	StartAt          string                      `json:"start_at"`
	EndAt            string                      `json:"end_at,omitempty"`
	ExpiresAt        string                      `json:"expires_at"`
	Email            string                      `json:"email"`
	InviterUserID    string                      `json:"inviter_user_id"`
	AcceptedUserID   string                      `json:"accepted_user_id,omitempty"`
	CreatedAt        string                      `json:"created_at"`
	UpdatedAt        string                      `json:"updated_at"`
	Revision         int64                       `json:"revision"`
	Delivery         *invitationDeliveryResponse `json:"delivery,omitempty"`
}

// invitationListResponse is the collection envelope; the first page also
// carries the total (see route.CountedList).
type invitationListResponse struct {
	Items             []invitationAdministrationResponse `json:"items"`
	NextCursor        string                             `json:"next_cursor,omitempty"`
	Total             *int                               `json:"total,omitempty"`
	TotalIsLowerBound *bool                              `json:"total_is_lower_bound,omitempty"`
}

type invitationCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

type invitationHandlers struct{ app InvitationApplication }

func invitationResource(app InvitationApplication) route.Resource {
	h := invitationHandlers{app: app}
	return route.NewResource("invitations",
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/invitation-batches",
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"authentication.strong_required",
				"authentication.reauthentication_required",
				"invitation.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
			},
			Handle: h.batch,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/invitations",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.query.invalid",
				"invitation.unavailable",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/invitations/{invitation_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.unavailable",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/invitations/{invitation_id}/resend",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.conflict",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"authentication.reauthentication_required",
				"authentication.strong_required",
				"onboarding_batch.duplicate",
			},
			Handle: h.resend,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/invitations/{invitation_id}/revoke",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.conflict",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"authentication.reauthentication_required",
				"authentication.strong_required",
				"onboarding_batch.duplicate",
			},
			Handle: h.revoke,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/invitations/{invitation_id}/replacement",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.invalid",
				"invitation.role_not_delegable",
				"invitation.conflict",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"authentication.strong_required",
				"authentication.reauthentication_required",
			},
			Handle: h.replace,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/classes/{class_id}/invitations/student",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.invalid",
				"invitation.class_period_conflict",
				"invitation.conflict",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"onboarding_batch.duplicate",
			},
			Handle: h.issueStudentClass,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/invitations/student-class/accept",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"invitation.user_invalid",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"authentication.password.invalid",
				"authentication.password.pattern_invalid",
				"authentication.password.common_invalid",
				"authentication.password.identity_invalid",
				"service.busy",
			},
			Handle: h.acceptStudentClass,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/academic-units/{academic_unit_id}/invitations/teacher",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.invalid",
				"invitation.role_not_delegable",
				"invitation.conflict",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"onboarding_batch.duplicate",
			},
			Handle: h.issueTeacherAcademicUnit,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/invitations/teacher-academic-unit/accept",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"invitation.user_invalid",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"authentication.password.invalid",
				"authentication.password.pattern_invalid",
				"authentication.password.common_invalid",
				"authentication.password.identity_invalid",
				"service.busy",
			},
			Handle: h.acceptTeacherAcademicUnit,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/academic-units/{academic_unit_id}/invitations/role",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.invalid",
				"invitation.role_not_delegable",
				"invitation.conflict",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"onboarding_batch.duplicate",
			},
			Handle: h.issueAcademicUnitRole,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/invitations/academic-unit-role/accept",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"audit.unavailable",
				"request.invalid",
			},
			Handle: h.acceptAcademicUnitRole,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/institutions/{institution_id}/invitations/role",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.invalid",
				"invitation.role_not_delegable",
				"invitation.conflict",
				"invitation.mail_unavailable",
				"invitation.unavailable",
				"onboarding_batch.duplicate",
			},
			Handle: h.issueInstitutionRole,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/invitations/institution-role/accept",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"invitation.invalid",
				"audit.unavailable",
				"request.invalid",
			},
			Handle: h.acceptInstitutionRole,
		},
	)
}

func (h invitationHandlers) batch(request route.Request) (route.Result, error) {
	var body invitationBatchRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	items := make([]onboarding.InvitationBatchItemCommand, 0, len(body.Items))
	for _, item := range body.Items {
		startAt, endAt, err := route.WireWindowMillis(item.StartAt, item.EndAt)
		if err != nil {
			return route.Result{}, err
		}

		items = append(items, onboarding.InvitationBatchItemCommand{
			IdempotencyKey:       item.Key,
			InvitationID:         model.InvitationID(item.InvitationID),
			ExpectedRevision:     item.ExpectedRevision,
			TargetEmail:          item.Email,
			RoleID:               model.RoleID(item.RoleID),
			IntendedStartsAt:     startAt,
			IntendedEndsAt:       endAt,
			SuggestedUsername:    item.SuggestedUsername,
			SuggestedDisplayName: item.SuggestedDisplayName,
			SuggestedFirstName:   item.SuggestedFirstName,
			SuggestedLastName:    item.SuggestedLastName,
			SuggestedLocale:      item.SuggestedLocale,
			SuggestedTimezone:    item.SuggestedTimezone,
		})
	}

	result, err := h.app.RunInvitationBatch(request.Context, request.Invocation(), onboarding.RunInvitationBatchCommand{
		Operation:      onboarding.InvitationBatchOperation(body.Operation),
		ScopeType:      model.RoleScopeType(body.ScopeType),
		ScopeID:        body.ScopeID,
		IdempotencyKey: request.IdempotencyKey,
		Items:          items,
	})
	if err != nil {
		return route.Result{}, err
	}

	response := invitationBatchResponse{
		Operation: string(result.Operation),
		Items:     make([]invitationBatchItemResponse, 0, len(result.Items)),
		Succeeded: result.Succeeded,
		NoOp:      result.NoOp,
		Failed:    result.Failed,
	}
	for _, item := range result.Items {
		response.Items = append(response.Items, invitationBatchItemResponse{
			Index:        item.Index,
			Status:       string(item.Status),
			InvitationID: item.InvitationID.String(),
			ErrorCode:    item.ErrorCode,
		})
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h invitationHandlers) list(request route.Request) (route.Result, error) {
	query, err := invitationListQuery(request.HTTP)
	if err != nil {
		return route.Result{}, apperror.Invalid("request.invalid", "query").Wrap(err)
	}

	page, err := h.app.ListInvitations(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := invitationListResponse{Items: make([]invitationAdministrationResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, invitationAdministrationResponseFromView(item))
	}

	if page.More && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("invitation page made no progress"))
	}

	if page.More {
		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = encodeInvitationCursor(invitationCursor{
			CreatedAt: model.FormatRFC3339(last.CreatedAt),
			ID:        last.ID.String(),
		})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	if response.Total, response.TotalIsLowerBound, err = route.PageTotal(page.Total, query.BeforeID.IsValid()); err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h invitationHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.InvitationID](request, "invitation_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.GetInvitation(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, invitationAdministrationResponseFromView(view)).WithHeaders(noStoreHeaders()), nil
}

func (h invitationHandlers) resend(request route.Request) (route.Result, error) {
	return h.mutate(request, func(id model.InvitationID, revision int64) (onboarding.InvitationAdministrationView, error) {
		return h.app.ResendInvitation(
			request.Context,
			request.Invocation(),
			onboarding.ResendInvitationCommand{ID: id, ExpectedRevision: revision},
		)
	})
}

func (h invitationHandlers) revoke(request route.Request) (route.Result, error) {
	return h.mutate(request, func(id model.InvitationID, revision int64) (onboarding.InvitationAdministrationView, error) {
		return h.app.RevokeInvitation(
			request.Context,
			request.Invocation(),
			onboarding.RevokeInvitationCommand{ID: id, ExpectedRevision: revision},
		)
	})
}

func (h invitationHandlers) mutate(
	request route.Request,
	mutation func(model.InvitationID, int64) (onboarding.InvitationAdministrationView, error),
) (route.Result, error) {
	id, err := route.PathID[model.InvitationID](request, "invitation_id")
	if err != nil {
		return route.Result{}, err
	}

	var body invitationMutationRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	view, err := mutation(id, body.ExpectedRevision)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, invitationAdministrationResponseFromView(view)).WithHeaders(noStoreHeaders()), nil
}

func (h invitationHandlers) replace(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.InvitationID](request, "invitation_id")
	if err != nil {
		return route.Result{}, err
	}

	var body replaceInvitationRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	startAt, endAt, err := route.WireWindowMillis(body.StartAt, body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	view, err := h.app.ReplaceInvitation(request.Context, request.Invocation(), onboarding.ReplaceInvitationCommand{
		ID:                   id,
		ExpectedRevision:     body.ExpectedRevision,
		Purpose:              body.Purpose,
		TargetEmail:          body.Email,
		ClassID:              model.ClassID(body.ClassID),
		AcademicUnitID:       model.AcademicUnitID(body.AcademicUnitID),
		InstitutionID:        model.InstitutionID(body.InstitutionID),
		RoleID:               model.RoleID(body.RoleID),
		IntendedStartsAt:     startAt,
		IntendedEndsAt:       endAt,
		SuggestedUsername:    body.SuggestedUsername,
		SuggestedDisplayName: body.SuggestedDisplayName,
		SuggestedFirstName:   body.SuggestedFirstName,
		SuggestedLastName:    body.SuggestedLastName,
		SuggestedLocale:      body.SuggestedLocale,
		SuggestedTimezone:    body.SuggestedTimezone,
		ExpiresInDays:        body.ExpiresInDays,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, invitationAdministrationResponseFromView(view)).WithHeaders(noStoreHeaders()), nil
}

func invitationListQuery(request *http.Request) (onboarding.ListInvitationsQuery, error) {
	page, err := route.ParsePage(request)
	if err != nil {
		return onboarding.ListInvitationsQuery{}, err
	}
	values := request.URL.Query()
	query := onboarding.ListInvitationsQuery{Limit: page.Limit, TargetEmail: values.Get("email"), TargetID: values.Get("target_id")}

	if raw := values.Get("purpose"); raw != "" {
		query.Purpose = model.InvitationPurpose(raw)
		if !query.Purpose.IsValid() {
			return query, errors.New("invalid Invitation purpose")
		}
	}

	if raw := values.Get("state"); raw != "" {
		query.State = model.InvitationState(raw)
		if !query.State.IsValid() {
			return query, errors.New("invalid Invitation state")
		}
	}

	if raw := values.Get("created_after"); raw != "" {
		query.CreatedAfter, err = parseInvitationFilterTime(raw)
		if err != nil {
			return query, err
		}
	}

	if raw := values.Get("created_before"); raw != "" {
		query.CreatedBefore, err = parseInvitationFilterTime(raw)
		if err != nil {
			return query, err
		}
	}

	if !query.CreatedAfter.IsZero() && !query.CreatedBefore.IsZero() && !query.CreatedBefore.After(query.CreatedAfter) {
		return query, errors.New("invalid Invitation time range")
	}

	if raw := values.Get("cursor"); raw != "" {
		cursor, err := decodeInvitationCursor(raw)
		if err != nil {
			return query, err
		}

		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.InvitationID(cursor.ID)
	}

	return query, nil
}

func parseInvitationFilterTime(raw string) (time.Time, error) {
	parsed, err := model.ParseRFC3339(raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("invalid Invitation time")
	}
	return parsed, nil
}

func encodeInvitationCursor(cursor invitationCursor) (string, error) {
	return route.EncodeCursor(cursor, invitationCursorSpec())
}

func decodeInvitationCursor(raw string) (invitationCursor, error) {
	return route.DecodeCursor(raw, invitationCursorSpec())
}

func invitationCursorSpec() route.CursorSpec[invitationCursor] {
	return route.CursorSpec[invitationCursor]{
		Label:                "Invitation",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"created_at", "id"},
		Validate: func(cursor invitationCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || !model.InvitationID(cursor.ID).IsValid() {
				return errors.New("invalid Invitation keyset")
			}

			return nil
		},
	}
}

func (h invitationHandlers) issueAcademicUnitRole(request route.Request) (route.Result, error) {
	unitID, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body issueScopedRoleInvitationRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	startAt, endAt, err := route.WireWindowMillis(body.StartAt, body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	created, err := h.app.IssueAcademicUnitRoleInvitation(
		request.Context,
		request.Invocation(),
		onboarding.IssueAcademicUnitRoleInvitationCommand{
			TargetEmail:      body.Email,
			AcademicUnitID:   unitID,
			RoleID:           model.RoleID(body.RoleID),
			IntendedStartsAt: startAt,
			IntendedEndsAt:   endAt,
			ExpiresInDays:    body.ExpiresInDays,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, invitationResponseFromView(created)), nil
}

func (h invitationHandlers) issueInstitutionRole(request route.Request) (route.Result, error) {
	institutionID, err := route.PathID[model.InstitutionID](request, "institution_id")
	if err != nil {
		return route.Result{}, err
	}

	var body issueScopedRoleInvitationRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	startAt, endAt, err := route.WireWindowMillis(body.StartAt, body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	created, err := h.app.IssueInstitutionRoleInvitation(
		request.Context,
		request.Invocation(),
		onboarding.IssueInstitutionRoleInvitationCommand{
			TargetEmail:      body.Email,
			InstitutionID:    institutionID,
			RoleID:           model.RoleID(body.RoleID),
			IntendedStartsAt: startAt,
			IntendedEndsAt:   endAt,
			ExpiresInDays:    body.ExpiresInDays,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, invitationResponseFromView(created)), nil
}

func (h invitationHandlers) acceptAcademicUnitRole(request route.Request) (route.Result, error) {
	var body acceptScopedRoleInvitationRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	accepted, err := h.app.AcceptAcademicUnitRoleInvitation(
		request.Context,
		request.Invocation(),
		onboarding.AcceptAcademicUnitRoleInvitationCommand{
			Claim:  body.Claim,
			Source: request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, invitationAcceptanceResponseFromView(accepted)), nil
}

func (h invitationHandlers) acceptInstitutionRole(request route.Request) (route.Result, error) {
	var body acceptScopedRoleInvitationRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	accepted, err := h.app.AcceptInstitutionRoleInvitation(
		request.Context,
		request.Invocation(),
		onboarding.AcceptInstitutionRoleInvitationCommand{
			Claim:  body.Claim,
			Source: request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, invitationAcceptanceResponseFromView(accepted)), nil
}

func (h invitationHandlers) issueTeacherAcademicUnit(request route.Request) (route.Result, error) {
	unitID, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body issueTeacherAcademicUnitInvitationRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	startAt, endAt, err := route.WireWindowMillis(body.StartAt, body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	created, err := h.app.IssueTeacherAcademicUnitInvitation(
		request.Context,
		request.Invocation(),
		onboarding.IssueTeacherAcademicUnitInvitationCommand{
			TargetEmail:          body.Email,
			AcademicUnitID:       unitID,
			RoleID:               model.RoleID(body.RoleID),
			IntendedStartsAt:     startAt,
			IntendedEndsAt:       endAt,
			SuggestedUsername:    body.SuggestedUsername,
			SuggestedDisplayName: body.SuggestedDisplayName,
			SuggestedFirstName:   body.SuggestedFirstName,
			SuggestedLastName:    body.SuggestedLastName,
			SuggestedLocale:      body.SuggestedLocale,
			SuggestedTimezone:    body.SuggestedTimezone,
			ExpiresInDays:        body.ExpiresInDays,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, invitationResponseFromView(created)), nil
}

func (h invitationHandlers) acceptTeacherAcademicUnit(request route.Request) (route.Result, error) {
	var body acceptTeacherAcademicUnitInvitationRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	accepted, err := h.app.AcceptTeacherAcademicUnitInvitation(
		request.Context,
		request.Invocation(),
		onboarding.AcceptTeacherAcademicUnitInvitationCommand{
			Claim:       body.Claim,
			Password:    body.Password,
			Username:    body.Username,
			DisplayName: body.DisplayName,
			FirstName:   body.FirstName,
			LastName:    body.LastName,
			Locale:      body.Locale,
			Timezone:    body.Timezone,
			Source:      request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, invitationAcceptanceResponseFromView(accepted)), nil
}

func (h invitationHandlers) issueStudentClass(request route.Request) (route.Result, error) {
	classID, err := route.PathID[model.ClassID](request, "class_id")
	if err != nil {
		return route.Result{}, err
	}

	var body issueStudentClassInvitationRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	startAt, endAt, err := route.WireWindowMillis(body.StartAt, body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	created, err := h.app.IssueStudentClassInvitation(
		request.Context,
		request.Invocation(),
		onboarding.IssueStudentClassInvitationCommand{
			TargetEmail:          body.Email,
			ClassID:              classID,
			IntendedStartsAt:     startAt,
			IntendedEndsAt:       endAt,
			SuggestedUsername:    body.SuggestedUsername,
			SuggestedDisplayName: body.SuggestedDisplayName,
			SuggestedFirstName:   body.SuggestedFirstName,
			SuggestedLastName:    body.SuggestedLastName,
			SuggestedLocale:      body.SuggestedLocale,
			SuggestedTimezone:    body.SuggestedTimezone,
			ExpiresInDays:        body.ExpiresInDays,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, invitationResponseFromView(created)), nil
}

func (h invitationHandlers) acceptStudentClass(request route.Request) (route.Result, error) {
	var body acceptStudentClassInvitationRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	accepted, err := h.app.AcceptStudentClassInvitation(
		request.Context,
		request.Invocation(),
		onboarding.AcceptStudentClassInvitationCommand{
			Claim:       body.Claim,
			Password:    body.Password,
			Username:    body.Username,
			DisplayName: body.DisplayName,
			FirstName:   body.FirstName,
			LastName:    body.LastName,
			Locale:      body.Locale,
			Timezone:    body.Timezone,
			Source:      request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, invitationAcceptanceResponseFromView(accepted)), nil
}

func invitationAcceptanceResponseFromView(accepted *onboarding.InvitationAcceptanceView) invitationAcceptanceResponse {
	response := invitationAcceptanceResponse{Replayed: accepted.Replayed}
	if accepted.User != nil {
		response.UserID = accepted.User.ID.String()
	}
	response.InvitationID = accepted.Invitation.ID.String()
	if accepted.Affiliation != nil {
		response.AffiliationID = accepted.Affiliation.ID.String()
	}
	if accepted.ClassMember != nil {
		response.ClassMemberID = accepted.ClassMember.ID.String()
	}
	if accepted.AcademicUnitMember != nil {
		response.AcademicUnitMemberID = accepted.AcademicUnitMember.ID.String()
	}
	if accepted.RoleBinding != nil {
		response.RoleBindingID = accepted.RoleBinding.ID.String()
	}
	return response
}

func invitationResponseFromView(view onboarding.InvitationView) invitationResponse {
	return invitationResponse{
		ID:               view.ID.String(),
		Purpose:          string(view.Purpose),
		State:            string(view.State),
		ClassID:          view.ClassID.String(),
		AcademicPeriodID: view.AcademicPeriodID.String(),
		AcademicUnitID:   view.AcademicUnitID.String(),
		RoleID:           view.RoleID.String(),
		RoleActions:      append([]string(nil), view.RoleActions...),
		StartAt:          model.FormatRFC3339(view.IntendedStartsAt),
		EndAt:            view.IntendedEndsAt.FormatRFC3339(),
		ExpiresAt:        model.FormatRFC3339(view.ExpiresAt),
	}
}

func invitationAdministrationResponseFromView(view onboarding.InvitationAdministrationView) invitationAdministrationResponse {
	base := invitationResponseFromView(view.InvitationView)
	response := invitationAdministrationResponse{
		ID:               base.ID,
		Purpose:          base.Purpose,
		State:            base.State,
		ClassID:          base.ClassID,
		AcademicPeriodID: base.AcademicPeriodID,
		AcademicUnitID:   base.AcademicUnitID,
		RoleID:           base.RoleID,
		RoleActions:      base.RoleActions,
		StartAt:          base.StartAt,
		EndAt:            base.EndAt,
		ExpiresAt:        base.ExpiresAt,
		Email:            view.TargetEmail,
		InviterUserID:    view.InviterUserID.String(),
		AcceptedUserID:   view.AcceptedUserID.String(),
		CreatedAt:        model.FormatRFC3339(view.CreatedAt),
		UpdatedAt:        model.FormatRFC3339(view.UpdatedAt),
		Revision:         view.Revision,
	}
	if view.Delivery != nil {
		response.Delivery = &invitationDeliveryResponse{
			TemplateKey:       string(view.Delivery.TemplateKey),
			State:             string(view.Delivery.State),
			MaskedRecipient:   view.Delivery.MaskedRecipient,
			CreatedAt:         model.FormatRFC3339(view.Delivery.CreatedAt),
			UpdatedAt:         model.FormatRFC3339(view.Delivery.UpdatedAt),
			Deadline:          model.FormatRFC3339(view.Delivery.Deadline),
			AcceptedAt:        view.Delivery.AcceptedAt.FormatRFC3339(),
			PublicFailureCode: view.Delivery.PublicFailureCode,
		}
	}

	return response
}
