// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import "time"

func constructAdministration(
	deps Dependencies,
	foundation applicationFoundation,
	access accessAcademicConstruction,
	accounts accountConstruction,
) administrationConstruction {
	roleAuthorization := roleAuthorization{
		authorization: access.authorization,
		institutions:  deps.Store.Institution(),
	}
	roleBindings := newRoleBindingService(roleBindingServiceDependencies{Bindings: deps.Store.RoleBinding(), Roles: deps.Store.Role(), Users: deps.Store.User(), Authorization: roleAuthorization, Capabilities: access.capabilities, Audit: foundation.audit.MutationAudit(), Mail: foundation.mail, Effects: roleBindingRealtimeEffects{effects: foundation.realtime}, Now: time.Now})
	return administrationConstruction{
		roles:        newRoleService(roleServiceDependencies{Roles: deps.Store.Role(), Authorization: roleAuthorization, Audit: foundation.audit.MutationAudit(), Effects: roleRealtimeEffects{effects: foundation.realtime}, Now: time.Now}),
		roleBindings: roleBindings,
		academicAdministrationBatches: newAcademicAdministrationBatchService(academicAdministrationBatchServiceDependencies{Commands: academicAdministrationCommandServices{academic: access.academic,
			roleBindings: roleBindings, identity: accounts.identity}, Authorization: invitationAuthorizationAdapter{authorization: access.authorization}, Outcomes: deps.Store.CommandOutcome(), Now: time.Now, RecentTTL: deps.RecentAuthenticationTTL}),
		auditListings: newAuditListingService(auditListingServiceDependencies{Audits: deps.Store.Audit(), Authorization: auditListingAuthorization{authorization: access.authorization, institutions: deps.Store.Institution()}}),
	}
}
