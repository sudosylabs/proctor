// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// These helpers build the real identity, access and mail mechanics that
// Invitations and public registration borrow.

type expiringAuthenticationAttemptCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]expiringAuthenticationAttemptCounter
	deleted []string
	// added records every counter key the accounting incremented.
	added []string
}

type expiringAuthenticationAttemptCounter struct {
	count     int64
	expiresAt time.Time
}

func newExpiringAuthenticationAttemptCache(
	now func() time.Time,
) *expiringAuthenticationAttemptCache {
	return &expiringAuthenticationAttemptCache{
		now: now, entries: make(map[string]expiringAuthenticationAttemptCounter),
	}
}

func (c *expiringAuthenticationAttemptCache) Add(
	_ context.Context,
	key string,
	delta int64,
	ttl time.Duration,
) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	entry := c.entries[key]
	if !entry.expiresAt.After(now) {
		// Model the platform cache's fixed windows: only a new counter
		// receives an expiration.
		entry.count = 0
		entry.expiresAt = now.Add(ttl)
	}
	entry.count += delta
	c.entries[key] = entry
	c.added = append(c.added, key)
	return entry.count, nil
}

func (c *expiringAuthenticationAttemptCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
	c.deleted = append(c.deleted, key)
	return nil
}

func (c *expiringAuthenticationAttemptCache) snapshot() map[string]expiringAuthenticationAttemptCounter {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string]expiringAuthenticationAttemptCounter, len(c.entries))
	for key, entry := range c.entries {
		result[key] = entry
	}
	return result
}

func mustAuthenticationAttemptAccounting(
	t *testing.T,
	cache identity.AttemptCache,
) *identity.AttemptAccounting {
	t.Helper()
	accounting, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: cache})
	require.NoError(t, err)
	return accounting
}

// testRateLimit is generous enough that no test reaches it by accident.
var testRateLimit = identity.LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 1000, MaximumSourceAttempts: 1000}

// testAttempts is shared attempt accounting over a fresh in-memory cache.
func testAttempts(t *testing.T) *identity.AttemptAccounting {
	t.Helper()
	return mustAuthenticationAttemptAccounting(t, newExpiringAuthenticationAttemptCache(time.Now))
}

// invitationAttemptChecks counts the Invitation claim checks the accounting
// received: each check increments one identity counter for the Invitation
// purpose, beside its source counter.
func invitationAttemptChecks(c *expiringAuthenticationAttemptCache) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	checks := 0
	for _, key := range c.added {
		if strings.HasPrefix(key, "authentication/attempts/invitation/identity/") {
			checks++
		}
	}
	return checks
}

// testPasswordHasher is the production Argon2id hasher at its cheapest
// accepted cost.
func testPasswordHasher(t testing.TB) *identity.PasswordHasher {
	t.Helper()
	settings := config.Default().Authentication.Password
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: identity.PasswordPolicy{
		MinimumLength: settings.MinimumLength, MaximumLength: settings.MaximumLength, MaximumConcurrentOperations: 2,
		ArgonMemoryKiB: 19 * 1024, ArgonIterations: 1, ArgonParallelism: 1,
		ArgonSaltBytes: settings.ArgonSaltBytes, ArgonKeyBytes: settings.ArgonKeyBytes,
	}})
	require.NoError(t, err)
	return hasher
}

// requirePasswordHash fails unless encoded is the hasher's encoding of
// password.
func requirePasswordHash(t *testing.T, hasher *identity.PasswordHasher, encoded, password string) {
	t.Helper()
	require.NoError(t, hasher.Verify(context.Background(), encoded, password), "stored hash does not verify the submitted password")
}

// passwordHashHook is what a test puts in Service.hashHook to reach a
// password work failure the real derivation never returns.
type passwordHashHook = func(context.Context, string) (string, error)

func testPasswordHashFailures(t *testing.T, unavailableCode string, invoke func(*testing.T, context.Context, passwordHashHook) error) {
	t.Helper()
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "busy", err: apperror.New("service.busy"), code: "service.busy"},
		{name: "canceled", err: context.Canceled, code: unavailableCode},
		{name: "operational failure", err: errors.New("password work unavailable"), code: unavailableCode},
		{name: "invalid password", err: apperror.New("authentication.password.invalid"), code: "authentication.password.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			hook := func(got context.Context, _ string) (string, error) {
				calls++
				require.Equal(t, ctx, got, "password work did not receive the request context")
				return "", test.err
			}
			err := invoke(t, ctx, hook)
			require.True(t, apperror.Is(err, test.code), "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
			require.Equal(t, 1, calls, "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
			require.ErrorIs(t, err, test.err, "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
		})
	}
}

// allActions is every Action the catalog defines, so one grant stands for a
// caller who holds every permission.
func allActions() []model.Action {
	var actions []model.Action
	for _, action := range model.AllActions() {
		actions = append(actions, model.Action(action))
	}
	return actions
}

// grantAll grants each User every Action Institution-wide.
func grantAll(users ...model.UserID) []accesstest.Grant {
	grants := make([]accesstest.Grant, 0, len(users))
	for _, id := range users {
		grants = append(grants, accesstest.Grant{UserID: id, Actions: allActions()})
	}
	return grants
}

// decidedActions returns the Action of every authorization decision the
// real authorizer recorded, in order.
func decidedActions(fixture *accesstest.Fixture) []model.Action {
	var actions []model.Action
	for _, decision := range fixture.Audits.Decisions() {
		actions = append(actions, model.Action(decision.Action))
	}
	return actions
}

// sessionPrincipal is an interactive web Session at the given strength,
// authenticated at at.
func sessionPrincipal(strength model.AuthenticationStrength, at time.Time) model.Principal {
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: strength,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at}
	if strength == model.AuthenticationMultiFactor {
		principal.MFACompletedAt = model.OptionalTimeFrom(at)
	}
	return principal
}

// sessionCaller is a single-factor web Session of userID.
func sessionCaller(userID model.UserID, at time.Time) invocation.Invocation {
	principal := sessionPrincipal(model.AuthenticationSingleFactor, at)
	principal.UserID = userID
	return invocation.New(principal, model.RequestMetadata{})
}

// auditParameters decodes an attempt's recorded operation and value.
func auditParameters(t *testing.T, event *model.AuditEvent) (string, map[string]any) {
	t.Helper()
	var parameters struct {
		Operation string         `json:"operation"`
		Value     map[string]any `json:"value"`
	}
	require.NoError(t, json.Unmarshal(event.Parameters, &parameters))
	return parameters.Operation, parameters.Value
}

// mailSenderFake is the mail transport port. The Composer reads From once
// for each message, right after rendering it, so the fake counts rendered
// messages; a failing sender answers an invalid From address, which makes
// mail preparation fail.
type mailSenderFake struct {
	enabled bool

	mu       sync.Mutex
	failing  bool
	rendered int
}

func (s *mailSenderFake) Enabled() bool { return s.enabled }
func (s *mailSenderFake) From() mail.Address {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rendered++
	if s.failing {
		return mail.Address{}
	}
	return mail.Address{Name: "Proctor", Address: "no-reply@example.test"}
}
func (*mailSenderFake) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, nil
}

// fail makes every later mail preparation fail.
func (s *mailSenderFake) fail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = true
}

// renders returns how many messages were rendered.
func (s *mailSenderFake) renders() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rendered
}

func testMailSealer(t testing.TB) *secretseal.Sealer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	return sealer
}

// newTestMail builds the production mail Composer over the real renderer. An
// enabled sender queues rendered mail; a disabled one makes the Composer
// record suppressed mail without rendering it.
func newTestMail(t testing.TB, enabled bool) (*mail.Composer, *mailSenderFake) {
	t.Helper()
	sender := &mailSenderFake{enabled: enabled}
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: mailtest.NewRenderer(t), Sender: sender, Sealer: testMailSealer(t)})
	require.NoError(t, err)
	return composer, sender
}

// openedMail opens a prepared delivery, so a test reads the message its
// recipient would get.
func openedMail(t *testing.T, delivery *model.MailDelivery) mail.Outbound {
	t.Helper()
	opened, err := mail.OpenDelivery(testMailSealer(t), delivery)
	require.NoError(t, err)
	return opened
}

// institutionStoreFake answers the singleton Institution.
type institutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
}

func (s institutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

// testClassStore resolves its Classes, each owned by unitID.
type testClassStore struct {
	store.ClassStore
	unitID  model.AcademicUnitID
	classes map[model.ClassID]*model.Class
}

func (s testClassStore) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	if class := s.classes[id]; class != nil {
		return class, nil
	}
	return nil, store.NewErrNotFound("class", id.String())
}

func (s testClassStore) GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error) {
	return s.unitID, nil
}

// testPeriodStore resolves one Academic Period owned by an Institution.
type testPeriodStore struct {
	store.AcademicPeriodStore
	period *model.AcademicPeriod
}

func (s testPeriodStore) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	if s.period == nil || s.period.ID != id {
		return nil, store.NewErrNotFound("academic_period", id.String())
	}
	return s.period, nil
}

// classAccess builds the real authorizer over one Class in one Academic Unit
// and an Institution-owned Academic Period, with the grants given.
func classAccess(t *testing.T, classID model.ClassID, grants ...accesstest.Grant) *accesstest.Fixture {
	t.Helper()
	unit := &model.AcademicUnit{ID: model.NewAcademicUnitID(), Name: "Computing"}
	period := &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), Owner: model.AcademicPeriodOwner{InstitutionID: model.NewInstitutionID()}}
	return accesstest.New(t, accesstest.Options{
		Grants: grants, Units: []*model.AcademicUnit{unit}, AcademicPeriods: testPeriodStore{period: period},
		Classes: testClassStore{unitID: unit.ID, classes: map[model.ClassID]*model.Class{classID: {ID: classID, AcademicPeriodID: period.ID}}},
	})
}

func TestVisibilityRecordsOneDecisionAndFailsClosedWithoutGrants(t *testing.T) {
	t.Parallel()
	now := time.Now()
	classID := model.NewClassID()
	caller := sessionCaller(model.NewUserID(), now)

	granted := classAccess(t, classID, accesstest.Grant{UserID: caller.Principal().UserID,
		Actions: []model.Action{model.ActionAcademicProgressionManage}, ScopeType: model.RoleScopeClass, ScopeID: classID.String()})
	scope, err := (&Service{authorizer: granted.Authorizer}).visibility(context.Background(), caller, model.ActionAcademicProgressionManage)
	require.NoError(t, err)
	require.Equal(t, store.InvitationVisibilityScope{ClassIDs: []model.ClassID{classID}}, scope)
	decisions := granted.Audits.Decisions()
	require.Len(t, decisions, 1)
	require.Equal(t, string(model.ActionAcademicProgressionManage), decisions[0].Action)
	require.Equal(t, model.Resource{Type: model.ResourceClass, ID: classID.String()}, decisions[0].Resource)
	require.Equal(t, model.RoleScopeClass, decisions[0].ScopeType)
	require.Equal(t, classID.String(), decisions[0].ScopeID)
	require.Equal(t, model.AuditStatusSuccess, decisions[0].Status)

	// No grant anywhere is one recorded denial at the Institution.
	none := accesstest.New(t, accesstest.Options{})
	_, err = (&Service{authorizer: none.Authorizer}).visibility(context.Background(), caller, model.ActionInvitationView)
	require.True(t, apperror.Is(err, "authorization.denied"), "visibility without grants = %v", err)
	decisions = none.Audits.Decisions()
	require.Len(t, decisions, 1)
	require.Equal(t, string(model.ActionInvitationView), decisions[0].Action)
	require.Equal(t, model.Resource{Type: model.ResourceInstitution, ID: none.Institution.ID.String()}, decisions[0].Resource)
	require.Equal(t, model.RoleScopeInstitution, decisions[0].ScopeType)
	require.Equal(t, none.Institution.ID.String(), decisions[0].ScopeID)
	require.Equal(t, model.AuditStatusFail, decisions[0].Status)

	// An Action outside the institution family and a missing authorizer both
	// refuse before any decision.
	other := accesstest.New(t, accesstest.Options{Grants: grantAll(caller.Principal().UserID)})
	_, err = (&Service{authorizer: other.Authorizer}).visibility(context.Background(), caller, model.ActionRoleView)
	require.True(t, apperror.Is(err, "invitation.unavailable"), "visibility for another Action = %v", err)
	require.Empty(t, other.Audits.Decisions())
	_, err = (&Service{}).visibility(context.Background(), caller, model.ActionInvitationView)
	require.True(t, apperror.Is(err, "invitation.unavailable"), "visibility without an authorizer = %v", err)
	require.True(t, apperror.Is((&Service{}).authorize(context.Background(), caller, model.ActionInvitationCreate, model.Resource{}), "invitation.unavailable"))
}

func TestImportUseCasesRefuseWithoutOnboardingImports(t *testing.T) {
	t.Parallel()
	caller := sessionCaller(model.NewUserID(), time.Now())
	authorization := accesstest.New(t, accesstest.Options{Grants: grantAll(caller.Principal().UserID)})
	// A Service built from a reduced Store catalog holds no import Store.
	service := &Service{authorizer: authorization.Authorizer, now: time.Now}
	ctx, id := context.Background(), model.NewOnboardingImportID()
	digest := strings.Repeat("a", 64)

	_, err := service.UploadOnboardingImport(ctx, caller, UploadOnboardingImportCommand{Mode: model.OnboardingImportStudentClass,
		ScopeType: model.RoleScopeClass, ScopeID: model.NewClassID().String(), Body: strings.NewReader("email\n")})
	require.True(t, apperror.Is(err, "onboarding_import.unavailable"), "upload = %v", err)
	_, _, err = service.GetOnboardingImport(ctx, caller, id)
	require.True(t, apperror.Is(err, "onboarding_import.unavailable"), "get = %v", err)
	_, err = service.ListOnboardingImports(ctx, caller, ListOnboardingImportsQuery{})
	require.True(t, apperror.Is(err, "onboarding_import.unavailable"), "list = %v", err)
	_, err = service.CommitOnboardingImport(ctx, caller, CommitOnboardingImportCommand{ID: id, ExpectedRevision: 1, PreviewDigest: digest,
		Policy: model.OnboardingImportValidRowsOnly, IdempotencyKey: "commit"})
	require.True(t, apperror.Is(err, "onboarding_import.unavailable"), "commit = %v", err)
	_, err = service.CancelOnboardingImport(ctx, caller, id)
	require.True(t, apperror.Is(err, "onboarding_import.unavailable"), "cancel = %v", err)
	err = service.OnboardingImportReport(ctx, caller, id, &strings.Builder{})
	require.True(t, apperror.Is(err, "onboarding_import.unavailable"), "report = %v", err)
	_, err = service.countOpenOnboardingImports(ctx, caller, 10)
	require.True(t, apperror.Is(err, "onboarding_import.unavailable"), "count = %v", err)

	_, _, err = service.GetStudentProgression(ctx, caller, id)
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "get progression = %v", err)
	_, err = service.ListStudentProgressions(ctx, caller, ListStudentProgressionsQuery{})
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "list progressions = %v", err)
	_, err = service.CommitStudentProgression(ctx, caller, CommitStudentProgressionCommand{ID: id, ExpectedRevision: 1, PreviewDigest: digest, IdempotencyKey: "commit"})
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "commit progression = %v", err)
	_, err = service.CancelStudentProgression(ctx, caller, id)
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "cancel progression = %v", err)
	err = service.StudentProgressionReport(ctx, caller, id, &strings.Builder{})
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "progression report = %v", err)
	_, err = service.DryRunStudentProgression(ctx, caller, DryRunStudentProgressionCommand{SourcePeriodID: model.NewAcademicPeriodID(),
		SourceClassID: model.NewClassID(), DestinationPeriodID: model.NewAcademicPeriodID(), DestinationClassID: model.NewClassID(), EffectiveAt: 1})
	require.True(t, apperror.Is(err, "student_progression.unavailable"), "dry run = %v", err)
	require.Empty(t, authorization.Audits.Decisions(), "a Service without imports authorized an import use case")
}

// newTestComposer is an enabled production Composer for tests that do not
// look at the mail it freezes.
func newTestComposer(t testing.TB) *mail.Composer {
	t.Helper()
	composer, _ := newTestMail(t, true)
	return composer
}

// allActionsExcept is every Action but the ones given, for a caller refused
// exactly those.
func allActionsExcept(refused ...model.Action) []model.Action {
	var actions []model.Action
	for _, action := range allActions() {
		if !slices.Contains(refused, action) {
			actions = append(actions, action)
		}
	}
	return actions
}

// scopedAccess grants userID the action at one Academic Unit and at one
// Class of another Academic Unit, both resolvable by the real authorizer.
func scopedAccess(t *testing.T, userID model.UserID, action model.Action, unitID model.AcademicUnitID, classID model.ClassID) *accesstest.Fixture {
	t.Helper()
	classUnit := &model.AcademicUnit{ID: model.NewAcademicUnitID(), Name: "Class unit"}
	period := &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), Owner: model.AcademicPeriodOwner{InstitutionID: model.NewInstitutionID()}}
	return accesstest.New(t, accesstest.Options{
		Units:           []*model.AcademicUnit{{ID: unitID, Name: "Granted unit"}, classUnit},
		AcademicPeriods: testPeriodStore{period: period},
		Classes:         testClassStore{unitID: classUnit.ID, classes: map[model.ClassID]*model.Class{classID: {ID: classID, AcademicPeriodID: period.ID}}},
		Grants: []accesstest.Grant{
			{UserID: userID, Actions: []model.Action{action}, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()},
			{UserID: userID, Actions: []model.Action{action}, ScopeType: model.RoleScopeClass, ScopeID: classID.String()},
		},
	})
}

// requireOneScopedDecision fails unless the authorizer recorded exactly one
// allowed decision for action, naming the granted Academic Unit.
func requireOneScopedDecision(t *testing.T, fixture *accesstest.Fixture, action model.Action, unitID model.AcademicUnitID) {
	t.Helper()
	decisions := fixture.Audits.Decisions()
	require.Len(t, decisions, 1, "one scope decision precedes the Store read")
	require.Equal(t, string(action), decisions[0].Action)
	require.Equal(t, model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}, decisions[0].Resource, "the one decision names the first granted Academic Unit")
	require.Equal(t, model.RoleScopeAcademicUnit, decisions[0].ScopeType)
	require.Equal(t, unitID.String(), decisions[0].ScopeID)
	require.Equal(t, model.AuditStatusSuccess, decisions[0].Status)
}
