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

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
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

// accountStateTestSeed is a valid default picture seed, so the test Users
// pass the validation the real mail Composer applies to a recipient.
const accountStateTestSeed = "0000000000000000000000000000000000000000000000000000000000000000"

// accountStateRenderer places "prepare-mail" in the shared log each time the
// real Composer renders an account state notice, and fails when err is set.
type accountStateRenderer struct {
	mailRenderRecorder
	events *[]string
}

func (r *accountStateRenderer) Render(request mail.RenderRequest) (mail.FrozenContent, error) {
	*r.events = append(*r.events, "prepare-mail")
	return r.mailRenderRecorder.Render(request)
}

// accountStateMail builds the production mail Composer over an enabled
// sender, rendering through an accountStateRenderer that logs to events.
func accountStateMail(t *testing.T, events *[]string) (*mail.Composer, *accountStateRenderer) {
	t.Helper()
	renderer := &accountStateRenderer{events: events}
	composer, err := mail.NewComposer(mail.ComposerDependencies{
		Renderer: renderer,
		Sender:   &mailSenderFake{enabled: true, from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}},
		Sealer:   mailTestSealer(t),
	})
	require.NoError(t, err)
	return composer, renderer
}

// accountStateResolvedUsers resolves every User the authorizer is asked
// about to an active account, so authorization does not read the logged
// User store under test.
type accountStateResolvedUsers struct{ store.UserStore }

func (accountStateResolvedUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	user := &model.User{Username: "subject", Email: "subject@example.edu", Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(id, time.UnixMilli(1))
	return user, nil
}

// accountStateTest is a Service built for account state, the invocation of a
// manager who holds user.manage Institution-wide, the real auditor's fixture
// and the renderer of the real mail Composer.
type accountStateTest struct {
	service  *Service
	inv      invocation.Invocation
	access   *accesstest.Fixture
	renderer *accountStateRenderer
}

// newAccountStateTest builds it over users. actor is the manager's User; a
// zero actor is a new User. The authorizer's reads and decision and the
// audit attempt and outcome join events next to the other fakes' entries.
func newAccountStateTest(t *testing.T, events *[]string, users store.UserStore, actor model.UserID, capabilities *DeploymentCapabilities) accountStateTest {
	t.Helper()
	principal := userSettingsSessionPrincipal(time.Now())
	if !actor.IsZero() {
		principal.UserID = actor
	}
	fixture := accessWith(t, events, accesstest.Options{
		Grants: []accesstest.Grant{institutionGrant(principal.UserID, model.ActionUserManage)},
		Users:  accountStateResolvedUsers{},
	})
	composer, renderer := accountStateMail(t, events)
	if capabilities == nil {
		capabilities = noCapabilities()
	}
	service := withAccess(&Service{users: users, capabilities: capabilities, mail: composer,
		publisher: accountStatePublisher(t, events), now: time.Now}, fixture)
	return accountStateTest{service: service, inv: invocation.New(principal, model.RequestMetadata{}), access: fixture, renderer: renderer}
}

func TestAccountDisableCommitsBeforePublishingRevocation(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DefaultProfilePictureSeed: accountStateTestSeed}
	updated := *user
	updated.DisabledAt = model.OptionalTimeFromMillis(500)
	updated.Revision++
	revoked := &model.Session{ID: model.NewSessionID()}
	persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: &updated, RevokedSessions: []*model.Session{revoked}, RevokedTokenHashes: []string{"hash"}}}
	capabilities := deploymentCapabilities(AccessPolicyCapabilitySnapshot{Providers: []AccessPolicyProviderCapability{{
		Descriptor: model.ExternalAuthenticationProvider{ID: "campus", Type: "oidc"},
	}}})
	test := newAccountStateTest(t, &events, persistence, "", capabilities)
	service := test.service
	service.now = func() time.Time { return time.UnixMilli(500) }
	effects := service.publisher.(*realtimetest.Publisher)
	result, err := service.SetUserEnabled(context.Background(), test.inv, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.NoError(t, err)
	require.Equal(t, int64(500), result.DisabledAt.Millis(), "result/input = %#v / %#v", result, persistence.input)
	require.Equal(t, int64(3), persistence.input.ExpectedRevision, "result/input = %#v / %#v", result, persistence.input)
	require.True(t, persistence.input.Disabled, "result/input = %#v / %#v", result, persistence.input)
	require.NotEmpty(t, persistence.input.AuditEventID, "result/input = %#v / %#v", result, persistence.input)
	_, available := persistence.input.Capabilities.Providers["campus"]
	require.True(t, available, "Store capability snapshot = %#v, want configured campus provider", persistence.input.Capabilities)
	rendered := test.renderer.keys()
	require.Equal(t, []model.MailTemplateKey{model.MailTemplateIdentityAccountDisabled}, rendered, "mail request/input = %#v / %#v", rendered, persistence.input)
	require.NotNil(t, persistence.input.Occurrence, "mail request/input = %#v / %#v", rendered, persistence.input)
	require.NotNil(t, persistence.input.Delivery, "mail request/input = %#v / %#v", rendered, persistence.input)
	require.NotNil(t, persistence.input.DeliveryJob, "mail request/input = %#v / %#v", rendered, persistence.input)
	attempts := recordedAttempts(t, test.access)
	require.Len(t, attempts, 1)
	require.Equal(t, attempts[0].ID, persistence.input.AuditEventID)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	require.Equal(t, []string{"grants", "decision:user.manage:success", "get-user", "prepare-mail", "audit-begin:user.manage", "store-set-disabled", "publish-revocation"}, events)
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
			user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DefaultProfilePictureSeed: accountStateTestSeed}
			updated := *user
			updated.DisabledAt = model.OptionalTimeFromMillis(500)
			persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: &updated}}
			fixture := newAccountStateTest(t, &events, persistence, "", nil)
			fixture.service.now = func() time.Time { return time.UnixMilli(500) }
			_, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv, SetUserEnabledCommand{ID: user.ID, Reason: test.reason})
			require.NoError(t, err)
			attempts := recordedAttempts(t, fixture.access)
			require.Len(t, attempts, 1)
			value, _ := attempts[0].value().(map[string]any)
			reason, present := value["reason"]
			require.Equal(t, test.want != nil, present, "parameters = %#v", value)
			if present {
				require.Equal(t, test.want, reason)
			}
			require.Equal(t, true, value["disabled"])
			require.Equal(t, user.ID.String(), value["user_id"])
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
			fixture := newAccountStateTest(t, &events, &accountStateStoreFake{events: &events}, "", nil)
			_, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv, SetUserEnabledCommand{ID: model.NewUserID(), Enabled: test.enabled, Reason: &test.reason})
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
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DefaultProfilePictureSeed: accountStateTestSeed}
	persistence := &accountStateStoreFake{events: &events, user: user, err: store.NewErrConflict("user", "users_revision", errors.New("stale"))}
	fixture := newAccountStateTest(t, &events, persistence, "", nil)
	_, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.True(t, apperror.Is(err, "user.conflict"), "error = %v", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	require.Equal(t, []string{"grants", "decision:user.manage:success", "get-user", "prepare-mail", "audit-begin:user.manage", "store-set-disabled", "audit-fail"}, events)
}

func TestAccountEnablePublishesNoRevocation(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3, Username: "student", Email: "student@example.edu", DisabledAt: model.OptionalTimeFromMillis(100), Locale: "en", Timezone: "UTC", DefaultProfilePictureSeed: accountStateTestSeed}
	updated := *user
	updated.DisabledAt = model.OptionalTime{}
	updated.Revision++
	persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: &updated, RevokedSessions: []*model.Session{}, RevokedTokenHashes: []string{}}}
	fixture := newAccountStateTest(t, &events, persistence, "", nil)
	fixture.service.now = func() time.Time { return time.UnixMilli(500) }
	result, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv, SetUserEnabledCommand{ID: user.ID, Enabled: true})
	require.NoError(t, err)
	require.False(t, result.DisabledAt.Valid, "result/input = %#v / %#v", result, persistence.input)
	require.False(t, persistence.input.Disabled, "result/input = %#v / %#v", result, persistence.input)
	require.Equal(t, []model.MailTemplateKey{model.MailTemplateIdentityAccountEnabled}, fixture.renderer.keys())
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"grants", "decision:user.manage:success", "get-user", "prepare-mail", "audit-begin:user.manage", "store-set-disabled"}, events)
}

func TestAccountSelfDisableIsRejectedAfterAuthorization(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewUserID()
	fixture := newAccountStateTest(t, &events, &accountStateStoreFake{events: &events}, userID, nil)
	_, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv, SetUserEnabledCommand{ID: userID, Enabled: false})
	require.True(t, apperror.Is(err, "request.invalid"), "error = %v", err)
	require.Equal(t, []string{"grants", "decision:user.manage:success"}, events, "events")
}

func TestAccountStateReplayDoesNotCreateAnotherNoticeOrAudit(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DisabledAt: model.OptionalTimeFromMillis(200), DefaultProfilePictureSeed: accountStateTestSeed}
	fixture := newAccountStateTest(t, &events, &accountStateStoreFake{events: &events, user: user}, "", nil)
	result, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.NoError(t, err, "replay result/error/mail")
	require.Same(t, user, result, "replay result/error/mail")
	require.Empty(t, fixture.renderer.rendered(), "replay result/error/mail")
	require.Equal(t, []string{"grants", "decision:user.manage:success", "get-user"}, events)
}

func TestAccountStateIdempotentNoOpPreparesNoticeForAuthoritativeRace(t *testing.T) {
	t.Parallel()
	events := []string{}
	actor := model.NewUserID()
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(200), Revision: 3,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DisabledAt: model.OptionalTimeFromMillis(200), DefaultProfilePictureSeed: accountStateTestSeed}
	persistence := &accountStateStoreFake{events: &events, user: user, result: &store.UserDisabledStateResult{User: user}}
	fixture := newAccountStateTest(t, &events, persistence, actor, nil)
	result, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv,
		SetUserEnabledCommand{ID: user.ID, Enabled: false, IdempotencyKey: "row"})
	require.NoError(t, err, "idempotent no-op result")
	require.Same(t, user, result, "idempotent no-op result")
	require.Len(t, fixture.renderer.rendered(), 1, "idempotent no-op result")
	require.NotNil(t, persistence.input, "idempotent no-op result")
	require.NotNil(t, persistence.input.Occurrence, "idempotent no-op result")
}

func TestAccountStateRetainedOutcomeBypassesMailAfterLaterStateChange(t *testing.T) {
	t.Parallel()
	events := []string{}
	actor := model.NewUserID()
	current := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(300), Revision: 4,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DefaultProfilePictureSeed: accountStateTestSeed}
	original := *current
	original.DisabledAt = model.OptionalTimeFromMillis(200)
	persistence := &accountStateStoreFake{events: &events, user: current, result: &store.UserDisabledStateResult{User: &original}}
	fixture := newAccountStateTest(t, &events, persistence, actor, nil)
	fixture.renderer.err = errors.New("mail unavailable")
	result, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv,
		SetUserEnabledCommand{ID: current.ID, Enabled: false, IdempotencyKey: "row", Batch: batchitem.New(batchitem.Fields{RetainedOutcome: true})})
	require.NoError(t, err, "retained result=%#v error=%v mail", result, err)
	require.False(t, result != &original && result.ID != original.ID, "retained result=%#v error=%v mail", result, err)
	require.Empty(t, fixture.renderer.rendered(), "retained result=%#v error=%v mail", result, err)
}

func TestAccountStateMailPreparationFailureStartsNoMutation(t *testing.T) {
	t.Parallel()
	events := []string{}
	user := &model.User{ID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 3,
		Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC", DefaultProfilePictureSeed: accountStateTestSeed}
	fixture := newAccountStateTest(t, &events, &accountStateStoreFake{events: &events, user: user}, "", nil)
	fixture.renderer.err = errors.New("render failed")
	_, err := fixture.service.SetUserEnabled(context.Background(), fixture.inv, SetUserEnabledCommand{ID: user.ID, Enabled: false})
	require.True(t, apperror.Is(err, "administration.unavailable"), "error = %v", err)
	require.Equal(t, []string{"grants", "decision:user.manage:success", "get-user", "prepare-mail"}, events)
}
