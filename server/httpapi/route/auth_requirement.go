// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

// AuthRequirement is the credential and assurance a route demands. It has no
// default; a public route declares AuthPublic.
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

// AuthMFAEnrollmentSessionRequired admits an ordinary session or one the
// access policy restricts to MFA enrollment, never an assisted-recovery
// session. Only the second-factor challenge declares it.
const AuthMFAEnrollmentSessionRequired AuthRequirement = "mfa_enrollment_session_required"

type authSpec struct {
	principal bool
	// personalAccessToken also admits a personal access token as a bearer
	// credential.
	personalAccessToken bool
	// refresh requires a refresh credential instead; the operation itself
	// redeems it.
	refresh bool
	strong  bool
	recent  bool
	// admitsRecoverySession admits a session restricted to MFA recovery, and
	// with it the enrollment restriction.
	admitsRecoverySession   bool
	admitsEnrollmentSession bool
}

var authSpecs = map[AuthRequirement]authSpec{
	AuthPublic:                       {},
	AuthRefreshCredentialRequired:    {refresh: true},
	AuthPrincipalRequired:            {principal: true, personalAccessToken: true},
	AuthSessionRequired:              {principal: true},
	AuthStrongSessionRequired:        {principal: true, strong: true},
	AuthRecentSessionRequired:        {principal: true, recent: true},
	AuthStrongRecentSessionRequired:  {principal: true, strong: true, recent: true},
	AuthMFAEnrollmentSessionRequired: {principal: true, admitsEnrollmentSession: true},
	AuthMFARecoverySessionRequired: {
		principal:               true,
		admitsRecoverySession:   true,
		admitsEnrollmentSession: true,
	},
	AuthRecentMFARecoverySessionRequired: {
		principal:               true,
		admitsRecoverySession:   true,
		admitsEnrollmentSession: true,
		recent:                  true,
	},
}

// specFor returns the zero spec for an undeclared requirement; catalog
// validation rejects it before any route is served.
func specFor(requirement AuthRequirement) (authSpec, bool) {
	spec, ok := authSpecs[requirement]
	return spec, ok
}

func authRequiresPrincipal(requirement AuthRequirement) bool {
	spec, _ := specFor(requirement)
	return spec.principal
}
