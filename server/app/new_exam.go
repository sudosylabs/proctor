// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
)

type examServices struct {
	authoring              *exam.Authoring
	publication            *exam.Publication
	sittings               *sitting.Service
	attempts               *attempt.Service
	reviews                *review.Service
	records                *exam.Records
	exports                *exam.Exports
	resources              *resource.Service
	corrections            *correction.Service
	starterWorkspaces      *workspace.Service
	sittingMail            *mail.SittingComposer
	sittingMailPreparation sitting.ScheduleMail
}

func newExamServices(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	desktopBuilds *identity.Service,
) (examServices, error) {
	var built examServices
	var err error
	if built.authoring, err = newExamAuthoring(deps, shared, authorizer); err != nil {
		return examServices{}, err
	}
	if built.sittingMail, err = newSittingMail(deps); err != nil {
		return examServices{}, err
	}
	if built.publication, err = newExamPublication(deps, shared, authorizer); err != nil {
		return examServices{}, err
	}

	built.sittingMailPreparation = sitting.ScheduleMail{
		Composer:  built.sittingMail,
		Revisions: deps.Store.ExamRevision(),
		Classes:   deps.Store.Class(),
	}
	if built.sittings, err = newSittings(deps, shared, authorizer, built.sittingMailPreparation); err != nil {
		return examServices{}, err
	}
	if built.attempts, err = newAttempts(deps, shared, built.sittings, desktopBuilds); err != nil {
		return examServices{}, err
	}
	if built.reviews, err = newReviews(deps, shared, built.sittings); err != nil {
		return examServices{}, err
	}
	if built.resources, err = newExamResources(deps, shared, authorizer); err != nil {
		return examServices{}, err
	}
	if built.corrections, err = newCorrections(deps, shared, authorizer); err != nil {
		return examServices{}, err
	}
	if built.starterWorkspaces, err = newStarterWorkspaces(deps, shared, authorizer); err != nil {
		return examServices{}, err
	}
	if built.records, err = newExamRecords(deps, shared, authorizer); err != nil {
		return examServices{}, err
	}
	if built.exports, err = newExamExports(deps, shared, authorizer); err != nil {
		return examServices{}, err
	}

	return built, nil
}

func newExamAuthoring(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
) (*exam.Authoring, error) {
	return exam.NewAuthoring(exam.AuthoringDependencies{
		Persistence:       deps.Store.ExamAuthoring(),
		Memberships:       deps.Store.AcademicUnitMember(),
		Users:             deps.Store.User(),
		Sittings:          deps.Store.ExamSitting(),
		Mail:              shared.mail,
		Authorizer:        authorizer,
		Auditor:           shared.audit,
		Publisher:         shared.realtime,
		Now:               time.Now,
		NewID:             model.NewExamID,
		InstitutionOrigin: deps.PublicURL,
		Operational:       deps.OperationalRecorder,
	})
}

func newSittingMail(deps Dependencies) (*mail.SittingComposer, error) {
	return newSittingMailPreparer(
		deps.MailTemplateRenderer,
		deps.MailDeliverySender,
		deps.MailSecretSealer,
		time.Now,
		newInstitutionName(deps),
	)
}

// newSittingMailPreparer takes an explicit clock so its tests can freeze
// time.
func newSittingMailPreparer(
	renderer *mail.TemplateRenderer,
	sender mail.Sender,
	sealer *secretseal.Sealer,
	now func() time.Time,
	institution func() string,
) (*mail.SittingComposer, error) {
	return mail.NewSittingComposer(mail.SittingComposerDependencies{
		Renderer:    renderer,
		Sender:      sender,
		Sealer:      sealer,
		Now:         now,
		Institution: institution,
	})
}

func newExamPublication(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
) (*exam.Publication, error) {
	return exam.NewPublication(exam.PublicationDependencies{
		Persistence:        deps.Store.ExamRevision(),
		Access:             deps.Store.ExamAuthoring(),
		Memberships:        deps.Store.AcademicUnitMember(),
		Authorizer:         authorizer,
		Auditor:            shared.audit,
		Publisher:          shared.realtime,
		Now:                time.Now,
		NewID:              model.NewExamRevisionID,
		InstitutionOrigin:  deps.PublicURL,
		ExecutionEnabled:   deps.ExecutionEnabled,
		AssistantEnabled:   deps.AssistantEnabled,
		AssistantProviders: deps.AssistantProviders,
		Operational:        deps.OperationalRecorder,
	})
}

func newSittings(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	scheduleMail sitting.ScheduleMail,
) (*sitting.Service, error) {
	return sitting.New(sitting.Dependencies{
		Persistence: deps.Store.ExamSitting(),
		Access:      deps.Store.ExamAuthoring(),
		Memberships: deps.Store.AcademicUnitMember(),
		Authorizer:  authorizer,
		Auditor:     shared.audit,
		Publisher:   shared.realtime,
		Jobs:        sittingLifecycleJobs(deps),
		Mail:        scheduleMail,
		Now:         time.Now,
		NewID:       model.NewExamSittingID,
		Operational: deps.OperationalRecorder,
	})
}

func newAttempts(
	deps Dependencies,
	shared sharedMechanics,
	sittings *sitting.Service,
	desktopBuilds *identity.Service,
) (*attempt.Service, error) {
	submissionMail := attempt.SubmissionReceiptMail{
		Composer:  shared.mail,
		Users:     deps.Store.User(),
		Sittings:  deps.Store.ExamSitting(),
		Revisions: deps.Store.ExamRevision(),
	}
	return attempt.New(attempt.Dependencies{
		KillSealedAssistantSessions: deps.KillSealedAssistantSessions,

		AssistantSessions: deps.Store.AssistantTranscript(),
		Persistence:       deps.Store.ExamAttempt(),
		FocusLoss:         deps.Store.ExamFocusLoss(),
		SecurityPreflight: deps.Store.SecurityPreflight(),
		NativeDelivery:    deps.Store.NativeDelivery(),
		BrowserDelivery:   deps.Store.BrowserDelivery(),
		DeliveryBudget:    deps.Store.DeliveryBudget(),
		Workspace:         deps.Store.ExamAttemptWorkspace(),
		Submissions:       deps.Store.ExamSubmission(),
		Sittings:          deps.Store.ExamSitting(),
		Managers: attempt.SittingManagerAuthorizer{
			Sittings:    sittings,
			Submissions: deps.Store.ExamSubmission(),
			Audit:       shared.audit,
		},
		Auditor:             shared.audit,
		Publisher:           shared.realtime,
		Content:             deps.FileContent,
		Mail:                submissionMail,
		DesktopBuilds:       desktopBuilds,
		Now:                 time.Now,
		NewAttemptID:        model.NewExamAttemptID,
		NewWorkspaceID:      model.NewExamAttemptWorkspaceID,
		NewParticipation:    model.NewAttemptParticipationID,
		NewConnection:       model.NewAttemptConnectionID,
		NewEvidence:         model.NewIntegrityEvidenceID,
		NewFlag:             model.NewIntegrityFlagID,
		NewSuspension:       model.NewAttemptSuspensionID,
		NewFocusLossSignal:  model.NewFocusLossSignalID,
		EnvironmentWaker:    deps.EnvironmentWaker,
		EnvironmentSettler:  environmentSettler(deps),
		Environments:        deps.Store.AttemptEnvironment(),
		NewDiscrepancy:      model.NewIntegrityDiscrepancyID,
		NewWorkspaceEntry:   model.NewAttemptWorkspaceEntryID,
		NewWorkspaceObject:  model.NewAttemptWorkspaceObjectID,
		NewWorkspaceVersion: model.NewWorkspaceContentVersion,
		NewSubmission:       model.NewSubmissionID,
		NewReceiptCode:      model.NewSubmissionReceiptCode,
		NewDomainEvent:      model.NewDomainEventID,
		NewJob:              model.NewJobID,
		Operational:         deps.OperationalRecorder,
	})
}

func newReviews(deps Dependencies, shared sharedMechanics, sittings *sitting.Service) (*review.Service, error) {
	resultReleaseMail := review.ResultReleaseMail{
		Composer:  shared.mail,
		Users:     deps.Store.User(),
		Sittings:  deps.Store.ExamSitting(),
		Revisions: deps.Store.ExamRevision(),
	}
	return review.New(review.Dependencies{
		Persistence: deps.Store.ExamIntegrityReview(),
		Authorizer: review.SittingManagerAuthorizer{
			Reviews:  deps.Store.ExamIntegrityReview(),
			Sittings: sittings,
			Audit:    shared.audit,
		},
		Auditor:       shared.audit,
		Publisher:     shared.realtime,
		Mail:          resultReleaseMail,
		Now:           time.Now,
		NewReviewID:   model.NewSubmissionReviewID,
		NewDecisionID: model.NewIntegrityReviewDecisionID,
		Transcripts:   deps.Store.AssistantTranscript(),
		Environments:  deps.Store.AttemptEnvironment(),
	})
}

func newExamResources(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*resource.Service, error) {
	return resource.New(resource.Dependencies{
		Persistence:   deps.Store.ExamResource(),
		Access:        deps.Store.ExamAuthoring(),
		Memberships:   deps.Store.AcademicUnitMember(),
		Authorizer:    authorizer,
		Auditor:       shared.audit,
		Publisher:     shared.realtime,
		Content:       deps.FileContent,
		Now:           time.Now,
		NewResourceID: model.NewExamResourceID,
		NewEntryID:    model.NewFileEntryID,
		NewRevisionID: model.NewFileRevisionID,
		NewLeaseID:    model.NewUploadLeaseID,
	})
}

func newCorrections(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
) (*correction.Service, error) {
	return correction.New(correction.Dependencies{
		Persistence:       deps.Store.ExamCorrection(),
		Revisions:         deps.Store.ExamRevision(),
		Sittings:          deps.Store.ExamSitting(),
		Access:            deps.Store.ExamAuthoring(),
		Memberships:       deps.Store.AcademicUnitMember(),
		Authorizer:        authorizer,
		Auditor:           shared.audit,
		Publisher:         shared.realtime,
		Content:           deps.FileContent,
		Now:               time.Now,
		NewStageID:        model.NewExamCorrectionResourceStageID,
		NewResourceID:     model.NewExamResourceID,
		NewEntryID:        model.NewFileEntryID,
		NewFileRevisionID: model.NewFileRevisionID,
		NewLeaseID:        model.NewUploadLeaseID,
		NewRenditionID:    model.NewFileRenditionID,
		NewExamRevisionID: model.NewExamRevisionID,
		InstitutionOrigin: deps.PublicURL,
	})
}

func newStarterWorkspaces(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*workspace.Service, error) {
	return workspace.New(workspace.Dependencies{
		Persistence: deps.Store.ExamStarterWorkspace(),
		Access:      deps.Store.ExamAuthoring(),
		Memberships: deps.Store.AcademicUnitMember(),
		Authorizer:  authorizer,
		Auditor:     shared.audit,
		Content:     deps.FileContent,
		Publisher:   shared.realtime,
		Now:         time.Now,
		NewEntryID:  model.NewStarterWorkspaceEntryID,
		NewObjectID: model.NewStarterWorkspaceObjectID,
		NewVersion:  model.NewWorkspaceContentVersion,
	})
}

func newExamRecords(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*exam.Records, error) {
	return exam.NewRecords(exam.RecordsDependencies{
		Persistence: deps.Store.ExamRecords(),
		Access:      deps.Store.ExamAuthoring(),
		Memberships: deps.Store.AcademicUnitMember(),
		Authorizer:  authorizer,
		Auditor:     shared.audit,
		RecentTTL:   deps.RecentAuthenticationTTL,
		Now:         time.Now,
		NewHoldID:   model.NewRetentionHoldID,
	})
}

func newExamExports(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*exam.Exports, error) {
	return exam.NewExports(exam.ExportsDependencies{
		Persistence: deps.Store.ExamExport(),
		Access:      deps.Store.ExamAuthoring(),
		Memberships: deps.Store.AcademicUnitMember(),
		Authorizer:  authorizer,
		Auditor:     shared.audit,
		Content:     deps.FileContent,
		Now:         time.Now,
		NewID:       model.NewExamExportID,
		NewJobID:    model.NewJobID,
	})
}

// attemptEnvironmentSettleBudget bounds how long a seal waits for an
// execution environment to settle.
const attemptEnvironmentSettleBudget = 5 * time.Second

// sittingLifecycleJobs delays a closing sitting's sealing job by the settle
// budget when the installation runs execution environments.
func sittingLifecycleJobs(deps Dependencies) jobs.ExamSittingLifecycleJobFactory {
	factory := jobs.NewExamSittingLifecycleJobFactory(time.Now, model.NewJobID)
	if deps.ExecutionEnabled {
		factory = factory.WithSettleBudget(attemptEnvironmentSettleBudget)
	}
	return factory
}

func environmentSettler(deps Dependencies) attempt.EnvironmentSettler {
	if !deps.ExecutionEnabled || deps.Store.AttemptEnvironment() == nil || deps.EnvironmentSettles == nil {
		return nil
	}
	settler, err := environment.NewSettler(environment.SettlerDependencies{
		Environments:  deps.Store.AttemptEnvironment(),
		Waker:         deps.EnvironmentWaker,
		Signals:       deps.EnvironmentSettles,
		Budget:        attemptEnvironmentSettleBudget,
		NewEvidenceID: model.NewIntegrityEvidenceID,
		Report:        deps.EnvironmentDiagnostics,
	})
	if err != nil {
		return nil
	}
	return settler
}

// newTerminals returns nil when the installation runs no execution
// environments.
func newTerminals(deps Dependencies, attempts *attempt.Service) (*environment.Terminals, error) {
	if deps.EnvironmentHosts == nil || deps.Store.AttemptEnvironment() == nil || attempts == nil {
		return nil, nil
	}
	return environment.NewTerminals(environment.TerminalsDependencies{
		Environments: deps.Store.AttemptEnvironment(),
		Evidence:     deps.Store.ExamAttemptWorkspace(),
		Hosts:        deps.EnvironmentHosts,
		Candidates:   environment.AttemptCandidates(attempts),
		Report:       deps.EnvironmentDiagnostics,
	})
}

// newAssistant returns nil when the installation has the assistant off, so
// every assistant route answers exam.attempt.assistant_unavailable.
func newAssistant(deps Dependencies, shared sharedMechanics, attempts *attempt.Service,
	terminals *environment.Terminals) (*assistant.Service, error) {
	if !deps.AssistantEnabled || deps.AssistantGateway == nil || deps.Store.AssistantTranscript() == nil {
		return nil, nil
	}
	return assistant.New(assistant.Dependencies{
		Wake:              shared.realtime.NotifyAssistantChanged,
		Transcripts:       deps.Store.AssistantTranscript(),
		Attempts:          attempts,
		Terminals:         terminals,
		Gateway:           deps.AssistantGateway,
		Limits:            deps.AssistantLimits,
		Auditor:           shared.audit,
		Publisher:         shared.realtime,
		Now:               time.Now,
		NewConversationID: model.NewAssistantConversationID,
		NewTurnID:         model.NewAssistantTurnID,
		NewCallID:         model.NewAssistantCallID,
		NewOwnerToken:     model.NewCredentialToken,
	})
}
