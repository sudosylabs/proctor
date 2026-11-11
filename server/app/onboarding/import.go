// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
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

const onboardingImportRetention = 7 * 24 * time.Hour

type UploadOnboardingImportCommand struct {
	Mode      model.OnboardingImportMode
	ScopeType model.RoleScopeType
	ScopeID   string
	RoleID    model.RoleID
	// FileName is the uploaded file's name when the uploader gave one: it is
	// trimmed and must be a base name of 1 to 255 runes.
	FileName *string
	Body     io.Reader
}

// OnboardingImportView is what a caller reads of one onboarding import or
// Student Progression: its target, lifecycle state, row counts and Jobs. It
// carries no uploaded row content and none of the frozen actor Principal.
type OnboardingImportView struct {
	ID                  model.OnboardingImportID
	Mode                model.OnboardingImportMode
	State               model.OnboardingImportState
	ScopeType           model.RoleScopeType
	ScopeID             string
	RoleID              model.RoleID
	SourcePeriodID      model.AcademicPeriodID
	SourceClassID       model.ClassID
	DestinationPeriodID model.AcademicPeriodID
	DestinationClassID  model.ClassID
	// SourceClassName and DestinationClassName are the Classes' display
	// names, set only on student progression reads and listings.
	SourceClassName      string
	DestinationClassName string
	EffectiveAt          time.Time
	PreviewDigest        string
	IgnoredHeaders       []string
	TotalRows            int
	ValidRows            int
	InvalidRows          int
	SucceededRows        int
	NoOpRows             int
	FailedRows           int
	SkippedRows          int
	CommitPolicy         model.OnboardingImportCommitPolicy
	ParseJobID           model.JobID
	ExecutionJobID       model.JobID
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ExpiresAt            time.Time
	Revision             int64
	FailureCode          string
	FileName             string
}

// OnboardingImportRowView is the safe projection of one import row: where it
// sits in the file, what it would do and how it ended, without the email
// address or names the row holds.
type OnboardingImportRowView struct {
	RowNumber    int
	Reference    string
	Operation    string
	Status       model.OnboardingImportRowStatus
	PreviewCode  string
	PublicCode   string
	InvitationID model.InvitationID
	ResourceID   string
}

// ListOnboardingImportsQuery selects one page of administrative imports,
// newest first. States is an optional closed-state filter; BeforeCreatedAt
// and BeforeID continue after the previous page's last item, both set or both
// zero.
type ListOnboardingImportsQuery struct {
	States          []model.OnboardingImportState
	BeforeCreatedAt time.Time
	BeforeID        model.OnboardingImportID
	Limit           int
}

// requireImports refuses an onboarding import use case on a Service built
// without onboarding imports.
func (s *Service) requireImports() error {
	if s.imports == nil {
		return apperror.New("onboarding_import.unavailable")
	}
	return nil
}

// UploadOnboardingImport stages an onboarding import file and proposes its parse Job.
func (s *Service) UploadOnboardingImport(
	ctx context.Context,
	invocation invocation.Invocation,
	command UploadOnboardingImportCommand,
) (OnboardingImportView, error) {
	command.ScopeID = strings.TrimSpace(command.ScopeID)
	command.RoleID = model.RoleID(strings.TrimSpace(command.RoleID.String()))
	if command.Body == nil ||
		command.Mode == model.OnboardingImportStudentProgression ||
		model.ValidateOnboardingImportScope(command.Mode, command.ScopeType, command.ScopeID) != nil ||
		(command.Mode == model.OnboardingImportTeacherAcademicUnit && !command.RoleID.IsValid()) ||
		(command.Mode != model.OnboardingImportTeacherAcademicUnit && command.RoleID != "") {
		return OnboardingImportView{}, apperror.New("request.invalid")
	}

	fileName := ""
	if command.FileName != nil {
		var valid bool
		if fileName, valid = model.NormalizeOnboardingImportFileName(*command.FileName); !valid {
			return OnboardingImportView{}, apperror.Invalid("request.invalid", "file_name")
		}
	}

	if err := s.requireImports(); err != nil {
		return OnboardingImportView{}, err
	}

	resource := onboardingImportResource(command.ScopeType, command.ScopeID)
	if err := s.authorize(ctx, invocation, model.ActionOnboardingBatchManage, resource); err != nil {
		return OnboardingImportView{}, err
	}
	if err := s.validatePrincipal(ctx, invocation.Principal()); err != nil {
		return OnboardingImportView{}, err
	}
	if err := s.validateExternalTarget(ctx, invocation, command.Mode, command.ScopeID, command.RoleID.String(), true); err != nil {
		return OnboardingImportView{}, err
	}

	at := model.TimeUTC(s.now())
	created, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionOnboardingBatchManage,
			Resource:   resource,
			ScopeType:  command.ScopeType,
			ScopeID:    command.ScopeID,
			Operation:  "onboarding_import.upload",
			Value:      map[string]any{"mode": command.Mode, "scope_type": command.ScopeType, "scope_id": command.ScopeID},
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*store.OnboardingImport, error) {
			id := model.NewOnboardingImportID()
			if _, _, stageErr := s.content.StageOnboardingImport(ctx, id, command.Body, store.OnboardingImportMaximumBytes); stageErr != nil {
				if s.content.IsOnboardingImportTooLarge(stageErr) {
					return nil, apperror.New("onboarding_import.file_invalid").Wrap(stageErr)
				}
				return nil, apperror.New("onboarding_import.unavailable").Wrap(stageErr)
			}

			job, jobErr := newOnboardingImportJob(model.JobTypeOnboardingImportParse, id, at)
			if jobErr != nil {
				_ = s.content.RemoveOnboardingImport(ctx, id)
				return nil, jobErr
			}

			value := &store.OnboardingImport{
				ID:          id,
				Mode:        command.Mode,
				State:       model.OnboardingImportParsing,
				ScopeType:   command.ScopeType,
				ScopeID:     command.ScopeID,
				ActorUserID: invocation.Principal().UserID,
				Principal:   invocation.Principal(),
				ParseJobID:  job.ID,
				CreatedAt:   at,
				UpdatedAt:   at,
				ExpiresAt:   at.Add(onboardingImportRetention),
				Revision:    1,
				FileName:    fileName,
			}
			if command.RoleID != "" {
				value.RoleID = command.RoleID
			}

			created, createErr := s.imports.CreateOnboardingImport(ctx, &store.OnboardingImportCreation{
				Import:       value,
				ParseJob:     job,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
			if createErr != nil {
				_ = s.content.RemoveOnboardingImport(ctx, id)
			}

			return created, createErr
		},
		onboardingImportStoreErrors.Translate,
	)
	if err != nil {
		return OnboardingImportView{}, err
	}
	if s.wake != nil {
		s.wake()
	}

	return onboardingImportView(created), nil
}

func newOnboardingImportJob(kind model.JobType, id model.OnboardingImportID, at time.Time) (*model.Job, error) {
	command, err := json.Marshal(onboardingImportJobCommand{ImportID: id.String()})
	if err != nil {
		return nil, err
	}
	return model.NewJob(model.NewJobID(), kind, command, string(kind)+":"+id.String(), at, at, 8)
}

// GetOnboardingImport reads one onboarding import and its rows.
func (s *Service) GetOnboardingImport(
	ctx context.Context,
	invocation invocation.Invocation,
	id model.OnboardingImportID,
) (OnboardingImportView, []OnboardingImportRowView, error) {
	return s.getImport(ctx, invocation, id, "")
}

// countOpenOnboardingImports counts the unexpired, unfinished administrative
// imports within the caller's onboarding_batch.view scopes, the authority
// GetOnboardingImport checks for those modes, stopping at maximum. Student
// progressions are authorized per Class pair and are not counted.
func (s *Service) countOpenOnboardingImports(ctx context.Context, invocation invocation.Invocation, maximum int) (int, error) {
	if err := s.requireImports(); err != nil {
		return 0, err
	}
	scope, err := s.visibility(ctx, invocation, model.ActionOnboardingBatchView)
	if err != nil {
		return 0, err
	}
	total, err := s.imports.CountOpenOnboardingImports(ctx, store.OnboardingImportVisibilityScope(scope), maximum)
	if err != nil {
		return 0, onboardingImportStoreErrors.Translate(err)
	}
	return total, nil
}

func (s *Service) getImport(
	ctx context.Context,
	invocation invocation.Invocation,
	id model.OnboardingImportID,
	expectedMode model.OnboardingImportMode,
) (OnboardingImportView, []OnboardingImportRowView, error) {
	if err := s.requireImports(); err != nil {
		return OnboardingImportView{}, nil, err
	}

	value, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return OnboardingImportView{}, nil, onboardingImportStoreErrors.Translate(err)
	}
	if err = s.authorizeImport(ctx, invocation, value, false); err != nil {
		return OnboardingImportView{}, nil, err
	}
	if expectedMode != "" && value.Mode != expectedMode {
		return OnboardingImportView{}, nil, apperror.New("resource.not_found").WithField("resource", "student_progression")
	}

	rows := make([]OnboardingImportRowView, 0, value.TotalRows)
	for after := 0; ; {
		page, pageErr := s.imports.ListOnboardingImportRows(ctx, id, after, store.OnboardingImportPageSize)
		if pageErr != nil {
			return OnboardingImportView{}, nil, onboardingImportStoreErrors.Translate(pageErr)
		}

		for _, row := range page.Rows {
			rows = append(rows, safeOnboardingImportRow(row))
			after = row.RowNumber
		}

		if !page.More {
			break
		}
	}

	return onboardingImportView(value), rows, nil
}

// ListOnboardingImports lists the unexpired administrative imports the caller
// could read one by one: the onboarding_batch.view authority is resolved once
// into a scope and recorded as one authorization decision, as the Invitation
// listing does. Student progression aggregates authorize through Class
// progression authority and stay on their own surface, so they are never
// listed here. Items carry the summary projection without rows.
func (s *Service) ListOnboardingImports(
	ctx context.Context,
	invocation invocation.Invocation,
	query ListOnboardingImportsQuery,
) (paging.Page[OnboardingImportView], error) {
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[OnboardingImportView]{}, err
	}
	if len(query.States) > 8 {
		return paging.Page[OnboardingImportView]{}, apperror.Invalid("request.invalid", "state")
	}

	states := make([]model.OnboardingImportState, 0, len(query.States))
	for _, state := range query.States {
		if !state.IsValid() {
			return paging.Page[OnboardingImportView]{}, apperror.Invalid("request.invalid", "state")
		}
		if !slices.Contains(states, state) {
			states = append(states, state)
		}
	}

	if (!query.BeforeID.IsZero() || !query.BeforeCreatedAt.IsZero()) && (!query.BeforeID.IsValid() || query.BeforeCreatedAt.IsZero()) {
		return paging.Page[OnboardingImportView]{}, apperror.Invalid("request.invalid", "cursor")
	}
	if err = s.requireImports(); err != nil {
		return paging.Page[OnboardingImportView]{}, err
	}

	scope, err := s.visibility(ctx, invocation, model.ActionOnboardingBatchView)
	if err != nil {
		return paging.Page[OnboardingImportView]{}, err
	}

	options := store.OnboardingImportListOptions{Visibility: store.OnboardingImportVisibilityScope(scope), States: states, Limit: probe}
	if query.BeforeID.IsValid() {
		options.BeforeCreatedAt, options.BeforeID = model.TimeUTC(query.BeforeCreatedAt), query.BeforeID
	}

	values, err := s.imports.ListOnboardingImports(ctx, options)
	if err != nil {
		return paging.Page[OnboardingImportView]{}, onboardingImportStoreErrors.Translate(err)
	}
	if len(values) > probe {
		return paging.Page[OnboardingImportView]{}, apperror.New("onboarding_import.unavailable")
	}

	items := make([]OnboardingImportView, 0, len(values))
	for _, value := range values {
		items = append(items, onboardingImportView(value))
	}

	return paging.New(items, limit), nil
}

func (s *Service) authorizeImport(
	ctx context.Context,
	invocation invocation.Invocation,
	value *store.OnboardingImport,
	mutation bool,
) error {
	if value == nil {
		return apperror.New("request.invalid")
	}

	if value.Mode != model.OnboardingImportStudentProgression {
		action := model.ActionOnboardingBatchView
		if mutation {
			action = model.ActionOnboardingBatchManage
		}

		return s.authorize(ctx, invocation, action, onboardingImportResource(value.ScopeType, value.ScopeID))
	}

	for _, classID := range []model.ClassID{value.SourceClassID, value.DestinationClassID} {
		resource := model.Resource{Type: model.ResourceClass, ID: classID.String()}
		if err := s.authorize(ctx, invocation, model.ActionAcademicProgressionManage, resource); err != nil {
			return err
		}

		if mutation {
			if err := s.authorize(ctx, invocation, model.ActionClassMembersManage, resource); err != nil {
				return err
			}
		}
	}

	return nil
}

func onboardingImportView(value *store.OnboardingImport) OnboardingImportView {
	if value == nil {
		return OnboardingImportView{}
	}
	return OnboardingImportView{
		ID:                  value.ID,
		Mode:                value.Mode,
		State:               value.State,
		ScopeType:           value.ScopeType,
		ScopeID:             value.ScopeID,
		RoleID:              value.RoleID,
		SourcePeriodID:      value.SourcePeriodID,
		SourceClassID:       value.SourceClassID,
		DestinationPeriodID: value.DestinationPeriodID,
		DestinationClassID:  value.DestinationClassID,
		EffectiveAt:         value.EffectiveAt,
		PreviewDigest:       value.PreviewDigest,
		IgnoredHeaders:      append([]string(nil), value.IgnoredHeaders...),
		TotalRows:           value.TotalRows,
		ValidRows:           value.ValidRows,
		InvalidRows:         value.InvalidRows,
		SucceededRows:       value.SucceededRows,
		NoOpRows:            value.NoOpRows,
		FailedRows:          value.FailedRows,
		SkippedRows:         value.SkippedRows,
		CommitPolicy:        value.CommitPolicy,
		ParseJobID:          value.ParseJobID,
		ExecutionJobID:      value.ExecutionJobID,
		CreatedAt:           value.CreatedAt,
		UpdatedAt:           value.UpdatedAt,
		ExpiresAt:           value.ExpiresAt,
		Revision:            value.Revision,
		FailureCode:         value.FailureCode,
		FileName:            value.FileName,
	}
}

func safeOnboardingImportRow(row store.OnboardingImportRow) OnboardingImportRowView {
	return OnboardingImportRowView{
		RowNumber:    row.RowNumber,
		Reference:    row.Reference,
		Operation:    row.Operation,
		Status:       row.Status,
		PreviewCode:  row.PreviewCode,
		PublicCode:   row.PublicCode,
		InvitationID: row.InvitationID,
		ResourceID:   row.ResourceID,
	}
}

func onboardingImportResource(scope model.RoleScopeType, id string) model.Resource {
	resourceType := model.ResourceInstitution
	if scope == model.RoleScopeAcademicUnit {
		resourceType = model.ResourceAcademicUnit
	}
	if scope == model.RoleScopeClass {
		resourceType = model.ResourceClass
	}
	return model.Resource{Type: resourceType, ID: id}
}

var onboardingImportStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Conflict:    "onboarding_import.conflict",
	Invalid:     "request.invalid",
	Unavailable: "onboarding_import.unavailable",
}
