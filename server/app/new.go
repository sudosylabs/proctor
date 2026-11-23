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
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/search"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// New builds the application graph. Only the module-root composition package
// calls it.
func New(deps Dependencies) (*App, error) {
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
	administrations, err := newAdministration(deps, shared, authorizer, &capabilities)
	if err != nil {
		return nil, err
	}

	academics, err := newAcademic(deps, shared, authorizer)
	if err != nil {
		return nil, err
	}

	// The job runtime is built last; newJobRuntime attaches its wake to these
	// proposals.
	defaultPictureJobs := newDefaultProfilePictureJobs(deps)
	users, err := newUsers(deps, shared, authorizer, defaultPictureJobs)
	if err != nil {
		return nil, err
	}

	// External sign-in admits through an invitation and MFA rekey proposes
	// jobs, but onboarding and the job runtime are built after identity, so
	// identity gets handles that are connected once they exist.
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
	runtime, err := newJobRuntime(deps, shared, authorizer, exams, administrations, users, defaultPictureJobs, onboardingPorts, wake)
	if err != nil {
		return nil, err
	}
	if runtime.engine != nil && deps.Store.Institution() != nil {
		changes := realtime.NewJobChangePublisher(shared.realtime, deps.Store.Institution())
		if err = runtime.engine.AttachChangeListener(changes); err != nil {
			return nil, err
		}
	}

	searches := search.New(search.Dependencies{
		Exams:              exams.authoring,
		Users:              users,
		ClassAuthorization: authorizer,
		Classes:            deps.Store.Class(),
	})
	counts := administration.NewCounts(administrationCountsDependencies(onboardingPorts, runtime, identities))
	terminals, err := newTerminals(deps, exams.attempts)
	if err != nil {
		return nil, err
	}
	assistants, err := newAssistant(deps, shared, exams.attempts, terminals)
	if err != nil {
		return nil, err
	}
	if assistants != nil {
		if err = shared.realtime.AttachAssistantListener(assistants.NotifyEvent); err != nil {
			return nil, err
		}
	}

	return &App{
		ExamPeople:           people.New(deps.Store, authorizer).Reporting("exam.unavailable"),
		ReviewPeople:         people.New(deps.Store, authorizer).Reporting("exam.integrity_review.unavailable"),
		SittingPeople:        people.New(deps.Store, authorizer).Reporting("exam.sitting.unavailable"),
		AttemptPeople:        people.New(deps.Store, authorizer).Reporting("exam.attempt.unavailable"),
		InvitationPeople:     people.New(deps.Store, authorizer).Reporting("invitation.unavailable"),
		AdministrationPeople: people.New(deps.Store, authorizer).Reporting("administration.unavailable"),
		DesktopPolicyPeople:  people.New(deps.Store, authorizer).Reporting("desktop_compatibility_policy.unavailable"),
		OnboardingPeople:     people.New(deps.Store, authorizer).Reporting("onboarding_import.unavailable"),
		ProgressionPeople:    people.New(deps.Store, authorizer).Reporting("student_progression.unavailable"),
		Terminals:            terminals,
		Previews:             environment.NewPreviews(terminals),
		Assistant:            assistants,
		Identity:             identities,
		Onboarding:           onboardings,
		Administration:       administrations,
		Academic:             academics,
		ExamAuthoring:        exams.authoring,
		ExamCopies:           exams.copies,
		ExamPublication:      exams.publication,
		Sittings:             exams.sittings,
		Attempts:             exams.attempts,
		Reviews:              exams.reviews,
		ExamRecords:          exams.records,
		ExamExports:          exams.exports,
		Resources:            exams.resources,
		Corrections:          exams.corrections,
		StarterWorkspaces:    exams.starterWorkspaces,
		Users:                users,
		Search:               searches,
		Counts:               counts,
		Realtime:             shared.realtime,
		RealtimeAdmission: NewRealtimeAdmission(RealtimeAdmissionDependencies{
			Principals: identities,
			Exams:      exams.authoring,
			Sittings:   exams.sittings,
			Reviews: review.SittingManagerAuthorizer{
				Reviews:  deps.Store.ExamIntegrityReview(),
				Sittings: exams.sittings,
				Audit:    shared.audit,
			},
			Authorizer:  authorizer,
			Operational: deps.OperationalRecorder,
		}),
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

// sharedMechanics is what every area is built over. It exists only during
// New.
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
		Attempts:    examAttempts,
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

// newInstitutionName reads the institution display name for mail copy
// through its own short-lived cache.
func newInstitutionName(deps Dependencies) func() string {
	return mail.NewInstitutionNameCache(mail.InstitutionNameCacheDependencies{
		Institutions: deps.Store.Institution(),
		Now:          time.Now,
	}).Name
}

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
	capabilities *identity.DeploymentCapabilities,
) (*administration.Service, error) {
	return administration.New(administration.Dependencies{
		AcademicUnits:           deps.Store.AcademicUnit(),
		Store:                   deps.Store,
		Authorizer:              authorizer,
		Auditor:                 shared.audit,
		Capabilities:            capabilities,
		Mail:                    shared.mail,
		Publisher:               shared.realtime,
		CanonicalOrigin:         deps.PublicURL,
		SupportAddress:          deps.SupportAddress,
		PasswordMinimumLength:   deps.Password.MinimumLength,
		RecentAuthenticationTTL: deps.RecentAuthenticationTTL,
		Sessions:                deps.Sessions,
		Plugins:                 deps.Plugins,
		AssistantProviders:      deps.AssistantProviders,
		AuditExportContent:      deps.FileContent,
		Now:                     time.Now,
	})
}

func newAcademic(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*academic.Service, error) {
	return academic.New(academic.Dependencies{
		Store:      deps.Store,
		Authorizer: authorizer,
		Auditor:    shared.audit,
		Mail:       shared.mail,
		Publisher:  shared.realtime,
		Now:        time.Now,
		NewID:      model.NewID,
	})
}

// newDefaultProfilePictureJobs returns nil when jobs are disabled.
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
		Store:       deps.Store,
		Authorizer:  authorizer,
		Auditor:     shared.audit,
		Publisher:   shared.realtime,
		DefaultJobs: proposals,
		Pictures:    deps.FileContent,
		Now:         time.Now,
	})
}

func newIdentity(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	capabilities identity.DeploymentCapabilities,
	invitations *externalInvitationAcceptor,
	wake *jobWake,
) (*identity.Service, error) {
	return identity.New(identity.Dependencies{
		Store:                     deps.Store,
		Sittings:                  deps.Store.ExamSitting(),
		Cache:                     deps.Cache,
		Registry:                  deps.Registry,
		Authorizer:                authorizer,
		Auditor:                   shared.audit,
		Capabilities:              &capabilities,
		Publisher:                 shared.realtime,
		Mail:                      shared.mail,
		Hasher:                    shared.hasher,
		Attempts:                  shared.attempts,
		Invalidator:               shared.invalidator,
		Invitations:               invitations,
		Operational:               deps.OperationalRecorder,
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
		Auditor:                 shared.audit,
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

// externalInvitationAcceptor breaks the build-order cycle: identity is built
// before onboarding, so New connects this to onboarding afterwards.
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

// jobWake breaks the build-order cycle: identity and onboarding need the wake
// before the job runtime they propose jobs to exists.
type jobWake struct{ wake func() }

func (w *jobWake) Wake() {
	if w.wake != nil {
		w.wake()
	}
}
