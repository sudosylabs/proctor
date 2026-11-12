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
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListClassesQuery is the input of ListClasses.
type ListClassesQuery struct {
	ProgrammeLevelID model.ProgrammeLevelID
	paging.NameQuery
}

// SearchClassesQuery is the input of SearchClasses.
type SearchClassesQuery struct {
	AcademicUnitID model.AcademicUnitID
	Query          string
	paging.NameQuery
}

// CreateClassCommand is the input of CreateClass.
type CreateClassCommand struct {
	ProgrammeLevelID model.ProgrammeLevelID
	AcademicPeriodID model.AcademicPeriodID
	Name             string
	DisplayName      string
	Description      string
}

// UpdateClassCommand is the input of UpdateClass.
type UpdateClassCommand struct {
	ExpectedRevision *int64
	ID               model.ClassID
	ProgrammeLevelID *model.ProgrammeLevelID
	AcademicPeriodID *model.AcademicPeriodID
	Name             *string
	DisplayName      *string
	Description      *string
}

// ArchiveClassCommand is the input of ArchiveClass.
type ArchiveClassCommand struct {
	ExpectedRevision *int64
	ID               model.ClassID
}

// GetClass reads one Class after authorizing the caller.
func (s *Service) GetClass(ctx context.Context, inv invocation.Invocation, id model.ClassID) (*model.Class, error) {
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "class_id")
	}

	if err := s.authorizer.Authorize(
		ctx,
		inv,
		model.ActionClassView,
		model.Resource{Type: model.ResourceClass, ID: id.String()},
	); err != nil {
		return nil, err
	}

	class, err := s.classes.Get(ctx, id)
	if err != nil {
		return nil, classStoreErrors.Translate(err)
	}

	return class, nil
}

// ClassPage is one page of Classes. The first page also counts every Class
// the listing matches. StudentCounts holds each listed Class's current member
// count, stopped at store.MaximumListCount and absent for a Class with none;
// it is nil when the caller lacks class.members.view on the listing's scope.
type ClassPage struct {
	paging.Counted[*model.Class]
	StudentCounts map[model.ClassID]int
}

// ListClasses lists a Programme Level's Classes by name after authorizing the
// caller.
func (s *Service) ListClasses(
	ctx context.Context,
	inv invocation.Invocation,
	query ListClassesQuery,
) (ClassPage, error) {
	levelID := query.ProgrammeLevelID
	if !levelID.IsValid() {
		return ClassPage{}, apperror.New("request.invalid").WithField("field", "programme_level_id")
	}

	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return ClassPage{}, err
	}

	resource := model.Resource{Type: model.ResourceProgrammeLevel, ID: levelID.String()}
	if err = s.authorizer.Authorize(ctx, inv, model.ActionClassView, resource); err != nil {
		return ClassPage{}, err
	}

	options := store.AcademicNameListOptions{
		Descending: query.Descending,
		AfterName:  query.AfterName,
		AfterID:    query.AfterID,
		Limit:      probe,
	}
	classes, err := s.classes.ListByProgrammeLevel(ctx, levelID, options)
	if err != nil {
		return ClassPage{}, classStoreErrors.Translate(err)
	}

	return s.classPage(ctx, inv, resource, paging.New(classes, limit), query.AfterID == "", func(maximum int) (int, error) {
		return s.classes.CountByProgrammeLevel(ctx, levelID, options, maximum)
	})
}

// SearchClasses searches an Academic Unit's Classes by name after
// authorizing the caller.
func (s *Service) SearchClasses(
	ctx context.Context,
	inv invocation.Invocation,
	query SearchClassesQuery,
) (ClassPage, error) {
	unitID := query.AcademicUnitID
	if !unitID.IsValid() {
		return ClassPage{}, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}

	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return ClassPage{}, err
	}

	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}
	if err = s.authorizer.Authorize(ctx, inv, model.ActionClassView, resource); err != nil {
		return ClassPage{}, err
	}

	options := store.AcademicNameListOptions{
		Term:       strings.TrimSpace(query.Query),
		Descending: query.Descending,
		AfterName:  query.AfterName,
		AfterID:    query.AfterID,
		Limit:      probe,
	}
	classes, err := s.classes.SearchByAcademicUnit(ctx, unitID, options)
	if err != nil {
		return ClassPage{}, classStoreErrors.Translate(err)
	}

	return s.classPage(ctx, inv, resource, paging.New(classes, limit), query.AfterID == "", func(maximum int) (int, error) {
		return s.classes.CountByAcademicUnit(ctx, unitID, options, maximum)
	})
}

// classPage counts the first page of a Class listing and, for a caller
// holding class.members.view on the listing's scope, each listed Class's
// current students in one grouped read.
func (s *Service) classPage(
	ctx context.Context,
	inv invocation.Invocation,
	scope model.Resource,
	page paging.Page[*model.Class],
	first bool,
	count func(int) (int, error),
) (ClassPage, error) {
	total, consistent, err := paging.CountFirst(first, len(page.Items), count)
	if err != nil {
		return ClassPage{}, classStoreErrors.Translate(err)
	}
	if !consistent {
		return ClassPage{}, apperror.New("administration.unavailable").WithField("resource", "class")
	}

	result := ClassPage{Counted: paging.Counted[*model.Class]{Page: page, Total: total}}
	if len(page.Items) == 0 {
		return result, nil
	}

	visible, err := s.authorizer.CanViewClassMembers(ctx, inv, scope)
	if err != nil || !visible {
		return result, err
	}

	classIDs := make([]model.ClassID, 0, len(page.Items))
	for _, class := range page.Items {
		classIDs = append(classIDs, class.ID)
	}

	counts, err := s.classMembers.CountCurrentByClasses(ctx, classIDs, model.TimeUTC(s.now()), store.MaximumListCount)
	if err != nil {
		return ClassPage{}, ClassMemberStoreErrors.Translate(err)
	}

	result.StudentCounts = make(map[model.ClassID]int, len(counts))
	for _, counted := range counts {
		result.StudentCounts[counted.ClassID] = counted.Count
	}

	return result, nil
}

// CreateClass creates a Class after authorizing the caller.
func (s *Service) CreateClass(ctx context.Context, inv invocation.Invocation, command CreateClassCommand) (*model.Class, error) {
	levelID := command.ProgrammeLevelID
	if !levelID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "programme_level_id")
	}

	resource := model.Resource{Type: model.ResourceProgrammeLevel, ID: levelID.String()}
	scope, err := s.authorizer.AuthorizeWithScope(ctx, inv, model.ActionClassManage, resource)
	scopeType, scopeID := scope.ScopeType, scope.ScopeID
	if err != nil {
		return nil, err
	}

	classID, err := model.ParseClassID(s.newID())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "class_id").Wrap(err)
	}

	academicPeriodID, err := model.ParseAcademicPeriodID(strings.TrimSpace(command.AcademicPeriodID.String()))
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "academic_period_id").Wrap(err)
	}

	candidate := &model.Class{
		ProgrammeLevelID: levelID,
		AcademicPeriodID: academicPeriodID,
		Name:             command.Name,
		DisplayName:      command.DisplayName,
		Description:      command.Description,
	}
	candidate.PrepareCreate(classID, s.now())
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("class.invalid", err)
	}

	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionClassManage,
			Resource:   resource,
			ScopeType:  scopeType,
			ScopeID:    scopeID,
			Operation:  "create",
			Value:      candidate.Auditable(),
			At:         s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*model.Class, error) {
			return s.classes.Create(ctx, &store.ClassCreation{
				Class:        candidate,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		classStoreErrors.Translate,
	)
}

// UpdateClass updates a Class after authorizing the caller.
func (s *Service) UpdateClass(ctx context.Context, inv invocation.Invocation, command UpdateClassCommand) (*model.Class, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "class_id")
	}

	resource := model.Resource{Type: model.ResourceClass, ID: id.String()}
	if err := s.authorizer.Authorize(ctx, inv, model.ActionClassManage, resource); err != nil {
		return nil, err
	}

	unitID, err := s.classes.GetAcademicUnitID(ctx, id)
	if err != nil {
		return nil, classStoreErrors.Translate(err)
	}

	current, err := s.classes.Get(ctx, id)
	if err != nil {
		return nil, classStoreErrors.Translate(err)
	}
	if err := checkAcademicRevision(command.ExpectedRevision, current.Revision, "class.conflict"); err != nil {
		return nil, err
	}

	candidate := *current
	if command.ProgrammeLevelID != nil {
		levelID, err := model.ParseProgrammeLevelID(strings.TrimSpace(command.ProgrammeLevelID.String()))
		if err != nil {
			return nil, apperror.New("request.invalid").WithField("field", "programme_level_id").Wrap(err)
		}

		candidate.ProgrammeLevelID = levelID
	}

	if command.AcademicPeriodID != nil {
		periodID, err := model.ParseAcademicPeriodID(strings.TrimSpace(command.AcademicPeriodID.String()))
		if err != nil {
			return nil, apperror.New("request.invalid").WithField("field", "academic_period_id").Wrap(err)
		}

		candidate.AcademicPeriodID = periodID
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

	if candidate.ProgrammeLevelID != current.ProgrammeLevelID {
		destination := model.Resource{Type: model.ResourceProgrammeLevel, ID: candidate.ProgrammeLevelID.String()}
		if err := s.authorizer.Authorize(ctx, inv, model.ActionClassManage, destination); err != nil {
			return nil, err
		}
	}

	at := s.now()
	if !at.After(current.UpdatedAt) {
		at = current.UpdatedAt.Add(time.Millisecond)
	}

	candidate.PrepareUpdate(at)
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("class.invalid", err)
	}

	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionClassManage,
			Resource:   resource,
			Operation:  "patch",
			Value:      candidate.Auditable(),
			Prior:      current.Auditable(),
			At:         s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*model.Class, error) {
			return s.classes.Update(ctx, &store.ClassUpdate{
				Class:                  &candidate,
				ExpectedAcademicUnitID: unitID,
				ExpectedRevision:       current.Revision,
				AuditEventID:           reference.ID,
				AuditAt:                reference.MutationAt,
			})
		},
		classStoreErrors.Translate,
	)
}

// ArchiveClass archives a Class after authorizing the caller.
func (s *Service) ArchiveClass(ctx context.Context, inv invocation.Invocation, command ArchiveClassCommand) error {
	id := command.ID
	if !id.IsValid() {
		return apperror.New("request.invalid").WithField("field", "class_id")
	}

	resource := model.Resource{Type: model.ResourceClass, ID: id.String()}
	if err := s.authorizer.Authorize(ctx, inv, model.ActionClassManage, resource); err != nil {
		return err
	}

	unitID, err := s.classes.GetAcademicUnitID(ctx, id)
	if err != nil {
		return classStoreErrors.Translate(err)
	}

	current, err := s.classes.Get(ctx, id)
	if err != nil {
		return classStoreErrors.Translate(err)
	}
	if checkErr := checkAcademicRevision(command.ExpectedRevision, current.Revision, "class.conflict"); checkErr != nil {
		return checkErr
	}

	at := s.now()
	if !at.After(current.UpdatedAt) {
		at = current.UpdatedAt.Add(time.Millisecond)
	}

	_, err = access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionClassManage,
			Resource:   resource,
			Operation:  "archive",
			Prior:      current.Auditable(),
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.Class, error) {
			return s.classes.Archive(ctx, &store.ClassArchive{
				ID:                     id,
				ExpectedAcademicUnitID: unitID,
				ExpectedRevision:       current.Revision,
				ArchiveAt:              reference.MutationAt,
				AuditEventID:           reference.ID,
				AuditAt:                reference.MutationAt,
			})
		},
		classStoreErrors.Translate,
	)
	return err
}

var classStoreErrors = apperror.AdministrationStoreTable("class", "class.conflict", "class.invalid", nil)
