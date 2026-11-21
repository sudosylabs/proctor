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
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/desktoprelease"
	"github.com/sudosylabs/proctor/server/metrics"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform"
	"github.com/sudosylabs/proctor/server/platform/modelgateway"
	"github.com/sudosylabs/proctor/server/platform/modelgateway/anthropic"
	"github.com/sudosylabs/proctor/server/platform/modelgateway/openaicompat"
	"github.com/sudosylabs/proctor/server/platform/modelgateway/openairesponses"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/secretseal"
)

// applicationDependencies builds app.Dependencies from the platform and the
// configuration, so package app never imports platform.
func applicationDependencies(
	capabilities constructionCapabilities,
	cfg config.Config,
	content app.FileContent,
	mailRenderer *mail.TemplateRenderer,
) (app.Dependencies, error) {
	auth := cfg.Authentication
	cache := platformAuthenticationCache{cache: capabilities.cache}
	log := capabilities.logger
	if content == nil {
		return app.Dependencies{}, errors.New("file content is nil")
	}

	mailSecretSealer, err := secretseal.NewMailSealer(cfg.Mail)
	if err != nil {
		return app.Dependencies{}, err
	}

	mfaSecretSealer, err := secretseal.NewMFASealer(cfg.Authentication.MFA)
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

	mailer := platform.NewMailSender(capabilities.mailer, mailAssets)
	mailDeliveryRecorder, mailMetricsReader := metrics.NewMailTelemetry(log)
	desktopBuilds, err := desktoprelease.VerifiedCatalog()
	if err != nil {
		return app.Dependencies{}, err
	}

	providerNames := make([]string, len(cfg.Assistant.Providers))
	for index, provider := range cfg.Assistant.Providers {
		providerNames[index] = provider.Name
	}
	gateway, err := assistantGateway(cfg.Assistant)
	if err != nil {
		return app.Dependencies{}, err
	}
	return app.Dependencies{
		AssistantGateway:        gateway,
		Store:                   capabilities.persistence,
		Cache:                   cache,
		MailDeliverySender:      mailer,
		MailTemplateRenderer:    mailRenderer,
		MailDeliveryRecorder:    mailDeliveryRecorder,
		MailMetricsReader:       mailMetricsReader,
		MFASecretSealer:         mfaSecretSealer,
		MailSecretSealer:        mailSecretSealer,
		Registry:                pluginhost.IdentityProviders(capabilities.externalAuthentication),
		FileContent:             content,
		DesktopBuildCatalog:     desktopBuilds,
		NodeID:                  capabilities.nodeID,
		PublicURL:               cfg.Server.PublicURL,
		ExecutionEnabled:        cfg.Execution.Enabled,
		AssistantEnabled:        cfg.Assistant.Enabled,
		AssistantProviders:      providerNames,
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

// explicitLoopbackHTTPDevelopment reports whether the public URL is plain
// HTTP on loopback. Non-loopback HTTP never gets the development allowance.
func explicitLoopbackHTTPDevelopment(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil &&
		parsed.Scheme == "http" &&
		model.ValidateBrowserAuthenticationIssuer(raw, true) == nil
}

// assistantGateway builds one adapter per configured provider behind a
// router that picks by name. It is nil when the assistant is off, so the
// application builds no assistant at all.
func assistantGateway(cfg config.Assistant) (assistant.ModelGateway, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	providers := make(map[string]assistant.ModelGateway, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		transport := modelgateway.Config{BaseURL: provider.BaseURL, APIKey: provider.APIKey, Timeout: provider.Timeout.Duration,
			MaxResponseBytes: provider.MaxResponseBytes, Capabilities: modelgateway.Capabilities{
				ToolChoice: provider.Capabilities.ToolChoice, ParallelTools: provider.Capabilities.ParallelTools,
				Streaming: provider.Capabilities.Streaming, CompletionTokens: provider.Capabilities.CompletionTokens,
				StreamUsage: provider.Capabilities.StreamUsage}}
		var gateway assistant.ModelGateway
		var err error
		switch provider.Kind {
		case "openai_compatible":
			gateway, err = openaicompat.New(transport)
		case "openai_responses":
			gateway, err = openairesponses.New(transport)
		case "anthropic":
			gateway, err = anthropic.New(transport)
		default:
			err = fmt.Errorf("unknown provider kind %q", provider.Kind)
		}
		if err != nil {
			// The error names the provider, never its key or URL.
			return nil, fmt.Errorf("assistant provider %s: %w", provider.Name, err)
		}
		providers[provider.Name] = gateway
	}
	return modelgateway.NewRouter(providers), nil
}
