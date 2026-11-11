// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
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

// examServices are the Exam area's services, together with the Sitting mail
// composition that the Sitting service and the Sitting mail Jobs share.
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

// newExamServices builds the Exam area in dependency order: authoring and
// publication, Sittings, the Attempts and reviews that run inside them, the
// Draft content services, then records and exports. desktopBuilds resolves
// Desktop builds for Attempt admission.
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
		Auditor:           shared.audit.MutationAudit(),
		Outcomes:          deps.Store.CommandOutcome(),
		Publisher:         shared.realtime,
		Now:               time.Now,
		NewID:             model.NewExamID,
		InstitutionOrigin: deps.PublicURL,
		Operational:       operationalRecorderAdapter{recorder: deps.OperationalRecorder},
	})
}

// newSittingMail builds the Sitting mail composer, which needs the Sitting
// renderer capability of the configured template renderer.
func newSittingMail(deps Dependencies) (*mail.SittingComposer, error) {
	sittingRenderer, ok := deps.MailTemplateRenderer.(mail.SittingRenderer)
	if !ok {
		return nil, errors.New("Sitting mail template renderer is unavailable")
	}

	return newSittingMailPreparer(
		sittingRenderer,
		deps.MailDeliverySender,
		deps.MailSecretSealer,
		time.Now,
		newInstitutionName(deps),
	)
}

// newSittingMailPreparer builds the Sitting mail composer over an explicit
// clock, so its tests freeze time.
func newSittingMailPreparer(
	renderer mail.SittingRenderer,
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
		Persistence:       deps.Store.ExamRevision(),
		Access:            deps.Store.ExamAuthoring(),
		Memberships:       deps.Store.AcademicUnitMember(),
		Authorizer:        authorizer,
		Auditor:           shared.audit.MutationAudit(),
		Publisher:         shared.realtime,
		Now:               time.Now,
		NewID:             model.NewExamRevisionID,
		InstitutionOrigin: deps.PublicURL,
		Operational:       operationalRecorderAdapter{recorder: deps.OperationalRecorder},
	})
}

func newSittings(
	deps Dependencies,
	shared sharedMechanics,
	authorizer *access.Authorizer,
	scheduleMail sitting.ScheduleMail,
) (*sitting.Service, error) {
	return sitting.New(sitting.Dependencies{
		Persistence:   deps.Store.ExamSitting(),
		Access:        deps.Store.ExamAuthoring(),
		Memberships:   deps.Store.AcademicUnitMember(),
		Authorizer:    authorizer,
		Auditor:       shared.audit.MutationAudit(),
		SystemAuditor: shared.audit.SystemAudit(),
		Publisher:     shared.realtime,
		Jobs:          jobs.NewExamSittingLifecycleJobFactory(time.Now, model.NewJobID),
		Mail:          scheduleMail,
		Now:           time.Now,
		NewID:         model.NewExamSittingID,
		Operational:   operationalRecorderAdapter{recorder: deps.OperationalRecorder},
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
		Auditor:             shared.audit.MutationAudit(),
		SystemAuditor:       shared.audit.SystemAudit(),
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
		NewDiscrepancy:      model.NewIntegrityDiscrepancyID,
		NewWorkspaceEntry:   model.NewAttemptWorkspaceEntryID,
		NewWorkspaceObject:  model.NewAttemptWorkspaceObjectID,
		NewWorkspaceVersion: model.NewWorkspaceContentVersion,
		NewSubmission:       model.NewSubmissionID,
		NewReceiptCode:      model.NewSubmissionReceiptCode,
		Operational:         operationalRecorderAdapter{recorder: deps.OperationalRecorder},
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
		Auditor:       shared.audit.MutationAudit(),
		Publisher:     shared.realtime,
		Mail:          resultReleaseMail,
		Now:           time.Now,
		NewReviewID:   model.NewSubmissionReviewID,
		NewDecisionID: model.NewIntegrityReviewDecisionID,
	})
}

func newExamResources(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*resource.Service, error) {
	return resource.New(resource.Dependencies{
		Persistence:   deps.Store.ExamResource(),
		Access:        deps.Store.ExamAuthoring(),
		Memberships:   deps.Store.AcademicUnitMember(),
		Authorizer:    authorizer,
		Auditor:       shared.audit.MutationAudit(),
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
		Auditor:           shared.audit.MutationAudit(),
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
		Auditor:     shared.audit.MutationAudit(),
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
		Auditor:     shared.audit.MutationAudit(),
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
		Auditor:     shared.audit.MutationAudit(),
		Content:     deps.FileContent,
		Now:         time.Now,
		NewID:       model.NewExamExportID,
		NewJobID:    model.NewJobID,
	})
}
