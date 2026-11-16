// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// batchWorld is a Service whose batch rows run through the real academic,
// administration and identity Services over in-memory Store contracts, with
// the real authorizer and auditor. ledger records every Store mutation the
// rows reach.
type batchWorld struct {
	access       *accesstest.Fixture
	ledger       *batchLedger
	classMembers *batchClassMemberStore
	service      *Service
}

// batchWorldOptions describe one batch world. scopeID owns every existing
// relationship the Stores resolve: an Academic Unit membership sits in it as
// an Academic Unit, and a Class membership or Role Binding sits in it as a
// Class. active are the current Class memberships of their Users.
type batchWorldOptions struct {
	callers  []model.UserID
	scopeID  string
	outcomes store.CommandOutcomeStore
	active   []*model.ClassMember
}

func newBatchWorld(t *testing.T, now time.Time, options batchWorldOptions) *batchWorld {
	t.Helper()
	ledger := &batchLedger{outcomes: map[[sha256.Size]byte]any{}}
	users := batchUserStore{ledger: ledger}
	classMembers := &batchClassMemberStore{ledger: ledger, classID: model.ClassID(options.scopeID), active: options.active}
	var units []model.AcademicUnitID
	if options.scopeID != "" {
		units = append(units, model.AcademicUnitID(options.scopeID))
	}
	fixture := permissiveAccess(t, accesstest.Options{Users: users}, options.callers, units...)
	composer := newTestComposer(t)
	publisher := &realtimetest.Publisher{}
	catalog := &batchCatalog{
		institution:   institutionStoreFake{institution: fixture.Institution},
		class:         batchClassStore{periodID: batchPeriodID},
		user:          users,
		affiliation:   &batchAffiliationStore{ledger: ledger},
		unitMember:    &batchUnitMemberStore{ledger: ledger, unitID: model.AcademicUnitID(options.scopeID)},
		classMember:   classMembers,
		role:          batchRoleStore{},
		roleBinding:   &batchRoleBindingStore{ledger: ledger, classID: options.scopeID},
		session:       &batchSessionStore{ledger: ledger},
		accessPolicy:  struct{ store.AccessPolicyStore }{},
		retention:     struct{ store.RetentionStore }{},
		retentionPol:  struct{ store.RetentionPolicyStore }{},
		passwords:     struct{ store.PasswordCredentialStore }{},
		credentials:   struct{ store.SessionCredentialStore }{},
		registrations: struct{ store.DesktopRegistrationStore }{},
		tokens:        struct{ store.PersonalAccessTokenStore }{},
		mfa:           struct{ store.MFAStore }{},
		browser: struct {
			store.BrowserAuthenticationStore
		}{},
		compatibility: struct {
			store.DesktopCompatibilityPolicyStore
		}{},
		userTokens:   struct{ store.UserTokenStore }{},
		academicUnit: struct{ store.AcademicUnitStore }{},
		loginStates:  struct{ store.ExternalLoginStateStore }{},
		identities:   struct{ store.ExternalIdentityStore }{},
	}

	academicService, err := academic.New(academic.Dependencies{Store: catalog, Authorizer: fixture.Authorizer, Auditor: fixture.Auditor,
		Mail: composer, Publisher: publisher, Now: func() time.Time { return now }, NewID: model.NewID})
	require.NoError(t, err)

	capabilities := &identity.DeploymentCapabilities{}
	administrationService, err := administration.New(administration.Dependencies{Store: catalog, Authorizer: fixture.Authorizer,
		Auditor: fixture.Auditor, Capabilities: capabilities, Mail: composer, Publisher: publisher,
		CanonicalOrigin: "https://proctor.example.edu", PasswordMinimumLength: 12, RecentAuthenticationTTL: 15 * time.Minute,
		Now: func() time.Time { return now }})
	require.NoError(t, err)

	diagnostics := newBatchDiagnostics(t)
	cache := struct{ identity.AuthenticationCache }{}
	invalidator, err := identity.NewCacheInvalidator(identity.CacheInvalidatorDependencies{Cache: cache, Diagnostics: diagnostics})
	require.NoError(t, err)
	identityService, err := identity.New(identity.Dependencies{
		Store: catalog, Cache: cache, Registry: batchProviders{}, Authorizer: fixture.Authorizer, Auditor: fixture.Auditor,
		Capabilities: capabilities, Publisher: publisher, Mail: composer, Hasher: testPasswordHasher(t), Attempts: testAttempts(t),
		Invalidator: invalidator, Invitations: struct{ identity.InvitationAcceptor }{}, SkipDesktopCompatibility: true,
		NodeID: "node-1", PublicURL: "https://proctor.example.edu",
		Sessions: identity.SessionPolicy{AccessTTL: time.Minute, RefreshTTL: time.Hour, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour,
			MaximumPerUser: 5},
		LoginRateLimit: testRateLimit, RecentAuthenticationTTL: 15 * time.Minute,
		AuthenticationDiagnostics: diagnostics, RecoveryDiagnostics: diagnostics,
	})
	require.NoError(t, err)

	return &batchWorld{access: fixture, ledger: ledger, classMembers: classMembers, service: &Service{
		academic: academicService, administration: administrationService, identity: identityService,
		authorizer: fixture.Authorizer, auditor: fixture.Auditor.MutationAudit(), commandOutcomes: options.outcomes,
		now: func() time.Time { return now }, recentAuthenticationTTL: 15 * time.Minute,
	}}
}

// batchPeriodID is the Academic Period every Class of a batch world runs in.
var batchPeriodID = model.NewAcademicPeriodID()

// batchCaller is an interactive web Session at multi-factor strength,
// authenticated at at.
func batchCaller(at time.Time) invocation.Invocation {
	return invocation.New(sessionPrincipal(model.AuthenticationMultiFactor, at), model.RequestMetadata{})
}

func newBatchDiagnostics(t *testing.T) *logging.Logger {
	t.Helper()
	var logs logging.Buffer
	logger, err := logging.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })
	require.NoError(t, logger.Configure(logging.Config{MaxFieldBytes: 1024, Targets: []logging.Target{{
		Name: "test", Type: "console", Level: "debug", Format: "json", Writer: &logs,
	}}}))
	return logger
}

// batchLedger records each Store mutation a batch row reaches and keeps the
// retained outcome of each committed command, as the command outcome table
// does.
type batchLedger struct {
	calls    []batchStoreCall
	outcomes map[[sha256.Size]byte]any
}

// batchStoreCall is one Store mutation: the Store method, the command
// identity it carried and its full input.
type batchStoreCall struct {
	method  string
	command *store.CommandIdempotency
	input   any
}

func (l *batchLedger) methods() []string {
	var methods []string
	for _, call := range l.calls {
		methods = append(methods, call.method)
	}
	return methods
}

// commitBatchOutcome follows the Store contract for a batch row: a row bound
// to its canonical member (DuplicateOfKeyDigest set) answers with the
// canonical member's retained outcome and has the Store set Duplicate; any
// other row commits fresh and retains its outcome.
func commitBatchOutcome[T any](l *batchLedger, method string, command *store.CommandIdempotency, input any, fresh T) (T, error) {
	l.calls = append(l.calls, batchStoreCall{method: method, command: command, input: input})
	if command != nil && command.Batch != nil && command.Batch.DuplicateOfKeyDigest != ([sha256.Size]byte{}) {
		canonical, found := l.outcomes[command.Batch.DuplicateOfKeyDigest].(T)
		if !found {
			var zero T
			return zero, store.NewErrConflict("command_outcome", "batch_canonical_missing", nil)
		}
		command.Batch.Duplicate = true
		return canonical, nil
	}
	if command != nil {
		l.outcomes[command.KeyDigest] = fresh
	}
	return fresh, nil
}

// batchCatalog hands the three Services the Store contracts of a batch
// world; the ones no batch row calls are inert stand-ins.
type batchCatalog struct {
	store.Catalog
	institution   store.InstitutionStore
	class         store.ClassStore
	user          store.UserStore
	affiliation   store.AffiliationStore
	unitMember    store.AcademicUnitMemberStore
	classMember   store.ClassMemberStore
	role          store.RoleStore
	roleBinding   store.RoleBindingStore
	session       store.SessionStore
	accessPolicy  store.AccessPolicyStore
	retention     store.RetentionStore
	retentionPol  store.RetentionPolicyStore
	passwords     store.PasswordCredentialStore
	credentials   store.SessionCredentialStore
	registrations store.DesktopRegistrationStore
	tokens        store.PersonalAccessTokenStore
	mfa           store.MFAStore
	browser       store.BrowserAuthenticationStore
	compatibility store.DesktopCompatibilityPolicyStore
	userTokens    store.UserTokenStore
	academicUnit  store.AcademicUnitStore
	loginStates   store.ExternalLoginStateStore
	identities    store.ExternalIdentityStore
}

func (c *batchCatalog) Institution() store.InstitutionStore               { return c.institution }
func (c *batchCatalog) AcademicUnit() store.AcademicUnitStore             { return c.academicUnit }
func (c *batchCatalog) Programme() store.ProgrammeStore                   { return nil }
func (c *batchCatalog) ProgrammeLevel() store.ProgrammeLevelStore         { return nil }
func (c *batchCatalog) AcademicPeriod() store.AcademicPeriodStore         { return nil }
func (c *batchCatalog) Class() store.ClassStore                           { return c.class }
func (c *batchCatalog) ClassMember() store.ClassMemberStore               { return c.classMember }
func (c *batchCatalog) User() store.UserStore                             { return c.user }
func (c *batchCatalog) Affiliation() store.AffiliationStore               { return c.affiliation }
func (c *batchCatalog) AcademicUnitMember() store.AcademicUnitMemberStore { return c.unitMember }
func (c *batchCatalog) RoleBinding() store.RoleBindingStore               { return c.roleBinding }
func (c *batchCatalog) ExamSitting() store.ExamSittingStore               { return nil }
func (c *batchCatalog) Role() store.RoleStore                             { return c.role }
func (c *batchCatalog) Audit() store.AuditStore                           { return nil }
func (c *batchCatalog) AccessPolicy() store.AccessPolicyStore             { return c.accessPolicy }
func (c *batchCatalog) RetentionPolicy() store.RetentionPolicyStore       { return c.retentionPol }
func (c *batchCatalog) Retention() store.RetentionStore                   { return c.retention }
func (c *batchCatalog) PluginSettings() store.PluginSettingsStore         { return nil }
func (c *batchCatalog) EnvironmentProfile() store.EnvironmentProfileStore { return nil }
func (c *batchCatalog) PasswordCredential() store.PasswordCredentialStore { return c.passwords }
func (c *batchCatalog) Session() store.SessionStore                       { return c.session }
func (c *batchCatalog) SessionCredential() store.SessionCredentialStore   { return c.credentials }
func (c *batchCatalog) DesktopRegistration() store.DesktopRegistrationStore {
	return c.registrations
}
func (c *batchCatalog) PersonalAccessToken() store.PersonalAccessTokenStore { return c.tokens }
func (c *batchCatalog) MFA() store.MFAStore                                 { return c.mfa }
func (c *batchCatalog) BrowserAuthentication() store.BrowserAuthenticationStore {
	return c.browser
}
func (c *batchCatalog) DesktopCompatibilityPolicy() store.DesktopCompatibilityPolicyStore {
	return c.compatibility
}
func (c *batchCatalog) UserToken() store.UserTokenStore                   { return c.userTokens }
func (c *batchCatalog) ExternalLoginState() store.ExternalLoginStateStore { return c.loginStates }
func (c *batchCatalog) ExternalIdentity() store.ExternalIdentityStore     { return c.identities }
func (c *batchCatalog) Installation() store.InstallationStore             { return nil }
func (c *batchCatalog) Job() store.JobStore                               { return nil }

// batchProviders is a registry with no external providers.
type batchProviders struct{}

func (batchProviders) Descriptors() []model.ExternalAuthenticationProvider { return nil }
func (batchProviders) Provider(string) (identity.ExternalIdentityProvider, bool) {
	return nil, false
}

// batchUserStore resolves every User as active and records account state
// changes.
type batchUserStore struct {
	store.UserStore
	ledger *batchLedger
}

func batchUser(id model.UserID) *model.User {
	return &model.User{ID: id, CreatedAt: model.TimeFromMillis(1), UpdatedAt: model.TimeFromMillis(1), Revision: 2,
		Username: "member", Email: "member@example.edu", Locale: "en", Timezone: "UTC",
		DefaultProfilePictureSeed: strings.Repeat("a", model.ProfilePictureSeedLength)}
}

func (batchUserStore) Get(_ context.Context, id model.UserID) (*model.User, error) {
	return batchUser(id), nil
}

func (s batchUserStore) SetDisabled(_ context.Context, input *store.UserDisabledStateChange) (*store.UserDisabledStateResult, error) {
	return commitBatchOutcome(s.ledger, "User.SetDisabled", input.Command, input, &store.UserDisabledStateResult{User: batchUser(input.ID)})
}

// batchClassStore resolves every Class in one Academic Period.
type batchClassStore struct {
	store.ClassStore
	periodID model.AcademicPeriodID
}

func (s batchClassStore) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	return &model.Class{ID: id, AcademicPeriodID: s.periodID, DisplayName: "Class " + id.String()}, nil
}

type batchAffiliationStore struct {
	store.AffiliationStore
	ledger *batchLedger
}

func (s *batchAffiliationStore) Get(_ context.Context, id model.AffiliationID) (*model.Affiliation, error) {
	return &model.Affiliation{ID: id, UserID: model.NewUserID(), Kind: model.AffiliationStaff, Revision: 1, StartsAt: model.TimeFromMillis(1)}, nil
}

func (s *batchAffiliationStore) Create(_ context.Context, input *store.AffiliationCreation) (*model.Affiliation, error) {
	return commitBatchOutcome(s.ledger, "Affiliation.Create", input.Command, input, input.Affiliation)
}

func (s *batchAffiliationStore) End(_ context.Context, input *store.AffiliationEnd) (*model.Affiliation, error) {
	return commitBatchOutcome(s.ledger, "Affiliation.End", input.Command, input, &model.Affiliation{ID: input.ID})
}

type batchUnitMemberStore struct {
	store.AcademicUnitMemberStore
	ledger *batchLedger
	unitID model.AcademicUnitID
}

func (s *batchUnitMemberStore) Get(_ context.Context, id model.AcademicUnitMemberID) (*model.AcademicUnitMember, error) {
	return &model.AcademicUnitMember{ID: id, AcademicUnitID: s.unitID, UserID: model.NewUserID(), Revision: 1, StartsAt: model.TimeFromMillis(1)}, nil
}

func (s *batchUnitMemberStore) Create(_ context.Context, input *store.AcademicUnitMemberCreation) (*model.AcademicUnitMember, error) {
	return commitBatchOutcome(s.ledger, "AcademicUnitMember.Create", input.Command, input, input.Member)
}

func (s *batchUnitMemberStore) End(_ context.Context, input *store.AcademicUnitMemberEnd) (*model.AcademicUnitMember, error) {
	return commitBatchOutcome(s.ledger, "AcademicUnitMember.End", input.Command, input, &model.AcademicUnitMember{ID: input.ID})
}

// batchClassMemberStore resolves every Class membership in its Class and
// lists the active memberships it was given.
type batchClassMemberStore struct {
	store.ClassMemberStore
	ledger  *batchLedger
	classID model.ClassID
	active  []*model.ClassMember
	// reads names each read the use case made: "Get" or "ListActiveByUser".
	reads []string
}

func (s *batchClassMemberStore) Get(_ context.Context, id model.ClassMemberID) (*model.ClassMember, error) {
	s.reads = append(s.reads, "Get")
	return &model.ClassMember{ID: id, ClassID: s.classID, AcademicPeriodID: batchPeriodID, UserID: model.NewUserID(), Revision: 1,
		StartsAt: model.TimeFromMillis(1_000)}, nil
}

func (s *batchClassMemberStore) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.ClassMember, error) {
	s.reads = append(s.reads, "ListActiveByUser")
	var active []*model.ClassMember
	for _, membership := range s.active {
		if membership.UserID == userID {
			active = append(active, membership)
		}
	}
	return active, nil
}

func (s *batchClassMemberStore) Enroll(_ context.Context, input *store.ClassMemberEnrollment) (*store.ClassEnrollmentResult, error) {
	return commitBatchOutcome(s.ledger, "ClassMember.Enroll", input.Command, input, &store.ClassEnrollmentResult{Membership: input.Member})
}

func (s *batchClassMemberStore) End(_ context.Context, input *store.ClassMemberEnd) (*model.ClassMember, error) {
	return commitBatchOutcome(s.ledger, "ClassMember.End", input.Command, input,
		&model.ClassMember{ID: input.ID, ClassID: s.classID, UserID: model.NewUserID()})
}

// batchRoleStore resolves every Role as one that grants class.view.
type batchRoleStore struct{ store.RoleStore }

func (batchRoleStore) Get(_ context.Context, id model.RoleID) (*model.Role, error) {
	return &model.Role{ID: id, Name: "teacher", Permissions: []string{string(model.ActionClassView)}, UpdatedAt: model.TimeFromMillis(1)}, nil
}

// batchRoleBindingStore resolves every Role Binding in one Class.
type batchRoleBindingStore struct {
	store.RoleBindingStore
	ledger  *batchLedger
	classID string
}

func (s *batchRoleBindingStore) Get(_ context.Context, id model.RoleBindingID) (*model.RoleBinding, error) {
	return &model.RoleBinding{ID: id, UserID: model.NewUserID(), RoleID: model.NewRoleID(), ScopeType: model.RoleScopeClass, ScopeID: s.classID}, nil
}

func (s *batchRoleBindingStore) Save(_ context.Context, input *store.RoleBindingCreation) (*model.RoleBinding, error) {
	saved := *input.Binding
	saved.ID = model.NewRoleBindingID()
	return commitBatchOutcome(s.ledger, "RoleBinding.Save", input.Command, input, &saved)
}

func (s *batchRoleBindingStore) End(_ context.Context, input *store.RoleBindingEnd) (*model.RoleBinding, error) {
	return commitBatchOutcome(s.ledger, "RoleBinding.End", input.Command, input, &model.RoleBinding{ID: input.ID, UserID: model.NewUserID()})
}

type batchSessionStore struct {
	store.SessionStore
	ledger *batchLedger
}

func (s *batchSessionStore) RevokeAllForUser(_ context.Context, input *store.UserSessionsRevocation) (*store.UserSessionsRevocationResult, error) {
	return commitBatchOutcome(s.ledger, "Session.RevokeAllForUser", input.Command, input, &store.UserSessionsRevocationResult{})
}

type academicAdministrationOutcomeLookupFake struct {
	store.CommandOutcomeStore
	found   bool
	command *store.CommandIdempotency
}

func (f *academicAdministrationOutcomeLookupFake) Has(_ context.Context, command *store.CommandIdempotency) (bool, error) {
	f.command = command
	return f.found, nil
}

// batchKeyDigest is the key digest of a batch row's command identity.
func batchKeyDigest(batchKey, rowKey string) [sha256.Size]byte {
	return sha256.Sum256([]byte(invitationBatchItemIdempotencyKey(batchKey, rowKey)))
}

func TestAcademicAdministrationBatchRetainsStableSemanticDuplicateDisposition(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	userID, classID := model.NewUserID().String(), model.NewClassID().String()
	inv := batchCaller(now)
	world := newBatchWorld(t, now, batchWorldOptions{callers: []model.UserID{inv.Principal().UserID}, scopeID: classID})

	result, err := world.service.RunAcademicAdministrationBatch(context.Background(), inv, RunAcademicAdministrationBatchCommand{
		Operation: AcademicAdministrationClassEnroll, ScopeType: model.RoleScopeClass, ScopeID: classID,
		IdempotencyKey: "batch", Items: []AcademicAdministrationBatchItemCommand{
			{IdempotencyKey: "b", UserID: model.UserID(userID)}, {IdempotencyKey: "a", UserID: model.UserID(userID)},
		},
	})
	require.NoError(t, err)
	calls := world.ledger.calls
	require.Equal(t, []string{"ClassMember.Enroll", "ClassMember.Enroll"}, world.ledger.methods(), "duplicate result = %#v", result)
	// The canonical row "a" runs first and commits; "b" reaches the Store bound
	// to it, and the Store answers with the canonical outcome and marks it a
	// duplicate.
	require.Equal(t, batchKeyDigest("batch", "a"), calls[0].command.KeyDigest, "duplicate result = %#v", result)
	require.Equal(t, "class_member.enroll", calls[0].command.Operation, "duplicate result = %#v", result)
	require.Zero(t, calls[0].command.Batch.DuplicateOfKeyDigest, "duplicate result = %#v", result)
	require.False(t, calls[0].command.Batch.Duplicate, "duplicate result = %#v", result)
	require.Equal(t, batchKeyDigest("batch", "b"), calls[1].command.KeyDigest, "duplicate result = %#v", result)
	require.Equal(t, batchKeyDigest("batch", "a"), calls[1].command.Batch.DuplicateOfKeyDigest, "duplicate result = %#v", result)
	require.Equal(t, calls[0].command.Batch.GroupDigest, calls[1].command.Batch.GroupDigest, "duplicate result = %#v", result)
	require.True(t, calls[1].command.Batch.Duplicate, "duplicate result = %#v", result)
	require.Equal(t, "onboarding_batch.duplicate", result.Items[0].ErrorCode, "duplicate result = %#v", result)
	require.Equal(t, InvitationBatchItemSucceeded, result.Items[1].Status, "duplicate result = %#v", result)
	require.Equal(t, 1, result.Succeeded, "duplicate result = %#v", result)
	require.Equal(t, 1, result.Failed, "duplicate result = %#v", result)
}

func TestAcademicAdministrationBatchLooksUpRetainedOutcomeBeforeDispatch(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	inv := batchCaller(now)
	classID := model.NewClassID().String()
	lookup := &academicAdministrationOutcomeLookupFake{found: true}
	world := newBatchWorld(t, now, batchWorldOptions{callers: []model.UserID{inv.Principal().UserID}, scopeID: classID, outcomes: lookup})
	result, err := world.service.RunAcademicAdministrationBatch(context.Background(), inv, RunAcademicAdministrationBatchCommand{Operation: AcademicAdministrationClassEnroll,
		ScopeType: model.RoleScopeClass, ScopeID: classID, IdempotencyKey: "batch",
		Items: []AcademicAdministrationBatchItemCommand{{IdempotencyKey: "row", UserID: model.NewUserID()}}})
	require.NoError(t, err, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Equal(t, 1, result.Succeeded, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.NotNil(t, lookup.command, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Equal(t, "class_member.enroll", lookup.command.Operation, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Equal(t, inv.Principal().UserID, lookup.command.UserID, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Equal(t, batchKeyDigest("batch", "row"), lookup.command.KeyDigest, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Equal(t, []string{"ClassMember.Enroll"}, world.ledger.methods(), "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	// A retained outcome runs the enrollment's replay path: it reads no current
	// membership or recipient and freezes no notice.
	enrollment := world.ledger.calls[0].input.(*store.ClassMemberEnrollment)
	require.Equal(t, lookup.command.KeyDigest, enrollment.Command.KeyDigest, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Nil(t, enrollment.Notice, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Equal(t, int64(1), enrollment.ExpectedRecipientRevision, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
	require.Empty(t, world.classMembers.reads, "retained lookup result=%#v command=%#v calls=%#v", result, lookup.command, world.ledger.calls)
}

func TestAcademicAdministrationBatchPreservesDistinctEffectiveHistories(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	inv := batchCaller(now)
	world := newBatchWorld(t, now, batchWorldOptions{callers: []model.UserID{inv.Principal().UserID}})
	userID := model.NewUserID().String()
	result, err := world.service.RunAcademicAdministrationBatch(context.Background(), inv, RunAcademicAdministrationBatchCommand{Operation: AcademicAdministrationAffiliationAdd,
		ScopeType: model.RoleScopeInstitution, ScopeID: world.access.Institution.ID.String(), IdempotencyKey: "batch",
		Items: []AcademicAdministrationBatchItemCommand{
			{IdempotencyKey: "a", UserID: model.UserID(userID), AffiliationKind: model.AffiliationStaff, StartAt: 100, EndAt: 200},
			{IdempotencyKey: "b", UserID: model.UserID(userID), AffiliationKind: model.AffiliationStaff, StartAt: 300, EndAt: 400},
		}})
	require.NoError(t, err, "distinct histories result=%#v calls=%#v", result, world.ledger.calls)
	require.Equal(t, 2, result.Succeeded, "distinct histories result=%#v calls=%#v", result, world.ledger.calls)
	require.Equal(t, 0, result.Failed, "distinct histories result=%#v calls=%#v", result, world.ledger.calls)
	require.Equal(t, []string{"Affiliation.Create", "Affiliation.Create"}, world.ledger.methods(), "distinct histories result=%#v calls=%#v", result, world.ledger.calls)
	for _, call := range world.ledger.calls {
		require.Zero(t, call.command.Batch.DuplicateOfKeyDigest, "each history is its own canonical row")
		require.False(t, call.command.Batch.Duplicate, "each history is its own canonical row")
	}
	require.NotEqual(t, world.ledger.calls[0].command.Batch.GroupDigest, world.ledger.calls[1].command.Batch.GroupDigest)
}

func TestAcademicAdministrationBatchRunsClosedRowsThroughOrdinaryCommands(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	inv := batchCaller(now)
	classID := model.NewClassID().String()
	previousID := model.NewClassMemberID()
	userID := model.NewUserID()
	previous := &model.ClassMember{ID: previousID, ClassID: model.NewClassID(), AcademicPeriodID: batchPeriodID, UserID: userID,
		Revision: 1, StartsAt: model.TimeFromMillis(1_000)}
	world := newBatchWorld(t, now, batchWorldOptions{callers: []model.UserID{inv.Principal().UserID}, scopeID: classID,
		active: []*model.ClassMember{previous}})

	result, err := world.service.RunAcademicAdministrationBatch(context.Background(), inv, RunAcademicAdministrationBatchCommand{
		Operation: AcademicAdministrationClassTransfer, ScopeType: model.RoleScopeClass, ScopeID: classID,
		IdempotencyKey: "batch-key", Items: []AcademicAdministrationBatchItemCommand{
			{IdempotencyKey: "row-1", UserID: userID, RelationshipID: previousID.String()},
			{IdempotencyKey: "repeated", UserID: model.NewUserID(), RelationshipID: model.NewClassMemberID().String()},
			{IdempotencyKey: "repeated", UserID: model.NewUserID(), RelationshipID: model.NewClassMemberID().String()},
		},
	})
	require.NoError(t, err)
	// One batch decision at the Class, then the ordinary transfer's own
	// decisions at the destination and the source Class.
	decisions := world.access.Audits.Decisions()
	require.Equal(t, []model.Action{model.ActionOnboardingBatchManage, model.ActionClassMembersManage, model.ActionClassMembersManage},
		decidedActions(world.access))
	require.Equal(t, model.Resource{Type: model.ResourceClass, ID: classID}, decisions[0].Resource)
	require.Equal(t, model.Resource{Type: model.ResourceClass, ID: classID}, decisions[1].Resource)
	require.Equal(t, model.Resource{Type: model.ResourceClass, ID: previous.ClassID.String()}, decisions[2].Resource)
	require.Equal(t, 1, result.Succeeded, "Run() = %#v", result)
	require.Equal(t, 2, result.Failed, "Run() = %#v", result)
	require.Equal(t, 0, result.NoOp, "Run() = %#v", result)
	require.Equal(t, []string{"ClassMember.Enroll"}, world.ledger.methods(), "Run() = %#v", result)
	enrollment := world.ledger.calls[0].input.(*store.ClassMemberEnrollment)
	require.Equal(t, model.ClassID(classID), enrollment.Member.ClassID)
	require.Equal(t, userID, enrollment.Member.UserID)
	require.Equal(t, previousID, enrollment.ExpectedPreviousID)
	require.Equal(t, batchKeyDigest("batch-key", "row-1"), enrollment.Command.KeyDigest)
	// The row ran as a transfer: the destination audit nests the source audit.
	require.False(t, enrollment.PreviousAuditEventID.IsZero(), "a transfer audits the source membership")
	var operations []string
	for _, attempt := range world.access.Audits.Attempts() {
		operation, _ := auditParameters(t, attempt)
		operations = append(operations, operation)
	}
	require.Equal(t, []string{"transfer_in", "transfer_out"}, operations)
}

func TestAcademicAdministrationBatchDispatchesEveryClosedOperation(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	userID, relationshipID, roleID := model.NewUserID(), model.NewID(), model.NewRoleID()
	cases := []struct {
		operation AcademicAdministrationBatchOperation
		scopeType model.RoleScopeType
		item      AcademicAdministrationBatchItemCommand
		// method is the Store mutation the ordinary command reaches and
		// command the operation of the command identity it carries.
		method  string
		command string
	}{
		{AcademicAdministrationAffiliationAdd, model.RoleScopeInstitution, AcademicAdministrationBatchItemCommand{UserID: userID, AffiliationKind: model.AffiliationStaff}, "Affiliation.Create", "affiliation.add"},
		{AcademicAdministrationAffiliationEnd, model.RoleScopeInstitution, AcademicAdministrationBatchItemCommand{RelationshipID: relationshipID}, "Affiliation.End", "affiliation.end"},
		{AcademicAdministrationAcademicUnitMemberAdd, model.RoleScopeAcademicUnit, AcademicAdministrationBatchItemCommand{UserID: userID}, "AcademicUnitMember.Create", "academic_unit_member.add"},
		{AcademicAdministrationAcademicUnitMemberEnd, model.RoleScopeAcademicUnit, AcademicAdministrationBatchItemCommand{RelationshipID: relationshipID}, "AcademicUnitMember.End", "academic_unit_member.end"},
		{AcademicAdministrationClassEnroll, model.RoleScopeClass, AcademicAdministrationBatchItemCommand{UserID: userID}, "ClassMember.Enroll", "class_member.enroll"},
		{AcademicAdministrationClassEnd, model.RoleScopeClass, AcademicAdministrationBatchItemCommand{RelationshipID: relationshipID}, "ClassMember.End", "class_member.end"},
		{AcademicAdministrationClassTransfer, model.RoleScopeClass, AcademicAdministrationBatchItemCommand{UserID: userID, RelationshipID: relationshipID}, "ClassMember.Enroll", "class_member.enroll"},
		{AcademicAdministrationRoleBindingCreate, model.RoleScopeClass, AcademicAdministrationBatchItemCommand{UserID: userID, RoleID: roleID}, "RoleBinding.Save", "role_binding.create"},
		{AcademicAdministrationRoleBindingEnd, model.RoleScopeClass, AcademicAdministrationBatchItemCommand{RelationshipID: relationshipID}, "RoleBinding.End", "role_binding.end"},
		{AcademicAdministrationUserEnable, model.RoleScopeInstitution, AcademicAdministrationBatchItemCommand{UserID: userID}, "User.SetDisabled", "user.enabled_state"},
		{AcademicAdministrationUserDisable, model.RoleScopeInstitution, AcademicAdministrationBatchItemCommand{UserID: userID}, "User.SetDisabled", "user.enabled_state"},
		{AcademicAdministrationUserSessionsRevoke, model.RoleScopeInstitution, AcademicAdministrationBatchItemCommand{UserID: userID}, "Session.RevokeAllForUser", "user_sessions.revoke"},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.operation), func(t *testing.T) {
			inv := batchCaller(now)
			scopeID := model.NewID()
			// The transferred membership is the User's current one in another
			// Class of the same Academic Period.
			previous := &model.ClassMember{ID: model.ClassMemberID(relationshipID), ClassID: model.NewClassID(), AcademicPeriodID: batchPeriodID,
				UserID: userID, Revision: 1, StartsAt: model.TimeFromMillis(1_000)}
			options := batchWorldOptions{callers: []model.UserID{inv.Principal().UserID}, scopeID: scopeID}
			if testCase.operation == AcademicAdministrationClassTransfer {
				options.active = []*model.ClassMember{previous}
			}
			world := newBatchWorld(t, now, options)
			if testCase.scopeType == model.RoleScopeInstitution {
				scopeID = world.access.Institution.ID.String()
			}
			testCase.item.IdempotencyKey = "row"
			result, err := world.service.RunAcademicAdministrationBatch(context.Background(), inv, RunAcademicAdministrationBatchCommand{Operation: testCase.operation,
				ScopeType: testCase.scopeType, ScopeID: scopeID, IdempotencyKey: "batch", Items: []AcademicAdministrationBatchItemCommand{testCase.item}})
			require.NoError(t, err, "Run(%s) = %#v, calls %v", testCase.operation, result, world.ledger.methods())
			require.Equal(t, 1, result.Succeeded, "Run(%s) = %#v, calls %v", testCase.operation, result, world.ledger.methods())
			require.Equal(t, []string{testCase.method}, world.ledger.methods(), "Run(%s) = %#v", testCase.operation, result)
			call := world.ledger.calls[0]
			require.Equal(t, testCase.command, call.command.Operation, "Run(%s) = %#v", testCase.operation, result)
			require.Equal(t, batchKeyDigest("batch", "row"), call.command.KeyDigest, "Run(%s) = %#v", testCase.operation, result)
			switch input := call.input.(type) {
			case *store.ClassMemberEnrollment:
				// Enrollment and transfer share the Store mutation; only a
				// transfer names the membership it closes.
				transfer := testCase.operation == AcademicAdministrationClassTransfer
				require.Equal(t, transfer, input.ExpectedPreviousID == model.ClassMemberID(relationshipID), "Run(%s) = %#v", testCase.operation, input)
				require.Equal(t, transfer, !input.ExpectedPreviousID.IsZero(), "Run(%s) = %#v", testCase.operation, input)
			case *store.UserDisabledStateChange:
				require.Equal(t, testCase.operation == AcademicAdministrationUserDisable, input.Disabled, "Run(%s) = %#v", testCase.operation, input)
			}
		})
	}
}

func TestAcademicAdministrationSensitiveBatchRequiresStrongRecentInteractiveSession(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	caller := model.Principal{UserID: model.NewUserID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialPersonalAccessToken}
	world := newBatchWorld(t, now, batchWorldOptions{callers: []model.UserID{caller.UserID}})
	userID := model.NewUserID()

	_, err := world.service.RunAcademicAdministrationBatch(context.Background(), invocation.New(caller, model.RequestMetadata{}),
		RunAcademicAdministrationBatchCommand{Operation: AcademicAdministrationUserDisable, ScopeType: model.RoleScopeInstitution,
			ScopeID: world.access.Institution.ID.String(), IdempotencyKey: "batch", Items: []AcademicAdministrationBatchItemCommand{{IdempotencyKey: "row", UserID: userID}}})
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "PAT sensitive batch error = %v", err)
	require.Empty(t, world.ledger.calls, "a refused batch reaches no Store mutation")
	require.Empty(t, world.access.Audits.Attempts(), "a refused batch begins no mutation audit")
}
