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
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// The fakes below stand in for the Stores the parent app package supplies;
// each records what the identity use cases ask of it. The authorizer, the
// auditors and the mail Composer are the production ones.

type accessAcademicPeriodStoreFake struct {
	store.AcademicPeriodStore

	period  *model.AcademicPeriod
	periods map[string]*model.AcademicPeriod
	gets    int
	err     error
}

func (s *accessAcademicPeriodStoreFake) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	s.gets++
	if s.err != nil {
		return nil, s.err
	}
	if period := s.periods[id.String()]; period != nil {
		return period, nil
	}
	if s.period == nil {
		return nil, store.NewErrNotFound("academic_period", "missing")
	}
	return s.period, nil
}

type accessInstitutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
	err         error
}

func (s *accessInstitutionStoreFake) Get(context.Context, model.InstitutionID) (*model.Institution, error) {
	return s.institution, s.err
}

func (s *accessInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, s.err
}

type accessClassStoreFake struct {
	store.ClassStore
	gets int
}

func (s *accessClassStoreFake) Get(context.Context, model.ClassID) (*model.Class, error) {
	s.gets++
	return nil, store.NewErrNotFound("class", "")
}

type accessUserStoreFake struct {
	store.UserStore
	user            *model.User
	match           store.UserVisibilityMatch
	matchVisibility store.UserVisibilityScope
	matchCalls      int
}

func (s *accessUserStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	if s.user == nil {
		return nil, store.NewErrNotFound("user", "")
	}
	return s.user, nil
}

func (s *accessUserStoreFake) MatchVisibility(_ context.Context, _ model.UserID, visibility store.UserVisibilityScope) (store.UserVisibilityMatch, error) {
	s.matchCalls++
	s.matchVisibility = visibility
	return s.match, nil
}

type accessClassMemberStoreFake struct{ store.ClassMemberStore }

type accessExamAuthoringStoreFake struct {
	store.ExamAuthoringStore
	exam *model.Exam
}

func (s *accessExamAuthoringStoreFake) Resolve(context.Context, model.ExamID) (*model.Exam, error) {
	return s.exam, nil
}

type accessExamSittingStoreFake struct {
	store.ExamSittingStore
	snapshot *store.ExamSittingSnapshot
}

type accessExamSubmissionStoreFake struct {
	store.ExamSubmissionStore
	authorization *store.ExamSubmissionAuthorization
}

func (s *accessExamSubmissionStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	if s.authorization == nil {
		return nil, store.NewErrNotFound("submission", "")
	}
	return s.authorization, nil
}

func (s *accessExamSittingStoreFake) Resolve(context.Context, model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if s.snapshot == nil {
		return nil, store.NewErrNotFound("exam_sitting", "")
	}
	return s.snapshot, nil
}

type accessRoleStoreFake struct {
	store.RoleStore
	roles []*model.Role
}

func (s *accessRoleStoreFake) GetByIDs(context.Context, []model.RoleID) ([]*model.Role, error) {
	return s.roles, nil
}

type accessRoleBindingStoreFake struct {
	store.RoleBindingStore
	bindings []*model.RoleBinding
	activeAt []time.Time
	lookup   func(time.Time) ([]*model.RoleBinding, error)
}

func (s *accessRoleBindingStoreFake) ListActiveByUser(_ context.Context, _ model.UserID, at time.Time) ([]*model.RoleBinding, error) {
	s.activeAt = append(s.activeAt, at)
	if s.lookup != nil {
		return s.lookup(at)
	}
	return s.bindings, nil
}

// decisionAudits keeps the authorization decisions a real Auditor records.
type decisionAudits struct {
	store.AuditStore
	mu     sync.Mutex
	events []*model.AuditEvent
}

func (a *decisionAudits) Save(_ context.Context, event *model.AuditEvent) (*model.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	saved := *event
	saved.ID = model.NewAuditEventID()
	a.events = append(a.events, &saved)
	return &saved, nil
}

type accessPolicyStoreFake struct {
	snapshot       *store.AccessPolicySnapshot
	blockers       []store.AccessPolicyBlocker
	preflightInput *store.AccessPolicyPreflight
	replaceInput   *store.AccessPolicyReplacement
	command        *store.CommandIdempotency
	result         *store.AccessPolicyReplacementResult
	preflightErr   error
	replaceErr     error
	// roleHolderMFAUsers are the Users HasRoleHolderMFARequirement reports as covered.
	roleHolderMFAUsers map[model.UserID]bool
	roleHolderMFAErr   error
	// getErr makes every read of the current policy fail.
	getErr error
}

func (s *accessPolicyStoreFake) Get(context.Context, int) (*store.AccessPolicySnapshot, error) {
	return s.snapshot, s.getErr
}

func (s *accessPolicyStoreFake) Preflight(_ context.Context, input *store.AccessPolicyPreflight) (*store.AccessPolicyPreflightResult, error) {
	s.preflightInput = input
	if s.preflightErr != nil {
		return nil, s.preflightErr
	}
	return &store.AccessPolicyPreflightResult{Blockers: append([]store.AccessPolicyBlocker(nil), s.blockers...)}, nil
}

func (s *accessPolicyStoreFake) HasRoleHolderMFARequirement(_ context.Context, userID model.UserID, _ time.Time) (bool, error) {
	return s.roleHolderMFAUsers[userID], s.roleHolderMFAErr
}

func (s *accessPolicyStoreFake) Replace(_ context.Context, input *store.AccessPolicyReplacement, command *store.CommandIdempotency) (*store.AccessPolicyReplacementResult, error) {
	s.replaceInput, s.command = input, command
	return s.result, s.replaceErr
}

type accessPolicyInstitutionFake struct {
	store.InstitutionStore
	institution *model.Institution
}

func (s *accessPolicyInstitutionFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

type systemAuditStoreFake struct {
	store.AuditStore
	saved *model.AuditEvent
}

func (f *systemAuditStoreFake) Save(_ context.Context, event *model.AuditEvent) (*model.AuditEvent, error) {
	copy := event.Clone()
	copy.PrepareCreate(model.NewAuditEventID(), time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC))
	if err := copy.Validate(); err != nil {
		return nil, err
	}
	f.saved = copy.Clone()
	return copy, nil
}

// seedAuthenticatedSession creates a local User and signs them in from the CLI.
func seedAuthenticatedSession(
	t *testing.T,
	service *Service,
) (*model.User, string) {
	t.Helper()
	password := "CorrectHorseBatteryStaple1!"
	suffix := model.NewID()
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{
			Username: "cluster-user-" + suffix,
			Email:    "cluster-user-" + suffix + "@example.edu",
		},
		Password: password,
	})
	require.NoError(t, err)
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID:    user.Username,
		Password:   password,
		ClientType: model.SessionClientCLI,
		Source:     "127.0.0.1",
	})
	require.NoError(t, err)
	require.NotNil(t, result.Tokens)
	require.NotEmpty(t, result.Tokens.AccessToken)
	return user, result.Tokens.AccessToken
}

func oldSealerKeyID(t *testing.T, key string) string {
	t.Helper()
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: 1})
	require.NoError(t, err)
	return sealer.PrimaryKeyID()
}

// mailSenderFake is the mail transport port. The Composer reads From once
// for each message, right after rendering it, so From counts the rendered
// messages and places "prepare-mail" in events when a test shares its log. A
// failing sender answers an invalid From address, which makes mail
// preparation fail.
type mailSenderFake struct {
	enabled bool
	from    mail.Address
	events  *[]string

	mu       sync.Mutex
	failing  bool
	rendered int
}

func (s *mailSenderFake) Enabled() bool { return s.enabled }

func (s *mailSenderFake) From() mail.Address {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rendered++
	if s.events != nil {
		*s.events = append(*s.events, "prepare-mail")
	}
	if s.failing {
		return mail.Address{}
	}
	return s.from
}

func (*mailSenderFake) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, nil
}

// fail makes every later mail preparation fail.
func (s *mailSenderFake) fail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = true
}

// renders returns how many messages were rendered.
func (s *mailSenderFake) renders() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rendered
}

// openedMail opens a delivery sealed with mailTestSealer, so a test reads the
// message its recipient would get.
func openedMail(t *testing.T, delivery *model.MailDelivery) mail.Outbound {
	t.Helper()
	opened, err := mail.OpenDelivery(mailTestSealer(t), delivery)
	require.NoError(t, err)
	return opened
}

func mailTestSealer(t testing.TB) *secretseal.Sealer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	value, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	return value
}

func mailTestPrincipal(at time.Time) model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewSessionCredentialID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: at}
}

func mailTestUser(principal model.Principal, at time.Time) *model.User {
	user := &model.User{Email: "operator@example.test", DisplayName: "Operator", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(principal.UserID, at.Add(-time.Hour))
	return user
}

// userSettingsSessionPrincipal is the interactive Session principal that the
// authentication tests sign a User in as.
func userSettingsSessionPrincipal(at time.Time) model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at,
	}
}

// accessWith builds the real authorizer and auditor the options describe and
// places what they do in events: "grants" for each read of the caller's Role
// Bindings, "decision:<action>:<status>" for a recorded decision,
// "audit-begin:<action>" for a mutation attempt and "audit-<status>" for its
// completion. A nil events drops them.
func accessWith(t testing.TB, events *[]string, options accesstest.Options) *accesstest.Fixture {
	t.Helper()
	options.Observe = func(event string) {
		if events != nil {
			*events = append(*events, event)
		}
	}
	return accesstest.New(t, options)
}

// grantedAccess builds the real authorizer and auditor over the grants alone.
func grantedAccess(t testing.TB, events *[]string, grants ...accesstest.Grant) *accesstest.Fixture {
	t.Helper()
	return accessWith(t, events, accesstest.Options{Grants: grants})
}

// institutionGrant grants the actions Institution-wide.
func institutionGrant(userID model.UserID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions}
}

// withAccess gives the Service the fixture's authorizer and mutation auditor,
// as composition gives it the shared ones.
func withAccess(service *Service, fixture *accesstest.Fixture) *Service {
	service.authorizer, service.auditor = fixture.Authorizer, fixture.Auditor.MutationAudit()
	return service
}

// recordedAttempt is one mutation attempt the real auditor recorded, with its
// Parameters and PriorState decoded from their stored JSON.
type recordedAttempt struct {
	*model.AuditEvent
	parameters map[string]any
	prior      any
}

// value is the "value" parameter the attempt was begun with.
func (a recordedAttempt) value() any {
	return a.parameters["value"]
}

// recordedAttempts returns the fixture's mutation attempts, in order.
func recordedAttempts(t testing.TB, fixture *accesstest.Fixture) []recordedAttempt {
	t.Helper()
	var attempts []recordedAttempt
	for _, event := range fixture.Audits.Attempts() {
		parameters, _ := decodedAuditData(t, event.Parameters).(map[string]any)
		attempts = append(attempts, recordedAttempt{AuditEvent: event, parameters: parameters, prior: decodedAuditData(t, event.PriorState)})
	}
	return attempts
}

// decodedAuditData decodes stored audit JSON; nothing stored decodes to nil.
func decodedAuditData(t testing.TB, raw json.RawMessage) any {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}

// newTestMail builds the production mail Composer over the real renderer. An
// enabled sender queues rendered mail; a disabled one makes the Composer
// record suppressed mail without rendering it.
func newTestMail(t testing.TB, enabled bool) (*mail.Composer, *mailSenderFake) {
	t.Helper()
	sender := &mailSenderFake{enabled: enabled, from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}}
	composer, err := mail.NewComposer(mail.ComposerDependencies{
		Renderer: mailtest.NewRenderer(t),
		Sender:   sender,
		Sealer:   mailTestSealer(t),
	})
	require.NoError(t, err)
	return composer, sender
}

// mailRecipient is an active User the real Composer can address.
func mailRecipient(id model.UserID) *model.User {
	user := &model.User{Username: "recipient", Email: "recipient@example.edu", DisplayName: "Recipient", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(id, time.UnixMilli(1))
	return user
}

// deploymentCapabilities builds the production DeploymentCapabilities that
// report the snapshot: its providers are live in the registry and durable
// mail rides an enabled, healthy sender.
func deploymentCapabilities(snapshot AccessPolicyCapabilitySnapshot) *DeploymentCapabilities {
	source := accessPolicyProviderSourceFake{providers: map[string]ExternalIdentityProvider{}}
	for _, provider := range snapshot.Providers {
		source.descriptors = append(source.descriptors, provider.Descriptor)
		source.providers[provider.Descriptor.ID] = capabilityProvider{descriptor: provider.Descriptor, autoProvision: provider.AutoProvision}
	}
	health := accessPolicyMailHealthFake{code: mail.HealthHealthy}
	return &DeploymentCapabilities{Providers: source, Mail: accessPolicyMailSenderFake{enabled: snapshot.DurableMail}, Health: health, MFAEnabled: snapshot.MFA}
}

// noCapabilities is a deployment with no providers, durable mail or MFA.
func noCapabilities() *DeploymentCapabilities {
	return deploymentCapabilities(AccessPolicyCapabilitySnapshot{})
}

// capabilityProvider is a configured provider as the capability snapshot
// sees it; it never signs anyone in.
type capabilityProvider struct {
	descriptor    model.ExternalAuthenticationProvider
	autoProvision bool
}

func (p capabilityProvider) Descriptor() model.ExternalAuthenticationProvider { return p.descriptor }
func (p capabilityProvider) AutoProvision() bool                              { return p.autoProvision }
func (capabilityProvider) Begin(context.Context, ExternalProviderBeginRequest) (*ExternalProviderBeginResponse, error) {
	return nil, errors.New("capability provider does not sign in")
}
func (capabilityProvider) State(model.ExternalAuthenticationCallback) (string, error) {
	return "", errors.New("capability provider does not sign in")
}
func (capabilityProvider) Complete(context.Context, ExternalProviderCompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	return nil, errors.New("capability provider does not sign in")
}

// hookedPasswordHasher is the production hasher whose Hash returns what hash
// returns, for password work failures and fixed encodings.
func hookedPasswordHasher(t testing.TB, hash func(context.Context, string) (string, error)) *PasswordHasher {
	t.Helper()
	hasher, err := NewPasswordHasher(PasswordHasherDependencies{Settings: testPasswordPolicy()})
	require.NoError(t, err)
	hasher.hashHook = hash
	return hasher
}

// fixedPasswordHasher hashes every password to encoded, appending
// "hash-password" to events when they are given.
func fixedPasswordHasher(t testing.TB, events *[]string, encoded string, err error) *PasswordHasher {
	t.Helper()
	return hookedPasswordHasher(t, func(context.Context, string) (string, error) {
		if events != nil {
			*events = append(*events, "hash-password")
		}
		return encoded, err
	})
}
