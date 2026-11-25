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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type sessionAdministrationStoreFake struct {
	store.SessionStore
	events          *[]string
	session         *model.Session
	list            []*model.Session
	revokeInput     *store.SessionRevocation
	revokeAllInput  *store.UserSessionsRevocation
	revokeResult    *store.SessionRevocationResult
	revokeAllResult *store.UserSessionsRevocationResult
	getErr          error
	listErr         error
	revokeErr       error
	revokeAllErr    error
	replayed        bool
	noOp            bool
	listedPage      *store.SessionPageQuery
}

func (s *sessionAdministrationStoreFake) Get(context.Context, model.SessionID) (*model.Session, error) {
	*s.events = append(*s.events, "get-session")
	return s.session, s.getErr
}

func (s *sessionAdministrationStoreFake) ListByUser(_ context.Context, page *store.SessionPageQuery) ([]*model.Session, error) {
	s.listedPage = page
	*s.events = append(*s.events, "list")
	return s.list, s.listErr
}

func (s *sessionAdministrationStoreFake) Revoke(_ context.Context, input *store.SessionRevocation) (*store.SessionRevocationResult, error) {
	*s.events = append(*s.events, "store-revoke")
	s.revokeInput = input
	return s.revokeResult, s.revokeErr
}

func (s *sessionAdministrationStoreFake) RevokeAllForUser(_ context.Context, input *store.UserSessionsRevocation) (*store.UserSessionsRevocationResult, error) {
	*s.events = append(*s.events, "store-revoke-all")
	s.revokeAllInput = input
	input.Replayed, input.NoOp = s.replayed, s.noOp
	return s.revokeAllResult, s.revokeAllErr
}

// sessionAdministrationTest is a Service built for Session administration,
// the invocation of an administrator who holds session.view and
// session.manage Institution-wide, the real auditor's fixture and what the
// real mail Composer rendered.
type sessionAdministrationTest struct {
	service *Service
	inv     invocation.Invocation
	access  *accesstest.Fixture
	renders *mailtest.Renders
}

// newSessionAdministrationTest builds it. The authorizer's reads and
// decisions and the audit attempt and outcome join events next to the other
// fakes' entries.
func newSessionAdministrationTest(t *testing.T, events *[]string, sessions store.SessionStore, users store.UserStore, now func() time.Time) sessionAdministrationTest {
	t.Helper()
	principal := userSettingsSessionPrincipal(time.Now())
	fixture := accessWith(t, events, accesstest.Options{
		Grants: []accesstest.Grant{institutionGrant(principal.UserID, model.ActionSessionView, model.ActionSessionManage)},
		Users:  accountStateResolvedUsers{},
	})
	composer, _, renders := accountStateMail(t, events)
	service := withAccess(&Service{sessions: sessions, users: users, mail: composer, publisher: sessionAdministrationPublisher(events), now: now}, fixture)
	return sessionAdministrationTest{service: service, inv: invocation.New(principal, model.RequestMetadata{}), access: fixture, renders: renders}
}

// sessionAdministrationPublisher returns a recording Publisher that also
// places each call in the shared log: a revocation as "publish-revocation",
// and an authentication-cache invalidation, which Session administration never
// publishes on its own, as "invalidate-authentication-cache".
func sessionAdministrationPublisher(events *[]string) *realtimetest.Publisher {
	return &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		switch call.Method {
		case realtimetest.SessionsRevoked:
			*events = append(*events, "publish-revocation")
		case realtimetest.AuthenticationCacheInvalidated:
			*events = append(*events, "invalidate-authentication-cache")
		default:
			*events = append(*events, string(call.Method))
		}
	}}
}

type sessionAdministrationUserStoreFake struct {
	store.UserStore
	events *[]string
	user   *model.User
	err    error
}

func (s *sessionAdministrationUserStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	*s.events = append(*s.events, "get-user")
	return s.user, s.err
}

func sessionAdministrationTestUser(id string) *model.User {
	return &model.User{ID: model.UserID(id), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Revision: 1,
		Username: "student", Email: "student@example.edu", DisplayName: "Student", Locale: "en", Timezone: "UTC",
		DefaultProfilePictureSeed: accountStateTestSeed}
}

func TestAdminSessionListAuthorizesThenReads(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	session := &model.Session{ID: model.NewSessionID(), UserID: model.UserID(userID)}
	at := time.Date(2026, 8, 12, 9, 30, 0, 123_456_789, time.FixedZone("offset", 7200))
	persistence := &sessionAdministrationStoreFake{events: &events, list: []*model.Session{session}}
	test := newSessionAdministrationTest(t, &events, persistence, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, func() time.Time { return at })
	service := test.service
	got, err := service.ListUserSessions(context.Background(), test.inv, ListUserSessionsQuery{UserID: model.UserID(userID), Limit: 50})
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, session.ID.String(), got.Items[0].ID.String())
	require.False(t, got.HasMore)
	// Contract: authorization precedes the read.
	require.Equal(t, []string{"grants", "decision:session.view:success", "list"}, events)
	require.Equal(t, 51, persistence.listedPage.Limit, "the read probes one past the page")
	require.True(t, persistence.listedPage.ActiveAt.Equal(model.TimeUTC(at)), "active Session listing time = %v", persistence.listedPage.ActiveAt)
	require.Same(t, time.UTC, persistence.listedPage.ActiveAt.Location(), "active Session listing time = %v", persistence.listedPage.ActiveAt)
}

// disabledResolvedUsers resolves every user as a disabled account.
type disabledResolvedUsers struct{ store.UserStore }

func (disabledResolvedUsers) Get(ctx context.Context, id model.UserID) (*model.User, error) {
	user, err := accountStateResolvedUsers{}.Get(ctx, id)
	user.DisabledAt = model.OptionalTimeFromMillis(2)
	return user, err
}

func TestAdminSessionListReadsADisabledAccount(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewUserID()
	persistence := &sessionAdministrationStoreFake{events: &events, list: []*model.Session{{ID: model.NewSessionID(), UserID: userID}}}
	principal := userSettingsSessionPrincipal(time.Now())
	fixture := accessWith(t, &events, accesstest.Options{
		Grants: []accesstest.Grant{institutionGrant(principal.UserID, model.ActionSessionView, model.ActionSessionManage)},
		Users:  disabledResolvedUsers{},
	})
	service := withAccess(&Service{sessions: persistence, now: time.Now}, fixture)
	inv := invocation.New(principal, model.RequestMetadata{})

	page, err := service.ListUserSessions(context.Background(), inv, ListUserSessionsQuery{UserID: userID, Limit: 50})
	require.NoError(t, err, "an administrator still reads a disabled account's sessions")
	require.Len(t, page.Items, 1)

	err = fixture.Authorizer.Authorize(context.Background(), inv, model.ActionSessionManage, model.Resource{Type: model.ResourceUser, ID: userID.String()})
	require.Error(t, err, "changes to a disabled account stay refused")
}

func TestAdminSessionHistoryIsPaged(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewUserID()
	rows := []*model.Session{{ID: model.NewSessionID()}, {ID: model.NewSessionID()}, {ID: model.NewSessionID()}}
	persistence := &sessionAdministrationStoreFake{events: &events, list: rows}
	test := newSessionAdministrationTest(t, &events, persistence, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID.String())}, time.Now)
	service := test.service
	before := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	beforeID := model.NewSessionID()
	page, err := service.ListUserSessions(context.Background(), test.inv, ListUserSessionsQuery{
		UserID: userID, IncludeRevoked: true, Limit: 2, BeforeCreatedAt: before, BeforeID: beforeID,
	})
	require.NoError(t, err)
	require.Equal(t, &store.SessionPageQuery{UserID: userID, Limit: 3, BeforeCreatedAt: before, BeforeID: beforeID}, persistence.listedPage,
		"history includes revoked Sessions, continues after the cursor and probes one past the page")
	require.Equal(t, rows[:2], page.Items)
	require.True(t, page.HasMore)

	persistence.list = rows[:2]
	page, err = service.ListUserSessions(context.Background(), test.inv, ListUserSessionsQuery{UserID: userID, IncludeRevoked: true, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, rows[:2], page.Items)
	require.False(t, page.HasMore, "an exactly full final page has no successor")

	for _, query := range []ListUserSessionsQuery{
		{UserID: userID, Limit: 0},
		{UserID: userID, Limit: 201},
		{UserID: userID, Limit: 10, BeforeID: beforeID},
	} {
		_, err = service.ListUserSessions(context.Background(), test.inv, query)
		require.True(t, apperror.Is(err, "request.invalid"), "query %#v: err = %v", query, err)
	}
}

func TestAdminSessionRevokeCommitsBeforePublishing(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	session := &model.Session{
		ID: model.NewSessionID(), UserID: model.UserID(userID),
		CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
	}
	revoked := *session
	revoked.RevokedAt = model.OptionalTimeFromMillis(500)
	persistence := &sessionAdministrationStoreFake{
		events:  &events,
		session: session,
		revokeResult: &store.SessionRevocationResult{
			Session: &revoked, TokenHashes: []string{"hash"},
		},
	}
	test := newSessionAdministrationTest(t, &events, persistence, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, func() time.Time { return time.UnixMilli(500) })
	service := test.service
	require.NoError(t, service.RevokeUserSession(context.Background(), test.inv, model.UserID(userID), session.ID))
	require.Equal(t, session.ID, persistence.revokeInput.SessionID)
	require.NotEmpty(t, persistence.revokeInput.AuditEventID)
	require.Equal(t, 1, test.renders.Count(), "mail input = %#v", persistence.revokeInput)
	require.NotNil(t, persistence.revokeInput.Occurrence, "mail input = %#v", persistence.revokeInput)
	require.NotNil(t, persistence.revokeInput.Delivery, "mail input = %#v", persistence.revokeInput)
	require.Equal(t, model.MailTemplateIdentitySessionsRevokedByAdmin, persistence.revokeInput.Delivery.TemplateKey, "mail input = %#v", persistence.revokeInput)
	require.NotNil(t, persistence.revokeInput.DeliveryJob, "mail input = %#v", persistence.revokeInput)
	attempts := recordedAttempts(t, test.access)
	require.Len(t, attempts, 1)
	require.Equal(t, attempts[0].ID, persistence.revokeInput.AuditEventID)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	accesstest.RequireContract(t, events, []string{"get-session", "get-user"},
		"grants", "decision:session.manage:success", "prepare-mail", "audit-begin:session.manage", "store-revoke", "publish-revocation")
}

func TestAdminSessionRevokeFailurePublishesNoEffect(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	session := &model.Session{
		ID: model.NewSessionID(), UserID: model.UserID(userID),
		CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
	}
	test := newSessionAdministrationTest(t, &events, &sessionAdministrationStoreFake{
		events:    &events,
		session:   session,
		revokeErr: store.NewErrConflict("session", "busy", errors.New("busy")),
	}, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, time.Now)
	service := test.service
	err := service.RevokeUserSession(context.Background(), test.inv, model.UserID(userID), session.ID)
	require.True(t, apperror.Is(err, "administration.unavailable"), "error = %v", err)
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	accesstest.RequireContract(t, events, []string{"get-session", "get-user"},
		"grants", "decision:session.manage:success", "prepare-mail", "audit-begin:session.manage", "store-revoke", "audit-fail")
}

func TestAdminSessionRevokeAllCommitsBeforePublishing(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	session := &model.Session{ID: model.NewSessionID(), UserID: model.UserID(userID)}
	persistence := &sessionAdministrationStoreFake{
		events: &events,
		revokeAllResult: &store.UserSessionsRevocationResult{
			Sessions: []*model.Session{session}, TokenHashes: []string{"hash"},
		},
	}
	test := newSessionAdministrationTest(t, &events, persistence, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, func() time.Time { return time.UnixMilli(500) })
	service := test.service
	require.NoError(t, service.RevokeUserSessions(context.Background(), test.inv, RevokeUserSessionsCommand{UserID: model.UserID(userID)}))
	require.Equal(t, model.UserID(userID), persistence.revokeAllInput.UserID)
	require.NotEmpty(t, persistence.revokeAllInput.AuditEventID)
	require.NotNil(t, persistence.revokeAllInput.Occurrence)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effect.
	accesstest.RequireContract(t, events, []string{"get-user"},
		"grants", "decision:session.manage:success", "prepare-mail", "audit-begin:session.manage", "store-revoke-all", "publish-revocation")
}

func TestAdminSessionIdempotentNoOpPreparesNoticeForAuthoritativeRace(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewUserID().String()
	persistence := &sessionAdministrationStoreFake{events: &events, list: []*model.Session{},
		revokeAllResult: &store.UserSessionsRevocationResult{Sessions: []*model.Session{}, TokenHashes: []string{}}}
	test := newSessionAdministrationTest(t, &events, persistence, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, time.Now)
	service := test.service
	inv := test.inv
	require.NoError(t, service.RevokeUserSessions(context.Background(), inv, RevokeUserSessionsCommand{UserID: model.UserID(userID), IdempotencyKey: "row"}))
	require.Equal(t, 1, test.renders.Count(), "idempotent no-op mail")
	require.NotNil(t, persistence.revokeAllInput, "idempotent no-op mail")
	require.NotNil(t, persistence.revokeAllInput.Occurrence, "idempotent no-op mail")
}

func TestAdminSessionRetainedOutcomeBypassesMailAfterNewSession(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewUserID().String()
	persistence := &sessionAdministrationStoreFake{events: &events,
		list:            []*model.Session{{ID: model.NewSessionID(), UserID: model.UserID(userID)}},
		revokeAllResult: &store.UserSessionsRevocationResult{Sessions: []*model.Session{}, TokenHashes: []string{}}}
	test := newSessionAdministrationTest(t, &events, persistence, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, time.Now)
	service := test.service
	inv := test.inv
	require.NoError(t, service.RevokeUserSessions(context.Background(), inv, RevokeUserSessionsCommand{UserID: model.UserID(userID), IdempotencyKey: "row", Batch: batchitem.New(batchitem.Fields{RetainedOutcome: true})}))
	require.Zero(t, test.renders.Count(), "retained mail")
	accesstest.RequireContract(t, events, []string{"get-user"},
		"grants", "decision:session.manage:success", "audit-begin:session.manage", "store-revoke-all")
}

func TestAdminSessionCrossUserNotFound(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	session := &model.Session{ID: model.NewSessionID(), UserID: model.NewUserID()}
	test := newSessionAdministrationTest(t, &events, &sessionAdministrationStoreFake{events: &events, session: session}, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, time.Now)
	service := test.service
	err := service.RevokeUserSession(context.Background(), test.inv, model.UserID(userID), session.ID)
	require.True(t, apperror.Is(err, "session.not_found"), "error = %v", err)
	require.Equal(t, []string{"grants", "decision:session.manage:success", "get-session"}, events)
}

func TestAdminSessionRevocationOutcomesPreserveAuditAndBatchResult(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"changed", "empty", "replay", "attempt unavailable", "commit unavailable", "failure audit unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			now := time.UnixMilli(500)
			userID := model.NewUserID().String()
			events := []string{}
			persistence := &sessionAdministrationStoreFake{events: &events,
				revokeAllResult: &store.UserSessionsRevocationResult{Sessions: []*model.Session{
					{ID: model.NewSessionID(), UserID: model.UserID(userID)},
					{ID: model.NewSessionID(), UserID: model.UserID(userID)},
				}, TokenHashes: []string{"first-hash", "second-hash"}}}
			unchanged := false
			effects := sessionAdministrationPublisher(&events)
			test := newSessionAdministrationTest(t, &events, persistence, &sessionAdministrationUserStoreFake{events: &events, user: sessionAdministrationTestUser(userID)}, func() time.Time { return now })
			service := test.service
			service.publisher = effects
			switch outcome {
			case "empty", "replay":
				persistence.revokeAllResult = &store.UserSessionsRevocationResult{}
				persistence.noOp, persistence.replayed = outcome == "empty", outcome == "replay"
			case "attempt unavailable":
				test.access.Audits.FailAttempts(errors.New("audit store unavailable"))
				unchanged = true // An unattempted mutation does not replace the caller's value.
			case "commit unavailable", "failure audit unavailable":
				persistence.revokeAllErr = errors.New("atomic revocation failed")
				if outcome == "failure audit unavailable" {
					test.access.Audits.FailCompletions(errors.New("audit store unavailable"))
				}
			}
			inv := test.inv
			err := service.RevokeUserSessions(context.Background(), inv, RevokeUserSessionsCommand{
				UserID: model.UserID(userID), IdempotencyKey: "repeat-command", Batch: batchitem.New(batchitem.Fields{Replayed: &unchanged}),
			})
			wantCode := ""
			switch outcome {
			case "attempt unavailable":
				wantCode = "audit.unavailable"
			case "commit unavailable", "failure audit unavailable":
				// A failed failure-audit completion joins, never replaces, the commit failure.
				wantCode = "administration.unavailable"
			}
			require.False(t, wantCode == "" && err != nil, "error = %v, want %q", err, wantCode)
			require.False(t, wantCode != "" && !apperror.Is(err, wantCode), "error = %v, want %q", err, wantCode)
			require.Equal(t, outcome == "empty" || outcome == "replay" || outcome == "attempt unavailable", unchanged, "unchanged = %t for %s", unchanged, outcome)
			// Contract (guardrails 8 and 14): authorization precedes the audit, the
			// audit begins before the durable revocation, and only a committed
			// change publishes. The exact sequence also proves that no other write,
			// effect, or audit completion ran.
			want := []string{"grants", "decision:session.manage:success", "get-user", "prepare-mail", "audit-begin:session.manage"}
			attempts := recordedAttempts(t, test.access)
			if outcome == "attempt unavailable" {
				require.Nil(t, persistence.revokeAllInput, "revoked without a durable audit attempt")
			} else {
				want = append(want, "store-revoke-all")
				input := persistence.revokeAllInput
				require.Len(t, attempts, 1)
				require.Equal(t, attempts[0].ID, input.AuditEventID)
				require.True(t, model.TimeUTC(now).Equal(input.AuditAt))
				require.True(t, model.TimeUTC(now).Equal(input.RevokedAt))
				require.NotNil(t, input.Command)
				require.Equal(t, model.SessionRevocationAdministratorAllSessions, input.Reason)
			}
			if outcome == "changed" {
				want = append(want, "publish-revocation")
				// Contract: one revocation, for the User, of exactly the Sessions
				// and access-token hashes the Store revoked.
				require.Equal(t, []realtimetest.Call{{
					Method: realtimetest.SessionsRevoked, UserID: model.UserID(userID),
					SessionIDs:        sessionIDs(persistence.revokeAllResult.Sessions),
					AccessTokenHashes: persistence.revokeAllResult.TokenHashes,
				}}, effects.Calls())
			}
			if outcome == "commit unavailable" || outcome == "failure audit unavailable" {
				want = append(want, "audit-fail")
				if outcome == "commit unavailable" {
					require.Equal(t, model.AuditStatusFail, attempts[0].Status)
					require.Equal(t, "administration.unavailable", attempts[0].ErrorCode)
				} else {
					// The audit store refused the failure completion, so the
					// attempt stays open for operator reconciliation.
					require.Equal(t, model.AuditStatusAttempt, attempts[0].Status)
				}
			}
			require.Equal(t, want, events)
			if outcome != "attempt unavailable" {
				// An attempt the audit store refused is never kept, so only a
				// saved attempt can show what it recorded; the begin entry in
				// events shows the refused one named session.manage.
				require.Equal(t, string(model.ActionSessionManage), attempts[0].Action)
				require.Equal(t, userID, attempts[0].Resource.ID)
				require.Equal(t, "revoke_sessions", attempts[0].parameters["operation"])
			}
		})
	}
}
