// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type authenticationMFAVerifier interface {
	recoveryState(context.Context, model.UserID) (*model.UserMFARecovery, error)
	verifyLogin(
		context.Context,
		string,
		string,
		time.Time,
	) (model.AuthenticationStrength, int64, error)
}

type sessionIssuance struct {
	AuthenticationGeneration int64
	MFARecoveryRequired      bool
	ExternalLoginStateID     model.ExternalLoginStateID
	PasswordProof            store.PasswordCredentialProof
	User                     *model.User
	ClientType               model.SessionClientType
	DeviceID                 string
	DeviceName               string
	AuthenticationMethod     string
	AuthenticationProviderID string
	ExternalIdentityID       model.ExternalIdentityID
	AuthenticationStrength   model.AuthenticationStrength
	AuthenticatedAt          int64
	MFACompletedAt           int64
	// AuditEventID is the login attempt that Session creation completes
	// atomically with the Session.
	AuditEventID model.AuditEventID
}

type authenticationSessionIssuer interface {
	mfaRecoveryState(context.Context, model.UserID) (*model.UserMFARecovery, error)
	createSession(
		context.Context,
		sessionIssuance,
	) (*model.Session, *model.AuthenticationTokens, error)
}

// resolvePersonalAccessTokenBearer resolves a raw personal access token to
// its CLI principal, debouncing the token's last-used write.
func (s *Service) resolvePersonalAccessTokenBearer(
	ctx context.Context,
	rawToken string,
	at time.Time,
) (*model.Principal, error) {
	if !validRawCredential(rawToken) {
		return nil, access.InvalidTokenAppError()
	}
	resolved, err := s.personalAccessTokens.Resolve(
		ctx,
		model.HashToken(rawToken),
		model.TimeUTC(at),
		s.personalAccessTokenPolicy.LastUsedUpdateInterval,
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}
	principal := &model.Principal{
		UserID:               resolved.User.ID,
		CredentialID:         model.PrincipalCredentialID(resolved.Token.ID),
		CredentialType:       model.CredentialPersonalAccessToken,
		AuthenticationMethod: "personal_access_token",
		ClientType:           model.SessionClientCLI,
		CredentialScopes:     append([]string(nil), resolved.Token.Scopes...),
		AcademicUnitID:       resolved.Token.AcademicUnitID,
	}
	if principal.Validate() != nil {
		s.diagnostics.WarnContext(
			ctx,
			"personal access token resolved to invalid principal",
			logging.Err(fmt.Errorf("personal_access_token_id=%s", resolved.Token.ID.String())),
		)
		return nil, access.InvalidTokenAppError()
	}
	return principal, nil
}
