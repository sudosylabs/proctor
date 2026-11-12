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
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/events"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// jobRuntime is the durable Job engine and the operator services built on
// it. Every member is nil in a lifecycle-only graph that has no Job store,
// and mail is nil when the graph has no mail store.
type jobRuntime struct {
	engine     *job.Engine
	operations *job.Operations
	mail       *mail.Operations
}

// newJobRuntime builds the Job engine over every application Job definition,
// connects the wakes that services built earlier hold, and builds the
// operator Job and mail services on it.
func newJobRuntime(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	exams examServices,
	users *user.Service,
	defaultPictureJobs *jobs.DefaultProfilePictureJobs,
	onboardingPorts onboarding.Ports,
	wake *jobWake,
) (jobRuntime, error) {
	if deps.Store.Job() == nil {
		return jobRuntime{}, nil
	}
	if exams.sittings == nil || exams.attempts == nil {
		return jobRuntime{}, errors.New("Examination lifecycle use cases are required when Jobs are enabled")
	}

	// Enabled workers must be able to open every active payload before runtime
	// construction. Disabled workers suppress payloads before decryption, so a
	// deliberately absent or retired ring must not prevent that convergence.
	if deps.Store.Mail() != nil && deps.MailDeliverySender != nil && deps.MailDeliverySender.Enabled() {
		if err := validateActiveMailPayloadKeys(context.Background(), deps.Store.Mail(), deps.MailSecretSealer); err != nil {
			return jobRuntime{}, err
		}
	}

	mailHealth := shared.mailHealth
	if mailHealth == nil {
		mailHealth = mail.NewHealth(deps.MailDeliverySender != nil && deps.MailDeliverySender.Enabled())
	}

	// No plugin host exists yet, so the dispatcher has no handlers: a domain
	// event is delivered to nobody and its outbox row is retired.
	domainEvents, err := events.NewDispatcher()
	if err != nil {
		return jobRuntime{}, err
	}
	definitions := buildApplicationJobDefinitions(deps, shared, exams, users, defaultPictureJobs, onboardingPorts, mailHealth, domainEvents)
	engine, err := job.New(job.Dependencies{
		Store:         deps.Store.Job(),
		Descriptors:   definitions.descriptors,
		NodeID:        deps.NodeID,
		Diagnostics:   deps.RecoveryDiagnostics,
		Recorder:      deps.JobRecorder,
		Tracer:        deps.JobTracer,
		Policy:        job.Policy{PollInterval: 500 * time.Millisecond},
		Recurrences:   definitions.recurrences,
		PeriodicTasks: definitions.periodicTasks,
	})
	if err != nil {
		return jobRuntime{}, err
	}
	if defaultPictureJobs != nil {
		defaultPictureJobs.SetWake(engine.Wake)
	}
	if wake != nil {
		wake.wake = engine.Wake
	}

	var mailOperations *mail.Operations
	if deps.Store.Mail() != nil {
		mailOperations, err = mail.NewOperations(mail.OperationsDependencies{
			Mail:                    deps.Store.Mail(),
			Users:                   deps.Store.User(),
			Authorization:           mail.InstitutionAuthorization{Authorizer: authorizer, Institutions: deps.Store.Institution()},
			Audit:                   shared.audit.MailAudit(),
			Attempts:                shared.attempts,
			Renderer:                deps.MailTemplateRenderer,
			Sender:                  deps.MailDeliverySender,
			Metrics:                 deps.MailMetricsReader,
			Sealer:                  deps.MailSecretSealer,
			RecentAuthenticationTTL: deps.RecentAuthenticationTTL,
			Now:                     time.Now,
			InstitutionName:         newInstitutionName(deps),
			RekeyJobs:               deps.Store.Job(),
			RekeyAudit:              shared.audit.MutationAudit(),
			Wake:                    engine.Wake,
		})
		if err != nil {
			return jobRuntime{}, err
		}
	}

	operations, err := job.NewOperations(job.OperationsDependencies{
		Jobs:          engine,
		Authorization: job.InstitutionAuthorization{Authorizer: authorizer, Institutions: deps.Store.Institution()},
		Audit:         shared.audit.MutationAudit(),
		Users:         deps.Store.User(),
		ExamExports:   deps.Store.ExamExport(),
		Now:           time.Now,
	})
	if err != nil {
		return jobRuntime{}, err
	}

	return jobRuntime{engine: engine, operations: operations, mail: mailOperations}, nil
}

func validateActiveMailPayloadKeys(ctx context.Context, persistence store.MailStore, sealer interface {
	HasKey(string) bool
	PrimaryKeyID() string
}) error {
	if persistence == nil {
		return errors.New("mail payload-key persistence is unavailable")
	}

	state, err := persistence.InspectKeyState(ctx)
	if err != nil {
		return fmt.Errorf("inspect active mail payload keys: %w", err)
	}
	if state == nil {
		return errors.New("mail payload-key state is unavailable")
	}

	if state.RequiredPrimaryKeyID != "" {
		if sealer == nil {
			return fmt.Errorf("required mail primary key %s is unavailable", state.RequiredPrimaryKeyID)
		}
		if primary := sealer.PrimaryKeyID(); primary != state.RequiredPrimaryKeyID && !state.PrimaryPromotionAllowed {
			return fmt.Errorf("configured mail primary key %s does not match required primary key %s", primary, state.RequiredPrimaryKeyID)
		}
		if !sealer.HasKey(state.RequiredPrimaryKeyID) {
			return fmt.Errorf("required mail primary key %s is unavailable", state.RequiredPrimaryKeyID)
		}
	}

	if len(state.Active) == 0 {
		return nil
	}
	if sealer == nil {
		return errors.New("active mail payload key ring is unavailable")
	}

	for _, usage := range state.Active {
		if !sealer.HasKey(usage.KeyID) {
			return fmt.Errorf("active mail payload key %s is unavailable for %d references", usage.KeyID, usage.ActiveReferences)
		}
	}

	return nil
}

type applicationJobDefinitions struct {
	descriptors   []job.Descriptor
	recurrences   []job.Recurrence
	periodicTasks []job.PeriodicTask
}

// buildApplicationJobDefinitions keeps the complete durable-work recipe
// inspectable without constructing or starting the runtime. Composition tests
// use the same definition graph that production passes to the Job engine.
func buildApplicationJobDefinitions(
	deps Dependencies,
	shared sharedMechanics,
	exams examServices,
	users *user.Service,
	defaultPictureJobs *jobs.DefaultProfilePictureJobs,
	onboardingPorts onboarding.Ports,
	mailHealth *mail.Health,
	domainEvents *events.Dispatcher,
) applicationJobDefinitions {
	if mailHealth == nil {
		mailHealth = mail.NewHealth(deps.MailDeliverySender != nil && deps.MailDeliverySender.Enabled())
	}

	mailDelivery := jobs.MailDeliveryDependencies{
		Deliveries: deps.Store.Mail(),
		Sender:     deps.MailDeliverySender,
		Sealer:     deps.MailSecretSealer,
		Recorder:   deps.MailDeliveryRecorder,
		Health:     mailHealth,
		Relevance:  mail.DeliveryIsRelevant,
		Now:        time.Now,
	}
	retentionJobs := administration.NewRetentionJobs(shared.audit, deps.Store.Retention(), deps.Store.User(), shared.mail, time.Now)
	jobStore, now := deps.Store.Job(), time.Now
	// This is the one place that lists every enabled Job, one line per Job.
	// Each line names the collaborators that Job uses; a Job proposed on a
	// schedule carries its recurrence with it.
	registrations := []job.Registration{
		jobs.NewDefaultProfilePicture(users),
		jobs.NewDefaultProfilePictureReconciliation(deps.Store.User(), defaultPictureJobs, jobStore, now),
		jobs.NewFilePurge(jobs.FilePurgeDependencies{
			Files:                   deps.Store.File(),
			Content:                 deps.FileContent,
			StarterWorkspaces:       deps.Store.ExamStarterWorkspace(),
			StarterWorkspaceContent: deps.FileContent,
			AttemptWorkspaces:       deps.Store.ExamAttemptWorkspace(),
			AttemptWorkspaceContent: deps.FileContent,
			Jobs:                    jobStore,
			Now:                     now,
		}),
		jobs.NewCommandOutcomeCleanup(deps.Store.CommandOutcome(), jobStore, now),
		jobs.NewAuditAttemptSweep(deps.Store.Audit(), jobStore, now),
		jobs.NewSessionPurge(deps.Store.Session(), deps.Store.SessionCredential(), deps.Sessions.RetiredRetention, jobStore, now),
		jobs.NewMailDelivery(mailDelivery),
		jobs.NewMailCredentialDelivery(mailDelivery),
		jobs.NewSittingMailExpansion(deps.Store.ExamSitting(), exams.sittingMail),
		jobs.NewMailCleanup(deps.Store.Mail(), deps.Store.ExamSitting(), mailDelivery.Recorder, jobStore, now),
		jobs.NewMailRekey(deps.Store.Mail(), deps.MailSecretSealer),
		jobs.NewMFARekey(deps.Store.MFA(), deps.MFASecretSealer),
		jobs.NewInvitationMaintenance(deps.Store.Invitation(), deps.Store.OnboardingImport(), deps.FileContent, jobStore, now),
		jobs.NewExamSittingLifecycle(exams.sittings),
		jobs.NewExamSittingSealing(exams.sittings, exams.attempts, jobStore, now, model.NewJobID),
		jobs.NewExamSittingLifecycleRecovery(exams.sittings, jobStore, now),
		jobs.NewExamExportBuild(exams.exports),
		jobs.NewExamExportCleanup(deps.Store.ExamExport(), deps.FileContent, jobStore, now),
		jobs.NewDomainEventDispatch(deps.Store.DomainEvent(), domainEvents),
		jobs.NewRetentionExpiry(deps.Store.Retention(), retentionJobs.ExpiryAudit, jobStore, now),
		jobs.NewRetentionReconcile(deps.Store.Retention(), retentionJobs.Audit, jobStore, now),
		jobs.NewRetentionPurge(deps.Store.Retention(), deps.FileContent, jobStore, now),
		jobs.NewRetentionNotice(deps.Store.Retention(), retentionJobs.Notices, jobStore, now),
	}
	if onboardingPorts.ImportJobs != nil {
		registrations = append(registrations,
			jobs.NewOnboardingImportParse(onboardingPorts.ImportJobs),
			jobs.NewStudentProgressionPreview(onboardingPorts.ImportJobs),
			jobs.NewAcademicAdministrationPreviewResolve(onboardingPorts.ImportJobs),
			jobs.NewOnboardingImportExecute(onboardingPorts.ImportJobs),
		)
	}
	descriptors, recurrences := job.SplitRegistrations(registrations)

	// Job history cleanup keeps each Job type for the retention its
	// descriptor declares, so it is built after the rest. The engine runs
	// each recurrence on its own, so their order carries no meaning.
	historyCleanup := jobs.NewJobHistoryCleanup(jobStore, jobStore, now, descriptors)
	descriptors = append(descriptors, historyCleanup.Descriptor)
	recurrences = append(recurrences, *historyCleanup.Recurrence)

	periodicTasks := []job.PeriodicTask{jobs.NewExamAttemptExpiryTask(exams.attempts)}
	periodicTasks = append(periodicTasks, identity.MaintenanceTasks(deps.Store)...)
	periodicTasks = append(periodicTasks, jobs.NewSittingMailReconciliationTask(deps.Store.ExamSitting(), exams.sittingMailPreparation))
	if deps.Store.Mail() != nil && deps.MailDeliverySender != nil {
		periodicTasks = append(periodicTasks, job.PeriodicTask{
			Name:     "mail-maintenance-monitor",
			Interval: time.Minute,
			Runner: mail.MaintenanceMonitor{
				Mail:     deps.Store.Mail(),
				Sender:   deps.MailDeliverySender,
				Health:   mailHealth,
				Recorder: deps.MailDeliveryRecorder,
				Now:      time.Now,
			},
		})
	}

	return applicationJobDefinitions{descriptors: descriptors, recurrences: recurrences, periodicTasks: periodicTasks}
}

// administrationCountsDependencies projects the summary's counters from the
// services that exist; a reduced lifecycle test graph may lack some, and
// their counts are then left out.
func administrationCountsDependencies(onboardingPorts onboarding.Ports, runtime jobRuntime) administration.CountsDependencies {
	deps := administration.CountsDependencies{
		PendingInvitations:    onboardingPorts.CountPendingInvitations,
		OpenOnboardingImports: onboardingPorts.CountOpenImports,
	}
	if runtime.operations != nil {
		deps.RecentJobFailures = runtime.operations.CountRecentJobFailures
	}
	if runtime.mail != nil {
		deps.FailedMailDeliveries = runtime.mail.CountFailedMailDeliveries
	}
	return deps
}
