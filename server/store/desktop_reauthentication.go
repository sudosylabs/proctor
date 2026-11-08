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

// DesktopReauthenticationCreation starts a browser handoff that refreshes
// primary proof on one existing Desktop Session. The Store rechecks that the
// exact Session, its current access credential, Desktop Registration and DPoP
// key are live and belong to the User, and pins the Session's own
// authentication method and provider as the only acceptable proof. It owns the
// initial state and every timestamp.
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

// DesktopReauthenticationContext is the safe current state selected only by
// the opaque browser-binding cookie. The expected method and provider are the
// Desktop Session's own; no other proof is offered or accepted.
type DesktopReauthenticationContext struct {
	ID                           model.BrowserAuthenticationTransactionID
	State                        model.BrowserAuthenticationState
	UserID                       model.UserID
	ExpectedAuthenticationMethod string
	ExpectedProviderID           string
	CreatedAt                    time.Time
	ExpiresAt                    time.Time
}

// DesktopReauthenticationAuthentication records one fresh primary proof on a
// bound Desktop reauthentication, selected by its browser binding or, after a
// provider redirect, by its identifier. A password proof names the exact
// current password revision; a provider proof names the exact External
// Identity and its asserted instant. The Store rechecks that the proof uses
// the pinned method and provider, that the proved User is the Session's User
// and, for a provider, that the identity is the Session's own; a different
// account denies the transaction. A live browser Web Session is never proof.
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

// DesktopReauthenticationAuthenticationResult reports whether the proof was
// refused because it proved another account, and names the Desktop Session's
// User the refusal protected.
type DesktopReauthenticationAuthenticationResult struct {
	Denied bool
	UserID model.UserID
}

// DesktopReauthenticationCodeIssue turns an authenticated reauthentication
// into one short-lived single-use code. CodeLifetime is applied to the Store's
// authoritative database timestamp.
type DesktopReauthenticationCodeIssue struct {
	BindingHash  string
	StateHash    string
	CodeHash     string
	CodeLifetime time.Duration
}

// DesktopReauthenticationCompletion consumes one code for the exact Desktop
// Session, Registration and DPoP key that started the handoff and, in the same
// transaction, refreshes that Session's primary proof and completes the
// required audit. No Session is created and no Registration changes.
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
