// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type mailStoreFake struct {
	store.MailStore
	enqueue      *store.MailTestEnqueue
	delivery     *model.MailDelivery
	gets         int
	lists        int
	listLimit    int
	listCopies   int
	counts       int
	countTotal   int
	countOptions store.MailDeliveryListOptions
	mutates      int
}

func (s *mailStoreFake) EnqueueTest(_ context.Context, input *store.MailTestEnqueue) (*model.MailDelivery, error) {
	s.enqueue = input
	s.delivery = input.Delivery.Clone()
	return s.delivery.Clone(), nil
}

func (s *mailStoreFake) GetDelivery(_ context.Context, id model.MailDeliveryID) (*model.MailDelivery, error) {
	s.gets++
	if s.delivery == nil || s.delivery.ID != id {
		return nil, store.NewErrNotFound("mail_delivery", id.String())
	}
	return s.delivery.Clone(), nil
}

func (s *mailStoreFake) ListDeliveries(_ context.Context, options store.MailDeliveryListOptions) ([]*model.MailDelivery, error) {
	s.lists++
	s.listLimit = options.Limit
	if s.delivery == nil {
		return []*model.MailDelivery{}, nil
	}
	deliveries := []*model.MailDelivery{s.delivery.Clone()}
	for range s.listCopies {
		deliveries = append(deliveries, s.delivery.Clone())
	}
	return deliveries, nil
}

func (s *mailStoreFake) CountDeliveries(_ context.Context, options store.MailDeliveryListOptions, maximum int) (int, error) {
	s.counts++
	s.countOptions = options
	if s.delivery == nil {
		return 0, nil
	}
	return min(max(s.countTotal, 1+s.listCopies), maximum), nil
}

func (s *mailStoreFake) CancelDelivery(_ context.Context, input *store.MailDeliveryMutation) (*model.MailDelivery, error) {
	s.mutates++
	if s.delivery == nil || s.delivery.ID != input.ID || s.delivery.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("mail_delivery", "stale", nil)
	}
	next, err := s.delivery.Cancel(model.TimeUTC(input.AuditAt))
	if err == nil {
		s.delivery = next
	}
	return next, err
}

func (s *mailStoreFake) RetryDelivery(_ context.Context, input *store.MailDeliveryMutation) (*model.MailDelivery, error) {
	s.mutates++
	if s.delivery == nil || s.delivery.ID != input.ID || s.delivery.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("mail_delivery", "stale", nil)
	}
	next, err := s.delivery.OperatorRetry(model.TimeUTC(input.AuditAt))
	if err == nil {
		s.delivery = next
	}
	return next, err
}

type mailUserStoreFake struct {
	store.UserStore
	user *model.User
}

func (s mailUserStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	if s.user == nil {
		return nil, store.NewErrNotFound("user", "missing")
	}
	copy := *s.user
	return &copy, nil
}

type mailAuthorizerFake struct {
	actions []model.Action
	err     error
}

func (a *mailAuthorizerFake) AuthorizeInstitution(_ context.Context, _ invocation.Invocation, action model.Action) (model.Resource, error) {
	a.actions = append(a.actions, action)
	if a.err != nil {
		return model.Resource{}, a.err
	}
	return model.Resource{Type: model.ResourceInstitution, ID: model.NewInstitutionID().String()}, nil
}

type mailAuditFake struct {
	calls      int
	beginCalls int
	failCalls  int
}

func (a *mailAuditFake) PrepareTest(_ context.Context, inv invocation.Invocation, resource model.Resource, id model.MailDeliveryID) (*model.AuditEvent, error) {
	a.calls++
	principal := inv.Principal()
	return &model.AuditEvent{ActorID: principal.UserID, SessionID: principal.SessionID, Action: string(model.ActionMailManage), Resource: model.Resource{Type: model.ResourceMailDelivery, ID: id.String()}, ScopeType: model.RoleScopeInstitution, ScopeID: resource.ID, Status: model.AuditStatusSuccess, NodeID: "test", ClientType: string(principal.ClientType), AuthMethod: principal.AuthenticationMethod}, nil
}

func (a *mailAuditFake) PrepareControl(_ context.Context, _ invocation.Invocation, institution model.Resource, delivery *model.MailDelivery, operation string) (model.AuditEventID, error) {
	a.beginCalls++
	if institution.Type != model.ResourceInstitution || delivery == nil || (operation != "cancel" && operation != "retry") {
		return "", errors.New("unsafe mail audit")
	}
	return model.NewAuditEventID(), nil
}

func (a *mailAuditFake) Fail(context.Context, model.AuditEventID, string) error {
	a.failCalls++
	return nil
}

func TestMailServicePreparesOnlyControlledSelfRecipientAndAtomicIntent(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	persistence := &mailStoreFake{}
	sender := &mailSenderFake{enabled: true, from: Address{Name: "Proctor", Address: "no-reply@institution.test"}}
	authorizer, auditor := &mailAuthorizerFake{}, &mailAuditFake{}
	content := FrozenContent{Subject: "Fixed test", Text: "fixed text", HTML: "<p>fixed html</p>"}
	service, err := NewOperations(OperationsDependencies{Mail: persistence, Users: mailUserStoreFake{user: mailTestUser(principal, at)}, Authorization: authorizer, Audit: auditor, Attempts: &testLimiterFake{}, Renderer: mailRendererFake{content}, Sender: sender, Metrics: nopDeliveryRecorder{}, Sealer: mailTestSealer(t), RecentAuthenticationTTL: 15 * time.Minute, Now: func() time.Time { return at }})
	require.NoError(t, err)
	woke := 0
	service.wake = func() { woke++ }
	view, err := service.SendTestMail(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.NotNil(t, persistence.enqueue, "atomic intent")
	require.Equal(t, principal.UserID, persistence.enqueue.Delivery.TargetUserID, "atomic intent")
	require.Equal(t, principal.UserID, persistence.enqueue.Occurrence.ActorUserID, "atomic intent")
	require.Equal(t, model.JobTypeMailDeliver, persistence.enqueue.Job.Type, "atomic intent")
	require.Equal(t, 1, auditor.calls, "atomic intent")
	require.Equal(t, 1, woke, "atomic intent")
	serialized, _ := json.Marshal(persistence.enqueue)
	require.NotContains(t, string(serialized), "operator@example.test")
	require.NotContains(t, string(serialized), "fixed text")
	require.Equal(t, "o***@example.test", view.MaskedRecipient)
	require.Equal(t, model.MailDeliveryQueued, view.State)
	require.Equal(t, persistence.enqueue.Delivery.MessageID, view.MessageID)
}

func TestMailMetricsRequiresMailViewAuthorization(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	authorizer := &mailAuthorizerFake{}
	metrics := &recordingMailMetrics{health: []HealthMetric{{Code: HealthHealthy}}}
	service, err := NewOperations(OperationsDependencies{Mail: &mailStoreFake{}, Users: mailUserStoreFake{user: mailTestUser(principal, at)}, Authorization: authorizer, Audit: &mailAuditFake{}, Attempts: &testLimiterFake{}, Renderer: mailRendererFake{}, Sender: &mailSenderFake{}, Metrics: metrics, Sealer: nil, RecentAuthenticationTTL: 15 * time.Minute, Now: func() time.Time { return at }})
	require.NoError(t, err)
	_, err = service.GetMailMetrics(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.NoError(t, err)
	require.Len(t, authorizer.actions, 1)
	require.Equal(t, model.ActionMailView, authorizer.actions[0])
}

func TestMailServiceRejectsPATUnverifiedAndRateLimitedBeforePersistence(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	user := mailTestUser(principal, at)
	persistence := &mailStoreFake{}
	sender := &mailSenderFake{enabled: true, from: Address{Address: "no-reply@example.test"}}
	content := FrozenContent{Subject: "test", Text: "test"}
	service, err := NewOperations(OperationsDependencies{Mail: persistence, Users: mailUserStoreFake{user: user}, Authorization: &mailAuthorizerFake{}, Audit: &mailAuditFake{}, Attempts: &testLimiterFake{}, Renderer: mailRendererFake{content}, Sender: sender, Metrics: nopDeliveryRecorder{}, Sealer: mailTestSealer(t), RecentAuthenticationTTL: 15 * time.Minute, Now: func() time.Time { return at }})
	require.NoError(t, err)
	pat := principal
	pat.SessionID = ""
	pat.CredentialType = model.CredentialPersonalAccessToken
	pat.CredentialID = model.PrincipalCredentialID(model.NewPersonalAccessTokenID())
	pat.AuthenticationStrength = ""
	pat.AuthenticatedAt = time.Time{}
	pat.ClientType = model.SessionClientCLI
	pat.CredentialScopes = []string{string(model.ActionMailManage)}
	_, err = service.SendTestMail(context.Background(), invocation.New(pat, model.RequestMetadata{}))
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "PAT error = %v", err)
	stale := principal
	stale.AuthenticatedAt = at.Add(-16 * time.Minute)
	_, err = service.SendTestMail(context.Background(), invocation.New(stale, model.RequestMetadata{}))
	require.True(t, apperror.Is(err, "authentication.reauthentication_required"), "stale Session error = %v", err)
	user.EmailVerified = false
	_, err = service.SendTestMail(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.True(t, apperror.Is(err, "mail.recipient_unverified"), "unverified error = %v", err)
	user.EmailVerified = true
	for index := 1; index < testRateLimitMaximum; index++ {
		_, err = service.SendTestMail(context.Background(), invocation.New(principal, model.RequestMetadata{}))
		require.NoError(t, err)
	}
	_, err = service.SendTestMail(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.True(t, apperror.Is(err, "mail.test.rate_limited"), "rate limit error = %v", err)
}

func TestMailOperatorReadsAuthorizeBeforeInspectionAndReturnOnlySafeViews(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	delivery := queuedMailDeliveryFixtureForApp(at)
	persistence := &mailStoreFake{delivery: delivery}
	authorizer := &mailAuthorizerFake{}
	service, err := NewOperations(OperationsDependencies{Mail: persistence, Users: mailUserStoreFake{user: mailTestUser(principal, at)}, Authorization: authorizer, Audit: &mailAuditFake{}, Attempts: &testLimiterFake{}, Renderer: mailRendererFake{}, Sender: &mailSenderFake{}, Metrics: nopDeliveryRecorder{}, Sealer: nil, RecentAuthenticationTTL: 15 * time.Minute, Now: func() time.Time { return at }})
	require.NoError(t, err)
	page, appErr := service.ListMailDeliveries(context.Background(), invocation.New(principal, model.RequestMetadata{}), ListDeliveriesQuery{States: []model.MailDeliveryState{model.MailDeliveryQueued}, Limit: 20})
	require.NoError(t, appErr, "List() page=%#v err=%v lists=%d actions=%v", page, appErr, persistence.lists, authorizer.actions)
	require.Len(t, page.Items, 1, "List() page=%#v err=%v lists=%d actions=%v", page, appErr, persistence.lists, authorizer.actions)
	require.Equal(t, 1, persistence.lists, "List() page=%#v err=%v lists=%d actions=%v", page, appErr, persistence.lists, authorizer.actions)
	require.Equal(t, model.ActionMailView, authorizer.actions[0], "List() page=%#v err=%v lists=%d actions=%v", page, appErr, persistence.lists, authorizer.actions)
	require.Equal(t, 21, persistence.listLimit, "the read probes one past the page")
	require.False(t, page.HasMore)
	full, appErr := service.ListMailDeliveries(context.Background(), invocation.New(principal, model.RequestMetadata{}), ListDeliveriesQuery{Limit: 1})
	require.NoError(t, appErr)
	require.Len(t, full.Items, 1)
	require.False(t, full.HasMore, "an exactly full final page has no successor")
	persistence.listCopies = 1
	more, appErr := service.ListMailDeliveries(context.Background(), invocation.New(principal, model.RequestMetadata{}), ListDeliveriesQuery{Limit: 1})
	require.NoError(t, appErr)
	require.Len(t, more.Items, 1)
	require.True(t, more.HasMore)
	persistence.listCopies = 0
	authorizer.err = apperror.New("authorization.denied")
	_, appErr = service.GetMailDelivery(context.Background(), invocation.New(principal, model.RequestMetadata{}), delivery.ID)
	require.True(t, apperror.Is(appErr, "authorization.denied"), "denied Get() err=%v gets", appErr)
	require.Equal(t, 0, persistence.gets, "denied Get() err=%v gets", appErr)
	encoded, _ := json.Marshal(page)
	for _, forbidden := range []string{"ciphertext", "secret", "encrypted_payload", "operator@example.test"} {
		require.NotContains(t, string(encoded), forbidden, "safe page leaked")
	}
}

func TestMailOperatorMutationsRequireRecentSessionAndAuditSameDelivery(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	delivery := queuedMailDeliveryFixtureForApp(at.Add(-time.Minute))
	persistence := &mailStoreFake{delivery: delivery}
	auditor := &mailAuditFake{}
	service, err := NewOperations(OperationsDependencies{Mail: persistence, Users: mailUserStoreFake{user: mailTestUser(principal, at)}, Authorization: &mailAuthorizerFake{}, Audit: auditor, Attempts: &testLimiterFake{}, Renderer: mailRendererFake{}, Sender: &mailSenderFake{}, Metrics: nopDeliveryRecorder{}, Sealer: nil, RecentAuthenticationTTL: 15 * time.Minute, Now: func() time.Time { return at }})
	require.NoError(t, err)
	pat := principal
	pat.SessionID = ""
	pat.CredentialType = model.CredentialPersonalAccessToken
	pat.CredentialID = model.PrincipalCredentialID(model.NewPersonalAccessTokenID())
	pat.AuthenticationStrength, pat.AuthenticatedAt = "", time.Time{}
	_, appErr := service.CancelMailDelivery(context.Background(), invocation.New(pat, model.RequestMetadata{}), delivery.ID)
	require.True(t, apperror.Is(appErr, "authentication.token_invalid"), "Cancel(PAT) err=%v gets", appErr)
	require.Equal(t, 0, persistence.gets, "Cancel(PAT) err=%v gets", appErr)
	stale := principal
	stale.AuthenticatedAt = at.Add(-16 * time.Minute)
	_, appErr = service.CancelMailDelivery(context.Background(), invocation.New(stale, model.RequestMetadata{}), delivery.ID)
	require.True(t, apperror.Is(appErr, "authentication.reauthentication_required"), "Cancel(stale) err=%v gets", appErr)
	require.Equal(t, 0, persistence.gets, "Cancel(stale) err=%v gets", appErr)
	view, appErr := service.CancelMailDelivery(context.Background(), invocation.New(principal, model.RequestMetadata{}), delivery.ID)
	require.NoError(t, appErr, "Cancel() view=%#v err=%v mutates=%d audit", view, appErr, persistence.mutates)
	require.Equal(t, model.MailDeliveryCanceled, view.State, "Cancel() view=%#v err=%v mutates=%d audit", view, appErr, persistence.mutates)
	require.Equal(t, 1, persistence.mutates, "Cancel() view=%#v err=%v mutates=%d audit", view, appErr, persistence.mutates)
	require.Equal(t, 1, auditor.beginCalls, "Cancel() view=%#v err=%v mutates=%d audit", view, appErr, persistence.mutates)
}

func queuedMailDeliveryFixtureForApp(at time.Time) *model.MailDelivery {
	return &model.MailDelivery{ID: model.NewMailDeliveryID(), OccurrenceID: model.NewMailOccurrenceID(), JobID: model.NewJobID(), TargetUserID: model.NewUserID(), TemplateKey: model.MailTemplateSystemTest, TemplateDigest: strings.Repeat("a", 64), MaskedRecipient: "o***@example.test", State: model.MailDeliveryQueued, CreatedAt: at, UpdatedAt: at, MessageDate: at, Deadline: at.Add(24 * time.Hour), MessageID: "<mail." + model.NewID() + "@example.test>", EncryptedPayload: json.RawMessage(`{"ciphertext":"secret"}`), Revision: 1}
}

func TestMailServiceRejectsEnabledDeliveryWithoutSecretSealer(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	content := FrozenContent{Subject: "test", Text: "test"}
	_, err := NewOperations(OperationsDependencies{Mail: &mailStoreFake{}, Users: mailUserStoreFake{user: mailTestUser(principal, at)}, Authorization: &mailAuthorizerFake{}, Audit: &mailAuditFake{}, Attempts: &testLimiterFake{}, Renderer: mailRendererFake{content}, Sender: &mailSenderFake{enabled: true, from: Address{Address: "no-reply@example.test"}}, Metrics: nopDeliveryRecorder{}, Sealer: nil, RecentAuthenticationTTL: 15 * time.Minute, Now: func() time.Time { return at }})
	require.Error(t, err)
	require.Contains(t, err.Error(), "enabled mail requires secret sealing")
}

func TestMailDeliveryListCountsTheFirstPageThroughTheListedOptions(t *testing.T) {
	t.Parallel()
	delivery := &model.MailDelivery{ID: model.NewMailDeliveryID(), CreatedAt: model.TimeFromMillis(100)}
	persistence := &mailStoreFake{delivery: delivery, countTotal: 3}
	authorizer := &mailAuthorizerFake{}
	service := &Operations{deps: OperationsDependencies{Mail: persistence, Authorization: authorizer}}
	caller := invocation.New(mailTestPrincipal(time.Now()), model.RequestMetadata{})
	query := ListDeliveriesQuery{States: []model.MailDeliveryState{model.MailDeliveryFailed}, Limit: 10}
	first, err := service.ListMailDeliveries(context.Background(), caller, query)
	require.NoError(t, err)
	require.Equal(t, paging.Total{Counted: true, Total: 3}, first.Total)
	require.Equal(t, query.States, persistence.countOptions.States, "the count reads the list's filters")

	query.Order, query.BeforeAt, query.BeforeID = store.MailDeliveryOrderCreated, delivery.CreatedAt, delivery.ID
	persistence.counts = 0
	later, err := service.ListMailDeliveries(context.Background(), caller, query)
	require.NoError(t, err)
	require.Equal(t, paging.Total{}, later.Total)
	require.Zero(t, persistence.counts, "a later page is not counted")

	failed, err := service.CountFailedMailDeliveries(context.Background(), caller, 1000)
	require.NoError(t, err)
	require.Equal(t, 3, failed)
	require.Equal(t, store.MailDeliveryListOptions{States: []model.MailDeliveryState{model.MailDeliveryFailed}}, persistence.countOptions)
	require.Equal(t, []model.Action{model.ActionMailView, model.ActionMailView, model.ActionMailView}, authorizer.actions,
		"every read, the summary included, authorizes mail.view")
}

// testLimiterFake counts test mail per User the way the identity area's
// attempt accounting does: the attempt that exceeds maximum is limited.
type testLimiterFake struct{ counts map[model.UserID]int }

func (f *testLimiterFake) AccountMailTest(_ context.Context, userID model.UserID, _ time.Duration, maximum int) (bool, error) {
	if f.counts == nil {
		f.counts = map[model.UserID]int{}
	}
	f.counts[userID]++
	return f.counts[userID] > maximum, nil
}
