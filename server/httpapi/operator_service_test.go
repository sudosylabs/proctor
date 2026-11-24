// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// jobFixture is the real job.Operations the Job handlers call: a real Engine
// over a Job Store that records every request, with the real authorizer and
// auditor deciding for the operator.
type jobFixture struct {
	access  *accesstest.Fixture
	jobs    *jobLedger
	service *job.Operations
}

// newJobFixture builds the fixture over job, a Job of an operator-visible
// type that operators may cancel and retry when it has failed. The operator
// holds actions across the Institution.
func newJobFixture(t *testing.T, operator model.UserID, record *model.Job, actions ...model.Action) *jobFixture {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	fixture := &jobFixture{
		access: accesstest.New(t, accesstest.Options{Grants: []accesstest.Grant{institutionWide(operator, actions...)}}),
		jobs:   &jobLedger{job: record},
	}
	descriptor := job.NewDescriptor(record.Type, jobNoopHandler{})
	descriptor.Cancelable = true
	descriptor.ExplicitRetryStatuses = []model.JobStatus{model.JobStatusFailed}
	engine, err := job.New(job.Dependencies{
		Store: fixture.jobs, Descriptors: []job.Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: logger, Policy: job.Policy{BackstopInterval: time.Hour},
	})
	require.NoError(t, err)
	fixture.service, err = job.NewOperations(job.OperationsDependencies{
		Jobs: engine,
		Authorization: job.InstitutionAuthorization{
			Authorizer: fixture.access.Authorizer, Institutions: identityInstitution{institution: fixture.access.Institution},
		},
		Audit:       fixture.access.Auditor,
		Users:       &directoryUserStore{byID: map[model.UserID]*model.User{}},
		ExamExports: struct{ store.ExamExportStore }{},
		Now:         time.Now,
	})
	require.NoError(t, err)
	return fixture
}

// failedOperatorJob is a failed profile-picture Job created at.
func failedOperatorJob(t *testing.T, at time.Time) *model.Job {
	t.Helper()
	queued, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, []byte(`{}`), "operations", at, at, 3)
	require.NoError(t, err)
	running, err := queued.Start(at)
	require.NoError(t, err)
	failed, err := running.Fail("job.timeout", at)
	require.NoError(t, err)
	return failed
}

type jobNoopHandler struct{}

func (jobNoopHandler) Run(context.Context, job.Execution) job.Outcome {
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: json.RawMessage(`{}`)}
}

// jobLedger holds one Job and records what reached it.
type jobLedger struct {
	store.JobStore
	job          *model.Job
	lists        []store.JobListOptions
	gets         []model.JobID
	attemptLists []store.JobAttemptListOptions
	cancels      []*store.JobMutation
	retries      []*store.JobMutation
}

// lastList is the options of the most recent List.
func (s *jobLedger) lastList(t *testing.T) store.JobListOptions {
	t.Helper()
	require.NotEmpty(t, s.lists, "the request never reached the Job Store")
	return s.lists[len(s.lists)-1]
}

func (s *jobLedger) List(_ context.Context, options store.JobListOptions) ([]*model.Job, error) {
	s.lists = append(s.lists, options)
	return []*model.Job{s.job}, nil
}

func (s *jobLedger) Count(context.Context, store.JobListOptions, int) (int, error) { return 1, nil }

func (s *jobLedger) Get(_ context.Context, id model.JobID) (*model.Job, error) {
	s.gets = append(s.gets, id)
	if id != s.job.ID {
		return nil, store.NewErrNotFound("job", id.String())
	}
	copied := *s.job
	return &copied, nil
}

func (s *jobLedger) ListAttemptsPage(_ context.Context, options store.JobAttemptListOptions) ([]model.JobAttempt, error) {
	s.attemptLists = append(s.attemptLists, options)
	return nil, nil
}

func (s *jobLedger) Cancel(_ context.Context, mutation *store.JobMutation) (*model.Job, error) {
	s.cancels = append(s.cancels, mutation)
	return s.job, nil
}

func (s *jobLedger) Retry(_ context.Context, mutation *store.JobMutation) (*model.Job, error) {
	s.retries = append(s.retries, mutation)
	return s.job, nil
}

// mailFixture is the real mail.Operations the mail handlers call, over a
// mail Store that records every request and a real payload sealer holding
// one retiring key beside the primary.
type mailFixture struct {
	access     *accesstest.Fixture
	operator   *model.User
	deliveries *mailDeliveryLedger
	rekeyJobs  *mailRekeyJobs
	metrics    *mailMetricsReads
	limiter    *mailTestAccounting
	sealer     *secretseal.Sealer
	retiringID string
	wakes      int
	now        time.Time
	service    *mail.Operations
}

// mailFixtureNow is the service clock; Sessions are recent against it.
var mailFixtureNow = time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)

// newMailFixture builds the fixture for the operator principal, who holds
// mail.view, mail.manage and mail.rekey across the Institution and whose own
// verified address receives test mail.
func newMailFixture(t *testing.T, principal model.Principal) *mailFixture {
	t.Helper()
	primaryKey := base64.StdEncoding.EncodeToString([]byte("new-mail-rekey-key-material-0001"))
	retiringKey := base64.StdEncoding.EncodeToString([]byte("old-mail-rekey-key-material-0001"))
	sealer, err := secretseal.New(secretseal.Settings{
		EncryptionKey: primaryKey, DecryptionKeys: []string{retiringKey}, MaximumPlaintext: secretseal.MaximumPlaintextBytes,
	})
	require.NoError(t, err)
	retiring, err := secretseal.New(secretseal.Settings{EncryptionKey: retiringKey, MaximumPlaintext: 1})
	require.NoError(t, err)

	operator := &model.User{
		Username: "operator", Email: "operator@example.test", DisplayName: "Operator",
		EmailVerified: true, Locale: "en", Timezone: "UTC",
	}
	operator.PrepareCreate(principal.UserID, mailFixtureNow.Add(-time.Hour))
	users := &mailRecipientUsers{directoryUserStore: &directoryUserStore{byID: map[model.UserID]*model.User{}}}
	users.add(operator)

	fixture := &mailFixture{
		access: accesstest.New(t, accesstest.Options{Grants: []accesstest.Grant{institutionWide(principal.UserID,
			model.ActionMailView, model.ActionMailManage, model.ActionMailRekey)}, Users: users.directoryUserStore}),
		operator:   operator,
		deliveries: &mailDeliveryLedger{},
		rekeyJobs:  &mailRekeyJobs{},
		metrics:    &mailMetricsReads{},
		limiter:    &mailTestAccounting{},
		sealer:     sealer,
		retiringID: retiring.PrimaryKeyID(),
		now:        mailFixtureNow,
	}
	fixture.service, err = mail.NewOperations(mail.OperationsDependencies{
		Mail:  fixture.deliveries,
		Users: users,
		Authorization: mail.InstitutionAuthorization{
			Authorizer: fixture.access.Authorizer, Institutions: identityInstitution{institution: fixture.access.Institution},
		},
		Audit:                   fixture.access.Auditor,
		Attempts:                fixture.limiter,
		Renderer:                httpMailRenderer(t),
		Sender:                  &identityMailSender{enabled: true},
		Metrics:                 fixture.metrics,
		Sealer:                  sealer,
		RecentAuthenticationTTL: 15 * time.Minute,
		Now:                     func() time.Time { return fixture.now },
		RekeyJobs:               fixture.rekeyJobs,
		Wake:                    func() { fixture.wakes++ },
	})
	require.NoError(t, err)
	return fixture
}

// mailOperatorPrincipal is a recent multi-factor Web Session at the
// fixture's clock, the assurance rekeying requires.
func mailOperatorPrincipal() model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationMultiFactor, AuthenticatedAt: mailFixtureNow,
		MFACompletedAt: model.OptionalTimeFrom(mailFixtureNow), ClientType: model.SessionClientWeb,
	}
}

// mailRecipientUsers names the Users it holds to any viewer.
type mailRecipientUsers struct {
	*directoryUserStore
}

func (s *mailRecipientUsers) ListVisibleDisplayNames(_ context.Context, ids []model.UserID, _ store.UserVisibilityScope) ([]store.UserDisplayName, error) {
	names := make([]store.UserDisplayName, 0, len(ids))
	for _, id := range ids {
		if user, ok := s.byID[id]; ok {
			names = append(names, store.UserDisplayName{UserID: id, DisplayName: user.DisplayName})
		}
	}
	return names, nil
}

// mailDeliveryLedger serves the deliveries a test lists and records what
// reached the mail Store.
type mailDeliveryLedger struct {
	store.MailStore
	listed      []*model.MailDelivery
	keyState    *store.MailKeyState
	lists       []store.MailDeliveryListOptions
	gets        []model.MailDeliveryID
	enqueued    []*store.MailTestEnqueue
	cancels     []*store.MailDeliveryMutation
	retries     []*store.MailDeliveryMutation
	rekeyStarts []*store.MailRekeyStart
}

// lastList is the options of the most recent delivery listing.
func (s *mailDeliveryLedger) lastList(t *testing.T) store.MailDeliveryListOptions {
	t.Helper()
	require.NotEmpty(t, s.lists, "the request never reached the mail Store")
	return s.lists[len(s.lists)-1]
}

func (s *mailDeliveryLedger) EnqueueTest(_ context.Context, input *store.MailTestEnqueue) (*model.MailDelivery, error) {
	s.enqueued = append(s.enqueued, input)
	return input.Delivery, nil
}

func (s *mailDeliveryLedger) GetDelivery(_ context.Context, id model.MailDeliveryID) (*model.MailDelivery, error) {
	s.gets = append(s.gets, id)
	for _, delivery := range s.listed {
		if delivery.ID == id {
			return delivery, nil
		}
	}
	return nil, store.NewErrNotFound("mail_delivery", id.String())
}

func (s *mailDeliveryLedger) ListDeliveries(_ context.Context, options store.MailDeliveryListOptions) ([]*model.MailDelivery, error) {
	s.lists = append(s.lists, options)
	return slices.Clone(s.listed), nil
}

func (s *mailDeliveryLedger) CountDeliveries(_ context.Context, _ store.MailDeliveryListOptions, maximum int) (int, error) {
	return min(len(s.listed), maximum), nil
}

func (s *mailDeliveryLedger) CancelDelivery(_ context.Context, input *store.MailDeliveryMutation) (*model.MailDelivery, error) {
	s.cancels = append(s.cancels, input)
	return s.GetDelivery(context.Background(), input.ID)
}

func (s *mailDeliveryLedger) RetryDelivery(_ context.Context, input *store.MailDeliveryMutation) (*model.MailDelivery, error) {
	s.retries = append(s.retries, input)
	return s.GetDelivery(context.Background(), input.ID)
}

func (s *mailDeliveryLedger) InspectKeyState(context.Context) (*store.MailKeyState, error) {
	return s.keyState, nil
}

func (s *mailDeliveryLedger) StartRekey(_ context.Context, input *store.MailRekeyStart) (*store.MailRekeyOperation, error) {
	s.rekeyStarts = append(s.rekeyStarts, input)
	return &store.MailRekeyOperation{
		JobID: input.Job.ID, PrimaryKeyID: input.PrimaryKeyID, RetiringKeyID: input.RetiringKeyID, CreatedAt: input.Job.CreatedAt,
	}, nil
}

// mailRekeyJobs serves the rekey Jobs a test sets and records each read.
type mailRekeyJobs struct {
	store.JobStore
	jobs []*model.Job
	gets []model.JobID
}

func (s *mailRekeyJobs) Get(_ context.Context, id model.JobID) (*model.Job, error) {
	s.gets = append(s.gets, id)
	for _, record := range s.jobs {
		if record.ID == id {
			copied := *record
			return &copied, nil
		}
	}
	return nil, store.NewErrNotFound("job", id.String())
}

// mailMetricsReads is the delivery metrics reader; it counts its reads.
type mailMetricsReads struct{ reads int }

func (r *mailMetricsReads) Snapshot() mail.MetricsSnapshot {
	r.reads++
	return mail.MetricsSnapshot{HealthCode: mail.HealthHealthy}
}

// mailTestAccounting admits every test mail and records whose it was.
type mailTestAccounting struct{ accounted []model.UserID }

func (a *mailTestAccounting) AccountMailTest(_ context.Context, userID model.UserID, _ time.Duration, _ int) (bool, error) {
	a.accounted = append(a.accounted, userID)
	return false, nil
}

// succeededMailRekeyJob is a finished rekey from retiringKeyID to
// primaryKeyID that processed nine payloads, re-encrypting eight, with its
// last checkpoint at four of nine.
func succeededMailRekeyJob(t *testing.T, primaryKeyID, retiringKeyID string, at time.Time) *model.Job {
	t.Helper()
	command, err := json.Marshal(mail.RekeyCommand{PrimaryKeyID: primaryKeyID, RetiringKeyID: retiringKeyID})
	require.NoError(t, err)
	queued, err := model.NewJob(model.NewJobID(), model.JobTypeMailRekey, command, "mail-rekey:test", at, at, 11)
	require.NoError(t, err)
	running, err := queued.Start(at.Add(time.Second))
	require.NoError(t, err)
	checkpoint, err := json.Marshal(mail.RekeyCheckpoint{AfterKind: store.MailRekeyTargetDelivery,
		AfterID: model.NewMailDeliveryID().String(), Processed: 4, Reencrypted: 3})
	require.NoError(t, err)
	running, err = running.UpdateProgress(&model.JobProgress{Current: 4, Total: 9, Stage: "reencrypting"}, checkpoint, at.Add(2*time.Second))
	require.NoError(t, err)
	result, err := json.Marshal(mail.RekeyResult{PrimaryKeyID: primaryKeyID, RetiringKeyID: retiringKeyID,
		Processed: 9, Reencrypted: 8, RetirementSafe: true})
	require.NoError(t, err)
	succeeded, err := running.Succeed(result, at.Add(3*time.Second))
	require.NoError(t, err)
	return succeeded
}
