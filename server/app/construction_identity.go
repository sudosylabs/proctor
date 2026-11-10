// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// constructAccounts builds the identity area. External sign-in admits an
// External Identity through an Invitation, so identity receives an acceptor
// that constructOnboarding later connects to the onboarding area.
func constructAccounts(
	deps Dependencies,
	foundation applicationFoundation,
	access accessAcademicConstruction,
) (accountConstruction, error) {
	wake := &jobWake{}
	acceptor := &externalInvitationAcceptor{}
	service, err := identity.New(identity.Dependencies{
		Store: deps.Store, Cache: deps.Cache, Registry: deps.Registry,
		Authorizer: access.authorization, Auditor: foundation.audit, Capabilities: access.capabilities,
		Effects: foundation.realtime, Mail: foundation.mail,
		Hasher: foundation.hasher, Attempts: foundation.attempts, Invalidator: foundation.invalidator,
		Invitations:     acceptor,
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
	return accountConstruction{identity: service, invitationAcceptor: acceptor, wake: wake}, nil
}

// constructOnboarding builds the onboarding area over the identity, academic
// and administration areas, and connects external sign-in to its Invitation
// acceptance.
func constructOnboarding(
	deps Dependencies,
	foundation applicationFoundation,
	access accessAcademicConstruction,
	accounts accountConstruction,
) (*onboarding.Service, onboarding.Ports, error) {
	service, ports, err := onboarding.New(onboarding.Dependencies{
		Store: deps.Store, Content: deps.FileContent, Authorizer: access.authorization, Auditor: foundation.audit.MutationAudit(),
		Mail: foundation.mail, Hasher: foundation.hasher, Attempts: foundation.attempts,
		Identity: accounts.identity, Academic: access.academic, Administration: access.administration,
		MembershipEffects: academicRealtimeEffects{sittings: deps.Store.ExamSitting(), realtime: foundation.realtime},
		Wake:              accounts.wake.Wake,
		NodeID:            deps.NodeID, PublicURL: deps.PublicURL, AccountRecovery: deps.AccountRecovery,
		RecentAuthenticationTTL: deps.RecentAuthenticationTTL,
	})
	if err != nil {
		return nil, onboarding.Ports{}, err
	}
	accounts.invitationAcceptor.accept = ports.AcceptExternalIdentity
	return service, ports, nil
}

// externalInvitationAcceptor lets external sign-in in the identity area admit
// an External Identity through the Invitation its login state carries. The
// identity area is built first, so constructOnboarding connects it.
type externalInvitationAcceptor struct {
	accept func(context.Context, *model.ExternalLoginState, *model.ExternalAuthenticationAssertion,
		store.AccessDeploymentCapabilities, model.RequestMetadata, string) (*store.ExternalIdentityInvitationAcceptanceResult, error)
}

func (a *externalInvitationAcceptor) AcceptExternalIdentity(ctx context.Context, state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion, capabilities store.AccessDeploymentCapabilities,
	metadata model.RequestMetadata, sessionClientType string,
) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	return a.accept(ctx, state, assertion, capabilities, metadata, sessionClientType)
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
