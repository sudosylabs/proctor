// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/audit.go and
// server/public/model/audit_record.go. Proctor writes authoritative events to
// PostgreSQL and treats decision-audit failure as a closed security boundary.

package access

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuditorDependencies configures NewAuditor.
type AuditorDependencies struct {
	Audits       store.AuditStore
	Institutions store.InstitutionStore
	NodeID       string
}

// Auditor writes attempts, terminal outcomes and authorization decisions.
type Auditor struct {
	deps AuditorDependencies

	now func() time.Time
}

// UserSettingsAuditInput records the source's size, never its contents.
type UserSettingsAuditInput struct {
	Invocation        invocation.Invocation
	UserID            model.UserID
	PreviousRevision  model.UserSettingsRevision
	ResultingRevision model.UserSettingsRevision
	FormatVersion     int
	SourceBytes       int
}

// UserSettingsAuditAdapter audits settings replacements.
type UserSettingsAuditAdapter struct{ audit *Auditor }

// MailAuditAdapter audits mail delivery control.
type MailAuditAdapter struct{ audit *Auditor }

// Fail completes a mail control attempt as failed; errorCode is the public
// error code.
func (a MailAuditAdapter) Fail(ctx context.Context, auditID model.AuditEventID, errorCode string) error {
	_, appErr := a.audit.CompleteCriticalAction(ctx, auditID, model.AuditStatusFail, errorCode, nil)
	return appErr
}

// PrepareControl begins the attempt of canceling or retrying a delivery.
func (a MailAuditAdapter) PrepareControl(
	ctx context.Context,
	inv invocation.Invocation,
	institution model.Resource,
	delivery *model.MailDelivery,
	operation string,
) (model.AuditEventID, error) {
	if a.audit == nil ||
		institution.Type != model.ResourceInstitution ||
		!model.InstitutionID(institution.ID).IsValid() ||
		delivery == nil ||
		delivery.Validate() != nil ||
		(operation != "cancel" && operation != "retry") {
		return "", AuditUnavailable(errors.New("mail control audit dependencies are invalid"))
	}

	event, appErr := a.audit.BeginCriticalActionAtScope(
		ctx,
		inv.Principal(),
		model.ActionMailManage,
		model.Resource{Type: model.ResourceMailDelivery, ID: delivery.ID.String()},
		model.RoleScopeInstitution,
		institution.ID,
		inv.RequestMetadata(),
		map[string]any{"operation": operation, "value": delivery.Auditable()},
		delivery.Auditable(),
	)
	if appErr != nil {
		return "", appErr
	}

	return event.ID, nil
}

// PrepareTest builds the audit event of a test message.
func (a MailAuditAdapter) PrepareTest(
	ctx context.Context,
	inv invocation.Invocation,
	institution model.Resource,
	deliveryID model.MailDeliveryID,
) (*model.AuditEvent, error) {
	if a.audit == nil ||
		institution.Type != model.ResourceInstitution ||
		!model.InstitutionID(institution.ID).IsValid() ||
		!deliveryID.IsValid() {
		return nil, AuditUnavailable(errors.New("mail audit dependencies are invalid"))
	}

	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, InvalidTokenAppError()
	}

	parameters, err := model.EncodeAuditData(map[string]string{
		"delivery_id":  deliveryID.String(),
		"template_key": string(model.MailTemplateSystemTest),
	})
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	metadata := inv.RequestMetadata()
	return &model.AuditEvent{
		ActorID:    principal.UserID,
		SessionID:  principal.SessionID,
		Action:     string(model.ActionMailManage),
		Resource:   model.Resource{Type: model.ResourceMailDelivery, ID: deliveryID.String()},
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institution.ID,
		Status:     model.AuditStatusSuccess,
		RequestID:  metadata.RequestID,
		NodeID:     a.audit.deps.NodeID,
		ClientType: string(principal.ClientType),
		AuthMethod: principal.AuthenticationMethod,
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
		Parameters: parameters,
	}, nil
}

// PrepareReplacement returns an unpersisted event for the settings store to
// insert in its own commit.
func (a UserSettingsAuditAdapter) PrepareReplacement(
	ctx context.Context,
	input UserSettingsAuditInput,
) (*model.AuditEvent, error) {
	if a.audit == nil {
		return nil, AuditUnavailable(errors.New("user settings audit service is unavailable"))
	}

	principal := input.Invocation.Principal()
	if principal.Validate() != nil ||
		principal.CredentialType != model.CredentialSessionAccess ||
		principal.UserID != input.UserID ||
		!input.PreviousRevision.IsValid() ||
		!input.ResultingRevision.IsValid() ||
		input.FormatVersion <= 0 ||
		input.SourceBytes < 0 {
		return nil, apperror.New("audit.internal")
	}

	institution, err := a.audit.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return nil, AuditUnavailable(err)
	}

	parameters, err := model.EncodeAuditData(map[string]any{
		"previous_revision":  input.PreviousRevision.String(),
		"resulting_revision": input.ResultingRevision.String(),
		"format_version":     input.FormatVersion,
		"source_bytes":       input.SourceBytes,
	})
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	metadata := input.Invocation.RequestMetadata()
	return &model.AuditEvent{
		ActorID:    principal.UserID,
		SessionID:  principal.SessionID,
		Action:     string(model.ActionUserSettingsReplace),
		Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institution.ID.String(),
		Status:     model.AuditStatusSuccess,
		RequestID:  metadata.RequestID,
		NodeID:     a.audit.deps.NodeID,
		ClientType: string(principal.ClientType),
		AuthMethod: principal.AuthenticationMethod,
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
		Parameters: parameters,
	}, nil
}

// NewAuditor fails when a store or the node ID is missing.
func NewAuditor(deps AuditorDependencies) (*Auditor, error) {
	if deps.Audits == nil {
		return nil, errors.New("audit store is required")
	}
	if deps.Institutions == nil {
		return nil, errors.New("audit institution store is required")
	}
	if deps.NodeID == "" {
		return nil, errors.New("audit node ID is required")
	}
	return &Auditor{deps: deps, now: time.Now}, nil
}

// BeginAuthentication records an external login attempt before any session is
// issued.
func (s *Auditor) BeginAuthentication(
	ctx context.Context,
	userID model.UserID,
	method string,
	providerID string,
	clientType model.SessionClientType,
	metadata model.RequestMetadata,
	institutionID model.InstitutionID,
) (*model.AuditEvent, error) {
	if !userID.IsValid() ||
		!institutionID.IsValid() ||
		!clientType.IsValid() ||
		method == "" {
		return nil, apperror.New("audit.internal")
	}

	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}

	parameters, err := model.EncodeAuditData(map[string]string{
		"provider": providerID,
	})
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	event := &model.AuditEvent{
		ActorID: userID,
		Action:  string(model.ActionAuthenticationExternalLogin),
		Resource: model.Resource{
			Type: model.ResourceUser,
			ID:   userID.String(),
		},
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institutionID.String(),
		Status:     model.AuditStatusAttempt,
		RequestID:  metadata.RequestID,
		NodeID:     s.deps.NodeID,
		ClientType: string(clientType),
		AuthMethod: method,
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
		Parameters: parameters,
	}
	saved, err := s.deps.Audits.Save(ctx, event)
	if err != nil {
		return nil, AuditUnavailable(err)
	}

	return saved, nil
}

// BeginLocalLogin records a proved password login before any session is
// issued; the event is completed with the session or the issuance failure.
func (s *Auditor) BeginLocalLogin(
	ctx context.Context,
	userID model.UserID,
	clientType model.SessionClientType,
	metadata model.RequestMetadata,
) (*model.AuditEvent, error) {
	if !userID.IsValid() || !clientType.IsValid() {
		return nil, apperror.New("audit.internal")
	}
	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}
	institution, err := s.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return nil, AuditUnavailable(err)
	}
	event := localLoginAuditEvent(s.deps.NodeID, institution.ID.String(), clientType, metadata)
	event.ActorID = userID
	event.Resource = model.Resource{Type: model.ResourceUser, ID: userID.String()}
	event.Status = model.AuditStatusAttempt
	saved, err := s.deps.Audits.Save(ctx, event)
	if err != nil {
		return nil, AuditUnavailable(err)
	}
	return saved, nil
}

// RecordLocalLoginFailure never makes the failed caller an actor. A resolved
// account is recorded only as the targeted resource, so operators can see
// attacks on it without the raw, user-controlled login identifier entering
// audit data.
func (s *Auditor) RecordLocalLoginFailure(
	ctx context.Context,
	target model.UserID,
	clientType model.SessionClientType,
	metadata model.RequestMetadata,
	errorCode string,
) error {
	if errorCode == "" {
		return apperror.New("audit.internal")
	}
	if s.deps.Audits == nil {
		return AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}
	institution, err := s.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return AuditUnavailable(err)
	}
	event := localLoginAuditEvent(s.deps.NodeID, institution.ID.String(), clientType, metadata)
	event.Resource = model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if target.IsValid() {
		event.Resource = model.Resource{Type: model.ResourceUser, ID: target.String()}
	}
	event.Status = model.AuditStatusFail
	event.ErrorCode = errorCode
	if _, err := s.deps.Audits.Save(ctx, event); err != nil {
		return AuditUnavailable(err)
	}
	return nil
}

func localLoginAuditEvent(
	nodeID,
	institutionID string,
	clientType model.SessionClientType,
	metadata model.RequestMetadata,
) *model.AuditEvent {
	return &model.AuditEvent{
		Action:     string(model.ActionAuthenticationLogin),
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institutionID,
		RequestID:  metadata.RequestID,
		NodeID:     nodeID,
		ClientType: string(clientType),
		AuthMethod: "password",
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
	}
}

// RecordExternalAuthenticationFailure records a terminal failed external
// login.
func (s *Auditor) RecordExternalAuthenticationFailure(
	ctx context.Context,
	providerID string,
	method string,
	metadata model.RequestMetadata,
	institutionID model.InstitutionID,
	errorCode string,
) error {
	if method == "" {
		return apperror.New("audit.internal")
	}

	parameters, err := model.EncodeAuditData(map[string]string{
		"provider": providerID,
	})
	if err != nil {
		return apperror.DomainInvalid("audit.internal", err)
	}
	if s.deps.Audits == nil {
		return AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}

	event := &model.AuditEvent{
		Action: string(model.ActionAuthenticationExternalLogin),
		Resource: model.Resource{
			Type: model.ResourceInstitution,
			ID:   institutionID.String(),
		},
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    institutionID.String(),
		Status:     model.AuditStatusFail,
		RequestID:  metadata.RequestID,
		NodeID:     s.deps.NodeID,
		AuthMethod: method,
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
		ErrorCode:  errorCode,
		Parameters: parameters,
	}
	if _, err := s.deps.Audits.Save(ctx, event); err != nil {
		return AuditUnavailable(err)
	}

	return nil
}

// BeginCriticalAction must succeed before a security-sensitive mutation
// starts. Pass only bounded safe values or Auditable projections, never
// credentials or secrets.
func (s *Auditor) BeginCriticalAction(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	metadata model.RequestMetadata,
	parameters any,
	priorState any,
) (*model.AuditEvent, error) {
	if principal.ValidateMFARecovery() != nil {
		return nil, InvalidTokenAppError()
	}
	return s.BeginCriticalActionForSubject(ctx, principal, "", action, resource, metadata, parameters, priorState)
}

// BeginCriticalActionForSubject is for a mutation whose resource is not the
// affected user, such as a role binding. A zero subject leaves the decision to
// model.AuditEvent's user resource rule.
func (s *Auditor) BeginCriticalActionForSubject(
	ctx context.Context,
	principal model.Principal,
	subject model.UserID,
	action model.Action,
	resource model.Resource,
	metadata model.RequestMetadata,
	parameters any,
	priorState any,
) (*model.AuditEvent, error) {
	if principal.ValidateMFARecovery() != nil {
		return nil, InvalidTokenAppError()
	}
	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}
	scopeType, scopeID, err := s.resourceScope(ctx, resource)
	if err != nil {
		return nil, err
	}
	return s.beginCriticalActionAtScope(ctx, principal, subject, action, resource, scopeType, scopeID, metadata, parameters, priorState)
}

// resourceScope records a user's audit at the institution, since a user is
// not a role binding scope.
func (s *Auditor) resourceScope(ctx context.Context, resource model.Resource) (model.RoleScopeType, string, error) {
	if resource.Type != model.ResourceUser {
		return model.RoleScopeType(resource.Type), resource.ID, nil
	}
	institution, err := s.deps.Institutions.GetSingleton(ctx)
	if err != nil {
		return "", "", AuditUnavailable(err)
	}
	return model.RoleScopeInstitution, institution.ID.String(), nil
}

// PrepareCriticalAction builds an unpersisted event for a store aggregate that
// is itself the pending attempt. The aggregate inserts exactly one terminal
// event, or none for an idempotent replay.
func (s *Auditor) PrepareCriticalAction(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	metadata model.RequestMetadata,
	parameters any,
	priorState any,
) (*model.AuditEvent, error) {
	if principal.ValidateMFARecovery() != nil {
		return nil, InvalidTokenAppError()
	}
	scopeType, scopeID, err := s.resourceScope(ctx, resource)
	if err != nil {
		return nil, err
	}
	return s.prepareCriticalActionAtScope(principal, "", action, resource, scopeType, scopeID, metadata, parameters, priorState)
}

// BeginCriticalActionAtScope keeps the resolved authorization scope beside the
// resource, for resources such as exams that are not role binding scopes.
func (s *Auditor) BeginCriticalActionAtScope(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	parameters any,
	priorState any,
) (*model.AuditEvent, error) {
	if principal.ValidateMFARecovery() != nil {
		return nil, InvalidTokenAppError()
	}
	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}
	return s.beginCriticalActionAtScope(ctx, principal, "", action, resource, scopeType, scopeID, metadata, parameters, priorState)
}

// BeginSystemCriticalActionAtScope records trusted work with no actor. It is
// separate from the principal path so jobs never fabricate a user or silently
// bypass user authorization.
func (s *Auditor) BeginSystemCriticalActionAtScope(
	ctx context.Context,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	parameters any,
) (*model.AuditEvent, error) {
	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}

	encodedParameters, err := model.EncodeAuditData(parameters)
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	event := &model.AuditEvent{
		Action:     string(action),
		Resource:   resource,
		ScopeType:  scopeType,
		ScopeID:    scopeID,
		Status:     model.AuditStatusAttempt,
		NodeID:     s.deps.NodeID,
		Parameters: encodedParameters,
	}
	saved, err := s.deps.Audits.Save(ctx, event)
	if err != nil {
		return nil, AuditUnavailable(err)
	}

	return saved, nil
}

// BeginOnBehalfAtScope records trusted work done for actor outside any request
// of theirs. It is separate from the principal path, so no principal is
// fabricated, and from the system path, so the work is attributed to the
// person whose work it is.
func (s *Auditor) BeginOnBehalfAtScope(
	ctx context.Context,
	actor model.UserID,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	parameters any,
) (*model.AuditEvent, error) {
	if !actor.IsValid() {
		return nil, apperror.New("audit.internal")
	}
	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}

	encodedParameters, err := model.EncodeAuditData(parameters)
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	saved, err := s.deps.Audits.Save(ctx, &model.AuditEvent{
		ActorID:    actor,
		Action:     string(action),
		Resource:   resource,
		ScopeType:  scopeType,
		ScopeID:    scopeID,
		Status:     model.AuditStatusAttempt,
		NodeID:     s.deps.NodeID,
		Parameters: encodedParameters,
	})
	if err != nil {
		return nil, AuditUnavailable(err)
	}

	return saved, nil
}

// AccountAuditActor is the proved account behind a request with no principal
// yet, such as desktop approval or code exchange. Never build it from
// user-controlled input.
type AccountAuditActor struct {
	UserID     model.UserID
	SessionID  model.SessionID
	ClientType model.SessionClientType
}

// BeginAccountActionAtScope records an attempt for a proved account that has
// no principal yet, keeping the request identity.
func (s *Auditor) BeginAccountActionAtScope(
	ctx context.Context,
	actor AccountAuditActor,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	parameters any,
) (*model.AuditEvent, error) {
	if !actor.UserID.IsValid() || !actor.ClientType.IsValid() {
		return nil, apperror.New("audit.internal")
	}
	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}

	encodedParameters, err := model.EncodeAuditData(parameters)
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	saved, err := s.deps.Audits.Save(ctx, &model.AuditEvent{
		ActorID:    actor.UserID,
		SessionID:  actor.SessionID,
		Action:     string(action),
		Resource:   resource,
		ScopeType:  scopeType,
		ScopeID:    scopeID,
		Status:     model.AuditStatusAttempt,
		RequestID:  metadata.RequestID,
		NodeID:     s.deps.NodeID,
		ClientType: string(actor.ClientType),
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
		Parameters: encodedParameters,
	})
	if err != nil {
		return nil, AuditUnavailable(err)
	}

	return saved, nil
}

func (s *Auditor) beginCriticalActionAtScope(
	ctx context.Context,
	principal model.Principal,
	subject model.UserID,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	parameters any,
	priorState any,
) (*model.AuditEvent, error) {
	event, appErr := s.prepareCriticalActionAtScope(principal, subject, action, resource, scopeType, scopeID, metadata, parameters, priorState)
	if appErr != nil {
		return nil, appErr
	}
	saved, err := s.deps.Audits.Save(ctx, event)
	if err != nil {
		return nil, AuditUnavailable(err)
	}
	return saved, nil
}

func (s *Auditor) prepareCriticalActionAtScope(
	principal model.Principal,
	subject model.UserID,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	parameters any,
	priorState any,
) (*model.AuditEvent, error) {
	encodedParameters, err := model.EncodeAuditData(parameters)
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	encodedPriorState, err := model.EncodeAuditData(priorState)
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}

	event := &model.AuditEvent{
		ActorID:       principal.UserID,
		SessionID:     principal.SessionID,
		SubjectUserID: subject,
		Action:        string(action),
		Resource:      resource,
		ScopeType:     scopeType,
		ScopeID:       scopeID,
		Status:        model.AuditStatusAttempt,
		RequestID:     metadata.RequestID,
		NodeID:        s.deps.NodeID,
		ClientType:    string(principal.ClientType),
		AuthMethod:    principal.AuthenticationMethod,
		IPAddress:     metadata.IPAddress,
		UserAgent:     metadata.UserAgent,
		Parameters:    encodedParameters,
		PriorState:    encodedPriorState,
	}
	return event, nil
}

// CompleteCriticalAction records the terminal outcome. If it fails after the
// mutation committed, the attempt stays for operator reconciliation and the
// use case must return the audit failure, not success. It ignores ctx
// cancellation so a disconnecting client cannot leave the attempt unresolved;
// the store's query timeout still bounds it.
func (s *Auditor) CompleteCriticalAction(
	ctx context.Context,
	eventID model.AuditEventID,
	status model.AuditStatus,
	errorCode string,
	result any,
) (*model.AuditEvent, error) {
	ctx = context.WithoutCancel(ctx)
	if s.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}
	encodedResult, err := model.EncodeAuditData(result)
	if err != nil {
		return nil, apperror.DomainInvalid("audit.internal", err)
	}
	event, err := s.deps.Audits.Complete(
		ctx,
		&store.AuditCompletion{ID: eventID, Status: status, ErrorCode: errorCode, Result: encodedResult, UpdateAt: s.now()},
	)
	if err != nil {
		return nil, AuditUnavailable(err)
	}
	return event, nil
}

// RecordAuthorizationDecision fails with authorization.unavailable when the
// decision cannot be recorded, so the request stays unauthorized.
func (s *Auditor) RecordAuthorizationDecision(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	allowed bool,
) error {
	return s.recordDecision(ctx, principal, string(action), resource, scopeType, scopeID, metadata, allowed)
}

// RecordUserSearchDecision records a directory search without a permission
// action of its own; authority comes from user.view and class.members.view.
func (s *Auditor) RecordUserSearchDecision(
	ctx context.Context,
	principal model.Principal,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	allowed bool,
) error {
	return s.recordDecision(
		ctx,
		principal,
		string(model.ActionUserSearch),
		resource,
		scopeType,
		scopeID,
		metadata,
		allowed,
	)
}

func (s *Auditor) recordDecision(
	ctx context.Context,
	principal model.Principal,
	action string,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	metadata model.RequestMetadata,
	allowed bool,
) error {
	status := model.AuditStatusFail
	errorCode := "authorization.denied"
	if allowed {
		status = model.AuditStatusSuccess
		errorCode = ""
	}

	event := &model.AuditEvent{
		ActorID:    principal.UserID,
		SessionID:  principal.SessionID,
		Action:     action,
		Resource:   resource,
		ScopeType:  scopeType,
		ScopeID:    scopeID,
		Status:     status,
		RequestID:  metadata.RequestID,
		NodeID:     s.deps.NodeID,
		ClientType: string(principal.ClientType),
		AuthMethod: principal.AuthenticationMethod,
		IPAddress:  metadata.IPAddress,
		UserAgent:  metadata.UserAgent,
		ErrorCode:  errorCode,
	}
	// A decision that cannot be recorded leaves the request unauthorized, so it
	// fails as the authorization step.
	if s.deps.Audits == nil {
		return apperror.New("authorization.unavailable").Wrap(store.NewErrNotFound("audit_store", ""))
	}
	if _, err := s.deps.Audits.Save(ctx, event); err != nil {
		return apperror.New("authorization.unavailable").Wrap(err)
	}

	return nil
}

// List applies institution-wide visibility, so the caller must already be
// authorized for that.
func (s *Auditor) List(
	ctx context.Context,
	query model.AuditQuery,
) ([]*model.AuditEvent, error) {
	if query.Limit < 1 ||
		query.Limit > 200 ||
		(query.ActorID != "" && !model.UserID(query.ActorID).IsValid()) ||
		(query.BeforeID != "" && !model.AuditEventID(query.BeforeID).IsValid()) ||
		(query.Resource != nil && query.Resource.Validate() != nil) {
		return nil, apperror.New("audit.query.invalid")
	}

	var actions []string
	if query.Action != "" {
		actions = []string{query.Action}
	}
	events, err := s.deps.Audits.List(ctx, store.AuditListOptions{
		ActorID:         model.UserID(query.ActorID),
		Actions:         actions,
		Resource:        query.Resource,
		BeforeCreatedAt: query.BeforeCreatedAt,
		BeforeID:        model.AuditEventID(query.BeforeID),
		Limit:           query.Limit,
		Visibility:      store.AuditVisibilityScope{InstitutionWide: true},
	})
	if err != nil {
		return nil, AuditUnavailable(err)
	}

	return events, nil
}

// AuditUnavailable reports an audit store failure.
func AuditUnavailable(err error) error {
	return apperror.New("audit.unavailable").Wrap(err)
}

// NodeID is the cluster node recorded on every audit event.
func (s *Auditor) NodeID() string {
	return s.deps.NodeID
}

// MutationAudit returns the view for principal-attributed mutations.
func (s *Auditor) MutationAudit() MutationAuditAdapter {
	return MutationAuditAdapter{audit: s}
}

// SystemAudit returns the actorless view for jobs and periodic tasks.
func (s *Auditor) SystemAudit() SystemAuditAdapter {
	return SystemAuditAdapter{audit: s}
}

// MailAudit returns the mail delivery view.
func (s *Auditor) MailAudit() MailAuditAdapter {
	return MailAuditAdapter{audit: s}
}

// UserSettingsAudit returns the settings replacement view.
func (s *Auditor) UserSettingsAudit() UserSettingsAuditAdapter {
	return UserSettingsAuditAdapter{audit: s}
}
