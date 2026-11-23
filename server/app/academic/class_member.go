// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/paging"
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

// publishMembershipChanged reports a failed publication without failing the
// use case.
func (s *Service) publishMembershipChanged(
	ctx context.Context,
	candidateID model.UserID,
	classIDs ...model.ClassID,
) {
	if s.publisher == nil {
		return
	}
	if err := PublishClassMembershipChanged(ctx, s.publisher, s.sittings, s.classes, candidateID, classIDs); err != nil {
		s.publisher.Report(ctx, "class_member.membership_changed", err)
	}
}

// EnrollClassMember enrolls a user in a class, transferring them from another
// class in the same period when needed; it needs class.members.manage.
func (s *Service) EnrollClassMember(
	ctx context.Context,
	inv invocation.Invocation,
	command EnrollClassMemberCommand,
) (*model.ClassEnrollment, error) {
	classID := command.ClassID
	resource, err := s.authorizeClass(ctx, inv, classID, model.ActionClassMembersManage)
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
		return s.enrollRetainedClassMember(ctx, inv, command, resource, userID, memberID, at)
	}

	plan, err := s.planClassEnrollment(ctx, inv, command, userID, memberID, at)
	if err != nil {
		return nil, err
	}

	candidate, previous := plan.candidate, plan.previous
	commandIdempotency, err := prepareClassEnrollmentIdempotency(inv, command, candidate)
	if err != nil {
		return nil, err
	}
	if previous != nil && command.Batch.Fields().Authorization != nil {
		command.Batch.Fields().Authorization.ClassMemberID = previous.ID
	}

	batchitem.BindOnboardingImport(commandIdempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(commandIdempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(commandIdempotency, command.Batch.Fields().Metadata)
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

	return s.runClassEnrollment(ctx, inv, command, resource, plan, recipient, prepared, commandIdempotency, at)
}

type classEnrollmentPlan struct {
	class         *model.Class
	candidate     *model.ClassMember
	previous      *model.ClassMember
	previousClass *model.Class
}

func prepareClassEnrollmentIdempotency(
	inv invocation.Invocation,
	command EnrollClassMemberCommand,
	candidate *model.ClassMember,
) (*store.CommandIdempotency, error) {
	return idempotency.Prepare(
		inv,
		"class_member.enroll",
		command.IdempotencyKey,
		struct {
			ClassID            string `json:"class_id"`
			UserID             string `json:"user_id"`
			StartAt            int64  `json:"start_at"`
			EndAt              int64  `json:"end_at"`
			ExpectedPreviousID string `json:"expected_previous_id,omitempty"`
			Transfer           bool   `json:"transfer"`
			Progression        bool   `json:"progression"`
		}{
			candidate.ClassID.String(),
			candidate.UserID.String(),
			command.StartAt,
			command.EndAt,
			strings.TrimSpace(command.ExpectedPreviousID.String()),
			command.RequireTransfer,
			command.Batch.Fields().StudentProgression,
		},
	)
}

// enrollClassMember mirrors replay to the batch and reports whether it
// freshly mutated.
func (s *Service) enrollClassMember(
	ctx context.Context,
	command EnrollClassMemberCommand,
	input *store.ClassMemberEnrollment,
) (*store.ClassEnrollmentResult, bool, error) {
	value, storeErr := s.classMembers.Enroll(ctx, input)
	freshMutation := storeErr == nil && !input.Replayed && !input.NoOp
	if command.Batch.Fields().Replayed != nil {
		*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
	}
	return value, freshMutation, storeErr
}

func (s *Service) enrollRetainedClassMember(
	ctx context.Context,
	inv invocation.Invocation,
	command EnrollClassMemberCommand,
	resource model.Resource,
	userID model.UserID,
	memberID model.ClassMemberID,
	at time.Time,
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
		if _, err = s.authorizeClass(ctx, inv, retainedPrevious.ClassID, model.ActionClassMembersManage); err != nil {
			return nil, concealMembershipAuthorizationError(err, "class_member")
		}
		if command.Batch.Fields().Authorization != nil {
			command.Batch.Fields().Authorization.ClassMemberID = retainedPrevious.ID
		}
	}

	candidate := &model.ClassMember{
		ClassID:  command.ClassID,
		UserID:   userID,
		StartsAt: model.TimeFromMillis(command.StartAt),
		EndsAt:   model.OptionalTimeFromMillis(command.EndAt),
	}
	candidate.PrepareCreate(memberID, at)
	commandIdempotency, commandErr := prepareClassEnrollmentIdempotency(inv, command, candidate)
	if commandErr != nil {
		return nil, commandErr
	}

	batchitem.BindOnboardingImport(commandIdempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(commandIdempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(commandIdempotency, command.Batch.Fields().Metadata)
	freshMutation := false
	result, replayErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionClassMembersManage,
			Resource:   resource,
			Operation:  "enroll",
			Value:      command.Batch.AuditValue(candidate.Auditable()),
			Subject:    candidate.UserID,
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*store.ClassEnrollmentResult, error) {
			execute := func(ctx context.Context, sourceAuditID model.AuditEventID) (*store.ClassEnrollmentResult, error) {
				input := &store.ClassMemberEnrollment{
					Member:                             candidate,
					StudentProgression:                 command.Batch.Fields().StudentProgression,
					ProgressionSourceAuditEventID:      command.Batch.Fields().ProgressionSourceAuditID,
					ProgressionDestinationAuditEventID: command.Batch.Fields().ProgressionDestinationAuditID,
					AuditEventID:                       reference.ID,
					PreviousAuditEventID:               sourceAuditID,
					AuditAt:                            reference.MutationAt,
					Command:                            commandIdempotency,
				}
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

			sourceAttempt := access.Mutation{
				Invocation: inv,
				Action:     model.ActionClassMembersManage,
				Resource:   model.Resource{Type: model.ResourceClass, ID: retainedPrevious.ClassID.String()},
				Operation:  "transfer_out",
				Value:      command.Batch.AuditValue(nil),
				Prior:      retainedPrevious.Auditable(),
				Subject:    retainedPrevious.UserID,
				At:         at,
			}
			return access.RunAudited(
				ctx,
				s.auditor,
				sourceAttempt,
				func(ctx context.Context, sourceReference access.MutationReference) (*store.ClassEnrollmentResult, error) {
					return execute(ctx, sourceReference.ID)
				},
				ClassMemberStoreErrors.Translate,
			)
		},
		ClassMemberStoreErrors.Translate,
	)
	if replayErr != nil {
		return nil, replayErr
	}
	if freshMutation {
		s.publishEnrollmentChanged(ctx, result)
	}

	return &model.ClassEnrollment{Membership: result.Membership, Previous: result.Previous}, nil
}

// planClassEnrollment also authorizes any membership the enrollment
// transfers from.
func (s *Service) planClassEnrollment(
	ctx context.Context,
	inv invocation.Invocation,
	command EnrollClassMemberCommand,
	userID model.UserID,
	memberID model.ClassMemberID,
	at time.Time,
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
	if validateErr := candidate.Validate(); validateErr != nil {
		return classEnrollmentPlan{}, apperror.DomainInvalid("class_member.invalid", validateErr)
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
		if _, err = s.authorizeClass(ctx, inv, previous.ClassID, model.ActionClassMembersManage); err != nil {
			return classEnrollmentPlan{}, concealMembershipAuthorizationError(err, "class_member")
		}

		previousClass, err = s.classes.Get(ctx, previous.ClassID)
		if err != nil {
			return classEnrollmentPlan{}, ClassMemberStoreErrors.Translate(err)
		}
	}

	return classEnrollmentPlan{class: class, candidate: candidate, previous: previous, previousClass: previousClass}, nil
}

func (s *Service) findTransferSource(
	ctx context.Context,
	command EnrollClassMemberCommand,
	candidate *model.ClassMember,
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
			case command.RequireTransfer &&
				(previous == nil || (previous.ID.String() != expectedPreviousID && previous.ClassID != candidate.ClassID)):
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

func (s *Service) prepareEnrollmentNotice(
	command EnrollClassMemberCommand,
	plan classEnrollmentPlan,
	recipient *model.User,
	at time.Time,
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
		prepared, err = s.mail.PrepareClassTransition(mail.ClassTransitionPreparation{
			Recipient:    recipient,
			OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey:  key,
			Details:      details,
			ActionAt:     at,
		})
		if err != nil {
			return nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}

	return prepared, nil
}

// runClassEnrollment nests the source membership's audit when the
// enrollment transfers.
func (s *Service) runClassEnrollment(
	ctx context.Context,
	inv invocation.Invocation,
	command EnrollClassMemberCommand,
	resource model.Resource,
	plan classEnrollmentPlan,
	recipient *model.User,
	prepared *store.PreparedMail,
	commandIdempotency *store.CommandIdempotency,
	at time.Time,
) (*model.ClassEnrollment, error) {
	candidate, previous := plan.candidate, plan.previous
	destinationAttempt := access.Mutation{
		Invocation: inv,
		Action:     model.ActionClassMembersManage,
		Resource:   resource,
		Operation:  "enroll",
		Value:      command.Batch.AuditValue(candidate.Auditable()),
		Subject:    candidate.UserID,
		At:         at,
	}
	if previous != nil {
		destinationAttempt.Operation = "transfer_in"
	}

	freshMutation := false
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		destinationAttempt,
		func(ctx context.Context, destinationReference access.MutationReference) (*store.ClassEnrollmentResult, error) {
			var expectedPreviousID model.ClassMemberID
			if previous == nil {
				var notice *store.PreparedMail
				if prepared != nil {
					notice = &store.PreparedMail{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job}
				}

				input := &store.ClassMemberEnrollment{
					Member:                             candidate,
					Recipient:                          recipient,
					StudentProgression:                 command.Batch.Fields().StudentProgression,
					ProgressionSourceAuditEventID:      command.Batch.Fields().ProgressionSourceAuditID,
					ProgressionDestinationAuditEventID: command.Batch.Fields().ProgressionDestinationAuditID,
					Notice:                             notice,
					AuditEventID:                       destinationReference.ID,
					AuditAt:                            destinationReference.MutationAt,
					Command:                            commandIdempotency,
				}
				value, fresh, storeErr := s.enrollClassMember(ctx, command, input)
				freshMutation = fresh
				return value, storeErr
			}

			expectedPreviousID = previous.ID
			sourceAttempt := access.Mutation{
				Invocation: inv,
				Action:     model.ActionClassMembersManage,
				Resource:   model.Resource{Type: model.ResourceClass, ID: previous.ClassID.String()},
				Operation:  "transfer_out",
				Value:      command.Batch.AuditValue(nil),
				Prior:      previous.Auditable(),
				Subject:    previous.UserID,
				At:         model.TimeUTC(destinationReference.MutationAt),
			}
			return access.RunAudited(
				ctx,
				s.auditor,
				sourceAttempt,
				func(ctx context.Context, sourceReference access.MutationReference) (*store.ClassEnrollmentResult, error) {
					var notice *store.PreparedMail
					if prepared != nil {
						notice = &store.PreparedMail{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job}
					}

					input := &store.ClassMemberEnrollment{
						Member:                             candidate,
						ExpectedPreviousID:                 expectedPreviousID,
						Recipient:                          recipient,
						StudentProgression:                 command.Batch.Fields().StudentProgression,
						ProgressionSourceAuditEventID:      command.Batch.Fields().ProgressionSourceAuditID,
						ProgressionDestinationAuditEventID: command.Batch.Fields().ProgressionDestinationAuditID,
						Notice:                             notice,
						AuditEventID:                       destinationReference.ID,
						PreviousAuditEventID:               sourceReference.ID,
						AuditAt:                            destinationReference.MutationAt,
						Command:                            commandIdempotency,
					}
					value, fresh, storeErr := s.enrollClassMember(ctx, command, input)
					freshMutation = fresh
					return value, storeErr
				},
				ClassMemberStoreErrors.Translate,
			)
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

// EndClassMember needs class.members.manage.
func (s *Service) EndClassMember(
	ctx context.Context,
	inv invocation.Invocation,
	command EndClassMemberCommand,
) (*model.ClassMember, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "class_member_id")
	}

	if err := s.authorizer.AuthorizePreflight(
		ctx,
		inv,
		model.ActionClassMembersManage,
		model.ResourceClass,
	); err != nil {
		return nil, err
	}

	if command.Batch.Fields().RetainedOutcome {
		return s.endRetainedClassMember(ctx, inv, command)
	}

	current, err := s.classMembers.Get(ctx, id)
	if err != nil {
		return nil, ClassMemberStoreErrors.Translate(err)
	}

	resource, err := s.authorizeClass(ctx, inv, current.ClassID, model.ActionClassMembersManage)
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

	commandIdempotency, err := prepareClassMemberEndIdempotency(inv, command)
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
		prepared, err = s.mail.PrepareClassTransition(mail.ClassTransitionPreparation{
			Recipient:    recipient,
			OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey:  model.MailTemplateAcademicClassEnrollmentEnded,
			Details:      mail.ClassTransitionDetails{ClassDisplayName: class.DisplayName, StartsAt: current.StartsAt, EndsAt: at},
			ActionAt:     at,
		})
		if err != nil {
			return nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}

	freshMutation := false
	ended, mutationErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionClassMembersManage,
			Resource:   resource,
			Operation:  "end",
			Value:      command.Batch.AuditValue(nil),
			Prior:      current.Auditable(),
			Subject:    current.UserID,
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.ClassMember, error) {
			input := &store.ClassMemberEnd{
				ID:               id,
				ExpectedRevision: current.Revision,
				Recipient:        recipient,
				EndAt:            reference.MutationAt,
				Notice:           &store.PreparedMail{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job},
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
				Command:          commandIdempotency,
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

func prepareClassMemberEndIdempotency(inv invocation.Invocation, command EndClassMemberCommand) (*store.CommandIdempotency, error) {
	commandIdempotency, err := idempotency.Prepare(inv, "class_member.end", command.IdempotencyKey, struct {
		ID      string `json:"id"`
		ScopeID string `json:"scope_id,omitempty"`
	}{command.ID.String(), strings.TrimSpace(command.BatchScopeID)})
	if err != nil {
		return nil, err
	}
	batchitem.BindOnboardingImport(commandIdempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(commandIdempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(commandIdempotency, command.Batch.Fields().Metadata)
	return commandIdempotency, nil
}

// endClassMember mirrors replay to the batch and reports whether it freshly
// mutated.
func (s *Service) endClassMember(
	ctx context.Context,
	command EndClassMemberCommand,
	input *store.ClassMemberEnd,
) (*model.ClassMember, bool, error) {
	value, storeErr := s.classMembers.End(ctx, input)
	freshMutation := storeErr == nil && !input.Replayed && !input.NoOp
	if command.Batch.Fields().Replayed != nil {
		*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
	}
	return value, freshMutation, storeErr
}

func (s *Service) endRetainedClassMember(
	ctx context.Context,
	inv invocation.Invocation,
	command EndClassMemberCommand,
) (*model.ClassMember, error) {
	commandIdempotency, err := prepareClassMemberEndIdempotency(inv, command)
	if err != nil {
		return nil, err
	}

	at := model.TimeUTC(s.now())
	freshMutation := false
	ended, mutationErr := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionClassMembersManage,
			Resource:   model.Resource{Type: model.ResourceClass, ID: strings.TrimSpace(command.BatchScopeID)},
			Operation:  "end",
			Value:      command.Batch.AuditValue(nil),
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.ClassMember, error) {
			input := &store.ClassMemberEnd{
				ID:               command.ID,
				ExpectedRevision: 1,
				EndAt:            reference.MutationAt,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
				Command:          commandIdempotency,
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

func (s *Service) authorizeClass(
	ctx context.Context,
	inv invocation.Invocation,
	classID model.ClassID,
	action model.Action,
) (model.Resource, error) {
	if !classID.IsValid() {
		return model.Resource{}, apperror.New("request.invalid").WithField("field", "class_id")
	}

	resource := model.Resource{Type: model.ResourceClass, ID: classID.String()}
	if err := s.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return model.Resource{}, err
	}

	return resource, nil
}

// ClassMemberStoreErrors maps class membership store failures; onboarding
// imports use it too.
var ClassMemberStoreErrors = apperror.AdministrationStoreTable(
	"class_member",
	"class.enrollment_conflict",
	"class_member.invalid",
	map[string]apperror.Code{
		"class_member_student_affiliation_required": "class_member.student_affiliation_required",
		"class_member_end_time":                     "resource.not_found",
	},
)

// ListClassMembersPageQuery selects one page of a class's memberships. Each
// page is authorized on its own.
type ListClassMembersPageQuery struct {
	AfterStartAt time.Time
	AfterUserID  model.UserID
	AfterID      model.ClassMemberID
	ClassID      model.ClassID
	// Query keeps members whose name or username contains it, and whose
	// email does for a caller with institution-wide user.view, as the user
	// directory matches.
	Query string
	MembershipPageQuery
}

// ClassMemberPage is one page of a class's memberships; only the first page
// is counted. Users names each member as a unit member page does: Email is
// set only for a caller with institution-wide user.view. Transfers holds the
// membership whose end is a transfer to another class.
type ClassMemberPage struct {
	Members   []*model.ClassMember
	HasMore   bool
	ActiveAt  int64
	Users     map[model.UserID]store.UserIdentity
	Transfers map[model.ClassMemberID]store.ClassMemberTransfer
	paging.Total
}

// ListClassMembersPage needs class.members.view.
func (s *Service) ListClassMembersPage(
	ctx context.Context,
	inv invocation.Invocation,
	query ListClassMembersPageQuery,
) (*ClassMemberPage, error) {
	if _, err := s.authorizeClass(ctx, inv, query.ClassID, model.ActionClassMembersView); err != nil {
		return nil, err
	}

	activeAt, limit, err := resolveMembershipPage(query.MembershipPageQuery, s.now())
	if err != nil {
		return nil, err
	}

	if !query.AfterID.IsZero() && !query.AfterID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "cursor")
	}

	keyset, err := membershipPageKeyset(query.MembershipPageQuery, query.AfterID.String(), query.AfterStartAt, query.AfterUserID)
	if err != nil {
		return nil, err
	}

	showEmail, err := s.authorizer.CanViewInstitutionUsers(ctx, inv)
	if err != nil {
		return nil, err
	}

	options := store.ClassMemberPageOptions{
		ClassID:              query.ClassID,
		ActiveAt:             model.TimeFromMillis(activeAt),
		Query:                strings.TrimSpace(query.Query),
		QueryMatchesEmail:    showEmail,
		MembershipPageKeyset: keyset,
		AfterID:              query.AfterID,
		Limit:                limit,
	}

	page, err := s.classMembers.ListPageByClass(ctx, options)
	if err != nil {
		return nil, ClassMemberStoreErrors.Translate(err)
	}

	result := &ClassMemberPage{Members: page.Members, ActiveAt: activeAt, HasMore: page.HasMore}
	if result.Members == nil {
		result.Members = []*model.ClassMember{}
	}

	total, consistent, err := paging.CountFirst(query.AfterID.IsZero(), len(result.Members), func(maximum int) (int, error) {
		return s.classMembers.CountPageByClass(ctx, options, maximum)
	})
	if err != nil {
		return nil, ClassMemberStoreErrors.Translate(err)
	}
	if !consistent {
		return nil, apperror.New("administration.unavailable").WithField("resource", "class_member")
	}

	result.Total = total
	if err := s.describeClassMembers(ctx, result, showEmail); err != nil {
		return nil, err
	}
	return result, nil
}

// describeClassMembers names the members and their transfers in one read
// each for the page.
func (s *Service) describeClassMembers(ctx context.Context, page *ClassMemberPage, showEmail bool) error {
	page.Users = map[model.UserID]store.UserIdentity{}
	page.Transfers = map[model.ClassMemberID]store.ClassMemberTransfer{}
	if len(page.Members) == 0 {
		return nil
	}

	userIDs := make([]model.UserID, 0, len(page.Members))
	ended := []model.ClassMemberID{}
	for _, member := range page.Members {
		if _, seen := page.Users[member.UserID]; !seen {
			page.Users[member.UserID] = store.UserIdentity{}
			userIDs = append(userIDs, member.UserID)
		}
		if member.EndsAt.Valid {
			ended = append(ended, member.ID)
		}
	}

	identities, err := s.users.ListIdentities(ctx, userIDs)
	if err != nil {
		return ClassMemberStoreErrors.Translate(err)
	}
	for _, identity := range identities {
		if !showEmail {
			identity.Email = ""
		}
		page.Users[identity.ID] = identity
	}
	for _, userID := range userIDs {
		if page.Users[userID].ID != userID {
			return apperror.New("administration.unavailable").WithField("resource", "class_member")
		}
	}

	if len(ended) == 0 {
		return nil
	}
	transfers, err := s.classMembers.ListTransfers(ctx, ended)
	if err != nil {
		return ClassMemberStoreErrors.Translate(err)
	}
	for _, transfer := range transfers {
		page.Transfers[transfer.ClassMemberID] = transfer
	}
	return nil
}
