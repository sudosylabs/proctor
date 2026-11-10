// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

// AuthRequirement is the credential and assurance a route demands. Every
// route declares exactly one.
type AuthRequirement string

// The requirements a route may declare. authSpecs says what each one demands.
const (
	AuthPublic                           AuthRequirement = "public"
	AuthMFARecoverySessionRequired       AuthRequirement = "mfa_recovery_session_required"
	AuthRecentMFARecoverySessionRequired AuthRequirement = "recent_mfa_recovery_session_required"
	AuthPrincipalRequired                AuthRequirement = "principal_required"
	AuthSessionRequired                  AuthRequirement = "session_required"
	AuthStrongSessionRequired            AuthRequirement = "strong_session_required"
	AuthRecentSessionRequired            AuthRequirement = "recent_session_required"
	AuthStrongRecentSessionRequired      AuthRequirement = "strong_recent_session_required"
	//nolint:gosec // G101: a requirement name, not a credential.
	AuthRefreshCredentialRequired AuthRequirement = "refresh_credential_required"
)

// AuthMFAEnrollmentSessionRequired admits an ordinary Session or one the
// Access Policy restricts to MFA enrollment, never an assisted-recovery
// Session. Only the second-factor challenge declares it.
const AuthMFAEnrollmentSessionRequired AuthRequirement = "mfa_enrollment_session_required"

// authSpec says what an AuthRequirement demands of a request. Everything the
// kernel decides from a requirement reads this one table: which credential it
// extracts, which assurance it checks, which restricted Sessions it admits,
// and which error codes it can therefore return.
type authSpec struct {
	// principal requires an access credential that authenticates a
	// Principal.
	principal bool
	// refresh requires a refresh credential instead; the operation itself
	// redeems it.
	refresh bool
	// strong requires strong (multi-factor) authentication.
	strong bool
	// recent requires authentication within the recent-authentication TTL.
	recent bool
	// mfaRecovery admits a Session restricted to MFA recovery, and one
	// restricted to MFA enrollment.
	mfaRecovery bool
	// mfaEnrollment admits a Session restricted to MFA enrollment.
	mfaEnrollment bool
}

// authSpecs is the closed set of requirements a route may declare.
var authSpecs = map[AuthRequirement]authSpec{
	AuthPublic:                           {},
	AuthRefreshCredentialRequired:        {refresh: true},
	AuthPrincipalRequired:                {principal: true},
	AuthSessionRequired:                  {principal: true},
	AuthStrongSessionRequired:            {principal: true, strong: true},
	AuthRecentSessionRequired:            {principal: true, recent: true},
	AuthStrongRecentSessionRequired:      {principal: true, strong: true, recent: true},
	AuthMFAEnrollmentSessionRequired:     {principal: true, mfaEnrollment: true},
	AuthMFARecoverySessionRequired:       {principal: true, mfaRecovery: true},
	AuthRecentMFARecoverySessionRequired: {principal: true, mfaRecovery: true, recent: true},
}

// specFor returns the requirement's spec and whether a route may declare it.
func specFor(requirement AuthRequirement) (authSpec, bool) {
	spec, ok := authSpecs[requirement]
	return spec, ok
}

// authRequiresPrincipal reports whether a request that reaches the operation
// carries an authenticated Principal.
func authRequiresPrincipal(requirement AuthRequirement) bool {
	return authSpecs[requirement].principal
}
