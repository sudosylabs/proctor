// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuthenticationCacheInvalidatorRequiresDependencies(t *testing.T) {
	t.Parallel()

	_, err := NewCacheInvalidator(CacheInvalidatorDependencies{Cache: nil, Diagnostics: newDiscardLogger(t)})
	require.Error(t, err, "nil cache was accepted")
	_, err = NewCacheInvalidator(CacheInvalidatorDependencies{Cache: newAuthenticationCacheFake(), Diagnostics: nil})
	require.Error(t, err, "nil diagnostics was accepted")
}

func TestAuthenticationServiceRequiresSecurityDependencies(t *testing.T) {
	t.Parallel()

	persistence := newAuthenticationStoreFake()
	cache := newAuthenticationCacheFake()
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy(), Recorder: nil})
	require.NoError(t, err)
	type dependencies struct {
		users              store.UserStore
		passwords          store.PasswordCredentialStore
		sessions           store.SessionStore
		sessionCredentials store.SessionCredentialStore
		attempts           *AttemptAccounting
		audit              access.MutationAuditor
		effects            realtime.Publisher
		mfa                *mfaMechanics
		personalTokens     store.PersonalAccessTokenStore
		newCredential      func() string
	}
	valid := dependencies{
		users: persistence.User(), passwords: persistence.PasswordCredential(),
		sessions: persistence.Session(), sessionCredentials: persistence.SessionCredential(),
		attempts: mustAuthenticationAttemptAccounting(t, cache),
		audit:    &mutationAttemptAuditorFake{},
		effects:  &realtimetest.Publisher{},
		mfa:      mustTestMFAMechanics(t), personalTokens: discardPersonalAccessTokenStore{},
		newCredential: model.NewCredentialToken,
	}
	dpop := testAuthenticationDPoP(t, cache)
	construct := func(deps dependencies) error {
		service := &Service{users: deps.users, passwords: deps.passwords, sessions: deps.sessions, sessionCredentials: deps.sessionCredentials,
			desktopRegistrations: authenticationDesktopRegistrationLookupFake{}, personalAccessTokens: deps.personalTokens,
			accessPolicy: allowAllAuthenticationAccessPolicy(), dpop: dpop, cache: cache, attempts: deps.attempts, hasher: hasher,
			auditor: deps.audit, securityAudit: &securityAuditorFake{}, publisher: deps.effects, mfaCredentials: &mfaApplicationStoreFake{}, mfa: deps.mfa,
			diagnostics: newDiscardLogger(t), newCredential: deps.newCredential, now: time.Now}
		return service.requireSessionAuthentication()
	}
	require.NoError(t, construct(valid), "complete dependencies were refused")
	tests := []struct {
		name   string
		mutate func(*dependencies)
	}{
		{name: "users", mutate: func(deps *dependencies) { deps.users = nil }},
		{name: "passwords", mutate: func(deps *dependencies) { deps.passwords = nil }},
		{name: "sessions", mutate: func(deps *dependencies) { deps.sessions = nil }},
		{name: "session credentials", mutate: func(deps *dependencies) { deps.sessionCredentials = nil }},
		{name: "attempt accounting", mutate: func(deps *dependencies) { deps.attempts = nil }},
		{name: "audit", mutate: func(deps *dependencies) { deps.audit = nil }},
		{name: "effects", mutate: func(deps *dependencies) { deps.effects = nil }},
		{name: "MFA mechanics", mutate: func(deps *dependencies) { deps.mfa = nil }},
		{name: "personal access token store", mutate: func(deps *dependencies) { deps.personalTokens = nil }},
		{name: "credential generator", mutate: func(deps *dependencies) { deps.newCredential = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			require.Error(t, construct(candidate), "nil %s dependency was accepted", test.name)
		})
	}
	// Composition without an Auditor still builds, as it did when local login
	// audit was an adapter over a nil Auditor; every admitted local login then
	// fails closed instead of signing in unaudited.
	t.Run("login audit", func(t *testing.T) {
		service := newTestAuthenticationService(t, persistence)
		service.securityAudit = nil
		user, createErr := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
			User: &model.User{Username: "unaudited", Email: "unaudited@example.edu"}, Password: "CorrectHorseBatteryStaple1!",
		})
		require.NoError(t, createErr)
		login, loginErr := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
			LoginID: user.Username, Password: "CorrectHorseBatteryStaple1!", ClientType: model.SessionClientCLI, Source: "192.0.2.1",
		})
		require.Nil(t, login, "nil login audit admitted a sign-in")
		require.True(t, apperror.Is(loginErr, "audit.unavailable"), "nil login audit = %v", loginErr)
	})
}

func TestExternalAuthenticationServiceRequiresInvalidator(t *testing.T) {
	t.Parallel()

	persistence := newAuthenticationStoreFake()
	authentication := newTestAuthenticationService(t, persistence)
	audit, auditErr := access.NewAuditor(access.AuditorDependencies{Audits: securityAuditStore{}, Institutions: securityInstitutionStore{}, NodeID: model.NewID()})
	require.NoError(t, auditErr)
	service := authentication
	service.externalProviders, service.externalLoginStates = securityEffectsProviderSourceFake{}, securityExternalLoginStateStore{}
	service.institutions, service.externalIdentities, service.sessions = securityInstitutionStore{}, securityExternalIdentityStore{}, persistence.Session()
	service.accessPolicy, service.attempts = allowAllAuthenticationAccessPolicy(), mustAuthenticationAttemptAccounting(t, newAuthenticationCacheFake())
	service.invalidator, service.securityAudit, service.auditor = nil, audit, audit.MutationAudit()
	service.capabilities, service.invitations = &accessPolicyCapabilitiesFake{}, &externalInvitationAcceptorFake{}
	service.externalAuthenticationPolicy, service.recentAuthenticationTTL = ExternalAuthenticationPolicy{}, 15*time.Minute
	service.diagnostics, service.newCredential, service.now = newDiscardLogger(t), model.NewCredentialToken, nil
	err := service.requireExternalAuthentication()
	require.Error(t, err, "nil authentication invalidator was accepted")
}

func TestAuditServiceRequiresDependencies(t *testing.T) {
	t.Parallel()

	_, err := access.NewAuditor(access.AuditorDependencies{Audits: nil, Institutions: securityInstitutionStore{}, NodeID: model.NewID()})
	require.Error(t, err, "nil audit store was accepted")
	_, err = access.NewAuditor(access.AuditorDependencies{Audits: securityAuditStore{}, Institutions: nil, NodeID: model.NewID()})
	require.Error(t, err, "nil audit institution store was accepted")
	_, err = access.NewAuditor(access.AuditorDependencies{Audits: securityAuditStore{}, Institutions: securityInstitutionStore{}, NodeID: ""})
	require.Error(t, err, "empty audit node ID was accepted")
}

type securityExternalLoginStateStore struct{ store.ExternalLoginStateStore }
type securityInstitutionStore struct{ store.InstitutionStore }
type securityExternalIdentityStore struct{ store.ExternalIdentityStore }
type securityAuditStore struct{ store.AuditStore }

// TestAuthenticationRetainsOnlyClockAndCredentialFunctions keeps sibling
// callbacks out of the Service. The realtime half of this rule, and
// the guarantee that authentication cannot hold the realtime service, now
// follow from the package boundary and live in the parent app package.
func TestAuthenticationRetainsOnlyClockAndCredentialFunctions(t *testing.T) {
	t.Parallel()

	authenticationType := reflect.TypeOf(Service{})
	for index := 0; index < authenticationType.NumField(); index++ {
		field := authenticationType.Field(index)
		// now and wait are clock primitives, not sibling callbacks;
		// mfaRekeyWake is the Job engine wake composition binds, which the MFA
		// key ring use cases held before they moved onto the Service.
		if field.Type.Kind() == reflect.Func && field.Name != "now" && field.Name != "wait" && field.Name != "mfaRekeyWake" {
			require.Equal(t, "newCredential", field.Name)
		}
	}
}

func TestAuthenticationCacheInvalidatorOwnsKeysAndSafeDiagnostics(t *testing.T) {
	t.Parallel()

	cache := &securityEffectsCacheFake{
		authenticationCacheFake: newAuthenticationCacheFake(),
		deleteErrors: map[string]error{
			authenticationCachePrefix + "failed-hash": errors.New("delete failed"),
		},
	}
	diagnostics, logs := newTestLogger(t)
	invalidator, err := NewCacheInvalidator(CacheInvalidatorDependencies{Cache: cache, Diagnostics: diagnostics})
	require.NoError(t, err)

	invalidator.InvalidateAccessCredentials(
		context.Background(),
		[]string{"first-hash", "failed-hash"},
	)
	invalidator.InvalidateSessionActivity(
		context.Background(),
		[]model.SessionID{"session-one", "session-two"},
	)

	wantDeletes := []string{
		authenticationCachePrefix + "first-hash",
		authenticationCachePrefix + "failed-hash",
		activityCachePrefix + "session-one",
		activityCachePrefix + "session-two",
	}
	require.Equal(t, wantDeletes, cache.deleted, "deleted keys")
	records := loggedRecords(t, logs)
	require.Equal(t, []string{"authentication cache delete failed"}, loggedMessages(records), "diagnostics")
	require.Equal(t, []string{"cache operation failed"}, loggedErrors(records), "diagnostic errors")
}

type securityEffectsCacheFake struct {
	*authenticationCacheFake
	deleted      []string
	deleteErrors map[string]error
}

type securityEffectsProviderSourceFake struct{}

func (securityEffectsProviderSourceFake) Descriptors() []model.ExternalAuthenticationProvider {
	return nil
}

func (securityEffectsProviderSourceFake) Provider(string) (ExternalIdentityProvider, bool) {
	return nil, false
}

func (c *securityEffectsCacheFake) Delete(_ context.Context, key string) error {
	c.deleted = append(c.deleted, key)
	return c.deleteErrors[key]
}

// newTestLogger returns a real logger writing JSON records, one per line, to a
// buffer the test may read through loggedRecords.
func newTestLogger(tb testing.TB) (*logging.Logger, *logging.Buffer) {
	tb.Helper()
	var logs logging.Buffer
	logger, err := logging.New()
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = logger.Shutdown() })
	require.NoError(tb, logger.Configure(logging.Config{
		MaxFieldBytes: 1024,
		Targets: []logging.Target{{
			Name: "test", Type: "console", Level: "debug", Format: "json", Writer: &logs,
		}},
	}))
	return logger, &logs
}

// newDiscardLogger returns a real logger whose records the test never reads.
func newDiscardLogger(tb testing.TB) *logging.Logger {
	tb.Helper()
	logger, _ := newTestLogger(tb)
	return logger
}

// loggedRecord is one record a test logger wrote.
type loggedRecord struct {
	Level   string `json:"level"`
	Message string `json:"msg"`
	Error   string `json:"error"`
}

// loggedRecords decodes every record the buffer holds, in order.
func loggedRecords(tb testing.TB, logs *logging.Buffer) []loggedRecord {
	tb.Helper()
	var records []loggedRecord
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record loggedRecord
		require.NoError(tb, json.Unmarshal([]byte(line), &record))
		records = append(records, record)
	}
	return records
}

// loggedMessages lists the message of each record, in order.
func loggedMessages(records []loggedRecord) []string {
	messages := []string{}
	for _, record := range records {
		messages = append(messages, record.Message)
	}
	return messages
}

// loggedErrors lists the error text of each record, in order.
func loggedErrors(records []loggedRecord) []string {
	texts := []string{}
	for _, record := range records {
		texts = append(texts, record.Error)
	}
	return texts
}

// revocationsIn returns a recording Publisher that also places each
// SessionsRevoked call in the log a test shares with its other fakes, under
// the name that test's ordering contract uses.
func revocationsIn(events *[]string, name string) *realtimetest.Publisher {
	return &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		if call.Method == realtimetest.SessionsRevoked {
			*events = append(*events, name)
		}
	}}
}

// publishedMethods lists the method of every call a Publisher recorded, in
// order, for a test whose flow generates the access-token hashes it revokes.
func publishedMethods(publisher *realtimetest.Publisher) []realtimetest.Method {
	methods := []realtimetest.Method{}
	for _, call := range publisher.Calls() {
		methods = append(methods, call.Method)
	}
	return methods
}
