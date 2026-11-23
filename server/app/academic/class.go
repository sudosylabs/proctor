// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"slices"
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
	// AcademicPeriodID, when set, keeps the classes of that Academic Period.
	AcademicPeriodID model.AcademicPeriodID
	paging.NameQuery
}

// SearchClassesQuery is the input of SearchClasses.
type SearchClassesQuery struct {
	AcademicUnitID model.AcademicUnitID
	// AcademicPeriodID, when set, keeps the classes of that Academic Period.
	AcademicPeriodID model.AcademicPeriodID
	Query            string
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

// ClassTeacherLimit is how many teachers a class read names; the rest are
// counted.
const ClassTeacherLimit = 3

// ClassDetail is one class and, when the caller holds class.members.view on
// it, its teachers. Teachers are the people currently bound to a role at the
// class with exam management or submission review permission.
type ClassDetail struct {
	Class *model.Class
	// Teachers is nil when the caller may not see the class's people, and
	// holds no IDs when the class has no teacher.
	Teachers *store.ClassTeachers
}

// GetClass needs class.view.
func (s *Service) GetClass(ctx context.Context, inv invocation.Invocation, id model.ClassID) (ClassDetail, error) {
	if !id.IsValid() {
		return ClassDetail{}, apperror.New("request.invalid").WithField("field", "class_id")
	}

	resource := model.Resource{Type: model.ResourceClass, ID: id.String()}
	if err := s.authorizer.Authorize(ctx, inv, model.ActionClassView, resource); err != nil {
		return ClassDetail{}, err
	}

	class, err := s.classes.Get(ctx, id)
	if err != nil {
		return ClassDetail{}, classStoreErrors.Translate(err)
	}

	// Teachers are people of the class, so they follow the same grant as its
	// members; the check records no second decision.
	visible, err := s.authorizer.CanViewClassMembers(ctx, inv, resource)
	if err != nil {
		return ClassDetail{}, err
	}
	detail := ClassDetail{Class: class}
	if !visible {
		return detail, nil
	}
	teachers, err := s.classTeachers(ctx, []model.ClassID{id}, model.TimeUTC(s.now()))
	if err != nil {
		return ClassDetail{}, err
	}
	detail.Teachers = teachers[id]
	return detail, nil
}

// classTeachers reads the teachers of classIDs in one grouped read; every
// listed class gets an entry, empty when it has none.
func (s *Service) classTeachers(ctx context.Context, classIDs []model.ClassID, at time.Time) (map[model.ClassID]*store.ClassTeachers, error) {
	read, err := s.roleBindings.ListTeachersByClasses(ctx, store.ClassTeacherOptions{
		ClassIDs: classIDs, ActiveAt: at, Limit: ClassTeacherLimit,
	})
	if err != nil {
		return nil, classStoreErrors.Translate(err)
	}
	result := make(map[model.ClassID]*store.ClassTeachers, len(classIDs))
	for _, id := range classIDs {
		result[id] = &store.ClassTeachers{ClassID: id, UserIDs: []model.UserID{}}
	}
	for index := range read {
		if _, listed := result[read[index].ClassID]; listed {
			result[read[index].ClassID] = &read[index]
		}
	}
	return result, nil
}

// ClassPage is one page of classes; only the first page is counted.
// StudentCounts holds the current member count, capped at
// store.MaximumListCount, and Teachers the teachers, of each listed class
// the caller holds class.members.view on; a class outside that grant has no
// entry in either.
type ClassPage struct {
	paging.Counted[*model.Class]
	StudentCounts map[model.ClassID]int
	Teachers      map[model.ClassID]*store.ClassTeachers
}

// ListClasses lists a programme level's classes by name; it needs
// class.view.
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

	if !query.AcademicPeriodID.IsZero() && !query.AcademicPeriodID.IsValid() {
		return ClassPage{}, apperror.New("request.invalid").WithField("field", "academic_period_id")
	}
	options := store.ClassListOptions{AcademicPeriodID: query.AcademicPeriodID, AcademicNameListOptions: store.AcademicNameListOptions{
		Descending: query.Descending,
		AfterName:  query.AfterName,
		AfterID:    query.AfterID,
		Limit:      probe,
	}}
	classes, err := s.classes.ListByProgrammeLevel(ctx, levelID, options)
	if err != nil {
		return ClassPage{}, classStoreErrors.Translate(err)
	}

	// Every class of one level shares its owning unit.
	owningUnit := func(class model.ClassID) (model.AcademicUnitID, error) {
		return s.classes.GetAcademicUnitID(ctx, class)
	}
	return s.classPage(ctx, inv, owningUnit, paging.New(classes, limit), query.AfterID == "", func(maximum int) (int, error) {
		return s.classes.CountByProgrammeLevel(ctx, levelID, options, maximum)
	})
}

// SearchClasses searches a unit's classes by name; it needs class.view.
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

	if !query.AcademicPeriodID.IsZero() && !query.AcademicPeriodID.IsValid() {
		return ClassPage{}, apperror.New("request.invalid").WithField("field", "academic_period_id")
	}
	options := store.ClassListOptions{AcademicPeriodID: query.AcademicPeriodID, AcademicNameListOptions: store.AcademicNameListOptions{
		Term:       strings.TrimSpace(query.Query),
		Descending: query.Descending,
		AfterName:  query.AfterName,
		AfterID:    query.AfterID,
		Limit:      probe,
	}}
	classes, err := s.classes.SearchByAcademicUnit(ctx, unitID, options)
	if err != nil {
		return ClassPage{}, classStoreErrors.Translate(err)
	}

	owningUnit := func(model.ClassID) (model.AcademicUnitID, error) { return unitID, nil }
	return s.classPage(ctx, inv, owningUnit, paging.New(classes, limit), query.AfterID == "", func(maximum int) (int, error) {
		return s.classes.CountByAcademicUnit(ctx, unitID, options, maximum)
	})
}

// classPage counts students and names teachers only in the listed classes
// the caller holds class.members.view on. The grant is read once for the page and records no
// decision, since the listing's class.view decision is already recorded.
// owningUnit reports the academic unit of a listed class; every class on one
// page shares it.
func (s *Service) classPage(
	ctx context.Context,
	inv invocation.Invocation,
	owningUnit func(model.ClassID) (model.AcademicUnitID, error),
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

	at := model.TimeUTC(s.now())
	grant, err := s.authorizer.GrantedScopesAt(ctx, inv.Principal(), model.ActionClassMembersView, model.ResourceClass, at)
	if err != nil {
		return ClassPage{}, err
	}

	wholePage := grant.InstitutionWide
	if !wholePage && len(grant.AcademicUnitRootIDs) > 0 {
		wholePage, err = s.unitWithinRoots(ctx, owningUnit, page.Items[0].ID, grant.AcademicUnitRootIDs)
		if err != nil {
			return ClassPage{}, err
		}
	}

	classIDs := make([]model.ClassID, 0, len(page.Items))
	for _, class := range page.Items {
		if wholePage || slices.Contains(grant.ClassIDs, class.ID) {
			classIDs = append(classIDs, class.ID)
		}
	}
	if len(classIDs) == 0 {
		return result, nil
	}

	counts, err := s.classMembers.CountCurrentByClasses(ctx, classIDs, at, store.MaximumListCount)
	if err != nil {
		return ClassPage{}, ClassMemberStoreErrors.Translate(err)
	}

	// A visible class with no current member counts zero.
	result.StudentCounts = make(map[model.ClassID]int, len(classIDs))
	for _, id := range classIDs {
		result.StudentCounts[id] = 0
	}
	for _, counted := range counts {
		if _, visible := result.StudentCounts[counted.ClassID]; visible {
			result.StudentCounts[counted.ClassID] = counted.Count
		}
	}

	result.Teachers, err = s.classTeachers(ctx, classIDs, at)
	if err != nil {
		return ClassPage{}, err
	}

	return result, nil
}

// unitWithinRoots reports whether the class's owning unit lies inside one of
// the granted unit subtrees.
func (s *Service) unitWithinRoots(
	ctx context.Context,
	owningUnit func(model.ClassID) (model.AcademicUnitID, error),
	classID model.ClassID,
	roots []model.AcademicUnitID,
) (bool, error) {
	unitID, err := owningUnit(classID)
	if err != nil {
		return false, classStoreErrors.Translate(err)
	}
	ancestors, err := s.academicUnits.ListAncestors(ctx, unitID)
	if err != nil {
		return false, academicUnitStoreErrors.Translate(err)
	}
	for _, unit := range ancestors {
		if slices.Contains(roots, unit.ID) {
			return true, nil
		}
	}
	return false, nil
}

// CreateClass needs class.manage.
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

	created, err := access.RunAudited(
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
	if err != nil {
		return nil, err
	}

	s.publishClassesChanged(ctx, created.ID)
	return created, nil
}

// UpdateClass needs class.manage.
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

	updated, err := access.RunAudited(
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
	if err != nil {
		return nil, err
	}

	// A move to another Programme Level can change the owning unit.
	s.publishClassesChanged(ctx, updated.ID)
	return updated, nil
}

// ArchiveClass needs class.manage.
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
	if err != nil {
		return err
	}

	s.publishClassChanged(ctx, id, unitID)
	return nil
}

var classStoreErrors = apperror.AdministrationStoreTable("class", "class.conflict", "class.invalid", nil)
