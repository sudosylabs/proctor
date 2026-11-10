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
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListAffiliationsQuery is the input of ListAffiliations.
type ListAffiliationsQuery struct {
	UserID model.UserID
}

// CreateAffiliationCommand is the input of CreateAffiliation.
type CreateAffiliationCommand struct {
	UserID         model.UserID
	Kind           model.AffiliationKind
	StartAt        int64
	EndAt          int64
	IdempotencyKey string
	Batch          batchitem.Item
}

// EndAffiliationCommand is the input of EndAffiliation.
type EndAffiliationCommand struct {
	ID             model.AffiliationID
	IdempotencyKey string
	BatchScopeType model.RoleScopeType
	BatchScopeID   string
	Batch          batchitem.Item
}

// ListAffiliations lists Affiliations after authorizing the caller.
func (s *Service) ListAffiliations(ctx context.Context, invocation invocation.Invocation, query ListAffiliationsQuery) ([]*model.Affiliation, error) {
	userID := query.UserID
	if !userID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	if err := s.authorizer.AuthorizeUserRead(ctx, invocation, userID); err != nil {
		return nil, err
	}
	affiliations, err := s.affiliations.ListByUser(ctx, userID)
	if err != nil {
		return nil, AffiliationStoreErrors.Translate(err)
	}
	if affiliations == nil {
		affiliations = []*model.Affiliation{}
	}
	return affiliations, nil
}

// CreateAffiliation creates an Affiliation after authorizing the caller.
func (s *Service) CreateAffiliation(ctx context.Context, invocation invocation.Invocation, command CreateAffiliationCommand) (*model.Affiliation, error) {
	resource, err := s.authorizeUser(ctx, invocation, command.UserID)
	if err != nil {
		return nil, err
	}
	userID, err := model.ParseUserID(resource.ID)
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "user_id").Wrap(err)
	}
	affiliationID, err := model.ParseAffiliationID(s.newID())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "affiliation_id").Wrap(err)
	}
	candidate := &model.Affiliation{
		UserID:   userID,
		Kind:     command.Kind,
		StartsAt: model.TimeFromMillis(command.StartAt),
		EndsAt:   model.OptionalTimeFromMillis(command.EndAt),
	}
	at := s.now()
	candidate.PrepareCreate(affiliationID, at)
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("affiliation.invalid", err)
	}
	idempotency, err := idempotency.Prepare(invocation, "affiliation.add", command.IdempotencyKey, struct {
		UserID  string                `json:"user_id"`
		Kind    model.AffiliationKind `json:"kind"`
		StartAt int64                 `json:"start_at"`
		EndAt   int64                 `json:"end_at"`
	}{candidate.UserID.String(), candidate.Kind, command.StartAt, command.EndAt})
	if err != nil {
		return nil, err
	}
	bindOnboardingImportCommand(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	bindAcademicAdministrationAuthorization(idempotency, command.Batch.Fields().Authorization)
	bindAcademicAdministrationBatch(idempotency, command.Batch.Fields().Metadata)
	changed := false
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionUserManage,
			Resource:   resource,
			Operation:  "create",
			Value:      candidate.Auditable(),
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*model.Affiliation, error) {
			input := &store.AffiliationCreation{Affiliation: candidate, AuditEventID: reference.ID,
				AuditAt: reference.MutationAt, Command: idempotency}
			value, storeErr := s.affiliations.Create(ctx, input)
			if command.Batch.Fields().Replayed != nil {
				*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
			}
			changed = !input.Replayed && !input.NoOp
			return value, storeErr
		},
		AffiliationStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	if changed {
		if effectErr := s.effects.Changed(ctx, result.UserID); effectErr != nil {
			s.effects.Report(ctx, "affiliation.changed", effectErr)
		}
	}
	return result, nil
}

// EndAffiliation ends an Affiliation after authorizing the caller.
func (s *Service) EndAffiliation(ctx context.Context, invocation invocation.Invocation, command EndAffiliationCommand) (*model.Affiliation, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "affiliation_id")
	}
	current, err := s.affiliations.Get(ctx, id)
	if err != nil {
		return nil, AffiliationStoreErrors.Translate(err)
	}
	resource, err := s.authorizeUser(ctx, invocation, current.UserID)
	if err != nil {
		return nil, err
	}
	if current.Kind == model.AffiliationStudent && !command.Batch.Fields().RetainedOutcome {
		enrollments, listErr := s.classMembers.ListByUser(ctx, current.UserID)
		if listErr != nil {
			return nil, AffiliationStoreErrors.Translate(listErr)
		}
		for _, enrollment := range enrollments {
			// Open-ended enrollment (EndsAt absent) blocks ending a student affiliation.
			if enrollment != nil && !enrollment.EndsAt.Valid {
				return nil, apperror.New("affiliation.student_has_active_enrollment")
			}
		}
	}
	idempotency, err := idempotency.Prepare(invocation, "affiliation.end", command.IdempotencyKey, struct {
		ID        string              `json:"id"`
		ScopeType model.RoleScopeType `json:"scope_type,omitempty"`
		ScopeID   string              `json:"scope_id,omitempty"`
	}{id.String(), command.BatchScopeType, strings.TrimSpace(command.BatchScopeID)})
	if err != nil {
		return nil, err
	}
	bindOnboardingImportCommand(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	bindAcademicAdministrationAuthorization(idempotency, command.Batch.Fields().Authorization)
	bindAcademicAdministrationBatch(idempotency, command.Batch.Fields().Metadata)
	changed := false
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionUserManage,
			Resource:   resource,
			Operation:  "end",
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Affiliation, error) {
			input := &store.AffiliationEnd{
				ID: id, ExpectedRevision: current.Revision, EndAt: reference.MutationAt,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt, Command: idempotency,
			}
			value, storeErr := s.affiliations.End(ctx, input)
			if command.Batch.Fields().Replayed != nil {
				*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
			}
			changed = !input.Replayed && !input.NoOp
			return value, storeErr
		},
		AffiliationStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	if changed {
		if effectErr := s.effects.Changed(ctx, result.UserID); effectErr != nil {
			s.effects.Report(ctx, "affiliation.changed", effectErr)
		}
	}
	return result, nil
}

func (s *Service) authorizeUser(ctx context.Context, invocation invocation.Invocation, userID model.UserID) (model.Resource, error) {
	if !userID.IsValid() {
		return model.Resource{}, apperror.New("request.invalid").WithField("field", "user_id")
	}
	resource := model.Resource{Type: model.ResourceUser, ID: userID.String()}
	if err := s.authorizer.Authorize(ctx, invocation, model.ActionUserManage, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
}

// AffiliationStoreErrors translates Affiliation Store failures; onboarding
// imports present them the same way.
var AffiliationStoreErrors = apperror.AdministrationStoreTable("affiliation", "affiliation.conflict", "affiliation.invalid", map[string]apperror.Code{
	"affiliation_student_has_active_enrollment": "affiliation.student_has_active_enrollment",
	"affiliation_end_time":                      "resource.not_found",
})
