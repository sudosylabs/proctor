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

// Sentinel errors returned by the composition-owned external provider adapter.
// Application code never imports concrete protocol packages.
var (
	ErrExternalAuthenticationRejected    = errors.New("external authentication rejected")
	ErrExternalAuthenticationInvalid     = errors.New("external authentication response is invalid")
	ErrExternalAuthenticationUnavailable = errors.New("external authentication provider is unavailable")
)

// ExternalProviderBeginRequest is the protocol-neutral start challenge.
type ExternalProviderBeginRequest struct {
	CallbackURL         string
	State               string
	Proof               string
	FreshAuthentication bool
}

// ExternalProviderBeginResponse is the protocol-neutral redirect challenge.
type ExternalProviderBeginResponse struct {
	RedirectURL string
}

// ExternalProviderCompleteRequest is the protocol-neutral callback completion.
type ExternalProviderCompleteRequest struct {
	CallbackURL             string
	State                   string
	Proof                   string
	Callback                model.ExternalAuthenticationCallback
	AuthenticationStartedAt time.Time
}

// ExternalIdentityProvider is the app-owned protocol-neutral provider port.
// Composition adapts concrete CAS/OIDC implementations to this surface.
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

// ExternalProviderSource is the protocol-neutral registry surface consumed by
// application external-login orchestration. Concrete CAS/OIDC adapters remain
// outside package app.
type ExternalProviderSource interface {
	Descriptors() []model.ExternalAuthenticationProvider
	Provider(id string) (ExternalIdentityProvider, bool)
}

// InvitationAcceptor admits an External Identity through the Invitation its
// login state carries. The parent app package connects it to the onboarding
// area's Invitation acceptance.
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

// ExternalAuthenticationPolicy is the deployment projection for external login.
type ExternalAuthenticationPolicy struct {
	PublicURL      string
	LoginStateTTL  time.Duration
	LoginRateLimit LoginRateLimitPolicy
	NodeID         string
}

// requireExternalAuthentication refuses a Service whose external
// authentication collaborators are incomplete. New calls it once, after
// sign-in and Desktop authorization are set up.
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
	if s.auditor == nil || s.capabilities == nil || s.invitations == nil || s.recentAuthenticationTTL <= 0 {
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

// ExternalAuthenticationProviders lists the configured providers the current
// Access Policy offers for sign-in.
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
