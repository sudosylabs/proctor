// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/store"
)

// namingCatalog is the little a people resolver reads in a handler test:
// users for names, and an audit name lookup that names no record.
type namingCatalog struct {
	store.Catalog
	users store.UserStore
}

func (c namingCatalog) User() store.UserStore             { return c.users }
func (namingCatalog) Audit() store.AuditStore             { return unnamedRecords{} }
func (namingCatalog) Institution() store.InstitutionStore { return nil }

type unnamedRecords struct{ store.AuditStore }

func (unnamedRecords) ListNames(context.Context, store.AuditNameOptions) (store.AuditNames, error) {
	return store.AuditNames{}, nil
}

// testPeople names people from users through the real authorizer.
func testPeople(users store.UserStore, authorizer *access.Authorizer) *people.Resolver {
	return people.New(namingCatalog{users: users}, authorizer)
}
