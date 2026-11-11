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
	"sort"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuthenticationMethodView lists how the current User can sign in: whether
// they hold a local password, and each linked External Identity.
type AuthenticationMethodView struct {
	Password  bool
	Providers []AuthenticationProviderMethodView
}

// AuthenticationProviderMethodView is one linked External Identity and the
// provider it signs in with.
type AuthenticationProviderMethodView struct {
	IdentityID  model.ExternalIdentityID
	ProviderID  string
	DisplayName string
	Type        string
}

// ChangePasswordCommand replaces the current User's existing local password.
type ChangePasswordCommand struct {
	Password string
}

// requireAuthenticationMethods refuses a Service whose authentication method
// management collaborators are incomplete. New calls it once, after external
// authentication is set up.
func (s *Service) requireAuthenticationMethods() error {
	if s.users == nil ||
		s.mail == nil ||
		s.passwords == nil ||
		s.externalIdentities == nil ||
		s.externalProviders == nil ||
		s.capabilities == nil ||
		s.hasher == nil ||
		s.auditor == nil ||
		s.publisher == nil ||
		s.recentAuthenticationTTL <= 0 ||
		s.now == nil {
		return errors.New("authentication method service dependencies are required")
	}

	return nil
}

// requireAuthenticationMethodSession admits a change to the caller's own
// authentication methods only from a strong, recently authenticated Session.
func (s *Service) requireAuthenticationMethodSession(inv invocation.Invocation) error {
	return access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL)
}

// AuthenticationMethods lists the current User's local password and linked
// External Identities, ordered by identity.
func (s *Service) AuthenticationMethods(ctx context.Context, inv invocation.Invocation) (AuthenticationMethodView, error) {
	if inv.Principal().Validate() != nil {
		return AuthenticationMethodView{}, access.InvalidTokenAppError()
	}

	userID := inv.Principal().UserID
	view := AuthenticationMethodView{Providers: []AuthenticationProviderMethodView{}}
	if _, err := s.passwords.GetByUser(ctx, userID); err == nil {
		view.Password = true
	} else if !store.IsNotFound(err) {
		return view, authenticationUnavailable(err)
	}

	identities, err := s.externalIdentities.ListByUser(ctx, userID)
	if err != nil {
		return view, authenticationUnavailable(err)
	}

	descriptors := map[string]model.ExternalAuthenticationProvider{}
	for _, descriptor := range s.externalProviders.Descriptors() {
		descriptors[descriptor.ID] = descriptor
	}

	for _, identity := range identities {
		descriptor, ok := descriptors[identity.Provider]
		if !ok {
			descriptor = model.ExternalAuthenticationProvider{ID: identity.Provider, DisplayName: identity.Provider}
		}

		view.Providers = append(view.Providers, AuthenticationProviderMethodView{
			IdentityID:  identity.ID,
			ProviderID:  identity.Provider,
			DisplayName: descriptor.DisplayName,
			Type:        descriptor.Type,
		})
	}

	sort.Slice(view.Providers, func(i, j int) bool {
		return view.Providers[i].IdentityID.String() < view.Providers[j].IdentityID.String()
	})
	return view, nil
}

// EnrollPassword adds a local password to the current User after strong
// recent authentication.
func (s *Service) EnrollPassword(ctx context.Context, inv invocation.Invocation, password string) error {
	if err := s.requireAuthenticationMethodSession(inv); err != nil {
		return err
	}

	userID := inv.Principal().UserID
	user, err := s.users.Get(ctx, userID)
	if err != nil {
		return authenticationMethodStoreErrors.Translate(err)
	}
	if err = CheckNewPassword(password, passwordIdentityOf(user)); err != nil {
		return err
	}

	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return PasswordHashError(err, "authentication.internal")
	}

	_, appErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionExternalIdentityManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  "enroll_password",
			Value:      map[string]any{"method": "password"},
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.AuthenticationMethodMutationResult, error) {
			return s.passwords.Enroll(ctx, &store.PasswordCredentialEnrollment{
				Credential:   &model.PasswordCredential{UserID: userID, PasswordHash: hash},
				Reuse:        s.hasher.ReuseCheck(password),
				Capabilities: AccessDeploymentCapabilities(s.capabilities.Snapshot()),
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		authenticationMethodStoreErrors.Translate,
	)
	return appErr
}

// ChangePassword replaces the current User's existing local password after
// strong recent authentication. The calling Session stays live; every other
// Session of the User is revoked, outstanding password-reset links stop
// working, and the account address receives the password-changed notice.
func (s *Service) ChangePassword(ctx context.Context, inv invocation.Invocation, command ChangePasswordCommand) error {
	if err := s.requireAuthenticationMethodSession(inv); err != nil {
		return err
	}

	principal := inv.Principal()
	user, err := s.users.Get(ctx, principal.UserID)
	if err != nil {
		return authenticationMethodStoreErrors.Translate(err)
	}
	if err = CheckNewPassword(command.Password, passwordIdentityOf(user)); err != nil {
		return err
	}

	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return PasswordHashError(err, "authentication.internal")
	}

	prepared, err := s.mail.PreparePasswordChanged(mail.NoticePreparation{Recipient: user, At: model.TimeUTC(s.now())})
	if err != nil {
		return apperror.New("authentication.method.unavailable").Wrap(err)
	}

	result, appErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionExternalIdentityManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: user.ID.String()},
			Operation:  "change_password",
			Value:      map[string]any{"method": "password"},
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.AuthenticationMethodMutationResult, error) {
			return s.passwords.Change(ctx, &store.PasswordCredentialChange{
				UserID:           user.ID,
				KeepSessionID:    principal.SessionID,
				PasswordHash:     hash,
				Reuse:            s.hasher.ReuseCheck(command.Password),
				RevocationReason: model.SessionRevocationPasswordChanged,
				Occurrence:       prepared.Occurrence,
				Delivery:         prepared.Delivery,
				Job:              prepared.Job,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			})
		},
		authenticationMethodStoreErrors.Translate,
	)
	if appErr == nil {
		s.publishAuthenticationMethodRevocations(ctx, user.ID, result)
	}

	return appErr
}

// RemovePassword removes the current User's local password after strong
// recent authentication and publishes the Session revocations the Store
// reports.
func (s *Service) RemovePassword(ctx context.Context, inv invocation.Invocation) error {
	if err := s.requireAuthenticationMethodSession(inv); err != nil {
		return err
	}

	userID := inv.Principal().UserID
	result, appErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionExternalIdentityManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  "remove_password",
			Value:      map[string]any{"method": "password"},
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.AuthenticationMethodMutationResult, error) {
			return s.passwords.Remove(ctx, &store.PasswordCredentialRemoval{
				UserID:           userID,
				Capabilities:     AccessDeploymentCapabilities(s.capabilities.Snapshot()),
				ChangedAt:        reference.MutationAt,
				RevocationReason: model.SessionRevocationPasswordRemoved,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			})
		},
		authenticationMethodStoreErrors.Translate,
	)
	if appErr == nil {
		s.publishAuthenticationMethodRevocations(ctx, userID, result)
	}

	return appErr
}

// UnlinkExternalIdentity removes one of the current User's linked External
// Identities after strong recent authentication and publishes the Session
// revocations the Store reports.
func (s *Service) UnlinkExternalIdentity(ctx context.Context, inv invocation.Invocation, id model.ExternalIdentityID) error {
	if err := s.requireAuthenticationMethodSession(inv); err != nil {
		return err
	}
	if !id.IsValid() {
		return apperror.New("request.invalid")
	}

	userID := inv.Principal().UserID
	result, appErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionExternalIdentityManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  "unlink_provider",
			Value:      map[string]any{"identity_id": id.String()},
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.AuthenticationMethodMutationResult, error) {
			return s.externalIdentities.Unlink(ctx, &store.ExternalIdentityUnlink{
				ID:               id,
				UserID:           userID,
				Capabilities:     AccessDeploymentCapabilities(s.capabilities.Snapshot()),
				ChangedAt:        reference.MutationAt,
				RevocationReason: model.SessionRevocationExternalIdentityUnlinked,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			})
		},
		authenticationMethodStoreErrors.Translate,
	)
	if appErr == nil {
		s.publishAuthenticationMethodRevocations(ctx, userID, result)
	}

	return appErr
}

func (s *Service) publishAuthenticationMethodRevocations(
	ctx context.Context,
	userID model.UserID,
	result *store.AuthenticationMethodMutationResult,
) {
	if result == nil {
		return
	}

	s.publisher.SessionsRevoked(ctx, userID, sessionIDs(result.RevokedSessions), result.RevokedTokenHashes)
}

var authenticationMethodStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationMethodDisabled, Code: "authentication.method.disabled"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrLastUsableAuthenticationMethod, Code: "authentication.method.last_usable"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrPasswordReused, Code: "authentication.password.reused"},
	},
	Constraints: map[string]apperror.Code{"external_identities_provider_subject_key": "authentication.method.provider_conflict"},
	NotFound:    "authentication.method.not_found",
	Conflict:    "authentication.method.conflict",
	Unavailable: "authentication.method.unavailable",
}
