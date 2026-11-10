// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// constructAccounts builds the identity area and the account flows that stay
// in this package: Invitations, browser Invitation handoff and public
// registration. Invitations come first because external sign-in admits an
// External Identity through them.
func constructAccounts(
	deps Dependencies,
	foundation applicationFoundation,
	access accessAcademicConstruction,
) (accountConstruction, error) {
	accountMail := foundation.mail
	invitations, err := newInvitationService(invitationServiceDependencies{Store: deps.Store.Invitation(), Classes: deps.Store.Class(), Periods: deps.Store.AcademicPeriod(), AcademicUnits: deps.Store.AcademicUnit(), Roles: deps.Store.Role(), Authorization: invitationAuthorizationAdapter{authorization: access.authorization}, Mail: accountMail, Hasher: foundation.hasher, Audit: foundation.audit.MutationAudit(), Attempts: invitationAttemptAccounting{attempts: foundation.attempts, policy: deps.AccountRecovery.RateLimit}, MembershipEffects: academicRealtimeEffects{sittings: deps.Store.ExamSitting(), realtime: foundation.realtime}, NodeID: deps.NodeID, PublicURL: deps.PublicURL, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, NewClaim: model.NewCredentialToken, Now: time.Now})
	if err != nil {
		return accountConstruction{}, err
	}
	wake := &jobWake{}
	service, err := identity.New(identity.Dependencies{
		Store: deps.Store, Cache: deps.Cache, Registry: deps.Registry,
		Authorizer: access.authorization, Auditor: foundation.audit, Capabilities: access.capabilities,
		Effects: foundation.realtime, Mail: accountMail,
		Hasher: foundation.hasher, Attempts: foundation.attempts, Invalidator: foundation.invalidator,
		Invitations:     externalInvitationAcceptor{invitations: invitations},
		Operational:     operationalRecorderAdapter{recorder: deps.OperationalRecorder},
		MFASecretSealer: deps.MFASecretSealer, MFARekeyWake: wake.Wake,
		DesktopBuildCatalog: deps.DesktopBuildCatalog, SkipDesktopCompatibility: deps.SkipDesktopCompatibility,
		NodeID: deps.NodeID, PublicURL: deps.PublicURL, LoopbackHTTPDevelopment: deps.LoopbackHTTPDevelopment,
		MFA: deps.MFA, Sessions: deps.Sessions, LoginRateLimit: deps.LoginRateLimit,
		BootstrapProtection: deps.BootstrapProtection, PersonalAccessToken: deps.PersonalAccessToken,
		AccountRecovery: deps.AccountRecovery, ExternalAuth: deps.ExternalAuth,
		RecentAuthenticationTTL:   deps.RecentAuthenticationTTL,
		AuthenticationDiagnostics: deps.AuthenticationDiagnostics, RecoveryDiagnostics: deps.RecoveryDiagnostics,
	})
	if err != nil {
		return accountConstruction{}, err
	}
	browserInvitations, err := newBrowserInvitationService(browserInvitationServiceDependencies{Transactions: deps.Store.BrowserAuthentication(), Institutions: deps.Store.Institution(), Invitations: invitations, Issuer: deps.PublicURL, NewProof: model.NewCredentialToken})
	if err != nil {
		return accountConstruction{}, err
	}
	publicRegistration, err := newPublicRegistrationService(publicRegistrationDependencies{
		registrations: deps.Store.User(),
		policies:      currentPublicRegistrationPolicy{policies: deps.Store.AccessPolicy()},
		institutions:  publicRegistrationInstitutionAdapter{institutions: deps.Store.Institution()},
		mail:          accountMail,
		attempts:      foundation.attempts,
		hasher:        foundation.hasher,
		rateLimit:     deps.AccountRecovery.RateLimit,
		tokenTTL:      deps.AccountRecovery.EmailVerificationTTL,
		publicURL:     deps.PublicURL,
		nodeID:        deps.NodeID,
		newToken:      model.NewCredentialToken,
		now:           time.Now,
	})
	if err != nil {
		return accountConstruction{}, err
	}
	return accountConstruction{
		identity: service, browserInvitations: browserInvitations, publicRegistration: publicRegistration,
		invitations: invitations, wake: wake,
	}, nil
}

// externalInvitationAcceptor lets external sign-in in the identity area admit
// an External Identity through the Invitation its login state carries.
type externalInvitationAcceptor struct{ invitations *invitationService }

func (a externalInvitationAcceptor) AcceptExternalIdentity(ctx context.Context, state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion, capabilities store.AccessDeploymentCapabilities,
	metadata model.RequestMetadata, sessionClientType string,
) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	return a.invitations.acceptExternalIdentity(ctx, state, assertion, capabilities, metadata, sessionClientType)
}

// jobWake wakes the Job runtime once it exists. The identity area needs the
// wake when it is built, before the runtime that its MFA rekey proposes Jobs
// to.
type jobWake struct{ wake func() }

func (w *jobWake) Wake() {
	if w.wake != nil {
		w.wake()
	}
}
