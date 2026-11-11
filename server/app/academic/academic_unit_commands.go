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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CreateAcademicUnitCommand contains caller-controlled Academic Unit fields.
// Installation ownership and persistence lifecycle fields are not inputs.
type CreateAcademicUnitCommand struct {
	ParentID       model.AcademicUnitID
	Name           string
	DisplayName    string
	Description    string
	IdempotencyKey string
}

// UpdateAcademicUnitCommand is the input of UpdateAcademicUnit.
type UpdateAcademicUnitCommand struct {
	ExpectedRevision *int64
	ID               model.AcademicUnitID
	ParentID         *model.AcademicUnitID
	Name             *string
	DisplayName      *string
	Description      *string
}

// ArchiveAcademicUnitCommand is the input of ArchiveAcademicUnit.
type ArchiveAcademicUnitCommand struct {
	ExpectedRevision *int64
	ID               model.AcademicUnitID
}

// CreateAcademicUnit creates a root or child Academic Unit through one
// application-owned authorization and one atomic durable mutation.
func (s *Service) CreateAcademicUnit(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateAcademicUnitCommand,
) (*model.AcademicUnit, error) {
	parentID := command.ParentID
	var institution model.Resource
	var authorized model.Resource
	var action model.Action
	if parentID == "" {
		action = model.ActionAcademicUnitManage
		var err error
		institution, err = s.authorizer.AuthorizeInstitution(ctx, inv, action)
		if err != nil {
			return nil, err
		}

		authorized = institution
	} else {
		if !parentID.IsValid() {
			return nil, apperror.New("request.invalid").WithField("field", "parent_id")
		}

		action = model.ActionAcademicUnitManage
		authorized = model.Resource{Type: model.ResourceAcademicUnit, ID: parentID.String()}
		if err := s.authorizer.Authorize(ctx, inv, action, authorized); err != nil {
			return nil, err
		}

		var err error
		institution, err = s.authorizer.InstitutionResource(ctx)
		if err != nil {
			return nil, err
		}
	}

	unitID, err := model.ParseAcademicUnitID(s.newID())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_id").Wrap(err)
	}

	institutionID, err := model.ParseInstitutionID(institution.ID)
	if err != nil {
		return nil, apperror.New("administration.unavailable").WithField("resource", "institution").Wrap(err)
	}

	parentUnitID := parentID
	candidate := &model.AcademicUnit{
		InstitutionID: model.InstitutionID(institutionID),
		ParentID:      model.AcademicUnitID(parentUnitID),
		Name:          command.Name,
		DisplayName:   command.DisplayName,
		Description:   command.Description,
	}
	candidate.PrepareCreate(unitID, s.now())
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("academic_unit.invalid", err)
	}

	idempotency, err := idempotency.Prepare(inv, "academic_unit.create", command.IdempotencyKey, struct {
		ParentID, Name, DisplayName, Description string
	}{command.ParentID.String(), command.Name, command.DisplayName, command.Description})
	if err != nil {
		return nil, err
	}

	saved, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     action,
			Resource:   authorized,
			Operation:  "create",
			Value:      candidate.Auditable(),
			At:         s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*store.AcademicUnitCommandResult, error) {
			input := &store.AcademicUnitCreation{
				Unit:         candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			}
			if idempotency == nil {
				unit, createErr := s.academicUnits.Create(ctx, input)
				return &store.AcademicUnitCommandResult{Value: unit}, createErr
			}

			return s.academicUnits.CreateIdempotently(ctx, input, idempotency)
		},
		academicUnitStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}

	// The unit and success audit are committed before transient fan-out.
	// Publication remains best effort so callers do not retry committed work.
	if !saved.Replayed {
		if err := s.publishAcademicUnitCreated(ctx, saved.Value.ID); err != nil {
			s.publisher.Report(ctx, "academic_unit_created", err)
		}
	}

	return saved.Value, nil
}

// UpdateAcademicUnit updates an Academic Unit after authorizing the caller.
func (s *Service) UpdateAcademicUnit(
	ctx context.Context,
	inv invocation.Invocation,
	command UpdateAcademicUnitCommand,
) (*model.AcademicUnit, error) {
	if !command.ID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}

	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: command.ID.String()}
	if err := s.authorizer.Authorize(
		ctx,
		inv,
		model.ActionAcademicUnitManage,
		resource,
	); err != nil {
		return nil, err
	}

	current, err := s.academicUnits.Get(ctx, command.ID)
	if err != nil {
		return nil, academicUnitStoreErrors.Translate(err)
	}
	if err := checkAcademicRevision(command.ExpectedRevision, current.Revision, "academic_unit.conflict"); err != nil {
		return nil, err
	}

	candidate := *current
	if command.ParentID != nil {
		parentID := strings.TrimSpace(command.ParentID.String())
		if parentID == "" {
			candidate.ParentID = ""
		} else {
			parentUnitID, parseErr := model.ParseAcademicUnitID(parentID)
			if parseErr != nil {
				return nil, apperror.New("request.invalid").WithField("field", "parent_id").Wrap(parseErr)
			}

			candidate.ParentID = parentUnitID
		}
	}

	if command.Name != nil {
		candidate.Name = *command.Name
	}
	if command.DisplayName != nil {
		candidate.DisplayName = *command.DisplayName
	}
	if command.Description != nil {
		candidate.Description = *command.Description
	}

	candidate.PrepareUpdate(s.now())
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("academic_unit.invalid", err)
	}

	if command.ParentID != nil && candidate.ParentID != current.ParentID {
		destination := model.Resource{Type: model.ResourceAcademicUnit, ID: candidate.ParentID.String()}
		if candidate.ParentID.IsZero() {
			destination = model.Resource{Type: model.ResourceInstitution, ID: current.InstitutionID.String()}
		}

		if err = s.authorizer.Authorize(
			ctx,
			inv,
			model.ActionAcademicUnitManage,
			destination,
		); err != nil {
			return nil, err
		}
	}

	updated, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionAcademicUnitManage,
			Resource:   resource,
			Operation:  "patch",
			Value:      candidate.Auditable(),
			Prior:      current.Auditable(),
			At:         s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*model.AcademicUnit, error) {
			return s.academicUnits.Update(ctx, &store.AcademicUnitUpdate{
				Unit:         &candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		func(err error) error { return academicUnitStoreErrors.Translate(err) },
	)
	if err != nil {
		return nil, err
	}
	if err := s.publishAcademicUnitUpdated(ctx, updated.ID); err != nil {
		s.publisher.Report(ctx, "academic_unit_updated", err)
	}

	return updated, nil
}

// ArchiveAcademicUnit archives an Academic Unit after authorizing the caller.
func (s *Service) ArchiveAcademicUnit(
	ctx context.Context,
	inv invocation.Invocation,
	command ArchiveAcademicUnitCommand,
) error {
	if !command.ID.IsValid() {
		return apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}

	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: command.ID.String()}
	if err := s.authorizer.Authorize(
		ctx,
		inv,
		model.ActionAcademicUnitManage,
		resource,
	); err != nil {
		return err
	}

	current, err := s.academicUnits.Get(ctx, command.ID)
	if err != nil {
		return academicUnitStoreErrors.Translate(err)
	}
	if err := checkAcademicRevision(command.ExpectedRevision, current.Revision, "academic_unit.conflict"); err != nil {
		return err
	}

	archived, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionAcademicUnitManage,
			Resource:   resource,
			Operation:  "archive",
			Value:      map[string]any{"id": command.ID},
			Prior:      current.Auditable(),
			At:         s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*model.AcademicUnit, error) {
			return s.academicUnits.Archive(ctx, &store.AcademicUnitArchive{
				ExpectedRevision: current.Revision,
				ID:               command.ID,
				ArchiveAt:        reference.MutationAt,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			})
		},
		func(err error) error { return academicUnitStoreErrors.Translate(err) },
	)
	if err != nil {
		return err
	}
	if err := s.publishAcademicUnitArchived(ctx, archived.ID); err != nil {
		s.publisher.Report(ctx, "academic_unit_archived", err)
	}

	return nil
}
