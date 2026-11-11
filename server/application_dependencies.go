// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
)

// applicationDependencies projects platform capabilities and deployment
// configuration into the explicit app.Dependencies bundle so package app never
// imports platform.
func applicationDependencies(
	capabilities constructionCapabilities,
	cfg config.Config,
	content app.FileContent,
	mailRenderer mail.Renderer,
) (app.Dependencies, error) {
	auth := cfg.Authentication
	cache := platformAuthenticationCache{cache: capabilities.cache}
	log := capabilities.logger
	if content == nil {
		return app.Dependencies{}, errors.New("file content is nil")
	}

	mailSecretSealer, err := newMailSecretSealer(cfg.Mail)
	if err != nil {
		return app.Dependencies{}, err
	}

	mfaSecretSealer, err := newMFASecretSealer(cfg.Authentication.MFA)
	if err != nil {
		return app.Dependencies{}, err
	}
	if mailRenderer == nil {
		return app.Dependencies{}, errors.New("mail template renderer is nil")
	}

	assetFiles, err := runtimeAssetDirectory("templates")
	if err != nil {
		return app.Dependencies{}, fmt.Errorf("open mail inline assets: %w", err)
	}

	mailAssets, err := mail.NewInlineAssets(assetFiles)
	if err != nil {
		return app.Dependencies{}, fmt.Errorf("construct mail inline assets: %w", err)
	}

	mailer := accountMailerAdapter{mailer: capabilities.mailer, assets: mailAssets}
	mailDeliveryRecorder, mailMetricsReader := newMailTelemetry(log)
	desktopBuilds, err := verifiedDesktopBuildCatalog()
	if err != nil {
		return app.Dependencies{}, err
	}

	return app.Dependencies{
		Store:                   capabilities.persistence,
		Cache:                   cache,
		MailDeliverySender:      mailer,
		MailTemplateRenderer:    mailRenderer,
		MailDeliveryRecorder:    mailDeliveryRecorder,
		MailMetricsReader:       mailMetricsReader,
		MFASecretSealer:         mfaSecretSealer,
		MailSecretSealer:        mailSecretSealer,
		Registry:                externalProviderRegistryAdapter{registry: capabilities.externalAuthentication},
		FileContent:             content,
		DesktopBuildCatalog:     desktopBuilds,
		NodeID:                  capabilities.nodeID,
		PublicURL:               cfg.Server.PublicURL,
		SupportAddress:          cfg.Mail.SupportAddress,
		LoopbackHTTPDevelopment: explicitLoopbackHTTPDevelopment(cfg.Server.PublicURL),
		Password:                passwordPolicyFromConfig(auth),
		Sessions:                sessionPolicyFromConfig(auth),
		LoginRateLimit: identity.LoginRateLimitPolicy{
			Window:                     auth.LoginRateLimit.Window.Duration,
			MaximumAttempts:            auth.LoginRateLimit.MaximumAttempts,
			MaximumSourceAttempts:      auth.LoginRateLimit.MaximumSourceAttempts,
			AccountFailuresBeforeDelay: auth.LoginRateLimit.AccountFailuresBeforeDelay,
			MaximumAccountFailureDelay: auth.LoginRateLimit.MaximumAccountFailureDelay.Duration,
		},
		PersonalAccessToken: identity.PersonalAccessTokenPolicy{
			MinimumLifetime:        auth.PersonalAccessTokens.MinimumLifetime.Duration,
			MaximumLifetime:        auth.PersonalAccessTokens.MaximumLifetime.Duration,
			LastUsedUpdateInterval: auth.PersonalAccessTokens.LastUsedUpdateInterval.Duration,
			MaximumPerUser:         auth.PersonalAccessTokens.MaximumPerUser,
		},
		AccountRecovery: accountRecoveryPolicyFromConfig(auth),
		MFA: identity.MFAPolicy{
			Enabled:           auth.MFA.Enabled,
			Issuer:            auth.MFA.Issuer,
			SetupTTL:          auth.MFA.SetupTTL.Duration,
			RecoveryCodeCount: auth.MFA.RecoveryCodeCount,
		},
		ExternalAuth:              externalAuthenticationPolicyFromConfig(auth, cfg.Server.PublicURL, capabilities.nodeID),
		RecentAuthenticationTTL:   auth.RecentAuthenticationTTL.Duration,
		AuthenticationDiagnostics: log,
		RealtimeDiagnostics:       log,
		RecoveryDiagnostics:       log,
	}, nil
}

// passwordPolicyFromConfig translates the configured password settings into the application password policy.
func passwordPolicyFromConfig(auth config.Authentication) identity.PasswordPolicy {
	return identity.PasswordPolicy{
		MaximumConcurrentOperations: auth.Password.MaximumConcurrentOperations,
		MinimumLength:               auth.Password.MinimumLength,
		MaximumLength:               auth.Password.MaximumLength,
		ArgonMemoryKiB:              auth.Password.ArgonMemoryKiB,
		ArgonIterations:             auth.Password.ArgonIterations,
		ArgonParallelism:            auth.Password.ArgonParallelism,
		ArgonSaltBytes:              auth.Password.ArgonSaltBytes,
		ArgonKeyBytes:               auth.Password.ArgonKeyBytes,
		HistoryCount:                auth.Password.HistoryCount,
	}
}

// sessionPolicyFromConfig translates the configured Session settings into the application Session policy.
func sessionPolicyFromConfig(auth config.Authentication) identity.SessionPolicy {
	return identity.SessionPolicy{
		AccessTTL:              auth.Sessions.AccessTTL.Duration,
		RefreshTTL:             auth.Sessions.RefreshTTL.Duration,
		IdleTTL:                auth.Sessions.IdleTTL.Duration,
		AbsoluteTTL:            auth.Sessions.AbsoluteTTL.Duration,
		ActivityUpdateInterval: auth.Sessions.ActivityUpdateInterval.Duration,
		MaximumPerUser:         auth.Sessions.MaximumPerUser,
		RetiredRetention:       auth.Sessions.RetiredRetention.Duration,
	}
}

// accountRecoveryPolicyFromConfig translates the configured account recovery settings into the application policy.
func accountRecoveryPolicyFromConfig(auth config.Authentication) identity.AccountRecoveryPolicy {
	return identity.AccountRecoveryPolicy{
		EmailVerificationTTL: auth.AccountRecovery.EmailVerificationTTL.Duration,
		PasswordResetTTL:     auth.AccountRecovery.PasswordResetTTL.Duration,
		EmailChangeUndoTTL:   auth.AccountRecovery.EmailChangeUndoTTL.Duration,
		RateLimit: identity.LoginRateLimitPolicy{
			Window:                auth.AccountRecovery.RateLimit.Window.Duration,
			MaximumAttempts:       auth.AccountRecovery.RateLimit.MaximumAttempts,
			MaximumSourceAttempts: auth.AccountRecovery.RateLimit.MaximumSourceAttempts,
		},
	}
}

// externalAuthenticationPolicyFromConfig translates the configured external authentication settings into the application policy.
func externalAuthenticationPolicyFromConfig(auth config.Authentication, publicURL, nodeID string) identity.ExternalAuthenticationPolicy {
	return identity.ExternalAuthenticationPolicy{
		PublicURL:     publicURL,
		LoginStateTTL: auth.External.LoginStateTTL.Duration,
		LoginRateLimit: identity.LoginRateLimitPolicy{
			Window:                auth.LoginRateLimit.Window.Duration,
			MaximumAttempts:       auth.LoginRateLimit.MaximumAttempts,
			MaximumSourceAttempts: auth.LoginRateLimit.MaximumSourceAttempts,
		},
		NodeID: nodeID,
	}
}

// explicitLoopbackHTTPDevelopment projects the deliberately local HTTP
// origin into the application without coupling app/model to deployment
// configuration. Non-loopback HTTP never receives this capability.
func explicitLoopbackHTTPDevelopment(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil &&
		parsed.Scheme == "http" &&
		model.ValidateBrowserAuthenticationIssuer(raw, true) == nil
}
