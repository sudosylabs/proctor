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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// requireMFA checks the collaborators two-step verification uses: the Stores
// it reads and writes, its effects, mail, mechanics, authorization, attempt
// accounting and policy, the role holder requirement and the clock.
func (s *Service) requireMFA() error {
	switch {
	case s.users == nil:
		return errors.New("MFA user store is required")
	case s.mfaCredentials == nil:
		return errors.New("MFA store is required")
	case s.passwords == nil:
		return errors.New("MFA password credential reader is required")
	case s.sessions == nil:
		return errors.New("MFA session store is required")
	case s.institutions == nil:
		return errors.New("MFA institution store is required")
	case s.publisher == nil:
		return errors.New("MFA effects are required")
	case s.mail == nil:
		return errors.New("MFA mail preparer is required")
	case s.mfa == nil:
		return errors.New("MFA mechanics are required")
	case s.authorizer == nil || s.capabilities == nil || s.attempts == nil:
		return errors.New("MFA security dependencies are required")
	case s.loginRateLimit.MaximumAttempts <= 0 || s.loginRateLimit.MaximumSourceAttempts <= 0 || s.loginRateLimit.Window <= 0:
		return errors.New("MFA attempt policy is required")
	case s.accessPolicy == nil:
		// The Access Policy refuses disabling the second factor of a User it
		// requires to use two-step verification.
		return errors.New("MFA role holder policy is required")
	case s.recentAuthenticationTTL <= 0:
		return errors.New("MFA recent authentication TTL must be positive")
	case s.now == nil:
		return errors.New("MFA clock is required")
	}

	return nil
}

// GetMFAStatus reports the caller's two-step verification enrollment and
// Session assurance.
func (s *Service) GetMFAStatus(
	ctx context.Context,
	invocation invocation.Invocation,
) (*MFAStatus, error) {
	principal := invocation.Principal()
	if err := s.requireMFARecoverySession(principal, false); err != nil {
		return nil, err
	}

	now := s.now()
	status := &MFAStatus{
		ServiceEnabled:           s.mfa.deps.Settings.Enabled,
		MFARecoveryRequired:      principal.MFARecoveryRequired,
		MFAEnrollmentRequired:    principal.MFAEnrollmentRequired,
		AuthenticationMethod:     principal.AuthenticationMethod,
		AuthenticationProviderID: principal.AuthenticationProviderID,
		AuthenticationStrength:   principal.AuthenticationStrength,
		RecentlyAuthenticated:    principal.IsRecentlyAuthenticated(now, s.recentAuthenticationTTL),
		RecentAuthenticationTTL:  s.recentAuthenticationTTL,
	}
	if status.RecentlyAuthenticated {
		status.RecentlyAuthenticatedUntil = model.OptionalTimeFrom(principal.LastAuthenticationAt().Add(s.recentAuthenticationTTL))
	}

	password, err := s.passwords.GetByUser(ctx, principal.UserID)
	switch {
	case err == nil && password != nil:
		status.PasswordChangedAt = model.OptionalTimeFrom(password.PasswordChangedAt)
	case err != nil && !store.IsNotFound(err):
		return nil, mfaStoreErrors.Translate(err)
	}

	credential, err := s.mfaCredentials.GetByUser(ctx, principal.UserID)
	if store.IsNotFound(err) {
		return status, nil
	}
	if err != nil {
		return nil, mfaStoreErrors.Translate(err)
	}

	status.Enabled = credential.IsActive()
	status.Pending = credential.IsPendingAt(model.TimeUTC(now))
	status.PendingExpiresAt = credential.PendingExpiresAt
	if status.Enabled || status.Pending {
		status.Method = MFAMethodAuthenticatorApp
	}

	if status.Enabled {
		status.EnabledAt = credential.ActivatedAt
		summary, err := s.mfaCredentials.SummarizeRecoveryCodes(ctx, principal.UserID)
		if err != nil {
			return nil, mfaStoreErrors.Translate(err)
		}

		status.RecoveryCodesRemaining, status.RecoveryCodesTotal = summary.Remaining, summary.Issued
		if !summary.GeneratedAt.IsZero() {
			status.RecoveryCodesGeneratedAt = model.OptionalTimeFrom(summary.GeneratedAt)
		}
	}

	return status, nil
}

// SetupMFA starts a pending TOTP enrollment for the caller with a freshly
// sealed secret.
func (s *Service) SetupMFA(
	ctx context.Context,
	invocation invocation.Invocation,
	command SetupMFACommand,
) (*MFASetup, error) {
	principal := invocation.Principal()
	if err := s.requireMFARecoverySession(principal, true); err != nil {
		return nil, err
	}
	if err := s.requireMFAEnabled(); err != nil {
		return nil, err
	}

	user, err := s.users.Get(ctx, principal.UserID)
	if err != nil {
		return nil, mfaStoreErrors.Translate(err)
	}

	accountName := command.AccountName
	if strings.TrimSpace(accountName) == "" {
		accountName = user.Email
	}

	secret, err := s.mfa.newSecret()
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	sealed, err := s.mfa.sealTOTPSecret(principal.UserID.String(), secret)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	now := model.TimeUTC(s.now())
	candidate := &model.MFACredential{
		UserID:           principal.UserID,
		State:            model.MFAStatePending,
		EncryptedSecret:  sealed.encoded,
		EncryptionKeyID:  sealed.keyID,
		PendingExpiresAt: model.OptionalTimeFrom(now.Add(s.mfa.deps.Settings.SetupTTL)),
		CreatedAt:        now,
	}
	resource, appErr := s.mfaAuditResource(ctx)
	if appErr != nil {
		return nil, appErr
	}

	auditID, appErr := s.beginMFAAudit(
		ctx, invocation, model.ActionMFASetup, resource, nil,
	)
	if appErr != nil {
		return nil, appErr
	}

	saved, err := s.mfaCredentials.SavePending(ctx, &store.MFAPendingEnrollment{
		Principal:               principal,
		Credential:              candidate,
		Lifetime:                s.mfa.deps.Settings.SetupTTL,
		RecentAuthenticationTTL: s.recentAuthenticationTTL,
		AuditEventID:            auditID,
	})
	if err != nil {
		return nil, s.failMFAMutation(ctx, auditID, err)
	}

	return &MFASetup{
		Secret: secret,
		ProvisioningURI: mfaProvisioningURI(
			s.mfa.deps.Settings.Issuer, accountName, secret,
		),
		ExpiresAt: saved.PendingExpiresAt.Time,
	}, nil
}

// ActivateMFA activates the caller's pending enrollment with a TOTP code and
// returns the new recovery codes once.
func (s *Service) ActivateMFA(
	ctx context.Context,
	invocation invocation.Invocation,
	command ActivateMFACommand,
) (*MFAActivation, error) {
	principal := invocation.Principal()
	if err := s.requireMFARecoverySession(principal, true); err != nil {
		return nil, err
	}
	if err := s.requireMFAEnabled(); err != nil {
		return nil, err
	}
	if err := s.limitMFAFactorAttempt(ctx, invocation); err != nil {
		return nil, err
	}

	credential, err := s.mfaCredentials.GetByUser(ctx, principal.UserID)
	if err != nil {
		return nil, mfaStoreErrors.Translate(err)
	}

	now := model.TimeUTC(s.now())
	if !credential.IsPendingAt(now) {
		return nil, mfaInvalidCodeError()
	}

	secret, err := s.mfa.decrypt(principal.UserID.String(), credential)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	timeStep, valid := verifyTOTP(secret, command.Code, 0, now)
	if !valid {
		return nil, mfaInvalidCodeError()
	}

	rawCodes, recoveryCodes, err := s.mfa.newRecoveryCodes(
		principal.UserID.String(), s.mfa.deps.Settings.RecoveryCodeCount,
	)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	prepared, appErr := s.prepareMFANotice(
		ctx, principal.UserID, mail.MFANoticeEnabled, model.TimeUTC(now),
	)
	if appErr != nil {
		return nil, appErr
	}

	resource, appErr := s.mfaAuditResource(ctx)
	if appErr != nil {
		return nil, appErr
	}

	auditID, appErr := s.beginMFAAudit(
		ctx, invocation, model.ActionMFAActivate, resource, credential.Auditable(),
	)
	if appErr != nil {
		return nil, appErr
	}

	activated, err := s.mfaCredentials.Activate(ctx, &store.MFAActivationMutation{
		Principal:               principal,
		RecentAuthenticationTTL: s.recentAuthenticationTTL,
		CredentialID:            credential.ID,
		UserID:                  principal.UserID,
		TimeStep:                timeStep,
		RecoveryCodes:           recoveryCodes,
		SessionID:               principal.SessionID,
		At:                      now,
		AuditEventID:            auditID,
		AuditAt:                 now,
		Notice:                  mfaSecurityNotice(prepared),
	})
	if err != nil {
		return nil, s.failMFAMutation(ctx, auditID, err)
	}

	s.publisher.SessionsRevoked(
		ctx,
		principal.UserID,
		[]model.SessionID{principal.SessionID},
		activated.AccessTokenHashes,
	)
	return &MFAActivation{RecoveryCodes: rawCodes}, nil
}

// ChallengeMFA upgrades the caller's Session to multi-factor assurance with a
// TOTP or recovery code.
func (s *Service) ChallengeMFA(
	ctx context.Context,
	invocation invocation.Invocation,
	command ChallengeMFACommand,
) (*model.Session, error) {
	principal := invocation.Principal()
	// A Session the Access Policy restricts to MFA enrollment may satisfy the
	// policy with a challenge, for example after an external sign-in by a
	// User who already has a local second factor. Assisted recovery may not.
	if principal.MFARecoveryRequired {
		return nil, apperror.New("authentication.session_required")
	}
	if err := s.requireMFARecoverySession(principal, false); err != nil {
		return nil, err
	}
	if err := s.requireMFAEnabled(); err != nil {
		return nil, err
	}
	if err := s.limitMFAFactorAttempt(ctx, invocation); err != nil {
		return nil, err
	}

	resource, appErr := s.mfaAuditResource(ctx)
	if appErr != nil {
		return nil, appErr
	}

	auditID, appErr := s.beginMFAAudit(ctx, invocation, model.ActionMFAChallenge, resource, nil)
	if appErr != nil {
		return nil, appErr
	}

	now := model.TimeUTC(s.now())
	proof, err := s.mfa.verifySecondFactor(ctx, s.mfaCredentials, principal.UserID.String(), command.Code, now)
	if err != nil {
		code := "authentication.mfa.code_invalid"
		if failure, ok := apperror.As(err); ok {
			code = failure.Code()
		}
		if auditErr := s.completeMFAAudit(ctx, auditID, model.AuditStatusFail, code, nil); auditErr != nil {
			return nil, errors.Join(err, auditErr)
		}

		return nil, err
	}

	result, err := s.mfaCredentials.Challenge(ctx, &store.MFAChallenge{
		Principal:        principal,
		CredentialID:     proof.credentialID,
		TimeStep:         proof.step,
		RecoveryCodeHash: proof.recoveryHash,
		VerifiedAt:       now,
		AuditEventID:     auditID,
	})
	if err != nil {
		if store.IsNotFound(err) {
			if auditErr := s.completeMFAAudit(ctx, auditID, model.AuditStatusFail, "authentication.mfa.code_invalid", nil); auditErr != nil {
				return nil, errors.Join(mfaInvalidCodeError(), auditErr)
			}
			return nil, mfaInvalidCodeError()
		}

		return nil, s.failMFAMutation(ctx, auditID, err)
	}

	if result == nil || result.Session == nil {
		return nil, authenticationUnavailable(errors.New("MFA challenge returned no Session"))
	}

	s.publisher.SessionsRevoked(ctx, principal.UserID, []model.SessionID{principal.SessionID}, result.AccessTokenHashes)
	return result.Session, nil
}

// RegenerateMFARecoveryCodes replaces the caller's recovery codes and returns
// the new set once.
func (s *Service) RegenerateMFARecoveryCodes(
	ctx context.Context,
	invocation invocation.Invocation,
) ([]string, error) {
	principal := invocation.Principal()
	if err := s.requireMFAStrongRecentSession(principal); err != nil {
		return nil, err
	}
	if err := s.requireMFAEnabled(); err != nil {
		return nil, err
	}

	rawCodes, codes, err := s.mfa.newRecoveryCodes(
		principal.UserID.String(), s.mfa.deps.Settings.RecoveryCodeCount,
	)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	now := model.TimeUTC(s.now())
	prepared, appErr := s.prepareMFANotice(
		ctx, principal.UserID, mail.MFANoticeRecoveryCodesRegenerated, now,
	)
	if appErr != nil {
		return nil, appErr
	}

	resource, appErr := s.mfaAuditResource(ctx)
	if appErr != nil {
		return nil, appErr
	}

	auditID, appErr := s.beginMFAAudit(
		ctx, invocation, model.ActionMFARecoveryCodesRegenerate, resource, nil,
	)
	if appErr != nil {
		return nil, appErr
	}

	if err := s.mfaCredentials.ReplaceRecoveryCodes(ctx, &store.MFARecoveryCodesRegeneration{
		Principal:               principal,
		RecentAuthenticationTTL: s.recentAuthenticationTTL,
		UserID:                  principal.UserID,
		RecoveryCodes:           codes,
		At:                      now,
		AuditEventID:            auditID,
		AuditAt:                 now,
		Notice:                  mfaSecurityNotice(prepared),
	}); err != nil {
		return nil, s.failMFAMutation(
			ctx, auditID, err)

	}

	return rawCodes, nil
}

// DisableMFA removes the caller's second factor unless the Access Policy
// requires it.
func (s *Service) DisableMFA(
	ctx context.Context,
	invocation invocation.Invocation,
) error {
	principal := invocation.Principal()
	if err := s.requireMFAStrongRecentSession(principal); err != nil {
		return err
	}
	if err := s.requireMFAEnabled(); err != nil {
		return err
	}

	now := model.TimeUTC(s.now())
	prepared, appErr := s.prepareMFANotice(
		ctx, principal.UserID, mail.MFANoticeDisabled, now,
	)
	if appErr != nil {
		return appErr
	}

	resource, appErr := s.mfaAuditResource(ctx)
	if appErr != nil {
		return appErr
	}

	auditID, appErr := s.beginMFAAudit(ctx, invocation, model.ActionMFADisable, resource, nil)
	if appErr != nil {
		return appErr
	}

	required, err := s.accessPolicy.RequiresMFA(ctx, principal.UserID)
	if err != nil {
		return s.failMFAMutation(ctx, auditID, err)
	}

	if required {
		refused := apperror.New("mfa.required_by_policy")
		if auditErr := s.completeMFAAudit(ctx, auditID, model.AuditStatusFail, refused.Code(), nil); auditErr != nil {
			return errors.Join(refused, auditErr)
		}

		return refused
	}

	result, err := s.mfaCredentials.Disable(ctx, &store.MFADisablement{
		Principal:               principal,
		RecentAuthenticationTTL: s.recentAuthenticationTTL,
		UserID:                  principal.UserID,
		At:                      now,
		AuditEventID:            auditID,
		AuditAt:                 now,
		Notice:                  mfaSecurityNotice(prepared),
	})
	if err != nil {
		return s.failMFAMutation(ctx, auditID, err)
	}

	s.publisher.SessionsRevoked(
		ctx, principal.UserID, nil, result.AccessTokenHashes,
	)
	return nil
}

func (s *Service) prepareMFANotice(
	ctx context.Context,
	userID model.UserID,
	kind mail.MFANoticeKind,
	at time.Time,
) (*store.PreparedMail, error) {
	user, err := s.users.Get(ctx, userID)
	if err != nil {
		return nil, mfaStoreErrors.Translate(err)
	}
	prepared, err := s.mail.PrepareMFANotice(mail.NoticePreparation{Recipient: user, At: at}, kind)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	return prepared, nil
}

func mfaSecurityNotice(prepared *store.PreparedMail) store.MFASecurityNotice {
	if prepared == nil {
		return store.MFASecurityNotice{}
	}
	return store.MFASecurityNotice{
		Occurrence: prepared.Occurrence,
		Delivery:   prepared.Delivery,
		Job:        prepared.Job,
	}
}

// verifyLoginMFA verifies and consumes the second factor of a local
// password sign-in. A User without an active credential signs in with one
// factor.
func (s *Service) verifyLoginMFA(
	ctx context.Context,
	userID string,
	code string,
	at time.Time,
) (model.AuthenticationStrength, int64, error) {
	credential, err := s.mfaCredentials.GetByUser(ctx, model.UserID(userID))
	if store.IsNotFound(err) {
		return model.AuthenticationSingleFactor, 0, nil
	}
	if err != nil {
		return "", 0, authenticationUnavailable(err)
	}
	if !credential.IsActive() {
		return model.AuthenticationSingleFactor, 0, nil
	}
	if !s.mfa.deps.Settings.Enabled {
		return "", 0, apperror.New("authentication.mfa.unavailable")
	}
	if strings.TrimSpace(code) == "" {
		return "", 0, apperror.New("authentication.mfa.required")
	}
	if appErr := s.consumeLoginSecondFactor(ctx, userID, code, at); appErr != nil {
		return "", 0, appErr
	}
	return model.AuthenticationMultiFactor, at.UnixMilli(), nil
}

// consumeLoginSecondFactor consumes the sign-in factor against the MFA Store.
func (s *Service) consumeLoginSecondFactor(
	ctx context.Context,
	userID string,
	code string,
	now time.Time,
) error {
	return s.mfa.consumeSecondFactor(ctx, s.mfaCredentials, userID, code, now)
}

func (s *Service) requireMFAEnabled() error {
	if s.mfa.deps.Settings.Enabled {
		return nil
	}
	return apperror.New("authentication.mfa.disabled")
}

func (s *Service) requireMFAInteractiveSession(
	principal model.Principal,
	recent bool,
) error {
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return apperror.New("authentication.session_required")
	}
	if recent && !principal.IsRecentlyAuthenticated(s.now(), s.recentAuthenticationTTL) {
		return apperror.New("authentication.reauthentication_required")
	}
	return nil
}

func (s *Service) requireMFAStrongRecentSession(principal model.Principal) error {
	if err := s.requireMFAInteractiveSession(principal, true); err != nil {
		return err
	}
	if !principal.HasStrongAuthentication() {
		return apperror.New("authentication.strong_required")
	}
	return nil
}

func (s *Service) mfaAuditResource(ctx context.Context) (model.Resource, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return model.Resource{}, mfaStoreErrors.Translate(err)
	}
	return model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}, nil
}

func (s *Service) failMFAMutation(
	ctx context.Context,
	auditID model.AuditEventID,
	err error,
) error {
	mapped := mfaStoreErrors.Translate(err)
	code := "authentication.mfa.unavailable"
	if failure, ok := apperror.As(mapped); ok {
		code = failure.Code()
	}
	if auditErr := s.completeMFAAudit(
		ctx, auditID, model.AuditStatusFail, code, nil,
	); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}

// beginMFAAudit records the attempt of an MFA mutation. Self-service MFA
// mutations are audited against the Institution, with the acting User as
// their subject. An administrator reset names the target User as its
// resource, which is its own subject.
func (s *Service) beginMFAAudit(
	ctx context.Context,
	invocation invocation.Invocation,
	action model.Action,
	resource model.Resource,
	prior any,
) (model.AuditEventID, error) {
	if s.securityAudit == nil {
		return "", access.AuditUnavailable(errors.New("MFA audit service is unavailable"))
	}

	var subject model.UserID
	if resource.Type != model.ResourceUser {
		subject = invocation.Principal().UserID
	}

	event, err := s.securityAudit.BeginCriticalActionForSubject(
		ctx,
		invocation.Principal(),
		subject,
		action,
		resource,
		invocation.RequestMetadata(),
		nil,
		prior,
	)
	if err != nil {
		return "", err
	}

	return event.ID, nil
}

// completeMFAAudit records the terminal outcome of an MFA mutation the
// Service itself, rather than its atomic Store operation, ends.
func (s *Service) completeMFAAudit(
	ctx context.Context,
	auditID model.AuditEventID,
	status model.AuditStatus,
	errorCode string,
	result any,
) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("MFA audit service is unavailable"))
	}
	_, err := s.securityAudit.CompleteCriticalAction(ctx, auditID, status, errorCode, result)
	return err
}

// mfaRecoveryState is the two-step verification recovery state sign-in and
// external authentication read for a User before issuing a Session.
func (s *Service) mfaRecoveryState(ctx context.Context, userID model.UserID) (*model.UserMFARecovery, error) {
	state, err := s.mfaCredentials.GetRecoveryState(ctx, userID)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	if state.ReenrollmentRequired && !s.mfa.deps.Settings.Enabled {
		return nil, apperror.New("authentication.mfa.unavailable")
	}
	return state, nil
}

func (s *Service) requireMFARecoverySession(principal model.Principal, recent bool) error {
	if principal.ValidateMFARecovery() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return apperror.New("authentication.session_required")
	}
	if recent && !principal.IsRecentlyAuthenticated(s.now(), s.recentAuthenticationTTL) {
		return apperror.New("authentication.reauthentication_required")
	}
	return nil
}

func (s *Service) limitMFAFactorAttempt(ctx context.Context, invocation invocation.Invocation) error {
	_, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeMFA,
		window:  s.loginRateLimit.Window,
		limits: []AttemptLimit{
			{
				dimension: authenticationAttemptDimensionIdentity,
				maximum:   s.loginRateLimit.MaximumAttempts,
				identity:  invocation.Principal().UserID.String(),
			},
			{
				dimension: authenticationAttemptDimensionSource,
				maximum:   s.loginRateLimit.MaximumSourceAttempts,
				source:    invocation.RequestMetadata().IPAddress,
			},
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
