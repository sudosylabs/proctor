// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RoleBindingNames names what a page of Role Bindings refers to. A reader of a
// Binding already sees its role and scope IDs, so their names add nothing the
// reader could not learn; an archived role or scope has no name.
type RoleBindingNames struct {
	Roles         map[model.RoleID]store.AuditRoleName
	AcademicUnits map[model.AcademicUnitID]string
	Classes       map[model.ClassID]string
	Institution   string
	People        people.Refs
}

// NameRoleBindings reads the names for bindings the caller was already
// authorized to read, in a fixed number of bounded reads. It grants nothing.
func (s *Service) NameRoleBindings(
	ctx context.Context,
	inv invocation.Invocation,
	bindings []*model.RoleBinding,
) (RoleBindingNames, error) {
	options := store.AuditNameOptions{
		AcademicUnitInstitutionWide: true,
		ClassVisibility:             store.ClassVisibilityScope{InstitutionWide: true},
	}
	granters := make([]model.UserID, 0, len(bindings))
	var institutionID model.InstitutionID
	for _, binding := range bindings {
		options.RoleIDs = append(options.RoleIDs, binding.RoleID)
		switch binding.ScopeType {
		case model.RoleScopeAcademicUnit:
			options.AcademicUnitIDs = append(options.AcademicUnitIDs, model.AcademicUnitID(binding.ScopeID))
		case model.RoleScopeClass:
			options.ClassIDs = append(options.ClassIDs, model.ClassID(binding.ScopeID))
		case model.RoleScopeInstitution:
			institutionID = model.InstitutionID(binding.ScopeID)
		}
		granters = append(granters, binding.GrantedByUserID)
	}

	names, err := s.audits.ListNames(ctx, options)
	if err != nil {
		return RoleBindingNames{}, apperror.New("administration.unavailable").Wrap(err)
	}
	refs, err := s.people.Resolve(ctx, inv.Principal(), granters)
	if err != nil {
		return RoleBindingNames{}, apperror.New("administration.unavailable").Wrap(err)
	}
	result := RoleBindingNames{
		Roles:         names.Roles,
		AcademicUnits: names.AcademicUnits,
		Classes:       names.Classes,
		People:        refs,
	}
	if !institutionID.IsZero() {
		institution, err := s.institutions.Get(ctx, institutionID)
		if err != nil {
			return RoleBindingNames{}, apperror.New("administration.unavailable").Wrap(err)
		}
		result.Institution = institution.DisplayName
	}
	return result, nil
}

// NamePeople names the people records the caller was already authorized to
// read refer to, such as who last changed a Role. It grants nothing.
func (s *Service) NamePeople(ctx context.Context, inv invocation.Invocation, ids []model.UserID) (people.Refs, error) {
	refs, err := s.people.Resolve(ctx, inv.Principal(), ids)
	if err != nil {
		return nil, apperror.New("administration.unavailable").Wrap(err)
	}
	return refs, nil
}
