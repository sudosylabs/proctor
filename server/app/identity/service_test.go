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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// The fakes below stand in for the collaborators the parent app package
// supplies; each records what the identity use cases ask of it.

type institutionAuditorFake struct {
	events    *[]string
	beginID   string
	beginIDs  []string
	failCode  string
	action    model.Action
	resource  model.Resource
	resources []model.Resource
	scopeType model.RoleScopeType
	scopeID   string
	subjects  []model.UserID
	values    []map[string]any
}

func (a *institutionAuditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	if !attempt.Subject.IsZero() {
		a.subjects = append(a.subjects, attempt.Subject)
	}
	if attempt.ScopeType != "" || attempt.ScopeID != "" {
		a.scopeType, a.scopeID = attempt.ScopeType, attempt.ScopeID
	}

	*a.events = append(*a.events, "audit-begin")
	a.values = append(a.values, attempt.Value)
	a.action, a.resource = attempt.Action, attempt.Resource
	a.resources = append(a.resources, attempt.Resource)
	if len(a.beginIDs) > 0 {
		id := a.beginIDs[0]
		a.beginIDs = a.beginIDs[1:]
		return model.AuditEventID(id), nil
	}
	return model.AuditEventID(a.beginID), nil
}

func (a *institutionAuditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	*a.events = append(*a.events, "audit-fail")
	a.failCode = code
	return nil
}

type mutationAttemptAuditorFake struct {
	events   *[]string
	beginID  string
	beginIDs []string
	beginErr error
	failErr  error
	failID   string
	failCode string
	attempt  access.Mutation
	attempts []access.Mutation
}

// Begin records the whole Mutation; the event names whether it carried an
// explicit scope, as the two positional entry points once did.
func (a *mutationAttemptAuditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	if attempt.ScopeType != "" {
		*a.events = append(*a.events, "begin-at-scope")
	} else {
		*a.events = append(*a.events, "begin")
	}

	a.attempt = attempt
	a.attempts = append(a.attempts, attempt)
	return model.AuditEventID(a.nextBeginID()), a.beginErr
}

func (a *mutationAttemptAuditorFake) nextBeginID() string {
	if len(a.beginIDs) == 0 {
		return a.beginID
	}
	id := a.beginIDs[0]
	a.beginIDs = a.beginIDs[1:]
	return id
}

func (a *mutationAttemptAuditorFake) Fail(_ context.Context, id model.AuditEventID, code string) error {
	*a.events = append(*a.events, "fail")
	a.failID = id.String()
	a.failCode = code
	return a.failErr
}

type accessAcademicPeriodStoreFake struct {
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

type accessDecisionAuditFake struct{}

func (accessDecisionAuditFake) RecordAuthorizationDecision(context.Context, model.Principal, model.Action, model.Resource, model.RoleScopeType, string, model.RequestMetadata, bool) error {
	return nil
}

func (accessDecisionAuditFake) RecordUserSearchDecision(context.Context, model.Principal, model.Resource, model.RoleScopeType, string, model.RequestMetadata, bool) error {
	return nil
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
}

func (s *accessPolicyStoreFake) Get(context.Context, int) (*store.AccessPolicySnapshot, error) {
	return s.snapshot, nil
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

type accessPolicyAuthorizationFake struct {
	resourceAuthorizer
	action model.Action
}

func (a *accessPolicyAuthorizationFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, _ model.Resource) error {
	a.action = action
	return nil
}

type accessPolicyCapabilitiesFake struct {
	snapshot AccessPolicyCapabilitySnapshot
}

func (c *accessPolicyCapabilitiesFake) Snapshot() AccessPolicyCapabilitySnapshot { return c.snapshot }

type accessPolicyAuditFake struct {
	beginID  string
	beginErr error
	failErr  error
	attempt  access.Mutation
	failID   string
	failCode string
}

func (a *accessPolicyAuditFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	// This fake keeps no subject, so it refuses a Mutation that names one
	// instead of dropping it.
	// It stands in for access.MutationAuditAdapter, which records it.
	if !attempt.Subject.IsZero() {
		return "", apperror.New("audit.internal")
	}

	a.attempt = attempt
	return model.AuditEventID(a.beginID), a.beginErr
}

func (a *accessPolicyAuditFake) Fail(_ context.Context, id model.AuditEventID, code string) error {
	a.failID, a.failCode = id.String(), code
	return a.failErr
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

type mailRekeyAuditFake struct {
	beginID  string
	attempt  access.Mutation
	failID   string
	failCode string
	failErr  error
	events   *[]string
}

func (f *mailRekeyAuditFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	// This fake keeps no subject, so it refuses a Mutation that names one
	// instead of dropping it.
	// It stands in for access.MutationAuditAdapter, which records it.
	if !attempt.Subject.IsZero() {
		return "", apperror.New("audit.internal")
	}

	if attempt.ScopeType == "" && attempt.ScopeID == "" {
		return "", errors.New("unscoped mail rekey audit is forbidden")
	}

	if f.events != nil {
		*f.events = append(*f.events, "audit")
	}
	f.attempt = attempt
	return model.AuditEventID(f.beginID), nil
}

func (f *mailRekeyAuditFake) Fail(_ context.Context, id model.AuditEventID, code string) error {
	f.failID, f.failCode = id.String(), code
	return f.failErr
}

func oldSealerKeyID(t *testing.T, key string) string {
	t.Helper()
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: 1})
	require.NoError(t, err)
	return sealer.PrimaryKeyID()
}

// mailAuthorizerFake records each operator authorization of the key rings:
// the action and the Resource it was asked to authorize.
type mailAuthorizerFake struct {
	resourceAuthorizer
	actions   []model.Action
	resources []model.Resource
	err       error
}

func (a *mailAuthorizerFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource) error {
	a.actions = append(a.actions, action)
	a.resources = append(a.resources, resource)
	return a.err
}

type mailRendererFake struct{ content mail.FrozenContent }

func (r mailRendererFake) Render(mail.RenderRequest) (mail.FrozenContent, error) {
	return r.content, nil
}

type mailSenderFake struct {
	enabled   bool
	from      mail.Address
	messages  []mail.Outbound
	err       error
	outcome   mail.TransportOutcome
	afterSend func()
}

func (s *mailSenderFake) Enabled() bool { return s.enabled }

func (s *mailSenderFake) From() mail.Address { return s.from }

func (s *mailSenderFake) Send(_ context.Context, message mail.Outbound) (mail.TransportOutcome, error) {
	s.messages = append(s.messages, message)
	if s.afterSend != nil {
		s.afterSend()
	}
	if s.err != nil {
		return s.outcome, s.err
	}
	return mail.TransportUnknown, nil
}

func mailTestSealer(t *testing.T) *secretseal.Sealer {
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

type DirectMailPreparation struct {
	Recipient    *model.User
	OccurrenceID model.MailOccurrenceID
	Kind         model.MailOccurrenceKind
	TemplateKey  model.MailTemplateKey
	ActionURL    string
	At           time.Time
	Deadline     time.Time
	JobType      model.JobType
}

// accountAuthorizerFake authorizes every account-state and email action and
// records the decision for the call-order assertions of those tests.
type accountAuthorizerFake struct {
	resourceAuthorizer
	events *[]string
}

func (a *accountAuthorizerFake) Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error {
	*a.events = append(*a.events, "authorize-manage")
	return nil
}

func (a *accountAuthorizerFake) AuthorizeAccountStateManage(context.Context, invocation.Invocation, model.UserID) error {
	*a.events = append(*a.events, "authorize-manage")
	return nil
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
