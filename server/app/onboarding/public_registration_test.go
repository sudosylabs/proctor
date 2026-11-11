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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestPublicRegistrationPreparesOneAtomicUnverifiedLocalAccount(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	persistence := &publicRegistrationStoreFake{}
	mail := &mailFake{}
	service := newTestPublicRegistrationService(t, persistence, mail, at)
	rawToken := model.NewCredentialToken()
	service.newToken = func() string { return rawToken }

	err := service.RegisterLocalUser(context.Background(), invocation.New(model.Principal{}, model.RequestMetadata{
		RequestID: "register-1", IPAddress: "192.0.2.10",
	}), RegisterLocalUserCommand{
		Username: "New.Student", Email: " New.Student@Example.EDU ",
		FirstName: "  New  ", LastName: " Student ", Password: "correct horse battery staple",
		Source: "192.0.2.10:443",
	})
	require.NoError(t, err)
	input := persistence.input
	require.NotNil(t, input)
	require.NotNil(t, input.User)
	require.NotNil(t, input.PasswordCredential)
	require.NotNil(t, input.Settings)
	require.NotNil(t, input.DefaultProfilePictureJob)
	require.NotNil(t, input.VerificationToken)
	require.NotNil(t, input.VerificationOccurrence)
	require.NotNil(t, input.VerificationDelivery)
	require.NotNil(t, input.VerificationJob)
	require.NotNil(t, input.AuditEvent)
	require.Equal(t, "new.student", input.User.Username)
	require.Equal(t, "new.student@example.edu", input.User.Email)
	require.Equal(t, "New", input.User.FirstName)
	require.Equal(t, "Student", input.User.LastName)
	require.Empty(t, input.User.DisplayName)
	require.False(t, input.User.EmailVerified)
	require.Equal(t, input.User.ID, input.PasswordCredential.UserID)
	require.Equal(t, input.User.ID, input.Settings.UserID)
	require.Equal(t, input.User.ID, input.VerificationToken.UserID)
	require.Equal(t, input.User.Email, input.VerificationToken.Target)
	require.Equal(t, model.UserTokenEmailVerification, input.VerificationToken.Purpose)
	require.Equal(t, input.User.ID, input.VerificationDelivery.TargetUserID)
	require.Equal(t, model.MailTemplateIdentityVerifyEmail, input.VerificationDelivery.TemplateKey)
	require.Equal(t, model.JobTypeMailDeliverCredential, input.VerificationJob.Type)
	require.False(t, input.AuditEvent.ActorID.IsValid())
	require.Equal(t, string(model.ActionAuthenticationPublicRegistration), input.AuditEvent.Action)
	require.NotContains(t, string(input.AuditEvent.Parameters), input.User.Email)
	require.NotContains(t, string(input.AuditEvent.Parameters), "correct horse")
	require.NotEmpty(t, mail.actionURL)
	require.Contains(t, mail.actionURL, rawToken)
	require.NotContains(t, mail.actionURL, input.VerificationToken.TokenHash)
	require.NotEqual(t, rawToken, input.VerificationToken.TokenHash)
	require.NotContains(t, string(input.AuditEvent.Parameters), rawToken)
	require.NotContains(t, string(input.AuditEvent.Result), rawToken)
}

func TestPublicRegistrationFailsClosedBeforeAccountPreparationWhenPolicyOrMailDisallowsIt(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		policy   accessPolicyStoreFake
		mail     bool
		wantCode string
	}{
		{name: "public registration disabled", policy: accessPolicyStoreFake{local: true}, mail: true, wantCode: "authentication.registration.invitation_required"},
		{name: "local login disabled", policy: accessPolicyStoreFake{}, mail: true, wantCode: "authentication.registration.invitation_required"},
		// The Access Policy cannot enable public registration without local
		// login; a Store answering such a policy fails closed.
		{name: "registration without local login", policy: accessPolicyStoreFake{registration: true}, mail: true, wantCode: "authentication.registration.unavailable"},
		{name: "mail disabled", policy: accessPolicyStoreFake{registration: true, local: true}, wantCode: "authentication.registration.unavailable"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			persistence := &publicRegistrationStoreFake{}
			mail := &mailFake{disabled: !test.mail}
			service := newTestPublicRegistrationService(t, persistence, mail, time.Now())
			service.accessPolicies = test.policy
			err := service.RegisterLocalUser(context.Background(), invocation.Invocation{}, RegisterLocalUserCommand{
				Username: "student", Email: "student@example.edu", FirstName: "New", LastName: "Student",
				Password: "correct horse battery staple", Source: "192.0.2.20",
			})
			require.True(t, apperror.Is(err, test.wantCode), "Register() = %v, want %s", err, test.wantCode)
			require.Nil(t, persistence.input, "disabled registration reached preparation: input")
			require.Equal(t, 0, mail.prepareCalls, "disabled registration reached preparation: input")
		})
	}
}

func TestPublicRegistrationConcealsDuplicateIdentity(t *testing.T) {
	t.Parallel()

	for _, constraint := range []string{"users_username_key", "users_email_key"} {
		constraint := constraint
		t.Run(constraint, func(t *testing.T) {
			t.Parallel()
			persistence := &publicRegistrationStoreFake{err: store.NewErrConflict("user", constraint, nil)}
			service := newTestPublicRegistrationService(t, persistence, &mailFake{}, time.Now())
			require.NoError(t, service.RegisterLocalUser(context.Background(), invocation.Invocation{}, RegisterLocalUserCommand{
				Username: "student", Email: "student@example.edu", FirstName: "New", LastName: "Student",
				Password: "correct horse battery staple", Source: "192.0.2.30",
			}))
		})
	}
}

func TestPublicRegistrationDoesNotConcealUnrelatedPersistenceConflicts(t *testing.T) {
	t.Parallel()

	for _, conflict := range []*store.ErrConflict{
		store.NewErrConflict("job", "jobs_pkey", nil),
		store.NewErrConflict("user_token", "user_tokens_pkey", nil),
		store.NewErrConflict("mail_delivery", "mail_deliveries_pkey", nil),
		store.NewErrConflict("user", "users_pkey", nil),
	} {
		conflict := conflict
		t.Run(conflict.Resource+"/"+conflict.Constraint, func(t *testing.T) {
			t.Parallel()
			persistence := &publicRegistrationStoreFake{err: conflict}
			service := newTestPublicRegistrationService(t, persistence, &mailFake{}, time.Now())
			err := service.RegisterLocalUser(context.Background(), invocation.Invocation{}, RegisterLocalUserCommand{
				Username: "student", Email: "student@example.edu", FirstName: "New", LastName: "Student",
				Password: "correct horse battery staple", Source: "192.0.2.30",
			})
			require.True(t, apperror.Is(err, "authentication.registration.unavailable"), "unrelated persistence conflict = %v, want authentication.registration.unavailable", err)
		})
	}
}

func TestPublicRegistrationMailAndPersistenceFailuresCreateNoPartialApplicationState(t *testing.T) {
	t.Parallel()

	mailFailureStore := &publicRegistrationStoreFake{}
	mailFailure := newTestPublicRegistrationService(t, mailFailureStore, &mailFake{
		registrationErr: errors.New("seal failed"),
	}, time.Now())
	command := RegisterLocalUserCommand{
		Username: "student", Email: "student@example.edu", FirstName: "New", LastName: "Student",
		Password: "correct horse battery staple", Source: "192.0.2.31",
	}
	err := mailFailure.RegisterLocalUser(context.Background(), invocation.Invocation{}, command)
	require.True(t, apperror.Is(err, "authentication.registration.unavailable"), "mail preparation failure = %v", err)
	require.Nil(t, mailFailureStore.input)

	persistenceFailure := newTestPublicRegistrationService(t, &publicRegistrationStoreFake{err: errors.New("database unavailable")}, &mailFake{}, time.Now())
	err = persistenceFailure.RegisterLocalUser(context.Background(), invocation.Invocation{}, command)
	require.True(t, apperror.Is(err, "authentication.registration.unavailable"), "persistence failure = %v", err)
}

func TestPublicRegistrationPasswordWorkFailuresStopBeforeMailAndPersistence(t *testing.T) {
	testPasswordHashFailures(t, "authentication.registration.unavailable", func(t *testing.T, ctx context.Context, hasher testPasswordHasher) error {
		persistence := &publicRegistrationStoreFake{}
		mail := &mailFake{}
		service := newTestPublicRegistrationService(t, persistence, mail, time.Now())
		service.hasher = hasher
		err := service.RegisterLocalUser(ctx, invocation.Invocation{}, RegisterLocalUserCommand{
			Username: "student", Email: "student@example.edu", FirstName: "New", LastName: "Student",
			Password: "correct horse battery staple", Source: "192.0.2.33",
		})
		require.Nil(t, persistence.input, "failed password work reached registration mail or persistence")
		require.Equal(t, 0, mail.prepareCalls, "failed password work reached registration mail or persistence")
		return err
	})
}

func TestPublicRegistrationRejectsAnInvalidPasswordBeforeMailOrPersistence(t *testing.T) {
	t.Parallel()

	persistence := &publicRegistrationStoreFake{}
	mail := &mailFake{}
	service := newTestPublicRegistrationService(t, persistence, mail, time.Now())
	err := service.RegisterLocalUser(context.Background(), invocation.Invocation{}, RegisterLocalUserCommand{
		Username: "student", Email: "student@example.edu", FirstName: "New", LastName: "Student",
		Password: "", Source: "192.0.2.32",
	})
	require.True(t, apperror.Is(err, "authentication.password.invalid"), "invalid password = %v", err)
	require.Nil(t, persistence.input, "invalid password reached mail/Store: calls")
	require.Equal(t, 0, mail.prepareCalls, "invalid password reached mail/Store: calls")
}

func TestPublicRegistrationRequiresFirstAndLastNamesBeforeCredentialPreparation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		firstName string
		lastName  string
	}{
		{name: "missing first name", lastName: "Student"},
		{name: "missing last name", firstName: "New"},
		{name: "whitespace names", firstName: "  ", lastName: "\t"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			persistence := &publicRegistrationStoreFake{}
			mail := &mailFake{}
			service := newTestPublicRegistrationService(t, persistence, mail, time.Now())
			err := service.RegisterLocalUser(context.Background(), invocation.Invocation{}, RegisterLocalUserCommand{
				Username: "student", Email: "student@example.edu", FirstName: test.firstName,
				LastName: test.lastName, Password: "correct horse battery staple", Source: "192.0.2.33",
			})
			require.True(t, apperror.Is(err, "authentication.registration.invalid"), "missing personal name = %v", err)
			require.Nil(t, persistence.input, "invalid personal name reached mail/Store: calls")
			require.Equal(t, 0, mail.prepareCalls, "invalid personal name reached mail/Store: calls")
		})
	}
}

func TestPublicRegistrationUsesPrivateSharedAttemptAccounting(t *testing.T) {
	t.Parallel()

	cache := newExpiringAuthenticationAttemptCache(time.Now)
	service := newTestPublicRegistrationService(t, &publicRegistrationStoreFake{}, &mailFake{}, time.Now())
	service.attempts = mustAuthenticationAttemptAccounting(t, cache)
	service.rateLimit = identity.LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 1, MaximumSourceAttempts: 10}
	command := RegisterLocalUserCommand{Username: "student", Email: " Student@Example.EDU ", FirstName: "New", LastName: "Student", Password: "correct horse battery staple", Source: "192.0.2.40:443"}
	require.NoError(t, service.RegisterLocalUser(context.Background(), invocation.Invocation{}, command))
	command.Email = "student@example.edu"
	err := service.RegisterLocalUser(context.Background(), invocation.Invocation{}, command)
	require.True(t, apperror.Is(err, "authentication.rate_limited"), "second registration = %v", err)
	for key := range cache.snapshot() {
		require.True(t, strings.HasPrefix(key, "authentication/attempts/public-registration/"))
		require.NotContains(t, strings.ToLower(key), "student")
		require.NotContains(t, key, "192.0.2.40")
	}
}

type publicRegistrationStoreFake struct {
	store.UserStore
	input *store.PublicLocalUserRegistration
	err   error
}

func (s *publicRegistrationStoreFake) RegisterLocal(_ context.Context, input *store.PublicLocalUserRegistration) (*store.PublicLocalUserRegistrationResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	return &store.PublicLocalUserRegistrationResult{User: input.User, Token: input.VerificationToken}, nil
}

// accessPolicyStoreFake answers the current Access Policy with the public
// registration and local login settings a test chooses.
type accessPolicyStoreFake struct {
	store.AccessPolicyStore
	registration bool
	local        bool
	err          error
}

func (p accessPolicyStoreFake) Get(context.Context, int) (*store.AccessPolicySnapshot, error) {
	if p.err != nil {
		return nil, p.err
	}
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.UnixMilli(100))
	policy.PublicRegistrationEnabled, policy.LocalLoginEnabled = p.registration, p.local
	// Invitation local credentials need local login, so they go with it.
	policy.InvitationLocalCredentialEnabled = policy.InvitationLocalCredentialEnabled && p.local
	return &store.AccessPolicySnapshot{Policy: policy}, nil
}

func (f *mailFake) PreparePublicRegistrationVerification(recipient *model.User, occurrenceID model.MailOccurrenceID, actionURL string, at, deadline time.Time) (*store.PreparedMail, error) {
	f.prepareCalls++
	f.actionURL = actionURL
	if f.registrationErr != nil {
		return nil, f.registrationErr
	}
	if f.disabled {
		return nil, errors.New("mail disabled")
	}
	occurrence := &model.MailOccurrence{ID: occurrenceID, Kind: model.MailOccurrenceAccountToken, TemplateKey: model.MailTemplateIdentityVerifyEmail,
		ActorUserID: recipient.ID, CreatedAt: at}
	deliveryID, jobID := model.NewMailDeliveryID(), model.NewJobID()
	delivery := &model.MailDelivery{ID: deliveryID, OccurrenceID: occurrence.ID, JobID: jobID,
		TargetUserID: recipient.ID, TemplateKey: model.MailTemplateIdentityVerifyEmail, State: model.MailDeliveryQueued,
		CreatedAt: at, UpdatedAt: at, MessageDate: at, Deadline: deadline, Revision: 1}
	job := &model.Job{ID: jobID, Type: model.JobTypeMailDeliverCredential, Status: model.JobStatusQueued}
	return &store.PreparedMail{Occurrence: occurrence, Delivery: delivery, Job: job}, nil
}

func newTestPublicRegistrationService(t *testing.T, persistence store.UserStore, mail mailComposer, at time.Time) *Service {
	t.Helper()
	return &Service{
		users:                persistence,
		accessPolicies:       accessPolicyStoreFake{registration: true, local: true},
		institutions:         institutionStoreFake{institution: &model.Institution{ID: model.NewInstitutionID()}},
		mail:                 mail,
		attempts:             mustAuthenticationAttemptAccounting(t, newExpiringAuthenticationAttemptCache(time.Now)),
		hasher:               passwordHasherFake{},
		rateLimit:            identity.LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 10, MaximumSourceAttempts: 100},
		emailVerificationTTL: time.Hour,
		publicURL:            "https://proctor.example.test",
		nodeID:               "registration-test-node",
		newToken:             model.NewCredentialToken,
		now:                  func() time.Time { return at },
	}
}
