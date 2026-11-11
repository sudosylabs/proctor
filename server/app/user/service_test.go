// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// catalogFake is the Store catalog of a test: it returns the store fakes the
// test sets and panics on any other accessor.
type catalogFake struct {
	store.Catalog
	user         store.UserStore
	roleBinding  store.RoleBindingStore
	file         store.FileStore
	userSettings store.UserSettingsStore
}

func (c *catalogFake) User() store.UserStore                 { return c.user }
func (c *catalogFake) RoleBinding() store.RoleBindingStore   { return c.roleBinding }
func (c *catalogFake) File() store.FileStore                 { return c.file }
func (c *catalogFake) UserSettings() store.UserSettingsStore { return c.userSettings }

// publisherIn returns a recording Publisher that also places each call in the
// log a test shares with its other fakes: "effect" for a publication and
// "effect-report" for a reported failure.
func publisherIn(events *[]string) *realtimetest.Publisher {
	return &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		if call.Method == realtimetest.Report {
			*events = append(*events, "effect-report")
			return
		}
		*events = append(*events, "effect")
	}}
}

// settingsAuditFake prepares the audit event of a settings replacement.
type settingsAuditFake struct {
	event *model.AuditEvent
	err   error
	input access.UserSettingsAuditInput
	calls int
}

func (a *settingsAuditFake) PrepareReplacement(_ context.Context, input access.UserSettingsAuditInput) (*model.AuditEvent, error) {
	a.calls++
	a.input = input
	return a.event, a.err
}

// authorizerFake answers every Authorizer question from its fields and
// records the calls it receives in order.
type authorizerFake struct {
	events      *[]string
	searchScope store.UserVisibilityScope
	roleScope   store.UserVisibilityScope
	roleScopeAt time.Time
	fullRead    bool
	readErr     error
	writeErr    error
}

func (a *authorizerFake) AuthorizeUserSearch(context.Context, invocation.Invocation) (store.UserVisibilityScope, error) {
	*a.events = append(*a.events, "authorize-search")
	if a.searchScope.InstitutionWide || a.searchScope.ClassMemberInstitutionWide ||
		len(a.searchScope.AcademicUnitRootIDs) > 0 || len(a.searchScope.ClassMemberAcademicUnitRootIDs) > 0 ||
		len(a.searchScope.ClassIDs) > 0 {
		return a.searchScope, nil
	}
	return store.UserVisibilityScope{ClassIDs: []model.ClassID{"class-a"}, ActiveAt: model.TimeFromMillis(100)}, nil
}

func (a *authorizerFake) RoleBindingViewScope(_ context.Context, _ invocation.Invocation, at time.Time) (store.UserVisibilityScope, error) {
	*a.events = append(*a.events, "role-binding-view-scope")
	a.roleScopeAt = at
	return a.roleScope, nil
}

func (a *authorizerFake) AuthorizeProfileRead(context.Context, invocation.Invocation, model.UserID) (access.ProfileReadAuthorization, error) {
	*a.events = append(*a.events, "authorize-read")
	return access.ProfileReadAuthorization{FullProfile: a.fullRead}, a.readErr
}

func (a *authorizerFake) Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error {
	*a.events = append(*a.events, "authorize-manage")
	return nil
}

func (a *authorizerFake) AuthorizeProfilePictureWrite(context.Context, invocation.Invocation, model.UserID) error {
	*a.events = append(*a.events, "authorize-profile-picture-write")
	return a.writeErr
}

func userSettingsSessionPrincipal(at time.Time) model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at,
	}
}
