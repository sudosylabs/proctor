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

// requireDesktopReauthentication refuses a Service whose Desktop
// reauthentication collaborators or policy are incomplete. Desktop
// reauthentication lets a Desktop Session refresh its primary proof in the
// system browser. It never creates a Session, never accepts a browser Web
// Session as proof, and accepts only the Desktop Session's own authentication
// method for that Session's own User. New calls it once, after Desktop
// authorization is set up.
func (s *Service) requireDesktopReauthentication() error {
	if s.browserAuthentications == nil ||
		s.institutions == nil ||
		s.users == nil ||
		s.capabilities == nil ||
		s.newCredential == nil ||
		s.now == nil {
		return errors.New("desktop reauthentication dependencies are required")
	}

	if s.recentAuthenticationTTL <= 0 {
		return errors.New("desktop reauthentication recency policy is invalid")
	}
	if s.desktopPolicy.TransactionLifetime == 0 {
		s.desktopPolicy.TransactionLifetime = model.BrowserAuthenticationTransactionLifetime
	}
	if s.desktopPolicy.CodeLifetime == 0 {
		s.desktopPolicy.CodeLifetime = model.DesktopAuthorizationCodeLifetime
	}

	if s.desktopPolicy.TransactionLifetime <= 0 ||
		s.desktopPolicy.TransactionLifetime > model.BrowserAuthenticationTransactionLifetime ||
		s.desktopPolicy.CodeLifetime <= 0 ||
		s.desktopPolicy.CodeLifetime > model.DesktopAuthorizationCodeLifetime {
		return errors.New("desktop reauthentication lifetime policy is invalid")
	}

	if err := model.ValidateBrowserAuthenticationIssuer(s.desktopPolicy.Issuer, s.desktopPolicy.AllowLoopbackHTTPDevelopment); err != nil {
		return errors.New("desktop reauthentication issuer policy is invalid")
	}

	return nil
}

// StartDesktopReauthenticationCommand carries the Desktop's loopback callback,
// state and S256 PKCE challenge, validated like Desktop Authorization start.
type StartDesktopReauthenticationCommand struct {
	CallbackURL   string
	State         string
	CodeChallenge string
	Source        string
}

// DesktopReauthenticationStart is the hosted page the Desktop opens in the
// system browser, valid until ExpiresAt.
type DesktopReauthenticationStart struct {
	AuthorizationURL string
	ExpiresAt        time.Time
}

// DesktopReauthenticationBrowserContext is the safe projection the hosted
// page renders: whose Desktop Session it is and the one proof it accepts.
type DesktopReauthenticationBrowserContext struct {
	State       model.BrowserAuthenticationState
	DisplayName string
	// AuthenticationMethod is "password" or the provider protocol type of the
	// Desktop Session's own authentication.
	AuthenticationMethod string
	// Provider is the Desktop Session's identity provider; nil for password.
	Provider  *model.ExternalAuthenticationProvider
	ExpiresAt time.Time
}

// requireDesktopDPoPSession accepts only a Desktop Session whose current
// request passed DPoP verification with its registered key.
func requireDesktopDPoPSession(principal model.Principal) (model.SessionCredentialID, error) {
	if principal.Validate() != nil ||
		principal.CredentialType != model.CredentialSessionAccess ||
		principal.ClientType != model.SessionClientDesktop ||
		!principal.HasRegisteredDesktopKey() ||
		principal.MFARecoveryRequired {
		return "", apperror.New("authentication.desktop_reauthentication.invalid")
	}

	credentialID, err := model.ParseSessionCredentialID(principal.CredentialID.String())
	if err != nil {
		return "", apperror.New("authentication.desktop_reauthentication.invalid")
	}

	return credentialID, nil
}

// StartDesktopReauthentication begins a browser reauthentication for the
// calling Desktop Session and returns the hosted page to open.
func (s *Service) StartDesktopReauthentication(
	ctx context.Context,
	inv invocation.Invocation,
	command StartDesktopReauthenticationCommand,
) (*DesktopReauthenticationStart, error) {
	principal := inv.Principal()
	credentialID, err := requireDesktopDPoPSession(principal)
	if err != nil {
		return nil, err
	}

	if err = s.checkDesktopAuthorizationAttempt(
		ctx,
		desktopAuthorizationAttemptReauthenticationStart,
		principal.UserID.String(),
		command.Source,
	); err != nil {
		return nil, err
	}

	if model.ValidateDesktopAuthorizationCallback(command.CallbackURL) != nil ||
		!model.IsValidCredentialToken(command.State) ||
		!model.IsValidCredentialToken(command.CodeChallenge) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, authenticationUnavailable(err)
	}

	handle, proof := s.newCredential(), s.newCredential()
	if !model.IsValidCredentialToken(handle) || !model.IsValidCredentialToken(proof) || handle == proof {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication credentials are invalid"))
	}

	issuer := strings.TrimSuffix(s.desktopPolicy.Issuer, "/")
	created, err := s.browserAuthentications.CreateDesktopReauthentication(ctx, &store.DesktopReauthenticationCreation{
		ID:                    model.NewBrowserAuthenticationTransactionID(),
		InstitutionID:         institution.ID,
		Issuer:                issuer,
		HandleHash:            model.HashToken(handle),
		BrowserProofHash:      model.HashToken(proof),
		StateHash:             model.HashToken(command.State),
		CallbackURL:           command.CallbackURL,
		CodeChallenge:         command.CodeChallenge,
		UserID:                principal.UserID,
		SessionID:             principal.SessionID,
		CredentialID:          credentialID,
		DesktopRegistrationID: principal.DesktopRegistrationID,
		KeyThumbprint:         principal.DPoPKeyThumbprint,
		Lifetime:              s.desktopPolicy.TransactionLifetime,
	})
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if created == nil || created.ExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no deadline"))
	}

	authorizationURL, err := hostedDesktopURL(issuer, "/reauthenticate/desktop", handle, proof, command.State)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	return &DesktopReauthenticationStart{AuthorizationURL: authorizationURL, ExpiresAt: created.ExpiresAt}, nil
}

// BindDesktopReauthentication exchanges the hosted page's one-use handle and
// fragment proof for a browser binding.
func (s *Service) BindDesktopReauthentication(
	ctx context.Context,
	_ invocation.Invocation,
	command BindDesktopAuthorizationCommand,
) (*DesktopAuthorizationBinding, error) {
	if !model.IsValidCredentialToken(command.Handle) ||
		!model.IsValidCredentialToken(command.BrowserProof) ||
		!model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}

	binding := s.newCredential()
	if !model.IsValidCredentialToken(binding) || binding == command.Handle || binding == command.BrowserProof {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication binding is invalid"))
	}

	bound, err := s.browserAuthentications.BindDesktopReauthentication(ctx, &store.DesktopAuthorizationBinding{
		HandleHash:       model.HashToken(command.Handle),
		BrowserProofHash: model.HashToken(command.BrowserProof),
		StateHash:        model.HashToken(command.State),
		BindingHash:      model.HashToken(binding),
	})
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if bound == nil || bound.ExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no deadline"))
	}

	return &DesktopAuthorizationBinding{Binding: binding, ExpiresAt: bound.ExpiresAt.UnixMilli()}, nil
}

// DesktopReauthenticationContext projects the bound reauthentication: the
// Desktop Session's account and the single proof it accepts.
func (s *Service) DesktopReauthenticationContext(
	ctx context.Context,
	_ invocation.Invocation,
	binding string,
) (*DesktopReauthenticationBrowserContext, error) {
	current, err := s.boundDesktopReauthentication(ctx, binding)
	if err != nil {
		return nil, err
	}

	user, err := s.users.Get(ctx, current.UserID)
	if err != nil || user == nil || !user.IsActive() {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid").Wrap(err)
	}

	result := &DesktopReauthenticationBrowserContext{
		State:                current.State,
		DisplayName:          user.DisplayName,
		AuthenticationMethod: current.ExpectedAuthenticationMethod,
		ExpiresAt:            current.ExpiresAt,
	}
	if current.ExpectedProviderID != "" {
		provider, configured := s.configuredProvider(current.ExpectedProviderID)
		if !configured {
			return nil, apperror.New("authentication.desktop_reauthentication.invalid")
		}

		result.Provider = &provider
	}

	return result, nil
}

func (s *Service) boundDesktopReauthentication(ctx context.Context, binding string) (*store.DesktopReauthenticationContext, error) {
	if !model.IsValidCredentialToken(binding) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	current, err := s.browserAuthentications.GetDesktopReauthenticationContext(ctx, model.HashToken(binding))
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if current == nil || !current.ID.IsValid() || !current.UserID.IsValid() || current.ExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication context is invalid"))
	}
	return current, nil
}

func (s *Service) configuredProvider(id string) (model.ExternalAuthenticationProvider, bool) {
	for _, configured := range s.capabilities.Snapshot().Providers {
		if configured.Descriptor.ID == id {
			return configured.Descriptor, true
		}
	}
	return model.ExternalAuthenticationProvider{}, false
}

// hostedDesktopURL is the hosted page at path carrying the one-use handle and
// state in its query and the browser proof only in its fragment.
func hostedDesktopURL(issuer, path, handle, proof, state string) (string, error) {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Host == "" {
		return "", errors.New("desktop hosted page issuer is invalid")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + path
	query := parsed.Query()
	query.Set("request", handle)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	parsed.Fragment = "proof=" + proof
	return parsed.String(), nil
}

var desktopReauthenticationStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrPasswordCredentialChanged, Code: "authentication.credentials_invalid"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationMethodDisabled, Code: "authentication.desktop_reauthentication.invalid"},
		{
			Kind:     apperror.StoreSentinel,
			Sentinel: store.ErrAuthenticationGenerationChanged,
			Code:     "authentication.desktop_reauthentication.invalid",
		},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrMFAReenrollmentRequired, Code: "authentication.desktop_reauthentication.invalid"},
	},
	NotFound:    "authentication.desktop_reauthentication.invalid",
	Conflict:    "authentication.desktop_reauthentication.invalid",
	Unavailable: "authentication.internal",
}
