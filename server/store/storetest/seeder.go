// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Seeder writes fixtures directly, without the audit, mail or idempotency of
// a production write. The SQL store implements it outside the Store contract.
// A test running through a store layer receives a store that also carries the
// SQL store's Seeder; see Seeded.
type Seeder interface {
	SeedInstitution(context.Context, *model.Institution) (*model.Institution, error)
	SeedAffiliation(context.Context, *model.Affiliation) (*model.Affiliation, error)
	SeedAcademicUnitMember(context.Context, *model.AcademicUnitMember) (*model.AcademicUnitMember, error)
	SeedClassEnrollment(context.Context, *model.ClassMember) (*store.ClassEnrollmentResult, error)
	SeedClassEnrollmentEnd(context.Context, *store.ClassMemberSeedEnd) (*model.ClassMember, error)
	SeedPendingMFA(context.Context, *model.MFACredential) (*model.MFACredential, error)
	SeedProgrammeUpdate(context.Context, *model.Programme) (*model.Programme, error)
	SeedProgrammeLevelUpdate(context.Context, *model.ProgrammeLevel) (*model.ProgrammeLevel, error)
	SeedRoleUpdate(context.Context, *model.Role) (*model.Role, error)
	SeedBuiltInRole(context.Context, *model.Role) (*model.Role, error)
	SeedPasswordCredential(context.Context, *model.PasswordCredential) (*model.PasswordCredential, error)
	SeedExternalIdentity(context.Context, *model.ExternalIdentity) (*model.ExternalIdentity, error)
	SeedLastLogin(context.Context, *store.UserLastLoginUpdate) error
	// DatabaseNow reads PostgreSQL's clock, which decides every deadline.
	DatabaseNow(context.Context) (time.Time, error)
}

// Seeded is a store under test that also seeds through the store beneath its
// layers.
type Seeded struct {
	store.Store
	Seeder
}

// seeder returns the Seeder ss carries. A store that carries none is a mistake
// in the test's setup, so it panics, which fails the test that called it.
func seeder(ss store.Store) Seeder {
	seeder, ok := ss.(Seeder)
	if !ok {
		panic("the store under test can't seed fixtures: pass a storetest.Seeded")
	}
	return seeder
}

// WaitUntilPassed returns once both this process's clock and PostgreSQL's
// have passed at. A test that sleeps on its own clock and then lets the
// database decide whether at has passed fails whenever the database's clock
// lags, as Docker's often does.
func WaitUntilPassed(t *testing.T, ctx context.Context, ss store.Store, at time.Time) {
	t.Helper()
	time.Sleep(time.Until(at) + time.Millisecond)
	for {
		now, err := seeder(ss).DatabaseNow(ctx)
		require.NoError(t, err)
		if now.After(at) {
			return
		}
		time.Sleep(at.Sub(now) + time.Millisecond)
	}
}
