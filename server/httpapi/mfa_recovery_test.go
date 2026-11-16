// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestMFARestrictedSessionOnlyReachesExplicitRecoveryRoutes(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "oidc", AuthenticationProviderID: "campus", ExternalIdentityID: model.NewExternalIdentityID(), AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now(), AuthenticationGeneration: 1, MFARecoveryRequired: true}
	mfa := newAccountMFA(newAccountSessions())
	fixture := newIdentityFixture(t, identityOptions{
		Stores: identityStores{
			Users:     newIdentityUsers(accountUser(principal.UserID)),
			Passwords: &identityPasswords{byUser: map[model.UserID]*model.PasswordCredential{}},
			MFA:       mfa,
		},
		MFA: true,
	})
	mfa.audits = fixture.access.Audits
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, mfaResource(fixture.service))
	for _, test := range []struct {
		path, method string
		status       int
	}{
		{"/api/v1/users/me/mfa", http.MethodGet, 200}, {"/api/v1/users/me/mfa/setup", http.MethodPost, 201},
		{"/api/v1/users/me/mfa/challenge", http.MethodPost, 401}, {"/api/v1/users/me/mfa/recovery-codes/regenerate", http.MethodPost, 401}, {"/api/v1/users/me/mfa/disable", http.MethodPost, 401},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(`{"code":"123456"}`))
			request.Header.Set("Authorization", "Bearer recovery")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code, "%s = %d: %s", test.path, response.Code, response.Body.String())
			if response.Code < 300 {
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "recovery response is cacheable")
			}
			if test.path == "/api/v1/users/me/mfa" {
				require.Contains(t, response.Body.String(), `"mfa_recovery_required":true`)
			}
		})
	}
	// Only the setup reached the MFA Store: a pending enrollment of the
	// restricted Session, sealed and audited. The refused routes wrote nothing.
	pending := mfa.pendingEnrollments()
	require.Len(t, pending, 1)
	require.Equal(t, principal.SessionID, pending[0].Principal.SessionID)
	require.Equal(t, model.MFAStatePending, pending[0].Credential.State)
	require.NotEmpty(t, pending[0].Credential.EncryptionKeyID, "the TOTP secret is sealed")
	require.Empty(t, mfa.challengeInputs())
	require.Equal(t, []string{"mfa.setup:success"}, accountAuditOutcomes(fixture))
}

// A Session the Access Policy restricts to MFA enrollment shares the recovery
// routes and also reaches the second-factor challenge, which can satisfy the
// policy for a User who already has a second factor; nothing else.
func TestMFAEnrollmentSessionReachesRecoveryRoutesAndChallenge(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "oidc", AuthenticationProviderID: "campus", ExternalIdentityID: model.NewExternalIdentityID(), AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now(), MFAEnrollmentRequired: true}
	const recoveryCode = "abcd-efgh-ijkl-mnop-qrst-uvwx"
	for _, test := range []struct {
		path, method string
		// secondFactor gives the User an active second factor with one
		// recovery code, which the challenge presents.
		secondFactor bool
		status       int
	}{
		{"/api/v1/users/me/mfa", http.MethodGet, false, 200}, {"/api/v1/users/me/mfa/setup", http.MethodPost, false, 201},
		{"/api/v1/users/me/mfa/challenge", http.MethodPost, true, 200}, {"/api/v1/users/me/mfa/recovery-codes/regenerate", http.MethodPost, true, 401}, {"/api/v1/users/me/mfa/disable", http.MethodPost, true, 401},
	} {
		t.Run(test.path, func(t *testing.T) {
			session := accountSession(principal.UserID, principal.SessionID, principal.AuthenticatedAt)
			session.AuthenticationMethod, session.MFAEnrollmentRequired = "oidc", true
			mfa := newAccountMFA(newAccountSessions(session))
			if test.secondFactor {
				mfa.enable(principal.UserID, time.Now().Add(-time.Hour))
				mfa.addRecoveryCode("abcdefghijklmnopqrstuvwx")
			}
			fixture := newIdentityFixture(t, identityOptions{
				Stores: identityStores{
					Users:     newIdentityUsers(accountUser(principal.UserID)),
					Passwords: &identityPasswords{byUser: map[model.UserID]*model.PasswordCredential{}},
					MFA:       mfa,
				},
				MFA: true,
			})
			mfa.audits = fixture.access.Audits
			api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, mfaResource(fixture.service))
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(`{"code":"`+recoveryCode+`"}`))
			request.Header.Set("Authorization", "Bearer enrollment")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code, "%s = %d: %s", test.path, response.Code, response.Body.String())
			switch test.path {
			case "/api/v1/users/me/mfa":
				require.Contains(t, response.Body.String(), `"mfa_enrollment_required":true`)
			case "/api/v1/users/me/mfa/setup":
				require.Len(t, mfa.pendingEnrollments(), 1)
			case "/api/v1/users/me/mfa/challenge":
				challenges := mfa.challengeInputs()
				require.Len(t, challenges, 1)
				require.Equal(t, principal.SessionID, challenges[0].Principal.SessionID)
				require.Equal(t, model.HashToken("abcdefghijklmnopqrstuvwx"), challenges[0].RecoveryCodeHash)
				require.Contains(t, response.Body.String(), `"authentication_strength":"multi_factor"`)
				require.Contains(t, response.Body.String(), `"mfa_enrollment_required":false`)
				require.Equal(t, []string{"mfa.challenge:success"}, accountAuditOutcomes(fixture))
			default:
				require.Empty(t, accountAuditOutcomes(fixture), "a refused route reaches no use case")
			}
		})
	}
}

func TestMFAStatusResponseMapsPasswordRecoveryAndRecentWindow(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	authenticatedAt := time.Now().UTC().Truncate(time.Second)
	read := func(principal model.Principal, passwords map[model.UserID]*model.PasswordCredential, mfa *accountMFA, recent time.Duration) string {
		fixture := newIdentityFixture(t, identityOptions{
			Stores: identityStores{Passwords: &identityPasswords{byUser: passwords}, MFA: mfa},
			MFA:    true, RecentAuthenticationTTL: recent,
		})
		api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, mfaResource(fixture.service))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/mfa", nil)
		request.Header.Set("Authorization", "Bearer status")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		return response.Body.String()
	}

	local := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: authenticatedAt}
	enrolled := newAccountMFA(newAccountSessions())
	enrolled.enable(local.UserID, time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC))
	enrolled.summary = &store.MFARecoveryCodeSummary{Remaining: 7, Issued: 10, GeneratedAt: time.Date(2026, 7, 9, 10, 15, 0, 0, time.UTC)}
	body := read(local, map[model.UserID]*model.PasswordCredential{local.UserID: {
		ID: model.NewPasswordCredentialID(), UserID: local.UserID, PasswordChangedAt: time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC),
	}}, enrolled, 15*time.Minute+59*time.Second)
	require.Contains(t, body, `"enabled":true`)
	require.Contains(t, body, `"recently_authenticated":true`)
	require.Contains(t, body, `"recovery_codes_remaining":7`)
	require.Contains(t, body, `"recovery_codes_total":10`)
	require.Contains(t, body, `"method":"authenticator_app"`)
	require.Contains(t, body, `"enabled_at":"2026-06-02T08:00:00Z"`)
	require.Contains(t, body, `"recovery_codes_generated_at":"2026-07-09T10:15:00Z"`)
	require.Contains(t, body, `"password_changed_at":"2026-07-01T09:30:00Z"`)
	// The window is reported in whole minutes, rounded down.
	require.Contains(t, body, `"recent_authentication_minutes":15`)
	require.Contains(t, body, `"recently_authenticated_until":"`+authenticatedAt.Add(15*time.Minute+59*time.Second).Format(time.RFC3339)+`"`)

	// An external Session past the recent window, with no password and no
	// second factor, reports none of their details.
	external := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "oidc", AuthenticationProviderID: "campus", ExternalIdentityID: model.NewExternalIdentityID(), AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: authenticatedAt.Add(-time.Hour)}
	body = read(external, map[model.UserID]*model.PasswordCredential{}, newAccountMFA(newAccountSessions()), 30*time.Second)
	require.Contains(t, body, `"recently_authenticated":false`)
	require.NotContains(t, body, "password_changed_at")
	require.NotContains(t, body, "recently_authenticated_until")
	require.NotContains(t, body, `"method"`)
	require.NotContains(t, body, "enabled_at")
	require.NotContains(t, body, "recovery_codes_generated_at")
	// A window shorter than a minute is reported as one minute.
	require.Contains(t, body, `"recent_authentication_minutes":1`)
}

// accountAudit completes a mutation attempt in the Store's commit, as the SQL
// Stores complete the audit they were handed.
type accountAudit struct{ audits *accesstest.Audits }

func (a *accountAudit) succeed(ctx context.Context, id model.AuditEventID) error {
	if a.audits == nil {
		panic("the fake Store was not given the fixture's audits")
	}
	_, err := a.audits.Complete(ctx, &store.AuditCompletion{ID: id, Status: model.AuditStatusSuccess, UpdateAt: time.Now()})
	return err
}

// accountMFA holds each User's second factor and recovery codes, the MFA key
// ring state, and the MFA mutations requests commit.
type accountMFA struct {
	store.MFAStore
	accountAudit
	mu          sync.Mutex
	sessions    *accountSessions
	credentials map[model.UserID]*model.MFACredential
	// recoveryCodes maps each live recovery code hash to whether it was used.
	recoveryCodes map[string]bool
	summary       *store.MFARecoveryCodeSummary
	keyState      store.MFAKeyState
	pending       []store.MFAPendingEnrollment
	challenges    []store.MFAChallenge
	rekeys        []store.MFARekeyStart
}

func newAccountMFA(sessions *accountSessions) *accountMFA {
	return &accountMFA{sessions: sessions, credentials: map[model.UserID]*model.MFACredential{}, recoveryCodes: map[string]bool{}}
}

// enable gives the User an active second factor.
func (s *accountMFA) enable(userID model.UserID, activatedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials[userID] = &model.MFACredential{
		ID: model.NewMFACredentialID(), CreatedAt: activatedAt, UpdatedAt: activatedAt, UserID: userID,
		State: model.MFAStateActive, ActivatedAt: model.OptionalTimeFrom(activatedAt),
	}
}

// addRecoveryCode stores the hash of a normalized recovery code.
func (s *accountMFA) addRecoveryCode(normalized string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveryCodes[model.HashToken(normalized)] = false
}

func (s *accountMFA) credential(userID model.UserID) *model.MFACredential {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credentials[userID]
}

func (s *accountMFA) pendingEnrollments() []store.MFAPendingEnrollment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.MFAPendingEnrollment(nil), s.pending...)
}

func (s *accountMFA) challengeInputs() []store.MFAChallenge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.MFAChallenge(nil), s.challenges...)
}

func (s *accountMFA) rekeyInputs() []store.MFARekeyStart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.MFARekeyStart(nil), s.rekeys...)
}

func (s *accountMFA) GetByUser(_ context.Context, userID model.UserID) (*model.MFACredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, ok := s.credentials[userID]
	if !ok {
		return nil, store.NewErrNotFound("mfa_credential", userID.String())
	}
	copied := *credential
	return &copied, nil
}

func (s *accountMFA) SummarizeRecoveryCodes(context.Context, model.UserID) (*store.MFARecoveryCodeSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.summary == nil {
		return &store.MFARecoveryCodeSummary{}, nil
	}
	summary := *s.summary
	return &summary, nil
}

// SavePending refuses a User who already has an active second factor, as the
// SQL Store does.
func (s *accountMFA) SavePending(ctx context.Context, input *store.MFAPendingEnrollment) (*model.MFACredential, error) {
	s.mu.Lock()
	s.pending = append(s.pending, *input)
	if s.credentials[input.Principal.UserID].IsActive() {
		s.mu.Unlock()
		return nil, store.NewErrConflict("mfa_credential", "mfa_already_enabled", nil)
	}
	at := model.TimeUTC(time.Now())
	candidate := *input.Credential
	candidate.PendingExpiresAt = model.OptionalTimeFrom(at.Add(input.Lifetime))
	candidate.PrepareCreate(model.NewMFACredentialID(), at)
	s.credentials[candidate.UserID] = &candidate
	s.mu.Unlock()
	if err := s.succeed(ctx, input.AuditEventID); err != nil {
		return nil, err
	}
	saved := candidate
	return &saved, nil
}

// Challenge consumes an unused recovery code of the active credential and
// upgrades the requesting Session.
func (s *accountMFA) Challenge(ctx context.Context, input *store.MFAChallenge) (*store.SessionReauthenticationResult, error) {
	s.mu.Lock()
	s.challenges = append(s.challenges, *input)
	credential := s.credentials[input.Principal.UserID]
	used, known := s.recoveryCodes[input.RecoveryCodeHash]
	if !credential.IsActive() || credential.ID != input.CredentialID || input.RecoveryCodeHash == "" || !known || used {
		s.mu.Unlock()
		return nil, store.NewErrNotFound("mfa_credential", input.CredentialID.String())
	}
	s.recoveryCodes[input.RecoveryCodeHash] = true
	s.mu.Unlock()
	session, ok := s.sessions.upgrade(input.Principal.SessionID, input.VerifiedAt)
	if !ok {
		return nil, store.NewErrNotFound("session", input.Principal.SessionID.String())
	}
	if err := s.succeed(ctx, input.AuditEventID); err != nil {
		return nil, err
	}
	return &store.SessionReauthenticationResult{Session: session}, nil
}

func (s *accountMFA) InspectKeyState(context.Context) (*store.MFAKeyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.keyState
	state.Active = append([]store.MFASecretKeyUsage(nil), s.keyState.Active...)
	return &state, nil
}

func (s *accountMFA) StartRekey(ctx context.Context, input *store.MFARekeyStart) (*store.MFARekeyOperation, error) {
	s.mu.Lock()
	s.rekeys = append(s.rekeys, *input)
	s.keyState.RequiredPrimaryKeyID, s.keyState.ActiveRekeyJobID = input.PrimaryKeyID, input.Job.ID
	s.mu.Unlock()
	if err := s.succeed(ctx, input.AuditEventID); err != nil {
		return nil, err
	}
	return &store.MFARekeyOperation{JobID: input.Job.ID, PrimaryKeyID: input.PrimaryKeyID, RetiringKeyID: input.RetiringKeyID, CreatedAt: input.Job.CreatedAt}, nil
}
