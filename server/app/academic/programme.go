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
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// GetProgrammeQuery names one Programme.
type GetProgrammeQuery struct{ ID model.ProgrammeID }

// ListProgrammesQuery is the input of ListProgrammes.
type ListProgrammesQuery struct {
	AcademicUnitID model.AcademicUnitID
	Query          string
	paging.NameQuery
}

// CreateProgrammeCommand is the input of CreateProgramme.
type CreateProgrammeCommand struct {
	AcademicUnitID model.AcademicUnitID
	Name           string
	DisplayName    string
	Description    string
}

// UpdateProgrammeCommand is the input of UpdateProgramme.
type UpdateProgrammeCommand struct {
	ExpectedRevision *int64
	ID               model.ProgrammeID
	Name             *string
	DisplayName      *string
	Description      *string
}

// ArchiveProgrammeCommand is the input of ArchiveProgramme.
type ArchiveProgrammeCommand struct {
	ExpectedRevision *int64
	ID               model.ProgrammeID
}

// GetProgramme reads one Programme after authorizing the caller.
func (s *Service) GetProgramme(ctx context.Context, invocation invocation.Invocation, query GetProgrammeQuery) (*model.Programme, error) {
	id := query.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "programme_id")
	}
	resource := model.Resource{Type: model.ResourceProgramme, ID: id.String()}
	if err := s.authorizer.Authorize(ctx, invocation, model.ActionProgrammeView, resource); err != nil {
		return nil, err
	}
	programme, err := s.programmes.Get(ctx, id)
	if err != nil {
		return nil, programmeStoreErrors.Translate(err)
	}
	return programme, nil
}

// ListProgrammes lists Programmes after authorizing the caller.
func (s *Service) ListProgrammes(ctx context.Context, invocation invocation.Invocation, query ListProgrammesQuery) (paging.Page[*model.Programme], error) {
	unitID := query.AcademicUnitID
	if !unitID.IsValid() {
		return paging.Page[*model.Programme]{}, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[*model.Programme]{}, err
	}
	if err = s.authorizer.Authorize(ctx, invocation, model.ActionProgrammeView, model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}); err != nil {
		return paging.Page[*model.Programme]{}, err
	}
	programmes, err := s.programmes.SearchByAcademicUnit(ctx, unitID, store.AcademicNameListOptions{
		Term: strings.TrimSpace(query.Query), AfterName: query.AfterName, AfterID: query.AfterID, Limit: probe,
	})
	if err != nil {
		return paging.Page[*model.Programme]{}, programmeStoreErrors.Translate(err)
	}
	return paging.New(programmes, limit), nil
}

// CreateProgramme creates a Programme after authorizing the caller.
func (s *Service) CreateProgramme(ctx context.Context, invocation invocation.Invocation, command CreateProgrammeCommand) (*model.Programme, error) {
	unitID := command.AcademicUnitID
	if !unitID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}
	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}
	scope, err := s.authorizer.AuthorizeWithScope(ctx, invocation, model.ActionProgrammeManage, resource)
	scopeType, scopeID := scope.ScopeType, scope.ScopeID
	if err != nil {
		return nil, err
	}
	programmeID, err := model.ParseProgrammeID(s.newID())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "programme_id").Wrap(err)
	}
	academicUnitID := unitID
	candidate := &model.Programme{
		AcademicUnitID: academicUnitID,
		Name:           command.Name,
		DisplayName:    command.DisplayName,
		Description:    command.Description,
	}
	candidate.PrepareCreate(programmeID, s.now())
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("programme.invalid", err)
	}
	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionProgrammeManage,
			Resource:   resource,
			ScopeType:  scopeType,
			ScopeID:    scopeID,
			Operation:  "create",
			Value:      candidate.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Programme, error) {
			return s.programmes.Create(ctx, &store.ProgrammeCreation{
				Programme: candidate, AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			})
		},
		programmeStoreErrors.Translate,
	)
}

// UpdateProgramme updates a Programme after authorizing the caller.
func (s *Service) UpdateProgramme(ctx context.Context, invocation invocation.Invocation, command UpdateProgrammeCommand) (*model.Programme, error) {
	current, scopeType, scopeID, err := s.getForMutation(ctx, invocation, command.ID)
	if err != nil {
		return nil, err
	}
	if err := checkAcademicRevision(command.ExpectedRevision, current.Revision, "programme.conflict"); err != nil {
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
	candidate.PrepareUpdate(s.now())
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("programme.invalid", err)
	}
	resource := model.Resource{Type: model.ResourceProgramme, ID: current.ID.String()}
	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionProgrammeManage,
			Resource:   resource,
			ScopeType:  scopeType,
			ScopeID:    scopeID,
			Operation:  "patch",
			Value:      candidate.Auditable(),
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Programme, error) {
			return s.programmes.Update(ctx, &store.ProgrammeUpdate{
				Programme: &candidate, AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			})
		},
		programmeStoreErrors.Translate,
	)
}

// ArchiveProgramme archives a Programme after authorizing the caller.
func (s *Service) ArchiveProgramme(ctx context.Context, invocation invocation.Invocation, command ArchiveProgrammeCommand) error {
	current, scopeType, scopeID, err := s.getForMutation(ctx, invocation, command.ID)
	if err != nil {
		return err
	}
	if err := checkAcademicRevision(command.ExpectedRevision, current.Revision, "programme.conflict"); err != nil {
		return err
	}
	resource := model.Resource{Type: model.ResourceProgramme, ID: current.ID.String()}
	_, err = access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionProgrammeManage,
			Resource:   resource,
			ScopeType:  scopeType,
			ScopeID:    scopeID,
			Operation:  "archive",
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.Programme, error) {
			return s.programmes.Archive(ctx, &store.ProgrammeArchive{
				ExpectedRevision: current.Revision,
				ID:               current.ID, ArchiveAt: reference.MutationAt,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt,
			})
		},
		programmeStoreErrors.Translate,
	)
	return err
}

func (s *Service) getForMutation(ctx context.Context, invocation invocation.Invocation, id model.ProgrammeID) (*model.Programme, model.RoleScopeType, string, error) {
	if !id.IsValid() {
		return nil, "", "", apperror.New("request.invalid").WithField("field", "programme_id")
	}
	scope, err := s.authorizer.AuthorizeWithScope(ctx, invocation, model.ActionProgrammeManage, model.Resource{Type: model.ResourceProgramme, ID: id.String()})
	scopeType, scopeID := scope.ScopeType, scope.ScopeID
	if err != nil {
		return nil, "", "", err
	}
	programme, err := s.programmes.Get(ctx, id)
	if err != nil {
		return nil, "", "", programmeStoreErrors.Translate(err)
	}
	return programme, scopeType, scopeID, nil
}

var programmeStoreErrors = apperror.AdministrationStoreTable("programme", "programme.conflict", "programme.invalid", nil)
