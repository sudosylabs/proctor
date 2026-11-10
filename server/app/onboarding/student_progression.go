// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func (s *onboardingImportService) GetStudentProgression(ctx context.Context, invocation invocation.Invocation, id model.OnboardingImportID) (OnboardingImportView, []OnboardingImportRowView, error) {
	view, rows, err := s.get(ctx, invocation, id, model.OnboardingImportStudentProgression)
	if err != nil {
		return OnboardingImportView{}, nil, studentProgressionError(err)
	}
	source, err := s.deps.Classes.Get(ctx, view.SourceClassID)
	if err != nil {
		return OnboardingImportView{}, nil, studentProgressionError(err)
	}
	destination, err := s.deps.Classes.Get(ctx, view.DestinationClassID)
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
func (s *onboardingImportService) ListStudentProgressions(ctx context.Context, invocation invocation.Invocation, query ListStudentProgressionsQuery) (paging.Counted[OnboardingImportView], error) {
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
	scope, err := s.deps.Authorization.Visibility(ctx, invocation, model.ActionAcademicProgressionManage)
	if err != nil {
		return paging.Counted[OnboardingImportView]{}, err
	}
	options := store.StudentProgressionListOptions{Visibility: store.StudentProgressionVisibilityScope(scope), States: states,
		SourcePeriodID: query.SourcePeriodID, SourceClassID: query.SourceClassID, DestinationPeriodID: query.DestinationPeriodID,
		DestinationClassID: query.DestinationClassID, Limit: probe}
	if query.BeforeID.IsValid() {
		options.BeforeCreatedAt, options.BeforeID = model.TimeUTC(query.BeforeCreatedAt), query.BeforeID
	}
	values, err := s.deps.Imports.ListStudentProgressions(ctx, options)
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
		return s.deps.Imports.CountStudentProgressions(ctx, options, maximum)
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

func (s *onboardingImportService) CommitStudentProgression(ctx context.Context, invocation invocation.Invocation, command CommitStudentProgressionCommand) (OnboardingImportView, error) {
	view, err := s.commit(ctx, invocation, CommitOnboardingImportCommand{ID: command.ID, ExpectedRevision: command.ExpectedRevision,
		PreviewDigest: command.PreviewDigest, Policy: model.OnboardingImportValidRowsOnly, IdempotencyKey: command.IdempotencyKey}, model.OnboardingImportStudentProgression)
	return view, studentProgressionError(err)
}

func (s *onboardingImportService) CancelStudentProgression(ctx context.Context, invocation invocation.Invocation, id model.OnboardingImportID) (OnboardingImportView, error) {
	view, err := s.cancel(ctx, invocation, id, model.OnboardingImportStudentProgression)
	return view, studentProgressionError(err)
}

func (s *onboardingImportService) StudentProgressionReport(ctx context.Context, invocation invocation.Invocation, id model.OnboardingImportID, output io.Writer) error {
	return studentProgressionError(s.report(ctx, invocation, id, output, model.OnboardingImportStudentProgression))
}

func (s *onboardingImportService) DryRunStudentProgression(ctx context.Context, invocation invocation.Invocation, command DryRunStudentProgressionCommand) (OnboardingImportView, error) {
	sourcePeriodID, sourceClassID := model.AcademicPeriodID(strings.TrimSpace(command.SourcePeriodID.String())), model.ClassID(strings.TrimSpace(command.SourceClassID.String()))
	destinationPeriodID, destinationClassID := model.AcademicPeriodID(strings.TrimSpace(command.DestinationPeriodID.String())), model.ClassID(strings.TrimSpace(command.DestinationClassID.String()))
	if !sourcePeriodID.IsValid() || !sourceClassID.IsValid() || !destinationPeriodID.IsValid() || !destinationClassID.IsValid() ||
		sourceClassID == destinationClassID || command.EffectiveAt <= 0 {
		return OnboardingImportView{}, apperror.New("request.invalid")
	}
	for _, classID := range []model.ClassID{sourceClassID, destinationClassID} {
		resource := model.Resource{Type: model.ResourceClass, ID: classID.String()}
		for _, action := range []model.Action{model.ActionAcademicProgressionManage, model.ActionClassMembersManage} {
			if err := s.deps.Authorization.Authorize(ctx, invocation, action, resource); err != nil {
				return OnboardingImportView{}, err
			}
		}
	}
	sourcePeriod, err := s.deps.Periods.Get(ctx, sourcePeriodID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}
	destinationPeriod, err := s.deps.Periods.Get(ctx, destinationPeriodID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}
	sourceClass, err := s.deps.Classes.Get(ctx, sourceClassID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}
	destinationClass, err := s.deps.Classes.Get(ctx, destinationClassID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}
	if sourceClass.AcademicPeriodID != sourcePeriod.ID || destinationClass.AcademicPeriodID != destinationPeriod.ID || sourceClass.IsArchived() || destinationClass.IsArchived() {
		return OnboardingImportView{}, apperror.New("student_progression.target_conflict")
	}
	sourceLevel, err := s.deps.Levels.Get(ctx, sourceClass.ProgrammeLevelID)
	if err != nil {
		return OnboardingImportView{}, studentProgressionError(err)
	}
	destinationLevel, err := s.deps.Levels.Get(ctx, destinationClass.ProgrammeLevelID)
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
	if err = s.validatePrincipal(ctx, invocation.Principal()); err != nil {
		return OnboardingImportView{}, err
	}
	at := model.TimeUTC(s.deps.Now())
	id := model.NewOnboardingImportID()
	job, err := newOnboardingImportJob(model.JobTypeStudentProgressionPreview, id, at)
	if err != nil {
		return OnboardingImportView{}, apperror.New("student_progression.unavailable").Wrap(err)
	}
	value := &store.OnboardingImport{ID: id, Mode: model.OnboardingImportStudentProgression, State: model.OnboardingImportParsing,
		ScopeType: model.RoleScopeClass, ScopeID: destinationClass.ID.String(), ActorUserID: invocation.Principal().UserID, Principal: invocation.Principal(),
		SourcePeriodID: sourcePeriod.ID, SourceClassID: sourceClass.ID, DestinationPeriodID: destinationPeriod.ID, DestinationClassID: destinationClass.ID,
		SourcePeriodRevision: sourcePeriod.Revision, SourceClassRevision: sourceClass.Revision, DestinationPeriodRevision: destinationPeriod.Revision,
		DestinationClassRevision: destinationClass.Revision, EffectiveAt: effectiveAt, ParseJobID: job.ID, CreatedAt: at, UpdatedAt: at,
		ExpiresAt: at.Add(onboardingImportRetention), Revision: 1}
	created, err := runStudentProgressionAuditedMutation(ctx, s.deps.Audit, invocation, "student_progression.dry_run", id.String(),
		sourceClass.ID, destinationClass.ID, at, func(ctx context.Context, destinationReference, sourceReference access.MutationReference) (*store.OnboardingImport, error) {
			return s.deps.Imports.CreateOnboardingImport(ctx, &store.OnboardingImportCreation{Import: value, ParseJob: job,
				AuditEventID: destinationReference.ID, SourceAuditEventID: sourceReference.ID, AuditAt: destinationReference.MutationAt})
		}, studentProgressionError)
	if err != nil {
		return OnboardingImportView{}, err
	}
	if s.wake != nil {
		s.wake()
	}
	return onboardingImportView(created), nil
}

func runStudentProgressionAuditedMutation[T any](ctx context.Context, audit access.MutationAuditor, invocation invocation.Invocation, operation, progressionID string,
	sourceClassID, destinationClassID model.ClassID, at time.Time,
	mutate func(context.Context, access.MutationReference, access.MutationReference) (T, error), mapError func(error) error,
) (T, error) {
	destination := access.Mutation{Invocation: invocation, Action: model.ActionAcademicProgressionManage,
		Resource: model.Resource{Type: model.ResourceClass, ID: destinationClassID.String()}, ScopeType: model.RoleScopeClass,
		ScopeID: destinationClassID.String(), Operation: operation,
		Value: map[string]any{"student_progression_id": progressionID, "class_id": destinationClassID.String()}}
	return access.RunAudited(ctx, audit, destination, func() time.Time { return at },
		func(ctx context.Context, destinationReference access.MutationReference) (T, error) {
			source := access.Mutation{Invocation: invocation, Action: model.ActionAcademicProgressionManage,
				Resource: model.Resource{Type: model.ResourceClass, ID: sourceClassID.String()}, ScopeType: model.RoleScopeClass,
				ScopeID: sourceClassID.String(), Operation: operation,
				Value: map[string]any{"student_progression_id": progressionID, "class_id": sourceClassID.String()}}
			return access.RunAudited(ctx, audit, source, func() time.Time { return model.TimeUTC(destinationReference.MutationAt) },
				func(ctx context.Context, sourceReference access.MutationReference) (T, error) {
					return mutate(ctx, destinationReference, sourceReference)
				}, mapError)
		}, mapError)
}

func (s *onboardingImportService) previewProgression(ctx context.Context, id model.OnboardingImportID) error {
	current, err := s.deps.Imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return err
	}
	if current.Mode == model.OnboardingImportStudentProgression &&
		(current.State == model.OnboardingImportPreviewReady || current.State == model.OnboardingImportCanceled || current.State == model.OnboardingImportFailed) {
		return nil
	}
	if current.Mode != model.OnboardingImportStudentProgression || current.State != model.OnboardingImportParsing {
		return store.NewErrConflict("student_progression", "state", nil)
	}
	invocation := invocation.New(current.Principal, model.RequestMetadata{})
	if err = s.validatePrincipal(ctx, current.Principal); err != nil {
		return err
	}
	for _, classID := range []model.ClassID{current.SourceClassID, current.DestinationClassID} {
		resource := model.Resource{Type: model.ResourceClass, ID: classID.String()}
		for _, action := range []model.Action{model.ActionAcademicProgressionManage, model.ActionClassMembersManage} {
			if err = s.deps.Authorization.Authorize(ctx, invocation, action, resource); err != nil {
				return err
			}
		}
	}
	sourcePeriod, err := s.deps.Periods.Get(ctx, current.SourcePeriodID)
	if err != nil {
		return err
	}
	if sourcePeriod.Revision != current.SourcePeriodRevision {
		return apperror.New("student_progression.target_conflict")
	}
	destinationPeriod, err := s.deps.Periods.Get(ctx, current.DestinationPeriodID)
	if err != nil {
		return err
	}
	if destinationPeriod.Revision != current.DestinationPeriodRevision {
		return apperror.New("student_progression.target_conflict")
	}
	sourceClass, err := s.deps.Classes.Get(ctx, current.SourceClassID)
	if err != nil {
		return err
	}
	if sourceClass.Revision != current.SourceClassRevision {
		return apperror.New("student_progression.target_conflict")
	}
	destinationClass, err := s.deps.Classes.Get(ctx, current.DestinationClassID)
	if err != nil {
		return err
	}
	if destinationClass.Revision != current.DestinationClassRevision {
		return apperror.New("student_progression.target_conflict")
	}
	rosterAt := studentProgressionRosterInstant(current, sourcePeriod)
	members, err := s.deps.Imports.ListStudentProgressionRoster(ctx, sourceClass.ID, rosterAt, store.OnboardingImportMaximumRows+1)
	if err != nil {
		return err
	}
	if len(members) > store.OnboardingImportMaximumRows {
		return apperror.New("student_progression.roster_too_large")
	}
	rows := make([]store.OnboardingImportRow, 0, len(members))
	for index, member := range members {
		row, rowErr := s.previewProgressionRow(ctx, id, current, destinationClass, index, member)
		if rowErr != nil {
			return rowErr
		}
		rows = append(rows, row)
	}
	document, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if err = requireOnboardingImportPreviewCodes(rows); err != nil {
		return err
	}
	digest := sha256.Sum256(document)
	_, err = s.deps.Imports.CompleteOnboardingImportPreview(ctx, &store.OnboardingImportPreviewCompletion{ID: id, ExpectedRevision: current.Revision,
		Digest: hex.EncodeToString(digest[:]), Rows: rows, At: model.TimeUTC(s.deps.Now())})
	return err
}

// previewProgressionRow builds the preview row for one source roster member, classifying the member's eligibility
// and any existing destination membership.
func (s *onboardingImportService) previewProgressionRow(ctx context.Context, id model.OnboardingImportID, current *store.OnboardingImport,
	destinationClass *model.Class, index int, member *model.ClassMember) (store.OnboardingImportRow, error) {
	row := store.OnboardingImportRow{ImportID: id, RowNumber: index + 1, Reference: member.ID.String(), ScopeType: model.RoleScopeClass,
		ScopeID: destinationClass.ID.String(), TargetRevision: destinationClass.Revision, UserID: member.UserID, RelationshipID: member.ID.String(),
		RelationshipRevision: member.Revision, StartsAt: model.OptionalTimeFrom(current.EffectiveAt), PreviewStatus: model.OnboardingImportRowValid,
		Status: model.OnboardingImportRowValid, UpdatedAt: current.CreatedAt}
	if current.SourcePeriodID == current.DestinationPeriodID {
		row.Operation = string(AcademicAdministrationClassTransfer)
		if studentProgressionTransferEffectiveDateConflict(member, current.EffectiveAt) {
			row.PreviewStatus, row.Status, row.PreviewCode = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid, "student_progression.effective_date_conflict"
			return row, nil
		}
	} else {
		row.Operation = string(AcademicAdministrationClassEnroll)
	}
	user, userErr := s.deps.Users.Get(ctx, member.UserID)
	if userErr != nil {
		return store.OnboardingImportRow{}, userErr
	}
	if !user.IsActive() {
		row.PreviewStatus, row.Status, row.PreviewCode = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid, "student_progression.ineligible_target"
		return row, nil
	}
	affiliations, affiliationErr := s.deps.Affiliations.ListActiveByUser(ctx, member.UserID, current.EffectiveAt)
	if affiliationErr != nil {
		return store.OnboardingImportRow{}, affiliationErr
	}
	if !studentProgressionEligibleAffiliation(affiliations) {
		row.PreviewStatus, row.Status, row.PreviewCode = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid, "student_progression.ineligible_target"
		return row, nil
	}
	active, activeErr := s.deps.ClassMembers.ListActiveByUser(ctx, member.UserID, current.EffectiveAt)
	if activeErr != nil {
		return store.OnboardingImportRow{}, activeErr
	}
	for _, existing := range active {
		if existing.AcademicPeriodID != current.DestinationPeriodID {
			continue
		}
		if existing.ClassID == current.DestinationClassID {
			row.Operation = string(AcademicAdministrationClassEnroll)
			row.PreviewCode = "student_progression.destination_exists"
			row.DestinationRelationshipID = existing.ID.String()
			row.DestinationRelationshipRevision = existing.Revision
		} else if existing.ID != member.ID {
			row.PreviewStatus, row.Status, row.PreviewCode = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid, "student_progression.destination_conflict"
		}
		break
	}
	return row, nil
}

func studentProgressionTransferEffectiveDateConflict(member *model.ClassMember, effectiveAt time.Time) bool {
	return member == nil || member.EndsAt.Valid || !effectiveAt.After(member.StartsAt)
}

func studentProgressionEligibleAffiliation(affiliations []*model.Affiliation) bool {
	for _, affiliation := range affiliations {
		if affiliation != nil && affiliation.Kind == model.AffiliationStudent && !affiliation.EndsAt.Valid {
			return true
		}
	}
	return false
}

func studentProgressionRosterInstant(value *store.OnboardingImport, sourcePeriod *model.AcademicPeriod) time.Time {
	if value != nil && sourcePeriod != nil && value.SourcePeriodID == value.DestinationPeriodID {
		return value.EffectiveAt
	}
	if sourcePeriod == nil {
		return time.Time{}
	}
	return sourcePeriod.EndsAt.Add(-time.Millisecond)
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
