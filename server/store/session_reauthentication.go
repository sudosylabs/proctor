// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// SessionPasswordReauthentication refreshes the primary proof of an active
// password session. It rechecks local login policy, the active user, the
// credential and password revision under the same per-user fence as password
// replacement and session revocation, then takes the proof instant from the
// database clock. The session's identity, expiry, initial authentication and
// MFA history do not change.
type SessionPasswordReauthentication struct {
	SessionID     model.SessionID
	CredentialID  model.SessionCredentialID
	UserID        model.UserID
	PasswordProof PasswordCredentialProof
	AuditEventID  model.AuditEventID
}

// SessionReauthenticationResult is the committed session and the access token
// hashes to invalidate once the commit succeeded.
type SessionReauthenticationResult struct {
	Session           *model.Session
	AccessTokenHashes []string
}

// SessionExternalReauthentication consumes a fresh provider proof for the
// session, credential and external identity bound to StateID. Subject must
// never appear in audit output.
type SessionExternalReauthentication struct {
	StateID         model.ExternalLoginStateID
	ProviderID      string
	Subject         string
	AuthenticatedAt time.Time
}
