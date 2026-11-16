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

// DesktopReauthenticationCreation starts a browser handoff that refreshes the
// primary proof of an existing Desktop session. The store rechecks that the
// session, its access credential, registration and DPoP key are live and the
// user's, pins the session's own method and provider as the only acceptable
// proof, and sets every timestamp.
type DesktopReauthenticationCreation struct {
	ID                    model.BrowserAuthenticationTransactionID
	InstitutionID         model.InstitutionID
	Issuer                string
	HandleHash            string
	BrowserProofHash      string
	StateHash             string
	CallbackURL           string
	CodeChallenge         string
	UserID                model.UserID
	SessionID             model.SessionID
	CredentialID          model.SessionCredentialID
	DesktopRegistrationID model.DesktopRegistrationID
	KeyThumbprint         string
	Lifetime              time.Duration
}

// DesktopReauthenticationContext is the state the browser-binding cookie may
// see. Only the session's own method and provider are offered or accepted.
type DesktopReauthenticationContext struct {
	ID                           model.BrowserAuthenticationTransactionID
	State                        model.BrowserAuthenticationState
	UserID                       model.UserID
	ExpectedAuthenticationMethod string
	ExpectedProviderID           string
	CreatedAt                    time.Time
	ExpiresAt                    time.Time
}

// DesktopReauthenticationAuthentication records one fresh primary proof,
// selected by browser binding or, after a provider redirect, by
// TransactionID. A password proof names the current password revision; a
// provider proof names the external identity and its asserted instant. The
// store rechecks the pinned method and provider and that the proof is for the
// session's user and identity; another account denies the transaction. A live
// browser web session is never accepted as proof.
type DesktopReauthenticationAuthentication struct {
	BindingHash          string
	TransactionID        model.BrowserAuthenticationTransactionID
	UserID               model.UserID
	AuthenticationMethod string
	ProviderID           string
	ExternalIdentityID   model.ExternalIdentityID
	PasswordProof        PasswordCredentialProof
	AuthenticatedAt      time.Time
}

// DesktopReauthenticationAuthenticationResult has Denied set when the proof
// was for another account; UserID is the session's user it protected.
type DesktopReauthenticationAuthenticationResult struct {
	Denied bool
	UserID model.UserID
}

// DesktopReauthenticationCodeIssue turns an authenticated reauthentication
// into a single-use code; CodeLifetime starts from the database clock.
type DesktopReauthenticationCodeIssue struct {
	BindingHash  string
	StateHash    string
	CodeHash     string
	CodeLifetime time.Duration
}

// DesktopReauthenticationCompletion consumes one code for the session,
// registration and DPoP key that started the handoff, and in the same
// transaction refreshes the session's primary proof and completes the audit.
// It creates no session and changes no registration.
type DesktopReauthenticationCompletion struct {
	CodeHash              string
	StateHash             string
	CodeChallenge         string
	UserID                model.UserID
	SessionID             model.SessionID
	CredentialID          model.SessionCredentialID
	DesktopRegistrationID model.DesktopRegistrationID
	KeyThumbprint         string
	AuditEventID          model.AuditEventID
}
