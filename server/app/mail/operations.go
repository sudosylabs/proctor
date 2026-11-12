// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// An operator may send testRateLimitMaximum test messages in each
// testRateLimitWindow.
const (
	testRateLimitWindow  = time.Hour
	testRateLimitMaximum = 3
)

// DeliveryView is the safe operator projection of one mail delivery: its
// identity, template, masked recipient, state and timings, never its
// recipient address or content.
type DeliveryView struct {
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

// DeliveryPage is one page of mail deliveries; HasMore reports that
// another page follows the last item. The first page also carries their
// total (see paging.Total).
type DeliveryPage struct {
	Items   []DeliveryView
	HasMore bool
	paging.Total
}

// ListDeliveriesQuery lists deliveries newest first by Order's instant (last
// update when Order is empty), or oldest first when Ascending; BeforeAt and
// BeforeID continue past the previous page's last delivery in that order.
type ListDeliveriesQuery struct {
	States        []model.MailDeliveryState
	TemplateKeys  []model.MailTemplateKey
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Order         store.MailDeliveryOrder
	Ascending     bool
	BeforeAt      time.Time
	BeforeID      model.MailDeliveryID
	Limit         int
}

// InstitutionAuthorization authorizes a mail operator Action on the singleton
// Institution. It is not access.Authorizer.AuthorizeInstitution because an
// Institution that cannot be read is presented as mail.unavailable here.
type InstitutionAuthorization struct {
	Authorizer   *access.Authorizer
	Institutions store.InstitutionStore
}

// AuthorizeInstitution reads the Institution and authorizes action on it.
func (a InstitutionAuthorization) AuthorizeInstitution(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
) (model.Resource, error) {
	if a.Authorizer == nil || a.Institutions == nil {
		return model.Resource{}, apperror.New("mail.unavailable")
	}

	institution, err := a.Institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return model.Resource{}, apperror.New("mail.unavailable").Wrap(err)
	}

	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err = a.Authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return model.Resource{}, err
	}

	return resource, nil
}

// institutionAuthorizer authorizes an Action on the installation's singleton
// Institution and returns that Resource for the caller's audit. Production
// passes InstitutionAuthorization; the tests substitute a fake.
type institutionAuthorizer interface {
	AuthorizeInstitution(context.Context, invocation.Invocation, model.Action) (model.Resource, error)
}

// testLimiter counts one operator test mail for a User and reports whether
// that User has now sent more than maximum within window. Production passes
// the identity area's attempt accounting, which this package cannot import.
type testLimiter interface {
	AccountMailTest(ctx context.Context, userID model.UserID, window time.Duration, maximum int) (bool, error)
}

type auditPreparer interface {
	PrepareTest(context.Context, invocation.Invocation, model.Resource, model.MailDeliveryID) (*model.AuditEvent, error)
	PrepareControl(context.Context, invocation.Invocation, model.Resource, *model.MailDelivery, string) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// OperationsDependencies are the collaborators of Operations;
// NewOperations validates them once and Operations keeps them in deps.
// RekeyJobs, RekeyAudit and Wake serve payload-key rotation and the Job
// wake-up after a queued delivery; without them those use cases answer that
// mail is unavailable.
type OperationsDependencies struct {
	Mail                    store.MailStore
	Users                   store.UserStore
	Authorization           institutionAuthorizer
	Audit                   auditPreparer
	Attempts                testLimiter
	Renderer                Renderer
	Sender                  Sender
	Metrics                 MetricsReader
	Sealer                  *secretseal.Sealer
	RecentAuthenticationTTL time.Duration
	Now                     func() time.Time
	// InstitutionName supplies the institution display name to mail copy.
	InstitutionName func() string
	RekeyJobs       store.JobStore
	RekeyAudit      access.MutationAuditor
	Wake            func()
}

// Operations holds the operator mail use cases: delivery listing, inspection,
// cancellation and retry, metrics, test mail, and payload-key rotation. A nil
// Operations answers that mail operations are unavailable.
type Operations struct {
	deps OperationsDependencies

	rekeyJobs  store.JobStore
	rekeyAudit access.MutationAuditor

	composer *Composer

	wake func()
}

// NewOperations builds the operator mail service and the Composer its test
// mail is prepared with. It refuses a missing collaborator, and an enabled
// sender without secret sealing.
func NewOperations(deps OperationsDependencies) (*Operations, error) {
	if deps.Mail == nil ||
		deps.Users == nil ||
		deps.Authorization == nil ||
		deps.Audit == nil ||
		deps.Attempts == nil ||
		deps.Renderer == nil ||
		deps.Sender == nil ||
		deps.Metrics == nil ||
		deps.Now == nil ||
		deps.RecentAuthenticationTTL <= 0 {
		return nil, errors.New("mail service dependencies are invalid")
	}

	if deps.Sender.Enabled() && deps.Sealer == nil {
		return nil, errors.New("enabled mail requires secret sealing")
	}

	composer, err := NewComposer(ComposerDependencies{
		Renderer:    deps.Renderer,
		Sender:      deps.Sender,
		Sealer:      deps.Sealer,
		Institution: deps.InstitutionName,
	})
	if err != nil {
		return nil, err
	}

	return &Operations{
		deps:       deps,
		rekeyJobs:  deps.RekeyJobs,
		rekeyAudit: deps.RekeyAudit,
		composer:   composer,
		wake:       deps.Wake,
	}, nil
}

// GetMailMetrics returns the current mail delivery metrics snapshot.
func (s *Operations) GetMailMetrics(ctx context.Context, inv invocation.Invocation) (MetricsSnapshot, error) {
	if s == nil {
		return MetricsSnapshot{}, apperror.New("mail.unavailable")
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionMailView); err != nil {
		return MetricsSnapshot{}, err
	}
	return s.deps.Metrics.Snapshot(), nil
}

// SendTestMail queues a rate-limited test message to the recently
// authenticated caller's own verified address, with audit.
func (s *Operations) SendTestMail(ctx context.Context, inv invocation.Invocation) (DeliveryView, error) {
	if s == nil {
		return DeliveryView{}, apperror.New("mail.unavailable")
	}

	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return DeliveryView{}, access.InvalidTokenAppError()
	}
	if !principal.IsRecentlyAuthenticated(s.deps.Now(), s.deps.RecentAuthenticationTTL) {
		return DeliveryView{}, apperror.New("authentication.reauthentication_required")
	}

	institutionResource, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionMailManage)
	if err != nil {
		return DeliveryView{}, err
	}
	if err = s.checkTestRateLimit(ctx, principal.UserID); err != nil {
		return DeliveryView{}, err
	}
	if !s.deps.Sender.Enabled() || s.deps.Sealer == nil {
		return DeliveryView{}, apperror.New("mail.unavailable")
	}

	user, err := s.deps.Users.Get(ctx, principal.UserID)
	if err != nil {
		return DeliveryView{}, storeErrors.Translate(err)
	}
	if user == nil || user.Validate() != nil || user.ID != principal.UserID {
		return DeliveryView{}, apperror.New("mail.unavailable")
	}
	if !user.EmailVerified {
		return DeliveryView{}, apperror.New("mail.recipient_unverified")
	}
	if !user.IsActive() {
		return DeliveryView{}, apperror.New("mail.recipient_ineligible")
	}

	now := model.TimeUTC(s.deps.Now())
	prepared, err := s.composer.PrepareOperatorTest(NoticePreparation{Recipient: user, At: now})
	if err != nil {
		return DeliveryView{}, apperror.New("mail.unavailable").Wrap(err)
	}

	audit, err := s.deps.Audit.PrepareTest(ctx, inv, institutionResource, prepared.Delivery.ID)
	if err != nil {
		return DeliveryView{}, err
	}

	created, err := s.deps.Mail.EnqueueTest(ctx, &store.MailTestEnqueue{
		Occurrence: prepared.Occurrence,
		Delivery:   prepared.Delivery,
		Job:        prepared.Job,
		AuditEvent: audit,
	})
	if err != nil {
		return DeliveryView{}, storeErrors.Translate(err)
	}
	if s.wake != nil {
		s.wake()
	}

	return deliveryView(created), nil
}

// GetMailDelivery returns one mail delivery.
func (s *Operations) GetMailDelivery(
	ctx context.Context,
	inv invocation.Invocation,
	id model.MailDeliveryID,
) (DeliveryView, error) {
	if s == nil {
		return DeliveryView{}, apperror.New("mail.unavailable")
	}

	principal := inv.Principal()
	if principal.Validate() != nil {
		return DeliveryView{}, access.InvalidTokenAppError()
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionMailView); err != nil {
		return DeliveryView{}, err
	}
	if !id.IsValid() {
		return DeliveryView{}, apperror.New("request.invalid")
	}

	delivery, err := s.deps.Mail.GetDelivery(ctx, id)
	if err != nil {
		return DeliveryView{}, storeErrors.Translate(err)
	}

	return deliveryView(delivery), nil
}

// ListMailDeliveries lists one page of mail deliveries.
func (s *Operations) ListMailDeliveries(
	ctx context.Context,
	inv invocation.Invocation,
	query ListDeliveriesQuery,
) (DeliveryPage, error) {
	if s == nil {
		return DeliveryPage{}, apperror.New("mail.unavailable")
	}
	if inv.Principal().Validate() != nil {
		return DeliveryPage{}, access.InvalidTokenAppError()
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionMailView); err != nil {
		return DeliveryPage{}, err
	}
	order := query.Order
	if order == "" {
		order = store.MailDeliveryOrderUpdated
	}
	if query.Limit < 1 || query.Limit > paging.MaximumLimit || !order.IsValid() {
		return DeliveryPage{}, apperror.New("mail.query.invalid")
	}

	options := store.MailDeliveryListOptions{
		States:        query.States,
		TemplateKeys:  query.TemplateKeys,
		CreatedAfter:  query.CreatedAfter,
		CreatedBefore: query.CreatedBefore,
		Order:         order,
		Ascending:     query.Ascending,
		BeforeAt:      query.BeforeAt,
		BeforeID:      query.BeforeID,
		Limit:         query.Limit + 1,
	}
	deliveries, err := s.deps.Mail.ListDeliveries(ctx, options)
	if err != nil {
		return DeliveryPage{}, listError(err)
	}

	page := DeliveryPage{HasMore: len(deliveries) > query.Limit}
	deliveries = deliveries[:min(len(deliveries), query.Limit)]
	page.Items = make([]DeliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		page.Items = append(page.Items, deliveryView(delivery))
	}

	total, consistent, err := paging.CountFirst(query.BeforeID.IsZero(), len(page.Items), func(maximum int) (int, error) {
		return s.deps.Mail.CountDeliveries(ctx, options, maximum)
	})
	if err != nil {
		return DeliveryPage{}, listError(err)
	}
	if !consistent {
		return DeliveryPage{}, apperror.New("mail.unavailable")
	}

	page.Total = total
	return page, nil
}

// CountFailedMailDeliveries counts the deliveries in the failed terminal
// state, authorized and audited exactly as ListMailDeliveries is, stopping
// at maximum. Suppressed and canceled deliveries are policy and operator
// outcomes rather than failures, so they are not counted. The administration
// counts read it.
func (s *Operations) CountFailedMailDeliveries(ctx context.Context, inv invocation.Invocation, maximum int) (int, error) {
	if s == nil {
		return 0, apperror.New("mail.unavailable")
	}
	if inv.Principal().Validate() != nil {
		return 0, access.InvalidTokenAppError()
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionMailView); err != nil {
		return 0, err
	}

	total, err := s.deps.Mail.CountDeliveries(
		ctx,
		store.MailDeliveryListOptions{States: []model.MailDeliveryState{model.MailDeliveryFailed}},
		maximum,
	)
	if err != nil {
		return 0, storeErrors.Translate(err)
	}

	return total, nil
}

// listError maps a delivery list or count failure: invalid filters are
// the caller's query, anything else is the mail Store's.
func listError(err error) error {
	var invalid *store.ErrInvalidInput
	if errors.As(err, &invalid) {
		return apperror.New("mail.query.invalid").Wrap(err)
	}
	return storeErrors.Translate(err)
}

// CancelMailDelivery cancels one mail delivery, with audit.
func (s *Operations) CancelMailDelivery(
	ctx context.Context,
	inv invocation.Invocation,
	id model.MailDeliveryID,
) (DeliveryView, error) {
	if s == nil {
		return DeliveryView{}, apperror.New("mail.unavailable")
	}
	return s.mutate(ctx, inv, "cancel", id, false, s.deps.Mail.CancelDelivery)
}

// RetryMailDelivery retries one mail delivery, with audit.
func (s *Operations) RetryMailDelivery(
	ctx context.Context,
	inv invocation.Invocation,
	id model.MailDeliveryID,
) (DeliveryView, error) {
	if s == nil {
		return DeliveryView{}, apperror.New("mail.unavailable")
	}
	return s.mutate(ctx, inv, "retry", id, true, s.deps.Mail.RetryDelivery)
}

func (s *Operations) mutate(
	ctx context.Context,
	inv invocation.Invocation,
	operation string,
	id model.MailDeliveryID,
	retry bool,
	apply func(context.Context, *store.MailDeliveryMutation) (*model.MailDelivery, error),
) (DeliveryView, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return DeliveryView{}, access.InvalidTokenAppError()
	}
	if !principal.IsRecentlyAuthenticated(s.deps.Now(), s.deps.RecentAuthenticationTTL) {
		return DeliveryView{}, apperror.New("authentication.reauthentication_required")
	}

	institutionResource, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionMailManage)
	if err != nil {
		return DeliveryView{}, err
	}
	if !id.IsValid() {
		return DeliveryView{}, apperror.New("request.invalid")
	}

	delivery, err := s.deps.Mail.GetDelivery(ctx, id)
	if err != nil {
		return DeliveryView{}, storeErrors.Translate(err)
	}

	now := model.TimeUTC(s.deps.Now())
	if retry {
		relevance, relevanceErr := evaluateDeliveryRelevance(ctx, delivery)
		if relevanceErr != nil || relevance != deliveryRelevant {
			return DeliveryView{}, apperror.New("mail.conflict").Wrap(relevanceErr)
		}
		if _, transitionErr := delivery.OperatorRetry(now); transitionErr != nil {
			return DeliveryView{}, apperror.New("mail.conflict").Wrap(transitionErr)
		}
	}

	auditID, err := s.deps.Audit.PrepareControl(ctx, inv, institutionResource, delivery, operation)
	if err != nil {
		return DeliveryView{}, err
	}

	updated, err := apply(ctx, &store.MailDeliveryMutation{
		ID:               delivery.ID,
		ExpectedRevision: delivery.Revision,
		AuditEventID:     auditID,
		AuditAt:          now,
	})
	if err != nil {
		_ = s.deps.Audit.Fail(ctx, auditID, "mail.conflict")
		return DeliveryView{}, storeErrors.Translate(err)
	}

	if retry && s.wake != nil {
		s.wake()
	}

	return deliveryView(updated), nil
}

func (s *Operations) checkTestRateLimit(ctx context.Context, userID model.UserID) error {
	limited, err := s.deps.Attempts.AccountMailTest(ctx, userID, testRateLimitWindow, testRateLimitMaximum)
	if err != nil {
		return apperror.New("mail.unavailable").Wrap(err)
	}
	if limited {
		return apperror.New("mail.test.rate_limited")
	}

	return nil
}

func deliveryView(delivery *model.MailDelivery) DeliveryView {
	if delivery == nil {
		return DeliveryView{}
	}
	return DeliveryView{
		ID:                 delivery.ID,
		OccurrenceID:       delivery.OccurrenceID,
		TargetUserID:       delivery.TargetUserID,
		TargetInvitationID: delivery.TargetInvitationID,
		TemplateKey:        delivery.TemplateKey,
		TemplateDigest:     delivery.TemplateDigest,
		MaskedRecipient:    delivery.MaskedRecipient,
		State:              delivery.State,
		CreatedAt:          delivery.CreatedAt,
		UpdatedAt:          delivery.UpdatedAt,
		MessageDate:        delivery.MessageDate,
		Deadline:           delivery.Deadline,
		MessageID:          delivery.MessageID,
		AttemptCount:       delivery.AttemptCount,
		AcceptedAt:         delivery.AcceptedAt,
		FailedAt:           delivery.FailedAt,
		PublicFailureCode:  delivery.PublicFailureCode,
	}
}

var storeErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Conflict:    "mail.conflict",
	Unavailable: "mail.unavailable",
}
