// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type userProfileResponse struct {
	ID                      string  `json:"id"`
	CreatedAt               string  `json:"created_at"`
	UpdatedAt               string  `json:"updated_at"`
	ArchivedAt              string  `json:"archived_at,omitempty"`
	Username                string  `json:"username"`
	Email                   *string `json:"email,omitempty"`
	EmailVerified           *bool   `json:"email_verified,omitempty"`
	DisplayName             string  `json:"display_name"`
	FirstName               string  `json:"first_name"`
	LastName                string  `json:"last_name"`
	Locale                  *string `json:"locale,omitempty"`
	Timezone                *string `json:"timezone,omitempty"`
	LastLoginAt             string  `json:"last_login_at,omitempty"`
	LastActivityAt          string  `json:"last_activity_at,omitempty"`
	DisabledAt              string  `json:"disabled_at,omitempty"`
	ProfilePictureURL       string  `json:"profile_picture_url"`
	ProfilePictureChangedAt string  `json:"profile_picture_changed_at,omitempty"`
	ProfilePictureKind      string  `json:"profile_picture_kind"`
}

// userDirectoryEntryResponse is one User directory item: the profile the
// caller may read and its bounded summary.
type userDirectoryEntryResponse struct {
	userProfileResponse
	Summary *userDirectorySummaryResponse `json:"summary,omitempty"`
}

// userDirectorySummaryResponse names a User's current Affiliations and, for
// a caller holding role_binding.view, the visible current Role Bindings.
type userDirectorySummaryResponse struct {
	Affiliations     []userDirectoryAffiliationResponse `json:"affiliations"`
	AffiliationsMore int                                `json:"affiliations_more"`
	*userDirectoryRolesResponse
}

type userDirectoryAffiliationResponse struct {
	Kind              string                              `json:"kind"`
	AcademicUnits     []userDirectoryAcademicUnitResponse `json:"academic_units"`
	AcademicUnitsMore int                                 `json:"academic_units_more"`
}

type userDirectoryAcademicUnitResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// userDirectoryRolesResponse is present only when the caller holds
// role_binding.view somewhere.
type userDirectoryRolesResponse struct {
	Roles     []userDirectoryRoleResponse `json:"roles"`
	RolesMore int                         `json:"roles_more"`
}

type userDirectoryRoleResponse struct {
	RoleID      string `json:"role_id"`
	DisplayName string `json:"display_name"`
	ScopeType   string `json:"scope_type"`
	ScopeID     string `json:"scope_id"`
	ScopeName   string `json:"scope_name"`
}

type updateUserProfileRequest struct {
	Username    *string `json:"username,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	FirstName   *string `json:"first_name,omitempty"`
	LastName    *string `json:"last_name,omitempty"`
	Locale      *string `json:"locale,omitempty"`
	Timezone    *string `json:"timezone,omitempty"`
}

type changeUserEmailRequest struct {
	Email string `json:"email"`
}

type userEmailStateResponse struct {
	ID            string `json:"id"`
	EmailVerified bool   `json:"email_verified"`
}

type currentUserContextResponse struct {
	User                         currentUserContextUserResponse     `json:"user"`
	NoCurrentAffiliation         bool                               `json:"no_current_affiliation"`
	NoAssignedAccess             bool                               `json:"no_assigned_access"`
	AvailableProductAreas        []store.CurrentUserProductArea     `json:"available_product_areas"`
	ManagementScopes             []currentUserContextScopeResponse  `json:"management_scopes"`
	ManagementScopesHasMore      bool                               `json:"management_scopes_has_more"`
	UnresolvedAttempt            *currentUserContextAttemptResponse `json:"unresolved_attempt"`
	SessionManagementAvailable   bool                               `json:"session_management_available"`
	CurrentDesktopRegistrationID *string                            `json:"current_desktop_registration_id"`
}

type currentUserContextScopeResponse struct {
	ScopeType   string `json:"scope_type"`
	ScopeID     string `json:"scope_id"`
	DisplayName string `json:"display_name"`
}

type currentUserContextAttemptResponse struct {
	ID        string                 `json:"id"`
	SittingID string                 `json:"exam_sitting_id"`
	State     model.ExamAttemptState `json:"state"`
}

type currentUserContextUserResponse struct {
	ID                      string                   `json:"id"`
	Username                string                   `json:"username"`
	DisplayName             string                   `json:"display_name"`
	ProfilePictureReference string                   `json:"profile_picture_reference"`
	ProfilePictureKind      model.ProfilePictureKind `json:"profile_picture_kind"`
}

const (
	candidateExamActivityCursorKind = "candidate_exam_activity"
)

type candidateExamActivityCursor struct {
	Kind             string `json:"kind"`
	ScheduledStartAt string `json:"scheduled_start_at"`
	ExamSittingID    string `json:"exam_sitting_id"`
}

type candidateExamActivityResponse struct {
	ServerTime string                              `json:"server_time"`
	Items      []candidateExamActivityItemResponse `json:"items"`
	NextCursor string                              `json:"next_cursor,omitempty"`
}

type candidateExamActivityItemResponse struct {
	ExamID            string                                   `json:"exam_id"`
	ExamSittingID     string                                   `json:"exam_sitting_id"`
	Title             string                                   `json:"title"`
	AcademicUnit      candidateExamActivityRelationResponse    `json:"academic_unit"`
	Class             candidateExamActivityRelationResponse    `json:"class"`
	ScheduledStartAt  string                                   `json:"scheduled_start_at"`
	ScheduledEndAt    string                                   `json:"scheduled_end_at"`
	SittingState      model.ExamSittingState                   `json:"sitting_state"`
	SittingReasonCode *string                                  `json:"sitting_reason_code"`
	ActivityState     store.CandidateExamActivityState         `json:"activity_state"`
	AccessState       store.CandidateExamAccessState           `json:"access_state"`
	AllowedActions    []store.CandidateExamAllowedAction       `json:"allowed_actions"`
	Attempt           *candidateExamActivityAttemptResponse    `json:"attempt"`
	Submission        *candidateExamActivitySubmissionResponse `json:"submission"`
	Result            *candidateExamActivityResultResponse     `json:"result"`
}

type candidateExamActivityRelationResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type candidateExamActivityAttemptResponse struct {
	ID                   string                 `json:"id"`
	State                model.ExamAttemptState `json:"state"`
	SuspensionReasonCode *string                `json:"suspension_reason_code"`
}

type candidateExamActivitySubmissionResponse struct {
	ID          string                         `json:"id"`
	SubmittedAt string                         `json:"submitted_at"`
	Provenance  model.ExamSubmissionProvenance `json:"provenance"`
}

type candidateExamActivityResultResponse struct {
	ReleasedAt string `json:"released_at"`
}

type userProfileHandlers struct {
	profiles UserProfileApplication
	accounts UserAccountApplication
	emails   UserEmailApplication
}

func userProfileResource(profiles UserProfileApplication, accounts UserAccountApplication, emails UserEmailApplication) route.Resource {
	h := userProfileHandlers{profiles: profiles, accounts: accounts, emails: emails}
	userPath := "/users/{user_id}"
	picture := "/users/{user_id}/profile-picture"
	email := "/users/{user_id}/email"
	return route.NewResource(
		"user-profiles",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"user.invalid",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.search,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/me",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"resource.not_found",
				"administration.unavailable",
				"request.invalid",
			},
			Handle: h.current,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/me/context",
			Auth:   route.AuthSessionRequired,
			Errors: currentUserContextSessionCodes("administration.unavailable"),
			Handle: h.context,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/me/exam-activity",
			Auth:   route.AuthSessionRequired,
			Errors: []string{"request.invalid", "exam.attempt.unavailable", "exam.attempt.invalid", "exam.delivery.metadata_capacity"},
			Handle: h.examActivity,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   userPath,
			Auth:   route.AuthPrincipalRequired,
			Errors: userProfileReadCodes("request.invalid", "resource.not_found"),
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPatch,
			Path:   userPath,
			Auth:   route.AuthPrincipalRequired,
			Errors: userProfileMutationCodes("request.invalid", "resource.not_found", "user.invalid", "user.conflict"),
			Handle: h.update,
		},
		route.Endpoint{
			Method: http.MethodPut,
			Path:   email,
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: userProfileMutationCodes(
				"request.invalid",
				"resource.not_found",
				"user.invalid",
				"user.conflict",
				"authentication.account_recovery.unavailable",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
			),
			Handle: h.changeEmail,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/{user_id}/email/verify",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: userProfileMutationCodes("request.invalid", "resource.not_found", "user.conflict", "authentication.account_recovery.unavailable"),
			Handle: h.verifyEmail,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         picture,
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "profile-picture-download",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"service.busy",
				"request.invalid",
				"resource.not_found",
				"profile_picture.unavailable",
				"administration.unavailable",
			},
			Protocol: h.downloadPicture,
		},
		route.Endpoint{
			Method:       http.MethodPut,
			Path:         picture,
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "profile-picture-upload",
			ProtocolKind: route.ProtocolStreamingUpload,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"service.busy",
				"request.invalid",
				"resource.not_found",
				"profile_picture.invalid",
				"profile_picture.unavailable",
				"user.conflict",
				"administration.unavailable",
			},
			Protocol: h.uploadPicture,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   picture,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"profile_picture.unavailable",
				"user.conflict",
				"administration.unavailable",
			},
			Handle: h.removePicture,
		},
	)
}

func (h userProfileHandlers) examActivity(request route.Request) (route.Result, error) {
	query := attempt.CandidateActivityQuery{Limit: 50}
	values := request.HTTP.URL.Query()
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 200"))
		}

		query.Limit = limit
	}

	if raw := values.Get("cursor"); raw != "" {
		cursor, err := route.DecodeCursor(raw, candidateExamActivityCursorSpec())
		if err != nil {
			return route.Result{}, route.InvalidRequest(err)
		}

		query.BeforeScheduledStart, _ = model.ParseRFC3339(cursor.ScheduledStartAt)
		query.BeforeSittingID = model.ExamSittingID(cursor.ExamSittingID)
	}

	page, err := h.accounts.ListCandidateExamActivity(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := candidateExamActivityResponse{ServerTime: model.FormatRFC3339(page.ServerTime),
		Items: make([]candidateExamActivityItemResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		mapped := candidateExamActivityItemResponse{
			ExamID:           item.ExamID.String(),
			ExamSittingID:    item.SittingID.String(),
			Title:            item.Title,
			AcademicUnit:     candidateExamActivityRelationResponse{ID: item.AcademicUnitID.String(), DisplayName: item.AcademicUnitDisplayName},
			Class:            candidateExamActivityRelationResponse{ID: item.ClassID.String(), DisplayName: item.ClassDisplayName},
			ScheduledStartAt: model.FormatRFC3339(item.ScheduledStartAt),
			ScheduledEndAt:   model.FormatRFC3339(item.ScheduledEndAt),
			SittingState:     item.SittingState,
			ActivityState:    item.ActivityState,
			AccessState:      item.AccessState,
			AllowedActions:   append([]store.CandidateExamAllowedAction(nil), item.AllowedActions...),
		}
		if item.SittingReasonCode != "" {
			value := item.SittingReasonCode
			mapped.SittingReasonCode = &value
		}

		if item.Attempt != nil {
			mapped.Attempt = &candidateExamActivityAttemptResponse{ID: item.Attempt.ID.String(), State: item.Attempt.State}
			if item.Attempt.SuspensionReasonCode != "" {
				value := string(item.Attempt.SuspensionReasonCode)
				mapped.Attempt.SuspensionReasonCode = &value
			}
		}

		if item.Submission != nil {
			mapped.Submission = &candidateExamActivitySubmissionResponse{
				ID:          item.Submission.ID.String(),
				SubmittedAt: model.FormatRFC3339(item.Submission.SubmittedAt),
				Provenance:  item.Submission.Provenance,
			}
		}

		if item.Result != nil {
			mapped.Result = &candidateExamActivityResultResponse{ReleasedAt: model.FormatRFC3339(item.Result.ReleasedAt)}
		}

		response.Items = append(response.Items, mapped)
	}

	if page.HasMore {
		if len(page.Items) == 0 {
			return route.Result{}, route.ErrUnpresentable
		}

		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = route.EncodeCursor(candidateExamActivityCursor{
			Kind:             candidateExamActivityCursorKind,
			ScheduledStartAt: model.FormatRFC3339(last.ScheduledStartAt),
			ExamSittingID:    last.SittingID.String(),
		}, candidateExamActivityCursorSpec())
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func candidateExamActivityCursorSpec() route.CursorSpec[candidateExamActivityCursor] {
	return route.CursorSpec[candidateExamActivityCursor]{Label: "candidate Exam activity",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"kind", "scheduled_start_at", "exam_sitting_id"},
		Validate: func(cursor candidateExamActivityCursor) error {
			at, err := model.ParseRFC3339(cursor.ScheduledStartAt)
			if cursor.Kind != candidateExamActivityCursorKind ||
				err != nil ||
				at.IsZero() ||
				!model.ExamSittingID(cursor.ExamSittingID).IsValid() {
				return errors.New("invalid candidate Exam activity keyset")
			}

			return nil
		}}
}

func currentUserContextSessionCodes(extra ...string) []string {
	return append([]string(nil), extra...)
}

func (h userProfileHandlers) context(request route.Request) (route.Result, error) {
	view, err := h.profiles.GetCurrentUserContext(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}

	response := currentUserContextResponse{
		User: currentUserContextUserResponse{
			ID:                      view.UserID.String(),
			Username:                view.Username,
			DisplayName:             view.DisplayName,
			ProfilePictureReference: view.ProfilePictureReference,
			ProfilePictureKind:      view.ProfilePictureKind,
		},
		NoCurrentAffiliation:       view.NoCurrentAffiliation,
		NoAssignedAccess:           view.NoAssignedAccess,
		AvailableProductAreas:      append([]store.CurrentUserProductArea(nil), view.AvailableProductAreas...),
		ManagementScopesHasMore:    view.ManagementScopesHasMore,
		SessionManagementAvailable: view.SessionManagementAvailable,
		ManagementScopes:           make([]currentUserContextScopeResponse, 0, len(view.ManagementScopes)),
	}
	for _, scope := range view.ManagementScopes {
		response.ManagementScopes = append(response.ManagementScopes, currentUserContextScopeResponse{
			ScopeType:   string(scope.ScopeType),
			ScopeID:     scope.ScopeID,
			DisplayName: scope.DisplayName,
		})
	}

	if view.UnresolvedAttempt != nil {
		response.UnresolvedAttempt = &currentUserContextAttemptResponse{
			ID:        view.UnresolvedAttempt.AttemptID.String(),
			SittingID: view.UnresolvedAttempt.SittingID.String(),
			State:     view.UnresolvedAttempt.State,
		}
	}

	if view.CurrentDesktopRegistrationID.IsValid() {
		id := view.CurrentDesktopRegistrationID.String()
		response.CurrentDesktopRegistrationID = &id
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h userProfileHandlers) changeEmail(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}

	var body changeUserEmailRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	state, appErr := h.emails.ChangeUserEmail(
		request.Context,
		request.Invocation(),
		identity.ChangeUserEmailCommand{UserID: userID, Email: body.Email},
	)
	if appErr != nil {
		return route.Result{}, appErr
	}

	return route.JSON(http.StatusOK, userEmailStateResponseFromApplication(state)), nil
}

func (h userProfileHandlers) verifyEmail(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}

	state, appErr := h.emails.VerifyUserEmailPrivileged(
		request.Context,
		request.Invocation(),
		userID,
	)
	if appErr != nil {
		return route.Result{}, appErr
	}

	return route.JSON(http.StatusOK, userEmailStateResponseFromApplication(state)), nil
}

func userEmailStateResponseFromApplication(state *identity.UserEmailState) userEmailStateResponse {
	if state == nil {
		return userEmailStateResponse{}
	}
	return userEmailStateResponse{ID: state.UserID.String(), EmailVerified: state.EmailVerified}
}

func userProfileReadCodes(extra ...string) []string {
	return append(userProfilePrincipalCodes(extra...), "administration.unavailable")
}

func userProfilePrincipalCodes(extra ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
	}
	return append(codes, extra...)
}

func userProfileMutationCodes(extra ...string) []string {
	return append(userProfilePrincipalMutationCodes(extra...), "administration.unavailable")
}

func userProfilePrincipalMutationCodes(extra ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"audit.unavailable",
	}
	return append(codes, extra...)
}

func (h userProfileHandlers) uploadPicture(request route.Request) (route.ProtocolResult, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}

	expectedSHA256, err := profilePictureIfMatch(request.HTTP.Header.Get("If-Match"), false)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}

	account, err := h.profiles.UploadProfilePicture(request.Context, request.Invocation(), user.UploadProfilePictureCommand{
		UserID:         userID,
		ExpectedSHA256: expectedSHA256,
		Body:           request.HTTP.Body,
		Size:           request.HTTP.ContentLength,
	})
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusOK, userProfileResponseFromModel(account)), nil
}

func (h userProfileHandlers) removePicture(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}

	expectedSHA256, err := profilePictureIfMatch(request.HTTP.Header.Get("If-Match"), true)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	account, appErr := h.profiles.RemoveProfilePicture(
		request.Context,
		request.Invocation(),
		user.RemoveProfilePictureCommand{UserID: userID, ExpectedSHA256: expectedSHA256},
	)
	if appErr != nil {
		return route.Result{}, appErr
	}

	return route.JSON(http.StatusOK, userProfileResponseFromModel(account)), nil
}

func profilePictureIfMatch(header string, required bool) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" && !required {
		return "", nil
	}
	if len(header) != 66 || header[0] != '"' || header[len(header)-1] != '"' {
		return "", fmt.Errorf("a single strong profile-picture ETag is required")
	}
	checksum := header[1 : len(header)-1]
	if strings.ToLower(checksum) != checksum {
		return "", fmt.Errorf("profile-picture ETag is not canonical")
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return "", fmt.Errorf("profile-picture ETag is invalid: %w", err)
	}
	return checksum, nil
}

func (h userProfileHandlers) downloadPicture(request route.Request) (route.ProtocolResult, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}

	size, err := strconv.Atoi(defaultQuery(request.HTTP, "size", "256"))
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}

	content, appErr := h.profiles.GetProfilePicture(
		request.Context,
		request.Invocation(),
		user.GetProfilePictureQuery{UserID: userID, Size: size},
	)
	if appErr != nil {
		return route.ProtocolResult{}, appErr
	}

	headers := http.Header{
		"ETag":          []string{content.ETag},
		"Cache-Control": []string{"private, max-age=86400"},
		"Content-Type":  []string{content.MediaType},
	}
	if etagMatches(request.HTTP.Header.Get("If-None-Match"), content.ETag) {
		_ = content.Body.Close()
		return route.NotModified(content.Size).WithHeaders(headers), nil
	}

	return route.BinaryDownload(content.Body, content.Size).WithHeaders(headers), nil
}

func etagMatches(header, current string) bool {
	current = strings.TrimPrefix(strings.TrimSpace(current), "W/")
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == current {
			return true
		}
	}
	return false
}

func (h userProfileHandlers) search(request route.Request) (route.Result, error) {
	requested, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}

	cursor, present, err := route.DecodePageCursor(requested, userDirectoryCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	includeDisabled, err := strconv.ParseBool(defaultQuery(request.HTTP, "include_disabled", "false"))
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	order, err := parseListOrder(request.HTTP.URL.Query(), userDirectorySorts, listAscending)
	if err != nil {
		return route.Result{}, err
	}

	query := user.SearchUsersQuery{
		Query:           request.HTTP.URL.Query().Get("q"),
		Order:           store.UserOrder(order.Sort),
		Descending:      order.Descending(),
		Limit:           requested.Limit,
		IncludeDisabled: includeDisabled,
	}
	if query.Affiliations, query.AcademicUnitID, err = userDirectoryFilters(request.HTTP.URL.Query()); err != nil {
		return route.Result{}, err
	}
	if err = userDirectoryAccountFilters(request.HTTP.URL.Query(), &query); err != nil {
		return route.Result{}, err
	}
	if present {
		if !order.continues(cursor.Sort, cursor.Dir) {
			return route.Result{}, route.InvalidRequest(errors.New("cursor belongs to another order"))
		}
		cursor.continueQuery(&query)
	}

	page, err := h.profiles.SearchUsers(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	envelope, err := route.NewList(
		page.Items,
		page.HasMore,
		userDirectoryEntryResponseFromApplication,
		func(last user.UserDirectoryEntry) (string, error) {
			return route.EncodeCursor(newUserDirectoryCursor(order, last.User), userDirectoryCursorSpec())
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.WithPageTotal(envelope, page.Total, present)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

// userDirectoryEntryResponseFromApplication presents one directory row. Each
// Affiliation carries the User's visible current Academic Units, since a
// membership is not tied to one Affiliation kind.
func userDirectoryEntryResponseFromApplication(entry user.UserDirectoryEntry) userDirectoryEntryResponse {
	units := make([]userDirectoryAcademicUnitResponse, 0, len(entry.Memberships.AcademicUnits))
	for _, unit := range entry.Memberships.AcademicUnits {
		units = append(units, userDirectoryAcademicUnitResponse{ID: unit.ID.String(), Name: unit.DisplayName})
	}

	summary := &userDirectorySummaryResponse{
		Affiliations:     make([]userDirectoryAffiliationResponse, 0, len(entry.Memberships.Affiliations)),
		AffiliationsMore: entry.Memberships.AffiliationsMore,
	}
	for _, kind := range entry.Memberships.Affiliations {
		summary.Affiliations = append(summary.Affiliations, userDirectoryAffiliationResponse{
			Kind:              string(kind),
			AcademicUnits:     units,
			AcademicUnitsMore: entry.Memberships.AcademicUnitsMore,
		})
	}

	if entry.Roles != nil {
		summary.userDirectoryRolesResponse = &userDirectoryRolesResponse{
			Roles:     make([]userDirectoryRoleResponse, 0, len(entry.Roles.Bindings)),
			RolesMore: entry.Roles.More,
		}
		for _, binding := range entry.Roles.Bindings {
			summary.Roles = append(summary.Roles, userDirectoryRoleResponse{
				RoleID:      binding.RoleID.String(),
				DisplayName: binding.DisplayName,
				ScopeType:   string(binding.ScopeType),
				ScopeID:     binding.ScopeID,
				ScopeName:   binding.ScopeName,
			})
		}
	}

	return userDirectoryEntryResponse{userProfileResponse: userProfileResponseFromModel(entry.User), Summary: summary}
}

// userDirectoryFilters reads the repeatable affiliation filter and the single
// academic_unit_id filter of the User directory.
func userDirectoryFilters(values url.Values) ([]model.AffiliationKind, model.AcademicUnitID, error) {
	var kinds []model.AffiliationKind
	for _, raw := range values["affiliation"] {
		kind := model.AffiliationKind(raw)
		if !kind.IsValid() || slices.Contains(kinds, kind) {
			return nil, "", apperror.Invalid("request.invalid", "affiliation")
		}
		kinds = append(kinds, kind)
	}
	raw, present := values["academic_unit_id"]
	if !present {
		return kinds, "", nil
	}
	if len(raw) != 1 {
		return nil, "", apperror.Invalid("request.invalid", "academic_unit_id")
	}
	unitID, err := model.ParseAcademicUnitID(raw[0])
	if err != nil {
		return nil, "", apperror.Invalid("request.invalid", "academic_unit_id")
	}
	return kinds, unitID, nil
}

// userDirectoryAccountFilters reads the repeatable role_id filter and the
// single state, never_signed_in, last_active_after and last_active_before
// filters. The application decides which of them the caller may apply.
func userDirectoryAccountFilters(values url.Values, query *user.SearchUsersQuery) error {
	for _, raw := range values["role_id"] {
		id, err := model.ParseRoleID(raw)
		if err != nil {
			return apperror.Invalid("request.invalid", "role_id")
		}
		query.RoleIDs = append(query.RoleIDs, id)
	}
	single := func(name string) (string, error) {
		raw, present := values[name]
		if !present {
			return "", nil
		}
		if len(raw) != 1 || raw[0] == "" {
			return "", apperror.Invalid("request.invalid", name)
		}
		return raw[0], nil
	}
	state, err := single("state")
	if err != nil {
		return err
	}
	query.State = user.AccountState(state)
	if state != "" && !query.State.IsValid() {
		return apperror.Invalid("request.invalid", "state")
	}
	never, err := single("never_signed_in")
	if err != nil {
		return err
	}
	if never != "" {
		if query.NeverSignedIn, err = strconv.ParseBool(never); err != nil {
			return apperror.Invalid("request.invalid", "never_signed_in")
		}
	}
	for _, bound := range []struct {
		name   string
		target *time.Time
	}{{"last_active_after", &query.LastActiveAfter}, {"last_active_before", &query.LastActiveBefore}} {
		name := bound.name
		raw, err := single(name)
		if err != nil {
			return err
		}
		if raw == "" {
			continue
		}
		at, err := model.ParseRFC3339(raw)
		if err != nil || at.IsZero() {
			return apperror.Invalid("request.invalid", name)
		}
		*bound.target = at
	}
	return nil
}

// userDirectorySorts is the User directory's closed sort set; username, the
// default, keeps the directory's historical order.
var userDirectorySorts = []string{
	string(store.UserOrderUsername),
	string(store.UserOrderName),
	string(store.UserOrderCreated),
	string(store.UserOrderLastActive),
}

// userDirectoryCursor continues the User directory in one order. Key is the
// last User's key in that order: the username, or the RFC 3339 creation or
// last-activity instant; it is empty for the name order, which the Store
// resumes from the User itself, and for a User who was never active.
type userDirectoryCursor struct {
	Sort string `json:"sort"`
	Dir  string `json:"dir"`
	Key  string `json:"key,omitempty"`
	ID   string `json:"id"`
}

func newUserDirectoryCursor(order listOrder, last *model.User) userDirectoryCursor {
	cursor := userDirectoryCursor{Sort: order.Sort, Dir: order.Dir, ID: last.ID.String()}
	switch store.UserOrder(order.Sort) {
	case store.UserOrderUsername:
		cursor.Key = last.Username
	case store.UserOrderCreated:
		cursor.Key = model.FormatRFC3339(last.CreatedAt)
	case store.UserOrderLastActive:
		cursor.Key = last.LastActivityAt.FormatRFC3339()
	}
	return cursor
}

// continueQuery sets the query's keyset from a validated cursor.
func (c userDirectoryCursor) continueQuery(query *user.SearchUsersQuery) {
	query.AfterID = model.UserID(c.ID)
	switch store.UserOrder(c.Sort) {
	case store.UserOrderUsername:
		query.AfterUsername = c.Key
	case store.UserOrderCreated:
		query.AfterCreatedAt, _ = model.ParseRFC3339(c.Key)
	case store.UserOrderLastActive:
		if c.Key != "" {
			at, _ := model.ParseRFC3339(c.Key)
			query.AfterLastActivityAt = model.OptionalTimeFrom(at)
		}
	}
}

func userDirectoryCursorSpec() route.CursorSpec[userDirectoryCursor] {
	return route.CursorSpec[userDirectoryCursor]{
		Label:                "User directory",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"sort", "dir", "key", "id"},
		Validate: func(cursor userDirectoryCursor) error {
			if !validListDirection(cursor.Dir) || !model.UserID(cursor.ID).IsValid() || !validUserDirectoryKey(cursor.Sort, cursor.Key) {
				return errors.New("invalid User directory keyset")
			}
			return nil
		},
	}
}

// validUserDirectoryKey reports whether key is a well-formed key of sort.
func validUserDirectoryKey(sort, key string) bool {
	switch store.UserOrder(sort) {
	case store.UserOrderUsername:
		return key != "" && len(key) <= 64
	case store.UserOrderName:
		return key == ""
	case store.UserOrderCreated:
		at, err := model.ParseRFC3339(key)
		return err == nil && !at.IsZero()
	case store.UserOrderLastActive:
		if key == "" {
			return true
		}
		at, err := model.ParseRFC3339(key)
		return err == nil && !at.IsZero()
	}
	return false
}

func (h userProfileHandlers) current(request route.Request) (route.Result, error) {
	account, err := h.profiles.GetUserProfile(request.Context, request.Invocation(), request.Principal.UserID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, userProfileResponseFromModel(account)), nil
}

func (h userProfileHandlers) get(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}
	account, err := h.profiles.GetUserProfile(request.Context, request.Invocation(), userID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, userProfileResponseFromModel(account)), nil
}

func (h userProfileHandlers) update(request route.Request) (route.Result, error) {
	userID, err := route.PathID[model.UserID](request, "user_id")
	if err != nil {
		return route.Result{}, err
	}

	var body updateUserProfileRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	account, err := h.profiles.UpdateUserProfile(request.Context, request.Invocation(), user.UpdateUserProfileCommand{
		ID:          userID,
		Username:    body.Username,
		DisplayName: body.DisplayName,
		FirstName:   body.FirstName,
		LastName:    body.LastName,
		Locale:      body.Locale,
		Timezone:    body.Timezone,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, userProfileResponseFromModel(account)), nil
}

func userProfileResponseFromModel(account *model.User) userProfileResponse {
	if account == nil {
		return userProfileResponse{}
	}

	pictureURL := "/api/v1/users/" + account.ID.String() + "/profile-picture"
	if account.ProfilePictureChangedAt.Valid {
		pictureURL += "?v=" + strconv.FormatInt(account.Revision, 10)
	}

	response := userProfileResponse{
		ID:                      account.ID.String(),
		CreatedAt:               model.FormatRFC3339(account.CreatedAt),
		UpdatedAt:               model.FormatRFC3339(account.UpdatedAt),
		ArchivedAt:              account.ArchivedAt.FormatRFC3339(),
		Username:                account.Username,
		DisplayName:             account.DisplayName,
		FirstName:               account.FirstName,
		LastName:                account.LastName,
		LastLoginAt:             account.LastLoginAt.FormatRFC3339(),
		LastActivityAt:          account.LastActivityAt.FormatRFC3339(),
		DisabledAt:              account.DisabledAt.FormatRFC3339(),
		ProfilePictureURL:       pictureURL,
		ProfilePictureChangedAt: account.ProfilePictureChangedAt.FormatRFC3339(),
		ProfilePictureKind:      string(account.ProfilePictureKind()),
	}
	if account.Email != "" {
		response.Email = &account.Email
		response.EmailVerified = &account.EmailVerified
		response.Locale = &account.Locale
		response.Timezone = &account.Timezone
	}

	return response
}

// UserProfileApplication is the User directory, profile and profile-picture
// use cases of the user area.
type UserProfileApplication interface {
	GetCurrentUserContext(context.Context, invocation.Invocation) (*user.CurrentUserContextView, error)
	SearchUsers(context.Context, invocation.Invocation, user.SearchUsersQuery) (paging.Counted[user.UserDirectoryEntry], error)
	GetUserProfile(context.Context, invocation.Invocation, model.UserID) (*model.User, error)
	UpdateUserProfile(context.Context, invocation.Invocation, user.UpdateUserProfileCommand) (*model.User, error)
	UploadProfilePicture(context.Context, invocation.Invocation, user.UploadProfilePictureCommand) (*model.User, error)
	RemoveProfilePicture(context.Context, invocation.Invocation, user.RemoveProfilePictureCommand) (*model.User, error)
	GetProfilePicture(context.Context, invocation.Invocation, user.GetProfilePictureQuery) (*user.ProfilePictureContent, error)
}

// UserAccountApplication is the User use case that stays on the application
// root: a candidate's own exam activity.
type UserAccountApplication interface {
	ListCandidateExamActivity(context.Context, invocation.Invocation, attempt.CandidateActivityQuery) (attempt.CandidateActivityPage, error)
}

// UserEmailApplication is the email address changes of the identity area.
type UserEmailApplication interface {
	ChangeUserEmail(context.Context, invocation.Invocation, identity.ChangeUserEmailCommand) (*identity.UserEmailState, error)
	VerifyUserEmailPrivileged(
		context.Context,
		invocation.Invocation,
		model.UserID,
	) (*identity.UserEmailState, error)
}
