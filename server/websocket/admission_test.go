// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// admissionWorld is the real RealtimeAdmission a connection consults, over
// in-memory stores. The identity stores hold the principal's Session, User
// and Desktop Registration, so revalidation accepts it unless the options
// disable the User. Every Class resolves as a live Class of one Academic Unit
// where the principal holds class.view, so a Class subscription is admitted
// unless the options withhold the grant or the Class is made missing or
// unreadable.
type admissionWorld struct {
	principal model.Principal
	access    *accesstest.Fixture
	sessions  *admissionSessions
	users     *admissionUsers
	classes   *admissionClasses
	// sitting is set when the options ask for one: a Sitting of an Exam the
	// principal manages in the unit, where it holds exam_sitting.view.
	sitting   *model.ExamSitting
	admission *app.RealtimeAdmission
}

type admissionOptions struct {
	// principal is the connection's principal; zero means a candidate's
	// Desktop Session.
	principal model.Principal
	// disabled stores the principal's User as disabled, so every
	// revalidation refuses the principal.
	disabled bool
	// withoutClassView withholds class.view, so a Class subscription is
	// denied.
	withoutClassView bool
	// sitting adds a Sitting the principal may view.
	sitting bool
}

func newAdmissionWorld(t testing.TB, options admissionOptions) *admissionWorld {
	t.Helper()
	principal := options.principal
	if principal.UserID.IsZero() {
		principal = testCandidatePrincipal()
	}
	unitID := model.NewAcademicUnitID()
	world := &admissionWorld{
		principal: principal,
		sessions: &admissionSessions{session: &model.Session{ID: principal.SessionID, UserID: principal.UserID,
			DesktopRegistrationID: principal.DesktopRegistrationID, DPoPKeyThumbprint: principal.DPoPKeyThumbprint,
			ExpiresAt: admissionFarFuture, IdleExpiresAt: admissionFarFuture}},
		users:   &admissionUsers{user: &model.User{ID: principal.UserID}},
		classes: &admissionClasses{unitID: unitID, periodID: model.NewAcademicPeriodID(), missing: map[model.ClassID]bool{}, failing: map[model.ClassID]bool{}},
	}
	if options.disabled {
		world.users.user.DisabledAt = model.OptionalTimeFrom(time.Now())
	}

	var actions []model.Action
	if !options.withoutClassView {
		actions = append(actions, model.ActionClassView)
	}
	var sittings *admissionSittings
	var exams admissionExams
	if options.sitting {
		actions = append(actions, model.ActionExamSittingView)
		world.sitting = &model.ExamSitting{ID: model.NewExamSittingID(), ExamID: model.NewExamID()}
		sittings = &admissionSittings{snapshot: &store.ExamSittingSnapshot{Sitting: world.sitting, AcademicUnitID: unitID}}
		exams = admissionExams{exam: &model.Exam{ID: world.sitting.ExamID, AcademicUnitID: unitID}, manager: principal.UserID}
	}
	periods := &admissionPeriods{id: world.classes.periodID}
	accessOptions := accesstest.Options{
		Grants:  []accesstest.Grant{{UserID: principal.UserID, Actions: actions, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}},
		Classes: world.classes, AcademicPeriods: periods,
	}
	if sittings != nil {
		accessOptions.Sittings, accessOptions.Exams = sittings, exams
	}
	world.access = accesstest.New(t, accessOptions)
	periods.institutionID = world.access.Institution.ID

	deps := app.RealtimeAdmissionDependencies{
		Principals: world.newIdentityService(t), Authorizer: world.access.Authorizer,
	}
	if sittings != nil {
		service, err := sitting.New(sitting.Dependencies{
			Persistence: sittings,
			Access:      exams,
			Memberships: admissionMemberships{unitID: unitID, userID: principal.UserID},
			Authorizer:  world.access.Authorizer, Auditor: world.access.Auditor, Publisher: &realtimetest.Publisher{},
			Jobs: unusedLifecycleJobs{}, Mail: sitting.ScheduleMail{Composer: &mail.SittingComposer{},
				Revisions: unusedRevisions{}, Classes: unusedClasses{}},
			Now: time.Now, NewID: model.NewExamSittingID,
		})
		require.NoError(t, err)
		deps.Sittings = service
	}
	world.admission = app.NewRealtimeAdmission(deps)
	return world
}

// newIdentityService is the real identity Service over the world's identity
// stores. Revalidation reads only the Session, the User, their two-step
// verification recovery state and the Desktop Registration; nothing else a
// connection does reaches the other stores, so they are empty.
func (w *admissionWorld) newIdentityService(t testing.TB) *identity.Service {
	t.Helper()
	logger, _ := newTestLogger(t)
	cache := struct{ identity.AuthenticationCache }{}
	attempts, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: cache})
	require.NoError(t, err)
	invalidator, err := identity.NewCacheInvalidator(identity.CacheInvalidatorDependencies{Cache: cache, Diagnostics: logger})
	require.NoError(t, err)
	// Revalidation hashes nothing; the hasher only has to be a valid one.
	settings := config.Default().Authentication.Password
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: identity.PasswordPolicy{
		MinimumLength: settings.MinimumLength, MaximumLength: settings.MaximumLength, MaximumConcurrentOperations: 1,
		ArgonMemoryKiB: 19 * 1024, ArgonIterations: 1, ArgonParallelism: 1,
		ArgonSaltBytes: settings.ArgonSaltBytes, ArgonKeyBytes: settings.ArgonKeyBytes,
	}})
	require.NoError(t, err)
	registration := &model.DesktopRegistration{ID: w.principal.DesktopRegistrationID, UserID: w.principal.UserID,
		KeyThumbprint: w.principal.DPoPKeyThumbprint}
	service, err := identity.New(identity.Dependencies{
		Store: admissionCatalog{sessions: w.sessions, users: w.users, registrations: admissionRegistrations{registration: registration},
			institution: w.access.Institution},
		Cache: cache, Registry: admissionProviders{}, Authorizer: w.access.Authorizer, Auditor: w.access.Auditor,
		Capabilities: &identity.DeploymentCapabilities{}, Publisher: &realtimetest.Publisher{}, Mail: &mail.Composer{},
		Hasher: hasher, Attempts: attempts, Invalidator: invalidator, Invitations: struct{ identity.InvitationAcceptor }{},
		SkipDesktopCompatibility: true, NodeID: "node-a", PublicURL: "https://proctor.example",
		Sessions: identity.SessionPolicy{AccessTTL: time.Minute, RefreshTTL: time.Hour, IdleTTL: time.Hour,
			AbsoluteTTL: 24 * time.Hour, MaximumPerUser: 5},
		LoginRateLimit:          identity.LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 10, MaximumSourceAttempts: 10},
		RecentAuthenticationTTL: 15 * time.Minute, AuthenticationDiagnostics: logger, RecoveryDiagnostics: logger,
	})
	require.NoError(t, err)
	return service
}

// missingClass is a Class subscription whose Class does not resolve.
func (w *admissionWorld) missingClass() Subscription {
	subscription := validInboundSubscription()
	w.classes.mark(w.classes.missing, model.ClassID(subscription.Resource.ID))
	return subscription
}

// unreadableClass is a Class subscription whose Class the store cannot read.
func (w *admissionWorld) unreadableClass() Subscription {
	subscription := validInboundSubscription()
	w.classes.mark(w.classes.failing, model.ClassID(subscription.Resource.ID))
	return subscription
}

// admissionFarFuture keeps the stored Session unexpired on the wall clock and
// on a synctest bubble's clock alike.
var admissionFarFuture = time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)

// admissionSessions holds the principal's Session and records each read, at
// the clock the reader saw.
type admissionSessions struct {
	store.SessionStore
	session *model.Session

	mu    sync.Mutex
	reads []admissionSessionRead
}

type admissionSessionRead struct {
	id model.SessionID
	at time.Time
}

func (s *admissionSessions) Get(_ context.Context, id model.SessionID) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, admissionSessionRead{id: id, at: time.Now()})
	if id != s.session.ID {
		return nil, store.NewErrNotFound("session", id.String())
	}
	copied := *s.session
	return &copied, nil
}

func (s *admissionSessions) read() []admissionSessionRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]admissionSessionRead(nil), s.reads...)
}

// admissionUsers holds the principal's User and records each read.
type admissionUsers struct {
	store.UserStore
	user *model.User

	mu    sync.Mutex
	reads []model.UserID
}

func (s *admissionUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, id)
	if id != s.user.ID {
		return nil, store.NewErrNotFound("user", id.String())
	}
	copied := *s.user
	return &copied, nil
}

func (s *admissionUsers) read() []model.UserID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.UserID(nil), s.reads...)
}

type admissionRegistrations struct {
	store.DesktopRegistrationStore
	registration *model.DesktopRegistration
}

func (s admissionRegistrations) Get(_ context.Context, id model.DesktopRegistrationID) (*model.DesktopRegistration, error) {
	if id != s.registration.ID {
		return nil, store.NewErrNotFound("desktop_registration", id.String())
	}
	copied := *s.registration
	return &copied, nil
}

// admissionMFA reports that no User has had two-step verification reset.
type admissionMFA struct{ store.MFAStore }

func (admissionMFA) GetRecoveryState(_ context.Context, userID model.UserID) (*model.UserMFARecovery, error) {
	return &model.UserMFARecovery{UserID: userID}, nil
}

// admissionCatalog serves the identity stores; every other store is empty.
type admissionCatalog struct {
	store.Catalog
	sessions      *admissionSessions
	users         *admissionUsers
	registrations admissionRegistrations
	institution   *model.Institution
}

func (c admissionCatalog) Session() store.SessionStore { return c.sessions }
func (c admissionCatalog) User() store.UserStore       { return c.users }
func (c admissionCatalog) DesktopRegistration() store.DesktopRegistrationStore {
	return c.registrations
}
func (admissionCatalog) MFA() store.MFAStore { return admissionMFA{} }
func (c admissionCatalog) Institution() store.InstitutionStore {
	return admissionInstitution{institution: c.institution}
}
func (admissionCatalog) PasswordCredential() store.PasswordCredentialStore {
	return struct{ store.PasswordCredentialStore }{}
}
func (admissionCatalog) SessionCredential() store.SessionCredentialStore {
	return struct{ store.SessionCredentialStore }{}
}
func (admissionCatalog) PersonalAccessToken() store.PersonalAccessTokenStore {
	return struct{ store.PersonalAccessTokenStore }{}
}
func (admissionCatalog) BrowserAuthentication() store.BrowserAuthenticationStore {
	return struct {
		store.BrowserAuthenticationStore
	}{}
}
func (admissionCatalog) UserToken() store.UserTokenStore { return struct{ store.UserTokenStore }{} }
func (admissionCatalog) AcademicUnit() store.AcademicUnitStore {
	return struct{ store.AcademicUnitStore }{}
}
func (admissionCatalog) AccessPolicy() store.AccessPolicyStore {
	return struct{ store.AccessPolicyStore }{}
}
func (admissionCatalog) DesktopCompatibilityPolicy() store.DesktopCompatibilityPolicyStore {
	return struct {
		store.DesktopCompatibilityPolicyStore
	}{}
}
func (admissionCatalog) ExternalLoginState() store.ExternalLoginStateStore {
	return struct{ store.ExternalLoginStateStore }{}
}
func (admissionCatalog) ExternalIdentity() store.ExternalIdentityStore {
	return struct{ store.ExternalIdentityStore }{}
}
func (admissionCatalog) Installation() store.InstallationStore {
	return struct{ store.InstallationStore }{}
}

// Job is absent, so the Service runs no two-step verification key rotation.
func (admissionCatalog) Job() store.JobStore { return nil }

type admissionInstitution struct {
	store.InstitutionStore
	institution *model.Institution
}

func (s admissionInstitution) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

type admissionProviders struct{}

func (admissionProviders) Descriptors() []model.ExternalAuthenticationProvider { return nil }
func (admissionProviders) Provider(string) (identity.ExternalIdentityProvider, bool) {
	return nil, false
}

// admissionClasses resolves every Class as a live Class of one Academic Unit
// in one Academic Period, except the Classes marked missing or failing.
type admissionClasses struct {
	store.ClassStore
	unitID   model.AcademicUnitID
	periodID model.AcademicPeriodID

	mu      sync.Mutex
	missing map[model.ClassID]bool
	failing map[model.ClassID]bool
}

func (s *admissionClasses) mark(set map[model.ClassID]bool, id model.ClassID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set[id] = true
}

func (s *admissionClasses) check(id model.ClassID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.missing[id]:
		return store.NewErrNotFound("class", id.String())
	case s.failing[id]:
		return errors.New("class store is unavailable")
	}
	return nil
}

func (s *admissionClasses) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	if err := s.check(id); err != nil {
		return nil, err
	}
	return &model.Class{ID: id, AcademicPeriodID: s.periodID}, nil
}

func (s *admissionClasses) GetAcademicUnitID(_ context.Context, id model.ClassID) (model.AcademicUnitID, error) {
	if err := s.check(id); err != nil {
		return "", err
	}
	return s.unitID, nil
}

// admissionPeriods holds the one Academic Period every Class runs in, owned
// by the Institution.
type admissionPeriods struct {
	store.AcademicPeriodStore
	id            model.AcademicPeriodID
	institutionID model.InstitutionID
}

func (s *admissionPeriods) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	if id != s.id {
		return nil, store.NewErrNotFound("academic_period", id.String())
	}
	return &model.AcademicPeriod{ID: id, Owner: model.NewInstitutionAcademicPeriodOwner(s.institutionID)}, nil
}

// admissionSittings holds one Sitting.
type admissionSittings struct {
	store.ExamSittingStore
	snapshot *store.ExamSittingSnapshot
}

func (s *admissionSittings) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if id != s.snapshot.Sitting.ID {
		return nil, store.NewErrNotFound("exam_sitting", id.String())
	}
	copied := *s.snapshot
	return &copied, nil
}

// admissionExams holds the Sitting's Exam, which manager manages.
type admissionExams struct {
	store.ExamAuthoringStore
	exam    *model.Exam
	manager model.UserID
}

func (s admissionExams) Resolve(_ context.Context, id model.ExamID) (*model.Exam, error) {
	if id != s.exam.ID {
		return nil, store.NewErrNotFound("exam", id.String())
	}
	return s.exam, nil
}

func (s admissionExams) Access(_ context.Context, id model.ExamID, userID model.UserID) (*store.ExamAccessSnapshot, error) {
	if id != s.exam.ID {
		return nil, store.NewErrNotFound("exam", id.String())
	}
	return &store.ExamAccessSnapshot{Exam: s.exam, ActorIsManager: userID == s.manager}, nil
}

// admissionMemberships makes userID a current member of unitID.
type admissionMemberships struct {
	store.AcademicUnitMemberStore
	unitID model.AcademicUnitID
	userID model.UserID
}

func (s admissionMemberships) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.AcademicUnitMember, error) {
	if userID != s.userID {
		return nil, nil
	}
	return []*model.AcademicUnitMember{{AcademicUnitID: s.unitID, UserID: userID}}, nil
}

func (*admissionSessions) DatabaseNow(context.Context) (time.Time, error) { return model.NowUTC(), nil }
