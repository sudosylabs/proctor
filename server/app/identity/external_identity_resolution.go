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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// externalProvisioning is the prepared auto-provisioning payload for an unlinked external identity.
type externalProvisioning struct {
	user              *model.User
	defaultPictureJob *model.Job
	settings          *model.UserSettingsDocument
	audit             *model.AuditEvent
}

// prepareExternalProvisioning builds the User, settings, picture Job and audit event used when the provider auto-provisions.
func (s *Service) prepareExternalProvisioning(
	attempt externalCompletionAttempt, assertion *model.ExternalAuthenticationAssertion, nowTime time.Time,
) (externalProvisioning, error) {
	var provisioning externalProvisioning
	var err error
	provisioning.user = ExternalUserCandidate(assertion)
	provisioning.user, provisioning.defaultPictureJob, err = jobs.PrepareUserDefaultProfilePictureJob(provisioning.user, nowTime)
	if err != nil {
		return externalProvisioning{}, authenticationUnavailable(err)
	}

	provisioning.settings, err = user.PrepareInitialSettingsDocument(provisioning.user)
	if err != nil {
		return externalProvisioning{}, authenticationUnavailable(err)
	}

	provisionParameters, appErr := model.EncodeAuditData(map[string]string{
		"provider": attempt.providerID,
	})
	if appErr != nil {
		return externalProvisioning{}, appErr
	}

	provisioning.audit = &model.AuditEvent{
		Action:     string(model.ActionAuthenticationExternalProvision),
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    attempt.institution.ID.String(),
		Status:     model.AuditStatusSuccess,
		RequestID:  attempt.metadata.RequestID,
		NodeID:     s.externalAuthenticationPolicy.NodeID,
		ClientType: string(attempt.state.ClientType),
		AuthMethod: attempt.method,
		IPAddress:  attempt.metadata.IPAddress,
		UserAgent:  attempt.metadata.UserAgent,
		Parameters: provisionParameters,
	}
	return provisioning, nil
}

// resolveExternalIdentity resolves or provisions the linked User and rejects invalid provenance or inactive accounts.
func (s *Service) resolveExternalIdentity(
	ctx context.Context,
	attempt externalCompletionAttempt,
	admission externalProviderAdmissionPolicy,
	provider ExternalIdentityProvider,
	assertion *model.ExternalAuthenticationAssertion,
	nowTime time.Time,
) (*store.ExternalIdentityResolution, error) {
	now := nowTime.UnixMilli()
	var provisioning externalProvisioning
	autoProvision := attempt.state.Purpose != model.ExternalAuthenticationPurposeMFARecovery &&
		admission.Mode == model.ProviderAdmissionAutoProvision &&
		provider.AutoProvision()
	if autoProvision {
		var err error
		provisioning, err = s.prepareExternalProvisioning(attempt, assertion, nowTime)
		if err != nil {
			return nil, err
		}
	}

	resolution, err := s.externalIdentities.ResolveOrProvision(
		ctx, &store.ExternalIdentityResolutionRequest{
			Identity: &model.ExternalIdentity{
				Provider:   attempt.providerID,
				Subject:    assertion.Subject,
				LastSeenAt: model.OptionalTimeFromMillis(now),
			},
			User:                     provisioning.user,
			Settings:                 provisioning.settings,
			Capabilities:             AccessDeploymentCapabilities(s.capabilities.Snapshot()),
			AuditEvent:               provisioning.audit,
			DefaultProfilePictureJob: provisioning.defaultPictureJob,
		},
	)
	if err != nil {
		return nil, s.identityResolutionFailure(ctx, attempt, err)
	}

	if resolution == nil ||
		resolution.User == nil ||
		resolution.Identity == nil ||
		!resolution.Identity.ID.IsValid() ||
		resolution.Identity.UserID != resolution.User.ID ||
		resolution.Identity.Provider != attempt.providerID {
		if auditErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.internal"); auditErr != nil {
			return nil, auditErr
		}
		return nil, authenticationUnavailable(errors.New("external identity resolution returned invalid provenance"))
	}

	if !resolution.User.IsActive() {
		if appErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.inactive_account"); appErr != nil {
			return nil, appErr
		}
		return nil, invalidExternalAuthenticationError()
	}

	return resolution, nil
}

// identityResolutionFailure audits a failed identity resolution and maps it to the public error.
func (s *Service) identityResolutionFailure(ctx context.Context, attempt externalCompletionAttempt, err error) error {
	var conflict *store.ErrConflict
	errorCode := "authentication.external.internal"
	if errors.Is(err, store.ErrAuthenticationMethodDisabled) {
		errorCode = "authentication.external.invalid"
	} else if errors.As(err, &conflict) {
		errorCode = "authentication.external.account_conflict"
	} else if store.IsNotFound(err) {
		errorCode = "authentication.external.account_not_linked"
	}
	if auditErr := s.recordExternalCompletionFailure(ctx, attempt, errorCode); auditErr != nil {
		return auditErr
	}
	switch {
	case errors.Is(err, store.ErrAuthenticationMethodDisabled):
		return invalidExternalAuthenticationError()
	case errors.As(err, &conflict):
		return apperror.New("authentication.external.account_conflict").Wrap(err)
	case store.IsNotFound(err):
		return apperror.New("authentication.external.account_not_linked")
	default:
		return authenticationUnavailable(err)
	}
}

// ExternalUserCandidate proposes the User an External Identity assertion would
// provision; Invitation acceptance reuses it.
func ExternalUserCandidate(
	assertion *model.ExternalAuthenticationAssertion,
) *model.User {
	displayName := strings.TrimSpace(assertion.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(
			assertion.FirstName + " " + assertion.LastName,
		)
	}

	return &model.User{
		Username:      assertion.Username,
		Email:         assertion.Email,
		EmailVerified: assertion.EmailVerified,
		DisplayName:   displayName,
		FirstName:     assertion.FirstName,
		LastName:      assertion.LastName,
	}
}
