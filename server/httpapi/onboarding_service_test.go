// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// onboardingFixture is the real onboarding.Service the Invitation, browser
// Invitation, import, Student Progression, batch, filter preview and public
// registration handlers call. It runs over in-memory Stores a test seeds and
// reads back, with the real authorizer and auditor, the real mail Composer,
// the real Argon2id hasher and attempt accounting, and the real identity,
// academic, administration and user Services the batches and previews
// dispatch to. The caller holds every Action across the Institution and has
// a live web Session, so a test asserts what each request became at the
// Store rather than what a fake application received.
type onboardingFixture struct {
	access       *accesstest.Fixture
	callerID     model.UserID
	sessionID    model.SessionID
	unit         *model.AcademicUnit
	period       *model.AcademicPeriod
	classes      *onboardingClasses
	levels       *onboardingLevels
	roles        *onboardingRoles
	users        *onboardingUsers
	invitations  *onboardingInvitations
	browser      *onboardingBrowserTransactions
	imports      *onboardingImportRecords
	files        *onboardingImportFiles
	classMembers *onboardingClassMembers
	accessPolicy *onboardingAccessPolicy
	attempts     *identityCache
	mail         *identityMailSender
	renders      *mailtest.Renders
	publisher    *realtimetest.Publisher

	service        *onboarding.Service
	administration *administration.Service
}

// newOnboardingFixture builds the Services over one Institution with one
// Academic Unit and one Academic Period that holds every Class a test adds.
func newOnboardingFixture(t *testing.T) *onboardingFixture {
	t.Helper()
	now := time.Now().UTC()
	fixture := &onboardingFixture{
		callerID:     model.NewUserID(),
		sessionID:    model.NewSessionID(),
		unit:         &model.AcademicUnit{ID: model.NewAcademicUnitID(), Name: "computing", DisplayName: "Computing", Revision: 1},
		classes:      &onboardingClasses{byID: map[model.ClassID]*model.Class{}},
		levels:       &onboardingLevels{byID: map[model.ProgrammeLevelID]*model.ProgrammeLevel{}},
		roles:        &onboardingRoles{byID: map[model.RoleID]*model.Role{}},
		invitations:  newOnboardingInvitations(),
		browser:      &onboardingBrowserTransactions{},
		imports:      &onboardingImportRecords{byID: map[model.OnboardingImportID]*store.OnboardingImport{}, rows: map[model.OnboardingImportID][]store.OnboardingImportRow{}},
		files:        &onboardingImportFiles{staged: map[model.OnboardingImportID][]byte{}},
		classMembers: &onboardingClassMembers{byID: map[model.ClassMemberID]*model.ClassMember{}, replayUsers: map[model.UserID]bool{}},
		attempts:     newIdentityCache(),
		mail:         &identityMailSender{enabled: true},
		publisher:    &realtimetest.Publisher{},
	}
	fixture.users = newOnboardingUsers(&model.User{ID: fixture.callerID, Username: "operator", Email: "operator@example.edu",
		CreatedAt: now, UpdatedAt: now, Revision: 1, Locale: "en", Timezone: "UTC",
		DefaultProfilePictureSeed: strings.Repeat("a", model.ProfilePictureSeedLength)})
	fixture.classes.unitID = fixture.unit.ID
	fixture.access = accesstest.New(t, accesstest.Options{
		Grants:          []accesstest.Grant{institutionWide(fixture.callerID, onboardingAllActions()...)},
		Units:           []*model.AcademicUnit{fixture.unit},
		Users:           fixture.users,
		Classes:         fixture.classes,
		AcademicPeriods: onboardingPeriods{classes: fixture.classes},
	})
	fixture.period = &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), Revision: 1, Name: "2026", DisplayName: "2026",
		Owner:    model.AcademicPeriodOwner{InstitutionID: fixture.access.Institution.ID},
		StartsAt: now.Add(-30 * 24 * time.Hour), EndsAt: now.Add(300 * 24 * time.Hour)}
	fixture.classes.periods = map[model.AcademicPeriodID]*model.AcademicPeriod{fixture.period.ID: fixture.period}
	fixture.accessPolicy = &onboardingAccessPolicy{identityAccessPolicy: newIdentityAccessPolicy(t, func(policy *model.AccessPolicy) {
		policy.PublicRegistrationEnabled = true
	})}

	sessions := &onboardingSessions{session: &model.Session{ID: fixture.sessionID, UserID: fixture.callerID,
		ClientType: model.SessionClientWeb, AuthenticatedAt: now, LastActivityAt: now,
		IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour)}}
	identities := newIdentityFixture(t, identityOptions{Stores: identityStores{
		Users: fixture.users, Sessions: sessions, MFA: &onboardingMFA{}, AccessPolicy: fixture.accessPolicy,
	}})

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	var renderer *mail.TemplateRenderer
	renderer, fixture.renders = recordingHTTPMailRenderer(t)
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: renderer, Sender: fixture.mail, Sealer: sealer})
	require.NoError(t, err)
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: identityPasswordPolicy()})
	require.NoError(t, err)
	attempts, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: fixture.attempts})
	require.NoError(t, err)

	catalog := onboardingCatalog{fixture: fixture}
	clock := time.Now
	academicService, err := academic.New(academic.Dependencies{Store: catalog, Authorizer: fixture.access.Authorizer,
		Auditor: fixture.access.Auditor, Mail: composer, Publisher: fixture.publisher, Now: clock, NewID: model.NewID})
	require.NoError(t, err)
	fixture.administration, err = administration.New(administration.Dependencies{Store: catalog, Authorizer: fixture.access.Authorizer,
		Auditor: fixture.access.Auditor, Capabilities: &identity.DeploymentCapabilities{}, Mail: composer, Publisher: fixture.publisher,
		CanonicalOrigin: identityPublicURL, PasswordMinimumLength: 8, RecentAuthenticationTTL: 15 * time.Minute, Now: clock})
	require.NoError(t, err)
	directory, err := user.New(user.Dependencies{Store: catalog, Authorizer: fixture.access.Authorizer,
		Auditor: fixture.access.Auditor, Publisher: fixture.publisher, Now: clock})
	require.NoError(t, err)

	fixture.service, _, err = onboarding.New(onboarding.Dependencies{
		Store: catalog, Content: fixture.files, Authorizer: fixture.access.Authorizer, Auditor: fixture.access.Auditor,
		Mail: composer, Hasher: hasher, Attempts: attempts, Identity: identities.service, Academic: academicService,
		Administration: fixture.administration, Users: directory, Publisher: fixture.publisher, Wake: func() {},
		NodeID: "node-a", PublicURL: identityPublicURL,
		AccountRecovery: identity.AccountRecoveryPolicy{EmailVerificationTTL: 24 * time.Hour,
			RateLimit: identity.LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 1000, MaximumSourceAttempts: 1000}},
		RecentAuthenticationTTL: 15 * time.Minute,
	})
	require.NoError(t, err)
	return fixture
}

// onboardingAllActions is every Action the catalog defines.
func onboardingAllActions() []model.Action {
	var actions []model.Action
	for _, action := range model.AllActions() {
		actions = append(actions, model.Action(action))
	}
	return actions
}

// principal is the caller's web Session at strength, authenticated now.
func (f *onboardingFixture) principal(strength model.AuthenticationStrength) model.Principal {
	now := time.Now().UTC()
	principal := model.Principal{UserID: f.callerID, SessionID: f.sessionID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: strength,
		ClientType: model.SessionClientWeb, AuthenticatedAt: now}
	if strength == model.AuthenticationMultiFactor {
		principal.MFACompletedAt = model.OptionalTimeFrom(now)
	}
	return principal
}

// addClass stores a live Class of the fixture's Academic Period.
func (f *onboardingFixture) addClass(displayName string) *model.Class {
	class := &model.Class{ID: model.NewClassID(), AcademicPeriodID: f.period.ID, ProgrammeLevelID: model.NewProgrammeLevelID(),
		Name: displayName, DisplayName: displayName, Revision: 1}
	f.classes.add(class)
	return class
}

// addPeriod stores another Institution-owned Academic Period.
func (f *onboardingFixture) addPeriod(startsAt, endsAt time.Time) *model.AcademicPeriod {
	period := &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), Revision: 1, Name: "period", DisplayName: "Period",
		Owner: model.AcademicPeriodOwner{InstitutionID: f.access.Institution.ID}, StartsAt: startsAt, EndsAt: endsAt}
	f.classes.mu.Lock()
	defer f.classes.mu.Unlock()
	f.classes.periods[period.ID] = period
	return period
}

// addDelegableRole stores a custom Role granting class.view, which an
// Invitation may delegate at an Academic Unit or the Institution.
func (f *onboardingFixture) addDelegableRole() *model.Role {
	role := &model.Role{ID: model.NewRoleID(), Name: "reviewer", Permissions: []string{string(model.ActionClassView)}}
	f.roles.mu.Lock()
	defer f.roles.mu.Unlock()
	f.roles.byID[role.ID] = role
	return role
}

// addUser stores an active User.
func (f *onboardingFixture) addUser(username string) *model.User {
	now := time.Now().UTC()
	account := &model.User{ID: model.NewUserID(), Username: username, Email: username + "@example.edu", CreatedAt: now,
		UpdatedAt: now, Revision: 1, Locale: "en", Timezone: "UTC", DefaultProfilePictureSeed: strings.Repeat("a", model.ProfilePictureSeedLength)}
	f.users.add(account)
	return account
}

// pendingStudentInvitation stores a pending Student Class Invitation into
// class issued by the caller, and returns it with its raw claim.
func (f *onboardingFixture) pendingStudentInvitation(t *testing.T, class *model.Class, email string) (*model.Invitation, string) {
	t.Helper()
	claim := model.NewCredentialToken()
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{
		ID: model.NewInvitationID(), TargetEmail: email, ClassID: class.ID, AcademicPeriodID: class.AcademicPeriodID,
		IntendedStartsAt: f.period.StartsAt, InviterUserID: f.callerID, ScopeType: model.RoleScopeClass, ScopeID: class.ID.String(),
		ClaimHash: model.HashInvitationClaim(claim), IssuedAt: time.Now().Add(-time.Hour), Lifetime: model.InvitationLifetime,
	})
	require.NoError(t, err)
	f.invitations.put(invitation)
	return invitation, claim
}

// pendingTeacherInvitation stores a pending teacher Academic Unit Invitation
// with role, and returns it with its raw claim.
func (f *onboardingFixture) pendingTeacherInvitation(t *testing.T, role *model.Role, email string) (*model.Invitation, string) {
	t.Helper()
	claim := model.NewCredentialToken()
	invitation, err := model.NewTeacherAcademicUnitInvitation(model.TeacherAcademicUnitInvitationInput{
		ID: model.NewInvitationID(), TargetEmail: email, AcademicUnitID: f.unit.ID, RoleID: role.ID, RoleActions: role.Permissions,
		IntendedStartsAt: time.Now().Add(-time.Hour), InviterUserID: f.callerID, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: f.unit.ID.String(), ClaimHash: model.HashInvitationClaim(claim), IssuedAt: time.Now().Add(-time.Hour),
		Lifetime: model.InvitationLifetime,
	})
	require.NoError(t, err)
	f.invitations.put(invitation)
	return invitation, claim
}

// pendingRoleInvitation stores a pending Role Invitation of purpose with
// role, at the fixture's Academic Unit or the Institution, and returns it
// with its raw claim.
func (f *onboardingFixture) pendingRoleInvitation(t *testing.T, purpose model.InvitationPurpose, role *model.Role) (*model.Invitation, string) {
	t.Helper()
	claim := model.NewCredentialToken()
	input := model.ScopedRoleInvitationInput{
		ID: model.NewInvitationID(), Purpose: purpose, TargetEmail: "existing@example.edu", RoleID: role.ID, RoleActions: role.Permissions,
		IntendedStartsAt: time.Now().Add(-time.Hour), InviterUserID: f.callerID, ClaimHash: model.HashInvitationClaim(claim),
		IssuedAt: time.Now().Add(-time.Hour), Lifetime: model.InvitationLifetime,
	}
	if purpose == model.InvitationPurposeAcademicUnitRole {
		input.AcademicUnitID, input.ScopeType, input.ScopeID = f.unit.ID, model.RoleScopeAcademicUnit, f.unit.ID.String()
	} else {
		input.ScopeType, input.ScopeID = model.RoleScopeInstitution, f.access.Institution.ID.String()
	}
	invitation, err := model.NewScopedRoleInvitation(input)
	require.NoError(t, err)
	f.invitations.put(invitation)
	return invitation, claim
}

// sourceCounted reports whether the attempt accounting counted one attempt
// of purpose and qualifier from source. The cache keeps only a digest of the
// source, so the key is derived by counting the same attempt in a second,
// empty cache.
func (f *onboardingFixture) sourceCounted(t *testing.T, purpose identity.AttemptPurpose, qualifier, source string) bool {
	t.Helper()
	probe := newIdentityCache()
	accounting, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: probe})
	require.NoError(t, err)
	_, err = accounting.Account(t.Context(), purpose, qualifier, time.Minute, identity.SourceAttemptLimit(source, 1))
	require.NoError(t, err)
	require.Len(t, probe.counters, 1)
	f.attempts.mu.Lock()
	defer f.attempts.mu.Unlock()
	for key := range probe.counters {
		return f.attempts.counters[key] > 0
	}
	return false
}

// onboardingCatalog serves the fixture's Stores to every Service it builds.
// A Store no onboarding request reaches is an inert stand-in that panics if
// called; CommandOutcome is absent, so batches look up no retained outcome.
type onboardingCatalog struct {
	store.Catalog
	fixture *onboardingFixture
}

func (c onboardingCatalog) Invitation() store.InvitationStore { return c.fixture.invitations }
func (c onboardingCatalog) BrowserAuthentication() store.BrowserAuthenticationStore {
	return c.fixture.browser
}
func (c onboardingCatalog) OnboardingImport() store.OnboardingImportStore { return c.fixture.imports }
func (c onboardingCatalog) Institution() store.InstitutionStore {
	return identityInstitution{institution: c.fixture.access.Institution}
}
func (c onboardingCatalog) AcademicUnit() store.AcademicUnitStore {
	return onboardingUnits{unit: c.fixture.unit}
}
func (c onboardingCatalog) Programme() store.ProgrammeStore           { return struct{ store.ProgrammeStore }{} }
func (c onboardingCatalog) ProgrammeLevel() store.ProgrammeLevelStore { return c.fixture.levels }
func (c onboardingCatalog) AcademicPeriod() store.AcademicPeriodStore {
	return onboardingPeriods{classes: c.fixture.classes}
}
func (c onboardingCatalog) Class() store.ClassStore                   { return c.fixture.classes }
func (c onboardingCatalog) ClassMember() store.ClassMemberStore       { return c.fixture.classMembers }
func (c onboardingCatalog) Role() store.RoleStore                     { return c.fixture.roles }
func (c onboardingCatalog) User() store.UserStore                     { return c.fixture.users }
func (c onboardingCatalog) AccessPolicy() store.AccessPolicyStore     { return c.fixture.accessPolicy }
func (c onboardingCatalog) CommandOutcome() store.CommandOutcomeStore { return nil }
func (c onboardingCatalog) ExamSitting() store.ExamSittingStore       { return onboardingSittings{} }
func (c onboardingCatalog) Affiliation() store.AffiliationStore {
	return struct{ store.AffiliationStore }{}
}
func (c onboardingCatalog) RoleBinding() store.RoleBindingStore {
	return struct{ store.RoleBindingStore }{}
}
func (c onboardingCatalog) Audit() store.AuditStore                   { return struct{ store.AuditStore }{} }
func (c onboardingCatalog) Retention() store.RetentionStore           { return struct{ store.RetentionStore }{} }
func (c onboardingCatalog) PluginSettings() store.PluginSettingsStore { return nil }
func (c onboardingCatalog) File() store.FileStore                     { return struct{ store.FileStore }{} }
func (c onboardingCatalog) UserSettings() store.UserSettingsStore {
	return struct{ store.UserSettingsStore }{}
}
func (c onboardingCatalog) EnvironmentProfile() store.EnvironmentProfileStore {
	return nil
}
func (c onboardingCatalog) RetentionPolicy() store.RetentionPolicyStore {
	return struct{ store.RetentionPolicyStore }{}
}
func (c onboardingCatalog) PersonalAccessToken() store.PersonalAccessTokenStore {
	return struct{ store.PersonalAccessTokenStore }{}
}
func (c onboardingCatalog) AcademicUnitMember() store.AcademicUnitMemberStore {
	return struct{ store.AcademicUnitMemberStore }{}
}

// onboardingAccessPolicy is the current Access Policy, counting its reads.
type onboardingAccessPolicy struct {
	*identityAccessPolicy
	mu    sync.Mutex
	reads int
}

func (s *onboardingAccessPolicy) Get(ctx context.Context, revision int) (*store.AccessPolicySnapshot, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.identityAccessPolicy.Get(ctx, revision)
}

// readCount is how many times the policy was read.
func (s *onboardingAccessPolicy) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// onboardingSessions holds the caller's one live Session.
type onboardingSessions struct {
	store.SessionStore
	session *model.Session
}

func (s *onboardingSessions) Get(_ context.Context, id model.SessionID) (*model.Session, error) {
	if s.session == nil || s.session.ID != id {
		return nil, store.NewErrNotFound("session", id.String())
	}
	copied := *s.session
	return &copied, nil
}

// onboardingMFA reports a key ring with no fenced primary and no recovery in
// progress for any User.
type onboardingMFA struct{ identityMFAKeys }

func (*onboardingMFA) GetRecoveryState(context.Context, model.UserID) (*model.UserMFARecovery, error) {
	return &model.UserMFARecovery{}, nil
}

// onboardingUnits is the one Academic Unit, a root of the Institution.
type onboardingUnits struct {
	store.AcademicUnitStore
	unit *model.AcademicUnit
}

func (s onboardingUnits) Get(_ context.Context, id model.AcademicUnitID) (*model.AcademicUnit, error) {
	if id != s.unit.ID {
		return nil, store.NewErrNotFound("academic_unit", id.String())
	}
	copied := *s.unit
	return &copied, nil
}

func (s onboardingUnits) ListAncestors(_ context.Context, id model.AcademicUnitID) ([]*model.AcademicUnit, error) {
	if id != s.unit.ID {
		return nil, store.NewErrNotFound("academic_unit", id.String())
	}
	return []*model.AcademicUnit{s.unit}, nil
}

// onboardingClasses holds the Classes, every one in the fixture's Academic
// Unit, and the Academic Periods they run in.
type onboardingClasses struct {
	store.ClassStore
	mu      sync.Mutex
	unitID  model.AcademicUnitID
	byID    map[model.ClassID]*model.Class
	periods map[model.AcademicPeriodID]*model.AcademicPeriod
}

func (s *onboardingClasses) add(class *model.Class) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[class.ID] = class
}

func (s *onboardingClasses) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	class, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("class", id.String())
	}
	copied := *class
	return &copied, nil
}

func (s *onboardingClasses) GetAcademicUnitID(_ context.Context, id model.ClassID) (model.AcademicUnitID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return "", store.NewErrNotFound("class", id.String())
	}
	return s.unitID, nil
}

// onboardingPeriods reads the Academic Periods onboardingClasses holds.
type onboardingPeriods struct {
	store.AcademicPeriodStore
	classes *onboardingClasses
}

func (s onboardingPeriods) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	s.classes.mu.Lock()
	defer s.classes.mu.Unlock()
	period, ok := s.classes.periods[id]
	if !ok {
		return nil, store.NewErrNotFound("academic_period", id.String())
	}
	copied := *period
	return &copied, nil
}

// onboardingLevels holds the Programme Levels a test adds.
type onboardingLevels struct {
	store.ProgrammeLevelStore
	byID map[model.ProgrammeLevelID]*model.ProgrammeLevel
}

func (s *onboardingLevels) Get(_ context.Context, id model.ProgrammeLevelID) (*model.ProgrammeLevel, error) {
	level, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("programme_level", id.String())
	}
	return level, nil
}

// onboardingRoles holds the Roles a test adds.
type onboardingRoles struct {
	store.RoleStore
	mu   sync.Mutex
	byID map[model.RoleID]*model.Role
}

func (s *onboardingRoles) Get(_ context.Context, id model.RoleID) (*model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	role, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("role", id.String())
	}
	copied := *role
	return &copied, nil
}

func (s *onboardingRoles) GetByIDs(_ context.Context, ids []model.RoleID) ([]*model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roles := []*model.Role{}
	for _, id := range ids {
		if role, ok := s.byID[id]; ok {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

func (s *onboardingRoles) GetByName(_ context.Context, name string) (*model.Role, error) {
	return nil, store.NewErrNotFound("role", name)
}

// onboardingUsers holds the Users, records each public registration and
// answers directory reads with every stored User.
type onboardingUsers struct {
	store.UserStore
	mu            sync.Mutex
	byID          map[model.UserID]*model.User
	registrations []*store.PublicLocalUserRegistration
	listOptions   []store.UserListOptions
}

func newOnboardingUsers(users ...*model.User) *onboardingUsers {
	s := &onboardingUsers{byID: map[model.UserID]*model.User{}}
	for _, account := range users {
		s.add(account)
	}
	return s
}

func (s *onboardingUsers) add(account *model.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[account.ID] = account
}

func (s *onboardingUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("user", id.String())
	}
	copied := *account
	return &copied, nil
}

func (s *onboardingUsers) List(_ context.Context, options store.UserListOptions) ([]*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listOptions = append(s.listOptions, options)
	return []*model.User{}, nil
}

func (s *onboardingUsers) RegisterLocal(_ context.Context, input *store.PublicLocalUserRegistration) (*store.PublicLocalUserRegistrationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registrations = append(s.registrations, input)
	return &store.PublicLocalUserRegistrationResult{}, nil
}

// registered returns the registrations that reached the Store.
func (s *onboardingUsers) registered() []*store.PublicLocalUserRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.registrations)
}

// onboardingInvitations holds the Invitations by ID and by claim hash and
// records every command input the Invitation use cases sent. An issue for
// an address in replayEmails answers as an exact replay; one in
// conflictEmails is refused as a conflict.
type onboardingInvitations struct {
	store.InvitationStore
	mu      sync.Mutex
	byID    map[model.InvitationID]*model.Invitation
	byClaim map[string]*model.Invitation
	// listed is what List returns, newest first; more marks a further page.
	listed []*model.Invitation
	more   bool

	replayEmails   map[string]bool
	conflictEmails map[string]bool
	// replayAcceptance makes every acceptance answer as an exact replay.
	replayAcceptance bool

	listOptions          []store.InvitationListOptions
	administrationReads  []model.InvitationID
	studentIssues        []*store.StudentClassInvitationIssue
	studentIssueCommands []*store.CommandIdempotency
	teacherIssues        []*store.TeacherAcademicUnitInvitationIssue
	scopedRoleIssues     []*store.ScopedRoleInvitationIssue
	resends              []*store.InvitationResend
	revocations          []*store.InvitationRevocation
	replacements         []*store.InvitationReplacement
	studentAcceptances   []*store.StudentClassInvitationAcceptance
	teacherAcceptances   []*store.TeacherAcademicUnitInvitationAcceptance
	scopedAcceptances    []*store.ScopedRoleInvitationAcceptance
}

func newOnboardingInvitations() *onboardingInvitations {
	return &onboardingInvitations{
		byID: map[model.InvitationID]*model.Invitation{}, byClaim: map[string]*model.Invitation{},
		replayEmails: map[string]bool{}, conflictEmails: map[string]bool{},
	}
}

func (s *onboardingInvitations) put(invitation *model.Invitation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[invitation.ID] = invitation
	s.byClaim[invitation.ClaimHash] = invitation
}

// record is an Invitation's administration record with a queued delivery.
func onboardingInvitationRecord(invitation *model.Invitation) *store.InvitationAdministrationRecord {
	return &store.InvitationAdministrationRecord{Invitation: invitation, Delivery: &store.InvitationDeliverySummary{
		TemplateKey: model.MailTemplateAccessStudentClassInvitation, State: model.MailDeliveryQueued,
		MaskedRecipient: "s***@example.edu", CreatedAt: invitation.CreatedAt, UpdatedAt: invitation.UpdatedAt,
		Deadline: invitation.ExpiresAt,
	}}
}

func (s *onboardingInvitations) List(_ context.Context, options store.InvitationListOptions) (*store.InvitationPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listOptions = append(s.listOptions, options)
	page := &store.InvitationPage{More: s.more}
	for _, invitation := range s.listed {
		page.Items = append(page.Items, onboardingInvitationRecord(invitation))
	}
	return page, nil
}

func (s *onboardingInvitations) Count(context.Context, store.InvitationListOptions, int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.listed), nil
}

// lastList is the options of the most recent List.
func (s *onboardingInvitations) lastList(t *testing.T) store.InvitationListOptions {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.listOptions, "the request never reached the Invitation list")
	return s.listOptions[len(s.listOptions)-1]
}

func (s *onboardingInvitations) GetForAdministration(_ context.Context, id model.InvitationID, _ store.InvitationVisibilityScope) (*store.InvitationAdministrationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.administrationReads = append(s.administrationReads, id)
	invitation, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("invitation", id.String())
	}
	return onboardingInvitationRecord(invitation), nil
}

func (s *onboardingInvitations) GetByClaimHash(_ context.Context, hash string) (*model.Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invitation, ok := s.byClaim[hash]
	if !ok {
		return nil, store.NewErrNotFound("invitation", "")
	}
	copied := *invitation
	return &copied, nil
}

func (s *onboardingInvitations) FindCommandOutcome(context.Context, *store.CommandIdempotency) (*store.InvitationCommandResult, error) {
	return nil, store.NewErrNotFound("command_outcome", "")
}

func (s *onboardingInvitations) IssueStudentClass(_ context.Context, input *store.StudentClassInvitationIssue) (*model.Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.studentIssues = append(s.studentIssues, input)
	return input.Invitation, nil
}

func (s *onboardingInvitations) IssueStudentClassIdempotently(_ context.Context, input *store.StudentClassInvitationIssue, command *store.CommandIdempotency) (*store.InvitationCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.studentIssues = append(s.studentIssues, input)
	s.studentIssueCommands = append(s.studentIssueCommands, command)
	switch {
	case s.conflictEmails[input.Invitation.TargetEmail]:
		return nil, store.NewErrConflict("invitation", "pending_target", nil)
	case s.replayEmails[input.Invitation.TargetEmail]:
		return &store.InvitationCommandResult{Invitation: input.Invitation, Replayed: true}, nil
	default:
		return &store.InvitationCommandResult{Invitation: input.Invitation}, nil
	}
}

func (s *onboardingInvitations) IssueTeacherAcademicUnit(_ context.Context, input *store.TeacherAcademicUnitInvitationIssue) (*model.Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teacherIssues = append(s.teacherIssues, input)
	return input.Invitation, nil
}

func (s *onboardingInvitations) IssueScopedRole(_ context.Context, input *store.ScopedRoleInvitationIssue) (*model.Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopedRoleIssues = append(s.scopedRoleIssues, input)
	return input.Invitation, nil
}

func (s *onboardingInvitations) Resend(_ context.Context, input *store.InvitationResend) (*store.InvitationAdministrationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resends = append(s.resends, input)
	return onboardingInvitationRecord(s.byID[input.ID]), nil
}

func (s *onboardingInvitations) Revoke(_ context.Context, input *store.InvitationRevocation) (*store.InvitationAdministrationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revocations = append(s.revocations, input)
	revoked := *s.byID[input.ID]
	revoked.State = model.InvitationRevoked
	return onboardingInvitationRecord(&revoked), nil
}

func (s *onboardingInvitations) Replace(_ context.Context, input *store.InvitationReplacement) (*store.InvitationAdministrationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replacements = append(s.replacements, input)
	return onboardingInvitationRecord(input.Replacement), nil
}

func (s *onboardingInvitations) AcceptStudentClass(_ context.Context, input *store.StudentClassInvitationAcceptance) (*store.StudentClassInvitationAcceptanceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.studentAcceptances = append(s.studentAcceptances, input)
	invitation, ok := s.byClaim[input.ClaimHash]
	if !ok {
		return nil, store.NewErrNotFound("invitation", "")
	}
	return &store.StudentClassInvitationAcceptanceResult{Invitation: invitation, User: input.User, Affiliation: input.Affiliation,
		ClassMember: input.ClassMember, Replayed: s.replayAcceptance}, nil
}

func (s *onboardingInvitations) AcceptTeacherAcademicUnit(_ context.Context, input *store.TeacherAcademicUnitInvitationAcceptance) (*store.TeacherAcademicUnitInvitationAcceptanceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teacherAcceptances = append(s.teacherAcceptances, input)
	invitation, ok := s.byClaim[input.ClaimHash]
	if !ok {
		return nil, store.NewErrNotFound("invitation", "")
	}
	return &store.TeacherAcademicUnitInvitationAcceptanceResult{Invitation: invitation, User: input.User, Affiliation: input.Affiliation,
		AcademicUnitMember: input.AcademicUnitMember, RoleBinding: input.RoleBinding, Replayed: s.replayAcceptance}, nil
}

func (s *onboardingInvitations) AcceptScopedRole(_ context.Context, input *store.ScopedRoleInvitationAcceptance) (*store.ScopedRoleInvitationAcceptanceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopedAcceptances = append(s.scopedAcceptances, input)
	invitation, ok := s.byClaim[input.ClaimHash]
	if !ok {
		return nil, store.NewErrNotFound("invitation", "")
	}
	return &store.ScopedRoleInvitationAcceptanceResult{Invitation: invitation, User: &model.User{ID: input.UserID},
		RoleBinding: input.RoleBinding, Replayed: s.replayAcceptance}, nil
}

// onboardingBrowserTransactions holds the browser Invitation handoffs it
// created, resolving one by its handle and proof hashes.
type onboardingBrowserTransactions struct {
	store.BrowserAuthenticationStore
	mu        sync.Mutex
	creations []*store.BrowserInvitationTransactionCreation
}

func (s *onboardingBrowserTransactions) CreateInvitation(_ context.Context, input *store.BrowserInvitationTransactionCreation) (*store.BrowserInvitationCreated, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creations = append(s.creations, input)
	return &store.BrowserInvitationCreated{ID: input.ID, ExpiresAt: time.Now().Add(10 * time.Minute)}, nil
}

func (s *onboardingBrowserTransactions) ResolveInvitation(_ context.Context, handleHash, proofHash string) (*store.BrowserInvitationResolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, creation := range s.creations {
		if creation.HandleHash == handleHash && creation.BrowserProofHash == proofHash {
			return &store.BrowserInvitationResolution{ID: creation.ID, InvitationID: creation.InvitationID,
				InvitationClaimHash: creation.InvitationClaimHash}, nil
		}
	}
	return nil, store.NewErrNotFound("browser_authentication_transaction", "")
}

// handoff stores a browser handoff for invitation with fresh handle and
// proof, and returns them.
func (s *onboardingBrowserTransactions) handoff(invitation *model.Invitation) (string, string) {
	handle, proof := model.NewCredentialToken(), model.NewCredentialToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creations = append(s.creations, &store.BrowserInvitationTransactionCreation{
		ID: model.NewBrowserAuthenticationTransactionID(), InvitationID: invitation.ID, InvitationPurpose: invitation.Purpose,
		InvitationClaimHash: invitation.ClaimHash, HandleHash: model.HashToken(handle), BrowserProofHash: model.HashToken(proof),
	})
	return handle, proof
}

// onboardingImportRecords holds the onboarding imports, Student
// Progressions and filter previews by ID with their rows, and records what
// each read and command asked of it.
type onboardingImportRecords struct {
	store.OnboardingImportStore
	mu   sync.Mutex
	byID map[model.OnboardingImportID]*store.OnboardingImport
	rows map[model.OnboardingImportID][]store.OnboardingImportRow
	// listed is what ListOnboardingImports returns; progressions and
	// progressionTotal are what ListStudentProgressions and its count
	// return; previewRows is what ListAcademicAdministrationPreviewRows
	// returns.
	listed           []*store.OnboardingImport
	progressions     []*store.StudentProgressionSummary
	progressionTotal int
	previewRows      []store.AcademicAdministrationPreviewRow

	creations          []*store.OnboardingImportCreation
	commits            []*store.OnboardingImportCommit
	listOptions        []store.OnboardingImportListOptions
	progressionOptions []store.StudentProgressionListOptions
	previewRowOptions  []store.AcademicAdministrationPreviewRowListOptions
	reads              []model.OnboardingImportID
}

func (s *onboardingImportRecords) put(value *store.OnboardingImport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[value.ID] = value
}

func (s *onboardingImportRecords) CreateOnboardingImport(_ context.Context, input *store.OnboardingImportCreation) (*store.OnboardingImport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creations = append(s.creations, input)
	s.byID[input.Import.ID] = input.Import
	return input.Import, nil
}

func (s *onboardingImportRecords) GetOnboardingImport(_ context.Context, id model.OnboardingImportID) (*store.OnboardingImport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, id)
	value, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("onboarding_import", id.String())
	}
	copied := *value
	return &copied, nil
}

func (s *onboardingImportRecords) ListOnboardingImportRows(_ context.Context, id model.OnboardingImportID, after, limit int) (*store.OnboardingImportPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := &store.OnboardingImportPage{}
	for _, row := range s.rows[id] {
		if row.RowNumber > after && len(page.Rows) < limit {
			page.Rows = append(page.Rows, row)
		}
	}
	return page, nil
}

func (s *onboardingImportRecords) ListOnboardingImports(_ context.Context, options store.OnboardingImportListOptions) ([]*store.OnboardingImport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listOptions = append(s.listOptions, options)
	return s.listed, nil
}

func (s *onboardingImportRecords) ListStudentProgressions(_ context.Context, options store.StudentProgressionListOptions) ([]*store.StudentProgressionSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progressionOptions = append(s.progressionOptions, options)
	return s.progressions, nil
}

func (s *onboardingImportRecords) CountStudentProgressions(context.Context, store.StudentProgressionListOptions, int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progressionTotal, nil
}

func (s *onboardingImportRecords) CommitOnboardingImport(_ context.Context, input *store.OnboardingImportCommit) (*store.OnboardingImport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits = append(s.commits, input)
	committed := *s.byID[input.ID]
	committed.State, committed.Revision, committed.ExecutionJobID = model.OnboardingImportExecuting, input.ExpectedRevision+1, input.ExecutionJob.ID
	return &committed, nil
}

func (s *onboardingImportRecords) CountAcademicAdministrationPreviewGroups(context.Context, store.AcademicAdministrationPreviewGroupCountOptions) ([]store.AcademicAdministrationPreviewGroup, error) {
	return []store.AcademicAdministrationPreviewGroup{}, nil
}

func (s *onboardingImportRecords) ListAcademicAdministrationPreviewRows(_ context.Context, options store.AcademicAdministrationPreviewRowListOptions) ([]store.AcademicAdministrationPreviewRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previewRowOptions = append(s.previewRowOptions, options)
	var rows []store.AcademicAdministrationPreviewRow
	for _, row := range s.previewRows {
		if row.RowNumber > options.AfterRowNumber && len(rows) < options.Limit {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// lastCreation is the most recent import creation.
func (s *onboardingImportRecords) lastCreation(t *testing.T) *store.OnboardingImportCreation {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.creations, "the request never created an import")
	return s.creations[len(s.creations)-1]
}

// onboardingImportFiles keeps every staged import file in memory.
type onboardingImportFiles struct {
	mu     sync.Mutex
	staged map[model.OnboardingImportID][]byte
}

func (f *onboardingImportFiles) StageOnboardingImport(_ context.Context, id model.OnboardingImportID, body io.Reader, maximum int64) (string, int64, error) {
	content, err := io.ReadAll(io.LimitReader(body, maximum))
	if err != nil {
		return "", 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staged[id] = content
	return id.String(), int64(len(content)), nil
}

func (*onboardingImportFiles) IsOnboardingImportTooLarge(error) bool { return false }

func (f *onboardingImportFiles) OpenOnboardingImport(_ context.Context, id model.OnboardingImportID) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return io.NopCloser(bytes.NewReader(f.staged[id])), nil
}

func (f *onboardingImportFiles) RemoveOnboardingImport(_ context.Context, id model.OnboardingImportID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.staged, id)
	return nil
}

func (*onboardingImportFiles) ListOnboardingImportFiles(context.Context, string, int, time.Time) ([]model.OnboardingImportID, string, error) {
	return nil, "", nil
}

// onboardingClassMembers holds the Class Memberships, lists each User's
// active ones and records the enrollments and ends the academic batches
// sent. An enrollment of a User in replayUsers answers as an exact replay.
type onboardingClassMembers struct {
	store.ClassMemberStore
	mu          sync.Mutex
	byID        map[model.ClassMemberID]*model.ClassMember
	replayUsers map[model.UserID]bool

	enrollments []*store.ClassMemberEnrollment
	ends        []*store.ClassMemberEnd
}

func (s *onboardingClassMembers) add(member *model.ClassMember) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[member.ID] = member
}

func (s *onboardingClassMembers) Get(_ context.Context, id model.ClassMemberID) (*model.ClassMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("class_member", id.String())
	}
	copied := *member
	return &copied, nil
}

func (s *onboardingClassMembers) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.ClassMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var active []*model.ClassMember
	for _, member := range s.byID {
		if member.UserID == userID {
			active = append(active, member)
		}
	}
	return active, nil
}

func (s *onboardingClassMembers) Enroll(_ context.Context, input *store.ClassMemberEnrollment) (*store.ClassEnrollmentResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enrollments = append(s.enrollments, input)
	input.Replayed = s.replayUsers[input.Member.UserID]
	result := &store.ClassEnrollmentResult{Membership: input.Member}
	if previous, ok := s.byID[input.ExpectedPreviousID]; ok {
		result.Previous = previous
	}
	return result, nil
}

func (s *onboardingClassMembers) End(_ context.Context, input *store.ClassMemberEnd) (*model.ClassMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ends = append(s.ends, input)
	ended := *s.byID[input.ID]
	ended.EndsAt = model.OptionalTimeFrom(input.EndAt)
	return &ended, nil
}

// onboardingSittings finds no Sitting board for any Class.
type onboardingSittings struct{ store.ExamSittingStore }

func (onboardingSittings) ListInvalidationTargetsByClass(context.Context, model.ClassID, model.ExamSittingID, int) ([]store.ExamSittingInvalidationTarget, error) {
	return nil, nil
}

// storedImport stores an onboarding import of mode at the scope, created by
// the caller, and returns it.
func (f *onboardingFixture) storedImport(mode model.OnboardingImportMode, state model.OnboardingImportState, scopeType model.RoleScopeType, scopeID string) *store.OnboardingImport {
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	value := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: mode, State: state, ScopeType: scopeType, ScopeID: scopeID,
		ActorUserID: f.callerID, ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 2}
	f.imports.put(value)
	return value
}

// storedProgression stores a ready Student Progression from source into
// destination, created by the caller, and returns it.
func (f *onboardingFixture) storedProgression(source, destination *model.Class) *store.OnboardingImport {
	value := f.storedImport(model.OnboardingImportStudentProgression, model.OnboardingImportPreviewReady, model.RoleScopeClass, destination.ID.String())
	value.SourcePeriodID, value.SourceClassID = source.AcademicPeriodID, source.ID
	value.DestinationPeriodID, value.DestinationClassID = destination.AcademicPeriodID, destination.ID
	value.EffectiveAt = value.CreatedAt.Add(24 * time.Hour)
	return value
}

// storedFilterPreview stores the caller's Institution-wide user.disable
// filter preview in state, and returns it.
func (f *onboardingFixture) storedFilterPreview(state model.OnboardingImportState) *store.OnboardingImport {
	value := f.storedImport(model.OnboardingImportAcademicAdministrationFilter, state, model.RoleScopeInstitution, f.access.Institution.ID.String())
	value.Filter = &store.AcademicAdministrationFilter{Operation: string(onboarding.AcademicAdministrationUserDisable)}
	return value
}

func (c onboardingCatalog) AssistantProfile() store.AssistantProfileStore { return nil }
