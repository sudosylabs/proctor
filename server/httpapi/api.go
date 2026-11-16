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

// New builds the production API. It fails when application lacks a service a
// resource needs, or when the settings or public URL are invalid.
func New(application *app.App, settings Settings) (*route.API, error) {
	if err := requireApplication(application); err != nil {
		return nil, err
	}

	cookies, err := route.NewCookies(settings.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("configure browser cookies: %w", err)
	}

	return compileAPI(
		settings,
		application.Identity,
		productionResources(application, settings.Health, settings.BuildInfo, settings.Plugins, cookies, settings.WebSocket,
			settings.BinarySockets, settings.Logger)...,
	)
}

// requireApplication reports the first missing service. Jobs and Mail are
// left out on purpose: a graph without a job store has neither, and both
// answer job.unavailable and mail.unavailable on a nil receiver.
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

// compileAPI validates the settings and compiles resources into the sealed
// route catalog. Handler tests call it with resources built over fakes.
func compileAPI(
	settings Settings,
	authenticator route.Authenticator,
	resources ...route.Resource,
) (*route.API, error) {
	if settings.Logger == nil {
		return nil, errors.New("logger is required")
	}
	if settings.Localizer == nil {
		return nil, errors.New("localizer is required")
	}
	if err := route.ValidateLocalization(settings.Localizer, LocalizationDefinitions()); err != nil {
		return nil, err
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

	routeSettings := route.Settings{
		Logger:                  settings.Logger,
		Metrics:                 settings.Metrics,
		Tracing:                 settings.Tracing,
		Localizer:               settings.Localizer,
		PublicURL:               settings.PublicURL,
		DocumentationURL:        settings.DocumentationURL,
		MaxBodyBytes:            settings.MaxBodyBytes,
		ReadTimeout:             settings.ReadTimeout,
		WriteTimeout:            settings.WriteTimeout,
		TrustedProxies:          settings.TrustedProxies,
		RecentAuthenticationTTL: settings.RecentAuthenticationTTL,
	}
	return route.New(routeSettings, authenticator, resources...)
}
