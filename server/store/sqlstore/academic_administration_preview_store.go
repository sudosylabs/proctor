// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	sq "github.com/Masterminds/squirrel"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// An academic administration filter preview is an onboarding import whose
// rows are the Users a User directory filter matched when its preview
// resolved. This file holds what only that mode needs: its persisted filter
// document, the checks on its frozen rows, the PostgreSQL guards on each row
// it executes, and its grouped and visibility-filtered row reads.

// academicAdministrationFilterQueryMaximumRunes bounds the directory search
// term a filter preview keeps.
const academicAdministrationFilterQueryMaximumRunes = 256

// academicAdministrationFilterScopes is every operation a filter preview
// applies and the one scope type, or any scope, it applies at.
var academicAdministrationFilterScopes = map[string][]model.RoleScopeType{
	"affiliation.add":          {model.RoleScopeInstitution},
	"affiliation.end":          {model.RoleScopeInstitution},
	"academic_unit_member.add": {model.RoleScopeAcademicUnit},
	"academic_unit_member.end": {model.RoleScopeAcademicUnit},
	"class.enroll":             {model.RoleScopeClass},
	"class.end":                {model.RoleScopeClass},
	"role_binding.create":      {model.RoleScopeInstitution, model.RoleScopeAcademicUnit, model.RoleScopeClass},
	"role_binding.end":         {model.RoleScopeInstitution, model.RoleScopeAcademicUnit, model.RoleScopeClass},
	"user.enable":              {model.RoleScopeInstitution},
	"user.disable":             {model.RoleScopeInstitution},
	"user_sessions.revoke":     {model.RoleScopeInstitution},
}

// academicAdministrationFilterDocument is the persisted spelling of
// store.AcademicAdministrationFilter. It is decoded strictly, so a renamed
// member or an older format fails closed as invalid persisted state.
type academicAdministrationFilterDocument struct {
	Operation        string     `json:"operation"`
	Query            string     `json:"q,omitempty"`
	Affiliations     []string   `json:"affiliation,omitempty"`
	AcademicUnitID   string     `json:"academic_unit_id,omitempty"`
	RoleIDs          []string   `json:"role_id,omitempty"`
	State            string     `json:"state,omitempty"`
	NeverSignedIn    bool       `json:"never_signed_in,omitempty"`
	LastActiveAfter  *time.Time `json:"last_active_after,omitempty"`
	LastActiveBefore *time.Time `json:"last_active_before,omitempty"`
	RoleBindingRole  string     `json:"binding_role_id,omitempty"`
	AffiliationKind  string     `json:"affiliation_kind,omitempty"`
	StartAt          *time.Time `json:"start_at,omitempty"`
	EndAt            *time.Time `json:"end_at,omitempty"`
}

func optionalDocumentTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	at := model.TimeUTC(value)
	return &at
}

// encodeAcademicAdministrationFilter returns the jsonb value of a filter, or
// nil for an import without one.
func encodeAcademicAdministrationFilter(filter *store.AcademicAdministrationFilter) (any, error) {
	if filter == nil {
		return nil, nil
	}
	document := academicAdministrationFilterDocument{
		Operation:        filter.Operation,
		Query:            filter.Query,
		AcademicUnitID:   filter.AcademicUnitID.String(),
		State:            filter.State,
		NeverSignedIn:    filter.NeverSignedIn,
		LastActiveAfter:  optionalDocumentTime(filter.LastActiveAfter),
		LastActiveBefore: optionalDocumentTime(filter.LastActiveBefore),
		RoleBindingRole:  filter.RoleID.String(),
		AffiliationKind:  string(filter.AffiliationKind),
		StartAt:          optionalDocumentTime(filter.StartAt.Time),
		EndAt:            optionalDocumentTime(filter.EndAt.Time),
	}
	for _, kind := range filter.Affiliations {
		document.Affiliations = append(document.Affiliations, string(kind))
	}
	for _, id := range filter.RoleIDs {
		document.RoleIDs = append(document.RoleIDs, id.String())
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, store.NewErrInvalidInput("onboarding_import", "filter", nil).Wrap(err)
	}
	return encoded, nil
}

// decodeAcademicAdministrationFilter reads the persisted filter document of an import at scopeType,
// refusing unknown members, trailing data, and any filter the Store would
// not have written.
func decodeAcademicAdministrationFilter(scopeType model.RoleScopeType, encoded []byte) (*store.AcademicAdministrationFilter, error) {
	var document academicAdministrationFilterDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || decoder.More() {
		return nil, fmt.Errorf("decode academic administration filter: %w", store.ErrInvalidState)
	}
	filter := &store.AcademicAdministrationFilter{
		Operation:       document.Operation,
		Query:           document.Query,
		AcademicUnitID:  model.AcademicUnitID(document.AcademicUnitID),
		State:           document.State,
		NeverSignedIn:   document.NeverSignedIn,
		RoleID:          model.RoleID(document.RoleBindingRole),
		AffiliationKind: model.AffiliationKind(document.AffiliationKind),
	}
	for _, kind := range document.Affiliations {
		filter.Affiliations = append(filter.Affiliations, model.AffiliationKind(kind))
	}
	for _, id := range document.RoleIDs {
		filter.RoleIDs = append(filter.RoleIDs, model.RoleID(id))
	}
	if document.LastActiveAfter != nil {
		filter.LastActiveAfter = model.TimeUTC(*document.LastActiveAfter)
	}
	if document.LastActiveBefore != nil {
		filter.LastActiveBefore = model.TimeUTC(*document.LastActiveBefore)
	}
	if document.StartAt != nil {
		filter.StartAt = model.OptionalTimeFrom(*document.StartAt)
	}
	if document.EndAt != nil {
		filter.EndAt = model.OptionalTimeFrom(*document.EndAt)
	}
	if !validAcademicAdministrationFilter(scopeType, filter) {
		return nil, fmt.Errorf("decode academic administration filter: %w", store.ErrInvalidState)
	}
	return filter, nil
}

// validAcademicAdministrationFilter reports whether a filter names an
// operation the scope admits, only the operation fields that operation
// owns, and a well-formed bounded directory filter.
func validAcademicAdministrationFilter(scopeType model.RoleScopeType, filter *store.AcademicAdministrationFilter) bool {
	if filter == nil || !slices.Contains(academicAdministrationFilterScopes[filter.Operation], scopeType) {
		return false
	}
	if !utf8.ValidString(filter.Query) ||
		utf8.RuneCountInString(filter.Query) > academicAdministrationFilterQueryMaximumRunes ||
		len(filter.Affiliations) > 4 ||
		len(filter.RoleIDs) > store.MaximumUserListRoles ||
		(filter.AcademicUnitID != "" && !filter.AcademicUnitID.IsValid()) ||
		(filter.State != "" && filter.State != "active" && filter.State != "disabled") ||
		(filter.NeverSignedIn && (!filter.LastActiveAfter.IsZero() || !filter.LastActiveBefore.IsZero())) ||
		(!filter.LastActiveAfter.IsZero() && !filter.LastActiveBefore.IsZero() && !filter.LastActiveAfter.Before(filter.LastActiveBefore)) {
		return false
	}
	for _, kind := range filter.Affiliations {
		if !kind.IsValid() {
			return false
		}
	}
	for _, id := range filter.RoleIDs {
		if !id.IsValid() {
			return false
		}
	}

	creates := filter.Operation == "affiliation.add" || filter.Operation == "academic_unit_member.add" ||
		filter.Operation == "class.enroll" || filter.Operation == "role_binding.create"
	ends := filter.Operation == "affiliation.add" || filter.Operation == "class.enroll" || filter.Operation == "role_binding.create"
	affiliation := strings.HasPrefix(filter.Operation, "affiliation.")
	roleBinding := strings.HasPrefix(filter.Operation, "role_binding.")
	return (!filter.StartAt.Valid || creates) &&
		(!filter.EndAt.Valid || ends) &&
		(!filter.StartAt.Valid || !filter.EndAt.Valid || filter.EndAt.Time.After(filter.StartAt.Time)) &&
		(affiliation == filter.AffiliationKind.IsValid()) &&
		(affiliation || filter.AffiliationKind == "") &&
		(roleBinding == filter.RoleID.IsValid()) &&
		(roleBinding || filter.RoleID == "")
}

// validAcademicAdministrationFilterRows reports whether frozen filter preview
// rows are at most AcademicAdministrationPreviewMaximumRows, each name a
// distinct User through the preview's own operation and scope, and each row
// held back from execution carries the reason it was.
func validAcademicAdministrationFilterRows(value *store.OnboardingImport, rows []store.OnboardingImportRow) bool {
	if value == nil || value.Filter == nil || len(rows) > store.AcademicAdministrationPreviewMaximumRows {
		return false
	}
	users := make(map[model.UserID]struct{}, len(rows))
	for _, row := range rows {
		if _, repeated := users[row.UserID]; repeated ||
			!row.UserID.IsValid() ||
			row.Operation != value.Filter.Operation ||
			row.ScopeType != value.ScopeType ||
			row.ScopeID != value.ScopeID ||
			row.Email != "" ||
			(row.PreviewStatus != model.OnboardingImportRowValid && row.PreviewStatus != model.OnboardingImportRowInvalid) ||
			(row.PreviewStatus == model.OnboardingImportRowValid) != (row.PreviewCode == "") ||
			row.Status != row.PreviewStatus {
			return false
		}
		users[row.UserID] = struct{}{}
	}
	return true
}

// countOnboardingImportNoChangeRows counts the rows of an academic
// administration filter preview whose change already held when it
// resolved, which its commit records as no-ops; any other import has none.
func countOnboardingImportNoChangeRows(ctx context.Context, tx *sqlxTxWrapper, id model.OnboardingImportID) (int, error) {
	var count int
	if err := tx.Get(
		ctx,
		&count,
		`SELECT count(*)
		FROM onboarding_import_rows r
		JOIN onboarding_imports oi ON oi.id = r.import_id
		WHERE r.import_id = ?
			AND oi.mode = 'academic_administration_filter'
			AND r.preview_status <> 'valid'
			AND r.preview_code = ?`,
		id.String(),
		store.AcademicAdministrationPreviewNoChange,
	); err != nil {
		return 0, fmt.Errorf("count academic administration preview no-op rows: %w", err)
	}
	return count, nil
}

// requireAcademicAdministrationFilterRowTarget refuses, inside the row's
// own transaction, a filter preview row whose User is the acting User, and
// a disable or Session revocation of a User who is a System Administrator
// at the PostgreSQL instant at. A preview skipped both kinds; this keeps
// them out even when the target changed after the preview froze.
func requireAcademicAdministrationFilterRowTarget(
	ctx context.Context,
	tx *sqlxTxWrapper,
	principal model.Principal,
	row onboardingImportDetailRow,
	at time.Time,
) error {
	if !row.UserID.Valid || row.UserID.String == principal.UserID.String() {
		return store.NewErrConflict("onboarding_import_row", "self", nil)
	}
	if row.Operation != "user.disable" && row.Operation != "user_sessions.revoke" {
		return nil
	}
	administrator, err := isActiveSystemAdministrator(ctx, tx, row.UserID.String, at)
	if err != nil {
		return err
	}
	if administrator {
		return store.NewErrConflict("onboarding_import_row", "system_administrator", nil)
	}
	return nil
}

// academicAdministrationPreviewClassSQL classifies a frozen row: a row
// admitted to execution will change, a row whose change already held is a
// no-op, and every other row is skipped for its preview code.
const academicAdministrationPreviewClassSQL = `CASE
		WHEN r.preview_status = 'valid' THEN 'will_change'
		WHEN r.preview_code = '` + store.AcademicAdministrationPreviewNoChange + `' THEN 'no_op'
		ELSE 'skipped'
	END`

// CountAcademicAdministrationPreviewGroups counts one filter preview's rows
// by class and, for skipped rows, reason: every frozen row, or through a
// Visibility only the rows ListAcademicAdministrationPreviewRows would show.
func (s SQLInvitationStore) CountAcademicAdministrationPreviewGroups(
	ctx context.Context,
	options store.AcademicAdministrationPreviewGroupCountOptions,
) ([]store.AcademicAdministrationPreviewGroup, error) {
	if !options.ImportID.IsValid() || (options.Visibility != nil && !validAcademicAdministrationPreviewVisibility(*options.Visibility)) {
		return nil, store.NewErrInvalidInput("academic_administration_preview", "group_count_options", nil)
	}

	query := s.getQueryBuilder().
		Select(academicAdministrationPreviewClassSQL + " AS class").
		Column(sq.Expr("CASE WHEN r.preview_status = 'valid' OR r.preview_code = ? THEN '' ELSE r.preview_code END AS reason",
			store.AcademicAdministrationPreviewNoChange)).
		Column("count(*) AS rows").
		From("onboarding_import_rows r").
		Join("onboarding_imports oi ON oi.id = r.import_id AND oi.mode = 'academic_administration_filter'").
		Where(sq.Eq{"r.import_id": options.ImportID.String()})
	if options.Visibility != nil {
		var err error
		if query, err = filterAcademicAdministrationPreviewRowVisibility(query, *options.Visibility); err != nil {
			return nil, err
		}
	}
	query = query.GroupBy("1", "2").OrderBy("1", "2")

	rows := []struct {
		Class  string `db:"class"`
		Reason string `db:"reason"`
		Rows   int    `db:"rows"`
	}{}
	if err := s.GetMaster().SelectBuilder(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("count academic administration preview groups: %w", err)
	}
	groups := make([]store.AcademicAdministrationPreviewGroup, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, store.AcademicAdministrationPreviewGroup{
			Class:  store.AcademicAdministrationPreviewClass(row.Class),
			Reason: row.Reason,
			Rows:   row.Rows,
		})
	}
	return groups, nil
}

// ListAcademicAdministrationPreviewRows reads one page of a filter preview's
// rows of one class whose User the reader's directory visibility reaches
// now. The visibility predicate is the User directory's own, so a reader
// never sees a row the directory would not show them.
func (s SQLInvitationStore) ListAcademicAdministrationPreviewRows(
	ctx context.Context,
	options store.AcademicAdministrationPreviewRowListOptions,
) ([]store.AcademicAdministrationPreviewRow, error) {
	visibility := options.Visibility
	if !options.ImportID.IsValid() ||
		!options.Class.IsValid() ||
		(options.Reason != "" && options.Class != store.AcademicAdministrationPreviewSkipped) ||
		len(options.Reason) > 128 ||
		options.AfterRowNumber < 0 ||
		options.Limit < 1 ||
		options.Limit > store.OnboardingImportListMaximum ||
		!validAcademicAdministrationPreviewVisibility(visibility) {
		return nil, store.NewErrInvalidInput("academic_administration_preview", "row_list_options", nil)
	}

	query := s.getQueryBuilder().
		Select(
			"r.row_number", "r.user_id", "users.username", "users.display_name",
			academicAdministrationPreviewClassSQL+" AS class",
			"r.preview_code", "r.status", "r.public_code", "COALESCE(r.result_ref, '') AS result_ref",
		).
		From("onboarding_import_rows r").
		Join("onboarding_imports oi ON oi.id = r.import_id AND oi.mode = 'academic_administration_filter'").
		Where(sq.Eq{"r.import_id": options.ImportID.String()}).
		Where(sq.Gt{"r.row_number": options.AfterRowNumber}).
		Where(academicAdministrationPreviewClassSQL+" = ?", string(options.Class))
	if options.Reason != "" {
		query = query.Where(sq.Eq{"r.preview_code": options.Reason})
	}
	query, err := filterAcademicAdministrationPreviewRowVisibility(query, visibility)
	if err != nil {
		return nil, err
	}
	query = query.OrderBy("r.row_number").Limit(uint64(options.Limit))

	rows := []struct {
		RowNumber   int                             `db:"row_number"`
		UserID      string                          `db:"user_id"`
		Username    string                          `db:"username"`
		DisplayName string                          `db:"display_name"`
		Class       string                          `db:"class"`
		PreviewCode string                          `db:"preview_code"`
		Status      model.OnboardingImportRowStatus `db:"status"`
		PublicCode  string                          `db:"public_code"`
		ResourceID  string                          `db:"result_ref"`
	}{}
	if err = s.GetMaster().SelectBuilder(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("list academic administration preview rows: %w", err)
	}
	values := make([]store.AcademicAdministrationPreviewRow, 0, len(rows))
	for _, row := range rows {
		value := store.AcademicAdministrationPreviewRow{
			RowNumber:   row.RowNumber,
			UserID:      model.UserID(row.UserID),
			Username:    row.Username,
			DisplayName: row.DisplayName,
			Class:       store.AcademicAdministrationPreviewClass(row.Class),
			Status:      row.Status,
			PublicCode:  row.PublicCode,
			ResourceID:  row.ResourceID,
		}
		if value.Class == store.AcademicAdministrationPreviewSkipped {
			value.Reason = row.PreviewCode
		}
		values = append(values, value)
	}
	return values, nil
}

// validAcademicAdministrationPreviewVisibility reports whether a reader's
// User directory visibility is bounded and well formed.
func validAcademicAdministrationPreviewVisibility(visibility store.UserVisibilityScope) bool {
	return len(visibility.ClassIDs)+len(visibility.AcademicUnitRootIDs)+len(visibility.ClassMemberAcademicUnitRootIDs) <= 256 &&
		validVisibilityIDs(visibility.ClassIDs) &&
		validVisibilityIDs(visibility.AcademicUnitRootIDs) &&
		validVisibilityIDs(visibility.ClassMemberAcademicUnitRootIDs)
}

// filterAcademicAdministrationPreviewRowVisibility keeps only the filter
// preview rows (aliased r) whose User the reader's directory shows now: an
// unarchived User, enabled unless the visibility is Institution-wide, inside
// the User directory's own visibility predicate. The rows read and the
// summary count apply the same filter, so the counts match the rows shown.
func filterAcademicAdministrationPreviewRowVisibility(
	query sq.SelectBuilder,
	visibility store.UserVisibilityScope,
) (sq.SelectBuilder, error) {
	query = query.Join("users ON users.id = r.user_id").Where(sq.Eq{"users.archived_at": nil})
	if !visibility.InstitutionWide {
		query = query.Where(sq.Eq{"users.disabled_at": nil})
	}
	return filterUserListVisibility(query, visibility)
}
