// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package onboarding owns bringing people into the installation: issuing,
// replacing, revoking and accepting Invitations, browser Invitation handoff,
// public self-registration, academic administration batches, onboarding
// imports and Student Progression. Service holds every one of these use
// cases, one file per concern.
//
// New also returns Ports, the trusted entry points only composition receives:
// external sign-in's Invitation acceptance, the administration summary
// counts and the onboarding import Jobs. They are not on Service, so no
// transport can reach them.
//
// Start with onboarding.go for the Service and its collaborators, then open
// the file named for the concern you are changing.
package onboarding

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// resourceAuthorizer is the one authorization port. A use case authorizes an
// Action on a concrete Resource immediately before the work it guards.
type resourceAuthorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// MembershipEffects publishes the realtime effects of the Class Membership
// and Role an accepted Invitation grants. Report records a publication that
// failed; the acceptance still succeeds.
type MembershipEffects interface {
	MembershipChanged(context.Context, model.UserID, []model.ClassID) error
	Report(context.Context, string, error)
	identity.AuthorizationInvalidationEffects
}

// OnboardingImportFiles stages, opens, removes and pages the private files of
// onboarding imports.
type OnboardingImportFiles interface {
	StageOnboardingImport(context.Context, model.OnboardingImportID, io.Reader, int64) (string, int64, error)
	IsOnboardingImportTooLarge(error) bool
	OpenOnboardingImport(context.Context, model.OnboardingImportID) (io.ReadCloser, error)
	RemoveOnboardingImport(context.Context, model.OnboardingImportID) error
	ListOnboardingImportFiles(context.Context, string, int, time.Time) ([]model.OnboardingImportID, string, error)
}

// Dependencies are the collaborators New distributes to the Service. Hasher
// and Attempts are the shared password hasher and attempt accounting the
// parent builds once for every flow. Wake wakes the Job runtime once it
// exists.
type Dependencies struct {
	Store             store.Catalog
	Content           OnboardingImportFiles
	Authorizer        *access.Authorizer
	Auditor           access.MutationAuditor
	Mail              *mail.Composer
	Hasher            *identity.PasswordHasher
	Attempts          *identity.AttemptAccounting
	Identity          *identity.Service
	Academic          *academic.Service
	Administration    *administration.Service
	MembershipEffects MembershipEffects
	Wake              func()

	NodeID                  string
	PublicURL               string
	AccountRecovery         identity.AccountRecoveryPolicy
	RecentAuthenticationTTL time.Duration
}

// Service holds every onboarding use case. Each concern keeps its own focused
// implementation, embedded here so the Service's method set is their exported
// methods. Onboarding imports and Student Progression are absent from a
// reduced Store catalog that lacks the academic administration stores.
type Service struct {
	*invitationService
	*browserInvitationService
	*publicRegistrationService
	*academicAdministrationBatchService
	*onboardingImportService
}

// Ports are the trusted entry points New hands only to composition. None
// authorizes a transport caller, so none is on Service.
type Ports struct {
	// AcceptExternalIdentity admits an External Identity through the
	// Invitation its login state carries. External sign-in calls it once it
	// has verified the provider's assertion.
	AcceptExternalIdentity func(context.Context, *model.ExternalLoginState, *model.ExternalAuthenticationAssertion,
		store.AccessDeploymentCapabilities, model.RequestMetadata, string) (*store.ExternalIdentityInvitationAcceptanceResult, error)
	// CountPendingInvitations and CountOpenImports are the administration
	// summary counts; each authorizes like its list. CountOpenImports is nil
	// when the Service has no onboarding imports.
	CountPendingInvitations func(context.Context, invocation.Invocation, int) (int, error)
	CountOpenImports        func(context.Context, invocation.Invocation, int) (int, error)
	// ImportJobs is the port the onboarding import Jobs call, or nil when the
	// Service has no onboarding imports.
	ImportJobs jobhandler.OnboardingImportService
}

// New builds the Service, every focused implementation it embeds, and the
// Ports composition wires into external sign-in, the administration summary
// and the Job catalog.
func New(deps Dependencies) (*Service, Ports, error) {
	if deps.Store == nil {
		return nil, Ports{}, errors.New("onboarding store catalog is required")
	}
	catalog := deps.Store
	authorization := invitationAuthorizationAdapter{authorization: deps.Authorizer}
	invitations, err := newInvitationService(invitationServiceDependencies{Store: catalog.Invitation(), Classes: catalog.Class(), Periods: catalog.AcademicPeriod(), AcademicUnits: catalog.AcademicUnit(), Roles: catalog.Role(), Authorization: authorization, Mail: deps.Mail, Hasher: deps.Hasher, Audit: deps.Auditor, Attempts: invitationAttemptAccounting{attempts: deps.Attempts, policy: deps.AccountRecovery.RateLimit}, MembershipEffects: deps.MembershipEffects, NodeID: deps.NodeID, PublicURL: deps.PublicURL, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, NewClaim: model.NewCredentialToken, Now: time.Now})
	if err != nil {
		return nil, Ports{}, err
	}
	browserInvitations, err := newBrowserInvitationService(browserInvitationServiceDependencies{Transactions: catalog.BrowserAuthentication(), Institutions: catalog.Institution(), Invitations: invitations, Issuer: deps.PublicURL, NewProof: model.NewCredentialToken})
	if err != nil {
		return nil, Ports{}, err
	}
	publicRegistration, err := newPublicRegistrationService(publicRegistrationDependencies{
		registrations: catalog.User(),
		policies:      currentPublicRegistrationPolicy{policies: catalog.AccessPolicy()},
		institutions:  publicRegistrationInstitutionAdapter{institutions: catalog.Institution()},
		mail:          deps.Mail,
		attempts:      deps.Attempts,
		hasher:        deps.Hasher,
		rateLimit:     deps.AccountRecovery.RateLimit,
		tokenTTL:      deps.AccountRecovery.EmailVerificationTTL,
		publicURL:     deps.PublicURL,
		nodeID:        deps.NodeID,
		newToken:      model.NewCredentialToken,
		now:           time.Now,
	})
	if err != nil {
		return nil, Ports{}, err
	}
	batches := newAcademicAdministrationBatchService(academicAdministrationBatchServiceDependencies{Commands: academicAdministrationCommandServices{academic: deps.Academic,
		roleBindings: deps.Administration, identity: deps.Identity}, Authorization: authorization, Outcomes: catalog.CommandOutcome(), Now: time.Now, RecentTTL: deps.RecentAuthenticationTTL})
	imports, err := constructOnboardingImports(deps, invitations, batches, authorization)
	if err != nil {
		return nil, Ports{}, err
	}
	service := &Service{
		invitationService: invitations, browserInvitationService: browserInvitations,
		publicRegistrationService: publicRegistration, academicAdministrationBatchService: batches,
		onboardingImportService: imports,
	}
	ports := Ports{AcceptExternalIdentity: invitations.acceptExternalIdentity, CountPendingInvitations: invitations.countPendingInvitations}
	if imports != nil {
		imports.wake = deps.Wake
		ports.CountOpenImports, ports.ImportJobs = imports.countOpenOnboardingImports, onboardingImportJobs{service: imports}
	}
	return service, ports, nil
}

// constructOnboardingImports projects the onboarding import stores. A deliberately
// reduced lifecycle test catalog omits whole capability families and so
// constructs no onboarding imports; production catalogs project every target
// store together.
func constructOnboardingImports(deps Dependencies, invitations *invitationService, batches *academicAdministrationBatchService,
	authorization invitationAuthorizationAdapter,
) (*onboardingImportService, error) {
	catalog := deps.Store
	if catalog.OnboardingImport() == nil {
		return nil, errors.New("onboarding import persistence is required")
	}
	if deps.Content == nil {
		return nil, errors.New("onboarding import file content is required")
	}
	if catalog.PersonalAccessToken() == nil || catalog.User() == nil {
		return nil, errors.New("onboarding import credential stores are required")
	}
	if catalog.Institution() == nil || catalog.AcademicUnit() == nil || catalog.Programme() == nil || catalog.ProgrammeLevel() == nil ||
		catalog.AcademicPeriod() == nil || catalog.Class() == nil || catalog.Role() == nil {
		return nil, nil
	}
	return newOnboardingImportService(onboardingImportServiceDependencies{Imports: catalog.OnboardingImport(), Content: deps.Content, Invitations: invitations, AdministrationBatches: batches, Authentication: deps.Identity, Authorization: authorization, Audit: deps.Auditor, Tokens: catalog.PersonalAccessToken(), Users: catalog.User(), Institutions: catalog.Institution(), Units: catalog.AcademicUnit(), Programmes: catalog.Programme(), Levels: catalog.ProgrammeLevel(), Periods: catalog.AcademicPeriod(), Classes: catalog.Class(), Roles: catalog.Role(), Affiliations: catalog.Affiliation(), UnitMembers: catalog.AcademicUnitMember(), ClassMembers: catalog.ClassMember(), RoleBindings: catalog.RoleBinding(), Now: time.Now})
}
