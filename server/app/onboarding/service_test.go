// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// These fakes stand in for the identity and access mechanics that
// Invitations and public registration borrow.

// testPasswordHasher is the password work Invitations and registration ask
// of the shared hasher.
type testPasswordHasher interface {
	Hash(context.Context, string) (string, error)
	ReuseCheck(string) store.PasswordReuseCheck
}

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

// passwordHasherFake stands in for the shared password hasher: it refuses an
// empty password and otherwise returns a recognizable encoding.
type passwordHasherFake struct{}

func (passwordHasherFake) Hash(_ context.Context, password string) (string, error) {
	if password == "" {
		return "", apperror.New("authentication.password.invalid")
	}
	return "encoded:" + password, nil
}

type passwordHashFunc func(context.Context, string) (string, error)

func (h passwordHashFunc) Hash(ctx context.Context, password string) (string, error) {
	return h(ctx, password)
}

func (passwordHashFunc) ReuseCheck(string) store.PasswordReuseCheck {
	return store.PasswordReuseCheck{}
}

func testPasswordHashFailures(t *testing.T, unavailableCode string, invoke func(*testing.T, context.Context, testPasswordHasher) error) {
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
			hasher := passwordHashFunc(func(got context.Context, _ string) (string, error) {
				calls++
				require.Equal(t, ctx, got, "password work did not receive the request context")
				return "", test.err
			})
			err := invoke(t, ctx, hasher)
			require.True(t, apperror.Is(err, test.code), "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
			require.Equal(t, 1, calls, "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
			require.ErrorIs(t, err, test.err, "password failure = %v, calls %d; want %s wrapping %v after one call", err, calls, test.code, test.err)
		})
	}
}

// authorizerFake stands in for *access.Authorizer. It records every Action it
// authorizes or resolves scopes for, the last Resource it authorized, the
// delegation it was asked about and every scoped decision it recorded. err
// refuses every check; actionErrors refuses one Action.
type authorizerFake struct {
	actions            []model.Action
	resource           model.Resource
	delegatedActions   []string
	delegatedScopeType model.RoleScopeType
	delegatedScopeID   string
	actionErrors       map[model.Action]error
	visibility         *store.InvitationVisibilityScope
	institution        *model.Institution
	decisions          []authorizationDecision
	err                error
	userVisibility     *store.UserVisibilityScope
	userSearchErr      error
	userSearches       int
	// classMembersVisible are the Classes whose members the caller sees;
	// classMemberViews records each Class asked about.
	classMembersVisible map[model.ClassID]bool
	classMemberViews    []model.Resource
}

// authorizationDecision is one scoped decision the Service recorded.
type authorizationDecision struct {
	action    model.Action
	resource  model.Resource
	scopeType model.RoleScopeType
	scopeID   string
	allowed   bool
}

func (f *authorizerFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource) error {
	f.actions = append(f.actions, action)
	f.resource = resource
	if err := f.actionErrors[action]; err != nil {
		return err
	}
	return f.err
}

func (f *authorizerFake) CanDelegateActionsAtScope(_ context.Context, _ model.Principal, actions []string, scopeType model.RoleScopeType, scopeID string) (bool, error) {
	f.delegatedActions = append([]string(nil), actions...)
	f.delegatedScopeType = scopeType
	f.delegatedScopeID = scopeID
	return f.err == nil, f.err
}

// GrantedScopes grants f.visibility, or the whole Institution when it is nil.
func (f *authorizerFake) GrantedScopes(_ context.Context, _ model.Principal, action model.Action, _ model.ResourceType) (access.AuthorizedScopeConstraint, error) {
	f.actions = append(f.actions, action)
	if f.err != nil {
		return access.AuthorizedScopeConstraint{}, f.err
	}
	if f.visibility == nil {
		return access.AuthorizedScopeConstraint{InstitutionWide: true}, nil
	}
	return access.AuthorizedScopeConstraint{InstitutionWide: f.visibility.InstitutionWide,
		AcademicUnitRootIDs: f.visibility.AcademicUnitRootIDs, ClassIDs: f.visibility.ClassIDs}, nil
}

// AuthorizeUserSearch grants f.userVisibility, or the whole Institution
// when it is nil, and refuses with f.userSearchErr.
func (f *authorizerFake) AuthorizeUserSearch(context.Context, invocation.Invocation) (store.UserVisibilityScope, error) {
	f.userSearches++
	if f.userSearchErr != nil {
		return store.UserVisibilityScope{}, f.userSearchErr
	}
	if f.userVisibility == nil {
		return store.UserVisibilityScope{InstitutionWide: true}, nil
	}
	return *f.userVisibility, nil
}

// CanViewClassMembers grants class.members.view on exactly the Classes in
// f.classMembersVisible.
func (f *authorizerFake) CanViewClassMembers(_ context.Context, _ invocation.Invocation, resource model.Resource) (bool, error) {
	f.classMemberViews = append(f.classMemberViews, resource)
	return f.classMembersVisible[model.ClassID(resource.ID)], f.err
}

func (f *authorizerFake) Institution(context.Context) (*model.Institution, error) {
	if f.institution == nil {
		f.institution = &model.Institution{ID: model.NewInstitutionID()}
	}
	return f.institution, nil
}

func (f *authorizerFake) RecordAuthorizationDecision(_ context.Context, _ model.Principal, action model.Action, resource model.Resource,
	scopeType model.RoleScopeType, scopeID string, _ model.RequestMetadata, allowed bool,
) error {
	f.decisions = append(f.decisions, authorizationDecision{action: action, resource: resource, scopeType: scopeType, scopeID: scopeID, allowed: allowed})
	return nil
}

// institutionStoreFake answers the singleton Institution.
type institutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
}

func (s institutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

type mutationAttemptAuditorFake struct {
	events   *[]string
	beginID  string
	beginIDs []string
	beginErr error
	failErr  error
	failID   string
	failCode string
	attempt  access.Mutation
	attempts []access.Mutation
}

// Begin records the whole Mutation; the event names whether it carried an
// explicit scope, as the two positional entry points once did.
func (a *mutationAttemptAuditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	if attempt.ScopeType != "" {
		*a.events = append(*a.events, "begin-at-scope")
	} else {
		*a.events = append(*a.events, "begin")
	}

	a.attempt = attempt
	a.attempts = append(a.attempts, attempt)
	return model.AuditEventID(a.nextBeginID()), a.beginErr
}

func (a *mutationAttemptAuditorFake) nextBeginID() string {
	if len(a.beginIDs) == 0 {
		return a.beginID
	}
	id := a.beginIDs[0]
	a.beginIDs = a.beginIDs[1:]
	return id
}

func (a *mutationAttemptAuditorFake) Fail(_ context.Context, id model.AuditEventID, code string) error {
	*a.events = append(*a.events, "fail")
	a.failID = id.String()
	a.failCode = code
	return a.failErr
}

func TestVisibilityRecordsOneDecisionAndFailsClosedWithoutGrants(t *testing.T) {
	t.Parallel()
	classID := model.NewClassID()
	caller := invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{RequestID: "visibility"})

	granted := &authorizerFake{visibility: &store.InvitationVisibilityScope{ClassIDs: []model.ClassID{classID}}}
	scope, err := (&Service{authorizer: granted}).visibility(context.Background(), caller, model.ActionAcademicProgressionManage)
	require.NoError(t, err)
	require.Equal(t, store.InvitationVisibilityScope{ClassIDs: []model.ClassID{classID}}, scope)
	require.Equal(t, []authorizationDecision{{action: model.ActionAcademicProgressionManage,
		resource: model.Resource{Type: model.ResourceClass, ID: classID.String()}, scopeType: model.RoleScopeClass,
		scopeID: classID.String(), allowed: true}}, granted.decisions)

	// No grant anywhere is one recorded denial at the Institution.
	none := &authorizerFake{visibility: &store.InvitationVisibilityScope{}}
	_, err = (&Service{authorizer: none}).visibility(context.Background(), caller, model.ActionInvitationView)
	require.True(t, apperror.Is(err, "authorization.denied"), "visibility without grants = %v", err)
	institution, institutionErr := none.Institution(context.Background())
	require.NoError(t, institutionErr)
	require.Equal(t, []authorizationDecision{{action: model.ActionInvitationView,
		resource: model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}, scopeType: model.RoleScopeInstitution,
		scopeID: institution.ID.String(), allowed: false}}, none.decisions)

	// An Action outside the institution family and a missing authorizer both
	// refuse before any decision.
	other := &authorizerFake{}
	_, err = (&Service{authorizer: other}).visibility(context.Background(), caller, model.ActionRoleView)
	require.True(t, apperror.Is(err, "invitation.unavailable"), "visibility for another Action = %v", err)
	require.Empty(t, other.actions)
	_, err = (&Service{}).visibility(context.Background(), caller, model.ActionInvitationView)
	require.True(t, apperror.Is(err, "invitation.unavailable"), "visibility without an authorizer = %v", err)
	require.True(t, apperror.Is((&Service{}).authorize(context.Background(), caller, model.ActionInvitationCreate, model.Resource{}), "invitation.unavailable"))
}

func TestImportUseCasesRefuseWithoutOnboardingImports(t *testing.T) {
	t.Parallel()
	authorizer := &authorizerFake{}
	// A Service built from a reduced Store catalog holds no import Store.
	service := &Service{authorizer: authorizer, now: time.Now}
	caller := invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{})
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
	require.Empty(t, authorizer.actions, "a Service without imports authorized an import use case")
}
