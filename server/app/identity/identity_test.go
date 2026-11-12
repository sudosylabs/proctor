// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type authenticationCacheFake struct {
	mu       sync.Mutex
	values   map[string][]byte
	counters map[string]int64
}

func newAuthenticationCacheFake() *authenticationCacheFake {
	return &authenticationCacheFake{
		values:   make(map[string][]byte),
		counters: make(map[string]int64),
	}
}

func (c *authenticationCacheFake) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	if !ok {
		return nil, ErrAuthenticationCacheMiss
	}
	return append([]byte(nil), value...), nil
}

func (c *authenticationCacheFake) SetAlways(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func (c *authenticationCacheFake) SetIfAbsent(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; ok {
		return ErrAuthenticationCacheNotStored
	}
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func (c *authenticationCacheFake) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key)
	delete(c.counters, key)
	return nil
}

func (c *authenticationCacheFake) Add(_ context.Context, key string, delta int64, _ time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counters[key] += delta
	return c.counters[key], nil
}

type authenticationStoreFake struct {
	users               map[string]*model.User
	usersByUsername     map[string]*model.User
	usersByEmail        map[string]*model.User
	passwords           map[string]*model.PasswordCredential
	sessions            map[string]*model.Session
	accessByHash        map[string]*model.SessionCredential
	sessionByCredential map[string]*model.Session
	saveErr             error
	// completedAudits records the login attempts Session creation completed
	// atomically; auditCompleteErr makes that completion, and so creation, fail.
	completedAudits   map[string]map[string]any
	auditCompleteErr  error
	maximumPerUser    int
	createdJob        *model.Job
	createdSettings   *model.UserSettingsDocument
	rotation          *store.SessionRotation
	userGetErr        error
	rotatedAccess     *model.SessionCredential
	rotatedRefresh    *model.SessionCredential
	rotatedAt         time.Time
	rotatedIdleExpiry time.Time
	rotationInput     *store.SessionRefreshRotation
}

func (authenticationDesktopRegistrationLookupFake) Get(context.Context, model.DesktopRegistrationID) (*model.DesktopRegistration, error) {
	return nil, store.NewErrNotFound("desktop_registration", "")
}

type authenticationDesktopRegistrationLookupFake struct{ store.DesktopRegistrationStore }

func testAuthenticationDPoP(t *testing.T, cache AuthenticationCache) *dpopSecurity {
	t.Helper()
	dpop, err := newDPoPSecurity(dpopSecurityDependencies{Cache: cache, Policy: dpopPolicy{Origin: "https://proctor.test", NonceLifetime: 5 * time.Minute,
		ProofLifetime: 5 * time.Minute, ClockSkew: time.Minute, NewNonce: model.NewCredentialToken, Now: time.Now}})
	require.NoError(t, err)
	return dpop
}

func (s *authenticationStoreFake) File() store.FileStore { return nil }

func (s *authenticationStoreFake) Job() store.JobStore { return nil }

func newAuthenticationStoreFake() *authenticationStoreFake {
	return &authenticationStoreFake{
		users:               make(map[string]*model.User),
		usersByUsername:     make(map[string]*model.User),
		usersByEmail:        make(map[string]*model.User),
		passwords:           make(map[string]*model.PasswordCredential),
		sessions:            make(map[string]*model.Session),
		accessByHash:        make(map[string]*model.SessionCredential),
		sessionByCredential: make(map[string]*model.Session),
		completedAudits:     make(map[string]map[string]any),
		maximumPerUser:      10,
	}
}

func (s *authenticationStoreFake) User() store.UserStore { return authenticationUserStore{s} }

func (s *authenticationStoreFake) PasswordCredential() store.PasswordCredentialStore {
	return authenticationPasswordStore{s}
}

func (s *authenticationStoreFake) Session() store.SessionStore { return authenticationSessionStore{s} }

func (s *authenticationStoreFake) SessionCredential() store.SessionCredentialStore {
	return authenticationSessionCredentialStore{s}
}

func (s *authenticationStoreFake) MFA() store.MFAStore { return nil }

func (s *authenticationStoreFake) PersonalAccessToken() store.PersonalAccessTokenStore {
	return nil
}

func (s *authenticationStoreFake) Institution() store.InstitutionStore { return nil }

func (s *authenticationStoreFake) AcademicUnit() store.AcademicUnitStore { return nil }

func (s *authenticationStoreFake) Programme() store.ProgrammeStore { return nil }

func (s *authenticationStoreFake) ProgrammeLevel() store.ProgrammeLevelStore { return nil }

func (s *authenticationStoreFake) AcademicPeriod() store.AcademicPeriodStore { return nil }

func (s *authenticationStoreFake) Class() store.ClassStore { return nil }

func (s *authenticationStoreFake) ExternalIdentity() store.ExternalIdentityStore { return nil }

func (s *authenticationStoreFake) ExternalLoginState() store.ExternalLoginStateStore { return nil }

func (s *authenticationStoreFake) UserToken() store.UserTokenStore { return nil }

func (s *authenticationStoreFake) Affiliation() store.AffiliationStore { return nil }

func (s *authenticationStoreFake) AcademicUnitMember() store.AcademicUnitMemberStore { return nil }

func (s *authenticationStoreFake) ClassMember() store.ClassMemberStore { return nil }

func (s *authenticationStoreFake) Role() store.RoleStore { return nil }

func (s *authenticationStoreFake) RoleBinding() store.RoleBindingStore { return nil }

func (s *authenticationStoreFake) Audit() store.AuditStore { return nil }

func (s *authenticationStoreFake) Installation() store.InstallationStore { return nil }

func (s *authenticationStoreFake) ClusterDiscovery() store.ClusterDiscoveryStore { return nil }

func (s *authenticationStoreFake) Ping(context.Context) error { return nil }

func (s *authenticationStoreFake) GetDBSchemaVersion(context.Context) (int, error) { return 0, nil }

func (s *authenticationStoreFake) GetLocalSchemaVersion() (int, error) { return 0, nil }

func (s *authenticationStoreFake) ValidateSchema(context.Context) error { return nil }

func (s *authenticationStoreFake) Close() error { return nil }

type authenticationUserStore struct{ root *authenticationStoreFake }

func (s authenticationUserStore) GetCurrentContext(context.Context, model.UserID, int) (*store.CurrentUserContext, error) {
	return nil, errors.New("current context is not implemented by authentication test store")
}

func (s authenticationUserStore) RegisterLocal(context.Context, *store.PublicLocalUserRegistration) (*store.PublicLocalUserRegistrationResult, error) {
	return nil, errors.New("public registration is not implemented by authentication test store")
}

func (s authenticationUserStore) Create(
	_ context.Context,
	input *store.UserCreation,
) (*store.UserCreationResult, error) {
	if input.Settings == nil || input.Settings.UserID != input.User.ID ||
		input.Settings.Source != model.UserSettingsInitialSource ||
		input.Settings.FormatVersion != model.UserSettingsFormatVersion1 {
		return nil, errors.New("invalid initial user settings")
	}
	cloned := *input.User
	s.root.users[cloned.ID.String()] = &cloned
	s.root.usersByUsername[strings.ToLower(cloned.Username)] = &cloned
	s.root.usersByEmail[strings.ToLower(cloned.Email)] = &cloned
	pass := *input.PasswordCredential
	s.root.passwords[cloned.ID.String()] = &pass
	job := *input.DefaultProfilePictureJob
	s.root.createdJob = &job
	s.root.createdSettings = input.Settings.Clone()
	return &store.UserCreationResult{User: &cloned, PasswordCredential: &pass}, nil
}

func (s authenticationUserStore) Get(_ context.Context, id model.UserID) (*model.User, error) {
	if s.root.userGetErr != nil {
		return nil, s.root.userGetErr
	}
	user, ok := s.root.users[id.String()]
	if !ok {
		return nil, store.NewErrNotFound("user", id.String())
	}
	cloned := *user
	return &cloned, nil
}

func (s authenticationUserStore) GetByUsername(_ context.Context, username string) (*model.User, error) {
	user, ok := s.root.usersByUsername[strings.ToLower(username)]
	if !ok {
		return nil, store.NewErrNotFound("user", username)
	}
	cloned := *user
	return &cloned, nil
}

func (s authenticationUserStore) GetByEmail(_ context.Context, email string) (*model.User, error) {
	user, ok := s.root.usersByEmail[strings.ToLower(email)]
	if !ok {
		return nil, store.NewErrNotFound("user", email)
	}
	cloned := *user
	return &cloned, nil
}

// Remaining UserStore methods are unused by the focused authentication paths.
func (authenticationUserStore) List(context.Context, store.UserListOptions) ([]*model.User, error) {
	return nil, errors.New("unused")
}

func (authenticationUserStore) Count(context.Context, store.UserListOptions, int) (int, error) {
	return 0, errors.New("unused")
}

func (authenticationUserStore) ListIdentities(context.Context, []model.UserID) ([]store.UserIdentity, error) {
	return nil, errors.New("unused")
}

func (authenticationUserStore) ListDirectorySummaries(context.Context, store.UserDirectorySummaryOptions) ([]store.UserDirectorySummary, error) {
	return nil, errors.New("unused")
}

func (authenticationUserStore) ListDisplayNames(context.Context, []model.UserID) ([]store.UserDisplayName, error) {
	return nil, errors.New("unused")
}

func (authenticationUserStore) MatchVisibility(context.Context, model.UserID, store.UserVisibilityScope) (store.UserVisibilityMatch, error) {
	return store.UserVisibilityMatch{}, errors.New("unused")
}

func (authenticationUserStore) SetDisabled(context.Context, *store.UserDisabledStateChange) (*store.UserDisabledStateResult, error) {
	return nil, errors.New("unused")
}

func (authenticationUserStore) UpdateProfile(context.Context, *store.UserProfileUpdate) (*model.User, error) {
	return nil, errors.New("unused")
}

func (authenticationUserStore) UpdateLastLogin(ctx context.Context, input *store.UserLastLoginUpdate) error {
	return errors.New("unused")
}

type authenticationPasswordStore struct{ root *authenticationStoreFake }

func (s authenticationPasswordStore) GetByUser(_ context.Context, userID model.UserID) (*model.PasswordCredential, error) {
	credential, ok := s.root.passwords[userID.String()]
	if !ok {
		return nil, store.NewErrNotFound("password_credential", userID.String())
	}
	cloned := *credential
	return &cloned, nil
}

func (s authenticationPasswordStore) Rehash(_ context.Context, input *store.PasswordCredentialRehash) error {
	credential := s.root.passwords[input.UserID.String()]
	if credential == nil || credential.ArchivedAt.Valid || credential.ID != input.ID ||
		credential.Revision != input.ExpectedRevision || credential.PasswordHash != input.ExpectedHash {
		return store.ErrPasswordCredentialChanged
	}
	cloned := *credential
	cloned.PasswordHash = input.PasswordHash
	s.root.passwords[input.UserID.String()] = &cloned
	return nil
}

func (authenticationPasswordStore) Save(context.Context, *model.PasswordCredential) (*model.PasswordCredential, error) {
	return nil, errors.New("unused")
}

func (authenticationPasswordStore) Enroll(context.Context, *store.PasswordCredentialEnrollment) (*store.AuthenticationMethodMutationResult, error) {
	return nil, errors.New("unused")
}

func (authenticationPasswordStore) Remove(context.Context, *store.PasswordCredentialRemoval) (*store.AuthenticationMethodMutationResult, error) {
	return nil, errors.New("unused")
}

func (authenticationPasswordStore) Change(context.Context, *store.PasswordCredentialChange) (*store.AuthenticationMethodMutationResult, error) {
	return nil, errors.New("unused")
}

type authenticationSessionStore struct{ root *authenticationStoreFake }

func (s authenticationSessionStore) Save(
	_ context.Context,
	input *store.SessionCreation,
) (*model.Session, []*model.SessionCredential, error) {
	if s.root.saveErr != nil {
		return nil, nil, s.root.saveErr
	}
	if !input.AuditEventID.IsValid() || input.AuditAt.IsZero() {
		return nil, nil, store.NewErrInvalidInput("session", "audit", nil)
	}
	if s.root.auditCompleteErr != nil {
		return nil, nil, s.root.auditCompleteErr
	}
	session, credentials, maximumPerUser := input.Session, input.Credentials, input.MaximumActive
	if session.AuthenticationMethod == "password" {
		credential := s.root.passwords[session.UserID.String()]
		if credential == nil || credential.ArchivedAt.Valid || credential.ID != input.PasswordProof.ID ||
			credential.Revision != input.PasswordProof.Revision {
			return nil, nil, store.ErrPasswordCredentialChanged
		}
	}
	if maximumPerUser > 0 {
		count := 0
		for _, existing := range s.root.sessions {
			if existing.UserID == session.UserID && !existing.RevokedAt.Valid {
				count++
			}
		}
		if count >= maximumPerUser {
			return nil, nil, store.NewErrConflict("session", "sessions_maximum_per_user", errors.New("max"))
		}
	}
	cloned := *session
	now := cloned.LastActivityAt
	if now.IsZero() {
		now = model.NowUTC()
	}
	if cloned.ID.IsZero() {
		cloned.PrepareCreate(model.NewSessionID(), now)
	} else {
		cloned.PrepareUpdate(now)
	}
	s.root.sessions[cloned.ID.String()] = &cloned
	savedCredentials := make([]*model.SessionCredential, 0, len(credentials))
	for _, credential := range credentials {
		item := *credential
		item.SessionID = cloned.ID
		if item.ID.IsZero() {
			item.PrepareCreate(model.NewSessionCredentialID(), now)
		}
		savedCredentials = append(savedCredentials, &item)
		if item.Kind == model.SessionCredentialAccess {
			s.root.accessByHash[item.TokenHash] = &item
			s.root.sessionByCredential[item.TokenHash] = &cloned
		}
	}
	s.root.completedAudits[input.AuditEventID.String()] = cloned.Auditable()
	return &cloned, savedCredentials, nil
}

func (s authenticationSessionStore) UpdateActivity(_ context.Context, input *store.SessionActivityUpdate) error {
	sessionID := input.ID
	lastActivityAt := input.LastActivityAt
	idleExpiresAt := input.IdleExpiresAt
	session, ok := s.root.sessions[sessionID.String()]
	if !ok {
		return store.NewErrNotFound("session", sessionID.String())
	}
	at := model.TimeUTC(lastActivityAt)
	session.LastActivityAt = at
	session.IdleExpiresAt = model.TimeUTC(idleExpiresAt)
	session.UpdatedAt = at
	return nil
}

func (s authenticationSessionStore) Withdraw(_ context.Context, input *store.SessionWithdrawal) ([]string, error) {
	sessionID := input.ID
	revokedAt := input.RevokedAt
	reason := input.Reason
	return s.revoke(sessionID.String(), model.TimeUTC(revokedAt), reason)
}

func (s authenticationSessionStore) revoke(sessionID string, at time.Time, reason model.SessionRevocationReason) ([]string, error) {
	session, ok := s.root.sessions[sessionID]
	if !ok {
		return nil, store.NewErrNotFound("session", sessionID)
	}
	session.RevokedAt = model.OptionalTimeFrom(at)
	session.RevocationReason = reason
	if session.UpdatedAt.Before(at) {
		session.UpdatedAt = at
	}
	var hashes []string
	for hash, credential := range s.root.accessByHash {
		if credential.SessionID.String() == sessionID {
			hashes = append(hashes, hash)
			delete(s.root.accessByHash, hash)
			delete(s.root.sessionByCredential, hash)
		}
	}
	return hashes, nil
}

func (s authenticationSessionStore) EnforceExpiry(_ context.Context, input *store.SessionExpiryEnforcement) (*store.SessionExpiryEnforcementResult, error) {
	sessionID := input.ID
	userID := input.UserID
	at := input.At
	session, ok := s.root.sessions[sessionID.String()]
	if !ok || session.UserID != userID || session.RevokedAt.Valid {
		return nil, store.NewErrNotFound("session", sessionID.String())
	}
	at = model.TimeUTC(at)
	if !session.IsExpiredAt(at) {
		cloned := *session
		return &store.SessionExpiryEnforcementResult{Session: &cloned}, nil
	}
	hashes, err := s.revoke(sessionID.String(), at, model.SessionRevocationExpired)
	if err != nil {
		return nil, err
	}
	cloned := *session
	return &store.SessionExpiryEnforcementResult{
		Session: &cloned, TokenHashes: hashes, Expired: true,
	}, nil
}

func (s authenticationSessionStore) Get(_ context.Context, id model.SessionID) (*model.Session, error) {
	session, ok := s.root.sessions[id.String()]
	if !ok {
		return nil, store.NewErrNotFound("session", id.String())
	}
	cloned := *session
	return &cloned, nil
}

func (authenticationSessionStore) ListByUser(context.Context, *store.SessionPageQuery) ([]*model.Session, error) {
	return nil, errors.New("unused")
}

func (authenticationSessionStore) PurgeRetired(context.Context, *store.SessionPurge) (*store.SessionPurgeResult, error) {
	return nil, errors.New("unused")
}

func (authenticationSessionStore) ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.Session, error) {
	return nil, errors.New("unused")
}

func (s authenticationSessionStore) Revoke(_ context.Context, input *store.SessionRevocation) (*store.SessionRevocationResult, error) {
	session := s.root.sessions[input.SessionID.String()]
	if session == nil || session.UserID != input.UserID || session.RevokedAt.Valid {
		if input.Reason == model.SessionRevocationUserLogout {
			return &store.SessionRevocationResult{}, nil
		}
		return nil, store.NewErrNotFound("session", input.SessionID.String())
	}
	hashes, err := s.revoke(input.SessionID.String(), model.TimeUTC(input.RevokedAt), input.Reason)
	if err != nil {
		return nil, err
	}
	cloned := *session
	return &store.SessionRevocationResult{Session: &cloned, TokenHashes: hashes}, nil
}

func (authenticationSessionStore) RevokeAllForUser(context.Context, *store.UserSessionsRevocation) (*store.UserSessionsRevocationResult, error) {
	return nil, errors.New("unused")
}

func (authenticationSessionStore) Rename(context.Context, *store.SessionRename) (*model.Session, error) {
	return nil, errors.New("unused")
}

type authenticationSessionCredentialStore struct{ root *authenticationStoreFake }

func (s authenticationSessionCredentialStore) GetSessionByTokenHash(
	_ context.Context,
	tokenHash string,
	kind model.SessionCredentialKind,
) (*model.SessionCredential, *model.Session, error) {
	if kind == model.SessionCredentialRefresh && s.root.rotation != nil && s.root.rotation.Session != nil {
		session := *s.root.rotation.Session
		return &model.SessionCredential{Kind: kind, TokenHash: tokenHash}, &session, nil
	}
	if kind != model.SessionCredentialAccess {
		return nil, nil, store.NewErrNotFound("session_credential", tokenHash)
	}
	credential, ok := s.root.accessByHash[tokenHash]
	if !ok {
		return nil, nil, store.NewErrNotFound("session_credential", tokenHash)
	}
	session, ok := s.root.sessionByCredential[tokenHash]
	if !ok {
		return nil, nil, store.NewErrNotFound("session", tokenHash)
	}
	credentialClone := *credential
	sessionClone := *session
	return &credentialClone, &sessionClone, nil
}

func (authenticationSessionCredentialStore) PurgeRetired(context.Context, *store.SessionCredentialPurge) (int64, error) {
	return 0, errors.New("unused")
}

func (s authenticationSessionCredentialStore) RotateRefresh(_ context.Context, input *store.SessionRefreshRotation) (*store.SessionRotation, error) {
	access := input.Access
	refresh := input.Refresh
	at := input.Now
	idleExpiry := input.IdleExpiresAt
	s.root.rotatedAccess = access
	s.root.rotatedRefresh = refresh
	s.root.rotatedAt = at
	s.root.rotatedIdleExpiry = idleExpiry
	s.root.rotationInput = input
	if s.root.rotation == nil {
		return nil, errors.New("unused")
	}
	result := *s.root.rotation
	if !result.ReplayDetected {
		result.AccessCredential = access
		result.RefreshCredential = refresh
	}
	return &result, nil
}

func newTestAuthenticationService(t *testing.T, persistence *authenticationStoreFake) *Service {
	return newTestAuthenticationServiceWithCache(t, persistence, newAuthenticationCacheFake())
}

func newTestAuthenticationServiceWithCache(
	t *testing.T,
	persistence *authenticationStoreFake,
	cache AuthenticationCache,
) *Service {
	return newTestAuthenticationServiceWithRuntime(
		t, persistence, cache, model.NewCredentialToken, time.Now,
	)
}

func newTestAuthenticationServiceWithRuntime(
	t *testing.T,
	persistence *authenticationStoreFake,
	cache AuthenticationCache,
	newCredential func() string,
	now func() time.Time,
) *Service {
	return newTestAuthenticationServiceWithPorts(
		t,
		persistence,
		cache,
		&mfaApplicationStoreFake{},
		discardPersonalAccessTokenStore{},
		newCredential,
		now,
	)
}

func newTestAuthenticationServiceWithPorts(
	t *testing.T,
	persistence *authenticationStoreFake,
	cache AuthenticationCache,
	mfaCredentials store.MFAStore,
	personalTokens store.PersonalAccessTokenStore,
	newCredential func() string,
	now func() time.Time,
) *Service {
	return newTestAuthenticationServiceWithEffects(
		t, persistence, cache, &realtimetest.Publisher{}, mfaCredentials,
		personalTokens, newCredential, now,
	)
}

func newTestAuthenticationServiceWithEffects(
	t *testing.T,
	persistence *authenticationStoreFake,
	cache AuthenticationCache,
	effects realtime.Publisher,
	mfaCredentials store.MFAStore,
	personalTokens store.PersonalAccessTokenStore,
	newCredential func() string,
	now func() time.Time,
) *Service {
	t.Helper()
	settings := testPasswordPolicy()
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: settings, Recorder: nil})
	require.NoError(t, err)
	service := &Service{
		users: persistence.User(), passwords: persistence.PasswordCredential(),
		sessions: persistence.Session(), sessionCredentials: persistence.SessionCredential(),
		desktopRegistrations: authenticationDesktopRegistrationLookupFake{}, personalAccessTokens: personalTokens,
		accessPolicy: allowAllAuthenticationAccessPolicy(), dpop: testAuthenticationDPoP(t, cache), cache: cache,
		attempts: mustAuthenticationAttemptAccounting(t, cache), hasher: hasher,
		auditor:       &mutationAttemptAuditorFake{events: &[]string{}, beginID: model.NewID()},
		securityAudit: &securityAuditorFake{}, publisher: effects, mfaCredentials: mfaCredentials, mfa: mustTestMFAMechanics(t), diagnostics: newDiscardLogger(t),
		sessionPolicy: SessionPolicy{
			AccessTTL:              time.Hour,
			RefreshTTL:             24 * time.Hour,
			IdleTTL:                2 * time.Hour,
			AbsoluteTTL:            7 * 24 * time.Hour,
			ActivityUpdateInterval: time.Minute,
			MaximumPerUser:         10,
		}, loginRateLimit: LoginRateLimitPolicy{
			Window:                     time.Minute,
			MaximumAttempts:            20,
			MaximumSourceAttempts:      100,
			AccountFailuresBeforeDelay: 50,
			MaximumAccountFailureDelay: time.Second,
		}, newCredential: newCredential, now: now, wait: waitContext,
	}
	require.NoError(t, service.requireSessionAuthentication())
	return service
}

func mustAuthenticationAttemptAccounting(
	t *testing.T,
	cache AttemptCache,
) *AttemptAccounting {
	t.Helper()
	accounting, err := NewAttemptAccounting(AttemptAccountingDependencies{Cache: cache})
	require.NoError(t, err)
	return accounting
}

// discardPersonalAccessTokenStore resolves no personal access token.
type discardPersonalAccessTokenStore struct{ store.PersonalAccessTokenStore }

func (discardPersonalAccessTokenStore) Resolve(
	context.Context,
	string,
	time.Time,
	time.Duration,
) (*store.PersonalAccessTokenResolution, error) {
	return nil, store.NewErrNotFound("personal_access_token", "")
}
