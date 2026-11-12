// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// auditNameRow is one record name a ListNames read returns.
type auditNameRow struct {
	ID          string `db:"id"`
	Name        string `db:"name"`
	DisplayName string `db:"display_name"`
}

// ListNames reads each kind of name with one statement that applies its
// visibility, so a page costs at most four bounded reads whatever its size.
func (s SQLAuditStore) ListNames(ctx context.Context, options store.AuditNameOptions) (store.AuditNames, error) {
	users, err := distinctIdentifiers(options.UserIDs, store.MaximumAuditNames)
	if err != nil {
		return store.AuditNames{}, store.NewErrInvalidInput("audit_event", "user_ids", err)
	}
	units, err := distinctIdentifiers(options.AcademicUnitIDs, store.MaximumAuditNames)
	if err != nil {
		return store.AuditNames{}, store.NewErrInvalidInput("audit_event", "academic_unit_ids", err)
	}
	classes, err := distinctIdentifiers(options.ClassIDs, store.MaximumAuditNames)
	if err != nil {
		return store.AuditNames{}, store.NewErrInvalidInput("audit_event", "class_ids", err)
	}
	roles, err := distinctIdentifiers(options.RoleIDs, store.MaximumAuditNames)
	if err != nil {
		return store.AuditNames{}, store.NewErrInvalidInput("audit_event", "role_ids", err)
	}
	if !validVisibilityIDs(options.AcademicUnitRootIDs) ||
		!validVisibilityIDs(options.ClassVisibility.AcademicUnitRootIDs) ||
		!validVisibilityIDs(options.ClassVisibility.ClassIDs) ||
		!validVisibilityIDs(options.UserVisibility.AcademicUnitRootIDs) ||
		!validVisibilityIDs(options.UserVisibility.ClassMemberAcademicUnitRootIDs) ||
		!validVisibilityIDs(options.UserVisibility.ClassIDs) {
		return store.AuditNames{}, store.NewErrInvalidInput("audit_event", "name_visibility", nil)
	}

	names := store.AuditNames{
		Users:         map[model.UserID]string{},
		AcademicUnits: map[model.AcademicUnitID]string{},
		Classes:       map[model.ClassID]string{},
		Roles:         map[model.RoleID]store.AuditRoleName{},
	}
	if err := s.listAuditUserNames(ctx, users, options.UserVisibility, names.Users); err != nil {
		return store.AuditNames{}, err
	}
	if err := s.listAuditAcademicUnitNames(ctx, units, options, names.AcademicUnits); err != nil {
		return store.AuditNames{}, err
	}
	if err := s.listAuditClassNames(ctx, classes, options.ClassVisibility, names.Classes); err != nil {
		return store.AuditNames{}, err
	}
	if err := s.listAuditRoleNames(ctx, roles, names.Roles); err != nil {
		return store.AuditNames{}, err
	}
	return names, nil
}

// listAuditUserNames names the Users visible through visibility as the User
// directory applies it: a scoped visibility never names a disabled User.
func (s SQLAuditStore) listAuditUserNames(
	ctx context.Context,
	ids []string,
	visibility store.UserVisibilityScope,
	names map[model.UserID]string,
) error {
	if len(ids) == 0 {
		return nil
	}
	query, err := filterUserListVisibility(sq.Select(
		"users.id",
		"users.username AS name",
		"COALESCE(NULLIF(users.display_name, ''), users.username) AS display_name",
	).From("users").Where("users.id = ANY(?)", pq.Array(ids)).Where(sq.Eq{"users.archived_at": nil}), visibility)
	if err != nil {
		return err
	}
	if !visibility.InstitutionWide {
		query = query.Where(sq.Eq{"users.disabled_at": nil})
	}
	return s.selectAuditNames(ctx, query, "user", func(row auditNameRow) error {
		id, err := parsePersistedID("user", "id", row.ID, model.ParseUserID)
		if err == nil {
			names[id] = row.DisplayName
		}
		return err
	})
}

// listAuditAcademicUnitNames names the unarchived Academic Units inside the
// Institution-wide or subtree visibility.
func (s SQLAuditStore) listAuditAcademicUnitNames(
	ctx context.Context,
	ids []string,
	options store.AuditNameOptions,
	names map[model.AcademicUnitID]string,
) error {
	if len(ids) == 0 {
		return nil
	}
	units, roots := academicSubtreeCTE("audit_name_units", options.AcademicUnitRootIDs)
	query := sq.Select("academic_units.id", "academic_units.name", "academic_units.display_name").
		Prefix(`WITH RECURSIVE `+units, roots).
		From("academic_units").
		Where("academic_units.id = ANY(?)", pq.Array(ids)).
		Where(sq.Eq{"academic_units.archived_at": nil}).
		Where("(? OR academic_units.id IN (SELECT id FROM audit_name_units))", options.AcademicUnitInstitutionWide)
	return s.selectAuditNames(ctx, query, "academic_unit", func(row auditNameRow) error {
		id, err := parsePersistedID("academic_unit", "id", row.ID, model.ParseAcademicUnitID)
		if err == nil {
			names[id] = row.DisplayName
		}
		return err
	})
}

// listAuditClassNames names the Classes inside visibility as a Class search
// applies it.
func (s SQLAuditStore) listAuditClassNames(
	ctx context.Context,
	ids []string,
	visibility store.ClassVisibilityScope,
	names map[model.ClassID]string,
) error {
	if len(ids) == 0 {
		return nil
	}
	query := filterClassVisibility(sq.Select("classes.id", "classes.name", "classes.display_name").From("classes"), visibility).
		Where("classes.id = ANY(?)", pq.Array(ids))
	return s.selectAuditNames(ctx, query, "class", func(row auditNameRow) error {
		id, err := parsePersistedID("class", "id", row.ID, model.ParseClassID)
		if err == nil {
			names[id] = row.DisplayName
		}
		return err
	})
}

// listAuditRoleNames names the unarchived Roles; the caller asks only for
// Roles the reader may see.
func (s SQLAuditStore) listAuditRoleNames(ctx context.Context, ids []string, names map[model.RoleID]store.AuditRoleName) error {
	if len(ids) == 0 {
		return nil
	}
	query := sq.Select("roles.id", "roles.name", "roles.display_name").
		From("roles").
		Where("roles.id = ANY(?)", pq.Array(ids)).
		Where(sq.Eq{"roles.archived_at": nil})
	return s.selectAuditNames(ctx, query, "role", func(row auditNameRow) error {
		id, err := parsePersistedID("role", "id", row.ID, model.ParseRoleID)
		if err == nil {
			names[id] = store.AuditRoleName{Name: row.Name, DisplayName: row.DisplayName}
		}
		return err
	})
}

func (s SQLAuditStore) selectAuditNames(ctx context.Context, query sq.SelectBuilder, entity string, keep func(auditNameRow) error) error {
	rows := []auditNameRow{}
	if err := s.GetMaster().SelectBuilder(ctx, &rows, query); err != nil {
		return fmt.Errorf("list audit %s names: %w", entity, err)
	}
	for _, row := range rows {
		if err := keep(row); err != nil {
			return err
		}
	}
	return nil
}
