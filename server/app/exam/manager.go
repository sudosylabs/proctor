// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ListManagersQuery struct {
	ExamID          model.ExamID
	BeforeGrantedAt time.Time
	BeforeUserID    model.UserID
	Limit           int
}

// ManagerSummary is the bounded application projection for one management
// relationship. User profile data is deliberately outside this seam.
type ManagerSummary struct {
	Manager   model.ExamManager
	IsCreator bool
	IsOwner   bool
}

// ManagerPage is one page of an Exam's Exam Managers; HasMore reports that
// another page follows the last item.
type ManagerPage struct {
	Items   []ManagerSummary
	HasMore bool
}

// ManagerChangeCommand names the user whose relationship to one Exam changes:
// added as an Exam Manager, removed as one, or made its owner.
type ManagerChangeCommand struct {
	ExamID               model.ExamID
	UserID               model.UserID
	ExpectedExamRevision int64
	IdempotencyKey       string
}

// ListExamManagers returns one page of an Exam's Exam Managers to a caller
// authorized to manage the Exam.
func (a *Authoring) ListExamManagers(
	ctx context.Context,
	inv invocation.Invocation,
	query ListManagersQuery,
) (_ ManagerPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.ExamID.IsValid() ||
		query.Limit < 1 ||
		query.Limit > 200 ||
		(query.BeforeGrantedAt.IsZero() != query.BeforeUserID.IsZero()) {
		return ManagerPage{}, apperror.Invalid("exam.invalid", "manager_list")
	}

	if _, _, err := a.authorizeManagement(ctx, inv, query.ExamID, model.TimeUTC(a.now())); err != nil {
		return ManagerPage{}, err
	}

	items, err := a.exams.ListManagers(ctx, store.ExamManagerListOptions{
		ExamID:          query.ExamID,
		BeforeGrantedAt: query.BeforeGrantedAt,
		BeforeUserID:    query.BeforeUserID,
		Limit:           query.Limit + 1,
	})
	if err != nil {
		return ManagerPage{}, examStoreErrors.Translate(err)
	}

	page := ManagerPage{HasMore: len(items) > query.Limit}
	items = items[:min(len(items), query.Limit)]
	page.Items = make([]ManagerSummary, 0, len(items))
	for _, item := range items {
		page.Items = append(page.Items, ManagerSummary{
			Manager:   item.Manager,
			IsCreator: item.IsCreator,
			IsOwner:   item.IsOwner,
		})
	}

	return page, nil
}

// AddExamManager idempotently grants Exam Manager membership to an eligible
// user, fenced by ExpectedExamRevision, and notifies that user by mail.
func (a *Authoring) AddExamManager(
	ctx context.Context,
	inv invocation.Invocation,
	command ManagerChangeCommand,
) (_ store.ExamManagerCommandResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return a.changeManager(ctx, inv, command, managerAddition, a.exams.AddManager)
}

// RemoveExamManager idempotently revokes a user's Exam Manager membership,
// fenced by ExpectedExamRevision, and notifies that user by mail.
func (a *Authoring) RemoveExamManager(
	ctx context.Context,
	inv invocation.Invocation,
	command ManagerChangeCommand,
) (_ store.ExamManagerCommandResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return a.changeManager(ctx, inv, command, managerRemoval, a.exams.RemoveManager)
}

// TransferExamOwnership idempotently makes an eligible user the Exam's owner,
// fenced by ExpectedExamRevision.
func (a *Authoring) TransferExamOwnership(
	ctx context.Context,
	inv invocation.Invocation,
	command ManagerChangeCommand,
) (_ store.ExamManagerCommandResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return a.changeManager(ctx, inv, command, ownershipTransfer, a.exams.TransferOwner)
}

type managerMutation func(context.Context, *store.ExamManagerMutation, *store.CommandIdempotency) (*store.ExamManagerCommandResult, error)

// managerEffect announces one committed Exam Manager transition: the Exam,
// the User it concerns, the Exam revision and the instant of the change.
type managerEffect func(*Authoring, context.Context, model.ExamID, model.UserID, int64, time.Time) error

type managerTransition struct {
	operation            string
	idempotencyOperation string
	eligibilityRequired  bool
	templateKey          model.MailTemplateKey
	relationship         ManagerMailRelationship
	ownershipTransfer    bool
	publish              managerEffect
}

var (
	managerAddition = managerTransition{
		operation:            "add_manager",
		idempotencyOperation: idempotencyOperationAddManager,
		eligibilityRequired:  true,
		templateKey:          model.MailTemplateExamManagerAdded,
		relationship:         ManagerMailRelationshipManager,
		publish:              (*Authoring).publishManagerAdded,
	}
	managerRemoval = managerTransition{
		operation:            "remove_manager",
		idempotencyOperation: idempotencyOperationRemoveManager,
		templateKey:          model.MailTemplateExamManagerRemoved,
		relationship:         ManagerMailRelationshipNoLongerManager,
		publish:              (*Authoring).publishManagerRemoved,
	}
	ownershipTransfer = managerTransition{
		operation:            "transfer_owner",
		idempotencyOperation: idempotencyOperationTransferOwner,
		eligibilityRequired:  true,
		ownershipTransfer:    true,
		publish:              (*Authoring).publishOwnerTransferred,
	}
)

func (a *Authoring) changeManager(
	ctx context.Context,
	inv invocation.Invocation,
	command ManagerChangeCommand,
	transition managerTransition,
	mutate managerMutation,
) (store.ExamManagerCommandResult, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || !command.UserID.IsValid() || command.ExpectedExamRevision < 1 {
		return store.ExamManagerCommandResult{}, apperror.Invalid("exam.invalid", "manager_command")
	}

	commandIdempotency, err := idempotency.Require(inv, transition.idempotencyOperation, command.IdempotencyKey, struct {
		ExamID               string `json:"exam_id"`
		UserID               string `json:"user_id"`
		ExpectedExamRevision int64  `json:"expected_exam_revision"`
	}{command.ExamID.String(), command.UserID.String(), command.ExpectedExamRevision})
	if err != nil {
		return store.ExamManagerCommandResult{}, err
	}

	decisionAt := model.TimeUTC(a.now())
	examAccess, action, err := a.authorizeManagement(ctx, inv, command.ExamID, decisionAt)
	if err != nil {
		return store.ExamManagerCommandResult{}, err
	}

	eligible := false
	if transition.eligibilityRequired {
		eligible, err = a.isEligibleManager(ctx, command.UserID, examAccess.Exam.AcademicUnitID, decisionAt)
		if err != nil {
			return store.ExamManagerCommandResult{}, err
		}
	}

	snapshot, err := a.exams.Get(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return store.ExamManagerCommandResult{}, examStoreErrors.Translate(err)
	}
	if snapshot == nil || snapshot.Draft == nil {
		return store.ExamManagerCommandResult{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned no mail identity"))
	}

	resource := model.Resource{Type: model.ResourceExam, ID: command.ExamID.String()}
	auditData := map[string]any{
		"exam_id":                command.ExamID.String(),
		"user_id":                command.UserID.String(),
		"expected_exam_revision": command.ExpectedExamRevision,
		"exam_revision":          command.ExpectedExamRevision + 1,
	}
	if transition.eligibilityRequired {
		auditData["target_eligible"] = eligible
	}

	auditID, err := a.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    examAccess.Exam.AcademicUnitID.String(),
		Operation:  transition.operation,
		Value:      auditData,
	})
	if err != nil {
		return store.ExamManagerCommandResult{}, err
	}

	// Mail and mutation records retain their existing millisecond contract;
	// current membership decisions above use the full domain precision.
	at := model.TimeUTC(decisionAt)
	notices, err := a.prepareManagerNotices(ctx, examAccess.Exam.OwnerUserID, command.UserID, snapshot.Draft.Title, at, transition)
	if err != nil {
		mapped := apperror.New("exam.unavailable").Wrap(err)
		if auditErr := a.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return store.ExamManagerCommandResult{}, errors.Join(mapped, auditErr)
		}

		return store.ExamManagerCommandResult{}, mapped
	}

	result, err := mutate(ctx, &store.ExamManagerMutation{
		ExamID:           command.ExamID,
		ActorUserID:      principal.UserID,
		TargetUserID:     command.UserID,
		ManagerOverride:  action == model.ActionExamManageOverride,
		ExpectedRevision: command.ExpectedExamRevision,
		ChangedAt:        at,
		AuditEventID:     auditID,
		AuditAt:          at,
		Notices:          notices,
	}, commandIdempotency)
	if err != nil {
		mapped := examStoreErrors.Translate(err)
		if auditErr := a.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return store.ExamManagerCommandResult{}, errors.Join(mapped, auditErr)
		}

		return store.ExamManagerCommandResult{}, mapped
	}

	if result == nil || result.Exam == nil || result.Manager == nil {
		return store.ExamManagerCommandResult{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned no manager result"))
	}

	if !result.Replayed {
		effectErr := transition.publish(a, ctx, result.Exam.ID, command.UserID, result.Exam.Revision, at)
		if effectErr != nil {
			a.publisher.Report(ctx, transition.operation, effectErr)
		}
	}

	return *result, nil
}

func (a *Authoring) prepareManagerNotices(
	ctx context.Context,
	ownerID, targetID model.UserID,
	title string,
	at time.Time,
	transition managerTransition,
) ([]store.ExamManagerMail, error) {
	type recipientNotice struct {
		userID       model.UserID
		key          model.MailTemplateKey
		relationship ManagerMailRelationship
	}
	requests := []recipientNotice{{userID: targetID, key: transition.templateKey, relationship: transition.relationship}}
	if transition.ownershipTransfer {
		requests = []recipientNotice{
			{userID: ownerID, key: model.MailTemplateExamOwnershipTransferredFromYou, relationship: ManagerMailRelationshipManager},
			{userID: targetID, key: model.MailTemplateExamOwnershipTransferredToYou, relationship: ManagerMailRelationshipOwner},
		}
	}

	notices := make([]store.ExamManagerMail, 0, len(requests))
	for _, request := range requests {
		recipient, err := a.users.Get(ctx, request.userID)
		if err != nil || recipient == nil {
			return nil, errors.Join(errors.New("resolve Exam Manager mail recipient"), err)
		}

		prepared, err := a.mail.PrepareManagerMail(ManagerMailPreparation{
			Recipient:    recipient,
			OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey:  request.key,
			ExamTitle:    title,
			Relationship: request.relationship,
			ActionAt:     at,
		})
		if err != nil || prepared == nil {
			return nil, errors.Join(errors.New("prepare Exam Manager mail"), err)
		}

		notices = append(notices, *prepared)
	}

	return notices, nil
}

func (a *Authoring) authorizeManagement(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	at time.Time,
) (*store.ExamAccessSnapshot, model.Action, error) {
	principal := inv.Principal()
	examAccess, err := a.exams.Access(ctx, examID, principal.UserID)
	if err != nil {
		return nil, "", examStoreErrors.Translate(err)
	}

	action, err := a.actionForAccess(ctx, principal.UserID, examAccess, at, model.ActionExamManage, model.ActionExamManageOverride)
	if err != nil {
		return nil, "", err
	}
	if err := a.authorizer.Authorize(ctx, inv, action, model.Resource{Type: model.ResourceExam, ID: examID.String()}); err != nil {
		return nil, "", err
	}

	return examAccess, action, nil
}

func (a *Authoring) isEligibleManager(ctx context.Context, userID model.UserID, unitID model.AcademicUnitID, at time.Time) (bool, error) {
	user, err := a.users.Get(ctx, userID)
	if err != nil {
		if store.IsNotFound(err) {
			return false, nil
		}
		return false, apperror.New("exam.unavailable").Wrap(err)
	}
	if user == nil || !user.IsActive() {
		return false, nil
	}
	eligible, err := manageraccess.HasCurrentMembership(ctx, a.academicUnitMembers, userID, unitID, at)
	if err != nil {
		return false, apperror.New("exam.unavailable").Wrap(err)
	}
	return eligible, nil
}
