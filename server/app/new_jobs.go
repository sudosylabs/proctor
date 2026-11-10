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
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
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
	operations *JobOperations
	mail       *MailService
}

// newJobRuntime builds the Job engine over every application Job definition,
// connects the wakes that services built earlier hold, and builds the
// operator Job and mail services on it.
func newJobRuntime(
	deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer, exams examServices,
	users *user.Service, defaultPictureJobs *jobhandler.DefaultProfilePictureJobs, onboardingPorts onboarding.Ports, wake *jobWake,
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
		mailHealth = newMailHealth(deps.MailDeliverySender != nil && deps.MailDeliverySender.Enabled())
	}
	definitions := buildApplicationJobDefinitions(deps, shared, exams, users, defaultPictureJobs, onboardingPorts, mailHealth)
	engine, err := job.New(job.Dependencies{
		Store: deps.Store.Job(), Descriptors: definitions.descriptors, NodeID: deps.NodeID,
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
	var mailOperations *MailService
	if deps.Store.Mail() != nil {
		mailOperations, err = newMailService(mailServiceDependencies{Mail: deps.Store.Mail(), Users: deps.Store.User(), Authorization: mailAuthorizationAdapter{authorization: authorizer, institutions: deps.Store.Institution()}, Audit: shared.audit.MailAudit(), Attempts: shared.attempts, Renderer: deps.MailTemplateRenderer, Sender: deps.MailDeliverySender, Metrics: deps.MailMetricsReader, Sealer: deps.MailSecretSealer, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Now: time.Now,
			InstitutionName: (&institutionNameCache{institutions: deps.Store.Institution(), now: time.Now}).Name})
		if err != nil {
			return jobRuntime{}, err
		}
		mailOperations.rekeyJobs = deps.Store.Job()
		mailOperations.rekeyAudit = shared.audit.MutationAudit()
		mailOperations.wake = engine.Wake
	}
	operations, err := newJobOperationsService(jobOperationsServiceDependencies{Jobs: engine, Authorization: jobOperationsAuthorization{authorization: authorizer, institutions: deps.Store.Institution()}, Audit: shared.audit.MutationAudit(),
		Users: deps.Store.User(), ExamExports: deps.Store.ExamExport(), Now: time.Now})
	if err != nil {
		return jobRuntime{}, err
	}
	return jobRuntime{engine: engine, operations: operations, mail: mailOperations}, nil
}

type activeMailPayloadKeyStore interface {
	InspectKeyState(context.Context) (*store.MailKeyState, error)
}

func validateActiveMailPayloadKeys(ctx context.Context, persistence activeMailPayloadKeyStore, sealer interface {
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
	defaultPictureJobs *jobhandler.DefaultProfilePictureJobs,
	onboardingPorts onboarding.Ports,
	mailHealth *MailHealth,
) applicationJobDefinitions {
	if mailHealth == nil {
		mailHealth = newMailHealth(deps.MailDeliverySender != nil && deps.MailDeliverySender.Enabled())
	}
	mailDelivery := jobhandler.MailDeliveryDependencies{
		Deliveries: deps.Store.Mail(), Sender: deps.MailDeliverySender, Sealer: deps.MailSecretSealer,
		Recorder: jobMailDeliveryRecorder{recorder: deps.MailDeliveryRecorder}, Health: jobMailHealth{health: mailHealth},
		Relevance: jobMailDeliveryIsRelevant, Now: time.Now,
	}
	retentionJobs := administration.NewRetentionJobs(shared.audit, deps.Store.Retention(), deps.Store.User(), shared.mail, time.Now)
	jobs, now := deps.Store.Job(), time.Now
	// This is the one place that lists every enabled Job and the schedule
	// that proposes it. Each line names the collaborators that Job uses.
	descriptors := []job.Descriptor{
		jobhandler.NewDefaultProfilePictureDescriptor(users),
		jobhandler.NewDefaultProfilePictureReconciliationDescriptor(deps.Store.User(), defaultPictureJobs, now),
		jobhandler.NewFilePurgeDescriptor(jobhandler.FilePurgeDependencies{
			Files: deps.Store.File(), Content: deps.FileContent,
			StarterWorkspaces: deps.Store.ExamStarterWorkspace(), StarterWorkspaceContent: deps.FileContent,
			AttemptWorkspaces: deps.Store.ExamAttemptWorkspace(), AttemptWorkspaceContent: deps.FileContent,
			Continuations: jobs, Now: now,
		}),
		jobhandler.NewCommandOutcomeCleanupDescriptor(deps.Store.CommandOutcome()),
		jobhandler.NewAuditAttemptSweepDescriptor(deps.Store.Audit()),
		jobhandler.NewSessionPurgeDescriptor(deps.Store.Session(), deps.Store.SessionCredential(), deps.Sessions.RetiredRetention),
		jobhandler.NewMailDeliveryDescriptor(mailDelivery),
		jobhandler.NewMailCredentialDeliveryDescriptor(mailDelivery),
		jobhandler.NewSittingMailExpansionDescriptor(deps.Store.ExamSitting(), exams.sittingMail),
		jobhandler.NewMailCleanupDescriptor(deps.Store.Mail(), deps.Store.ExamSitting(), mailDelivery.Recorder),
		jobhandler.NewMailRekeyDescriptor(deps.Store.Mail(), deps.MailSecretSealer),
		jobhandler.NewMFARekeyDescriptor(deps.Store.MFA(), deps.MFASecretSealer),
		jobhandler.NewInvitationMaintenanceDescriptor(deps.Store.Invitation(), deps.Store.OnboardingImport(), deps.FileContent, now),
		jobhandler.NewExamSittingLifecycleDescriptor(exams.sittings),
		jobhandler.NewExamSittingSealingDescriptor(exams.sittings, exams.attempts, jobs, now, model.NewJobID),
		jobhandler.NewExamSittingLifecycleRecoveryDescriptor(exams.sittings),
		jobhandler.NewExamExportBuildDescriptor(exams.exports),
		jobhandler.NewExamExportCleanupDescriptor(deps.Store.ExamExport(), deps.FileContent),
		jobhandler.NewRetentionExpiryDescriptor(deps.Store.Retention(), retentionJobs.ExpiryAudit, jobs, now),
		jobhandler.NewRetentionReconcileDescriptor(deps.Store.Retention(), retentionJobs.Audit, jobs, now),
		jobhandler.NewRetentionPurgeDescriptor(deps.Store.Retention(), deps.FileContent, jobs, now),
		jobhandler.NewRetentionNoticeDescriptor(deps.Store.Retention(), retentionJobs.Notices, jobs, now),
	}
	if onboardingPorts.ImportJobs != nil {
		descriptors = append(descriptors, jobhandler.NewOnboardingImportDescriptors(onboardingPorts.ImportJobs)...)
	}
	// Job history cleanup keeps each Job type for the retention its
	// descriptor declares, so it is built after the rest.
	descriptors = append(descriptors, jobhandler.NewJobHistoryCleanupDescriptor(jobs, jobs, now, descriptors))

	recurrences := []job.Recurrence{
		jobhandler.NewDefaultProfilePictureReconciliationRecurrence(jobs, now),
		jobhandler.NewFilePurgeRecurrence(jobs, now),
		jobhandler.NewJobHistoryCleanupRecurrence(jobs, now),
		jobhandler.NewCommandOutcomeCleanupRecurrence(jobs, now),
		jobhandler.NewAuditAttemptSweepRecurrence(jobs, now),
		jobhandler.NewSessionPurgeRecurrence(jobs, now),
		jobhandler.NewMailCleanupRecurrence(jobs, now),
		jobhandler.NewInvitationMaintenanceRecurrence(jobs, now),
		jobhandler.NewExamSittingLifecycleRecoveryRecurrence(jobs, now),
		jobhandler.NewExamExportCleanupRecurrence(jobs, now),
		jobhandler.NewRetentionExpiryRecurrence(jobs, now),
		jobhandler.NewRetentionReconcileRecurrence(jobs, now),
		jobhandler.NewRetentionPurgeRecurrence(jobs, now),
		jobhandler.NewRetentionNoticeRecurrence(jobs, now),
	}
	periodicTasks := []job.PeriodicTask{
		{Name: examAttemptExpiryPeriodicTaskName, Interval: examAttemptExpiryScanInterval, Runner: examAttemptExpiryPeriodicRunner{attempts: exams.attempts}},
	}
	periodicTasks = append(periodicTasks, identity.MaintenanceTasks(deps.Store)...)
	periodicTasks = append(periodicTasks, []job.PeriodicTask{
		{Name: "exam-sitting-mail-reconciliation", Interval: sittingMailReconciliationInterval,
			Runner: sittingMailReconciliationPeriodicRunner{sittings: deps.Store.ExamSitting(), mail: exams.sittingMailPreparation}},
	}...)
	if deps.Store.Mail() != nil && deps.MailDeliverySender != nil {
		periodicTasks = append(periodicTasks, job.PeriodicTask{Name: "mail-maintenance-monitor", Interval: time.Minute, Runner: mailMaintenanceMonitor{mail: deps.Store.Mail(), sender: deps.MailDeliverySender, health: mailHealth, recorder: deps.MailDeliveryRecorder, now: time.Now}})
	}
	return applicationJobDefinitions{descriptors: descriptors, recurrences: recurrences, periodicTasks: periodicTasks}
}

// administrationCountsDependencies projects the summary's counters from the
// services that exist; a reduced lifecycle test graph may lack some, and
// their counts are then left out.
func administrationCountsDependencies(onboardingPorts onboarding.Ports, jobs jobRuntime) administrationCountsServiceDependencies {
	var deps administrationCountsServiceDependencies
	if onboardingPorts.CountPendingInvitations != nil {
		deps.Invitations = pendingInvitationCounter(onboardingPorts.CountPendingInvitations)
	}
	if onboardingPorts.CountOpenImports != nil {
		deps.Imports = openImportCounter(onboardingPorts.CountOpenImports)
	}
	if jobs.operations != nil {
		deps.Jobs = jobs.operations
	}
	if jobs.mail != nil {
		deps.Mail = jobs.mail
	}
	return deps
}
