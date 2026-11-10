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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// securityAuditRecord is one security audit event as *access.Auditor
// received it: who acted for which resource and scope, with the request
// identity, and how the event ended.
type securityAuditRecord struct {
	id         model.AuditEventID
	status     model.AuditStatus
	actor      model.UserID
	sessionID  model.SessionID
	target     model.UserID
	clientType model.SessionClientType
	action     model.Action
	resource   model.Resource
	scopeType  model.RoleScopeType
	scopeID    string
	metadata   model.RequestMetadata
	parameters any
	prior      any
	errorCode  string
	result     any
}

// operation is the "operation" parameter the event was begun with.
func (r *securityAuditRecord) operation() string {
	parameters, _ := r.parameters.(map[string]any)
	operation, _ := parameters["operation"].(string)
	return operation
}

// securityAuditorFake stands in for *access.Auditor. It keeps every security
// audit event it received, in order, with the request metadata, and the
// terminal outcome of each, so tests assert what became durable rather than
// which calls were made.
type securityAuditorFake struct {
	events      []*securityAuditRecord
	pending     map[string]*securityAuditRecord
	terminal    []securityAuditRecord
	completeErr error
	// trace, when set, receives audit_begin and audit_success or audit_fail
	// in call order, interleaved with the other fakes' events.
	trace *[]string
}

func (f *securityAuditorFake) appendTrace(event string) {
	if f.trace != nil {
		*f.trace = append(*f.trace, event)
	}
}

func (f *securityAuditorFake) begin(record *securityAuditRecord) *model.AuditEvent {
	f.appendTrace("audit_begin")
	if f.pending == nil {
		f.pending = map[string]*securityAuditRecord{}
	}
	record.id, record.status = model.NewAuditEventID(), model.AuditStatusAttempt
	f.pending[record.id.String()] = record
	f.events = append(f.events, record)
	return &model.AuditEvent{ID: record.id}
}

func (f *securityAuditorFake) BeginLocalLogin(_ context.Context, userID model.UserID, clientType model.SessionClientType, metadata model.RequestMetadata) (*model.AuditEvent, error) {
	return f.begin(&securityAuditRecord{actor: userID, target: userID, clientType: clientType, metadata: metadata}), nil
}

func (f *securityAuditorFake) BeginAccountActionAtScope(_ context.Context, actor access.AccountAuditActor, action model.Action,
	resource model.Resource, scopeType model.RoleScopeType, scopeID string, metadata model.RequestMetadata, parameters any,
) (*model.AuditEvent, error) {
	return f.begin(&securityAuditRecord{actor: actor.UserID, sessionID: actor.SessionID, target: model.UserID(resource.ID),
		clientType: actor.ClientType, action: action, resource: resource, scopeType: scopeType, scopeID: scopeID,
		metadata: metadata, parameters: parameters}), nil
}

func (f *securityAuditorFake) BeginCriticalActionForSubject(_ context.Context, principal model.Principal, subject model.UserID,
	action model.Action, resource model.Resource, metadata model.RequestMetadata, parameters, prior any,
) (*model.AuditEvent, error) {
	return f.begin(&securityAuditRecord{actor: principal.UserID, sessionID: principal.SessionID, target: subject,
		clientType: principal.ClientType, action: action, resource: resource, metadata: metadata,
		parameters: parameters, prior: prior}), nil
}

func (f *securityAuditorFake) PrepareCriticalAction(_ context.Context, principal model.Principal, action model.Action,
	resource model.Resource, metadata model.RequestMetadata, parameters, prior any,
) (*model.AuditEvent, error) {
	return nil, errors.New("unexpected prepared audit draft")
}

func (f *securityAuditorFake) CompleteCriticalAction(_ context.Context, auditID model.AuditEventID, status model.AuditStatus, errorCode string, result any) (*model.AuditEvent, error) {
	if status == model.AuditStatusSuccess {
		f.appendTrace("audit_success")
	} else {
		f.appendTrace("audit_fail")
	}
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	record, ok := f.pending[auditID.String()]
	if !ok {
		return nil, errors.New("unknown audit attempt")
	}
	delete(f.pending, auditID.String())
	record.status, record.errorCode, record.result = status, errorCode, result
	f.terminal = append(f.terminal, *record)
	return &model.AuditEvent{ID: auditID, Status: status, ErrorCode: errorCode}, nil
}

func (f *securityAuditorFake) RecordLocalLoginFailure(_ context.Context, target model.UserID,
	clientType model.SessionClientType, metadata model.RequestMetadata, errorCode string,
) error {
	record := securityAuditRecord{id: model.NewAuditEventID(), status: model.AuditStatusFail, target: target,
		clientType: clientType, metadata: metadata, errorCode: errorCode}
	f.events = append(f.events, &record)
	f.terminal = append(f.terminal, record)
	return nil
}

func (f *securityAuditorFake) BeginAuthentication(_ context.Context, userID model.UserID, method, providerID string,
	clientType model.SessionClientType, metadata model.RequestMetadata, institutionID model.InstitutionID,
) (*model.AuditEvent, error) {
	return f.begin(&securityAuditRecord{actor: userID, target: userID, clientType: clientType,
		action: model.ActionAuthenticationExternalLogin, resource: model.Resource{Type: model.ResourceUser, ID: userID.String()},
		scopeType: model.RoleScopeInstitution, scopeID: institutionID.String(), metadata: metadata,
		parameters: map[string]any{"provider": providerID, "method": method}}), nil
}

func (f *securityAuditorFake) RecordExternalAuthenticationFailure(_ context.Context, providerID, method string,
	metadata model.RequestMetadata, institutionID model.InstitutionID, errorCode string,
) error {
	record := securityAuditRecord{id: model.NewAuditEventID(), status: model.AuditStatusFail,
		action: model.ActionAuthenticationExternalLogin, resource: model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		scopeType: model.RoleScopeInstitution, scopeID: institutionID.String(), metadata: metadata,
		parameters: map[string]any{"provider": providerID, "method": method}, errorCode: errorCode}
	f.events = append(f.events, &record)
	f.terminal = append(f.terminal, record)
	return nil
}

// externalFailureCodes returns the error codes of the failed external
// sign-ins recorded, in order.
func (f *securityAuditorFake) externalFailureCodes() []string {
	codes := []string{}
	for _, record := range f.terminal {
		if record.action == model.ActionAuthenticationExternalLogin && record.status == model.AuditStatusFail {
			codes = append(codes, record.errorCode)
		}
	}
	return codes
}

// withOperation returns the events begun with operation, in order.
func (f *securityAuditorFake) withOperation(operation string) []*securityAuditRecord {
	var matched []*securityAuditRecord
	for _, record := range f.events {
		if record.operation() == operation {
			matched = append(matched, record)
		}
	}
	return matched
}

// lastFailure returns the last event that ended as a failure, or nil.
func (f *securityAuditorFake) lastFailure() *securityAuditRecord {
	for index := len(f.terminal) - 1; index >= 0; index-- {
		if f.terminal[index].status == model.AuditStatusFail {
			return &f.terminal[index]
		}
	}
	return nil
}

func newLocalLoginAuditFixture(t *testing.T, mfa store.MFAStore) (*Service, *authenticationStoreFake, *securityAuditorFake, *model.User) {
	t.Helper()
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationServiceWithPorts(t, persistence, newAuthenticationCacheFake(), mfa,
		discardPersonalAccessTokenStore{}, model.NewCredentialToken, time.Now)
	audit := &securityAuditorFake{}
	service.securityAudit = audit
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "audited-user", Email: "audited-user@example.edu"}, Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	return service, persistence, audit, user
}

func localLoginInvocation() invocation.Invocation {
	return invocation.New(model.Principal{}, model.RequestMetadata{RequestID: "request-login", IPAddress: "192.0.2.10", UserAgent: "login-test"})
}

func TestLocalLoginSuccessIsDurablyAuditedWithActorAndSession(t *testing.T) {
	t.Parallel()
	service, persistence, audit, user := newLocalLoginAuditFixture(t, &mfaApplicationStoreFake{})

	result, err := service.Login(context.Background(), localLoginInvocation(), LoginCommand{
		LoginID: user.Email, Password: "CorrectHorseBatteryStaple1!", ClientType: model.SessionClientWeb, Source: "192.0.2.10:1",
	})
	require.NoError(t, err)
	// The application begins the attempt; Session creation completes it in
	// the same transaction, so the application records no terminal outcome.
	require.Len(t, audit.pending, 1, "pending")
	require.Empty(t, audit.terminal, "pending")
	for id, record := range audit.pending {
		require.Equal(t, user.ID, record.actor)
		require.Equal(t, model.SessionClientWeb, record.clientType)
		require.Equal(t, "192.0.2.10", record.metadata.IPAddress)
		require.Equal(t, "login-test", record.metadata.UserAgent)
		require.Equal(t, "request-login", record.metadata.RequestID)
		projection, ok := persistence.completedAudits[id]
		require.True(t, ok, "Session creation completion = %#v, session %s", persistence.completedAudits, result.Session.ID)
		require.Equal(t, result.Session.ID.String(), projection["id"], "Session creation completion = %#v, session %s", persistence.completedAudits, result.Session.ID)
	}
}

func TestLocalLoginFailuresAreDurablyAudited(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		mfa         *mfaApplicationStoreFake
		mfaCode     string
		loginID     string
		password    string
		code        string
		knownTarget bool
	}{
		{name: "wrong password", mfa: &mfaApplicationStoreFake{}, loginID: "audited-user@example.edu",
			password: "incorrect password", code: "authentication.credentials_invalid", knownTarget: true},
		{name: "unknown account", mfa: &mfaApplicationStoreFake{}, loginID: "nobody@example.edu",
			password: "CorrectHorseBatteryStaple1!", code: "authentication.credentials_invalid"},
		{name: "mfa failure", mfa: &mfaApplicationStoreFake{credential: &model.MFACredential{ID: model.NewMFACredentialID(),
			State: model.MFAStateActive}}, mfaCode: "not-a-factor!", loginID: "audited-user@example.edu",
			password: "CorrectHorseBatteryStaple1!", code: "authentication.mfa.code_invalid", knownTarget: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, persistence, audit, user := newLocalLoginAuditFixture(t, test.mfa)
			_, err := service.Login(context.Background(), localLoginInvocation(), LoginCommand{
				LoginID: test.loginID, Password: test.password, ClientType: model.SessionClientCLI, MFACode: test.mfaCode, Source: "192.0.2.10:2",
			})
			require.True(t, apperror.Is(err, test.code), "err = %v", err)
			require.Empty(t, persistence.sessions, "sessions")
			require.Empty(t, audit.pending, "sessions")
			require.Len(t, audit.terminal, 1, "sessions")
			record := audit.terminal[0]
			wantTarget := model.UserID("")
			if test.knownTarget {
				wantTarget = user.ID
			}
			require.Equal(t, model.AuditStatusFail, record.status)
			require.Empty(t, record.actor)
			require.Equal(t, wantTarget, record.target)
			require.Equal(t, test.code, record.errorCode)
			require.Equal(t, model.SessionClientCLI, record.clientType)
			require.Equal(t, "192.0.2.10", record.metadata.IPAddress)
			require.Equal(t, "login-test", record.metadata.UserAgent)
		})
	}
}

func TestLocalLoginRejectedBeforeAdmissionWritesNoAudit(t *testing.T) {
	t.Parallel()
	service, _, audit, user := newLocalLoginAuditFixture(t, &mfaApplicationStoreFake{})
	service.loginRateLimit.MaximumAttempts = 1
	command := LoginCommand{LoginID: user.Email, Password: "incorrect password", ClientType: model.SessionClientCLI, Source: "192.0.2.10:3"}
	_, err := service.Login(context.Background(), localLoginInvocation(), command)
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "first err = %v", err)
	_, err = service.Login(context.Background(), localLoginInvocation(), command)
	require.True(t, apperror.Is(err, "authentication.rate_limited"), "limited err = %v", err)
	require.Len(t, audit.terminal, 1)
	require.Equal(t, "authentication.credentials_invalid", audit.terminal[0].errorCode)
}

func TestLocalLoginIssuesNoSessionWhenSuccessAuditCannotComplete(t *testing.T) {
	t.Parallel()
	service, persistence, audit, user := newLocalLoginAuditFixture(t, &mfaApplicationStoreFake{})
	persistence.auditCompleteErr = errors.New("audit completion unavailable")

	result, err := service.Login(context.Background(), localLoginInvocation(), LoginCommand{
		LoginID: user.Email, Password: "CorrectHorseBatteryStaple1!", ClientType: model.SessionClientCLI, Source: "192.0.2.10:4",
	})
	require.Nil(t, result, "result")
	require.Error(t, err, "result")
	require.Empty(t, persistence.sessions, "sessions")
	require.Empty(t, audit.pending, "pending")
	require.Len(t, audit.terminal, 1, "pending")
	require.Equal(t, model.AuditStatusFail, audit.terminal[0].status, "pending")
}

func TestAuditServiceLocalLoginEventsCarryRequestIdentity(t *testing.T) {
	t.Parallel()
	audits := &systemAuditStoreFake{}
	institutionID := model.NewInstitutionID()
	service, err := access.NewAuditor(access.AuditorDependencies{Audits: audits, Institutions: loginAuditInstitutionStore{id: institutionID}, NodeID: "node-login"})
	require.NoError(t, err)
	metadata := model.RequestMetadata{RequestID: "request-1", IPAddress: "192.0.2.20", UserAgent: "agent"}
	userID := model.NewUserID()
	event, err := service.BeginLocalLogin(context.Background(), userID, model.SessionClientWeb, metadata)
	require.NoError(t, err)
	require.Equal(t, userID, event.ActorID)
	require.Equal(t, "authentication.login", event.Action)
	require.Equal(t, model.AuditStatusAttempt, event.Status)
	require.Equal(t, "password", event.AuthMethod)
	require.Equal(t, "192.0.2.20", event.IPAddress)
	require.Equal(t, "agent", event.UserAgent)
	require.Equal(t, institutionID.String(), event.ScopeID)
	require.Equal(t, userID.String(), event.Resource.ID)
	require.NoError(t, service.RecordLocalLoginFailure(context.Background(), "", model.SessionClientCLI, metadata,
		"authentication.credentials_invalid"))
	failed := audits.saved
	require.Empty(t, failed.ActorID)
	require.Equal(t, model.AuditStatusFail, failed.Status)
	require.Equal(t, "authentication.credentials_invalid", failed.ErrorCode)
	require.Equal(t, model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()}, failed.Resource)
	require.Equal(t, "192.0.2.20", failed.IPAddress)
}

type loginAuditInstitutionStore struct {
	store.InstitutionStore
	id model.InstitutionID
}

func (s loginAuditInstitutionStore) GetSingleton(context.Context) (*model.Institution, error) {
	return &model.Institution{ID: s.id}, nil
}
