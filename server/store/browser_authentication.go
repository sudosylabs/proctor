// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// DesktopAuthorizationCreation is the input of a pending desktop browser
// handoff. The store sets the initial state and every timestamp.
type DesktopAuthorizationCreation struct {
	ID                      model.BrowserAuthenticationTransactionID
	InstitutionID           model.InstitutionID
	Issuer                  string
	HandleHash              string
	BrowserProofHash        string
	StateHash               string
	CallbackURL             string
	CodeChallenge           string
	DeviceID                string
	DeviceName              string
	ProposedPublicJWK       model.DesktopPublicJWK
	ProposedKeyThumbprint   string
	DesktopRelease          string
	DesktopBuildID          string
	DesktopPlatform         model.DesktopPlatform
	DesktopArchitecture     model.DesktopArchitecture
	DesktopRealtimeProtocol int
	Lifetime                time.Duration
}

type DesktopAuthorizationCreated struct {
	ID        model.BrowserAuthenticationTransactionID
	ExpiresAt time.Time
}

// DesktopAuthorizationBinding consumes the URL handle and fragment proof and
// replaces them, atomically, with one server-owned browser binding.
type DesktopAuthorizationBinding struct {
	HandleHash       string
	BrowserProofHash string
	StateHash        string
	BindingHash      string
}

type DesktopAuthorizationBound struct {
	ExpiresAt time.Time
}

// DesktopAuthorizationContext is the state the browser-binding cookie may
// see; proof details are stored but never projected.
type DesktopAuthorizationContext struct {
	ID         model.BrowserAuthenticationTransactionID
	State      model.BrowserAuthenticationState
	UserID     model.UserID
	DeviceName string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// DesktopAuthorizationAuthentication binds one proved identity to the browser
// transaction. The store rechecks the active user, authentication policy,
// external-identity provenance, password proof and the active-attempt session
// lock together, and rolls back on an inconsistent result.
//
// Exactly one proof is given: a source web session with its live access
// credential (the store derives the authentication from that session), a
// PasswordProof, or an external identity. Fresh password and MFA proofs take
// the store's transition instant. External proofs keep their asserted times,
// capped at that instant when the provider clock is ahead. Session reuse takes
// the session's latest primary proof and its MFA history, records an earlier
// MFA completion at that proof instant, and rechecks session and access-proof
// expiry. Nothing here extends the transaction's original deadline.
type DesktopAuthorizationAuthentication struct {
	PasswordProof            PasswordCredentialProof
	SourceSessionID          model.SessionID
	SourceCredentialID       model.SessionCredentialID
	BindingHash              string
	TransactionID            model.BrowserAuthenticationTransactionID
	UserID                   model.UserID
	AuthenticationMethod     string
	AuthenticationProviderID string
	ExternalIdentityID       model.ExternalIdentityID
	AuthenticationStrength   model.AuthenticationStrength
	AuthenticatedAt          time.Time
	MFACompletedAt           model.OptionalTime
	Capabilities             AccessDeploymentCapabilities
	AuditEventID             model.AuditEventID
}

type DesktopAuthorizationAuthenticationResult struct {
	Denied  bool
	Context *DesktopAuthorizationContext
}

type DesktopAuthorizationAccountReset struct{ BindingHash string }

// BrowserInvitationTransactionProof lets an invitation acceptance consume the
// proved browser transaction in the same commit. The accepting store supplies
// the invitation and user.
type BrowserInvitationTransactionProof struct {
	ID               model.BrowserAuthenticationTransactionID
	HandleHash       string
	BrowserProofHash string
}

// BrowserInvitationTransactionCreation rechecks one invitation and creates its
// browser handoff atomically. The store sets the creation time and deadline.
type BrowserInvitationTransactionCreation struct {
	ID                  model.BrowserAuthenticationTransactionID
	InstitutionID       model.InstitutionID
	Issuer              string
	InvitationID        model.InvitationID
	InvitationPurpose   model.InvitationPurpose
	InvitationClaimHash string
	HandleHash          string
	BrowserProofHash    string
}

type BrowserInvitationCreated struct {
	ID        model.BrowserAuthenticationTransactionID
	ExpiresAt time.Time
}

type BrowserInvitationResolution struct {
	ID                  model.BrowserAuthenticationTransactionID
	InvitationID        model.InvitationID
	InvitationClaimHash string
}

// BrowserAuthenticationMaintenanceResult is one bounded maintenance page, run
// on the database clock. More reports that eligible work remains now.
type BrowserAuthenticationMaintenanceResult struct {
	Expired int
	Purged  int
	More    bool
}

// BrowserAuthenticationStore persists the browser transactions behind desktop
// authorization, desktop reauthentication and invitation acceptance. Only
// hashes of browser credentials cross it, never raw values.
type BrowserAuthenticationStore interface {
	CreateDesktopAuthorization(context.Context, *DesktopAuthorizationCreation) (*DesktopAuthorizationCreated, error)
	BindDesktopAuthorization(context.Context, *DesktopAuthorizationBinding) (*DesktopAuthorizationBound, error)
	GetDesktopAuthorizationContext(context.Context, string) (*DesktopAuthorizationContext, error)
	AuthenticateDesktopAuthorization(context.Context, *DesktopAuthorizationAuthentication) (*DesktopAuthorizationAuthenticationResult, error)
	ResetDesktopAuthorizationAccount(context.Context, *DesktopAuthorizationAccountReset) error
	CreateInvitation(context.Context, *BrowserInvitationTransactionCreation) (*BrowserInvitationCreated, error)
	ResolveInvitation(context.Context, string, string) (*BrowserInvitationResolution, error)
	IssueCode(context.Context, *DesktopAuthorizationCodeIssue) (*DesktopAuthorizationCodeIssued, error)
	Cancel(context.Context, *DesktopAuthorizationCancellation) error
	ResolveDesktopAuthorizationExchange(context.Context, *DesktopAuthorizationExchangeProof) (*DesktopAuthorizationExchangeTarget, error)
	Exchange(context.Context, *DesktopAuthorizationExchange) (*DesktopAuthorizationExchangeResult, error)
	Maintain(context.Context, int) (*BrowserAuthenticationMaintenanceResult, error)
	CreateDesktopReauthentication(context.Context, *DesktopReauthenticationCreation) (*DesktopAuthorizationCreated, error)
	BindDesktopReauthentication(context.Context, *DesktopAuthorizationBinding) (*DesktopAuthorizationBound, error)
	GetDesktopReauthenticationContext(context.Context, string) (*DesktopReauthenticationContext, error)
	AuthenticateDesktopReauthentication(
		context.Context,
		*DesktopReauthenticationAuthentication,
	) (*DesktopReauthenticationAuthenticationResult, error)
	IssueDesktopReauthenticationCode(context.Context, *DesktopReauthenticationCodeIssue) (*DesktopAuthorizationCodeIssued, error)
	CancelDesktopReauthentication(context.Context, *DesktopAuthorizationCancellation) error
	CompleteDesktopReauthentication(context.Context, *DesktopReauthenticationCompletion) (*SessionReauthenticationResult, error)
}
