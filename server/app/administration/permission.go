// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// Permission is one action a custom role may hold, as the role editor shows
// it. OverrideOf is set for an override action.
type Permission struct {
	Action     model.Action
	Area       model.ActionArea
	ScopeTypes []model.RoleScopeType
	OverrideOf model.Action
}

// ListPermissions returns the closed catalogue of role-grantable actions. It
// is product data, so role.view is enough to read it.
func (s *Service) ListPermissions(ctx context.Context, inv invocation.Invocation) ([]Permission, error) {
	if _, err := s.authorizeRoles(ctx, inv, model.ActionRoleView); err != nil {
		return nil, err
	}
	actions := model.GrantableActions()
	permissions := make([]Permission, 0, len(actions))
	for _, action := range actions {
		definition, _ := model.DefinitionForAction(action)
		area, _ := model.AreaForAction(action)
		permission := Permission{Action: action, Area: area}
		for _, scope := range []model.RoleScopeType{model.RoleScopeInstitution, model.RoleScopeAcademicUnit, model.RoleScopeClass} {
			if definition.SupportsRoleScope(scope) {
				permission.ScopeTypes = append(permission.ScopeTypes, scope)
			}
		}
		if base, ok := model.OverriddenAction(action); ok {
			permission.OverrideOf = base
		}
		permissions = append(permissions, permission)
	}
	return permissions, nil
}
