// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authentication.go,
// server/channels/app/login.go, and server/channels/app/session.go. Proctor
// keeps the single application authentication flow, generic login failures,
// server-side revocable sessions, activity debouncing, and boundary-safe
// errors while using split hashed access/refresh credentials and rotation.

package identity

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Logout revokes the caller's current Session.
func (s *Service) Logout(ctx context.Context, inv invocation.Invocation) (resultErr error) {
	defer func() { recordOperational(s.operational, "authentication", "logout", resultErr) }()
	principal := inv.Principal()
	if principal.ValidateMFARecovery() != nil {
		return access.InvalidTokenAppError()
	}

	return s.revokeSession(
		ctx,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation:  "logout",
			Value:      map[string]any{"session_id": principal.SessionID.String()},
			At:         s.now(),
		},
		&store.SessionRevocation{
			SessionID: principal.SessionID,
			UserID:    principal.UserID,
			Reason:    model.SessionRevocationUserLogout,
		},
		authenticationUnavailable,
	)
}
