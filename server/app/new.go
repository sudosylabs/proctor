// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/search"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// New constructs the application graph from explicit dependencies. Only the
// module-root composition package calls it. It reads top to bottom: validate
// the dependencies, build the shared mechanics and Access Control, then each
// area in the order its collaborators require, then the optional Job runtime
// and the services that span areas.
func New(deps Dependencies) (*App, error) {
	if deps.OperationalRecorder == nil {
		deps.OperationalRecorder = nopOperationalRecorder{}
	}
	if err := validateApplicationDependencies(deps); err != nil {
		return nil, err
	}

	shared, err := newSharedMechanics(deps)
	if err != nil {
		return nil, err
	}

	authorizer, err := newAuthorizer(deps, shared)
	if err != nil {
		return nil, err
	}

	capabilities := identity.DeploymentCapabilities{
		Providers:  deps.Registry,
		Mail:       deps.MailDeliverySender,
		Health:     shared.mailHealth,
		MFAEnabled: deps.MFA.Enabled,
	}
	administrations, err := newAdministration(deps, shared, authorizer, capabilities)
	if err != nil {
		return nil, err
	}

	academics, err := newAcademic(deps, shared, authorizer)
	if err != nil {
		return nil, err
	}

	// The Job runtime that a default profile picture proposal wakes is built
	// last, so newJobRuntime attaches its wake to these proposals.
	defaultPictureJobs := newDefaultProfilePictureJobs(deps)
	users, err := newUsers(deps, shared, authorizer, defaultPictureJobs)
	if err != nil {
		return nil, err
	}

	// External sign-in admits an External Identity through an Invitation, and
	// MFA rekey proposes Jobs. Onboarding and the Job runtime are built after
	// identity, so it receives handles that are connected once they exist.
	wake, acceptor := &jobWake{}, &externalInvitationAcceptor{}
	identities, err := newIdentity(deps, shared, authorizer, capabilities, acceptor, wake)
	if err != nil {
		return nil, err
	}

	exams, err := newExamServices(deps, shared, authorizer, identities)
	if err != nil {
		return nil, err
	}

	onboardings, onboardingPorts, err := newOnboarding(deps, shared, authorizer, identities, academics, administrations, users, wake)
	if err != nil {
		return nil, err
	}

	acceptor.accept = onboardingPorts.AcceptExternalIdentity
	runtime, err := newJobRuntime(deps, shared, authorizer, exams, users, defaultPictureJobs, onboardingPorts, wake)
	if err != nil {
		return nil, err
	}
	if runtime.engine != nil && deps.Store.Institution() != nil {
		changes := &jobChangePublisher{publisher: shared.realtime, institutions: deps.Store.Institution()}
		if err = runtime.engine.AttachChangeListener(changes); err != nil {
			return nil, err
		}
	}

	// Search spans Exams, Users and Classes, and the administration counts
	// span Invitations, Jobs, mail and onboarding imports, so both are built
	// once every one of those services exists.
	searches := search.New(search.Dependencies{
		Exams:              exams.authoring,
		Users:              users,
		ClassAuthorization: authorizer,
		Classes:            deps.Store.Class(),
	})
	counts := administration.NewCounts(administrationCountsDependencies(onboardingPorts, runtime))
	return &App{
		Identity:          identities,
		Onboarding:        onboardings,
		Administration:    administrations,
		Academic:          academics,
		ExamAuthoring:     exams.authoring,
		ExamPublication:   exams.publication,
		Sittings:          exams.sittings,
		Attempts:          exams.attempts,
		Reviews:           exams.reviews,
		ExamRecords:       exams.records,
		ExamExports:       exams.exports,
		Resources:         exams.resources,
		Corrections:       exams.corrections,
		StarterWorkspaces: exams.starterWorkspaces,
		Users:             users,
		Search:            searches,
		Counts:            counts,
		Realtime:          shared.realtime,
		RealtimeAdmission: &RealtimeAdmission{
			principals:    identities,
			exams:         exams.authoring,
			sittings:      exams.sittings,
			authorization: authorizer,
			operational:   deps.OperationalRecorder,
		},
		Jobs:      runtime.operations,
		Mail:      runtime.mail,
		jobEngine: runtime.engine,
	}, nil
}

func validateApplicationDependencies(deps Dependencies) error {
	if deps.Store == nil {
		return errors.New("store is required")
	}
	if deps.Cache == nil {
		return errors.New("cache is required")
	}
	if deps.MailDeliverySender == nil {
		return errors.New("mail delivery sender is required")
	}
	if deps.MailTemplateRenderer == nil {
		return errors.New("mail template renderer is required")
	}
	if deps.Registry == nil {
		return errors.New("external provider registry is required")
	}
	if deps.FileContent == nil {
		return errors.New("file content is required")
	}
	if deps.NodeID == "" {
		return errors.New("node ID is required")
	}
	if deps.AuthenticationDiagnostics == nil {
		return errors.New("authentication diagnostics is required")
	}
	if deps.RealtimeDiagnostics == nil {
		return errors.New("realtime diagnostics is required")
	}
	if deps.RecoveryDiagnostics == nil {
		return errors.New("recovery diagnostics is required")
	}

	return nil
}

// sharedMechanics holds what every area is built over: password hashing,
// attempt accounting, authentication cache invalidation, realtime
// publication, audit, mail composition and mail health. It exists only
// during New.
type sharedMechanics struct {
	hasher      *identity.PasswordHasher
	attempts    *identity.AttemptAccounting
	invalidator *identity.CacheInvalidator
	realtime    *realtime.Service
	audit       *access.Auditor
	mail        *mail.Composer
	mailHealth  *mail.Health
}

func newSharedMechanics(deps Dependencies) (sharedMechanics, error) {
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{
		Settings: deps.Password,
		Recorder: deps.PasswordWorkRecorder,
	})
	if err != nil {
		return sharedMechanics{}, err
	}

	attempts, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: deps.Cache})
	if err != nil {
		return sharedMechanics{}, err
	}

	invalidator, err := identity.NewCacheInvalidator(identity.CacheInvalidatorDependencies{
		Cache:       deps.Cache,
		Diagnostics: deps.AuthenticationDiagnostics,
	})
	if err != nil {
		return sharedMechanics{}, err
	}

	delivery, err := realtime.NewDelivery(realtime.DeliveryDependencies{AuthenticationInvalidator: invalidator, Diagnostics: deps.RealtimeDiagnostics})
	if err != nil {
		return sharedMechanics{}, err
	}

	examAttempts := deps.Store.ExamAttempt()
	if examAttempts == nil {
		return sharedMechanics{}, errors.New("realtime publication requires the Exam Attempt store")
	}

	publication, err := realtime.New(realtime.Dependencies{
		Delivery:    delivery,
		Diagnostics: deps.RealtimeDiagnostics,
		Attempts:    sessionAttemptTargets{attempts: examAttempts},
	})
	if err != nil {
		return sharedMechanics{}, err
	}

	audit, err := access.NewAuditor(access.AuditorDependencies{
		Audits:       deps.Store.Audit(),
		Institutions: deps.Store.Institution(),
		NodeID:       deps.NodeID,
	})
	if err != nil {
		return sharedMechanics{}, err
	}

	composer, err := mail.NewComposer(mail.ComposerDependencies{
		Renderer:    deps.MailTemplateRenderer,
		Sender:      deps.MailDeliverySender,
		Sealer:      deps.MailSecretSealer,
		Institution: newInstitutionName(deps),
	})
	if err != nil {
		return sharedMechanics{}, err
	}

	return sharedMechanics{
		hasher:      hasher,
		attempts:    attempts,
		invalidator: invalidator,
		realtime:    publication,
		audit:       audit,
		mail:        composer,
		mailHealth:  mail.NewHealth(deps.MailDeliverySender.Enabled()),
	}, nil
}

// newInstitutionName supplies the institution display name to mail copy,
// read through its own short-lived cache.
func newInstitutionName(deps Dependencies) func() string {
	return mail.NewInstitutionNameCache(mail.InstitutionNameCacheDependencies{
		Institutions: deps.Store.Institution(),
		Now:          time.Now,
	}).Name
}

// newAuthorizer builds Access Control over the Role and Role Binding stores
// and the scope resolver that places each Resource in its scopes.
func newAuthorizer(deps Dependencies, shared sharedMechanics) (*access.Authorizer, error) {
	scopeResolver, err := access.NewScopeResolver(access.ScopeResolverDependencies{
		Institutions:    deps.Store.Institution(),
		AcademicUnits:   deps.Store.AcademicUnit(),
		Classes:         deps.Store.Class(),
		Users:           deps.Store.User(),
		ClassMembers:    deps.Store.ClassMember(),
		Exams:           deps.Store.ExamAuthoring(),
		Sittings:        deps.Store.ExamSitting(),
		Submissions:     deps.Store.ExamSubmission(),
		AcademicPeriods: deps.Store.AcademicPeriod(),
		Programmes:      deps.Store.Programme(),
		ProgrammeLevels: deps.Store.ProgrammeLevel(),
	})
	if err != nil {
		return nil, err
	}

	return access.NewAuthorizer(access.AuthorizerDependencies{
		Roles:    deps.Store.Role(),
		Bindings: deps.Store.RoleBinding(),
		Resolver: scopeResolver,
		Audit:    shared.audit,
	})
}

func newAdministration(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	capabilities identity.DeploymentCapabilities,
) (*administration.Service, error) {
	return administration.New(administration.Dependencies{
		Store:                   deps.Store,
		Authorizer:              authorizer,
		Auditor:                 shared.audit.MutationAudit(),
		Capabilities:            capabilities,
		Mail:                    shared.mail,
		Publisher:               shared.realtime,
		CanonicalOrigin:         deps.PublicURL,
		SupportAddress:          deps.SupportAddress,
		PasswordMinimumLength:   deps.Password.MinimumLength,
		RecentAuthenticationTTL: deps.RecentAuthenticationTTL,
		Sessions:                deps.Sessions,
		Now:                     time.Now,
	})
}

func newAcademic(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*academic.Service, error) {
	return academic.New(academic.Dependencies{
		Store:      deps.Store,
		Authorizer: authorizer,
		Auditor:    shared.audit.MutationAudit(),
		Mail:       shared.mail,
		Publisher:  shared.realtime,
		Now:        time.Now,
		NewID:      model.NewID,
	})
}

// newDefaultProfilePictureJobs proposes default profile picture generation;
// it exists only when Jobs are enabled.
func newDefaultProfilePictureJobs(deps Dependencies) *jobs.DefaultProfilePictureJobs {
	if deps.Store.Job() == nil {
		return nil
	}
	return jobs.NewDefaultProfilePictureJobs(jobs.DefaultProfilePictureJobsDependencies{Jobs: deps.Store.Job()})
}

func newUsers(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	defaultPictureJobs *jobs.DefaultProfilePictureJobs,
) (*user.Service, error) {
	var proposals user.DefaultPictureJobs
	if defaultPictureJobs != nil {
		proposals = defaultPictureJobs
	}

	return user.New(user.Dependencies{
		Store:           deps.Store,
		Authorizer:      authorizer,
		Auditor:         shared.audit.MutationAudit(),
		SettingsAuditor: shared.audit.UserSettingsAudit(),
		Publisher:       shared.realtime,
		DefaultJobs:     proposals,
		Uploads:         deps.FileContent,
		Reads:           deps.FileContent,
		Fallback:        deps.FileContent,
		Defaults:        deps.FileContent,
		Now:             time.Now,
	})
}

func newIdentity(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	capabilities identity.AccessPolicyCapabilitySource,
	invitations *externalInvitationAcceptor,
	wake *jobWake,
) (*identity.Service, error) {
	return identity.New(identity.Dependencies{
		Store:                     deps.Store,
		Cache:                     deps.Cache,
		Registry:                  deps.Registry,
		Authorizer:                authorizer,
		Auditor:                   shared.audit,
		Capabilities:              capabilities,
		Publisher:                 shared.realtime,
		Mail:                      shared.mail,
		Hasher:                    shared.hasher,
		Attempts:                  shared.attempts,
		Invalidator:               shared.invalidator,
		Invitations:               invitations,
		Operational:               operationalRecorderAdapter{recorder: deps.OperationalRecorder},
		MFASecretSealer:           deps.MFASecretSealer,
		MFARekeyWake:              wake.Wake,
		DesktopBuildCatalog:       deps.DesktopBuildCatalog,
		SkipDesktopCompatibility:  deps.SkipDesktopCompatibility,
		NodeID:                    deps.NodeID,
		PublicURL:                 deps.PublicURL,
		LoopbackHTTPDevelopment:   deps.LoopbackHTTPDevelopment,
		MFA:                       deps.MFA,
		Sessions:                  deps.Sessions,
		LoginRateLimit:            deps.LoginRateLimit,
		BootstrapProtection:       deps.BootstrapProtection,
		PersonalAccessToken:       deps.PersonalAccessToken,
		AccountRecovery:           deps.AccountRecovery,
		ExternalAuth:              deps.ExternalAuth,
		RecentAuthenticationTTL:   deps.RecentAuthenticationTTL,
		AuthenticationDiagnostics: deps.AuthenticationDiagnostics,
		RecoveryDiagnostics:       deps.RecoveryDiagnostics,
	})
}

// newOnboarding builds the onboarding area over the identity, academic and
// administration areas. New connects external sign-in to the Invitation
// acceptance its Ports return.
func newOnboarding(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	identities *identity.Service,
	academics *academic.Service,
	administrations *administration.Service,
	users *user.Service,
	wake *jobWake,
) (*onboarding.Service, onboarding.Ports, error) {
	return onboarding.New(onboarding.Dependencies{
		Store:                   deps.Store,
		Content:                 deps.FileContent,
		Authorizer:              authorizer,
		Auditor:                 shared.audit.MutationAudit(),
		Mail:                    shared.mail,
		Hasher:                  shared.hasher,
		Attempts:                shared.attempts,
		Identity:                identities,
		Academic:                academics,
		Administration:          administrations,
		Users:                   users,
		Publisher:               shared.realtime,
		Wake:                    wake.Wake,
		NodeID:                  deps.NodeID,
		PublicURL:               deps.PublicURL,
		AccountRecovery:         deps.AccountRecovery,
		RecentAuthenticationTTL: deps.RecentAuthenticationTTL,
	})
}

// externalInvitationAcceptor lets external sign-in in the identity area admit
// an External Identity through the Invitation its login state carries. The
// identity area is built first, so New connects it to the onboarding area.
type externalInvitationAcceptor struct {
	accept func(
		context.Context,
		*model.ExternalLoginState,
		*model.ExternalAuthenticationAssertion,
		store.AccessDeploymentCapabilities,
		model.RequestMetadata,
		string,
	) (*store.ExternalIdentityInvitationAcceptanceResult, error)
}

func (a *externalInvitationAcceptor) AcceptExternalIdentity(
	ctx context.Context,
	state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion,
	capabilities store.AccessDeploymentCapabilities,
	metadata model.RequestMetadata,
	sessionClientType string,
) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	return a.accept(ctx, state, assertion, capabilities, metadata, sessionClientType)
}

// jobWake wakes the Job runtime once it exists. The identity and onboarding
// areas need the wake when they are built, before the runtime that they
// propose Jobs to.
type jobWake struct{ wake func() }

func (w *jobWake) Wake() {
	if w.wake != nil {
		w.wake()
	}
}
