// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"errors"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RegisterLocalUserCommand contains the public, request-only account fields.
// Password and mailbox values never enter audit data or successful responses.
type RegisterLocalUserCommand struct {
	Username  string
	Email     string
	FirstName string
	LastName  string
	Password  string
	Source    string
}

// RegisterLocalUser registers an unverified local account when the Access Policy
// allows public registration, mailing its email verification. A duplicate
// identity is indistinguishable from accepted work.
func (s *Service) RegisterLocalUser(ctx context.Context, inv invocation.Invocation, command RegisterLocalUserCommand) error {
	registrationEnabled, localLoginEnabled, err := s.publicRegistrationPolicy(ctx)
	if err != nil {
		return registrationUnavailable(err)
	}
	if !registrationEnabled || !localLoginEnabled {
		return apperror.New("authentication.registration.invitation_required")
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(model.SanitizeUnicode(command.Email)))
	if err = s.checkPublicRegistrationAttempt(ctx, normalizedEmail, command.Source); err != nil {
		return err
	}
	if !s.mail.Enabled() {
		return registrationUnavailable(errors.New("mail delivery is disabled"))
	}

	firstName := strings.TrimSpace(model.SanitizeUnicode(command.FirstName))
	lastName := strings.TrimSpace(model.SanitizeUnicode(command.LastName))
	if firstName == "" || lastName == "" {
		return apperror.New("authentication.registration.invalid")
	}

	at := model.TimeUTC(s.now())
	account, defaultJob, err := jobs.PrepareUserDefaultProfilePictureJob(&model.User{
		Username:  command.Username,
		Email:     command.Email,
		FirstName: firstName,
		LastName:  lastName,
	}, at)
	if err != nil {
		return apperror.New("authentication.registration.invalid").Wrap(err)
	}

	if err = identity.CheckNewPassword(command.Password, identity.PasswordIdentity{
		Username:  account.Username,
		Email:     account.Email,
		FirstName: account.FirstName,
		LastName:  account.LastName,
	}); err != nil {
		return err
	}

	passwordHash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return identity.PasswordHashError(err, "authentication.registration.unavailable")
	}

	credential := &model.PasswordCredential{UserID: account.ID, PasswordHash: passwordHash}
	credential.PrepareCreate(model.NewPasswordCredentialID(), at)
	settings, err := user.PrepareInitialSettingsDocument(account)
	if err != nil {
		return registrationUnavailable(err)
	}

	rawToken := s.newToken()
	token := &model.UserToken{
		UserID:    account.ID,
		Purpose:   model.UserTokenEmailVerification,
		TokenHash: model.HashToken(rawToken),
		Target:    account.Email,
		ExpiresAt: at.Add(s.emailVerificationTTL),
	}
	token.PrepareCreate(model.NewUserTokenID(), at)
	actionURL, err := identity.AccountCredentialLink(s.publicURL, "/account/verify-email", rawToken)
	if err != nil {
		return registrationUnavailable(err)
	}

	prepared, err := s.mail.PreparePublicRegistrationVerification(
		account, model.MailOccurrenceID(token.ID.String()), actionURL, at, token.ExpiresAt,
	)
	if err != nil {
		return registrationUnavailable(err)
	}

	institutionID, err := s.institutionID(ctx)
	if err != nil {
		return registrationUnavailable(err)
	}

	audit := identity.RecoveryAuditEvent(
		model.ActionAuthenticationPublicRegistration,
		model.Resource{Type: model.ResourceUser, ID: account.ID.String()},
		institutionID,
		inv.RequestMetadata(),
		s.nodeID,
		nil,
		"anonymous",
	)
	_, err = s.users.RegisterLocal(ctx, &store.PublicLocalUserRegistration{
		User:                     account,
		Settings:                 settings,
		PasswordCredential:       credential,
		DefaultProfilePictureJob: defaultJob,
		VerificationToken:        token,
		TokenLifetime:            s.emailVerificationTTL,
		MailLifetime:             s.emailVerificationTTL,
		VerificationOccurrence:   prepared.Occurrence,
		VerificationDelivery:     prepared.Delivery,
		VerificationJob:          prepared.Job,
		AuditEvent:               audit,
	})
	if err == nil {
		return nil
	}

	if store.IsUserIdentityConflict(err) {
		// A syntactically valid duplicate is indistinguishable from accepted work.
		return nil
	}

	if errors.Is(err, store.ErrAuthenticationMethodDisabled) || store.IsNotFound(err) {
		return apperror.New("authentication.registration.invitation_required")
	}

	return registrationUnavailable(err)
}

func (s *Service) checkPublicRegistrationAttempt(ctx context.Context, registrantIdentity, source string) error {
	limited, err := s.attempts.Account(
		ctx,
		identity.AttemptPurposePublicRegistration,
		"register",
		s.rateLimit.Window,
		identity.IdentityAttemptLimit(registrantIdentity, s.rateLimit.MaximumAttempts),
		identity.SourceAttemptLimit(source, s.rateLimit.MaximumSourceAttempts),
	)
	if err != nil {
		return apperror.New("authentication.rate_limit_unavailable").Wrap(err)
	}
	if limited {
		return apperror.New("authentication.rate_limited")
	}

	return nil
}

func registrationUnavailable(err error) error {
	return apperror.New("authentication.registration.unavailable").Wrap(err)
}

// publicRegistrationPolicy reads whether the current Access Policy allows
// public registration and local login.
func (s *Service) publicRegistrationPolicy(ctx context.Context) (enabled bool, localLogin bool, err error) {
	snapshot, err := s.accessPolicies.Get(ctx, 0)
	if err != nil {
		return false, false, err
	}
	if snapshot == nil || snapshot.Policy == nil || snapshot.Policy.Validate() != nil {
		return false, false, errors.New("current access policy is invalid")
	}
	return snapshot.Policy.PublicRegistrationEnabled, snapshot.Policy.LocalLoginEnabled, nil
}

// institutionID reads the singleton Institution's identifier; an absent or
// invalid Institution yields an empty identifier and the Store's error.
func (s *Service) institutionID(ctx context.Context) (model.InstitutionID, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return "", err
	}
	return institution.ID, nil
}
