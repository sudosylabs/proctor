// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/email/email.go and
// authentication.go. Proctor uses purpose-specific hashed credentials,
// target-bound single-use consumption, generic public recovery responses,
// fail-closed rate limiting, and durable transactional security audits.

package identity

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

type accountRecoveryAttemptOperation uint8

const (
	accountRecoveryAttemptEmailVerificationRequest accountRecoveryAttemptOperation = iota + 1
	accountRecoveryAttemptPasswordResetRequest
	accountRecoveryAttemptEmailVerificationComplete
	accountRecoveryAttemptPasswordResetComplete
	accountRecoveryAttemptEmailChangeNotice
	accountRecoveryAttemptEmailChangeUndoComplete
	accountRecoveryAttemptEmailVerificationInspect
	accountRecoveryAttemptEmailChangeUndoInspect
)

func (o accountRecoveryAttemptOperation) qualifier() (string, bool) {
	switch o {
	case accountRecoveryAttemptEmailVerificationRequest:
		return "email-verification-request", true
	case accountRecoveryAttemptPasswordResetRequest:
		return "password-reset-request", true
	case accountRecoveryAttemptEmailVerificationComplete:
		return "email-verification-complete", true
	case accountRecoveryAttemptPasswordResetComplete:
		return "password-reset-complete", true
	case accountRecoveryAttemptEmailChangeNotice:
		return "email-change-notice", true
	case accountRecoveryAttemptEmailChangeUndoComplete:
		return "email-change-undo-complete", true
	case accountRecoveryAttemptEmailVerificationInspect:
		return "email-verification-inspect", true
	case accountRecoveryAttemptEmailChangeUndoInspect:
		return "email-change-undo-inspect", true
	default:
		return "", false
	}
}

// requireAccountTokens checks the collaborators of purpose-specific
// verification and recovery tokens. Terminal transitions and their success
// audits remain named atomic store operations; only post-commit session
// effects happen outside persistence.
func (s *Service) requireAccountTokens() error {
	required := []struct {
		missing bool
		name    string
	}{
		{s.users == nil, "user store"},
		{s.passwords == nil, "password credential store"},
		{s.userTokens == nil, "user token store"},
		{s.accessPolicy == nil, "authentication access policy"},
		{s.institutions == nil, "institution store"},
		{s.mail == nil, "account mail preparer"},
		{s.attempts == nil, "authentication attempt accounting"},
		{s.hasher == nil, "password hasher"},
		{s.nodeID == "", "account recovery audit"},
		{s.publisher == nil, "account recovery effects"},
		{s.recoveryDiagnostics == nil, "account recovery diagnostics"},
		{s.newCredential == nil, "account token generator"},
		{s.now == nil, "account token clock"},
	}
	for _, dependency := range required {
		if dependency.missing {
			return errors.New(dependency.name + " is required")
		}
	}
	return nil
}

// recoveryAuditEvent is the success audit an account-token transition commits
// with its atomic Store operation, recorded on this node.
func (s *Service) recoveryAuditEvent(
	action model.Action,
	resource model.Resource,
	institutionID model.InstitutionID,
	inv invocation.Invocation,
	principal *model.Principal,
	authenticationMethod string,
) *model.AuditEvent {
	return RecoveryAuditEvent(
		action,
		resource,
		institutionID,
		inv.RequestMetadata(),
		s.nodeID,
		principal,
		authenticationMethod,
	)
}

func (s *Service) accountRecoveryInstitution(ctx context.Context) (*model.Institution, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, accountRecoveryStoreFailure(err)
	}
	return institution, nil
}

func (s *Service) checkAccountRecoveryRateLimit(
	ctx context.Context,
	operation accountRecoveryAttemptOperation,
	identity string,
	source string,
) error {
	qualifier, valid := operation.qualifier()
	if !valid {
		return RateLimitUnavailableAppError(fmt.Errorf("account recovery attempt operation is invalid"))
	}
	settings := s.accountRecoveryPolicy.RateLimit
	_, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose:   authenticationAttemptPurposeAccountRecovery,
		qualifier: qualifier,
		window:    settings.Window,
		limits: []AttemptLimit{
			{
				dimension: authenticationAttemptDimensionIdentity,
				maximum:   settings.MaximumAttempts,
				identity:  identity,
			},
			{
				dimension: authenticationAttemptDimensionSource,
				maximum:   settings.MaximumSourceAttempts,
				source:    source,
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

func recoveryCredentialRateIdentity(rawToken string) string {
	return model.HashToken(rawToken)
}

// RecoveryAuditEvent builds the success audit event of an unauthenticated
// account-credential flow; public registration records it too.
func RecoveryAuditEvent(
	action model.Action,
	resource model.Resource,
	institutionID model.InstitutionID,
	metadata model.RequestMetadata,
	nodeID string,
	principal *model.Principal,
	authenticationMethod string,
) *model.AuditEvent {
	event := &model.AuditEvent{
		Action:     string(action),
		Resource:   resource,
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institutionID.String(),
		Status:     model.AuditStatusSuccess,
		RequestID:  metadata.RequestID,
		NodeID:     nodeID,
		AuthMethod: authenticationMethod,
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
	}
	if principal != nil {
		event.ActorID = principal.UserID
		event.SessionID = principal.SessionID
		event.ClientType = string(principal.ClientType)
		event.AuthMethod = principal.AuthenticationMethod
	}

	return event
}

// AccountCredentialLink builds the hosted link that carries a raw account
// credential, such as an Invitation claim or an email verification token.
func AccountCredentialLink(
	publicURL string,
	path string,
	rawToken string,
) (string, error) {
	base, err := url.Parse(publicURL)
	if err != nil || !base.IsAbs() || base.Host == "" {
		return "", fmt.Errorf("invalid public URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = "token=" + url.QueryEscape(rawToken)
	return base.String(), nil
}

func (s *Service) logHiddenRecoveryFailure(
	ctx context.Context,
	message string,
	err error,
) {
	s.recoveryDiagnostics.ErrorContext(ctx, message, logging.Err(err))
}

func invalidAccountCredential() error {
	return apperror.New("authentication.account_token.invalid")
}

func accountRecoveryStoreFailure(err error) error {
	return apperror.New("authentication.account_recovery.unavailable").Wrap(err)
}

func accountRecoveryUnavailable(err error) error {
	return apperror.New("authentication.account_recovery.unavailable").Wrap(err)
}

// AccountRecoveryPolicy is the operator projection for email verification and
// password reset TTLs and rate limits.
type AccountRecoveryPolicy struct {
	EmailVerificationTTL time.Duration
	PasswordResetTTL     time.Duration
	EmailChangeUndoTTL   time.Duration
	RateLimit            LoginRateLimitPolicy
}
