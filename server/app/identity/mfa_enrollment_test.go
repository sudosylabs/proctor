// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

func TestMFASetupUsesControlledClockAndSecretGenerator(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 10, 20, 0, 0, time.UTC)
	principal := mfaTestPrincipal(now, model.AuthenticationSingleFactor)
	events := []string{}
	persistence := &mfaApplicationStoreFake{events: &events}
	audit := &securityAuditorFake{trace: &events}
	service := newTestMFAApplicationService(
		t, persistence, audit, &realtimetest.Publisher{}, now,
	)
	const secret = "JBSWY3DPEHPK3PXP" // #nosec G101 -- Public synthetic TOTP seed.
	service.mfa.newSecret = func() (string, error) { return secret, nil }
	application := service

	setup, err := application.SetupMFA(
		context.Background(),
		invocation.New(principal, model.RequestMetadata{}),
		SetupMFACommand{},
	)
	require.NoError(t, err)
	require.Equal(t, secret, setup.Secret)
	require.Equal(t, now.Add(10*time.Minute), setup.ExpiresAt)
	require.NotNil(t, persistence.savedPending)
	require.Equal(t, now, persistence.savedPending.CreatedAt)
	require.Equal(t, now.Add(10*time.Minute), persistence.savedPending.PendingExpiresAt.Time)
	require.NotEqual(t, secret, persistence.savedPending.EncryptedSecret)
	// Contract (guardrail 8): the audit begins before the durable write, and the
	// audit outcome follows the write.
	require.Equal(t, []string{"institution", "audit_begin", "save_pending", "audit_success"}, events)
}

func TestMFASecurityTransitionsPrepareOneOrdinaryNoticeWithoutSecrets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	principal := mfaTestPrincipal(now, model.AuthenticationMultiFactor)
	persistence := &mfaApplicationStoreFake{
		session: &model.Session{ID: principal.SessionID, UserID: principal.UserID},
	}
	mailer := &mfaSecurityNoticeMailPreparerFake{}
	service := newTestMFAApplicationService(t, persistence, &securityAuditorFake{}, &realtimetest.Publisher{}, now)
	service.mail = mailer
	application := service
	inv := invocation.New(principal, model.RequestMetadata{RequestID: "mfa-notices"})
	const secret = "JBSWY3DPEHPK3PXP" // #nosec G101 -- Public synthetic TOTP seed.
	sealed, err := service.mfa.sealTOTPSecret(principal.UserID.String(), secret)
	require.NoError(t, err)
	persistence.credential = &model.MFACredential{
		ID: model.NewMFACredentialID(), UserID: principal.UserID,
		State: model.MFAStatePending, EncryptedSecret: sealed.encoded,
		EncryptionKeyID: sealed.keyID, CreatedAt: now.Add(-time.Minute),
		UpdatedAt: now.Add(-time.Minute), PendingExpiresAt: model.OptionalTimeFrom(now.Add(time.Minute)),
	}
	code, err := computeTOTP(secret, now.Unix()/30)
	require.NoError(t, err)
	_, err = application.ActivateMFA(context.Background(), inv, ActivateMFACommand{Code: code})
	require.NoError(t, err)

	_, err = application.RegenerateMFARecoveryCodes(context.Background(), inv)
	require.NoError(t, err)
	require.NoError(t, application.DisableMFA(context.Background(), inv))
	require.Len(t, mailer.requests, 3, "mail preparations")
	for index, want := range []model.MailTemplateKey{
		model.MailTemplateIdentityMFAEnabled,
		model.MailTemplateIdentityMFARecoveryCodesRegenerated,
		model.MailTemplateIdentityMFADisabled,
	} {
		request := mailer.requests[index]
		require.Equal(t, want, request.TemplateKey, "mail request %d = %#v", index, request)
		require.Equal(t, principal.UserID, request.Recipient.ID, "mail request %d = %#v", index, request)
		require.True(t, request.At.Equal(now), "mail request %d = %#v", index, request)
	}
}

func TestMFAApplicationServiceRequiresFocusedDependencies(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name   string
		mutate func(*Service)
	}{
		{"user store", func(s *Service) { s.users = nil }},
		{"password credential reader", func(s *Service) { s.passwords = nil }},
		{"MFA store", func(s *Service) { s.mfaCredentials = nil }},
		{"session store", func(s *Service) { s.sessions = nil }},
		{"institution store", func(s *Service) { s.institutions = nil }},
		{"effects", func(s *Service) { s.publisher = nil }},
		{"mail", func(s *Service) { s.mail = nil }},
		{"mechanics", func(s *Service) { s.mfa = nil }},
		{"role holder MFA policy", func(s *Service) { s.accessPolicy = nil }},
		{"recent authentication TTL", func(s *Service) { s.recentAuthenticationTTL = 0 }},
		{"clock", func(s *Service) { s.now = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := newTestMFAApplicationService(t, &mfaApplicationStoreFake{}, &securityAuditorFake{}, &realtimetest.Publisher{}, now)
			test.mutate(service)
			require.Error(t, service.requireMFA(), "nil dependency was accepted")
		})
	}
	// Composition without an Auditor still builds, as it did when the MFA
	// audit was an adapter over a nil Auditor; every audited MFA mutation then
	// fails closed before it writes.
	t.Run("audit", func(t *testing.T) {
		t.Parallel()
		persistence := &mfaApplicationStoreFake{}
		service := newTestMFAApplicationService(t, persistence, &securityAuditorFake{}, &realtimetest.Publisher{}, now)
		service.securityAudit = nil
		require.NoError(t, service.requireMFA())
		_, err := service.SetupMFA(context.Background(), invocation.New(mfaTestPrincipal(now, model.AuthenticationSingleFactor), model.RequestMetadata{}), SetupMFACommand{})
		require.True(t, apperror.Is(err, "audit.unavailable"), "unaudited setup = %v", err)
		require.Nil(t, persistence.savedPending, "unaudited setup reached durable mutation")
	})
}

func newTestMFAApplicationService(
	t *testing.T,
	persistence *mfaApplicationStoreFake,
	audit *securityAuditorFake,
	effects *realtimetest.Publisher,
	now time.Time,
) *Service {
	t.Helper()
	service := &Service{
		users: mfaApplicationUserStoreFake{}, mfaCredentials: persistence, passwords: mfaPasswordCredentialsFake{},
		sessions: mfaApplicationSessionStoreFake{persistence: persistence}, institutions: mfaApplicationInstitutionStoreFake{persistence: persistence},
		securityAudit: audit, publisher: effects, mail: &mfaSecurityNoticeMailPreparerFake{}, mfa: mustTestMFAMechanics(t),
		authorizer: mfaResetAuthorizationFake{}, capabilities: &accessPolicyCapabilitiesFake{},
		attempts:       &AttemptAccounting{deps: AttemptAccountingDependencies{Cache: newAuthenticationCacheFake()}},
		loginRateLimit: LoginRateLimitPolicy{MaximumAttempts: 100, MaximumSourceAttempts: 1000, Window: time.Minute},
		accessPolicy:   allowAllAuthenticationAccessPolicy(), recentAuthenticationTTL: 15 * time.Minute,
		now: func() time.Time { return now },
	}
	require.NoError(t, service.requireMFA())
	return service
}

func mustTestMFAMechanics(t *testing.T) *mfaMechanics {
	t.Helper()
	policy := MFAPolicy{
		Enabled: true, Issuer: "Proctor", SetupTTL: 10 * time.Minute,
		RecoveryCodeCount: 5,
	}
	sealer, err := secretseal.New(secretseal.Settings{
		EncryptionKey:    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
		MaximumPlaintext: 128,
	})
	require.NoError(t, err)
	mechanics, err := newMFAMechanics(mfaMechanicsDependencies{Settings: policy, Sealer: sealer})
	require.NoError(t, err)
	return mechanics
}

func mfaTestPrincipal(at time.Time, strength model.AuthenticationStrength) model.Principal {
	principal := selfSessionPrincipal(at.Add(-time.Minute))
	principal.AuthenticationStrength = strength
	if strength == model.AuthenticationMultiFactor {
		principal.MFACompletedAt = model.OptionalTimeFrom(at.Add(-30 * time.Second))
	}
	return principal
}

type mfaApplicationStoreFake struct {
	store.MFAStore

	mu             sync.Mutex
	events         *[]string
	credential     *model.MFACredential
	savedPending   *model.MFACredential
	session        *model.Session
	hashes         []string
	consumed       bool
	upgradeCalls   int
	upgradedAt     time.Time
	credentialGets int
	activation     *store.MFAActivationMutation
	regeneration   *store.MFARecoveryCodesRegeneration
	disablement    *store.MFADisablement
	recoveryCodes  *store.MFARecoveryCodeSummary
	// consumptions are the sign-in factor consumptions received, in order.
	consumptions []*store.MFASecondFactorConsumption
	// afterCredentialGet runs after each credential read, outside the lock.
	afterCredentialGet func()
	// recoveryReads counts recovery-state reads; recoveryErr fails them and
	// recoveryGeneration is the generation they report.
	recoveryReads      int
	recoveryErr        error
	recoveryGeneration int64
}

func (s *mfaApplicationStoreFake) appendEvent(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

func (s *mfaApplicationStoreFake) GetByUser(context.Context, model.UserID) (*model.MFACredential, error) {
	s.mu.Lock()
	s.credentialGets++
	s.appendEvent("credential_get")
	credential, after := s.credential, s.afterCredentialGet
	s.mu.Unlock()
	if after != nil {
		after()
	}
	if credential == nil {
		return nil, store.NewErrNotFound("mfa_credential", "")
	}
	return credential, nil
}

func (s *mfaApplicationStoreFake) savePendingCredential(candidate *model.MFACredential) (*model.MFACredential, error) {
	s.appendEvent("save_pending")
	saved := *candidate
	saved.PrepareCreate(model.NewMFACredentialID(), candidate.CreatedAt)
	s.savedPending = &saved
	return &saved, nil
}

func (s *mfaApplicationStoreFake) ConsumeSecondFactor(ctx context.Context, input *store.MFASecondFactorConsumption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendEvent("consume")
	s.consumptions = append(s.consumptions, input)
	if s.consumed {
		return store.NewErrNotFound("mfa_recovery_code", "")
	}
	s.consumed = true
	return nil
}

func (s *mfaApplicationStoreFake) UpgradeSession(_ context.Context, input *store.MFASessionUpgrade) ([]string, error) {
	at := input.Now
	s.upgradeCalls++
	s.upgradedAt = at
	s.appendEvent("upgrade")
	return append([]string(nil), s.hashes...), nil
}

func (s *mfaApplicationStoreFake) Activate(_ context.Context, input *store.MFAActivationMutation) (*store.MFAActivationResult, error) {
	s.activation = input
	active := *s.credential
	active.State = model.MFAStateActive
	return &store.MFAActivationResult{Credential: &active, Session: s.session, AccessTokenHashes: append([]string(nil), s.hashes...)}, nil
}

func (s *mfaApplicationStoreFake) ReplaceRecoveryCodes(_ context.Context, input *store.MFARecoveryCodesRegeneration) error {
	s.regeneration = input
	return nil
}

func (s *mfaApplicationStoreFake) SummarizeRecoveryCodes(context.Context, model.UserID) (*store.MFARecoveryCodeSummary, error) {
	if s.recoveryCodes == nil {
		return &store.MFARecoveryCodeSummary{}, nil
	}
	return s.recoveryCodes, nil
}

func (s *mfaApplicationStoreFake) Disable(_ context.Context, input *store.MFADisablement) (*store.MFADisableResult, error) {
	s.disablement = input
	return &store.MFADisableResult{AccessTokenHashes: append([]string(nil), s.hashes...)}, nil
}

type mfaApplicationUserStoreFake struct{ store.UserStore }

func (mfaApplicationUserStoreFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	return &model.User{ID: id, Email: "user@example.edu"}, nil
}

type mfaSecurityNoticeMailPreparerFake struct {
	mailComposer
	requests []DirectMailPreparation
}

func (m *mfaSecurityNoticeMailPreparerFake) PrepareMFANotice(request mail.NoticePreparation, kind mail.MFANoticeKind) (*store.PreparedMail, error) {
	key := model.MailTemplateIdentityMFAEnabled
	switch kind {
	case mail.MFANoticeDisabled:
		key = model.MailTemplateIdentityMFADisabled
	case "reset":
		key = model.MailTemplateIdentityMFAReset
	case mail.MFANoticeRecoveryCodesRegenerated:
		key = model.MailTemplateIdentityMFARecoveryCodesRegenerated
	}
	direct := DirectMailPreparation{Recipient: request.Recipient, OccurrenceID: model.NewMailOccurrenceID(),
		Kind: model.MailOccurrenceSecurityNotice, TemplateKey: key, At: request.At,
		Deadline: request.At.Add(24 * time.Hour), JobType: model.JobTypeMailDeliver}
	m.requests = append(m.requests, direct)
	occurrenceID, deliveryID, jobID := model.NewMailOccurrenceID(), model.NewMailDeliveryID(), model.NewJobID()
	return &store.PreparedMail{
		Occurrence: &model.MailOccurrence{ID: occurrenceID, Kind: model.MailOccurrenceSecurityNotice, TemplateKey: key, ActorUserID: request.Recipient.ID, CreatedAt: request.At},
		Delivery:   &model.MailDelivery{ID: deliveryID, OccurrenceID: occurrenceID, JobID: jobID, TargetUserID: request.Recipient.ID, TemplateKey: key, Deadline: request.At.Add(24 * time.Hour)},
		Job:        &model.Job{ID: jobID, Type: model.JobTypeMailDeliver},
	}, nil
}

type mfaApplicationSessionStoreFake struct {
	store.SessionStore
	persistence *mfaApplicationStoreFake
}

func (s mfaApplicationSessionStoreFake) Get(context.Context, model.SessionID) (*model.Session, error) {
	s.persistence.appendEvent("session_get")
	return s.persistence.session, nil
}

type mfaApplicationInstitutionStoreFake struct {
	store.InstitutionStore
	persistence *mfaApplicationStoreFake
}

func (s mfaApplicationInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	s.persistence.appendEvent("institution")
	return &model.Institution{ID: model.NewInstitutionID()}, nil
}

type mfaResetAuthorizationFake struct {
	resourceAuthorizer
	err error
}

func (a mfaResetAuthorizationFake) Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error {
	return a.err
}

func (s *mfaApplicationStoreFake) GetRecoveryState(_ context.Context, userID model.UserID) (*model.UserMFARecovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveryReads++
	if s.recoveryErr != nil {
		return nil, s.recoveryErr
	}
	return &model.UserMFARecovery{UserID: userID, Generation: s.recoveryGeneration}, nil
}

func (s *mfaApplicationStoreFake) SavePending(ctx context.Context, input *store.MFAPendingEnrollment) (*model.MFACredential, error) {
	saved, err := s.savePendingCredential(input.Credential)
	if err == nil {
		s.appendEvent("audit_success")
	}
	return saved, err
}

func (s *mfaApplicationStoreFake) Challenge(ctx context.Context, input *store.MFAChallenge) (*store.SessionReauthenticationResult, error) {
	if err := s.ConsumeSecondFactor(ctx, &store.MFASecondFactorConsumption{UserID: input.Principal.UserID, TimeStep: input.TimeStep, RecoveryCodeHash: input.RecoveryCodeHash, Now: input.VerifiedAt}); err != nil {
		return nil, err
	}
	hashes, err := s.UpgradeSession(ctx, &store.MFASessionUpgrade{SessionID: input.Principal.SessionID, UserID: input.Principal.UserID, Now: input.VerifiedAt})
	if err != nil {
		return nil, err
	}
	s.appendEvent("audit_success")
	return &store.SessionReauthenticationResult{Session: s.session, AccessTokenHashes: hashes}, nil
}

// mfaPasswordCredentialsFake reports an account without a password credential
// unless credential is set.
type mfaPasswordCredentialsFake struct {
	store.PasswordCredentialStore
	credential *model.PasswordCredential
	err        error
}

func (f mfaPasswordCredentialsFake) GetByUser(context.Context, model.UserID) (*model.PasswordCredential, error) {
	if f.err != nil || f.credential != nil {
		return f.credential, f.err
	}
	return nil, store.NewErrNotFound("password_credential", "")
}

func TestMFAStatusReportsPasswordChangeRecoveryTotalAndRecentWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	changedAt := time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)
	principal := mfaTestPrincipal(now, model.AuthenticationSingleFactor) // authenticated one minute ago
	service := newTestMFAApplicationService(t, &mfaApplicationStoreFake{}, &securityAuditorFake{}, &realtimetest.Publisher{}, now)
	service.passwords = mfaPasswordCredentialsFake{credential: &model.PasswordCredential{UserID: principal.UserID, PasswordChangedAt: changedAt}}

	status, err := service.GetMFAStatus(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.True(t, status.PasswordChangedAt.Valid)
	require.True(t, status.PasswordChangedAt.Time.Equal(changedAt))
	require.Zero(t, status.RecoveryCodesTotal, "no credential has no issued set")
	require.Empty(t, status.Method)
	require.Equal(t, 15*time.Minute, status.RecentAuthenticationTTL)
	require.True(t, status.RecentlyAuthenticated)
	require.True(t, status.RecentlyAuthenticatedUntil.Valid)
	require.True(t, status.RecentlyAuthenticatedUntil.Time.Equal(now.Add(14*time.Minute)), "until = %s", status.RecentlyAuthenticatedUntil.Time)

	// A Session past its window has no end time; an account without a
	// password credential has no change time.
	stale := principal
	stale.AuthenticatedAt = now.Add(-time.Hour)
	service.passwords = mfaPasswordCredentialsFake{}
	status, err = service.GetMFAStatus(context.Background(), invocation.New(stale, model.RequestMetadata{}))
	require.NoError(t, err)
	require.False(t, status.RecentlyAuthenticated)
	require.False(t, status.RecentlyAuthenticatedUntil.Valid)
	require.False(t, status.PasswordChangedAt.Valid)
	require.Equal(t, 15*time.Minute, status.RecentAuthenticationTTL)

	service.passwords = mfaPasswordCredentialsFake{err: errors.New("connection reset")}
	_, err = service.GetMFAStatus(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.Error(t, err, "a failed password read was reported as no password")
}

func TestMFAStatusReportsMethodActivationAndIssuedRecoverySet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	activatedAt := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	generatedAt := time.Date(2026, 7, 9, 10, 15, 0, 0, time.UTC)
	principal := mfaTestPrincipal(now, model.AuthenticationMultiFactor)
	persistence := &mfaApplicationStoreFake{
		credential: &model.MFACredential{
			UserID: principal.UserID, State: model.MFAStateActive, ActivatedAt: model.OptionalTimeFrom(activatedAt),
		},
		// Issued is the live set, not the configured count of 5.
		recoveryCodes: &store.MFARecoveryCodeSummary{Remaining: 3, Issued: 8, GeneratedAt: generatedAt},
	}
	service := newTestMFAApplicationService(t, persistence, &securityAuditorFake{}, &realtimetest.Publisher{}, now)

	status, err := service.GetMFAStatus(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.Equal(t, MFAMethodAuthenticatorApp, status.Method)
	require.True(t, status.EnabledAt.Valid)
	require.True(t, status.EnabledAt.Time.Equal(activatedAt), "enabled_at = %s", status.EnabledAt.Time)
	require.Equal(t, 3, status.RecoveryCodesRemaining)
	require.Equal(t, 8, status.RecoveryCodesTotal)
	require.True(t, status.RecoveryCodesGeneratedAt.Valid)
	require.True(t, status.RecoveryCodesGeneratedAt.Time.Equal(generatedAt), "generated_at = %s", status.RecoveryCodesGeneratedAt.Time)

	// A pending enrollment names its method but has no activation time or codes.
	persistence.credential = &model.MFACredential{
		UserID: principal.UserID, State: model.MFAStatePending, PendingExpiresAt: model.OptionalTimeFrom(now.Add(time.Minute)),
	}
	status, err = service.GetMFAStatus(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.True(t, status.Pending)
	require.Equal(t, MFAMethodAuthenticatorApp, status.Method)
	require.False(t, status.EnabledAt.Valid)
	require.Zero(t, status.RecoveryCodesTotal)
	require.False(t, status.RecoveryCodesGeneratedAt.Valid)
}
