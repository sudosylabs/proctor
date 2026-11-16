// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"testing"
	"time"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
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
	role         store.RoleStore
	roleBinding  store.RoleBindingStore
	file         store.FileStore
	userSettings store.UserSettingsStore
}

func (c *catalogFake) User() store.UserStore                 { return c.user }
func (c *catalogFake) Role() store.RoleStore                 { return c.role }
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

// directoryInstant is the clock of the directory tests: the authorizer
// decides at it, so every visibility scope it returns is active at it.
var directoryInstant = model.TimeFromMillis(100)

// accessWith builds the real authorizer and auditor over users, with the
// grants given, and places what they do in events: "grants" for each read of
// the caller's Role Bindings, "decision:<action>:<status>" for a recorded
// decision, and "audit-begin:<action>" and "audit-<status>" for the audit of
// a mutation.
func accessWith(t *testing.T, events *[]string, users store.UserStore, grants ...accesstest.Grant) *accesstest.Fixture {
	t.Helper()
	return accesstest.New(t, accesstest.Options{
		Grants: grants, Users: users, Now: func() time.Time { return directoryInstant },
		Observe: func(event string) {
			if events != nil {
				*events = append(*events, event)
			}
		},
	})
}

// institutionGrant grants the actions Institution-wide.
func institutionGrant(userID model.UserID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions}
}

// unitGrant grants the actions in one Academic Unit.
func unitGrant(userID model.UserID, unitID model.AcademicUnitID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}
}

// newCaller returns a signed-in caller and the invocation it makes.
func newCaller() (model.Principal, invocation.Invocation) {
	principal := userSettingsSessionPrincipal(directoryInstant)
	return principal, invocation.New(principal, model.RequestMetadata{})
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
