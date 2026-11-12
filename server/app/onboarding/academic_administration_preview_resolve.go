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
	"strconv"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The skip reasons of a filter preview row. A skipped row is never executed.
const (
	// academicAdministrationPreviewSelf skips the caller's own account,
	// whatever the operation.
	academicAdministrationPreviewSelf = "academic_administration_preview.self"
	// academicAdministrationPreviewSystemAdministrator skips a System
	// Administrator from a disable or Session revocation.
	academicAdministrationPreviewSystemAdministrator = "academic_administration_preview.system_administrator"
	// academicAdministrationPreviewEnrollmentConflict skips a Class
	// enrollment of a User enrolled in another Class of the same Period,
	// which an enrollment would silently turn into a transfer.
	academicAdministrationPreviewEnrollmentConflict = "academic_administration_preview.enrollment_conflict"
	// academicAdministrationPreviewStudentAffiliationRequired skips a Class
	// enrollment of a User without a current student Affiliation.
	academicAdministrationPreviewStudentAffiliationRequired = "academic_administration_preview.student_affiliation_required"
	// academicAdministrationPreviewAmbiguous skips an end when the User
	// holds several current relationships it could end.
	academicAdministrationPreviewAmbiguous = "academic_administration_preview.ambiguous"
)

// AcademicAdministrationPreviewSkipReasons is the closed catalogue of the
// reasons a filter preview skips a row.
func AcademicAdministrationPreviewSkipReasons() []string {
	return []string{
		academicAdministrationPreviewAmbiguous,
		academicAdministrationPreviewEnrollmentConflict,
		academicAdministrationPreviewSelf,
		academicAdministrationPreviewStudentAffiliationRequired,
		academicAdministrationPreviewSystemAdministrator,
	}
}

// resolveAcademicAdministrationPreview resolves a filter preview: it
// authorizes the frozen caller again, reads every User the directory
// filter matches under that caller's visibility now, in username order,
// and freezes one classified row per User. More than
// AcademicAdministrationPreviewMaximumRows matching Users fails the preview,
// so the caller narrows the filter rather than acting on a truncated list.
func (s *Service) resolveAcademicAdministrationPreview(ctx context.Context, id model.OnboardingImportID) error {
	current, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return err
	}
	if current.Mode != model.OnboardingImportAcademicAdministrationFilter || current.Filter == nil {
		return store.NewErrConflict("academic_administration_preview", "mode", nil)
	}
	if current.State != model.OnboardingImportParsing {
		return nil
	}
	if err = s.requireAcademicAdministrationPreviews(); err != nil {
		return err
	}

	inv := invocation.New(current.Principal, model.RequestMetadata{})
	if err = s.validatePrincipal(ctx, current.Principal); err != nil {
		return err
	}
	resource, err := academicAdministrationBatchResource(current.ScopeType, current.ScopeID)
	if err != nil {
		return err
	}
	if err = s.authorize(ctx, inv, model.ActionOnboardingBatchManage, resource); err != nil {
		return err
	}
	if err = s.authorizeAcademicAdministrationPreviewTarget(ctx, inv, *current.Filter, current.ScopeType, current.ScopeID, resource); err != nil {
		if failure, ok := apperror.As(err); ok && (failure.Code() == "resource.not_found" || failure.Code() == "request.invalid") {
			return apperror.New("academic_administration_preview.target_conflict").Wrap(err)
		}
		return err
	}
	_, scopeRevision, err := s.academicAdministrationImportScope(ctx, current.ScopeType, current.ScopeID)
	if err != nil {
		return apperror.New("academic_administration_preview.target_conflict").Wrap(err)
	}

	users, err := s.matchAcademicAdministrationPreviewUsers(ctx, inv, *current.Filter)
	if err != nil {
		return err
	}

	classifier, err := s.newAcademicAdministrationPreviewClassifier(ctx, current, scopeRevision)
	if err != nil {
		return err
	}
	rows := make([]store.OnboardingImportRow, 0, len(users))
	for index, target := range users {
		row, rowErr := classifier.row(ctx, index+1, target)
		if rowErr != nil {
			return rowErr
		}
		rows = append(rows, row)
	}
	if err = requireOnboardingImportPreviewCodes(rows); err != nil {
		return err
	}

	document, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(document)
	_, err = s.imports.CompleteOnboardingImportPreview(ctx, &store.OnboardingImportPreviewCompletion{
		ID:               id,
		ExpectedRevision: current.Revision,
		Digest:           hex.EncodeToString(digest[:]),
		Rows:             rows,
		At:               model.TimeUTC(s.now()),
	})
	return err
}

// matchAcademicAdministrationPreviewUsers pages the User directory through
// the frozen caller's own visibility, which the directory resolves on every
// page, and stops one User past the preview bound.
func (s *Service) matchAcademicAdministrationPreviewUsers(
	ctx context.Context,
	inv invocation.Invocation,
	filter store.AcademicAdministrationFilter,
) ([]*model.User, error) {
	var users []*model.User
	query := academicAdministrationDirectoryQuery(filter, store.OnboardingImportPageSize)
	for {
		page, err := s.directory.SearchUserPage(ctx, inv, query)
		if err != nil {
			return nil, err
		}
		users = append(users, page.Items...)
		if len(users) > store.AcademicAdministrationPreviewMaximumRows {
			return nil, apperror.New("academic_administration_preview.too_many_users")
		}
		if !page.HasMore || len(page.Items) == 0 {
			return users, nil
		}
		last := page.Items[len(page.Items)-1]
		query.AfterUsername, query.AfterID = last.Username, last.ID
	}
}

// academicAdministrationPreviewClassifier classifies the Users of one
// filter preview at one instant into rows that will change, rows whose
// change already holds, and rows it skips with a reason.
type academicAdministrationPreviewClassifier struct {
	service       *Service
	current       *store.OnboardingImport
	filter        store.AcademicAdministrationFilter
	scopeRevision int64
	roleRevision  int64
	at            time.Time
	// systemAdministratorRoleID is the built-in System Administrator Role,
	// read only for a disable or Session revocation.
	systemAdministratorRoleID model.RoleID
	// period is the Academic Period of the Class a Class enrollment targets.
	period model.AcademicPeriodID
}

func (s *Service) newAcademicAdministrationPreviewClassifier(
	ctx context.Context,
	current *store.OnboardingImport,
	scopeRevision int64,
) (*academicAdministrationPreviewClassifier, error) {
	classifier := &academicAdministrationPreviewClassifier{
		service:       s,
		current:       current,
		filter:        *current.Filter,
		scopeRevision: scopeRevision,
		at:            model.TimeUTC(s.now()),
	}
	switch AcademicAdministrationBatchOperation(classifier.filter.Operation) {
	case AcademicAdministrationUserDisable, AcademicAdministrationUserSessionsRevoke:
		roleID, err := s.systemAdministratorRoleID(ctx)
		if err != nil {
			return nil, err
		}
		classifier.systemAdministratorRoleID = roleID
	case AcademicAdministrationClassEnroll:
		class, err := s.classes.Get(ctx, model.ClassID(current.ScopeID))
		if err != nil {
			return nil, apperror.New("academic_administration_preview.target_conflict").Wrap(err)
		}
		classifier.period = class.AcademicPeriodID
	case AcademicAdministrationRoleBindingCreate:
		role, err := s.roles.Get(ctx, classifier.filter.RoleID)
		if err != nil {
			return nil, apperror.New("academic_administration_preview.target_conflict").Wrap(err)
		}
		classifier.roleRevision = role.UpdatedAt.UnixMicro()
	}
	return classifier, nil
}

// systemAdministratorRoleID reads the built-in System Administrator Role.
func (s *Service) systemAdministratorRoleID(ctx context.Context) (model.RoleID, error) {
	role, err := s.roles.GetByName(ctx, model.SystemAdministratorRoleName)
	if err != nil {
		return "", apperror.New("onboarding_import.unavailable").Wrap(err)
	}
	if !role.BuiltIn {
		return "", apperror.New("onboarding_import.unavailable")
	}
	return role.ID, nil
}

// isSystemAdministrator reports whether the User holds a current
// Institution binding of the System Administrator Role at the instant.
func (s *Service) isSystemAdministrator(ctx context.Context, roleID model.RoleID, userID model.UserID, at time.Time) (bool, error) {
	bindings, err := s.roleBindings.ListActiveByUser(ctx, userID, at)
	if err != nil {
		return false, apperror.New("onboarding_import.unavailable").Wrap(err)
	}
	for _, binding := range bindings {
		if binding.RoleID == roleID && binding.ScopeType == model.RoleScopeInstitution {
			return true, nil
		}
	}
	return false, nil
}

// row builds the frozen row of the rowNumber-th matched User.
func (c *academicAdministrationPreviewClassifier) row(
	ctx context.Context,
	rowNumber int,
	target *model.User,
) (store.OnboardingImportRow, error) {
	row := store.OnboardingImportRow{
		ImportID:        c.current.ID,
		RowNumber:       rowNumber,
		Reference:       target.ID.String(),
		Operation:       c.filter.Operation,
		ScopeType:       c.current.ScopeType,
		ScopeID:         c.current.ScopeID,
		TargetRevision:  c.scopeRevision,
		UserID:          target.ID,
		AffiliationKind: c.filter.AffiliationKind,
		StartsAt:        c.filter.StartAt,
		EndsAt:          c.filter.EndAt,
		PreviewStatus:   model.OnboardingImportRowValid,
		Status:          model.OnboardingImportRowValid,
		UpdatedAt:       c.current.CreatedAt,
	}
	operation := AcademicAdministrationBatchOperation(c.filter.Operation)
	if operation == AcademicAdministrationRoleBindingCreate {
		row.RoleID, row.RoleRevision = c.filter.RoleID, c.roleRevision
	}
	creates := operation == AcademicAdministrationAffiliationAdd || operation == AcademicAdministrationAcademicUnitMemberAdd ||
		operation == AcademicAdministrationClassEnroll || operation == AcademicAdministrationRoleBindingCreate
	if creates && !row.StartsAt.Valid {
		row.StartsAt = model.OptionalTimeFrom(c.current.CreatedAt)
	}

	reason, relationshipID, err := c.classify(ctx, operation, target, row.StartsAt.Time)
	if err != nil {
		return store.OnboardingImportRow{}, err
	}
	row.RelationshipID = relationshipID
	if reason != "" {
		row.PreviewStatus, row.Status, row.PreviewCode = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid, reason
	}
	return row, nil
}

// classify returns the row's skip reason or the no-change marker, empty
// when the row will change, and the relationship an ending operation ends.
// The caller's own account is always skipped, and a System Administrator is
// skipped from a disable or Session revocation.
func (c *academicAdministrationPreviewClassifier) classify(
	ctx context.Context,
	operation AcademicAdministrationBatchOperation,
	target *model.User,
	startAt time.Time,
) (string, string, error) {
	s := c.service
	if target.ID == c.current.ActorUserID {
		return academicAdministrationPreviewSelf, "", nil
	}

	switch operation {
	case AcademicAdministrationUserDisable, AcademicAdministrationUserSessionsRevoke:
		administrator, err := s.isSystemAdministrator(ctx, c.systemAdministratorRoleID, target.ID, c.at)
		if err != nil || administrator {
			return academicAdministrationPreviewSystemAdministrator, "", err
		}
		if operation == AcademicAdministrationUserSessionsRevoke {
			return "", "", nil
		}
		return c.userEnabledChange(ctx, target.ID, false)
	case AcademicAdministrationUserEnable:
		return c.userEnabledChange(ctx, target.ID, true)
	case AcademicAdministrationAffiliationAdd, AcademicAdministrationAffiliationEnd:
		at := c.at
		if operation == AcademicAdministrationAffiliationAdd {
			at = startAt
		}
		affiliations, err := s.affiliations.ListActiveByUser(ctx, target.ID, at)
		if err != nil {
			return "", "", err
		}
		var matching []string
		for _, affiliation := range affiliations {
			if affiliation.Kind == c.filter.AffiliationKind {
				matching = append(matching, affiliation.ID.String())
			}
		}
		if operation == AcademicAdministrationAffiliationAdd {
			return noChangeWhen(len(matching) > 0), "", nil
		}
		return endingClassification(matching)
	case AcademicAdministrationAcademicUnitMemberAdd, AcademicAdministrationAcademicUnitMemberEnd:
		at := c.at
		if operation == AcademicAdministrationAcademicUnitMemberAdd {
			at = startAt
		}
		members, err := s.unitMembers.ListActiveByUser(ctx, target.ID, at)
		if err != nil {
			return "", "", err
		}
		var matching []string
		for _, member := range members {
			if member.AcademicUnitID.String() == c.current.ScopeID {
				matching = append(matching, member.ID.String())
			}
		}
		if operation == AcademicAdministrationAcademicUnitMemberAdd {
			return noChangeWhen(len(matching) > 0), "", nil
		}
		return endingClassification(matching)
	case AcademicAdministrationClassEnroll:
		return c.classEnrollment(ctx, target.ID, startAt)
	case AcademicAdministrationClassEnd:
		members, err := s.classMembers.ListActiveByUser(ctx, target.ID, c.at)
		if err != nil {
			return "", "", err
		}
		var matching []string
		for _, member := range members {
			if member.ClassID.String() == c.current.ScopeID {
				matching = append(matching, member.ID.String())
			}
		}
		return endingClassification(matching)
	case AcademicAdministrationRoleBindingCreate, AcademicAdministrationRoleBindingEnd:
		at := c.at
		if operation == AcademicAdministrationRoleBindingCreate {
			at = startAt
		}
		bindings, err := s.roleBindings.ListActiveByUser(ctx, target.ID, at)
		if err != nil {
			return "", "", err
		}
		var matching []string
		for _, binding := range bindings {
			if binding.RoleID == c.filter.RoleID && binding.ScopeType == c.current.ScopeType && binding.ScopeID == c.current.ScopeID {
				matching = append(matching, binding.ID.String())
			}
		}
		if operation == AcademicAdministrationRoleBindingCreate {
			return noChangeWhen(len(matching) > 0), "", nil
		}
		return endingClassification(matching)
	default:
		return "", "", apperror.New("request.invalid")
	}
}

// userEnabledChange classifies enabling or disabling a User whose account
// already is in that state as no change.
func (c *academicAdministrationPreviewClassifier) userEnabledChange(ctx context.Context, userID model.UserID, enable bool) (string, string, error) {
	current, err := c.service.users.Get(ctx, userID)
	if err != nil {
		return "", "", err
	}
	return noChangeWhen(current.DisabledAt.Valid != enable), "", nil
}

// classEnrollment classifies a Class enrollment: no change for a current
// member of the Class, and skipped for a User without a current student
// Affiliation or enrolled in another Class of the same Period.
func (c *academicAdministrationPreviewClassifier) classEnrollment(
	ctx context.Context,
	userID model.UserID,
	startAt time.Time,
) (string, string, error) {
	s := c.service
	members, err := s.classMembers.ListActiveByUser(ctx, userID, startAt)
	if err != nil {
		return "", "", err
	}
	for _, member := range members {
		if member.ClassID.String() == c.current.ScopeID {
			return store.AcademicAdministrationPreviewNoChange, "", nil
		}
		if member.AcademicPeriodID == c.period {
			return academicAdministrationPreviewEnrollmentConflict, "", nil
		}
	}

	affiliations, err := s.affiliations.ListActiveByUser(ctx, userID, startAt)
	if err != nil {
		return "", "", err
	}
	for _, affiliation := range affiliations {
		if affiliation.Kind == model.AffiliationStudent {
			return "", "", nil
		}
	}
	return academicAdministrationPreviewStudentAffiliationRequired, "", nil
}

func noChangeWhen(holds bool) string {
	if holds {
		return store.AcademicAdministrationPreviewNoChange
	}
	return ""
}

// endingClassification ends the one current relationship, makes no change
// when there is none, and skips a User holding several.
func endingClassification(matching []string) (string, string, error) {
	switch len(matching) {
	case 0:
		return store.AcademicAdministrationPreviewNoChange, "", nil
	case 1:
		return "", matching[0], nil
	default:
		return academicAdministrationPreviewAmbiguous, "", nil
	}
}

// academicAdministrationFilterRowGuard holds what one page of a filter
// preview's execution checks each row against: the frozen caller's User
// directory visibility, resolved once per page, or the denial that leaves
// the caller none.
type academicAdministrationFilterRowGuard struct {
	visibility                store.UserVisibilityScope
	denied                    bool
	systemAdministratorRoleID model.RoleID
}

// newAcademicAdministrationFilterRowGuard resolves, and records as one
// decision, the frozen caller's directory visibility for one page of rows.
func (s *Service) newAcademicAdministrationFilterRowGuard(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
) (*academicAdministrationFilterRowGuard, error) {
	guard := &academicAdministrationFilterRowGuard{}
	visibility, err := s.authorizer.AuthorizeUserSearch(ctx, inv)
	if err != nil {
		if failure, ok := apperror.As(err); ok && failure.Code() == "authorization.denied" {
			guard.denied = true
		} else {
			return nil, err
		}
	}
	guard.visibility = visibility

	if current.Filter != nil {
		switch AcademicAdministrationBatchOperation(current.Filter.Operation) {
		case AcademicAdministrationUserDisable, AcademicAdministrationUserSessionsRevoke:
			if guard.systemAdministratorRoleID, err = s.systemAdministratorRoleID(ctx); err != nil {
				return nil, err
			}
		}
	}
	return guard, nil
}

// checkAcademicAdministrationFilterRow is the execution-time check of one
// frozen filter preview row, before the row runs and its own transaction
// authorizes it again in PostgreSQL. It returns the public code the row
// fails with, or "" when the row may run: the User must still be one the
// caller's directory shows, a disable or Session revocation never reaches a
// User who became a System Administrator, and a Class enrollment never
// turns into a transfer the preview did not show.
func (s *Service) checkAcademicAdministrationFilterRow(
	ctx context.Context,
	current *store.OnboardingImport,
	guard *academicAdministrationFilterRowGuard,
	row store.OnboardingImportRow,
) (string, error) {
	if guard == nil || current.Filter == nil {
		return "", apperror.New("onboarding_import.unavailable")
	}
	if guard.denied || row.UserID == current.Principal.UserID {
		return "authorization.denied", nil
	}

	visible, err := s.users.List(ctx, store.UserListOptions{
		ID:              row.UserID,
		Limit:           1,
		IncludeDisabled: true,
		Visibility:      guard.visibility,
	})
	if err != nil {
		return "", apperror.New("onboarding_import.unavailable").Wrap(err)
	}
	if len(visible) != 1 {
		return "authorization.denied", nil
	}

	at := model.TimeUTC(s.now())
	switch AcademicAdministrationBatchOperation(current.Filter.Operation) {
	case AcademicAdministrationUserDisable, AcademicAdministrationUserSessionsRevoke:
		administrator, adminErr := s.isSystemAdministrator(ctx, guard.systemAdministratorRoleID, row.UserID, at)
		if adminErr != nil {
			return "", adminErr
		}
		if administrator {
			return academicAdministrationPreviewSystemAdministrator, nil
		}
	case AcademicAdministrationClassEnroll:
		class, classErr := s.classes.Get(ctx, model.ClassID(row.ScopeID))
		if classErr != nil {
			return "", apperror.New("onboarding_import.unavailable").Wrap(classErr)
		}
		members, listErr := s.classMembers.ListActiveByUser(ctx, row.UserID, row.StartsAt.Time)
		if listErr != nil {
			return "", apperror.New("onboarding_import.unavailable").Wrap(listErr)
		}
		for _, member := range members {
			if member.ClassID != class.ID && member.AcademicPeriodID == class.AcademicPeriodID {
				return "class.enrollment_conflict", nil
			}
		}
	}
	return "", nil
}

// academicAdministrationFilterRowItem is the one batch item a frozen filter
// preview row runs as: an ending operation names only the relationship it
// ends, and every other operation names the User.
func academicAdministrationFilterRowItem(row store.OnboardingImportRow) AcademicAdministrationBatchItemCommand {
	item := AcademicAdministrationBatchItemCommand{
		IdempotencyKey: strconv.Itoa(row.RowNumber),
		UserID:         row.UserID,
	}
	switch AcademicAdministrationBatchOperation(row.Operation) {
	case AcademicAdministrationAffiliationEnd,
		AcademicAdministrationAcademicUnitMemberEnd,
		AcademicAdministrationClassEnd,
		AcademicAdministrationRoleBindingEnd:
		item.UserID, item.RelationshipID = "", row.RelationshipID
	case AcademicAdministrationAffiliationAdd:
		item.AffiliationKind, item.StartAt, item.EndAt = row.AffiliationKind, row.StartsAt.Millis(), row.EndsAt.Millis()
	case AcademicAdministrationAcademicUnitMemberAdd:
		item.StartAt = row.StartsAt.Millis()
	case AcademicAdministrationClassEnroll:
		item.StartAt, item.EndAt = row.StartsAt.Millis(), row.EndsAt.Millis()
	case AcademicAdministrationRoleBindingCreate:
		item.RoleID, item.StartAt, item.EndAt = row.RoleID, row.StartsAt.Millis(), row.EndsAt.Millis()
	}
	return item
}

// academicAdministrationFilterAuthorizationLost reports whether a resolution
// failure means the frozen caller no longer holds what it asked with.
func academicAdministrationFilterAuthorizationLost(err error) bool {
	failure, ok := apperror.As(err)
	if !ok {
		return false
	}
	// The directory refuses with request.invalid a filter the caller's
	// current visibility no longer admits, such as an activity filter after
	// Institution-wide visibility was lost.
	switch failure.Code() {
	case "authorization.denied", "authentication.token_invalid", "request.invalid":
		return true
	}
	return false
}
