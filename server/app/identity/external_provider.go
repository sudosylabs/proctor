// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Sentinel errors the provider adapter must translate protocol failures into,
// so this package never imports a concrete protocol.
var (
	ErrExternalAuthenticationRejected    = errors.New("external authentication rejected")
	ErrExternalAuthenticationInvalid     = errors.New("external authentication response is invalid")
	ErrExternalAuthenticationUnavailable = errors.New("external authentication provider is unavailable")
)

// ExternalProviderBeginRequest asks a provider for its redirect challenge.
type ExternalProviderBeginRequest struct {
	CallbackURL         string
	State               string
	Proof               string
	FreshAuthentication bool
}

// ExternalProviderBeginResponse is the provider's redirect challenge.
type ExternalProviderBeginResponse struct {
	RedirectURL string
}

// ExternalProviderCompleteRequest hands a callback to its provider to verify.
type ExternalProviderCompleteRequest struct {
	CallbackURL             string
	State                   string
	Proof                   string
	Callback                model.ExternalAuthenticationCallback
	AuthenticationStartedAt time.Time
}

// ExternalIdentityProvider is one configured provider; composition adapts the
// CAS and OIDC implementations to it.
type ExternalIdentityProvider interface {
	Descriptor() model.ExternalAuthenticationProvider
	AutoProvision() bool
	Begin(context.Context, ExternalProviderBeginRequest) (*ExternalProviderBeginResponse, error)
	State(model.ExternalAuthenticationCallback) (string, error)
	Complete(
		context.Context,
		ExternalProviderCompleteRequest,
	) (*model.ExternalAuthenticationAssertion, error)
}

// ExternalProviderSource is the configured provider registry.
type ExternalProviderSource interface {
	Descriptors() []model.ExternalAuthenticationProvider
	Provider(id string) (ExternalIdentityProvider, bool)
}

// InvitationAcceptor admits an external identity through the invitation its
// login state carries; onboarding implements it.
type InvitationAcceptor interface {
	AcceptExternalIdentity(
		context.Context,
		*model.ExternalLoginState,
		*model.ExternalAuthenticationAssertion,
		store.AccessDeploymentCapabilities,
		model.RequestMetadata,
		string,
	) (*store.ExternalIdentityInvitationAcceptanceResult, error)
}

// ExternalAuthenticationPolicy is the deployment configuration for external
// login; empty fields default to the Service's own.
type ExternalAuthenticationPolicy struct {
	PublicURL      string
	LoginStateTTL  time.Duration
	LoginRateLimit LoginRateLimitPolicy
	NodeID         string
}

// requireExternalAuthentication must run after sign-in and desktop
// authorization are set up.
func (s *Service) requireExternalAuthentication() error {
	if s.externalProviders == nil {
		return errors.New("external authentication provider registry is required")
	}
	if s.externalLoginStates == nil || s.institutions == nil || s.externalIdentities == nil || s.sessions == nil {
		return errors.New("external authentication persistence is required")
	}
	if s.accessPolicy == nil {
		return errors.New("external authentication access policy is required")
	}
	if s.attempts == nil {
		return errors.New("external authentication attempt accounting is required")
	}
	if s.invalidator == nil {
		return errors.New("authentication invalidator is required")
	}
	if s.capabilities == nil || s.invitations == nil || s.recentAuthenticationTTL <= 0 {
		return errors.New("external authentication method lifecycle dependencies are required")
	}
	if s.diagnostics == nil {
		return errors.New("external authentication diagnostics are required")
	}
	if s.newCredential == nil {
		return errors.New("external authentication credential generator is required")
	}
	if s.now == nil {
		return errors.New("external authentication clock is required")
	}
	return nil
}

// ExternalAuthenticationProviders lists the providers the current access
// policy offers. If the policy can't be read it fails rather than fall back to
// the configured list, which could offer providers the policy has disabled.
func (s *Service) ExternalAuthenticationProviders(
	ctx context.Context,
	_ invocation.Invocation,
) ([]model.ExternalAuthenticationProvider, error) {
	providers, err := s.accessPolicy.AvailableExternalProviders(ctx, s.externalProviders.Descriptors())
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	return providers, nil
}

func externalAuthenticationCallbackURL(
	publicURL string,
	providerID string,
) (string, error) {
	callback, err := url.Parse(publicURL)
	if err != nil || callback.Host == "" {
		return "", errors.New("public URL is invalid")
	}
	callback.Path = strings.TrimSuffix(callback.Path, "/") +
		model.APIURLSuffix + "/auth/providers/" +
		url.PathEscape(providerID) + "/callback"
	callback.RawQuery = ""
	callback.Fragment = ""
	return callback.String(), nil
}

func externalProviderNotFoundError() error {
	return apperror.New("authentication.external.provider_not_found")
}

func invalidExternalAuthenticationError() error {
	return apperror.New("authentication.external.invalid")
}

func externalProviderOperationError(err error) error {
	if errors.Is(err, ErrExternalAuthenticationRejected) {
		return apperror.New("authentication.external.rejected").Wrap(err)
	}
	if errors.Is(err, ErrExternalAuthenticationUnavailable) ||
		errors.Is(err, ErrExternalAuthenticationInvalid) {
		return apperror.New("authentication.external.unavailable").Wrap(err)
	}
	return authenticationUnavailable(err)
}
