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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// accountStateStoreFake stands in for the User store account state reads and
// changes.
type accountStateStoreFake struct {
	store.UserStore
	events *[]string
	user   *model.User
	input  *store.UserDisabledStateChange
	result *store.UserDisabledStateResult
	err    error
}

func (s *accountStateStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	*s.events = append(*s.events, "get-user")
	return s.user, nil
}

func (s *accountStateStoreFake) SetDisabled(_ context.Context, input *store.UserDisabledStateChange) (*store.UserDisabledStateResult, error) {
	*s.events = append(*s.events, "store-set-disabled")
	s.input = input
	return s.result, s.err
}

// accountStatePublisher returns a recording Publisher that also places each
// revocation in the shared log as "publish-revocation". Account state changes
// publish nothing else, so any other call fails the test.
func accountStatePublisher(t *testing.T, events *[]string) *realtimetest.Publisher {
	t.Helper()
	return &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		assert.Equal(t, realtimetest.SessionsRevoked, call.Method, "account state published another effect")
		*events = append(*events, "publish-revocation")
	}}
}

type securityNoticeMailerFake struct {
	mailComposer
	events   *[]string
	requests []DirectMailPreparation
	err      error
}

func (m *securityNoticeMailerFake) prepare(request mail.NoticePreparation, key model.MailTemplateKey) (*store.PreparedMail, error) {
	if m.events != nil {
		*m.events = append(*m.events, "prepare-mail")
	}
	direct := DirectMailPreparation{Recipient: request.Recipient, OccurrenceID: model.NewMailOccurrenceID(),
		Kind: model.MailOccurrenceSecurityNotice, TemplateKey: key, At: request.At,
		Deadline: request.At.Add(24 * time.Hour), JobType: model.JobTypeMailDeliver}
	m.requests = append(m.requests, direct)
	if m.err != nil {
		return nil, m.err
	}
	occurrenceID := model.NewMailOccurrenceID()
	return &store.PreparedMail{
		Occurrence: &model.MailOccurrence{ID: occurrenceID, Kind: model.MailOccurrenceSecurityNotice, TemplateKey: key},
		Delivery:   &model.MailDelivery{ID: model.NewMailDeliveryID(), OccurrenceID: occurrenceID, TemplateKey: key},
		Job:        &model.Job{ID: model.NewJobID(), Type: model.JobTypeMailDeliver},
	}, nil
}

func (m *securityNoticeMailerFake) PrepareAccountStateChanged(request mail.NoticePreparation, enabled bool) (*store.PreparedMail, error) {
	key := model.MailTemplateIdentityAccountDisabled
	if enabled {
		key = model.MailTemplateIdentityAccountEnabled
	}
	return m.prepare(request, key)
}

func (m *securityNoticeMailerFake) PrepareSessionsRevokedByAdministrator(request mail.NoticePreparation) (*store.PreparedMail, error) {
	return m.prepare(request, model.MailTemplateIdentitySessionsRevokedByAdmin)
}

func TestAccountDisableCommitsBeforePublishingRevocation(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Locale: "en", Timezone: "UTC"}
	updated := *user
	updated.DisabledAt = model.OptionalTimeFromMillis(500)
	updated.Revision++
	revoked := &model.Session{ID: model.NewSessionID()}
	persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: &updated, RevokedSessions: []*model.Session{revoked}, RevokedTokenHashes: []string{"hash"}}}
	auditor := &institutionAuditorFake{events: &events, beginID: model.NewID()}
	capabilities := &accessPolicyCapabilitiesFake{snapshot: AccessPolicyCapabilitySnapshot{Providers: []AccessPolicyProviderCapability{{
		Descriptor: model.ExternalAuthenticationProvider{ID: "campus", Type: "oidc"},
	}}}}
	mailer := &securityNoticeMailerFake{events: &events}
	effects := accountStatePublisher(t, &events)
	service := &Service{users: persistence, authorizer: &accountAuthorizerFake{events: &events}, capabilities: capabilities, auditor: auditor, mail: mailer, publisher: effects, now: func() time.Time { return time.UnixMilli(500) }}
	result, err := service.SetUserEnabled(context.Background(), invocation.Invocation{}, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.NoError(t, err)
	require.Equal(t, int64(500), result.DisabledAt.Millis(), "result/input = %#v / %#v", result, persistence.input)
	require.Equal(t, int64(3), persistence.input.ExpectedRevision, "result/input = %#v / %#v", result, persistence.input)
	require.True(t, persistence.input.Disabled, "result/input = %#v / %#v", result, persistence.input)
	require.NotEmpty(t, persistence.input.AuditEventID, "result/input = %#v / %#v", result, persistence.input)
	_, available := persistence.input.Capabilities.Providers["campus"]
	require.True(t, available, "Store capability snapshot = %#v, want configured campus provider", persistence.input.Capabilities)
	require.Len(t, mailer.requests, 1, "mail request/input = %#v / %#v", mailer.requests, persistence.input)
	require.Equal(t, model.MailTemplateIdentityAccountDisabled, mailer.requests[0].TemplateKey, "mail request/input = %#v / %#v", mailer.requests, persistence.input)
	require.NotNil(t, persistence.input.Occurrence, "mail request/input = %#v / %#v", mailer.requests, persistence.input)
	require.NotNil(t, persistence.input.Delivery, "mail request/input = %#v / %#v", mailer.requests, persistence.input)
	require.NotNil(t, persistence.input.DeliveryJob, "mail request/input = %#v / %#v", mailer.requests, persistence.input)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	require.Equal(t, []string{"authorize-manage", "get-user", "prepare-mail", "audit-begin", "store-set-disabled", "publish-revocation"}, events)
	// Contract: one revocation, published for the disabled User, naming each
	// revoked Session and carrying the revoked access-token hashes.
	require.Equal(t, []realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: user.ID,
		SessionIDs: []model.SessionID{revoked.ID}, AccessTokenHashes: []string{"hash"},
	}}, effects.Calls())
}

func TestAccountDisableRecordsTheTrimmedReasonOnlyInAuditParameters(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		reason *string
		want   any
	}{
		"absent":  {},
		"trimmed": {reason: new("  Left the institution\t"), want: "Left the institution"},
		"longest": {reason: new(strings.Repeat("é", MaximumAccountDisableReasonRunes)), want: strings.Repeat("é", MaximumAccountDisableReasonRunes)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Locale: "en", Timezone: "UTC"}
			updated := *user
			updated.DisabledAt = model.OptionalTimeFromMillis(500)
			persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: &updated}}
			auditor := &institutionAuditorFake{events: &events, beginID: model.NewID()}
			service := &Service{users: persistence, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: auditor, mail: &securityNoticeMailerFake{events: &events}, publisher: accountStatePublisher(t, &events), now: func() time.Time { return time.UnixMilli(500) }}
			_, err := service.SetUserEnabled(context.Background(), invocation.Invocation{}, SetUserEnabledCommand{ID: user.ID, Reason: test.reason})
			require.NoError(t, err)
			require.Len(t, auditor.values, 1)
			reason, present := auditor.values[0]["reason"]
			require.Equal(t, test.want != nil, present, "parameters = %#v", auditor.values[0])
			if present {
				require.Equal(t, test.want, reason)
			}
			require.Equal(t, true, auditor.values[0]["disabled"])
			require.Equal(t, user.ID, auditor.values[0]["user_id"])
		})
	}
}

func TestAccountDisableRejectsAnInvalidReasonBeforeAuthorization(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		reason  string
		enabled bool
	}{
		"blank":           {reason: " \t "},
		"too long":        {reason: strings.Repeat("a", MaximumAccountDisableReasonRunes+1)},
		"control":         {reason: "left\nthe institution"},
		"C1 control":      {reason: "left\u0085the institution"},
		"enable with one": {reason: "back", enabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			service := &Service{users: &accountStateStoreFake{events: &events}, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events}, mail: &securityNoticeMailerFake{events: &events}, publisher: accountStatePublisher(t, &events), now: time.Now}
			_, err := service.SetUserEnabled(context.Background(), invocation.Invocation{}, SetUserEnabledCommand{ID: model.NewUserID(), Enabled: test.enabled, Reason: &test.reason})
			var appErr *apperror.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, "request.invalid", appErr.Code())
			require.Equal(t, "reason", appErr.Fields()["field"])
			require.Empty(t, events, "an invalid reason must not authorize, read, or audit")
		})
	}
}

func TestAccountDisableFailurePublishesNoRevocation(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Locale: "en", Timezone: "UTC"}
	persistence := &accountStateStoreFake{events: &events, user: user, err: store.NewErrConflict("user", "users_revision", errors.New("stale"))}
	service := &Service{users: persistence, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, mail: &securityNoticeMailerFake{events: &events}, publisher: accountStatePublisher(t, &events), now: time.Now}
	_, err := service.SetUserEnabled(context.Background(), invocation.Invocation{}, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.True(t, apperror.Is(err, "user.conflict"), "error = %v", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	require.Equal(t, []string{"authorize-manage", "get-user", "prepare-mail", "audit-begin", "store-set-disabled", "audit-fail"}, events)
}

func TestAccountEnablePublishesNoRevocation(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", DisabledAt: model.OptionalTimeFromMillis(100), Locale: "en", Timezone: "UTC"}
	updated := *user
	updated.DisabledAt = model.OptionalTime{}
	updated.Revision++
	persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: &updated, RevokedSessions: []*model.Session{}, RevokedTokenHashes: []string{}}}
	mailer := &securityNoticeMailerFake{events: &events}
	service := &Service{users: persistence, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, mail: mailer, publisher: accountStatePublisher(t, &events), now: func() time.Time { return time.UnixMilli(500) }}
	result, err := service.SetUserEnabled(context.Background(), invocation.Invocation{}, SetUserEnabledCommand{ID: user.ID, Enabled: true})
	require.NoError(t, err)
	require.False(t, result.DisabledAt.Valid, "result/input = %#v / %#v", result, persistence.input)
	require.False(t, persistence.input.Disabled, "result/input = %#v / %#v", result, persistence.input)
	require.Len(t, mailer.requests, 1)
	require.Equal(t, model.MailTemplateIdentityAccountEnabled, mailer.requests[0].TemplateKey)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"authorize-manage", "get-user", "prepare-mail", "audit-begin", "store-set-disabled"}, events)
}

func TestAccountSelfDisableIsRejectedAfterAuthorization(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	service := &Service{users: &accountStateStoreFake{events: &events}, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events}, mail: &securityNoticeMailerFake{events: &events}, publisher: accountStatePublisher(t, &events), now: time.Now}
	invocation := invocation.New(model.Principal{UserID: model.UserID(userID)}, model.RequestMetadata{})
	_, err := service.SetUserEnabled(context.Background(), invocation, SetUserEnabledCommand{ID: model.UserID(userID), Enabled: false})
	require.True(t, apperror.Is(err, "request.invalid"), "error = %v", err)
	require.Equal(t, []string{"authorize-manage"}, events, "events")
}

func TestAccountStateReplayDoesNotCreateAnotherNoticeOrAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DisabledAt: model.OptionalTimeFromMillis(200)}
	mailer := &securityNoticeMailerFake{events: &events}
	service := &Service{users: &accountStateStoreFake{events: &events, user: user}, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events}, mail: mailer, publisher: accountStatePublisher(t, &events), now: time.Now}
	result, err := service.SetUserEnabled(context.Background(), invocation.Invocation{}, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.NoError(t, err, "replay result/error/mail")
	require.Same(t, user, result, "replay result/error/mail")
	require.Empty(t, mailer.requests, "replay result/error/mail")
	require.Equal(t, []string{"authorize-manage", "get-user"}, events)
}

func TestAccountStateIdempotentNoOpPreparesNoticeForAuthoritativeRace(t *testing.T) {
	t.Parallel()
	events := []string{}
	actor := model.NewUserID()
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(200), Revision: 3,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DisabledAt: model.OptionalTimeFromMillis(200)}
	persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: user}}
	mailer := &securityNoticeMailerFake{events: &events}
	service := &Service{users: persistence, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, mail: mailer, publisher: accountStatePublisher(t, &events), now: time.Now}
	result, err := service.SetUserEnabled(context.Background(), invocation.New(model.Principal{UserID: actor}, model.RequestMetadata{}),
		SetUserEnabledCommand{ID: user.ID, Enabled: false, IdempotencyKey: "row"})
	require.NoError(t, err, "idempotent no-op result")
	require.Same(t, user, result, "idempotent no-op result")
	require.Len(t, mailer.requests, 1, "idempotent no-op result")
	require.NotNil(t, persistence.input, "idempotent no-op result")
	require.NotNil(t, persistence.input.Occurrence, "idempotent no-op result")
}

func TestAccountStateRetainedOutcomeBypassesMailAfterLaterStateChange(t *testing.T) {
	t.Parallel()
	events := []string{}
	actor := model.NewUserID()
	current := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(300), Revision: 4,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC"}
	original := *current
	original.DisabledAt = model.OptionalTimeFromMillis(200)
	persistence := &accountStateStoreFake{events: &events, user: current, result: &store.UserDisabledStateResult{User: &original}}
	mailer := &securityNoticeMailerFake{events: &events, err: errors.New("mail unavailable")}
	service := &Service{users: persistence, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events, beginID: model.NewID()}, mail: mailer, publisher: accountStatePublisher(t, &events), now: time.Now}
	result, err := service.SetUserEnabled(context.Background(), invocation.New(model.Principal{UserID: actor}, model.RequestMetadata{}),
		SetUserEnabledCommand{ID: current.ID, Enabled: false, IdempotencyKey: "row", Batch: batchitem.New(batchitem.Fields{RetainedOutcome: true})})
	require.NoError(t, err, "retained result=%#v error=%v mail", result, err)
	require.False(t, result != &original && result.ID != original.ID, "retained result=%#v error=%v mail", result, err)
	require.Empty(t, mailer.requests, "retained result=%#v error=%v mail", result, err)
}

func TestAccountStateMailPreparationFailureStartsNoMutation(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC"}
	service := &Service{users: &accountStateStoreFake{events: &events, user: user}, authorizer: &accountAuthorizerFake{events: &events}, capabilities: &accessPolicyCapabilitiesFake{}, auditor: &institutionAuditorFake{events: &events}, mail: &securityNoticeMailerFake{events: &events, err: errors.New("render failed")}, publisher: accountStatePublisher(t, &events), now: time.Now}
	_, err := service.SetUserEnabled(context.Background(), invocation.Invocation{}, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.True(t, apperror.Is(err, "administration.unavailable"), "error = %v", err)
	require.Equal(t, []string{"authorize-manage", "get-user", "prepare-mail"}, events)
}
