// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ScopeResolverDependencies configures NewScopeResolver.
type ScopeResolverDependencies struct {
	Institutions    store.InstitutionStore
	AcademicUnits   store.AcademicUnitStore
	Classes         store.ClassStore
	Users           store.UserStore
	ClassMembers    store.ClassMemberStore
	Exams           store.ExamAuthoringStore
	Sittings        store.ExamSittingStore
	Submissions     store.ExamSubmissionStore
	AcademicPeriods store.AcademicPeriodStore
	// Programmes and ProgrammeLevels are optional; without them programme and
	// programme level resources do not resolve.
	Programmes      store.ProgrammeStore
	ProgrammeLevels store.ProgrammeLevelStore
}

// ScopeResolver loads a resource's institution, academic unit ancestry and
// class for an authorization decision. Anything archived along the way reads
// as resource.not_found.
type ScopeResolver struct {
	deps ScopeResolverDependencies
}

// NewScopeResolver fails when a required store is missing.
func NewScopeResolver(deps ScopeResolverDependencies) (*ScopeResolver, error) {
	if deps.Institutions == nil ||
		deps.AcademicUnits == nil ||
		deps.Classes == nil ||
		deps.Users == nil ||
		deps.ClassMembers == nil ||
		deps.Exams == nil ||
		deps.Sittings == nil ||
		deps.Submissions == nil ||
		deps.AcademicPeriods == nil {
		return nil, errors.New("access scope resolver persistence is required")
	}

	resolver := &ScopeResolver{deps: deps}
	return resolver, nil
}

func (r *ScopeResolver) resolve(
	ctx context.Context,
	resource model.Resource,
) (resolvedAuthorizationResource, error) {
	resolved := resolvedAuthorizationResource{academicUnitID: make(map[model.AcademicUnitID]struct{})}
	var err error
	switch resource.Type {
	case model.ResourceInstitution:
		err = r.resolveInstitution(ctx, resource, &resolved)
	case model.ResourceAcademicUnit:
		err = r.resolveAcademicUnit(ctx, resource, &resolved)
	case model.ResourceAcademicPeriod:
		err = r.resolveAcademicPeriod(ctx, resource, &resolved)
	case model.ResourceProgramme:
		err = r.resolveProgramme(ctx, resource, &resolved)
	case model.ResourceProgrammeLevel:
		err = r.resolveProgrammeLevel(ctx, resource, &resolved)
	case model.ResourceClass:
		err = r.resolveClass(ctx, resource, &resolved)
	case model.ResourceExam:
		err = r.resolveExam(ctx, resource, &resolved)
	case model.ResourceExamSitting:
		err = r.resolveExamSitting(ctx, resource, &resolved)
	case model.ResourceSubmission:
		err = r.resolveSubmission(ctx, resource, &resolved)
	case model.ResourceUser:
		err = r.resolveUser(ctx, resource, &resolved)
	default:
		return resolved, apperror.New("authorization.request.invalid")
	}

	if err != nil {
		return resolved, err
	}

	return resolved, nil
}

func (r *ScopeResolver) listScopeAncestors(
	ctx context.Context,
	academicUnitID model.AcademicUnitID,
	lookupResource,
	notFoundResource string,
) ([]*model.AcademicUnit, error) {
	units, err := r.deps.AcademicUnits.ListAncestors(ctx, academicUnitID)
	if err != nil {
		return nil, ResourceError(lookupResource, err)
	}
	if len(units) == 0 {
		return nil, apperror.New("resource.not_found").WithField("resource", notFoundResource)
	}
	return units, nil
}

func applyScopeAncestors(
	resolved *resolvedAuthorizationResource,
	units []*model.AcademicUnit,
	target model.AcademicUnitID,
	notFoundResource string,
) error {
	resolved.institutionID = units[0].InstitutionID
	resolved.targetAcademicUnitID = target
	for _, unit := range units {
		if unit == nil || unit.IsArchived() {
			return apperror.New("resource.not_found").WithField("resource", notFoundResource)
		}
		resolved.academicUnitID[unit.ID] = struct{}{}
	}
	return nil
}

func (r *ScopeResolver) resolveAcademicUnitScope(
	ctx context.Context,
	resolved *resolvedAuthorizationResource,
	academicUnitID model.AcademicUnitID,
	lookupResource,
	notFoundResource string,
) error {
	units, err := r.listScopeAncestors(ctx, academicUnitID, lookupResource, notFoundResource)
	if err != nil {
		return err
	}

	return applyScopeAncestors(resolved, units, academicUnitID, notFoundResource)
}

func (r *ScopeResolver) resolveInstitution(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	institution, err := r.deps.Institutions.Get(ctx, model.InstitutionID(resource.ID))
	if err != nil {
		return ResourceError("institution", err)
	}
	if institution.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "institution")
	}
	resolved.institutionID = institution.ID
	return nil
}

func (r *ScopeResolver) resolveAcademicUnit(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	units, err := r.deps.AcademicUnits.ListAncestors(ctx, model.AcademicUnitID(resource.ID))
	if err != nil {
		return ResourceError("academic_unit", err)
	}
	if len(units) == 0 {
		return apperror.New("resource.not_found").WithField("resource", "academic_unit")
	}
	return applyScopeAncestors(resolved, units, model.AcademicUnitID(resource.ID), "academic_unit")
}

func (r *ScopeResolver) resolveAcademicPeriod(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	period, err := r.deps.AcademicPeriods.Get(ctx, model.AcademicPeriodID(resource.ID))
	if err != nil {
		return ResourceError("academic_period", err)
	}
	if period == nil || period.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "academic_period")
	}

	if period.Owner.InstitutionID.IsValid() {
		institution, institutionErr := r.deps.Institutions.Get(ctx, period.Owner.InstitutionID)
		if institutionErr != nil {
			return ResourceError("academic_period_institution", institutionErr)
		}
		if institution.IsArchived() {
			return apperror.New("resource.not_found").WithField("resource", "academic_period")
		}

		resolved.institutionID = institution.ID
		resolved.academicPeriodInstitutionOwned = true
		return nil
	}

	return r.resolveAcademicUnitScope(
		ctx,
		resolved,
		period.Owner.AcademicUnitID,
		"academic_period_academic_unit",
		"academic_period",
	)
}

func (r *ScopeResolver) resolveProgramme(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	if r.deps.Programmes == nil {
		return UnavailableError(errors.New("Programme persistence is required"))
	}
	programme, err := r.deps.Programmes.Get(ctx, model.ProgrammeID(resource.ID))
	if err != nil {
		return ResourceError("programme", err)
	}
	if programme == nil || programme.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "programme")
	}
	return r.resolveAcademicUnitScope(ctx, resolved, programme.AcademicUnitID, "programme_academic_unit", "programme")
}

func (r *ScopeResolver) resolveProgrammeLevel(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	if r.deps.ProgrammeLevels == nil || r.deps.Programmes == nil {
		return UnavailableError(errors.New("Programme Level persistence is required"))
	}

	level, err := r.deps.ProgrammeLevels.Get(ctx, model.ProgrammeLevelID(resource.ID))
	if err != nil {
		return ResourceError("programme_level", err)
	}
	if level == nil || level.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "programme_level")
	}

	programme, err := r.deps.Programmes.Get(ctx, level.ProgrammeID)
	if err != nil {
		return ResourceError("programme_level_programme", err)
	}
	if programme == nil || programme.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "programme_level")
	}

	return r.resolveAcademicUnitScope(
		ctx,
		resolved,
		programme.AcademicUnitID,
		"programme_level_academic_unit",
		"programme_level",
	)
}

// resolveClass also refuses a class whose academic period does not apply to
// the class's unit.
func (r *ScopeResolver) resolveClass(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	class, err := r.deps.Classes.Get(ctx, model.ClassID(resource.ID))
	if err != nil {
		return ResourceError("class", err)
	}
	if class.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "class")
	}

	academicUnitID, err := r.deps.Classes.GetAcademicUnitID(ctx, model.ClassID(resource.ID))
	if err != nil {
		return ResourceError("class", err)
	}

	units, err := r.listScopeAncestors(ctx, academicUnitID, "class_academic_unit", "class")
	if err != nil {
		return err
	}

	resolved.classID = model.ClassID(resource.ID)
	if err = applyScopeAncestors(resolved, units, academicUnitID, "class"); err != nil {
		return err
	}

	periodScope, err := r.resolve(ctx, model.Resource{Type: model.ResourceAcademicPeriod, ID: class.AcademicPeriodID.String()})
	if err != nil {
		if apperror.Is(err, "resource.not_found") {
			return apperror.New("resource.not_found").WithField("resource", "class")
		}
		return err
	}

	if periodScope.institutionID != resolved.institutionID {
		return apperror.New("resource.not_found").WithField("resource", "class")
	}

	if !periodScope.academicPeriodInstitutionOwned {
		if _, applicable := resolved.academicUnitID[periodScope.targetAcademicUnitID]; !applicable {
			return apperror.New("resource.not_found").WithField("resource", "class")
		}
	}

	return nil
}

func (r *ScopeResolver) resolveExam(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	if r.deps.Exams == nil {
		return UnavailableError(errors.New("exam persistence is required"))
	}
	exam, err := r.deps.Exams.Resolve(ctx, model.ExamID(resource.ID))
	if err != nil {
		return ResourceError("exam", err)
	}
	return r.resolveAcademicUnitScope(ctx, resolved, exam.AcademicUnitID, "exam_academic_unit", "exam")
}

func (r *ScopeResolver) resolveExamSitting(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	snapshot, err := r.deps.Sittings.Resolve(ctx, model.ExamSittingID(resource.ID))
	if err != nil {
		return ResourceError("exam_sitting", err)
	}
	if snapshot == nil || snapshot.Sitting == nil {
		return UnavailableError(errors.New("Exam Sitting persistence returned no projection"))
	}
	exam, err := r.deps.Exams.Resolve(ctx, snapshot.Sitting.ExamID)
	if err != nil {
		return ResourceError("exam_sitting_exam", err)
	}
	return r.resolveAcademicUnitScope(ctx, resolved, exam.AcademicUnitID, "exam_sitting_academic_unit", "exam_sitting")
}

func (r *ScopeResolver) resolveSubmission(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	submissionID, err := model.ParseSubmissionID(resource.ID)
	if err != nil {
		return apperror.New("authorization.request.invalid").Wrap(err)
	}

	ownership, err := r.deps.Submissions.Resolve(ctx, submissionID)
	if err != nil {
		return ResourceError("submission", err)
	}

	if ownership == nil ||
		ownership.SubmissionID != submissionID ||
		!ownership.ExamID.IsValid() ||
		!ownership.SittingID.IsValid() ||
		!ownership.AttemptID.IsValid() ||
		!ownership.AcademicUnitID.IsValid() {
		return UnavailableError(errors.New("Submission persistence returned no ownership projection"))
	}

	return r.resolveAcademicUnitScope(ctx, resolved, ownership.AcademicUnitID, "submission_academic_unit", "submission")
}

// resolveUser reports a disabled user as resource.not_found.
func (r *ScopeResolver) resolveUser(ctx context.Context, resource model.Resource, resolved *resolvedAuthorizationResource) error {
	user, err := r.deps.Users.Get(ctx, model.UserID(resource.ID))
	if err != nil {
		return ResourceError("user", err)
	}
	if !user.IsActive() {
		return apperror.New("resource.not_found").WithField("resource", "user")
	}
	institution, err := r.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return ResourceError("institution", err)
	}
	if institution.IsArchived() {
		return apperror.New("resource.not_found").WithField("resource", "institution")
	}
	resolved.institutionID = institution.ID
	return nil
}
