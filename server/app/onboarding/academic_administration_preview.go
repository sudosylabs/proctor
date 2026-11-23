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

// A filter preview freezes the users a directory filter matched when it
// resolved. Execution runs that frozen list, never the filter again, and
// authorizes every row again as it executes.

const academicAdministrationFilterQueryMaximumRunes = 256

// AcademicAdministrationPreviewOperations are the batch operations a filter
// preview may apply. class.transfer is excluded: a transfer names the exact
// source membership it replaces, which a filter cannot choose.
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

// AcademicAdministrationUserFilter is a preview's user directory filter; each
// field means and validates exactly as the directory query field of that name.
type AcademicAdministrationUserFilter struct {
	Query            string
	Affiliations     []model.AffiliationKind
	AcademicUnitID   model.AcademicUnitID
	RoleIDs          []model.RoleID
	State            user.AccountState
	MFA              store.UserMFAFilter
	NeverSignedIn    bool
	LastActiveAfter  time.Time
	LastActiveBefore time.Time
}

// CreateAcademicAdministrationPreviewCommand describes a filter preview.
// Reason is accepted only for user.disable, with the single disable's limits,
// and reaches every disabled person's audit entry and the batch's.
type CreateAcademicAdministrationPreviewCommand struct {
	Operation       AcademicAdministrationBatchOperation
	ScopeType       model.RoleScopeType
	ScopeID         string
	Filter          AcademicAdministrationUserFilter
	RoleID          model.RoleID
	AffiliationKind model.AffiliationKind
	StartAt         model.OptionalTime
	EndAt           model.OptionalTime
	Reason          *string
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

// AcademicAdministrationPreviewView is the creator's view of a filter
// preview, with row counts by class and skip reason.
type AcademicAdministrationPreviewView struct {
	Import           OnboardingImportView
	Filter           store.AcademicAdministrationFilter
	PreviewExpiresAt model.OptionalTime
	Groups           []store.AcademicAdministrationPreviewGroup
}

func (s *Service) requireAcademicAdministrationPreviews() error {
	if err := s.requireImports(); err != nil {
		return err
	}
	if s.directory == nil {
		return apperror.New("onboarding_import.unavailable")
	}
	return nil
}

// CreateAcademicAdministrationPreview records a filter preview request and
// queues the job that resolves it; it requires a strong, recent session.
func (s *Service) CreateAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateAcademicAdministrationPreviewCommand,
) (AcademicAdministrationPreviewView, error) {
	command.ScopeID = strings.TrimSpace(command.ScopeID)
	at := model.TimeUTC(s.now())
	filter, err := academicAdministrationPreviewFilter(command, at)
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
	// Let the directory refuse, with its own field codes, a filter this
	// caller may not apply, so a preview admits exactly what it admits.
	if _, err = s.directory.SearchUserPage(ctx, inv, academicAdministrationDirectoryQuery(filter, 1)); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

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

// GetAcademicAdministrationPreview reads the creator's own filter preview.
// Counts include only rows whose user the creator can still see.
func (s *Service) GetAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (AcademicAdministrationPreviewView, error) {
	value, err := s.readAcademicAdministrationPreview(ctx, inv, id)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err = s.admitAcademicAdministrationFilter(ctx, inv, value); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

	groups, err := s.countVisibleAcademicAdministrationPreviewGroups(ctx, inv, value)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	return academicAdministrationPreviewView(value, groups), nil
}

// ListAcademicAdministrationPreviewRows pages one class of rows of the
// creator's own preview, hiding users the creator can no longer see since
// the preview froze.
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

	current, err := s.readAcademicAdministrationPreview(ctx, inv, query.ID)
	if err != nil {
		return paging.Page[store.AcademicAdministrationPreviewRow]{}, err
	}
	if err = s.admitAcademicAdministrationFilter(ctx, inv, current); err != nil {
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

// CommitAcademicAdministrationPreview commits a ready preview whose digest
// and revision still match, within 15 minutes of it becoming ready. Another
// user's preview reads as absent; an idempotent replay returns the recorded
// snapshot.
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

	current, err := s.readAcademicAdministrationPreview(ctx, inv, command.ID)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	if err = s.admitAcademicAdministrationFilter(ctx, inv, current); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}

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
		model.OnboardingImportAcademicAdministrationFilter,
	)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	return s.academicAdministrationPreviewResult(ctx, inv, committed), nil
}

// CancelAcademicAdministrationPreview cancels an unfinished preview; rows
// already executed are not undone.
func (s *Service) CancelAcademicAdministrationPreview(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
) (AcademicAdministrationPreviewView, error) {
	if _, err := s.readAcademicAdministrationPreview(ctx, inv, id); err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	canceled, err := s.cancelImport(ctx, inv, id, model.OnboardingImportAcademicAdministrationFilter)
	if err != nil {
		return AcademicAdministrationPreviewView{}, err
	}
	return s.academicAdministrationPreviewResult(ctx, inv, canceled), nil
}

// academicAdministrationPreviewResult counts rows best effort: the change
// already happened, so a failed count omits the counts rather than turning
// the change into an error.
func (s *Service) academicAdministrationPreviewResult(
	ctx context.Context,
	inv invocation.Invocation,
	value *store.OnboardingImport,
) AcademicAdministrationPreviewView {
	var groups []store.AcademicAdministrationPreviewGroup
	if s.admitAcademicAdministrationFilter(ctx, inv, value) == nil {
		if counted, err := s.countVisibleAcademicAdministrationPreviewGroups(ctx, inv, value); err == nil {
			groups = counted
		}
	}
	return academicAdministrationPreviewView(value, groups)
}

// countVisibleAcademicAdministrationPreviewGroups counts through the
// caller's current visibility, the same visibility rows are read through.
func (s *Service) countVisibleAcademicAdministrationPreviewGroups(
	ctx context.Context,
	inv invocation.Invocation,
	value *store.OnboardingImport,
) ([]store.AcademicAdministrationPreviewGroup, error) {
	if value.PreviewDigest == "" {
		return nil, nil
	}
	visibility, err := s.authorizer.AuthorizeUserSearch(ctx, inv)
	if err != nil {
		return nil, err
	}
	groups, err := s.imports.CountAcademicAdministrationPreviewGroups(ctx, store.AcademicAdministrationPreviewGroupCountOptions{
		ImportID:   value.ID,
		Visibility: &visibility,
	})
	if err != nil {
		return nil, academicAdministrationPreviewStoreErrors.Translate(err)
	}
	return groups, nil
}

// admitAcademicAdministrationFilter re-asks the directory whether the caller
// may still apply the stored filter. A creator who lost the authority since
// (institution-wide visibility for an activity filter, role.view for a role
// filter) is refused with academic_administration_preview.authorization_lost.
func (s *Service) admitAcademicAdministrationFilter(
	ctx context.Context,
	inv invocation.Invocation,
	value *store.OnboardingImport,
) error {
	if value == nil || value.Filter == nil {
		return apperror.New("onboarding_import.unavailable")
	}
	if _, err := s.directory.SearchUserPage(ctx, inv, academicAdministrationDirectoryQuery(*value.Filter, 1)); err != nil {
		if academicAdministrationFilterAuthorizationLost(err) {
			return apperror.New("academic_administration_preview.authorization_lost").Wrap(err)
		}
		return err
	}
	return nil
}

// readAcademicAdministrationPreview admits only the creator, so the users a
// preview froze are never shown to a second reader; anyone else reads it as
// absent.
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

// authorizeAcademicAdministrationPreviewTarget runs when the preview is
// requested and again when it resolves. For a role binding operation the
// caller must be able to delegate every action the binding grants.
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

// authorizeAcademicAdministrationAction checks user actions (user.manage,
// session.manage) at the institution, never at the scope: the operations
// that need them apply institution-wide.
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

// academicAdministrationPreviewFilter validates the request shape only; an
// omitted start becomes createdAt for every row. Whether the caller may
// apply the directory filter is the directory's decision.
func academicAdministrationPreviewFilter(
	command CreateAcademicAdministrationPreviewCommand,
	createdAt time.Time,
) (store.AcademicAdministrationFilter, error) {
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
	effectiveStart := createdAt
	if command.StartAt.Valid {
		effectiveStart = command.StartAt.Time
	}
	switch {
	case command.StartAt.Valid && !creates:
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "start_at")
	case command.EndAt.Valid && (!ends || !command.EndAt.Time.After(effectiveStart)):
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
	case users.MFA != "" && !users.MFA.IsValid():
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "mfa")
	case users.NeverSignedIn && (!users.LastActiveAfter.IsZero() || !users.LastActiveBefore.IsZero()):
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "never_signed_in")
	case !users.LastActiveAfter.IsZero() && !users.LastActiveBefore.IsZero() && !users.LastActiveAfter.Before(users.LastActiveBefore):
		return store.AcademicAdministrationFilter{}, apperror.Invalid("request.invalid", "last_active_before")
	}

	reason, err := academicAdministrationBatchReason(operation, command.Reason)
	if err != nil {
		return store.AcademicAdministrationFilter{}, err
	}

	filter := store.AcademicAdministrationFilter{
		Operation:        string(operation),
		Query:            users.Query,
		AcademicUnitID:   users.AcademicUnitID,
		State:            string(users.State),
		MFA:              users.MFA,
		NeverSignedIn:    users.NeverSignedIn,
		Reason:           reason,
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

// academicAdministrationDirectoryQuery orders by username, the order a
// preview freezes its rows in.
func academicAdministrationDirectoryQuery(filter store.AcademicAdministrationFilter, limit int) user.SearchUsersQuery {
	return user.SearchUsersQuery{
		Query:            filter.Query,
		Order:            store.UserOrderUsername,
		Limit:            limit,
		State:            user.AccountState(filter.State),
		MFA:              filter.MFA,
		Affiliations:     slices.Clone(filter.Affiliations),
		AcademicUnitID:   filter.AcademicUnitID,
		RoleIDs:          slices.Clone(filter.RoleIDs),
		NeverSignedIn:    filter.NeverSignedIn,
		LastActiveAfter:  filter.LastActiveAfter,
		LastActiveBefore: filter.LastActiveBefore,
	}
}

// academicAdministrationPreviewAuditValue records the search term only as
// present or not, since it may spell a person's name.
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
	if filter.MFA != "" {
		users["mfa"] = filter.MFA
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
	if filter.Reason != "" {
		value["reason"] = filter.Reason
	}
	return value
}

// prepareAcademicAdministrationBatchAudit drafts the batch-level audit entry;
// the store inserts it on the first commit only.
func (s *Service) prepareAcademicAdministrationBatchAudit(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
) (*model.AuditEvent, error) {
	if current == nil || current.Filter == nil {
		return nil, apperror.New("onboarding_import.unavailable")
	}
	groups, err := s.imports.CountAcademicAdministrationPreviewGroups(ctx, store.AcademicAdministrationPreviewGroupCountOptions{ImportID: current.ID})
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
	return s.auditor.PrepareAtScope(
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

// academicAdministrationPreviewStoreErrors maps every stale preview, state or
// fence failure other than expiry to a preview conflict.
var academicAdministrationPreviewStoreErrors = apperror.StoreTable{
	Rules: apperror.Conflicts("onboarding_import", map[string]apperror.Code{
		"preview_expired": "academic_administration_preview.expired",
	}),
	NotFound:    "resource.not_found",
	Conflict:    "academic_administration_preview.conflict",
	Invalid:     "request.invalid",
	Unavailable: "onboarding_import.unavailable",
}
