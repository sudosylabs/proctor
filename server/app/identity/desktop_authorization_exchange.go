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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ExchangeDesktopAuthorizationCommand struct {
	Code, State, CodeVerifier, Source string
	DPoPProof                         string
	PublicJWK                         model.DesktopPublicJWK
	DesktopRelease, DesktopBuildID    string
	Platform                          model.DesktopPlatform
	Architecture                      model.DesktopArchitecture
	RealtimeProtocol                  int
}

// DesktopAuthorizationExchangeResult is what a redeemed authorization code
// yields: the Desktop Session, its Desktop Registration, and the raw access
// and refresh tokens, whose token type is DPoP. DPoPNonce is a fresh nonce
// bound to that Session, Registration, and DPoP key.
type DesktopAuthorizationExchangeResult struct {
	Session      *model.Session
	Registration *model.DesktopRegistration
	Tokens       *model.AuthenticationTokens
	DPoPNonce    string
}

// ExchangeDesktopAuthorization redeems the one-use code with its PKCE
// verifier and DPoP proof for a Desktop Session and Desktop Registration.
func (s *Service) ExchangeDesktopAuthorization(
	ctx context.Context,
	invocation invocation.Invocation,
	command ExchangeDesktopAuthorizationCommand,
) (*DesktopAuthorizationExchangeResult, error) {
	if !model.IsValidCredentialToken(command.Code) ||
		!model.IsValidCredentialToken(command.State) ||
		!model.IsValidPKCEVerifier(command.CodeVerifier) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	if s.dpop == nil || command.PublicJWK.Validate() != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	thumbprint, err := command.PublicJWK.Thumbprint()
	if err != nil {
		return nil, apperror.New("authentication.dpop.invalid")
	}

	codeHash := model.HashToken(command.Code)
	if err = s.checkDesktopAuthorizationAttempt(ctx, desktopAuthorizationAttemptExchange, codeHash, command.Source); err != nil {
		return nil, err
	}

	resolved, err := s.browserAuthentications.ResolveDesktopAuthorizationExchange(ctx, &store.DesktopAuthorizationExchangeProof{
		CodeHash:                codeHash,
		StateHash:               model.HashToken(command.State),
		CodeChallenge:           model.PKCES256Challenge(command.CodeVerifier),
		Issuer:                  strings.TrimSuffix(s.desktopPolicy.Issuer, "/"),
		ExpectedKeyThumbprint:   thumbprint,
		DesktopRelease:          command.DesktopRelease,
		DesktopBuildID:          command.DesktopBuildID,
		DesktopPlatform:         command.Platform,
		DesktopArchitecture:     command.Architecture,
		DesktopRealtimeProtocol: command.RealtimeProtocol,
	})
	if err != nil {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	if resolved == nil || !resolved.ID.IsValid() || !resolved.UserID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	if err = s.verifyDesktopExchangeProof(ctx, command, resolved.ID, thumbprint); err != nil {
		return nil, err
	}

	compatibility, err := s.evaluateDesktopExchangeCompatibility(ctx, invocation, command)
	if err != nil {
		return nil, err
	}

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}

	mfaRequired, err := s.accessPolicy.RequiresMFA(ctx, resolved.UserID)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}

	lifetimes, err := s.accessPolicy.SessionLifetimes(ctx, s.sessionPolicy)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}

	audit, err := s.beginDesktopExchangeAudit(ctx, invocation, resolved.UserID, institution.ID)
	if err != nil || audit == nil || !audit.ID.IsValid() {
		return nil, apperror.New("audit.unavailable").Wrap(err)
	}

	at := model.TimeUTC(s.now())
	accessToken, refreshToken := s.newCredential(), s.newCredential()
	if !model.IsValidCredentialToken(accessToken) || !model.IsValidCredentialToken(refreshToken) {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	result, err := s.browserAuthentications.Exchange(ctx, &store.DesktopAuthorizationExchange{
		CodeHash:                           codeHash,
		StateHash:                          model.HashToken(command.State),
		CodeChallenge:                      model.PKCES256Challenge(command.CodeVerifier),
		Issuer:                             strings.TrimSuffix(s.desktopPolicy.Issuer, "/"),
		ExpectedPublicJWK:                  command.PublicJWK,
		ExpectedKeyThumbprint:              thumbprint,
		DesktopRelease:                     command.DesktopRelease,
		DesktopBuildID:                     command.DesktopBuildID,
		DesktopPlatform:                    command.Platform,
		DesktopArchitecture:                command.Architecture,
		DesktopRealtimeProtocol:            command.RealtimeProtocol,
		DesktopCompatibilityPolicyRevision: compatibility.PolicyRevision,
		AccessTokenHash:                    model.HashToken(accessToken),
		RefreshTokenHash:                   model.HashToken(refreshToken),
		AccessLifetime:                     min(s.sessionPolicy.AccessTTL, lifetimes.Idle),
		RefreshLifetime:                    min(s.sessionPolicy.RefreshTTL, lifetimes.Absolute),
		IdleLifetime:                       lifetimes.Idle,
		AbsoluteLifetime:                   lifetimes.Absolute,
		MaximumActive:                      s.sessionPolicy.MaximumPerUser,
		Capabilities:                       AccessDeploymentCapabilities(s.capabilities.Snapshot()),
		MFARequiredByPolicy:                mfaRequired,
		AuditEventID:                       audit.ID,
		AuditAt:                            at,
	})
	if err != nil {
		failure := desktopAuthorizationStoreErrors.Translate(err)
		if auditErr := s.failDesktopAudit(
			ctx,
			audit.ID,
			apperror.CodeOf(failure, "authentication.desktop_authorization.unavailable"),
		); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}

		return nil, failure
	}

	if result != nil && result.Denied {
		return nil, apperror.New("authentication.desktop_authorization.account_session_locked")
	}

	if !validDesktopAuthorizationExchangeResult(result, s.sessionPolicy) {
		failure := apperror.New("authentication.desktop_authorization.unavailable")
		if auditErr := s.failDesktopAudit(ctx, audit.ID, failure.Code()); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}

		return nil, failure
	}

	nonce, nonceErr := s.dpop.IssueNonce(ctx, dpopBinding{
		Kind:                  dpopBindingSession,
		SessionID:             result.Session.ID,
		DesktopRegistrationID: result.Registration.ID,
		KeyThumbprint:         result.Session.DPoPKeyThumbprint,
		Origin:                s.dpop.deps.Policy.Origin,
	})
	if nonceErr != nil {
		return nil, s.withdrawUndisclosedDesktopSession(ctx, invocation, institution.ID, result, nonceErr)
	}

	return &DesktopAuthorizationExchangeResult{
		Session:      result.Session,
		Registration: result.Registration,
		Tokens: &model.AuthenticationTokens{
			TokenType:        "DPoP",
			AccessToken:      accessToken,
			RefreshToken:     refreshToken,
			AccessExpiresAt:  result.AccessExpiresAt,
			RefreshExpiresAt: result.RefreshExpiresAt,
		},
		DPoPNonce: nonce,
	}, nil
}

// verifyDesktopExchangeProof verifies the token request's DPoP proof against the authorization's bound key.
func (s *Service) verifyDesktopExchangeProof(
	ctx context.Context,
	command ExchangeDesktopAuthorizationCommand,
	authorizationID model.BrowserAuthenticationTransactionID,
	thumbprint string,
) error {
	binding := dpopBinding{
		Kind:            dpopBindingAuthorization,
		AuthorizationID: authorizationID,
		KeyThumbprint:   thumbprint,
		Origin:          s.dpop.deps.Policy.Origin,
	}
	target, targetErr := canonicalDPoPTarget(s.dpop.deps.Policy.Origin + "/api/v1/auth/desktop/token")
	if targetErr != nil {
		return apperror.New("authentication.desktop_authorization.unavailable").Wrap(targetErr)
	}
	if _, err := s.dpop.Verify(ctx, command.DPoPProof, "POST", target, "", &command.PublicJWK, binding); err != nil {
		return err
	}

	return nil
}

// evaluateDesktopExchangeCompatibility requires the exchanging desktop build to be compatible and ready.
func (s *Service) evaluateDesktopExchangeCompatibility(
	ctx context.Context, invocation invocation.Invocation, command ExchangeDesktopAuthorizationCommand,
) (DesktopCompatibilityResult, error) {
	compatibility, err := s.EvaluateDesktopCompatibility(ctx, invocation, desktopCompatibilityQueryFromAuthorization(command))
	if err != nil {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if compatibility.PolicyRevision < 1 {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.unavailable")
	}

	if compatibility.Compatibility != DesktopCompatibilityCompatible {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.incompatible").WithField(
			"reason",
			compatibility.Reason,
		)
	}

	if compatibility.Availability != model.DesktopAvailabilityReady {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.unavailable").WithField(
			"reason",
			string(compatibility.Availability),
		)
	}

	return compatibility, nil
}

// withdrawUndisclosedDesktopSession revokes and audits the withdrawal of an exchanged Session whose nonce could not be issued.
func (s *Service) withdrawUndisclosedDesktopSession(
	ctx context.Context,
	invocation invocation.Invocation,
	institutionID model.InstitutionID,
	result *store.DesktopAuthorizationExchangeResult,
	nonceErr error,
) error {
	// The credentials were never disclosed, but the SQL exchange has
	// committed. Revoke that exact Session synchronously so a cache outage
	// cannot consume the per-user Session allowance with inaccessible
	// Sessions. The active Desktop Registration remains reusable by a fresh
	// authorization after nonce storage recovers.
	// The exchange's success audit already committed, so a separate
	// withdrawal record says the issued Session was taken back.
	revokeCtx := context.WithoutCancel(ctx)
	withdrawal, auditErr := s.beginDesktopWithdrawalAudit(
		revokeCtx,
		invocation,
		result.Session.UserID,
		institutionID,
		result.Session.ID,
	)
	hashes, revokeErr := s.sessions.Withdraw(revokeCtx, &store.SessionWithdrawal{
		ID:        result.Session.ID,
		UserID:    result.Session.UserID,
		RevokedAt: s.now(),
		Reason:    model.SessionRevocationDesktopAuthorizationFailed,
	})
	if auditErr == nil && withdrawal != nil {
		if revokeErr != nil {
			auditErr = s.failDesktopAudit(revokeCtx, withdrawal.ID, "authentication.desktop_authorization.unavailable")
		} else {
			auditErr = s.completeDesktopAudit(revokeCtx, withdrawal.ID, map[string]any{
				"session_id": result.Session.ID,
				"reason":     string(model.SessionRevocationDesktopAuthorizationFailed),
			})
		}
	}

	if revokeErr != nil {
		return apperror.New("authentication.desktop_authorization.unavailable").Wrap(errors.Join(nonceErr, revokeErr, auditErr))
	}

	s.publisher.SessionsRevoked(
		revokeCtx,
		result.Session.UserID,
		[]model.SessionID{result.Session.ID},
		hashes,
	)
	if auditErr != nil {
		return apperror.New("audit.unavailable").Wrap(errors.Join(nonceErr, auditErr))
	}

	return nonceErr
}

func validDesktopAuthorizationExchangeResult(result *store.DesktopAuthorizationExchangeResult, policy SessionPolicy) bool {
	if result == nil ||
		result.Session == nil ||
		result.Registration == nil ||
		result.Registration.Validate() != nil ||
		result.Session.DesktopRegistrationID != result.Registration.ID ||
		result.Session.DPoPKeyThumbprint != result.Registration.KeyThumbprint ||
		result.Session.Validate() != nil ||
		result.Session.ClientType != model.SessionClientDesktop ||
		result.Session.ArchivedAt.Valid ||
		result.Session.RevokedAt.Valid ||
		result.AccessExpiresAt.IsZero() ||
		result.RefreshExpiresAt.IsZero() ||
		!result.AccessExpiresAt.After(result.Session.CreatedAt) ||
		!result.RefreshExpiresAt.After(result.Session.CreatedAt) {
		return false
	}

	return !result.AccessExpiresAt.After(result.Session.CreatedAt.Add(policy.AccessTTL)) &&
		!result.RefreshExpiresAt.After(result.Session.CreatedAt.Add(policy.RefreshTTL))
}

func desktopCompatibilityQueryFromAuthorization(command ExchangeDesktopAuthorizationCommand) DesktopCompatibilityQuery {
	return desktopCompatibilityQueryFromBuild(desktopAuthorizationBuild{
		release:          command.DesktopRelease,
		buildID:          command.DesktopBuildID,
		platform:         command.Platform,
		architecture:     command.Architecture,
		realtimeProtocol: command.RealtimeProtocol,
	})
}

type desktopAuthorizationBuild struct {
	release          string
	buildID          string
	platform         model.DesktopPlatform
	architecture     model.DesktopArchitecture
	realtimeProtocol int
}

func desktopCompatibilityQueryFromBuild(build desktopAuthorizationBuild) DesktopCompatibilityQuery {
	return DesktopCompatibilityQuery{
		DesktopRelease:   build.release,
		DesktopBuildID:   build.buildID,
		Platform:         string(build.platform),
		Architecture:     string(build.architecture),
		RealtimeProtocol: build.realtimeProtocol,
	}
}
