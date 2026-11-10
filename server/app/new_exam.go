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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
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
	sittingMailPreparation sittingScheduleMailPreparationAdapter
}

// newExamServices builds the Exam area in dependency order: authoring and
// publication, Sittings, the Attempts and reviews that run inside them, the
// Draft content services, then records and exports. desktopBuilds resolves
// Desktop builds for Attempt admission.
func newExamServices(
	deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer, desktopBuilds *identity.Service,
) (examServices, error) {
	collections := examCollectionInvalidationEffects{sittings: deps.Store.ExamSitting(), realtime: shared.realtime}
	effects := examRealtimeEffects{realtime: shared.realtime, collections: collections}
	var built examServices
	var err error
	if built.authoring, err = newExamAuthoring(deps, shared, authorizer, effects); err != nil {
		return examServices{}, err
	}
	if built.sittingMail, err = newSittingMail(deps); err != nil {
		return examServices{}, err
	}
	if built.publication, err = newExamPublication(deps, shared, authorizer, effects); err != nil {
		return examServices{}, err
	}
	built.sittingMailPreparation = sittingScheduleMailPreparationAdapter{preparer: built.sittingMail,
		revisions: deps.Store.ExamRevision(), classes: deps.Store.Class()}
	if built.sittings, err = newSittings(deps, shared, authorizer, collections, built.sittingMailPreparation); err != nil {
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
	if built.corrections, err = newCorrections(deps, shared, authorizer, collections); err != nil {
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

func newExamAuthoring(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer, effects examRealtimeEffects) (*exam.Authoring, error) {
	return exam.NewAuthoring(exam.AuthoringDependencies{
		Persistence:       deps.Store.ExamAuthoring(),
		Memberships:       deps.Store.AcademicUnitMember(),
		Users:             deps.Store.User(),
		Mail:              shared.mail,
		Authorizer:        examAuthorizationAdapter{authorization: authorizer},
		Auditor:           shared.audit.MutationAudit(),
		Outcomes:          deps.Store.CommandOutcome(),
		Effects:           effects,
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
	return newSittingMailPreparer(sittingRenderer, deps.MailDeliverySender, deps.MailSecretSealer, time.Now,
		(&institutionNameCache{institutions: deps.Store.Institution(), now: time.Now}).Name)
}

func newExamPublication(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer, effects examRealtimeEffects) (*exam.Publication, error) {
	return exam.NewPublication(exam.PublicationDependencies{
		Persistence:       deps.Store.ExamRevision(),
		Access:            deps.Store.ExamAuthoring(),
		Memberships:       deps.Store.AcademicUnitMember(),
		Authorizer:        examAuthorizationAdapter{authorization: authorizer},
		Auditor:           shared.audit.MutationAudit(),
		Effects:           effects,
		Now:               time.Now,
		NewID:             model.NewExamRevisionID,
		InstitutionOrigin: deps.PublicURL,
		Operational:       operationalRecorderAdapter{recorder: deps.OperationalRecorder},
	})
}

func newSittings(
	deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer,
	collections examCollectionInvalidationEffects, scheduleMail sittingScheduleMailPreparationAdapter,
) (*sitting.Service, error) {
	effects := examSittingRealtimeEffects{realtime: shared.realtime, collections: collections}
	return sitting.New(sitting.Dependencies{
		Persistence:   deps.Store.ExamSitting(),
		Access:        deps.Store.ExamAuthoring(),
		Memberships:   deps.Store.AcademicUnitMember(),
		Authorizer:    authorizer,
		Auditor:       shared.audit.MutationAudit(),
		SystemAuditor: examSittingSystemAuditAdapter{audit: shared.audit},
		Effects:       effects,
		Jobs:          jobhandler.NewExamSittingLifecycleJobFactory(time.Now, model.NewJobID),
		Mail:          scheduleMail,
		Now:           time.Now,
		NewID:         model.NewExamSittingID,
		Operational:   operationalRecorderAdapter{recorder: deps.OperationalRecorder},
	})
}

func newAttempts(deps Dependencies, shared sharedMechanics, sittings *sitting.Service, desktopBuilds *identity.Service) (*attempt.Service, error) {
	effects := examAttemptRealtimeEffects{realtime: shared.realtime}
	submissionMail := examSubmissionMailPreparationAdapter{preparer: shared.mail, users: deps.Store.User(),
		sittings: deps.Store.ExamSitting(), revisions: deps.Store.ExamRevision()}
	return attempt.New(attempt.Dependencies{
		Persistence: deps.Store.ExamAttempt(), Workspace: deps.Store.ExamAttemptWorkspace(),
		Submissions: deps.Store.ExamSubmission(), Sittings: deps.Store.ExamSitting(),
		Managers:      examAttemptManagerAuthorizationAdapter{sittings: sittings, submissions: deps.Store.ExamSubmission(), audit: shared.audit},
		Auditor:       shared.audit.MutationAudit(),
		SystemAuditor: examAttemptSystemAuditAdapter{audit: shared.audit},
		Effects:       effects, Content: deps.FileContent, Mail: submissionMail,
		DesktopBuilds: desktopBuilds,
		Now:           time.Now, NewAttemptID: model.NewExamAttemptID, NewWorkspaceID: model.NewExamAttemptWorkspaceID,
		NewParticipation: model.NewAttemptParticipationID, NewConnection: model.NewAttemptConnectionID,
		NewEvidence: model.NewIntegrityEvidenceID, NewFlag: model.NewIntegrityFlagID,
		NewSuspension: model.NewAttemptSuspensionID, NewFocusLossSignal: model.NewFocusLossSignalID,
		NewDiscrepancy:    model.NewIntegrityDiscrepancyID,
		NewWorkspaceEntry: model.NewAttemptWorkspaceEntryID, NewWorkspaceObject: model.NewAttemptWorkspaceObjectID,
		NewWorkspaceVersion: model.NewWorkspaceContentVersion,
		NewSubmission:       model.NewSubmissionID,
		NewReceiptCode:      model.NewSubmissionReceiptCode,
		Operational:         operationalRecorderAdapter{recorder: deps.OperationalRecorder},
	})
}

func newReviews(deps Dependencies, shared sharedMechanics, sittings *sitting.Service) (*review.Service, error) {
	effects := examIntegrityReviewRealtimeEffects{realtime: shared.realtime}
	resultReleaseMail := examResultReleaseMailPreparationAdapter{preparer: shared.mail, users: deps.Store.User(),
		sittings: deps.Store.ExamSitting(), revisions: deps.Store.ExamRevision()}
	return review.New(review.Dependencies{Persistence: deps.Store.ExamIntegrityReview(),
		Authorizer: examIntegrityReviewAuthorizationAdapter{reviews: deps.Store.ExamIntegrityReview(), sittings: sittings,
			audit: shared.audit},
		Auditor: shared.audit.MutationAudit(),
		Effects: effects, Mail: resultReleaseMail, Now: time.Now,
		NewReviewID: model.NewSubmissionReviewID, NewDecisionID: model.NewIntegrityReviewDecisionID})
}

func newExamResources(deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer) (*resource.Service, error) {
	effects := examResourceRealtimeEffects{realtime: shared.realtime}
	return resource.New(resource.Dependencies{
		Persistence:   deps.Store.ExamResource(),
		Access:        deps.Store.ExamAuthoring(),
		Memberships:   deps.Store.AcademicUnitMember(),
		Authorizer:    authorizer,
		Auditor:       shared.audit.MutationAudit(),
		Effects:       effects,
		Content:       deps.FileContent,
		Now:           time.Now,
		NewResourceID: model.NewExamResourceID,
		NewEntryID:    model.NewFileEntryID,
		NewRevisionID: model.NewFileRevisionID,
		NewLeaseID:    model.NewUploadLeaseID,
	})
}

func newCorrections(
	deps Dependencies, shared sharedMechanics, authorizer *access.Authorizer, collections examCollectionInvalidationEffects,
) (*correction.Service, error) {
	effects := examCorrectionRealtimeEffects{realtime: shared.realtime, collections: collections}
	return correction.New(correction.Dependencies{
		Persistence:       deps.Store.ExamCorrection(),
		Revisions:         deps.Store.ExamRevision(),
		Access:            deps.Store.ExamAuthoring(),
		Memberships:       deps.Store.AcademicUnitMember(),
		Authorizer:        authorizer,
		Auditor:           shared.audit.MutationAudit(),
		Effects:           effects,
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
	effects := examStarterWorkspaceRealtimeEffects{realtime: shared.realtime}
	return workspace.New(workspace.Dependencies{
		Persistence: deps.Store.ExamStarterWorkspace(),
		Access:      deps.Store.ExamAuthoring(),
		Memberships: deps.Store.AcademicUnitMember(),
		Authorizer:  authorizer,
		Auditor:     shared.audit.MutationAudit(),
		Content:     deps.FileContent,
		Effects:     effects,
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

type examAttemptSystemAuditAdapter struct{ audit *access.Auditor }

func (a examAttemptSystemAuditAdapter) Begin(ctx context.Context, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID, operation string, value map[string]any) (model.AuditEventID, error) {
	auditValue := make(map[string]any, len(value))
	for key, item := range value {
		auditValue[key] = item
	}
	event, err := a.audit.BeginSystemCriticalActionAtScope(ctx, action, resource, scopeType, scopeID,
		map[string]any{"operation": operation, "value": auditValue})
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func (a examAttemptSystemAuditAdapter) Fail(ctx context.Context, id model.AuditEventID, code string) error {
	_, err := a.audit.CompleteCriticalAction(ctx, id, model.AuditStatusFail, code, nil)
	return err
}
