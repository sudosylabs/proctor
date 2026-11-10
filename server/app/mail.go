// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	mailDeliverySealingPurpose = mail.DeliverySealingPurpose
	mailTestRateLimitWindow    = time.Hour
	mailTestRateLimitMaximum   = 3
)

type MailDeliveryView struct {
	ID                                          model.MailDeliveryID
	OccurrenceID                                model.MailOccurrenceID
	TargetUserID                                model.UserID
	TargetInvitationID                          model.InvitationID
	TemplateKey                                 model.MailTemplateKey
	TemplateDigest                              string
	MaskedRecipient                             string
	State                                       model.MailDeliveryState
	CreatedAt, UpdatedAt, MessageDate, Deadline time.Time
	MessageID                                   string
	AttemptCount                                int
	AcceptedAt, FailedAt                        model.OptionalTime
	PublicFailureCode                           string
}

// MailDeliveryPage is one page of mail deliveries; HasMore reports that
// another page follows the last item. The first page also carries their
// total (see paging.Total).
type MailDeliveryPage struct {
	Items   []MailDeliveryView
	HasMore bool
	paging.Total
}

type ListMailDeliveriesQuery struct {
	States          []model.MailDeliveryState
	TemplateKeys    []model.MailTemplateKey
	CreatedAfter    time.Time
	CreatedBefore   time.Time
	BeforeCreatedAt time.Time
	BeforeID        model.MailDeliveryID
	Limit           int
}

type mailStore interface {
	EnqueueTest(context.Context, *store.MailTestEnqueue) (*model.MailDelivery, error)
	GetDelivery(context.Context, model.MailDeliveryID) (*model.MailDelivery, error)
	ListDeliveries(context.Context, store.MailDeliveryListOptions) ([]*model.MailDelivery, error)
	CountDeliveries(context.Context, store.MailDeliveryListOptions, int) (int, error)
	CancelDelivery(context.Context, *store.MailDeliveryMutation) (*model.MailDelivery, error)
	RetryDelivery(context.Context, *store.MailDeliveryMutation) (*model.MailDelivery, error)
}

type mailUserStore interface {
	Get(context.Context, model.UserID) (*model.User, error)
}

type mailInstitutionStore interface {
	GetSingleton(context.Context) (*model.Institution, error)
}

type mailAuthorizationAdapter struct {
	authorization *access.Authorizer
	institutions  mailInstitutionStore
}

func (a mailAuthorizationAdapter) AuthorizeInstitution(ctx context.Context, invocation invocation.Invocation, action model.Action) (model.Resource, error) {
	if a.authorization == nil || a.institutions == nil {
		return model.Resource{}, apperror.New("mail.unavailable")
	}
	institution, err := a.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return model.Resource{}, apperror.New("mail.unavailable").Wrap(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err = a.authorization.Authorize(ctx, invocation, action, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
}

type mailAuditPreparer interface {
	PrepareTest(context.Context, invocation.Invocation, model.Resource, model.MailDeliveryID) (*model.AuditEvent, error)
	PrepareControl(context.Context, invocation.Invocation, model.Resource, *model.MailDelivery, string) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// mailServiceDependencies are the collaborators of mailService;
// newMailService validates them once and mailService keeps them in deps.
type mailServiceDependencies struct {
	Mail                    mailStore
	Users                   mailUserStore
	Authorization           institutionAuthorizer
	Audit                   mailAuditPreparer
	Attempts                *identity.AttemptAccounting
	Renderer                mail.Renderer
	Sender                  mail.Sender
	Metrics                 MailMetricsReader
	Sealer                  *secretseal.Sealer
	RecentAuthenticationTTL time.Duration
	Now                     func() time.Time
	// InstitutionName supplies the institution display name to mail copy.
	InstitutionName func() string
}

type mailService struct {
	deps mailServiceDependencies

	rekey      mailRekeyStarter
	keyState   mailKeyInspector
	rekeyJobs  mailRekeyJobReader
	rekeyAudit access.MutationAuditor

	composer *mail.Composer

	wake func()
}

func newDirectMailPreparer(renderer mail.Renderer, sender mail.Sender, sealer *secretseal.Sealer, institution func() string) (*mail.Composer, error) {
	return mail.NewComposer(mail.ComposerDependencies{Renderer: renderer, Sender: sender, Sealer: sealer, Institution: institution})
}

// institutionNameCacheLifetime bounds how stale the institution name in newly
// prepared mail may be after an administrator renames the institution.
const institutionNameCacheLifetime = time.Minute

// institutionNameRetryDelay spaces read attempts while the institution cannot
// be read, such as before bootstrap.
const institutionNameRetryDelay = 10 * time.Second

// institutionNameCache supplies the institution display name to mail copy. The
// read happens outside the lock so one slow read never queues every message;
// a failure keeps the last known name, and with none copy names "your
// institution".
type institutionNameCache struct {
	institutions mailInstitutionStore
	now          func() time.Time

	mu        sync.Mutex
	name      string
	nextFetch time.Time
}

func (c *institutionNameCache) Name() string {
	if c == nil || c.institutions == nil {
		return ""
	}
	c.mu.Lock()
	now := c.now()
	if now.Before(c.nextFetch) {
		name := c.name
		c.mu.Unlock()
		return name
	}
	c.nextFetch = now.Add(institutionNameRetryDelay)
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	institution, err := c.institutions.GetSingleton(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && institution != nil {
		c.name, c.nextFetch = strings.TrimSpace(institution.DisplayName), c.now().Add(institutionNameCacheLifetime)
	}
	return c.name
}

func newMailService(deps mailServiceDependencies) (*mailService, error) {
	if deps.Mail == nil || deps.Users == nil || deps.Authorization == nil || deps.Audit == nil || deps.Attempts == nil || deps.Renderer == nil || deps.Sender == nil || deps.Metrics == nil || deps.Now == nil || deps.RecentAuthenticationTTL <= 0 {
		return nil, errors.New("mail service dependencies are invalid")
	}
	if deps.Sender.Enabled() && deps.Sealer == nil {
		return nil, errors.New("enabled mail requires secret sealing")
	}
	composer, err := newDirectMailPreparer(deps.Renderer, deps.Sender, deps.Sealer, deps.InstitutionName)
	if err != nil {
		return nil, err
	}
	rekey, _ := deps.Mail.(mailRekeyStarter)
	keyState, _ := deps.Mail.(mailKeyInspector)
	return &mailService{deps: deps, rekey: rekey, keyState: keyState, composer: composer}, nil
}

func (s *mailService) GetMailMetrics(ctx context.Context, invocation invocation.Invocation) (MailMetricsSnapshot, error) {
	if s == nil {
		return MailMetricsSnapshot{}, apperror.New("mail.unavailable")
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailView); err != nil {
		return MailMetricsSnapshot{}, err
	}
	return s.deps.Metrics.Snapshot(), nil
}

func (s *mailService) SendTestMail(ctx context.Context, invocation invocation.Invocation) (MailDeliveryView, error) {
	if s == nil {
		return MailDeliveryView{}, apperror.New("mail.unavailable")
	}
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return MailDeliveryView{}, access.InvalidTokenAppError()
	}
	if !principal.IsRecentlyAuthenticated(s.deps.Now(), s.deps.RecentAuthenticationTTL) {
		return MailDeliveryView{}, apperror.New("authentication.reauthentication_required")
	}
	institutionResource, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailManage)
	if err != nil {
		return MailDeliveryView{}, err
	}
	if err = s.checkTestRateLimit(ctx, principal.UserID); err != nil {
		return MailDeliveryView{}, err
	}
	if !s.deps.Sender.Enabled() || s.deps.Sealer == nil {
		return MailDeliveryView{}, apperror.New("mail.unavailable")
	}
	user, err := s.deps.Users.Get(ctx, principal.UserID)
	if err != nil {
		return MailDeliveryView{}, mailStoreErrors.Translate(err)
	}
	if user == nil || user.Validate() != nil || user.ID != principal.UserID {
		return MailDeliveryView{}, apperror.New("mail.unavailable")
	}
	if !user.EmailVerified {
		return MailDeliveryView{}, apperror.New("mail.recipient_unverified")
	}
	if !user.IsActive() {
		return MailDeliveryView{}, apperror.New("mail.recipient_ineligible")
	}
	now := model.TimeUTC(s.deps.Now())
	prepared, err := s.composer.PrepareOperatorTest(mail.NoticePreparation{Recipient: user, At: now})
	if err != nil {
		return MailDeliveryView{}, apperror.New("mail.unavailable").Wrap(err)
	}
	audit, err := s.deps.Audit.PrepareTest(ctx, invocation, institutionResource, prepared.Delivery.ID)
	if err != nil {
		return MailDeliveryView{}, err
	}
	created, err := s.deps.Mail.EnqueueTest(ctx, &store.MailTestEnqueue{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job, AuditEvent: audit})
	if err != nil {
		return MailDeliveryView{}, mailStoreErrors.Translate(err)
	}
	if s.wake != nil {
		s.wake()
	}
	return mailDeliveryView(created), nil
}

func (s *mailService) GetMailDelivery(ctx context.Context, invocation invocation.Invocation, id model.MailDeliveryID) (MailDeliveryView, error) {
	if s == nil {
		return MailDeliveryView{}, apperror.New("mail.unavailable")
	}
	principal := invocation.Principal()
	if principal.Validate() != nil {
		return MailDeliveryView{}, access.InvalidTokenAppError()
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailView); err != nil {
		return MailDeliveryView{}, err
	}
	if !id.IsValid() {
		return MailDeliveryView{}, apperror.New("request.invalid")
	}
	delivery, err := s.deps.Mail.GetDelivery(ctx, id)
	if err != nil {
		return MailDeliveryView{}, mailStoreErrors.Translate(err)
	}
	return mailDeliveryView(delivery), nil
}

func (s *mailService) ListMailDeliveries(ctx context.Context, invocation invocation.Invocation, query ListMailDeliveriesQuery) (MailDeliveryPage, error) {
	if s == nil {
		return MailDeliveryPage{}, apperror.New("mail.unavailable")
	}
	if invocation.Principal().Validate() != nil {
		return MailDeliveryPage{}, access.InvalidTokenAppError()
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailView); err != nil {
		return MailDeliveryPage{}, err
	}
	if query.Limit < 1 || query.Limit > paging.MaximumLimit {
		return MailDeliveryPage{}, apperror.New("mail.query.invalid")
	}
	options := store.MailDeliveryListOptions{
		States: query.States, TemplateKeys: query.TemplateKeys,
		CreatedAfter: query.CreatedAfter, CreatedBefore: query.CreatedBefore,
		BeforeCreatedAt: query.BeforeCreatedAt, BeforeID: query.BeforeID, Limit: query.Limit + 1,
	}
	deliveries, err := s.deps.Mail.ListDeliveries(ctx, options)
	if err != nil {
		return MailDeliveryPage{}, mailListError(err)
	}
	page := MailDeliveryPage{HasMore: len(deliveries) > query.Limit}
	deliveries = deliveries[:min(len(deliveries), query.Limit)]
	page.Items = make([]MailDeliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		page.Items = append(page.Items, mailDeliveryView(delivery))
	}
	total, consistent, err := paging.CountFirst(query.BeforeID.IsZero(), len(page.Items), func(maximum int) (int, error) {
		return s.deps.Mail.CountDeliveries(ctx, options, maximum)
	})
	if err != nil {
		return MailDeliveryPage{}, mailListError(err)
	}
	if !consistent {
		return MailDeliveryPage{}, apperror.New("mail.unavailable")
	}
	page.Total = total
	return page, nil
}

// countFailedMailDeliveries counts the deliveries in the failed terminal
// state, authorized and audited exactly as ListMailDeliveries is, stopping
// at maximum. Suppressed and canceled deliveries are policy and operator
// outcomes rather than failures, so they are not counted.
func (s *mailService) countFailedMailDeliveries(ctx context.Context, invocation invocation.Invocation, maximum int) (int, error) {
	if s == nil {
		return 0, apperror.New("mail.unavailable")
	}
	if invocation.Principal().Validate() != nil {
		return 0, access.InvalidTokenAppError()
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailView); err != nil {
		return 0, err
	}
	total, err := s.deps.Mail.CountDeliveries(ctx, store.MailDeliveryListOptions{States: []model.MailDeliveryState{model.MailDeliveryFailed}}, maximum)
	if err != nil {
		return 0, mailStoreErrors.Translate(err)
	}
	return total, nil
}

// mailListError maps a delivery list or count failure: invalid filters are
// the caller's query, anything else is the mail Store's.
func mailListError(err error) error {
	var invalid *store.ErrInvalidInput
	if errors.As(err, &invalid) {
		return apperror.New("mail.query.invalid").Wrap(err)
	}
	return mailStoreErrors.Translate(err)
}

func (s *mailService) CancelMailDelivery(ctx context.Context, invocation invocation.Invocation, id model.MailDeliveryID) (MailDeliveryView, error) {
	if s == nil {
		return MailDeliveryView{}, apperror.New("mail.unavailable")
	}
	return s.mutate(ctx, invocation, "cancel", id, false, s.deps.Mail.CancelDelivery)
}

func (s *mailService) RetryMailDelivery(ctx context.Context, invocation invocation.Invocation, id model.MailDeliveryID) (MailDeliveryView, error) {
	if s == nil {
		return MailDeliveryView{}, apperror.New("mail.unavailable")
	}
	return s.mutate(ctx, invocation, "retry", id, true, s.deps.Mail.RetryDelivery)
}

func (s *mailService) mutate(ctx context.Context, invocation invocation.Invocation, operation string, id model.MailDeliveryID, retry bool, apply func(context.Context, *store.MailDeliveryMutation) (*model.MailDelivery, error)) (MailDeliveryView, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return MailDeliveryView{}, access.InvalidTokenAppError()
	}
	if !principal.IsRecentlyAuthenticated(s.deps.Now(), s.deps.RecentAuthenticationTTL) {
		return MailDeliveryView{}, apperror.New("authentication.reauthentication_required")
	}
	institutionResource, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailManage)
	if err != nil {
		return MailDeliveryView{}, err
	}
	if !id.IsValid() {
		return MailDeliveryView{}, apperror.New("request.invalid")
	}
	delivery, err := s.deps.Mail.GetDelivery(ctx, id)
	if err != nil {
		return MailDeliveryView{}, mailStoreErrors.Translate(err)
	}
	now := model.TimeUTC(s.deps.Now())
	if retry {
		relevance, relevanceErr := evaluateMailDeliveryRelevance(ctx, delivery)
		if relevanceErr != nil || relevance != mailDeliveryRelevant {
			return MailDeliveryView{}, apperror.New("mail.conflict").Wrap(relevanceErr)
		}
		if _, transitionErr := delivery.OperatorRetry(now); transitionErr != nil {
			return MailDeliveryView{}, apperror.New("mail.conflict").Wrap(transitionErr)
		}
	}
	auditID, err := s.deps.Audit.PrepareControl(ctx, invocation, institutionResource, delivery, operation)
	if err != nil {
		return MailDeliveryView{}, err
	}
	updated, err := apply(ctx, &store.MailDeliveryMutation{ID: delivery.ID, ExpectedRevision: delivery.Revision, AuditEventID: auditID, AuditAt: now})
	if err != nil {
		_ = s.deps.Audit.Fail(ctx, auditID, "mail.conflict")
		return MailDeliveryView{}, mailStoreErrors.Translate(err)
	}
	if retry && s.wake != nil {
		s.wake()
	}
	return mailDeliveryView(updated), nil
}

func (s *mailService) checkTestRateLimit(ctx context.Context, userID model.UserID) error {
	limited, err := s.deps.Attempts.Account(ctx, identity.AttemptPurposeMailTest, "", mailTestRateLimitWindow, identity.IdentityAttemptLimit(userID.String(), mailTestRateLimitMaximum))
	if err != nil {
		return apperror.New("mail.unavailable").Wrap(err)
	}
	if limited {
		return apperror.New("mail.test.rate_limited")
	}
	return nil
}

func mailDeliveryView(delivery *model.MailDelivery) MailDeliveryView {
	if delivery == nil {
		return MailDeliveryView{}
	}
	return MailDeliveryView{ID: delivery.ID, OccurrenceID: delivery.OccurrenceID, TargetUserID: delivery.TargetUserID, TargetInvitationID: delivery.TargetInvitationID, TemplateKey: delivery.TemplateKey, TemplateDigest: delivery.TemplateDigest, MaskedRecipient: delivery.MaskedRecipient, State: delivery.State, CreatedAt: delivery.CreatedAt, UpdatedAt: delivery.UpdatedAt, MessageDate: delivery.MessageDate, Deadline: delivery.Deadline, MessageID: delivery.MessageID, AttemptCount: delivery.AttemptCount, AcceptedAt: delivery.AcceptedAt, FailedAt: delivery.FailedAt, PublicFailureCode: delivery.PublicFailureCode}
}

var mailStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Conflict:    "mail.conflict",
	Unavailable: "mail.unavailable",
}
