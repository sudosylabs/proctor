// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// EnrollClassMemberCommand is the input of EnrollClassMember.
type EnrollClassMemberCommand struct {
	ClassID            model.ClassID
	UserID             model.UserID
	StartAt            int64
	EndAt              int64
	ExpectedPreviousID model.ClassMemberID
	RequireTransfer    bool
	IdempotencyKey     string
	Batch              batchitem.Item
}

// EndClassMemberCommand is the input of EndClassMember.
type EndClassMemberCommand struct {
	ID             model.ClassMemberID
	IdempotencyKey string
	BatchScopeID   string
	Batch          batchitem.Item
}

func (s *Service) publishMembershipChanged(
	ctx context.Context,
	candidateID model.UserID,
	classIDs ...model.ClassID,
) {
	if s.effects == nil {
		return
	}
	if err := s.effects.MembershipChanged(ctx, candidateID, classIDs); err != nil {
		s.effects.Report(ctx, "class_member.membership_changed", err)
	}
}

// EnrollClassMember enrolls a User in a Class after authorizing the caller.
func (s *Service) EnrollClassMember(ctx context.Context, invocation invocation.Invocation, command EnrollClassMemberCommand) (*model.ClassEnrollment, error) {
	classID := command.ClassID
	resource, err := s.authorizeClass(ctx, invocation, classID, model.ActionClassMembersManage)
	if err != nil {
		return nil, err
	}
	userID, err := model.ParseUserID(strings.TrimSpace(command.UserID.String()))
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "user_id").Wrap(err)
	}
	memberID, err := model.ParseClassMemberID(s.newID())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "class_member_id").Wrap(err)
	}
	at := model.TimeUTC(s.now())
	if command.Batch.Fields().RetainedOutcome {
		return s.enrollRetainedClassMember(ctx, invocation, command, resource, userID, memberID, at)
	}
	plan, err := s.planClassEnrollment(ctx, invocation, command, userID, memberID, at)
	if err != nil {
		return nil, err
	}
	candidate, previous := plan.candidate, plan.previous
	idempotency, err := prepareClassEnrollmentIdempotency(invocation, command, candidate)
	if err != nil {
		return nil, err
	}
	if previous != nil && command.Batch.Fields().Authorization != nil {
		command.Batch.Fields().Authorization.ClassMemberID = previous.ID
	}
	batchitem.BindOnboardingImport(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(idempotency, command.Batch.Fields().Metadata)
	recipient := &model.User{ID: userID, Revision: 1}
	if !command.Batch.Fields().RetainedOutcome {
		recipient, err = s.users.Get(ctx, userID)
		if err != nil {
			return nil, ClassMemberStoreErrors.Translate(err)
		}
	}
	prepared, err := s.prepareEnrollmentNotice(command, plan, recipient, at)
	if err != nil {
		return nil, err
	}
	return s.runClassEnrollment(ctx, invocation, command, resource, plan, recipient, prepared, idempotency, at)
}

// classEnrollmentPlan is the validated candidate membership and any membership it transfers from.
type classEnrollmentPlan struct {
	class         *model.Class
	candidate     *model.ClassMember
	previous      *model.ClassMember
	previousClass *model.Class
}

// prepareClassEnrollmentIdempotency prepares the enrollment command idempotency from its semantic fields.
func prepareClassEnrollmentIdempotency(
	invocation invocation.Invocation, command EnrollClassMemberCommand, candidate *model.ClassMember,
) (*store.CommandIdempotency, error) {
	return idempotency.Prepare(invocation, "class_member.enroll", command.IdempotencyKey, struct {
		ClassID            string `json:"class_id"`
		UserID             string `json:"user_id"`
		StartAt            int64  `json:"start_at"`
		EndAt              int64  `json:"end_at"`
		ExpectedPreviousID string `json:"expected_previous_id,omitempty"`
		Transfer           bool   `json:"transfer"`
		Progression        bool   `json:"progression"`
	}{candidate.ClassID.String(), candidate.UserID.String(), command.StartAt, command.EndAt, strings.TrimSpace(command.ExpectedPreviousID.String()), command.RequireTransfer, command.Batch.Fields().StudentProgression})
}

// enrollClassMember runs one Store enrollment, mirrors replay to the batch, and reports whether it freshly mutated.
func (s *Service) enrollClassMember(
	ctx context.Context, command EnrollClassMemberCommand, input *store.ClassMemberEnrollment,
) (*store.ClassEnrollmentResult, bool, error) {
	value, storeErr := s.classMembers.Enroll(ctx, input)
	freshMutation := storeErr == nil && !input.Replayed && !input.NoOp
	if command.Batch.Fields().Replayed != nil {
		*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
	}
	return value, freshMutation, storeErr
}

// enrollRetainedClassMember replays a batch enrollment whose outcome the batch already retained.
func (s *Service) enrollRetainedClassMember(
	ctx context.Context, invocation invocation.Invocation, command EnrollClassMemberCommand,
	resource model.Resource, userID model.UserID, memberID model.ClassMemberID, at time.Time,
) (*model.ClassEnrollment, error) {
	var retainedPrevious *model.ClassMember
	if command.Batch.Fields().RetainedOutcome && command.RequireTransfer {
		previousID, parseErr := model.ParseClassMemberID(strings.TrimSpace(command.ExpectedPreviousID.String()))
		if parseErr != nil {
			return nil, apperror.New("request.invalid").WithField("field", "expected_previous_id").Wrap(parseErr)
		}
		var err error
		retainedPrevious, err = s.classMembers.Get(ctx, previousID)
		if err != nil {
			return nil, ClassMemberStoreErrors.Translate(err)
		}
		if retainedPrevious.UserID != userID {
			return nil, apperror.New("resource.not_found").WithField("resource", "class_member")
		}
		if _, err = s.authorizeClass(ctx, invocation, retainedPrevious.ClassID, model.ActionClassMembersManage); err != nil {
			return nil, concealMembershipAuthorizationError(err, "class_member")
		}
		if command.Batch.Fields().Authorization != nil {
			command.Batch.Fields().Authorization.ClassMemberID = retainedPrevious.ID
		}
	}
	candidate := &model.ClassMember{ClassID: command.ClassID, UserID: userID, StartsAt: model.TimeFromMillis(command.StartAt), EndsAt: model.OptionalTimeFromMillis(command.EndAt)}
	candidate.PrepareCreate(memberID, at)
	idempotency, commandErr := prepareClassEnrollmentIdempotency(invocation, command, candidate)
	if commandErr != nil {
		return nil, commandErr
	}
	batchitem.BindOnboardingImport(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(idempotency, command.Batch.Fields().Metadata)
	freshMutation := false
	result, replayErr := access.RunAudited(ctx, s.auditor, access.Mutation{Invocation: invocation, Action: model.ActionClassMembersManage,
		Resource: resource, Operation: "enroll", Value: candidate.Auditable(), Subject: candidate.UserID}, func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*store.ClassEnrollmentResult, error) {
			execute := func(ctx context.Context, sourceAuditID model.AuditEventID) (*store.ClassEnrollmentResult, error) {
				input := &store.ClassMemberEnrollment{Member: candidate, ExpectedRecipientRevision: 1, StudentProgression: command.Batch.Fields().StudentProgression,
					ProgressionSourceAuditEventID: command.Batch.Fields().ProgressionSourceAuditID, ProgressionDestinationAuditEventID: command.Batch.Fields().ProgressionDestinationAuditID,
					AuditEventID: reference.ID, PreviousAuditEventID: sourceAuditID, AuditAt: reference.MutationAt, Command: idempotency}
				if retainedPrevious != nil {
					input.ExpectedPreviousID = retainedPrevious.ID
				}
				value, fresh, storeErr := s.enrollClassMember(ctx, command, input)
				freshMutation = fresh
				return value, storeErr
			}
			if retainedPrevious == nil {
				return execute(ctx, "")
			}
			sourceAttempt := access.Mutation{Invocation: invocation, Action: model.ActionClassMembersManage,
				Resource:  model.Resource{Type: model.ResourceClass, ID: retainedPrevious.ClassID.String()},
				Operation: "transfer_out", Prior: retainedPrevious.Auditable(), Subject: retainedPrevious.UserID}
			return access.RunAudited(ctx, s.auditor, sourceAttempt, func() time.Time { return at },
				func(ctx context.Context, sourceReference access.MutationReference) (*store.ClassEnrollmentResult, error) {
					return execute(ctx, sourceReference.ID)
				}, ClassMemberStoreErrors.Translate)
		}, ClassMemberStoreErrors.Translate)
	if replayErr != nil {
		return nil, replayErr
	}
	if freshMutation {
		s.publishEnrollmentChanged(ctx, result)
	}
	return &model.ClassEnrollment{Membership: result.Membership, Previous: result.Previous}, nil
}

// planClassEnrollment validates the candidate membership and authorizes any active membership it transfers from.
func (s *Service) planClassEnrollment(
	ctx context.Context, invocation invocation.Invocation, command EnrollClassMemberCommand,
	userID model.UserID, memberID model.ClassMemberID, at time.Time,
) (classEnrollmentPlan, error) {
	class, err := s.classes.Get(ctx, command.ClassID)
	if err != nil {
		return classEnrollmentPlan{}, ClassMemberStoreErrors.Translate(err)
	}
	candidate := &model.ClassMember{
		ClassID:          command.ClassID,
		AcademicPeriodID: class.AcademicPeriodID,
		UserID:           userID,
		StartsAt:         model.TimeFromMillis(command.StartAt),
		EndsAt:           model.OptionalTimeFromMillis(command.EndAt),
	}
	candidate.PrepareCreate(memberID, at)
	if err := candidate.Validate(); err != nil {
		return classEnrollmentPlan{}, apperror.DomainInvalid("class_member.invalid", err)
	}
	previous, err := s.findTransferSource(ctx, command, candidate)
	if err != nil {
		return classEnrollmentPlan{}, err
	}
	var previousClass *model.Class
	if previous != nil {
		if previous.ClassID == candidate.ClassID {
			return classEnrollmentPlan{}, apperror.New("class.enrollment_conflict").WithField("resource", "class_member")
		}
		if _, err = s.authorizeClass(ctx, invocation, previous.ClassID, model.ActionClassMembersManage); err != nil {
			return classEnrollmentPlan{}, concealMembershipAuthorizationError(err, "class_member")
		}
		previousClass, err = s.classes.Get(ctx, previous.ClassID)
		if err != nil {
			return classEnrollmentPlan{}, ClassMemberStoreErrors.Translate(err)
		}
	}
	return classEnrollmentPlan{class: class, candidate: candidate, previous: previous, previousClass: previousClass}, nil
}

// findTransferSource finds the User's active membership in the candidate's Academic Period, if the enrollment transfers.
func (s *Service) findTransferSource(
	ctx context.Context, command EnrollClassMemberCommand, candidate *model.ClassMember,
) (*model.ClassMember, error) {
	var previous *model.ClassMember
	possibleReplay := command.Batch.Fields().RetainedOutcome
	if !command.Batch.Fields().RetainedOutcome {
		active, listErr := s.classMembers.ListActiveByUser(ctx, candidate.UserID, candidate.StartsAt)
		if listErr != nil {
			return nil, ClassMemberStoreErrors.Translate(listErr)
		}
		for _, membership := range active {
			if membership != nil && membership.AcademicPeriodID == candidate.AcademicPeriodID {
				if previous != nil {
					return nil, apperror.New("class.enrollment_conflict").WithField("resource", "class_member")
				}
				previous = membership
			}
		}
		if command.IdempotencyKey != "" {
			expectedPreviousID := strings.TrimSpace(command.ExpectedPreviousID.String())
			switch {
			case command.RequireTransfer && (previous == nil || (previous.ID.String() != expectedPreviousID && previous.ClassID != candidate.ClassID)):
				return nil, apperror.New("class.enrollment_conflict").WithField("resource", "class_member")
			case !command.RequireTransfer && previous != nil && previous.ClassID != candidate.ClassID:
				return nil, apperror.New("class.enrollment_conflict").WithField("resource", "class_member")
			}
		}
		possibleReplay = command.IdempotencyKey != "" && previous != nil && previous.ClassID == candidate.ClassID
	}
	if possibleReplay {
		previous = nil
	}
	return previous, nil
}

// prepareEnrollmentNotice prepares the enrollment or transfer mail for the recipient.
func (s *Service) prepareEnrollmentNotice(
	command EnrollClassMemberCommand, plan classEnrollmentPlan, recipient *model.User, at time.Time,
) (*store.PreparedMail, error) {
	candidate := plan.candidate
	details := mail.ClassTransitionDetails{ClassDisplayName: plan.class.DisplayName, StartsAt: candidate.StartsAt}
	if candidate.EndsAt.Valid {
		details.EndsAt = candidate.EndsAt.Time
	}
	key := model.MailTemplateAcademicClassEnrolled
	if plan.previous != nil {
		key = model.MailTemplateAcademicClassTransferred
		details.PreviousClassDisplayName = plan.previousClass.DisplayName
	}
	var prepared *store.PreparedMail
	if !command.Batch.Fields().RetainedOutcome {
		var err error
		prepared, err = s.mail.PrepareClassTransition(mail.ClassTransitionPreparation{Recipient: recipient,
			OccurrenceID: model.NewMailOccurrenceID(), TemplateKey: key,
			Details: details, ActionAt: at})
		if err != nil {
			return nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}
	return prepared, nil
}

// runClassEnrollment audits and stores the enrollment, nesting the source audit when it transfers a membership.
func (s *Service) runClassEnrollment(
	ctx context.Context, invocation invocation.Invocation, command EnrollClassMemberCommand, resource model.Resource,
	plan classEnrollmentPlan, recipient *model.User, prepared *store.PreparedMail,
	idempotency *store.CommandIdempotency, at time.Time,
) (*model.ClassEnrollment, error) {
	candidate, previous := plan.candidate, plan.previous
	destinationAttempt := access.Mutation{Invocation: invocation, Action: model.ActionClassMembersManage,
		Resource: resource, Operation: "enroll", Value: candidate.Auditable(), Subject: candidate.UserID}
	if previous != nil {
		destinationAttempt.Operation = "transfer_in"
	}
	freshMutation := false
	result, err := access.RunAudited(ctx, s.auditor, destinationAttempt, func() time.Time { return at },
		func(ctx context.Context, destinationReference access.MutationReference) (*store.ClassEnrollmentResult, error) {
			var expectedPreviousID model.ClassMemberID
			if previous == nil {
				var notice *store.PreparedMail
				if prepared != nil {
					notice = &store.PreparedMail{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job}
				}
				input := &store.ClassMemberEnrollment{
					Member: candidate, ExpectedRecipientRevision: recipient.Revision, StudentProgression: command.Batch.Fields().StudentProgression,
					ProgressionSourceAuditEventID: command.Batch.Fields().ProgressionSourceAuditID, ProgressionDestinationAuditEventID: command.Batch.Fields().ProgressionDestinationAuditID,
					Notice:       notice,
					AuditEventID: destinationReference.ID, AuditAt: destinationReference.MutationAt, Command: idempotency,
				}
				value, fresh, storeErr := s.enrollClassMember(ctx, command, input)
				freshMutation = fresh
				return value, storeErr
			}
			expectedPreviousID = previous.ID
			sourceAttempt := access.Mutation{Invocation: invocation, Action: model.ActionClassMembersManage,
				Resource:  model.Resource{Type: model.ResourceClass, ID: previous.ClassID.String()},
				Operation: "transfer_out", Prior: previous.Auditable(), Subject: previous.UserID}
			return access.RunAudited(ctx, s.auditor, sourceAttempt,
				func() time.Time { return model.TimeUTC(destinationReference.MutationAt) },
				func(ctx context.Context, sourceReference access.MutationReference) (*store.ClassEnrollmentResult, error) {
					var notice *store.PreparedMail
					if prepared != nil {
						notice = &store.PreparedMail{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job}
					}
					input := &store.ClassMemberEnrollment{
						Member: candidate, ExpectedPreviousID: expectedPreviousID, ExpectedRecipientRevision: recipient.Revision, StudentProgression: command.Batch.Fields().StudentProgression,
						ProgressionSourceAuditEventID: command.Batch.Fields().ProgressionSourceAuditID, ProgressionDestinationAuditEventID: command.Batch.Fields().ProgressionDestinationAuditID,
						Notice:       notice,
						AuditEventID: destinationReference.ID, PreviousAuditEventID: sourceReference.ID,
						AuditAt: destinationReference.MutationAt, Command: idempotency,
					}
					value, fresh, storeErr := s.enrollClassMember(ctx, command, input)
					freshMutation = fresh
					return value, storeErr
				}, ClassMemberStoreErrors.Translate)
		},
		ClassMemberStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	if freshMutation {
		s.publishEnrollmentChanged(ctx, result)
	}
	return &model.ClassEnrollment{Membership: result.Membership, Previous: result.Previous}, nil
}

// EndClassMember ends a Class Membership after authorizing the caller.
func (s *Service) EndClassMember(ctx context.Context, invocation invocation.Invocation, command EndClassMemberCommand) (*model.ClassMember, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "class_member_id")
	}
	if err := s.authorizer.AuthorizePreflight(
		ctx, invocation, model.ActionClassMembersManage, model.ResourceClass,
	); err != nil {
		return nil, err
	}
	if command.Batch.Fields().RetainedOutcome {
		return s.endRetainedClassMember(ctx, invocation, command)
	}
	current, err := s.classMembers.Get(ctx, id)
	if err != nil {
		return nil, ClassMemberStoreErrors.Translate(err)
	}
	resource, err := s.authorizeClass(ctx, invocation, current.ClassID, model.ActionClassMembersManage)
	if err != nil {
		return nil, concealMembershipAuthorizationError(err, "class_member")
	}
	if command.BatchScopeID != "" && strings.TrimSpace(command.BatchScopeID) != current.ClassID.String() {
		return nil, apperror.New("resource.not_found").WithField("resource", "class_member")
	}
	class, err := s.classes.Get(ctx, current.ClassID)
	if err != nil {
		return nil, ClassMemberStoreErrors.Translate(err)
	}
	idempotency, err := prepareClassMemberEndIdempotency(invocation, command)
	if err != nil {
		return nil, err
	}
	recipient, err := s.users.Get(ctx, current.UserID)
	if err != nil {
		return nil, ClassMemberStoreErrors.Translate(err)
	}
	at := model.TimeUTC(s.now())
	prepared := &store.PreparedMail{}
	if !command.Batch.Fields().RetainedOutcome {
		prepared, err = s.mail.PrepareClassTransition(mail.ClassTransitionPreparation{Recipient: recipient,
			OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey:  model.MailTemplateAcademicClassEnrollmentEnded,
			Details:      mail.ClassTransitionDetails{ClassDisplayName: class.DisplayName, StartsAt: current.StartsAt, EndsAt: at}, ActionAt: at})
		if err != nil {
			return nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}
	freshMutation := false
	ended, mutationErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionClassMembersManage,
			Resource:   resource,
			Operation:  "end",
			Prior:      current.Auditable(),
			Subject:    current.UserID,
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*model.ClassMember, error) {
			input := &store.ClassMemberEnd{
				ID: id, ExpectedRevision: current.Revision, ExpectedRecipientRevision: recipient.Revision,
				EndAt:        reference.MutationAt,
				Notice:       &store.PreparedMail{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job},
				AuditEventID: reference.ID, AuditAt: reference.MutationAt, Command: idempotency,
			}
			value, fresh, storeErr := s.endClassMember(ctx, command, input)
			freshMutation = fresh
			return value, storeErr
		},
		ClassMemberStoreErrors.Translate,
	)
	if mutationErr != nil {
		return nil, mutationErr
	}
	if freshMutation {
		s.publishMembershipChanged(ctx, ended.UserID, ended.ClassID)
	}
	return ended, nil
}

// prepareClassMemberEndIdempotency prepares the end command idempotency and binds its batch provenance.
func prepareClassMemberEndIdempotency(invocation invocation.Invocation, command EndClassMemberCommand) (*store.CommandIdempotency, error) {
	idempotency, err := idempotency.Prepare(invocation, "class_member.end", command.IdempotencyKey, struct {
		ID      string `json:"id"`
		ScopeID string `json:"scope_id,omitempty"`
	}{command.ID.String(), strings.TrimSpace(command.BatchScopeID)})
	if err != nil {
		return nil, err
	}
	batchitem.BindOnboardingImport(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(idempotency, command.Batch.Fields().Metadata)
	return idempotency, nil
}

// endClassMember runs one Store end, mirrors replay to the batch, and reports whether it freshly mutated.
func (s *Service) endClassMember(
	ctx context.Context, command EndClassMemberCommand, input *store.ClassMemberEnd,
) (*model.ClassMember, bool, error) {
	value, storeErr := s.classMembers.End(ctx, input)
	freshMutation := storeErr == nil && !input.Replayed && !input.NoOp
	if command.Batch.Fields().Replayed != nil {
		*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
	}
	return value, freshMutation, storeErr
}

// endRetainedClassMember replays a batch end whose outcome the batch already retained.
func (s *Service) endRetainedClassMember(
	ctx context.Context, invocation invocation.Invocation, command EndClassMemberCommand,
) (*model.ClassMember, error) {
	idempotency, err := prepareClassMemberEndIdempotency(invocation, command)
	if err != nil {
		return nil, err
	}
	at := model.TimeUTC(s.now())
	freshMutation := false
	ended, mutationErr := access.RunAudited(ctx, s.auditor, access.Mutation{Invocation: invocation, Action: model.ActionClassMembersManage,
		Resource: model.Resource{Type: model.ResourceClass, ID: strings.TrimSpace(command.BatchScopeID)}, Operation: "end"},
		func() time.Time { return at }, func(ctx context.Context, reference access.MutationReference) (*model.ClassMember, error) {
			input := &store.ClassMemberEnd{ID: command.ID, ExpectedRevision: 1, ExpectedRecipientRevision: 1,
				EndAt: reference.MutationAt, AuditEventID: reference.ID, AuditAt: reference.MutationAt, Command: idempotency}
			value, fresh, storeErr := s.endClassMember(ctx, command, input)
			freshMutation = fresh
			return value, storeErr
		}, ClassMemberStoreErrors.Translate)
	if mutationErr != nil {
		return nil, mutationErr
	}
	if freshMutation {
		s.publishMembershipChanged(ctx, ended.UserID, ended.ClassID)
	}
	return ended, nil
}

func (s *Service) publishEnrollmentChanged(ctx context.Context, result *store.ClassEnrollmentResult) {
	if result == nil || result.Membership == nil {
		return
	}
	classIDs := []model.ClassID{result.Membership.ClassID}
	if result.Previous != nil {
		classIDs = append(classIDs, result.Previous.ClassID)
	}
	s.publishMembershipChanged(ctx, result.Membership.UserID, classIDs...)
}

func (s *Service) authorizeClass(ctx context.Context, invocation invocation.Invocation, classID model.ClassID, action model.Action) (model.Resource, error) {
	if !classID.IsValid() {
		return model.Resource{}, apperror.New("request.invalid").WithField("field", "class_id")
	}
	resource := model.Resource{Type: model.ResourceClass, ID: classID.String()}
	if err := s.authorizer.Authorize(ctx, invocation, action, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
}

// ClassMemberStoreErrors translates Class Membership Store failures;
// onboarding imports present them the same way.
var ClassMemberStoreErrors = apperror.AdministrationStoreTable("class_member", "class.enrollment_conflict", "class_member.invalid", map[string]apperror.Code{
	"class_member_student_affiliation_required": "class_member.student_affiliation_required",
	"class_member_end_time":                     "resource.not_found",
})

// ListClassMembersPageQuery opts into bounded membership enumeration. Each page is
// authorized independently and explicit selectors preserve its effective filter.
type ListClassMembersPageQuery struct {
	AfterUserID model.UserID
	AfterID     model.ClassMemberID
	ClassID     model.ClassID
	MembershipPageQuery
}

// ClassMemberPage is one page of a Class's memberships with the members' identities.
type ClassMemberPage struct {
	Members  []*model.ClassMember
	HasMore  bool
	ActiveAt int64
}

// ListClassMembersPage lists a page of Class Memberships after authorizing the caller.
func (s *Service) ListClassMembersPage(ctx context.Context, invocation invocation.Invocation, query ListClassMembersPageQuery) (*ClassMemberPage, error) {
	resource, err := s.authorizeClass(ctx, invocation, query.ClassID, model.ActionClassMembersView)
	if err != nil {
		return nil, err
	}
	activeAt, limit, err := resolveMembershipPage(query.MembershipPageQuery, s.now())
	if err != nil {
		return nil, err
	}
	scopeID, err := model.ParseClassID(resource.ID)
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "class_id")
	}
	if query.AfterID.IsZero() != query.AfterUserID.IsZero() || (!query.AfterID.IsZero() && (!query.AfterID.IsValid() || !query.AfterUserID.IsValid())) {
		return nil, apperror.New("request.invalid").WithField("field", "cursor")
	}
	options := store.ClassMemberPageOptions{ClassID: scopeID, ActiveAt: model.TimeFromMillis(activeAt), Limit: limit, AfterID: query.AfterID, AfterUserID: query.AfterUserID}

	page, err := s.classMembers.ListPageByClass(ctx, options)
	if err != nil {
		return nil, ClassMemberStoreErrors.Translate(err)
	}
	result := &ClassMemberPage{Members: page.Members, ActiveAt: activeAt, HasMore: page.HasMore}
	if result.Members == nil {
		result.Members = []*model.ClassMember{}
	}
	return result, nil
}
