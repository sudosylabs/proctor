// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// UpdateInstitutionCommand is the input of UpdateInstitution.
type UpdateInstitutionCommand struct {
	Name         *string
	DisplayName  *string
	Description  *string
	ExamCapacity *model.ExamCapacityPolicy
}

// GetInstitution reads one Institution after authorizing the caller.
func (s *Service) GetInstitution(
	ctx context.Context,
	inv invocation.Invocation,
) (*model.Institution, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, access.InstitutionStoreErrors.Translate(err)
	}

	if err := s.authorizer.Authorize(
		ctx,
		inv,
		model.ActionInstitutionManage,
		model.Resource{Type: model.ResourceInstitution, ID: institution.ResourceID()},
	); err != nil {
		return nil, err
	}

	return institution, nil
}

// UpdateInstitution updates an Institution after authorizing the caller.
func (s *Service) UpdateInstitution(
	ctx context.Context,
	inv invocation.Invocation,
	command UpdateInstitutionCommand,
) (*model.Institution, error) {
	current, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, access.InstitutionStoreErrors.Translate(err)
	}

	resource := model.Resource{Type: model.ResourceInstitution, ID: current.ResourceID()}
	if err := s.authorizer.Authorize(
		ctx,
		inv,
		model.ActionInstitutionManage,
		resource,
	); err != nil {
		return nil, err
	}

	candidate := *current
	if command.Name != nil {
		candidate.Name = *command.Name
	}
	if command.DisplayName != nil {
		candidate.DisplayName = *command.DisplayName
	}
	if command.Description != nil {
		candidate.Description = *command.Description
	}
	if command.ExamCapacity != nil {
		candidate.ExamCapacity = *command.ExamCapacity
	}

	candidate.PrepareUpdate(s.now())
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("institution.invalid", err)
	}

	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionInstitutionManage,
			Resource:   resource,
			Operation:  "patch",
			Value:      candidate.Auditable(),
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Institution, error) {
			return s.institutions.Update(ctx, &store.InstitutionUpdate{
				Institution:  &candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		access.InstitutionStoreErrors.Translate,
	)
}
