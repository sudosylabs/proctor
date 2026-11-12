// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"io"
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

type DryRunStudentProgressionCommand struct {
	SourcePeriodID      model.AcademicPeriodID
	SourceClassID       model.ClassID
	DestinationPeriodID model.AcademicPeriodID
	DestinationClassID  model.ClassID
	EffectiveAt         int64
}

type CommitStudentProgressionCommand struct {
	ID               model.OnboardingImportID
	ExpectedRevision int64
	PreviewDigest    string
	IdempotencyKey   string
}

// GetStudentProgression reads one Student Progression, its rows and its Class names.
func (s *Service) GetStudentProgression(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (OnboardingImportView, []OnboardingImportRowView, error) {
	view, rows, err := s.getImport(ctx, inv, id, model.OnboardingImportStudentProgression)
	if err != nil {
		return OnboardingImportView{}, nil, studentProgressionError(err)
	}

	source, err := s.classes.Get(ctx, view.SourceClassID)
	if err != nil {
		return OnboardingImportView{}, nil, studentProgressionError(err)
	}

	destination, err := s.classes.Get(ctx, view.DestinationClassID)
	if err != nil {
		return OnboardingImportView{}, nil, studentProgressionError(err)
	}

	view.SourceClassName, view.DestinationClassName = source.DisplayName, destination.DisplayName
	return view, rows, nil
}

// ListStudentProgressionsQuery selects one page of student progressions,
// newest first. States is an optional closed-state filter and each Period or
// Class filter is optional; BeforeCreatedAt and BeforeID continue after the
// previous page's last item, both set or both zero.
type ListStudentProgressionsQuery struct {
	States              []model.OnboardingImportState
	SourcePeriodID      model.AcademicPeriodID
	SourceClassID       model.ClassID
	DestinationPeriodID model.AcademicPeriodID
	DestinationClassID  model.ClassID
	BeforeCreatedAt     time.Time
	BeforeID            model.OnboardingImportID
	Limit               int
}

// ListStudentProgressions lists the unexpired student progressions the caller
// could read one by one. The single read authorizes
// academic_progression.manage on both the source and the destination Class,
// so that authority is resolved once into a scope, recorded as one
// authorization decision, and applied to both Classes as a Store predicate.
// Items carry the summary projection without rows plus both Class names; the
// first page also carries the total.
func (s *Service) ListStudentProgressions(
	ctx context.Context,
	inv invocation.Invocation,
	query ListStudentProgressionsQuery,
) (paging.Counted[OnboardingImportView], error) {
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Counted[OnboardingImportView]{}, err
	}
	if len(query.States) > 8 {
		return paging.Counted[OnboardingImportView]{}, apperror.Invalid("request.invalid", "state")
	}

	states := make([]model.OnboardingImportState, 0, len(query.States))
	for _, state := range query.States {
		if !state.IsValid() {
			return paging.Counted[OnboardingImportView]{}, apperror.Invalid("request.invalid", "state")
		}
		if !slices.Contains(states, state) {
			states = append(states, state)
		}
	}

	for _, filter := range []struct {
		field string
		valid bool
	}{
		{"source_period_id", query.SourcePeriodID.IsZero() || query.SourcePeriodID.IsValid()},
		{"source_class_id", query.SourceClassID.IsZero() || query.SourceClassID.IsValid()},
		{"destination_period_id", query.DestinationPeriodID.IsZero() || query.DestinationPeriodID.IsValid()},
		{"destination_class_id", query.DestinationClassID.IsZero() || query.DestinationClassID.IsValid()},
	} {
		if !filter.valid {
			return paging.Counted[OnboardingImportView]{}, apperror.Invalid("request.invalid", filter.field)
		}
	}

	if (!query.BeforeID.IsZero() || !query.BeforeCreatedAt.IsZero()) && (!query.BeforeID.IsValid() || query.BeforeCreatedAt.IsZero()) {
		return paging.Counted[OnboardingImportView]{}, apperror.Invalid("request.invalid", "cursor")
	}
	if err = s.requireImports(); err != nil {
		return paging.Counted[OnboardingImportView]{}, studentProgressionError(err)
	}

	scope, err := s.visibility(ctx, inv, model.ActionAcademicProgressionManage)
	if err != nil {
		return paging.Counted[OnboardingImportView]{}, err
	}

	options := store.StudentProgressionListOptions{
		Visibility:          store.StudentProgressionVisibilityScope(scope),
		States:              states,
		SourcePeriodID:      query.SourcePeriodID,
		SourceClassID:       query.SourceClassID,
		DestinationPeriodID: query.DestinationPeriodID,
		DestinationClassID:  query.DestinationClassID,
		Limit:               probe,
	}
	if query.BeforeID.IsValid() {
		options.BeforeCreatedAt, options.BeforeID = model.TimeUTC(query.BeforeCreatedAt), query.BeforeID
	}

	values, err := s.imports.ListStudentProgressions(ctx, options)
	if err != nil {
		return paging.Counted[OnboardingImportView]{}, studentProgressionError(err)
	}
	if len(values) > probe {
		return paging.Counted[OnboardingImportView]{}, apperror.New("student_progression.unavailable")
	}

	items := make([]OnboardingImportView, 0, len(values))
	for _, value := range values {
		if value == nil || value.Progression == nil || value.Progression.Mode != model.OnboardingImportStudentProgression {
			return paging.Counted[OnboardingImportView]{}, apperror.New("student_progression.unavailable")
		}

		view := onboardingImportView(value.Progression)
		view.SourceClassName, view.DestinationClassName = value.SourceClassName, value.DestinationClassName
		items = append(items, view)
	}

	page := paging.Counted[OnboardingImportView]{Page: paging.New(items, limit)}
	total, consistent, err := paging.CountFirst(!query.BeforeID.IsValid(), len(page.Items), func(maximum int) (int, error) {
		return s.imports.CountStudentProgressions(ctx, options, maximum)
	})
	if err != nil {
		return paging.Counted[OnboardingImportView]{}, studentProgressionError(err)
	}
	if !consistent {
		return paging.Counted[OnboardingImportView]{}, apperror.New("student_progression.unavailable")
	}

	page.Total = total
	return page, nil
}

// CommitStudentProgression commits a previewed Student Progression whose preview
// digest and revision still match.
func (s *Service) CommitStudentProgression(
	ctx context.Context,
	inv invocation.Invocation,
	command CommitStudentProgressionCommand,
) (OnboardingImportView, error) {
	committed, err := s.commitImport(
		ctx,
		inv,
		CommitOnboardingImportCommand{
			ID:               command.ID,
			ExpectedRevision: command.ExpectedRevision,
			PreviewDigest:    command.PreviewDigest,
			Policy:           model.OnboardingImportValidRowsOnly,
			IdempotencyKey:   command.IdempotencyKey,
		},
		model.OnboardingImportStudentProgression,
	)
	return onboardingImportView(committed), studentProgressionError(err)
}

// CancelStudentProgression cancels an unfinished Student Progression.
func (s *Service) CancelStudentProgression(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (OnboardingImportView, error) {
	canceled, err := s.cancelImport(ctx, inv, id, model.OnboardingImportStudentProgression)
	return onboardingImportView(canceled), studentProgressionError(err)
}

// StudentProgressionReport writes a finished Student Progression's row report.
func (s *Service) StudentProgressionReport(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
	output io.Writer,
) error {
	return studentProgressionError(s.reportImport(ctx, inv, id, output, model.OnboardingImportStudentProgression))
}

// DryRunStudentProgression proposes the preview of moving a source Class's Students
// into a destination Class.
func (s *Service) DryRunStudentProgression(
	ctx context.Context,
	inv invocation.Invocation,
	command DryRunStudentProgressionCommand,
) (OnboardingImportView, error) {
	sourcePeriodID, sourceClassID :=
		model.AcademicPeriodID(strings.TrimSpace(command.SourcePeriodID.String())),
		model.ClassID(strings.TrimSpace(command.SourceClassID.String()))
	destinationPeriodID, destinationClassID :=
		model.AcademicPeriodID(strings.TrimSpace(command.DestinationPeriodID.String())),
		model.ClassID(strings.TrimSpace(command.DestinationClassID.String()))
	if !sourcePeriodID.IsValid() ||
		!sourceClassID.IsValid() ||
		!destinationPeriodID.IsValid() ||
		!destinationClassID.IsValid() ||
		sourceClassID == destinationClassID ||
		command.EffectiveAt <= 0 {
		return OnboardingImportView{}, apperror.New("request.invalid")
	}

	if err := s.requireImports(); err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}

	for _, classID := range []model.ClassID{sourceClassID, destinationClassID} {
		resource := model.Resource{Type: model.ResourceClass, ID: classID.String()}
		for _, action := range []model.Action{model.ActionAcademicProgressionManage, model.ActionClassMembersManage} {
			if err := s.authorize(ctx, inv, action, resource); err != nil {
				return OnboardingImportView{}, err
			}
		}
	}

	sourcePeriod, err := s.periods.Get(ctx, sourcePeriodID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}

	destinationPeriod, err := s.periods.Get(ctx, destinationPeriodID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}

	sourceClass, err := s.classes.Get(ctx, sourceClassID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}

	destinationClass, err := s.classes.Get(ctx, destinationClassID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}

	if sourceClass.AcademicPeriodID != sourcePeriod.ID ||
		destinationClass.AcademicPeriodID != destinationPeriod.ID ||
		sourceClass.IsArchived() ||
		destinationClass.IsArchived() {
		return OnboardingImportView{}, apperror.New("student_progression.target_conflict")
	}

	sourceLevel, err := s.levels.Get(ctx, sourceClass.ProgrammeLevelID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}

	destinationLevel, err := s.levels.Get(ctx, destinationClass.ProgrammeLevelID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}
	if sourceLevel.ProgrammeID != destinationLevel.ProgrammeID {
		return OnboardingImportView{}, apperror.New("student_progression.lineage_conflict")
	}

	effectiveAt := model.TimeFromMillis(command.EffectiveAt)
	if effectiveAt.Before(destinationPeriod.StartsAt) || !effectiveAt.Before(destinationPeriod.EndsAt) {
		return OnboardingImportView{}, apperror.New("student_progression.effective_date_conflict")
	}
	if err = s.validatePrincipal(ctx, inv.Principal()); err != nil {
		return OnboardingImportView{}, err
	}

	at := model.TimeUTC(s.now())
	id := model.NewOnboardingImportID()
	job, err := newOnboardingImportJob(model.JobTypeStudentProgressionPreview, id, at)
	if err != nil {
		return OnboardingImportView{}, apperror.New("student_progression.unavailable").Wrap(err)
	}

	value := &store.OnboardingImport{
		ID:                        id,
		Mode:                      model.OnboardingImportStudentProgression,
		State:                     model.OnboardingImportParsing,
		ScopeType:                 model.RoleScopeClass,
		ScopeID:                   destinationClass.ID.String(),
		ActorUserID:               inv.Principal().UserID,
		Principal:                 inv.Principal(),
		SourcePeriodID:            sourcePeriod.ID,
		SourceClassID:             sourceClass.ID,
		DestinationPeriodID:       destinationPeriod.ID,
		DestinationClassID:        destinationClass.ID,
		SourcePeriodRevision:      sourcePeriod.Revision,
		SourceClassRevision:       sourceClass.Revision,
		DestinationPeriodRevision: destinationPeriod.Revision,
		DestinationClassRevision:  destinationClass.Revision,
		EffectiveAt:               effectiveAt,
		ParseJobID:                job.ID,
		CreatedAt:                 at,
		UpdatedAt:                 at,
		ExpiresAt:                 at.Add(onboardingImportRetention),
		Revision:                  1,
	}
	created, err := runProgressionAudited(
		ctx,
		s.auditor,
		inv,
		"student_progression.dry_run",
		id.String(),
		sourceClass.ID,
		destinationClass.ID,
		at,
		func(ctx context.Context, destinationReference, sourceReference access.MutationReference) (*store.OnboardingImport, error) {
			return s.imports.CreateOnboardingImport(ctx, &store.OnboardingImportCreation{
				Import:             value,
				ParseJob:           job,
				AuditEventID:       destinationReference.ID,
				SourceAuditEventID: sourceReference.ID,
				AuditAt:            destinationReference.MutationAt,
			})
		},
		studentProgressionError,
	)
	if err != nil {
		return OnboardingImportView{}, err
	}
	if s.wake != nil {
		s.wake()
	}

	return onboardingImportView(created), nil
}

func runProgressionAudited[T any](
	ctx context.Context,
	audit access.MutationAuditor,
	inv invocation.Invocation,
	operation, progressionID string,
	sourceClassID, destinationClassID model.ClassID,
	at time.Time,
	mutate func(context.Context, access.MutationReference, access.MutationReference) (T, error),
	mapError func(error) error,
) (T, error) {
	destination := access.Mutation{
		Invocation: inv,
		Action:     model.ActionAcademicProgressionManage,
		Resource:   model.Resource{Type: model.ResourceClass, ID: destinationClassID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    destinationClassID.String(),
		Operation:  operation,
		Value:      map[string]any{"student_progression_id": progressionID, "class_id": destinationClassID.String()},
		At:         at,
	}
	return access.RunAudited(
		ctx,
		audit,
		destination,
		func(ctx context.Context, destinationReference access.MutationReference) (T, error) {
			source := access.Mutation{
				Invocation: inv,
				Action:     model.ActionAcademicProgressionManage,
				Resource:   model.Resource{Type: model.ResourceClass, ID: sourceClassID.String()},
				ScopeType:  model.RoleScopeClass,
				ScopeID:    sourceClassID.String(),
				Operation:  operation,
				Value:      map[string]any{"student_progression_id": progressionID, "class_id": sourceClassID.String()},
				At:         model.TimeUTC(destinationReference.MutationAt),
			}
			return access.RunAudited(
				ctx,
				audit,
				source,
				func(ctx context.Context, sourceReference access.MutationReference) (T, error) {
					return mutate(ctx, destinationReference, sourceReference)
				},
				mapError,
			)
		},
		mapError,
	)
}

func studentProgressionError(err error) error {
	switch {
	case err == nil:
		return nil
	case apperror.Is(err, "onboarding_import.conflict"):
		return apperror.New("student_progression.conflict").Wrap(err)
	case apperror.Is(err, "onboarding_import.unavailable"):
		return apperror.New("student_progression.unavailable").Wrap(err)
	default:
		return studentProgressionStoreErrors.Translate(err)
	}
}

var studentProgressionStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Conflict:    "student_progression.conflict",
	Unavailable: "student_progression.unavailable",
	Fields:      map[string]string{"resource": "student_progression"},
}
