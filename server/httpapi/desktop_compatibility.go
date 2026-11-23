// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type desktopCompatibilityPolicyRequest struct {
	ExpectedRevision      int64                                           `json:"expected_revision"`
	MinimumDesktopRelease route.Optional[string]                          `json:"minimum_desktop_release"`
	RevokedDesktopBuilds  route.Optional[[]desktopBuildRevocationRequest] `json:"revoked_desktop_builds"`
	AdministratorMessage  route.Optional[string]                          `json:"administrator_message"`
	Availability          route.Optional[string]                          `json:"availability"`
	RetryAt               route.Optional[string]                          `json:"retry_at"`
}

type desktopBuildRevocationRequest struct {
	BuildID string `json:"build_id"`
	Reason  string `json:"reason"`
}

type desktopCompatibilityPolicyResponse struct {
	Revision              int64                            `json:"revision"`
	MinimumDesktopRelease string                           `json:"minimum_desktop_release"`
	RevokedDesktopBuilds  []desktopBuildRevocationResponse `json:"revoked_desktop_builds"`
	AdministratorMessage  string                           `json:"administrator_message"`
	Availability          string                           `json:"availability"`
	RetryAt               *string                          `json:"retry_at,omitempty"`
	CreatedAt             string                           `json:"created_at"`
	UpdatedAt             string                           `json:"updated_at"`
}

// desktopBuildRevocationResponse is one revoked build. Release, platform and
// architecture are absent for a build this server's catalogue doesn't hold.
type desktopBuildRevocationResponse struct {
	BuildID      string            `json:"build_id"`
	Release      string            `json:"release,omitempty"`
	Platform     string            `json:"platform,omitempty"`
	Architecture string            `json:"architecture,omitempty"`
	Reason       string            `json:"reason"`
	RevokedAt    string            `json:"revoked_at"`
	RevokedBy    personRefResponse `json:"revoked_by"`
}

// desktopReleaseResponse is one build of the compiled catalogue.
type desktopReleaseResponse struct {
	Release      string `json:"release"`
	BuildID      string `json:"build_id"`
	Platform     string `json:"platform"`
	Architecture string `json:"architecture"`
	ReleasedAt   string `json:"released_at"`
	Newest       bool   `json:"newest"`
}

type desktopReleaseCursor struct {
	BuildID string `json:"build_id"`
}

func desktopReleaseCursorSpec() route.CursorSpec[desktopReleaseCursor] {
	return route.CursorSpec[desktopReleaseCursor]{
		Label:                "desktop release",
		MaximumEncodedLength: 512,
		Members:              []string{"build_id"},
		Validate: func(cursor desktopReleaseCursor) error {
			if !model.IsValidDesktopBuildID(cursor.BuildID) {
				return errors.New("invalid desktop release keyset")
			}
			return nil
		},
	}
}

type desktopCompatibilityPolicyHandlers struct {
	app    *identity.Service
	people *people.Resolver
}

func desktopCompatibilityPolicyResource(app *identity.Service, resolver *people.Resolver) route.Resource {
	h := desktopCompatibilityPolicyHandlers{app: app, people: resolver}
	replace := route.Endpoint{
		Method:      http.MethodPut,
		Path:        "/desktop-compatibility-policy",
		Auth:        route.AuthStrongRecentSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors: []string{
			"authorization.denied",
			"authorization.request.invalid",
			"authorization.unavailable",
			"audit.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
			"request.invalid",
			"desktop_compatibility_policy.invalid",
			"desktop_compatibility_policy.revision_conflict",
			"desktop_compatibility_policy.unavailable",
			"resource.not_found",
		},
		Handle: h.replace,
	}
	return route.NewResource(
		"desktop-compatibility-policy",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/desktop-compatibility-policy",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"desktop_compatibility_policy.unavailable",
				"desktop_compatibility_policy.revision_conflict",
				"resource.not_found",
			},
			Handle: h.get,
		},
		replace,
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/desktop-releases",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"desktop_compatibility_policy.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.releases,
		},
	)
}

// releases pages the compiled catalogue, which is fixed for this server
// release, so a cursor is the last build listed.
func (h desktopCompatibilityPolicyHandlers) releases(request route.Request) (route.Result, error) {
	requested, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	cursor, present, err := route.DecodePageCursor(requested, desktopReleaseCursorSpec())
	if err != nil {
		return route.Result{}, err
	}
	releases, err := h.app.ListDesktopReleases(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	if present {
		next := slices.IndexFunc(releases, func(release identity.DesktopRelease) bool {
			return release.Build.DesktopBuildID == cursor.BuildID
		})
		if next < 0 {
			return route.Result{}, route.InvalidRequest(errors.New("the cursor names no listed build"))
		}
		releases = releases[next+1:]
	}
	more := len(releases) > requested.Limit
	if more {
		releases = releases[:requested.Limit]
	}
	response, err := route.NewList(releases, more, func(release identity.DesktopRelease) desktopReleaseResponse {
		return desktopReleaseResponse{
			Release:      release.Build.DesktopRelease,
			BuildID:      release.Build.DesktopBuildID,
			Platform:     string(release.Build.Platform),
			Architecture: string(release.Build.Architecture),
			ReleasedAt:   model.FormatRFC3339(release.Build.ReleasedAt),
			Newest:       release.Newest,
		}
	}, func(last identity.DesktopRelease) (string, error) {
		return route.EncodeCursor(desktopReleaseCursor{BuildID: last.Build.DesktopBuildID}, desktopReleaseCursorSpec())
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h desktopCompatibilityPolicyHandlers) present(
	request route.Request,
	view *identity.DesktopCompatibilityPolicyView,
) (route.Result, error) {
	ids := make([]model.UserID, 0, len(view.Policy.RevokedDesktopBuilds))
	for _, revocation := range view.Policy.RevokedDesktopBuilds {
		ids = append(ids, revocation.RevokedByUserID)
	}
	refs, err := namePeople(request, h.people, ids...)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(
		http.StatusOK,
		desktopCompatibilityPolicyResponseFromView(view, refs),
	).WithHeaders(privateNoStoreHeaders()), nil
}

func (h desktopCompatibilityPolicyHandlers) get(
	request route.Request,
) (route.Result, error) {
	view, err := h.app.GetDesktopCompatibilityPolicy(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}
	return h.present(request, view)
}

func (h desktopCompatibilityPolicyHandlers) replace(
	request route.Request,
) (route.Result, error) {
	var body desktopCompatibilityPolicyRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	settings, err := requiredDesktopCompatibilityPolicySettings(body)
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.ReplaceDesktopCompatibilityPolicy(
		request.Context,
		request.Invocation(),
		identity.ReplaceDesktopCompatibilityPolicyCommand{
			ExpectedRevision: body.ExpectedRevision,
			Settings:         settings,
			IdempotencyKey:   request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return h.present(request, view)
}

func requiredDesktopCompatibilityPolicySettings(
	body desktopCompatibilityPolicyRequest,
) (model.DesktopCompatibilityPolicySettings, error) {
	minimumRelease := body.MinimumDesktopRelease.ValuePointer()
	if minimumRelease == nil {
		return model.DesktopCompatibilityPolicySettings{}, apperror.New(
			"request.invalid",
		).WithField("field", "minimum_desktop_release")
	}

	revokedBuilds := body.RevokedDesktopBuilds.ValuePointer()
	if revokedBuilds == nil {
		return model.DesktopCompatibilityPolicySettings{}, apperror.New(
			"request.invalid",
		).WithField("field", "revoked_desktop_builds")
	}

	message := body.AdministratorMessage.ValuePointer()
	if message == nil {
		return model.DesktopCompatibilityPolicySettings{}, apperror.New(
			"request.invalid",
		).WithField("field", "administrator_message")
	}

	availability := body.Availability.ValuePointer()
	if availability == nil {
		return model.DesktopCompatibilityPolicySettings{}, apperror.New(
			"request.invalid",
		).WithField("field", "availability")
	}

	if body.RetryAt.IsNull() {
		return model.DesktopCompatibilityPolicySettings{}, apperror.New(
			"request.invalid",
		).WithField("field", "retry_at")
	}

	var retryAt model.OptionalTime
	if value := body.RetryAt.ValuePointer(); value != nil {
		parsed, parseErr := model.ParseRFC3339(*value)
		if parseErr != nil {
			return model.DesktopCompatibilityPolicySettings{}, apperror.New(
				"request.invalid",
			).WithField("field", "retry_at").Wrap(parseErr)
		}

		retryAt = model.OptionalTimeFrom(parsed)
	}

	canonicalBuilds := make([]model.DesktopBuildRevocationRequest, 0, len(*revokedBuilds))
	for _, revocation := range *revokedBuilds {
		canonicalBuilds = append(canonicalBuilds, model.DesktopBuildRevocationRequest{
			BuildID: revocation.BuildID, Reason: strings.TrimSpace(revocation.Reason),
		})
	}
	slices.SortFunc(canonicalBuilds, func(left, right model.DesktopBuildRevocationRequest) int {
		return strings.Compare(left.BuildID, right.BuildID)
	})
	return model.DesktopCompatibilityPolicySettings{
		MinimumDesktopRelease: *minimumRelease,
		RevokedDesktopBuilds:  canonicalBuilds,
		AdministratorMessage:  strings.TrimSpace(*message),
		Availability:          model.DesktopAvailability(*availability),
		RetryAt:               retryAt,
	}, nil
}

func desktopCompatibilityPolicyResponseFromView(
	view *identity.DesktopCompatibilityPolicyView,
	refs people.Refs,
) desktopCompatibilityPolicyResponse {
	response := desktopCompatibilityPolicyResponse{
		RevokedDesktopBuilds: []desktopBuildRevocationResponse{},
	}
	if view == nil || view.Policy == nil {
		return response
	}
	policy := view.Policy
	response.Revision = policy.Revision
	response.MinimumDesktopRelease = policy.MinimumDesktopRelease
	for _, revocation := range policy.RevokedDesktopBuilds {
		item := desktopBuildRevocationResponse{
			BuildID:   revocation.BuildID,
			Reason:    revocation.Reason,
			RevokedAt: model.FormatRFC3339(revocation.RevokedAt),
			RevokedBy: *personRef(refs, revocation.RevokedByUserID),
		}
		if build, known := view.RevokedBuilds[revocation.BuildID]; known {
			item.Release, item.Platform, item.Architecture = build.DesktopRelease, string(build.Platform), string(build.Architecture)
		}
		response.RevokedDesktopBuilds = append(response.RevokedDesktopBuilds, item)
	}
	response.AdministratorMessage = policy.AdministratorMessage
	response.Availability = string(policy.Availability)
	if policy.RetryAt.Valid {
		value := model.FormatRFC3339(policy.RetryAt.Time)
		response.RetryAt = &value
	}
	response.CreatedAt = model.FormatRFC3339(policy.CreatedAt)
	response.UpdatedAt = model.FormatRFC3339(policy.UpdatedAt)
	return response
}
