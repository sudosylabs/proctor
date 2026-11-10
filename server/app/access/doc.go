// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package access owns the authorization decisions and the audit trail every
// application area uses. Authorizer resolves a Resource to its Institution,
// Academic Unit and Class scopes, evaluates current Role Bindings, and records
// each decision before it returns; Auditor writes attempt, decision and
// terminal audit events; RunAudited is the shared attempt protocol around one
// named Store mutation.
//
// It owns no use case, transport, or post-commit effect. It may import only
// model, store, app/apperror and app/invocation; it never imports app itself,
// an area package, a transport, or a concrete adapter.
package access

import (
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// UserSettingsAuditInput is the bounded, safe description of one User
// Settings replacement that UserSettingsAuditAdapter turns into an audit
// draft.
type UserSettingsAuditInput struct {
	Invocation        invocation.Invocation
	UserID            model.UserID
	PreviousRevision  model.UserSettingsRevision
	ResultingRevision model.UserSettingsRevision
	FormatVersion     int
	SourceBytes       int
}

// InvalidTokenAppError reports a Principal, credential or token that is not a
// valid caller. It is the one definition of that failure: the identity and
// onboarding areas and the parent app package return it too.
func InvalidTokenAppError() error {
	return apperror.New("authentication.token_invalid")
}

// InstitutionStoreErrors translates Institution Store failures. The academic
// Institution use cases translate through it too.
var InstitutionStoreErrors = apperror.AdministrationStoreTable("institution", "institution.conflict", "institution.invalid", nil)
