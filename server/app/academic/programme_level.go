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

// GetProgrammeLevelQuery names one Programme Level.
type GetProgrammeLevelQuery struct{ ID model.ProgrammeLevelID }

// ListProgrammeLevelsQuery is the input of ListProgrammeLevels.
type ListProgrammeLevelsQuery struct {
	ProgrammeID model.ProgrammeID
	Query       string
	paging.NameQuery
}

// CreateProgrammeLevelCommand is the input of CreateProgrammeLevel.
type CreateProgrammeLevelCommand struct {
	ProgrammeID model.ProgrammeID
	Name        string
	DisplayName string
	Description string
}

// UpdateProgrammeLevelCommand is the input of UpdateProgrammeLevel.
type UpdateProgrammeLevelCommand struct {
	ExpectedRevision *int64
	ID               model.ProgrammeLevelID
	Name             *string
	DisplayName      *string
	Description      *string
}

// ArchiveProgrammeLevelCommand is the input of ArchiveProgrammeLevel.
type ArchiveProgrammeLevelCommand struct {
	ExpectedRevision *int64
	ID               model.ProgrammeLevelID
}

// GetProgrammeLevel reads one Programme Level after authorizing the caller.
func (s *Service) GetProgrammeLevel(
	ctx context.Context,
	invocation invocation.Invocation,
	query GetProgrammeLevelQuery,
) (*model.ProgrammeLevel, error) {
	id := query.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "programme_level_id")
	}

	if err := s.authorizer.Authorize(
		ctx,
		invocation,
		model.ActionProgrammeLevelView,
		model.Resource{Type: model.ResourceProgrammeLevel, ID: id.String()},
	); err != nil {
		return nil, err
	}

	level, err := s.programmeLevels.Get(ctx, id)
	if err != nil {
		return nil, programmeLevelStoreErrors.Translate(err)
	}

	return level, nil
}

// ListProgrammeLevels lists Programme Levels after authorizing the caller.
func (s *Service) ListProgrammeLevels(
	ctx context.Context,
	invocation invocation.Invocation,
	query ListProgrammeLevelsQuery,
) (paging.Page[*model.ProgrammeLevel], error) {
	programmeID := query.ProgrammeID
	if !programmeID.IsValid() {
		return paging.Page[*model.ProgrammeLevel]{}, apperror.New("request.invalid").WithField("field", "programme_id")
	}

	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[*model.ProgrammeLevel]{}, err
	}

	if err = s.authorizer.Authorize(
		ctx,
		invocation,
		model.ActionProgrammeLevelView,
		model.Resource{Type: model.ResourceProgramme, ID: programmeID.String()},
	); err != nil {
		return paging.Page[*model.ProgrammeLevel]{}, err
	}

	levels, err := s.programmeLevels.SearchByProgramme(ctx, programmeID, store.AcademicNameListOptions{
		Term:      strings.TrimSpace(query.Query),
		AfterName: query.AfterName,
		AfterID:   query.AfterID,
		Limit:     probe,
	})
	if err != nil {
		return paging.Page[*model.ProgrammeLevel]{}, programmeLevelStoreErrors.Translate(err)
	}

	return paging.New(levels, limit), nil
}

// CreateProgrammeLevel creates a Programme Level after authorizing the caller.
func (s *Service) CreateProgrammeLevel(
	ctx context.Context,
	invocation invocation.Invocation,
	command CreateProgrammeLevelCommand,
) (*model.ProgrammeLevel, error) {
	programmeID := command.ProgrammeID
	if !programmeID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "programme_id")
	}

	resource := model.Resource{Type: model.ResourceProgramme, ID: programmeID.String()}
	scope, err := s.authorizer.AuthorizeWithScope(ctx, invocation, model.ActionProgrammeLevelManage, resource)
	scopeType, scopeID := scope.ScopeType, scope.ScopeID
	if err != nil {
		return nil, err
	}

	levelID, err := model.ParseProgrammeLevelID(s.newID())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "programme_level_id").Wrap(err)
	}

	programmeTypedID := programmeID
	candidate := &model.ProgrammeLevel{
		ProgrammeID: programmeTypedID,
		Name:        command.Name,
		DisplayName: command.DisplayName,
		Description: command.Description,
	}
	candidate.PrepareCreate(levelID, s.now())
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("programme_level.invalid", err)
	}

	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionProgrammeLevelManage,
			Resource:   resource,
			ScopeType:  scopeType,
			ScopeID:    scopeID,
			Operation:  "create",
			Value:      candidate.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.ProgrammeLevel, error) {
			return s.programmeLevels.Create(ctx, &store.ProgrammeLevelCreation{
				Level:        candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		programmeLevelStoreErrors.Translate,
	)
}

// UpdateProgrammeLevel updates a Programme Level after authorizing the caller.
func (s *Service) UpdateProgrammeLevel(
	ctx context.Context,
	invocation invocation.Invocation,
	command UpdateProgrammeLevelCommand,
) (*model.ProgrammeLevel, error) {
	current, scopeType, scopeID, err := s.levelForMutation(ctx, invocation, command.ID)
	if err != nil {
		return nil, err
	}

	resource := model.Resource{Type: model.ResourceProgrammeLevel, ID: current.ID.String()}
	if err := checkAcademicRevision(command.ExpectedRevision, current.Revision, "programme_level.conflict"); err != nil {
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
		return nil, apperror.DomainInvalid("programme_level.invalid", err)
	}

	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionProgrammeLevelManage,
			Resource:   resource,
			ScopeType:  scopeType,
			ScopeID:    scopeID,
			Operation:  "patch",
			Value:      candidate.Auditable(),
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.ProgrammeLevel, error) {
			return s.programmeLevels.Update(ctx, &store.ProgrammeLevelUpdate{
				Level:        &candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		programmeLevelStoreErrors.Translate,
	)
}

// ArchiveProgrammeLevel archives a Programme Level after authorizing the caller.
func (s *Service) ArchiveProgrammeLevel(ctx context.Context, invocation invocation.Invocation, command ArchiveProgrammeLevelCommand) error {
	current, scopeType, scopeID, err := s.levelForMutation(ctx, invocation, command.ID)
	if err != nil {
		return err
	}
	if revisionErr := checkAcademicRevision(command.ExpectedRevision, current.Revision, "programme_level.conflict"); revisionErr != nil {
		return revisionErr
	}

	resource := model.Resource{Type: model.ResourceProgrammeLevel, ID: current.ID.String()}
	_, err = access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionProgrammeLevelManage,
			Resource:   resource,
			ScopeType:  scopeType,
			ScopeID:    scopeID,
			Operation:  "archive",
			Prior:      current.Auditable(),
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.ProgrammeLevel, error) {
			return s.programmeLevels.Archive(ctx, &store.ProgrammeLevelArchive{
				ExpectedRevision: current.Revision,
				ID:               current.ID,
				ArchiveAt:        reference.MutationAt,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			})
		},
		programmeLevelStoreErrors.Translate,
	)
	return err
}

func (s *Service) levelForMutation(
	ctx context.Context,
	invocation invocation.Invocation,
	id model.ProgrammeLevelID,
) (*model.ProgrammeLevel, model.RoleScopeType, string, error) {
	if !id.IsValid() {
		return nil, "", "", apperror.New("request.invalid").WithField("field", "programme_level_id")
	}

	scope, err := s.authorizer.AuthorizeWithScope(
		ctx,
		invocation,
		model.ActionProgrammeLevelManage,
		model.Resource{Type: model.ResourceProgrammeLevel, ID: id.String()},
	)
	scopeType, scopeID := scope.ScopeType, scope.ScopeID
	if err != nil {
		return nil, "", "", err
	}

	level, err := s.programmeLevels.Get(ctx, id)
	if err != nil {
		return nil, "", "", programmeLevelStoreErrors.Translate(err)
	}

	return level, scopeType, scopeID, nil
}

var programmeLevelStoreErrors = apperror.AdministrationStoreTable(
	"programme_level",
	"programme_level.conflict",
	"programme_level.invalid",
	nil,
)
