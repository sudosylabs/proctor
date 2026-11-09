// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SetUserEnabledCommand enables or disables a User. Reason, accepted only
// when disabling, is the administrator's optional note; it is trimmed, holds
// 1 to MaximumAccountDisableReasonRunes runes without control characters,
// and is kept only in the audit event's parameters.
type SetUserEnabledCommand struct {
	ID                        model.UserID
	Enabled                   bool
	Reason                    *string
	IdempotencyKey            string
	batchReplayed             *bool
	batchAuthorization        *store.CommandAuthorization
	batchMetadata             *store.CommandBatch
	batchRetainedOutcome      bool
	onboardingImportID        model.OnboardingImportID
	onboardingImportRowNumber int
}

// MaximumAccountDisableReasonRunes bounds the optional disable reason.
const MaximumAccountDisableReasonRunes = 500

// accountDisableReason validates the optional disable reason and returns it
// trimmed; an absent reason is empty.
func accountDisableReason(command SetUserEnabledCommand) (string, error) {
	if command.Reason == nil {
		return "", nil
	}
	reason := strings.TrimSpace(*command.Reason)
	if command.Enabled || reason == "" || utf8.RuneCountInString(reason) > MaximumAccountDisableReasonRunes ||
		!utf8.ValidString(reason) || strings.ContainsFunc(reason, unicode.IsControl) {
		return "", apperror.Invalid("request.invalid", "reason")
	}
	return reason, nil
}

type accountStateStore interface {
	Get(context.Context, model.UserID) (*model.User, error)
	SetDisabled(context.Context, *store.UserDisabledStateChange) (*store.UserDisabledStateResult, error)
}

type accountStateAuthorizer interface {
	AuthorizeAccountStateManage(context.Context, invocation.Invocation, model.UserID) error
}

type accountStateEffects interface {
	SessionsRevoked(context.Context, model.UserID, []*model.Session, []string)
}

// accountStateServiceDependencies are the collaborators of accountStateService;
// newAccountStateService validates them once and accountStateService keeps them in deps.
type accountStateServiceDependencies struct {
	Users         accountStateStore
	Authorization accountStateAuthorizer
	Capabilities  accessPolicyCapabilitySource
	Audit         mutationAuditor
	Mail          accountStateMailPreparer
	Effects       accountStateEffects
	Now           func() time.Time
}

type accountStateService struct {
	deps accountStateServiceDependencies
}

func newAccountStateService(deps accountStateServiceDependencies) *accountStateService {
	return &accountStateService{deps: deps}
}

func (s *accountStateService) SetUserEnabled(ctx context.Context, invocation invocation.Invocation, command SetUserEnabledCommand) (*model.User, error) {
	userID := command.ID
	if !userID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	reason, err := accountDisableReason(command)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Authorization.AuthorizeAccountStateManage(ctx, invocation, userID); err != nil {
		return nil, err
	}
	if invocation.Principal().UserID == userID && !command.Enabled {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}
	current, err := s.deps.Users.Get(ctx, userID)
	if err != nil {
		return nil, accountStateStoreErrors.Translate(err)
	}
	disabled := !command.Enabled
	idempotency, err := idempotency.Prepare(invocation, "user.enabled_state", command.IdempotencyKey, struct {
		UserID  string `json:"user_id"`
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason,omitempty"`
	}{userID.String(), command.Enabled, reason})
	if err != nil {
		return nil, err
	}
	bindOnboardingImportCommand(idempotency, command.onboardingImportID, command.onboardingImportRowNumber)
	bindAcademicAdministrationAuthorization(idempotency, command.batchAuthorization)
	bindAcademicAdministrationBatch(idempotency, command.batchMetadata)
	alreadySatisfied := current.DisabledAt.Valid == disabled
	if alreadySatisfied && idempotency == nil {
		return current, nil
	}
	now := model.TimeUTC(s.deps.Now())
	recipient := *current
	// Preparation freezes recipient fields only. SQL remains authoritative for
	// whether the state change is a no-op, so make the snapshot mail-eligible
	// even when a concurrent re-enable could turn a stale no-op into a mutation.
	recipient.DisabledAt = model.OptionalTime{}
	prepared := &store.PreparedMail{}
	if !command.batchRetainedOutcome {
		prepared, err = s.deps.Mail.PrepareAccountStateChanged(mail.NoticePreparation{Recipient: &recipient, At: now}, command.Enabled)
		if err != nil {
			return nil, accountStateStoreErrors.Translate(err)
		}
	}
	operation := model.AuditOperationEnableAccount
	if disabled {
		operation = model.AuditOperationDisableAccount
	}
	parameters := map[string]any{"user_id": userID, "disabled": disabled}
	if reason != "" {
		parameters["reason"] = reason
	}
	result, err := runAuditedMutation(
		ctx,
		s.deps.Audit,
		mutationAttempt{
			Invocation: invocation,
			Action:     model.ActionUserManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  operation,
			Value:      parameters,
			Prior:      current.Auditable(),
		},
		func() time.Time { return now },
		func(ctx context.Context, reference mutationAttemptReference) (*store.UserDisabledStateResult, error) {
			input := &store.UserDisabledStateChange{
				ID: userID, ExpectedRevision: current.Revision, Disabled: disabled,
				Capabilities: accessDeploymentCapabilities(s.deps.Capabilities.Snapshot()),
				Occurrence:   prepared.Occurrence, Delivery: prepared.Delivery, DeliveryJob: prepared.Job,
				ChangedAt: reference.MutationAt, RevocationReason: model.SessionRevocationAccountDisabled,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt, Command: idempotency,
			}
			value, storeErr := s.deps.Users.SetDisabled(ctx, input)
			if command.batchReplayed != nil {
				*command.batchReplayed = input.Replayed || input.NoOp
			}
			return value, storeErr
		},
		accountStateStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	if disabled && result != nil && len(result.RevokedSessions) > 0 {
		s.deps.Effects.SessionsRevoked(ctx, userID, result.RevokedSessions, result.RevokedTokenHashes)
	}
	return result.User, nil
}

type accountStateRealtimeEffects struct {
	effects authenticationSecurityEffects
}

func (e accountStateRealtimeEffects) SessionsRevoked(ctx context.Context, userID model.UserID, sessions []*model.Session, hashes []string) {
	e.effects.SessionsRevoked(ctx, userID, sessionIDs(sessions), hashes)
}

var accountStateStoreErrors = apperror.AdministrationStoreTable("user", "user.conflict", "user.invalid", map[string]apperror.Code{
	"users_last_system_admin": "user.last_system_admin",
})
