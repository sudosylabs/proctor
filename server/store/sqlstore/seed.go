// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The Seed methods write test fixtures directly, without the audit, mail or
// idempotency a production write carries. They live on the SQL store, not on
// the Store contract, so application code and the store layers can't reach
// them. Tests that need them hold the SQL store, or receive a storetest.Seeder.

// SeedInstitution saves an institution.
func (ss *SQLStore) SeedInstitution(ctx context.Context, institution *model.Institution) (*model.Institution, error) {
	return ss.stores.institution.(interface {
		Save(context.Context, *model.Institution) (*model.Institution, error)
	}).Save(ctx, institution)
}

// SeedAffiliation saves an affiliation.
func (ss *SQLStore) SeedAffiliation(ctx context.Context, affiliation *model.Affiliation) (*model.Affiliation, error) {
	return ss.stores.affiliation.(interface {
		Save(context.Context, *model.Affiliation) (*model.Affiliation, error)
	}).Save(ctx, affiliation)
}

// SeedAcademicUnitMember saves an academic unit membership.
func (ss *SQLStore) SeedAcademicUnitMember(ctx context.Context, member *model.AcademicUnitMember) (*model.AcademicUnitMember, error) {
	return ss.stores.academicUnitMember.(interface {
		Save(context.Context, *model.AcademicUnitMember) (*model.AcademicUnitMember, error)
	}).Save(ctx, member)
}

// SeedClassEnrollment enrolls a class member.
func (ss *SQLStore) SeedClassEnrollment(ctx context.Context, member *model.ClassMember) (*store.ClassEnrollmentResult, error) {
	return ss.stores.classMember.(interface {
		SeedEnroll(context.Context, *model.ClassMember) (*store.ClassEnrollmentResult, error)
	}).SeedEnroll(ctx, member)
}

// SeedClassEnrollmentEnd ends a class membership.
func (ss *SQLStore) SeedClassEnrollmentEnd(ctx context.Context, end *store.ClassMemberSeedEnd) (*model.ClassMember, error) {
	return ss.stores.classMember.(interface {
		SeedEnd(context.Context, *store.ClassMemberSeedEnd) (*model.ClassMember, error)
	}).SeedEnd(ctx, end)
}

// SeedPendingMFA saves a pending second-factor credential.
func (ss *SQLStore) SeedPendingMFA(ctx context.Context, credential *model.MFACredential) (*model.MFACredential, error) {
	return ss.stores.mfa.(interface {
		SeedPending(context.Context, *model.MFACredential) (*model.MFACredential, error)
	}).SeedPending(ctx, credential)
}

// SeedProgrammeUpdate rewrites a programme in place.
func (ss *SQLStore) SeedProgrammeUpdate(ctx context.Context, programme *model.Programme) (*model.Programme, error) {
	return ss.stores.programme.(interface {
		SeedUpdate(context.Context, *model.Programme) (*model.Programme, error)
	}).SeedUpdate(ctx, programme)
}

// SeedProgrammeLevelUpdate rewrites a programme level in place.
func (ss *SQLStore) SeedProgrammeLevelUpdate(ctx context.Context, level *model.ProgrammeLevel) (*model.ProgrammeLevel, error) {
	return ss.stores.programmeLevel.(interface {
		SeedUpdate(context.Context, *model.ProgrammeLevel) (*model.ProgrammeLevel, error)
	}).SeedUpdate(ctx, level)
}

// SeedRoleUpdate rewrites a custom role in place.
func (ss *SQLStore) SeedRoleUpdate(ctx context.Context, role *model.Role) (*model.Role, error) {
	return ss.stores.role.(interface {
		SeedUpdate(context.Context, *model.Role) (*model.Role, error)
	}).SeedUpdate(ctx, role)
}

// SeedBuiltInRole inserts a built-in role, which production creates only
// through the installation bootstrap.
func (ss *SQLStore) SeedBuiltInRole(ctx context.Context, role *model.Role) (*model.Role, error) {
	return ss.stores.role.(interface {
		SeedBuiltIn(context.Context, *model.Role) (*model.Role, error)
	}).SeedBuiltIn(ctx, role)
}

// SeedPasswordCredential saves a password credential.
func (ss *SQLStore) SeedPasswordCredential(ctx context.Context, credential *model.PasswordCredential) (*model.PasswordCredential, error) {
	return ss.stores.passwordCredential.(interface {
		Save(context.Context, *model.PasswordCredential) (*model.PasswordCredential, error)
	}).Save(ctx, credential)
}

// SeedExternalIdentity saves an external identity link.
func (ss *SQLStore) SeedExternalIdentity(ctx context.Context, identity *model.ExternalIdentity) (*model.ExternalIdentity, error) {
	return ss.stores.externalIdentity.(interface {
		Save(context.Context, *model.ExternalIdentity) (*model.ExternalIdentity, error)
	}).Save(ctx, identity)
}

// SeedLastLogin advances a user's last login and activity times, which
// production stamps when it issues a session.
func (ss *SQLStore) SeedLastLogin(ctx context.Context, update *store.UserLastLoginUpdate) error {
	return ss.stores.user.(interface {
		UpdateLastLogin(context.Context, *store.UserLastLoginUpdate) error
	}).UpdateLastLogin(ctx, update)
}
