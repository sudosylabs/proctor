// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/api.go. Proctor applies its own
// immutable route catalog, typed authentication policies, and Problem Details
// boundary.

package httpapi

import (
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/httpapi/route"
)

// New constructs the production API over the application. Each resource in
// productionResources is built from the application service that owns its
// use cases, and the Identity service authenticates every request.
func New(application *app.App, settings Settings) (*route.API, error) {
	if err := requireApplication(application); err != nil {
		return nil, err
	}

	cookies, err := route.NewCookies(settings.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("configure browser cookies: %w", err)
	}

	return newAPI(
		settings,
		application.Identity,
		productionResources(application, settings.Health, settings.BuildInfo, cookies, settings.WebSocket)...,
	)
}

// requireApplication rejects an application missing a service a resource is
// built from, reporting the first absent one. Jobs and Mail are absent in a
// graph that has no Job store; those services answer job.unavailable and
// mail.unavailable on a nil receiver.
func requireApplication(application *app.App) error {
	if application == nil {
		return errors.New("application is required")
	}

	required := []struct {
		missing bool
		message string
	}{
		{application.Identity == nil, "identity application is required"},
		{application.Onboarding == nil, "onboarding application is required"},
		{application.Administration == nil, "administration application is required"},
		{application.Academic == nil, "academic application is required"},
		{application.Users == nil, "user application is required"},
		{application.Search == nil, "search application is required"},
		{application.Counts == nil, "administration counts application is required"},
		{application.ExamAuthoring == nil, "exam application is required"},
		{application.ExamPublication == nil, "exam revision application is required"},
		{application.Sittings == nil, "exam sitting application is required"},
		{application.Corrections == nil, "exam sitting correction application is required"},
		{application.Resources == nil, "exam resource application is required"},
		{application.StarterWorkspaces == nil, "exam starter workspace application is required"},
		{application.Attempts == nil, "exam attempt application is required"},
		{application.Reviews == nil, "exam integrity review application is required"},
		{application.ExamRecords == nil, "exam records application is required"},
		{application.ExamExports == nil, "exam exports application is required"},
	}
	for _, service := range required {
		if service.missing {
			return errors.New(service.message)
		}
	}

	return nil
}

// newAPI validates the settings and hands the resources to the routing
// kernel, which compiles them into the sealed route catalog. New passes the
// production inventory; handler tests pass resources built over their fakes.
func newAPI(settings Settings, authenticator route.Authenticator, resources ...route.Resource) (*route.API, error) {
	if settings.Logger == nil {
		return nil, errors.New("logger is required")
	}
	if settings.Localizer == nil {
		return nil, errors.New("localizer is required")
	}
	for _, definition := range LocalizationDefinitions() {
		if _, err := settings.Localizer.Translate("", definition.ID, nil); err != nil {
			return nil, fmt.Errorf("validate localization %q: %w", definition.ID, err)
		}
	}
	if settings.Health == nil {
		return nil, errors.New("health state is required")
	}
	if settings.MaxBodyBytes <= 0 {
		return nil, errors.New("maximum body size must be greater than zero")
	}
	if settings.RecentAuthenticationTTL <= 0 {
		return nil, errors.New("recent authentication TTL must be greater than zero")
	}
	if settings.NodeID == "" {
		return nil, errors.New("cluster node ID is required")
	}
	if settings.WebSocket == nil {
		return nil, errors.New("websocket transport is required")
	}

	return route.New(route.Settings{
		Logger:                  settings.Logger,
		Metrics:                 settings.Metrics,
		Tracing:                 settings.Tracing,
		Localizer:               settings.Localizer,
		PublicURL:               settings.PublicURL,
		MaxBodyBytes:            settings.MaxBodyBytes,
		ReadTimeout:             settings.ReadTimeout,
		WriteTimeout:            settings.WriteTimeout,
		TrustedProxies:          settings.TrustedProxies,
		RecentAuthenticationTTL: settings.RecentAuthenticationTTL,
	}, authenticator, resources...)
}
