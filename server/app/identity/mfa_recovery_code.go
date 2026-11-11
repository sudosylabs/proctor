// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/platform/shared/mfa/mfa.go and
// server/channels/app/user.go. Proctor retains the proven TOTP setup,
// activation, validation, and replay-prevention flow while separating the
// encrypted credential aggregate, hashed recovery codes, session assurance,
// and durable security audit.

package identity

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RegenerateMFARecoveryCodes replaces the caller's recovery codes and returns
// the new set once.
func (s *Service) RegenerateMFARecoveryCodes(
	ctx context.Context,
	inv invocation.Invocation,
) ([]string, error) {
	principal := inv.Principal()
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
		ctx, inv, model.ActionMFARecoveryCodesRegenerate, resource, nil,
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
