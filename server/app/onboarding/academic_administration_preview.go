// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// An academic administration filter preview applies one academic
// administration operation to the Users a User directory filter matches.
// Creating it records only the request; a preview Job then resolves the
// filter under the caller's visibility at that moment and freezes the
// ordered User list, classifying each row. The caller reads the summary and
// pages the rows of each group, and commits the exact preview, digest and
// revision within 15 minutes of it becoming ready. One execution Job then
// runs the frozen list, never the filter again, and every row is authorized
// again when it executes.

// academicAdministrationFilterQueryMaximumRunes bounds the directory search
// term a filter preview keeps.
const academicAdministrationFilterQueryMaximumRunes = 256

// AcademicAdministrationPreviewOperations are the batch operations a filter
// preview applies. class.transfer is not one: a transfer names the exact
// source membership it replaces, which a filter cannot choose; Student
// Progression moves a Class's students instead.
func AcademicAdministrationPreviewOperations() []AcademicAdministrationBatchOperation {
	return []AcademicAdministrationBatchOperation{
		AcademicAdministrationAffiliationAdd,
		AcademicAdministrationAffiliationEnd,
		AcademicAdministrationAcademicUnitMemberAdd,
		AcademicAdministrationAcademicUnitMemberEnd,
		AcademicAdministrationClassEnroll,
		AcademicAdministrationClassEnd,
		AcademicAdministrationRoleBindingCreate,
		AcademicAdministrationRoleBindingEnd,
		AcademicAdministrationUserEnable,
		AcademicAdministrationUserDisable,
		AcademicAdministrationUserSessionsRevoke,
	}
}

// AcademicAdministrationUserFilter is the User directory filter of a
// preview. Each field means what the User directory query field of the same
// name means, and the same validation and visibility rules admit it.
type AcademicAdministrationUserFilter struct {
	Query            string
	Affiliations     []model.AffiliationKind
	AcademicUnitID   model.AcademicUnitID
	RoleIDs          []model.RoleID
	State            user.AccountState
	NeverSignedIn    bool
	LastActiveAfter  time.Time
	LastActiveBefore time.Time
}

type CreateAcademicAdministrationPreviewCommand struct {
	Operation AcademicAdministrationBatchOperation
	ScopeType model.RoleScopeType
	ScopeID   string
	Filter    AcademicAdministrationUserFilter
	// RoleID is the Role a role_binding operation applies, and
	// AffiliationKind the kind an affiliation operation applies.
	RoleID          model.RoleID
	AffiliationKind model.AffiliationKind
	// StartAt and EndAt bound the relationship a creating operation makes.
	StartAt model.OptionalTime
	EndAt   model.OptionalTime
}

type CommitAcademicAdministrationPreviewCommand struct {
	ID               model.OnboardingImportID
	ExpectedRevision int64
	PreviewDigest    string
	IdempotencyKey   string
}

type ListAcademicAdministrationPreviewRowsQuery struct {
	ID             model.OnboardingImportID
	Class          store.AcademicAdministrationPreviewClass
	Reason         string
	AfterRowNumber int
	Limit          int
}

// AcademicAdministrationPreviewView is what the preview's creator reads of
// it: the import projection, the operation and filter it was asked for,
// when the ready preview stops admitting a commit, and its row counts by
// class and skip reason.
type AcademicAdministrationPreviewView struct {
	Import           OnboardingImportView
	Filter           store.AcademicAdministrationFilter
	PreviewExpiresAt model.OptionalTime
	Groups           []store.AcademicAdministrationPreviewGroup
}

// requireAcademicAdministrationPreviews refuses a filter preview use case on
// a Service built without onboarding imports, the User directory, or the
// audit drafts the batch entry needs.
func (s *Service) requireAcademicAdministrationPreviews() error {
	if err := s.requireImports(); err != nil {
		return err
	}
	if s.directory == nil || s.drafts == nil {
		return apperror.New("onboarding_import.unavailable")
	}
	return nil
}

// CreateAcademicAdministrationPreview proposes the preview of applying one
// academic administration operation to every User the directory filter
// matches. It requires a strong, recent Session, refuses a filter the
// caller's directory would refuse, and queues the Job that resolves it.
func (s *Service) CreateAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateAcademicAdministrationPreviewCommand,
) (AcademicAdministrationPreviewView, error) {
	command.ScopeID = strings.TrimSpace(command.ScopeID)
	filter, err := academicAdministrationPreviewFilter(command)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err = s.requireAcademicAdministrationPreviews(); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err = access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

	resource, err := academicAdministrationBatchResource(command.ScopeType, command.ScopeID)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err = s.authorize(ctx, inv, model.ActionOnboardingBatchManage, resource); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err = s.validatePrincipal(ctx, inv.Principal()); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err = s.authorizeAcademicAdministrationPreviewTarget(ctx, inv, filter, command.ScopeType, command.ScopeID, resource); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	// The directory refuses, with its own field codes, a filter this caller
	// may not apply (an activity filter or Role filter without the
	// Institution-wide visibility it needs), so the preview admits exactly
	// the filters the directory admits.
	if _, err = s.directory.SearchUserPage(ctx, inv, academicAdministrationDirectoryQuery(filter, 1)); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

	at := model.TimeUTC(s.now())
	created, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionOnboardingBatchManage,
			Resource:   resource,
			ScopeType:  command.ScopeType,
			ScopeID:    command.ScopeID,
			Operation:  "academic_administration_preview.create",
			Value:      academicAdministrationPreviewAuditValue("", filter, command.ScopeType, command.ScopeID),
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*store.OnboardingImport, error) {
			id := model.NewOnboardingImportID()
			job, jobErr := newOnboardingImportJob(model.JobTypeAcademicAdministrationPreviewResolve, id, at)
			if jobErr != nil {
				return nil, jobErr
			}

			return s.imports.CreateOnboardingImport(ctx, &store.OnboardingImportCreation{
				Import: &store.OnboardingImport{
					ID:          id,
					Mode:        model.OnboardingImportAcademicAdministrationFilter,
					State:       model.OnboardingImportParsing,
					ScopeType:   command.ScopeType,
					ScopeID:     command.ScopeID,
					ActorUserID: inv.Principal().UserID,
					Principal:   inv.Principal(),
					ParseJobID:  job.ID,
					CreatedAt:   at,
					UpdatedAt:   at,
					ExpiresAt:   at.Add(onboardingImportRetention),
					Revision:    1,
					Filter:      &filter,
				},
				ParseJob:     job,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		academicAdministrationPreviewStoreErrors.Translate,
	)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if s.wake != nil {
		s.wake()
	}

	return academicAdministrationPreviewView(created, nil), nil
}

// GetAcademicAdministrationPreview reads the creator's own filter preview
// with its row counts by class and skip reason.
func (s *Service) GetAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (AcademicAdministrationPreviewView, error) {
	value, err := s.readAcademicAdministrationPreview(ctx, inv, id)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

	var groups []store.AcademicAdministrationPreviewGroup
	if value.PreviewDigest != "" {
		if groups, err = s.imports.CountAcademicAdministrationPreviewGroups(ctx, id); err != nil {
			return AcademicAdministrationPreviewView{}, academicAdministrationPreviewStoreErrors.Translate(err)
		}
	}

	return academicAdministrationPreviewView(value, groups), nil
}

// ListAcademicAdministrationPreviewRows pages the rows of one class of the
// creator's own filter preview, showing only the rows whose User the
// caller's User directory shows now: a viewer whose visibility narrowed
// since the preview froze no longer sees the Users it lost.
func (s *Service) ListAcademicAdministrationPreviewRows(
	ctx context.Context,
	inv invocation.Invocation,
	query ListAcademicAdministrationPreviewRowsQuery,
) (paging.Page[store.AcademicAdministrationPreviewRow], error) {
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, err
	}
	if !query.Class.IsValid() {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, apperror.Invalid("request.invalid", "group")
	}
	if query.Reason != "" && (query.Class != store.AcademicAdministrationPreviewSkipped ||
		!slices.Contains(AcademicAdministrationPreviewSkipReasons(), query.Reason)) {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, apperror.Invalid("request.invalid", "reason")
	}
	if query.AfterRowNumber < 0 {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, apperror.Invalid("request.invalid", "cursor")
	}

	if _, err = s.readAcademicAdministrationPreview(ctx, inv, query.ID); err != nil {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, err
	}
	visibility, err := s.authorizer.AuthorizeUserSearch(ctx, inv)
	if err != nil {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, err
	}

	rows, err := s.imports.ListAcademicAdministrationPreviewRows(ctx, store.AcademicAdministrationPreviewRowListOptions{
		ImportID:       query.ID,
		Class:          query.Class,
		Reason:         query.Reason,
		AfterRowNumber: query.AfterRowNumber,
		Limit:          probe,
		Visibility:     visibility,
	})
	if err != nil {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, academicAdministrationPreviewStoreErrors.Translate(err)
	}

	return paging.New(rows, limit), nil
}

// CommitAcademicAdministrationPreview commits the creator's ready filter
// preview whose digest and revision still match, within 15 minutes of it
// becoming ready, and proposes the one execution Job over its frozen rows.
// It requires a strong, recent Session, and its first commit records the
// batch-level audit entry.
func (s *Service) CommitAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	command CommitAcademicAdministrationPreviewCommand,
) (AcademicAdministrationPreviewView, error) {
	if err := s.requireAcademicAdministrationPreviews(); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

	view, err := s.commitImport(
		ctx,
		inv,
		CommitOnboardingImportCommand{
			ID:               command.ID,
			ExpectedRevision: command.ExpectedRevision,
			PreviewDigest:    command.PreviewDigest,
			Policy:           model.OnboardingImportValidRowsOnly,
			IdempotencyKey:   command.IdempotencyKey,
		},
		model.OnboardingImportAcademicAdministrationFilter,
	)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

	return s.GetAcademicAdministrationPreview(ctx, inv, view.ID)
}

// CancelAcademicAdministrationPreview cancels the creator's unfinished
// filter preview; rows already executed stay as they ended.
func (s *Service) CancelAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (AcademicAdministrationPreviewView, error) {
	if _, err := s.readAcademicAdministrationPreview(ctx, inv, id); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if _, err := s.cancelImport(ctx, inv, id, model.OnboardingImportAcademicAdministrationFilter); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	return s.GetAcademicAdministrationPreview(ctx, inv, id)
}

// readAcademicAdministrationPreview reads a filter preview the caller may
// read: onboarding_batch.view at its scope, and only its creator, so the
// Users it froze are never shown to a second reader. Any other caller reads
// it as absent.
func (s *Service) readAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (*store.OnboardingImport, error) {
	if err := s.requireAcademicAdministrationPreviews(); err != nil {
		return nil, err
	}
	if !id.IsValid() {
		return nil, apperror.New("request.invalid")
	}

	value, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return nil, academicAdministrationPreviewStoreErrors.Translate(err)
	}
	if err = s.authorizeImport(ctx, inv, value, false); err != nil {
		return nil, err
	}
	if err = requireImportMode(value, model.OnboardingImportAcademicAdministrationFilter); err != nil {
		return nil, err
	}
	if value.ActorUserID != inv.Principal().UserID {
		return nil, apperror.New("resource.not_found").WithField("resource", "academic_administration_preview")
	}

	return value, nil
}

// authorizeAcademicAdministrationPreviewTarget authorizes the operation's
// own action at the scope and, for a Role Binding operation, checks the
// Role and that the caller may delegate every Action a created binding
// grants. It is checked when the preview is asked for and again when it
// resolves; every row is authorized again when it executes.
func (s *Service) authorizeAcademicAdministrationPreviewTarget(
	ctx context.Context,
	inv invocation.Invocation,
	filter store.AcademicAdministrationFilter,
	scopeType model.RoleScopeType,
	scopeID string,
	resource model.Resource,
) error {
	operation := AcademicAdministrationBatchOperation(filter.Operation)
	authority := academicAdministrationBatchAuthority(inv, RunAcademicAdministrationBatchCommand{Operation: operation})
	if err := s.authorizeAcademicAdministrationAction(ctx, inv, authority.Actions[1], scopeType, scopeID, resource); err != nil {
		return err
	}
	if _, _, err := s.academicAdministrationImportScope(ctx, scopeType, scopeID); err != nil {
		return err
	}
	if operation != AcademicAdministrationRoleBindingCreate && operation != AcademicAdministrationRoleBindingEnd {
		return nil
	}

	role, err := s.roles.Get(ctx, filter.RoleID)
	if err != nil {
		return academicAdministrationPreviewStoreErrors.Translate(err)
	}
	if role.IsArchived() {
		return apperror.Invalid("request.invalid", "role_id")
	}
	if operation == AcademicAdministrationRoleBindingCreate {
		return s.authorizeActionDelegation(ctx, inv, role.Permissions, scopeType, scopeID)
	}
	return nil
}

// authorizeAcademicAdministrationAction authorizes the action an operation's
// rows need at the preview's scope. An action on a User (user.manage,
// session.manage) is never checked against the scope resource itself: the
// operations that need it apply Institution-wide, so the caller must hold it
// Institution-wide, recorded as one decision at the Institution.
func (s *Service) authorizeAcademicAdministrationAction(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	scopeType model.RoleScopeType,
	scopeID string,
	resource model.Resource,
) error {
	definition, known := model.DefinitionForAction(action)
	if !known {
		return apperror.New("request.invalid")
	}
	if definition.AcceptsResource(resource.Type) {
		return s.authorize(ctx, inv, action, resource)
	}
	if s.authorizer == nil || scopeType != model.RoleScopeInstitution {
		return access.DeniedError()
	}

	constraint, err := s.authorizer.GrantedScopes(ctx, inv.Principal(), action, definition.ResourceType)
	if err != nil {
		return err
	}
	if err = s.authorizer.RecordAuthorizationDecision(
		ctx,
		inv.Principal(),
		action,
		resource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		constraint.InstitutionWide,
	); err != nil {
		return err
	}
	if !constraint.InstitutionWide {
		return access.DeniedError()
	}
	return nil
}

// academicAdministrationPreviewFilter validates a preview request and
// returns the filter the preview keeps: a supported operation at a scope it
// applies at, exactly the operation fields it owns, and a well-formed
// bounded directory filter. Whether the caller may apply the directory
// filter is the directory's own decision.
func academicAdministrationPreviewFilter(command CreateAcademicAdministrationPreviewCommand) (store.AcademicAdministrationFilter, error) {
	operation := command.Operation
	if !slices.Contains(AcademicAdministrationPreviewOperations(), operation) {
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "operation")
	}
	if !model.IsValidOpaqueID(command.ScopeID) || !academicAdministrationBatchScopeMatches(operation, command.ScopeType) {
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "scope_id")
	}

	creates := operation == AcademicAdministrationAffiliationAdd || operation == AcademicAdministrationAcademicUnitMemberAdd ||
		operation == AcademicAdministrationClassEnroll || operation == AcademicAdministrationRoleBindingCreate
	ends := creates && operation != AcademicAdministrationAcademicUnitMemberAdd
	affiliation := operation == AcademicAdministrationAffiliationAdd || operation == AcademicAdministrationAffiliationEnd
	roleBinding := operation == AcademicAdministrationRoleBindingCreate || operation == AcademicAdministrationRoleBindingEnd
	switch {
	case command.StartAt.Valid && !creates:
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "start_at")
	case command.EndAt.Valid && (!ends || (command.StartAt.Valid && !command.EndAt.Time.After(command.StartAt.Time))):
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "end_at")
	case affiliation != command.AffiliationKind.IsValid() || (!affiliation && command.AffiliationKind != ""):
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "affiliation_kind")
	case roleBinding != command.RoleID.IsValid() || (!roleBinding && command.RoleID != ""):
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "role_id")
	}

	users := command.Filter
	if !utf8.ValidString(users.Query) || utf8.RuneCountInString(users.Query) > academicAdministrationFilterQueryMaximumRunes {
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "q")
	}
	if len(users.Affiliations) > 4 {
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "affiliation")
	}
	affiliations := []model.AffiliationKind{}
	for _, kind := range users.Affiliations {
		if !kind.IsValid() {
			return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "affiliation")
		}
		if !slices.Contains(affiliations, kind) {
			affiliations = append(affiliations, kind)
		}
	}
	roleIDs := []model.RoleID{}
	for _, id := range users.RoleIDs {
		if !id.IsValid() {
			return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "role_id")
		}
		if !slices.Contains(roleIDs, id) {
			roleIDs = append(roleIDs, id)
		}
	}
	if len(roleIDs) > store.MaximumUserListRoles {
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "role_id")
	}
	switch {
	case users.AcademicUnitID != "" && !users.AcademicUnitID.IsValid():
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "academic_unit_id")
	case users.State != "" && !users.State.IsValid():
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "state")
	case users.NeverSignedIn && (!users.LastActiveAfter.IsZero() || !users.LastActiveBefore.IsZero()):
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "never_signed_in")
	case !users.LastActiveAfter.IsZero() && !users.LastActiveBefore.IsZero() && !users.LastActiveAfter.Before(users.LastActiveBefore):
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "last_active_before")
	}

	filter := store.AcademicAdministrationFilter{
		Operation:        string(operation),
		Query:            users.Query,
		AcademicUnitID:   users.AcademicUnitID,
		State:            string(users.State),
		NeverSignedIn:    users.NeverSignedIn,
		LastActiveAfter:  model.TimeUTC(users.LastActiveAfter),
		LastActiveBefore: model.TimeUTC(users.LastActiveBefore),
		RoleID:           command.RoleID,
		AffiliationKind:  command.AffiliationKind,
		StartAt:          model.OptionalTimeFrom(command.StartAt.Time),
		EndAt:            model.OptionalTimeFrom(command.EndAt.Time),
	}
	if !command.StartAt.Valid {
		filter.StartAt = model.OptionalTime{}
	}
	if !command.EndAt.Valid {
		filter.EndAt = model.OptionalTime{}
	}
	if users.LastActiveAfter.IsZero() {
		filter.LastActiveAfter = time.Time{}
	}
	if users.LastActiveBefore.IsZero() {
		filter.LastActiveBefore = time.Time{}
	}
	if len(affiliations) > 0 {
		filter.Affiliations = affiliations
	}
	if len(roleIDs) > 0 {
		filter.RoleIDs = roleIDs
	}

	return filter, nil
}

// academicAdministrationDirectoryQuery is the User directory query of a
// preview filter: one page of up to limit Users in username order, the
// order a preview freezes its rows in.
func academicAdministrationDirectoryQuery(filter store.AcademicAdministrationFilter, limit int) user.SearchUsersQuery {
	return user.SearchUsersQuery{
		Query:            filter.Query,
		Order:            store.UserOrderUsername,
		Limit:            limit,
		State:            user.AccountState(filter.State),
		Affiliations:     slices.Clone(filter.Affiliations),
		AcademicUnitID:   filter.AcademicUnitID,
		RoleIDs:          slices.Clone(filter.RoleIDs),
		NeverSignedIn:    filter.NeverSignedIn,
		LastActiveAfter:  filter.LastActiveAfter,
		LastActiveBefore: filter.LastActiveBefore,
	}
}

// academicAdministrationPreviewAuditValue is the safe audit projection of a
// preview: its operation, scope and filter. The directory search term is
// recorded only as present or not, since it may spell a person's name.
func academicAdministrationPreviewAuditValue(
	id model.OnboardingImportID,
	filter store.AcademicAdministrationFilter,
	scopeType model.RoleScopeType,
	scopeID string,
) map[string]any {
	users := map[string]any{"q_present": filter.Query != ""}
	if len(filter.Affiliations) > 0 {
		users["affiliation"] = filter.Affiliations
	}
	if filter.AcademicUnitID != "" {
		users["academic_unit_id"] = filter.AcademicUnitID
	}
	if len(filter.RoleIDs) > 0 {
		users["role_id"] = filter.RoleIDs
	}
	if filter.State != "" {
		users["state"] = filter.State
	}
	if filter.NeverSignedIn {
		users["never_signed_in"] = true
	}
	if !filter.LastActiveAfter.IsZero() {
		users["last_active_after"] = model.FormatRFC3339(filter.LastActiveAfter)
	}
	if !filter.LastActiveBefore.IsZero() {
		users["last_active_before"] = model.FormatRFC3339(filter.LastActiveBefore)
	}

	value := map[string]any{
		"operation":  filter.Operation,
		"scope_type": scopeType,
		"scope_id":   scopeID,
		"filter":     users,
	}
	if id != "" {
		value["onboarding_import_id"] = id
	}
	if filter.RoleID != "" {
		value["role_id"] = filter.RoleID
	}
	if filter.AffiliationKind != "" {
		value["affiliation_kind"] = filter.AffiliationKind
	}
	return value
}

// prepareAcademicAdministrationBatchAudit drafts the one batch-level audit
// entry of a filter preview's commit: the operation, the filter, the frozen
// counts by class and reason, and the preview's identity. The commit
// aggregate inserts it once, on the first commit only.
func (s *Service) prepareAcademicAdministrationBatchAudit(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
) (*model.AuditEvent, error) {
	if current == nil || current.Filter == nil || s.drafts == nil {
		return nil, apperror.New("onboarding_import.unavailable")
	}
	groups, err := s.imports.CountAcademicAdministrationPreviewGroups(ctx, current.ID)
	if err != nil {
		return nil, academicAdministrationPreviewStoreErrors.Translate(err)
	}

	value := academicAdministrationPreviewAuditValue(current.ID, *current.Filter, current.ScopeType, current.ScopeID)
	counts := map[string]any{"total_rows": current.TotalRows}
	skipped := map[string]int{}
	for _, group := range groups {
		if group.Class == store.AcademicAdministrationPreviewSkipped {
			skipped[group.Reason] += group.Rows
			continue
		}
		counts[string(group.Class)] = group.Rows
	}
	counts["skipped"] = skipped
	value["counts"] = counts

	resource, err := academicAdministrationBatchResource(current.ScopeType, current.ScopeID)
	if err != nil {
		return nil, err
	}
	return s.drafts.PrepareAtScope(
		ctx,
		inv,
		model.ActionOnboardingBatchManage,
		resource,
		current.ScopeType,
		current.ScopeID,
		"academic_administration_preview.batch",
		value,
	)
}

func academicAdministrationPreviewView(
	value *store.OnboardingImport,
	groups []store.AcademicAdministrationPreviewGroup,
) AcademicAdministrationPreviewView {
	view := AcademicAdministrationPreviewView{Import: onboardingImportView(value), Groups: groups}
	if value != nil {
		view.PreviewExpiresAt = value.PreviewExpiresAt
		if value.Filter != nil {
			view.Filter = *value.Filter
		}
	}
	return view
}

// academicAdministrationPreviewStoreErrors presents the Store failures of a
// filter preview: an expired preview, and any other stale preview, state or
// fence as a preview conflict.
var academicAdministrationPreviewStoreErrors = apperror.StoreTable{
	Rules: apperror.Conflicts("onboarding_import", map[string]apperror.Code{
		"preview_expired": "academic_administration_preview.expired",
	}),
	NotFound:    "resource.not_found",
	Conflict:    "academic_administration_preview.conflict",
	Invalid:     "request.invalid",
	Unavailable: "onboarding_import.unavailable",
}
