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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DesktopAuthorizationPolicy bounds hosted desktop authorization. Issuer must
// be HTTPS unless AllowLoopbackHTTPDevelopment permits HTTP on loopback. A zero
// lifetime selects the model maximum (five minutes for the transaction, one
// for the code); a longer one is refused.
type DesktopAuthorizationPolicy struct {
	Issuer                       string
	AllowLoopbackHTTPDevelopment bool
	TransactionLifetime          time.Duration
	CodeLifetime                 time.Duration
}

type desktopAuthorizationAttemptOperation uint8

const (
	desktopAuthorizationAttemptStart desktopAuthorizationAttemptOperation = iota + 1
	desktopAuthorizationAttemptExchange
	desktopAuthorizationAttemptReauthenticationStart
	desktopAuthorizationAttemptReauthenticationComplete
)

func (o desktopAuthorizationAttemptOperation) qualifier() (string, bool) {
	switch o {
	case desktopAuthorizationAttemptStart:
		return "start", true
	case desktopAuthorizationAttemptExchange:
		return "exchange", true
	case desktopAuthorizationAttemptReauthenticationStart:
		return "reauthentication_start", true
	case desktopAuthorizationAttemptReauthenticationComplete:
		return "reauthentication_complete", true
	default:
		return "", false
	}
}

func (s *Service) checkDesktopAuthorizationAttempt(
	ctx context.Context,
	operation desktopAuthorizationAttemptOperation,
	identity, source string,
) error {
	qualifier, valid := operation.qualifier()
	if !valid ||
		s.attempts == nil ||
		s.loginRateLimit.Window <= 0 ||
		s.loginRateLimit.MaximumAttempts <= 0 ||
		s.loginRateLimit.MaximumSourceAttempts <= 0 {
		return RateLimitUnavailableAppError(errors.New("desktop authorization attempt accounting is unavailable"))
	}

	_, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose:   authenticationAttemptPurposeDesktopAuthorization,
		qualifier: qualifier,
		window:    s.loginRateLimit.Window,
		limits: []AttemptLimit{
			{dimension: authenticationAttemptDimensionIdentity, maximum: s.loginRateLimit.MaximumAttempts, identity: identity},
			{dimension: authenticationAttemptDimensionSource, maximum: s.loginRateLimit.MaximumSourceAttempts, source: source},
		},
	})
	if err != nil {
		return RateLimitUnavailableAppError(err)
	}
	if limited {
		return apperror.New("authentication.rate_limited")
	}

	return nil
}

type StartDesktopAuthorizationCommand struct {
	CallbackURL      string
	State            string
	CodeChallenge    string
	DeviceID         string
	DeviceName       string
	PublicJWK        model.DesktopPublicJWK
	DesktopRelease   string
	DesktopBuildID   string
	Platform         model.DesktopPlatform
	Architecture     model.DesktopArchitecture
	RealtimeProtocol int
	Source           string
}

// DesktopAuthorizationStart is returned to the desktop app. AuthorizationURL
// carries the browser proof in its fragment so it never reaches a server log;
// ExpiresAt is Unix milliseconds; DPoPNonce is bound to the proposed key.
type DesktopAuthorizationStart struct {
	AuthorizationURL string
	ExpiresAt        int64
	DPoPNonce        string
}

type BindDesktopAuthorizationCommand struct{ Handle, BrowserProof, State string }

// DesktopAuthorizationBinding is the browser's credential for one bound
// desktop authorization. Binding is the raw secret (only its hash is stored),
// set as a cookie; ExpiresAt is Unix milliseconds.
type DesktopAuthorizationBinding struct {
	Binding   string
	ExpiresAt int64
}

// DesktopAuthorizationAccount is the account the hosted page shows before the
// person approves signing the desktop app in as it.
type DesktopAuthorizationAccount struct {
	ID          model.UserID
	Username    string
	DisplayName string
	Email       string
}

// DesktopAuthorizationBrowserContext is what the hosted page shows. Account is
// nil until the transaction is authenticated; until then the sign-in fields
// list the methods the policy allows. Times are Unix milliseconds.
type DesktopAuthorizationBrowserContext struct {
	State             model.BrowserAuthenticationState
	Account           *DesktopAuthorizationAccount
	LocalLoginEnabled bool
	ExternalProviders []model.ExternalAuthenticationProvider
	DeviceName        string
	RequestedAt       int64
	ExpiresAt         int64
}

// requireDesktopAuthorization also defaults the lifetimes, and must run after
// sign-in is set up.
func (s *Service) requireDesktopAuthorization() error {
	if s.browserAuthentications == nil || s.institutions == nil {
		return errors.New("desktop authorization persistence is required")
	}
	if s.accessPolicy == nil || s.capabilities == nil {
		return errors.New("desktop authorization access policy is required")
	}
	if s.newCredential == nil || s.now == nil {
		return errors.New("desktop authorization security dependencies are required")
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
		return errors.New("desktop authorization lifetime policy is invalid")
	}

	if err := model.ValidateBrowserAuthenticationIssuer(s.desktopPolicy.Issuer, s.desktopPolicy.AllowLoopbackHTTPDevelopment); err != nil {
		return errors.New("desktop authorization issuer policy is invalid")
	}

	if s.sessionPolicy.AccessTTL <= 0 ||
		s.sessionPolicy.RefreshTTL <= 0 ||
		s.sessionPolicy.IdleTTL <= 0 ||
		s.sessionPolicy.AbsoluteTTL <= 0 ||
		s.sessionPolicy.MaximumPerUser < 1 {
		return errors.New("desktop authorization session policy is invalid")
	}

	if s.users == nil || s.dpop == nil {
		return errors.New("desktop authorization identity dependencies are invalid")
	}

	return nil
}

// BindDesktopAuthorization exchanges the hosted page's one-use handle and
// fragment proof for a browser binding.
func (s *Service) BindDesktopAuthorization(
	ctx context.Context,
	_ invocation.Invocation,
	command BindDesktopAuthorizationCommand,
) (*DesktopAuthorizationBinding, error) {
	if !model.IsValidCredentialToken(command.Handle) ||
		!model.IsValidCredentialToken(command.BrowserProof) ||
		!model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	binding := s.newCredential()
	if !model.IsValidCredentialToken(binding) || binding == command.Handle || binding == command.BrowserProof {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	bound, err := s.browserAuthentications.BindDesktopAuthorization(ctx, &store.DesktopAuthorizationBinding{
		HandleHash:       model.HashToken(command.Handle),
		BrowserProofHash: model.HashToken(command.BrowserProof),
		StateHash:        model.HashToken(command.State),
		BindingHash:      model.HashToken(binding),
	})
	if err != nil {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	if bound == nil || bound.ExpiresAt.IsZero() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	return &DesktopAuthorizationBinding{Binding: binding, ExpiresAt: bound.ExpiresAt.UnixMilli()}, nil
}

// DesktopAuthorizationContext returns the authenticated account, or the
// sign-in methods still open to the bound authorization.
func (s *Service) DesktopAuthorizationContext(
	ctx context.Context,
	_ invocation.Invocation,
	binding string,
) (*DesktopAuthorizationBrowserContext, error) {
	if !model.IsValidCredentialToken(binding) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	current, err := s.browserAuthentications.GetDesktopAuthorizationContext(ctx, model.HashToken(binding))
	if err != nil || current == nil {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	if current.CreatedAt.IsZero() || current.ExpiresAt.Before(current.CreatedAt) {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	result := &DesktopAuthorizationBrowserContext{
		State:       current.State,
		DeviceName:  current.DeviceName,
		RequestedAt: current.CreatedAt.UnixMilli(),
		ExpiresAt:   current.ExpiresAt.UnixMilli(),
	}
	if current.State == model.BrowserAuthenticationStateAuthenticated {
		user, getErr := s.users.Get(ctx, current.UserID)
		if getErr != nil || user == nil || !user.IsActive() {
			return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(getErr)
		}

		result.Account = &DesktopAuthorizationAccount{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email}
		return result, nil
	}

	if current.State == model.BrowserAuthenticationStateSecondFactorRequired {
		return result, nil
	}
	if current.State != model.BrowserAuthenticationStateBound {
		return nil, apperror.New("authentication.desktop_authorization.rejected")
	}

	local, policyErr := s.accessPolicy.AllowsLocalLogin(ctx)
	if policyErr != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(policyErr)
	}

	if local {
		local, policyErr = s.accessPolicy.AllowsDesktopAuthorization(ctx, "password", "")
		if policyErr != nil {
			return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(policyErr)
		}
	}

	result.LocalLoginEnabled = local
	for _, configured := range s.capabilities.Snapshot().Providers {
		allowed, allowErr := s.accessPolicy.AllowsDesktopAuthorization(ctx, configured.Descriptor.Type, configured.Descriptor.ID)
		if allowErr != nil {
			return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(allowErr)
		}
		if allowed {
			result.ExternalProviders = append(result.ExternalProviders, configured.Descriptor)
		}
	}

	return result, nil
}

var desktopAuthorizationStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationMethodDisabled, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrPasswordCredentialChanged, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationGenerationChanged, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrMFAReenrollmentRequired, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrMFAEnrollmentRequired, Code: "authentication.mfa.enrollment_required"},
		{Kind: apperror.StoreConflict, Constraint: "sessions_maximum_per_user", Code: "authentication.sessions.maximum_reached"},
	},
	NotFound:    "authentication.desktop_authorization.rejected",
	Unavailable: "authentication.desktop_authorization.unavailable",
}

// StartDesktopAuthorization begins an authorization for a compatible desktop
// build; it fails with incompatible or unavailable otherwise.
func (s *Service) StartDesktopAuthorization(
	ctx context.Context,
	inv invocation.Invocation,
	command StartDesktopAuthorizationCommand,
) (*DesktopAuthorizationStart, error) {
	if err := s.checkDesktopAuthorizationAttempt(
		ctx,
		desktopAuthorizationAttemptStart,
		model.HashToken(command.State),
		command.Source,
	); err != nil {
		return nil, err
	}

	if model.ValidateDesktopAuthorizationCallback(command.CallbackURL) != nil ||
		!model.IsValidCredentialToken(command.State) ||
		!model.IsValidCredentialToken(command.CodeChallenge) ||
		command.PublicJWK.Validate() != nil ||
		len(command.DeviceID) > model.SessionDeviceIDMaxLength ||
		utf8.RuneCountInString(command.DeviceName) > model.SessionDeviceNameMaxRunes {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	if s.dpop == nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	compatibility, err := s.EvaluateDesktopCompatibility(ctx, inv, desktopCompatibilityQueryFromStart(command))
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if compatibility.Compatibility != DesktopCompatibilityCompatible {
		return nil, apperror.New("authentication.desktop_authorization.incompatible").WithField("reason", compatibility.Reason)
	}
	if compatibility.Availability != model.DesktopAvailabilityReady {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").WithField("reason", string(compatibility.Availability))
	}

	thumbprint, err := command.PublicJWK.Thumbprint()
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	enabled, err := s.hasAuthenticationChoice(ctx)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if !enabled {
		return nil, apperror.New("authentication.desktop_authorization.disabled")
	}

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}

	handle := s.newCredential()
	proof := s.newCredential()
	if !model.IsValidCredentialToken(handle) || !model.IsValidCredentialToken(proof) || handle == proof {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	creation := &store.DesktopAuthorizationCreation{
		ID:                      model.NewBrowserAuthenticationTransactionID(),
		InstitutionID:           institution.ID,
		Issuer:                  strings.TrimSuffix(s.desktopPolicy.Issuer, "/"),
		HandleHash:              model.HashToken(handle),
		BrowserProofHash:        model.HashToken(proof),
		StateHash:               model.HashToken(command.State),
		CallbackURL:             command.CallbackURL,
		CodeChallenge:           command.CodeChallenge,
		DeviceID:                command.DeviceID,
		DeviceName:              command.DeviceName,
		ProposedPublicJWK:       command.PublicJWK,
		ProposedKeyThumbprint:   thumbprint,
		DesktopRelease:          command.DesktopRelease,
		DesktopBuildID:          command.DesktopBuildID,
		DesktopPlatform:         command.Platform,
		DesktopArchitecture:     command.Architecture,
		DesktopRealtimeProtocol: command.RealtimeProtocol,
		Lifetime:                s.desktopPolicy.TransactionLifetime,
	}
	saved, err := s.browserAuthentications.CreateDesktopAuthorization(ctx, creation)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if saved == nil || saved.ID != creation.ID || saved.ExpiresAt.IsZero() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	nonce, err := s.dpop.IssueNonce(ctx, dpopBinding{
		Kind:            dpopBindingAuthorization,
		AuthorizationID: creation.ID,
		KeyThumbprint:   thumbprint,
		Origin:          s.dpop.deps.Policy.Origin,
	})
	if err != nil {
		return nil, err
	}

	authorizationURL, err := desktopAuthorizationURL(creation.Issuer, handle, proof, command.State)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}

	return &DesktopAuthorizationStart{
		AuthorizationURL: authorizationURL,
		ExpiresAt:        saved.ExpiresAt.UnixMilli(),
		DPoPNonce:        nonce,
	}, nil
}

func desktopCompatibilityQueryFromStart(command StartDesktopAuthorizationCommand) DesktopCompatibilityQuery {
	return desktopCompatibilityQueryFromBuild(desktopAuthorizationBuild{
		release:          command.DesktopRelease,
		buildID:          command.DesktopBuildID,
		platform:         command.Platform,
		architecture:     command.Architecture,
		realtimeProtocol: command.RealtimeProtocol,
	})
}

func (s *Service) hasAuthenticationChoice(ctx context.Context) (bool, error) {
	allowed, err := s.accessPolicy.AllowsDesktopAuthorization(ctx, "password", "")
	if err != nil || allowed {
		return allowed, err
	}
	for _, provider := range s.capabilities.Snapshot().Providers {
		allowed, err = s.accessPolicy.AllowsDesktopAuthorization(ctx, provider.Descriptor.Type, provider.Descriptor.ID)
		if err != nil || allowed {
			return allowed, err
		}
	}
	return false, nil
}

func desktopAuthorizationURL(issuer, handle, proof, state string) (string, error) {
	return hostedDesktopURL(issuer, "/authorize/desktop", handle, proof, state)
}
