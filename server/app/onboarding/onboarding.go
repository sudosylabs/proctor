// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package onboarding owns bringing people into the installation: issuing,
// replacing, revoking and accepting Invitations, browser Invitation handoff,
// public self-registration, academic administration batches, onboarding
// imports and Student Progression. One flat Service holds every one of these
// use cases, one file per concern; its fields are the Store contracts it
// calls and one of each collaborator: the authorizer, the mutation auditor,
// Mail, the shared password hasher and attempt accounting, the membership
// effects, and the academic, administration and identity commands a batch
// runs.
//
// Onboarding imports and Student Progression are optional: a reduced Store
// catalog that lacks the academic administration stores builds a Service
// without them, whose import use cases refuse with onboarding_import.unavailable.
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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// resourceAuthorizer is the part of *access.Authorizer the onboarding use
// cases call. A use case authorizes an Action on a concrete Resource
// immediately before the work it guards; a scoped list resolves the caller's
// granted scopes and records its one decision.
type resourceAuthorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
	CanDelegateActionsAtScope(context.Context, model.Principal, []string, model.RoleScopeType, string) (bool, error)
	GrantedScopes(context.Context, model.Principal, model.Action, model.ResourceType) (access.AuthorizedScopeConstraint, error)
	Institution(context.Context) (*model.Institution, error)
	RecordAuthorizationDecision(
		context.Context,
		model.Principal,
		model.Action,
		model.Resource,
		model.RoleScopeType,
		string,
		model.RequestMetadata,
		bool,
	) error
}

var _ resourceAuthorizer = (*access.Authorizer)(nil)

// mailComposer is the part of *mail.Composer onboarding prepares frozen mail
// with: the Invitation lifecycle messages and the public registration
// verification.
type mailComposer interface {
	Enabled() bool
	PrepareInvitation(*model.Invitation, string) (*store.PreparedMail, error)
	PrepareInvitationResend(*model.Invitation, string, model.UserID, time.Time) (*store.PreparedMail, error)
	PrepareInvitationRevocation(*model.Invitation, model.UserID, time.Time) (*store.PreparedMail, error)
	PrepareInvitationAccepted(mail.NoticePreparation) (*store.PreparedMail, error)
	PreparePublicRegistrationVerification(*model.User, model.MailOccurrenceID, string, time.Time, time.Time) (*store.PreparedMail, error)
}

var _ mailComposer = (*mail.Composer)(nil)

// passwordHasher is the password work of the shared *identity.PasswordHasher.
type passwordHasher interface {
	Hash(context.Context, string) (string, error)
}

var _ passwordHasher = (*identity.PasswordHasher)(nil)

// academicCommands, roleBindingCommands and identityCommands are the
// ordinary use cases an academic administration batch row runs, held as the
// academic, administration and identity Services themselves. identityCommands
// also revalidates an onboarding import's frozen Principal.
type academicCommands interface {
	CreateAffiliation(context.Context, invocation.Invocation, academic.CreateAffiliationCommand) (*model.Affiliation, error)
	EndAffiliation(context.Context, invocation.Invocation, academic.EndAffiliationCommand) (*model.Affiliation, error)
	CreateAcademicUnitMember(
		context.Context,
		invocation.Invocation,
		academic.CreateAcademicUnitMemberCommand,
	) (*model.AcademicUnitMember, error)
	EndAcademicUnitMember(context.Context, invocation.Invocation, academic.EndAcademicUnitMemberCommand) (*model.AcademicUnitMember, error)
	EnrollClassMember(context.Context, invocation.Invocation, academic.EnrollClassMemberCommand) (*model.ClassEnrollment, error)
	EndClassMember(context.Context, invocation.Invocation, academic.EndClassMemberCommand) (*model.ClassMember, error)
}

type roleBindingCommands interface {
	CreateRoleBinding(context.Context, invocation.Invocation, administration.CreateRoleBindingCommand) (*model.RoleBinding, error)
	EndRoleBinding(context.Context, invocation.Invocation, administration.EndRoleBindingCommand) (*model.RoleBinding, error)
}

type identityCommands interface {
	SetUserEnabled(context.Context, invocation.Invocation, identity.SetUserEnabledCommand) (*model.User, error)
	RevokeUserSessions(context.Context, invocation.Invocation, identity.RevokeUserSessionsCommand) error
	ValidatePrincipal(context.Context, model.Principal) error
}

var (
	_ academicCommands    = (*academic.Service)(nil)
	_ roleBindingCommands = (*administration.Service)(nil)
	_ identityCommands    = (*identity.Service)(nil)
)

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

// Service holds every onboarding use case. Its fields are the Store contracts
// it calls and one of each collaborator.
type Service struct {
	invitations           store.InvitationStore
	browserAuthentication store.BrowserAuthenticationStore
	institutions          store.InstitutionStore
	academicUnits         store.AcademicUnitStore
	programmes            store.ProgrammeStore
	levels                store.ProgrammeLevelStore
	periods               store.AcademicPeriodStore
	classes               store.ClassStore
	roles                 store.RoleStore
	users                 store.UserStore
	accessPolicies        store.AccessPolicyStore
	commandOutcomes       store.CommandOutcomeStore
	tokens                store.PersonalAccessTokenStore
	affiliations          store.AffiliationStore
	unitMembers           store.AcademicUnitMemberStore
	classMembers          store.ClassMemberStore
	roleBindings          store.RoleBindingStore
	// imports is nil when the Store catalog is too reduced for onboarding
	// imports; every import use case then refuses (see requireImports).
	imports store.OnboardingImportStore
	content OnboardingImportFiles

	// authorizer is nil when composition supplies no Authorizer; every
	// authorization then refuses with invitation.unavailable.
	authorizer        resourceAuthorizer
	auditor           access.MutationAuditor
	mail              mailComposer
	hasher            passwordHasher
	attempts          *identity.AttemptAccounting
	membershipEffects MembershipEffects
	academic          academicCommands
	administration    roleBindingCommands
	identity          identityCommands
	wake              func()

	nodeID    string
	publicURL string
	// rateLimit bounds Invitation claim acceptance and public registration
	// attempts; emailVerificationTTL is a registration token's lifetime.
	rateLimit               identity.LoginRateLimitPolicy
	emailVerificationTTL    time.Duration
	recentAuthenticationTTL time.Duration
	newToken                func() string
	now                     func() time.Time
}

// Ports are the trusted entry points New hands only to composition. None
// authorizes a transport caller, so none is on Service.
type Ports struct {
	// AcceptExternalIdentity admits an External Identity through the
	// Invitation its login state carries. External sign-in calls it once it
	// has verified the provider's assertion.
	AcceptExternalIdentity func(
		context.Context,
		*model.ExternalLoginState,
		*model.ExternalAuthenticationAssertion,
		store.AccessDeploymentCapabilities,
		model.RequestMetadata,
		string,
	) (*store.ExternalIdentityInvitationAcceptanceResult, error)
	// CountPendingInvitations and CountOpenImports are the administration
	// summary counts; each authorizes like its list. CountOpenImports is nil
	// when the Service has no onboarding imports.
	CountPendingInvitations func(context.Context, invocation.Invocation, int) (int, error)
	CountOpenImports        func(context.Context, invocation.Invocation, int) (int, error)
	// ImportJobs is the port the onboarding import Jobs call, or nil when the
	// Service has no onboarding imports.
	ImportJobs jobhandler.OnboardingImportService
}

// New builds the Service, keeping the Store contracts it calls, and the
// Ports composition wires into external sign-in, the administration summary
// and the Job catalog. It refuses missing collaborators.
func New(deps Dependencies) (*Service, Ports, error) {
	if deps.Store == nil {
		return nil, Ports{}, errors.New("onboarding store catalog is required")
	}

	catalog := deps.Store
	if catalog.Invitation() == nil ||
		catalog.Class() == nil ||
		catalog.AcademicPeriod() == nil ||
		catalog.AcademicUnit() == nil ||
		catalog.Role() == nil ||
		deps.Auditor == nil ||
		deps.MembershipEffects == nil ||
		deps.NodeID == "" ||
		deps.PublicURL == "" ||
		deps.RecentAuthenticationTTL <= 0 {
		return nil, Ports{}, errors.New("invitation service dependencies are invalid")
	}

	if catalog.BrowserAuthentication() == nil ||
		catalog.Institution() == nil ||
		model.ValidateBrowserAuthenticationIssuer(deps.PublicURL, true) != nil {
		return nil, Ports{}, errors.New("browser invitation dependencies are invalid")
	}

	rateLimit := deps.AccountRecovery.RateLimit
	if catalog.User() == nil ||
		deps.Attempts == nil ||
		rateLimit.Window <= 0 ||
		rateLimit.MaximumAttempts <= 0 ||
		rateLimit.MaximumSourceAttempts <= 0 ||
		deps.AccountRecovery.EmailVerificationTTL <= 0 {
		return nil, Ports{}, errors.New("public registration dependencies are invalid")
	}

	s := &Service{
		invitations:             catalog.Invitation(),
		browserAuthentication:   catalog.BrowserAuthentication(),
		institutions:            catalog.Institution(),
		academicUnits:           catalog.AcademicUnit(),
		programmes:              catalog.Programme(),
		levels:                  catalog.ProgrammeLevel(),
		periods:                 catalog.AcademicPeriod(),
		classes:                 catalog.Class(),
		roles:                   catalog.Role(),
		users:                   catalog.User(),
		accessPolicies:          catalog.AccessPolicy(),
		commandOutcomes:         catalog.CommandOutcome(),
		tokens:                  catalog.PersonalAccessToken(),
		affiliations:            catalog.Affiliation(),
		unitMembers:             catalog.AcademicUnitMember(),
		classMembers:            catalog.ClassMember(),
		roleBindings:            catalog.RoleBinding(),
		auditor:                 deps.Auditor,
		mail:                    deps.Mail,
		hasher:                  deps.Hasher,
		attempts:                deps.Attempts,
		membershipEffects:       deps.MembershipEffects,
		academic:                deps.Academic,
		administration:          deps.Administration,
		identity:                deps.Identity,
		nodeID:                  deps.NodeID,
		publicURL:               deps.PublicURL,
		rateLimit:               rateLimit,
		emailVerificationTTL:    deps.AccountRecovery.EmailVerificationTTL,
		recentAuthenticationTTL: deps.RecentAuthenticationTTL,
		newToken:                model.NewCredentialToken,
		now:                     time.Now,
	}
	if deps.Authorizer != nil {
		s.authorizer = deps.Authorizer
	}

	enabled, err := onboardingImportsEnabled(deps)
	if err != nil {
		return nil, Ports{}, err
	}

	ports := Ports{AcceptExternalIdentity: s.acceptExternalIdentity, CountPendingInvitations: s.countPendingInvitations}
	if enabled {
		s.imports, s.content, s.wake = catalog.OnboardingImport(), deps.Content, deps.Wake
		ports.CountOpenImports, ports.ImportJobs = s.countOpenOnboardingImports, onboardingImportJobs{service: s}
	}

	return s, ports, nil
}

// onboardingImportsEnabled reports whether the Store catalog projects every
// onboarding import store. A deliberately reduced lifecycle test catalog
// omits whole capability families and so has no onboarding imports;
// production catalogs project every target store together.
func onboardingImportsEnabled(deps Dependencies) (bool, error) {
	catalog := deps.Store
	if catalog.OnboardingImport() == nil {
		return false, errors.New("onboarding import persistence is required")
	}
	if deps.Content == nil {
		return false, errors.New("onboarding import file content is required")
	}
	if catalog.PersonalAccessToken() == nil || catalog.User() == nil {
		return false, errors.New("onboarding import credential stores are required")
	}

	if catalog.Institution() == nil ||
		catalog.AcademicUnit() == nil ||
		catalog.Programme() == nil ||
		catalog.ProgrammeLevel() == nil ||
		catalog.AcademicPeriod() == nil ||
		catalog.Class() == nil ||
		catalog.Role() == nil {
		return false, nil
	}

	if deps.Identity == nil {
		return false, errors.New("onboarding import application collaborators are required")
	}
	if catalog.Affiliation() == nil || catalog.AcademicUnitMember() == nil || catalog.ClassMember() == nil || catalog.RoleBinding() == nil {
		return false, errors.New("onboarding import academic administration stores are required")
	}

	return true, nil
}

// requireImports refuses an onboarding import use case on a Service built
// without onboarding imports.
func (s *Service) requireImports() error {
	if s.imports == nil {
		return apperror.New("onboarding_import.unavailable")
	}
	return nil
}

// authorize authorizes an Action on a concrete Resource.
func (s *Service) authorize(ctx context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource) error {
	if s.authorizer == nil {
		return apperror.New("invitation.unavailable")
	}
	return s.authorizer.Authorize(ctx, invocation, action, resource)
}

// authorizeActionDelegation refuses unless the caller may grant every Action
// at the scope.
func (s *Service) authorizeActionDelegation(
	ctx context.Context,
	invocation invocation.Invocation,
	actions []string,
	scopeType model.RoleScopeType,
	scopeID string,
) error {
	if s.authorizer == nil {
		return apperror.New("invitation.unavailable")
	}

	allowed, err := s.authorizer.CanDelegateActionsAtScope(ctx, invocation.Principal(), actions, scopeType, scopeID)
	if err != nil {
		return err
	}
	if !allowed {
		return access.DeniedError()
	}

	return nil
}

// visibility resolves and records the caller's institution-family scopes for
// invitation.view, invitation.manage, onboarding_batch.view, or
// academic_progression.manage, which share one scope shape: the Institution,
// Academic Unit subtrees, and Classes. Progression authority is a Class
// action, so its scopes are resolved for Class resources.
func (s *Service) visibility(
	ctx context.Context,
	invocation invocation.Invocation,
	action model.Action,
) (store.InvitationVisibilityScope, error) {
	resourceType := model.ResourceInstitution
	switch action {
	case model.ActionInvitationView, model.ActionInvitationManage, model.ActionOnboardingBatchView:
	case model.ActionAcademicProgressionManage:
		resourceType = model.ResourceClass
	default:
		return store.InvitationVisibilityScope{}, apperror.New("invitation.unavailable")
	}

	if s.authorizer == nil {
		return store.InvitationVisibilityScope{}, apperror.New("invitation.unavailable")
	}

	constraint, err := s.authorizer.GrantedScopes(ctx, invocation.Principal(), action, resourceType)
	if err != nil {
		return store.InvitationVisibilityScope{}, err
	}

	institution, err := s.authorizer.Institution(ctx)
	if err != nil {
		return store.InvitationVisibilityScope{}, invitationStoreErrors.Translate(err)
	}

	allowed := constraint.InstitutionWide || len(constraint.AcademicUnitRootIDs) > 0 || len(constraint.ClassIDs) > 0
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	scopeType, scopeID := model.RoleScopeInstitution, institution.ID.String()
	if len(constraint.AcademicUnitRootIDs) > 0 {
		resource = model.Resource{Type: model.ResourceAcademicUnit, ID: constraint.AcademicUnitRootIDs[0].String()}
		scopeType, scopeID = model.RoleScopeAcademicUnit, resource.ID
	} else if len(constraint.ClassIDs) > 0 {
		resource = model.Resource{Type: model.ResourceClass, ID: constraint.ClassIDs[0].String()}
		scopeType, scopeID = model.RoleScopeClass, resource.ID
	}

	if err = s.authorizer.RecordAuthorizationDecision(
		ctx,
		invocation.Principal(),
		action,
		resource,
		scopeType,
		scopeID,
		invocation.RequestMetadata(),
		allowed,
	); err != nil {
		return store.InvitationVisibilityScope{}, err
	}

	if !allowed {
		return store.InvitationVisibilityScope{}, access.DeniedError()
	}

	return store.InvitationVisibilityScope{
		InstitutionWide:     constraint.InstitutionWide,
		AcademicUnitRootIDs: append([]model.AcademicUnitID(nil), constraint.AcademicUnitRootIDs...),
		ClassIDs:            append([]model.ClassID(nil), constraint.ClassIDs...),
	}, nil
}
