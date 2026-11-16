// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

const (
	systemAdministratorRoleNameID        = "authorization.role.system_admin.name"
	systemAdministratorRoleDescriptionID = "authorization.role.system_admin.description"
)

const localizationOrigin = "httpapi"

// LocalizationDefinitions returns the closed set of messages owned by the
// HTTP presentation boundary: the routing kernel's Problem Details prose,
// then the messages resources present.
func LocalizationDefinitions() []localization.Definition {
	return append(route.LocalizationDefinitions(), resourceLocalizationDefinitions()...)
}

// resourceLocalizationDefinitions are the messages resources present: session
// revocation reasons, personal access token scope titles and descriptions, the
// system administrator role, and audit summaries.
func resourceLocalizationDefinitions() []localization.Definition {
	definitions := make([]localization.Definition, 0, len(model.AllSessionRevocationReasons())+
		len(model.PersonalAccessTokenActions())*2+2)
	for _, reason := range model.AllSessionRevocationReasons() {
		definitions = append(definitions, localization.Definition{
			ID:     sessionRevocationLocalizationID(reason),
			Origin: localizationOrigin,
		})
	}
	for _, action := range model.PersonalAccessTokenActions() {
		definitions = append(definitions,
			localization.Definition{ID: accessActionTitleLocalizationID(model.Action(action)), Origin: localizationOrigin},
			localization.Definition{ID: accessActionDescriptionLocalizationID(model.Action(action)), Origin: localizationOrigin},
		)
	}
	definitions = append(definitions,
		localization.Definition{ID: systemAdministratorRoleNameID, Origin: localizationOrigin},
		localization.Definition{ID: systemAdministratorRoleDescriptionID, Origin: localizationOrigin},
	)
	return append(definitions, auditSummaryLocalizationDefinitions()...)
}

func sessionRevocationLocalizationID(reason model.SessionRevocationReason) string {
	return "session.revocation." + string(reason)
}

// accessActionTitleLocalizationID names the short title of an action offered
// as a personal access token scope.
func accessActionTitleLocalizationID(action model.Action) string {
	return "access.action." + string(action) + ".title"
}

// accessActionDescriptionLocalizationID names the one-sentence explanation of
// an action offered as a personal access token scope.
func accessActionDescriptionLocalizationID(action model.Action) string {
	return "access.action." + string(action) + ".description"
}
