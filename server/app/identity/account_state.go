// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
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
	ID             model.UserID
	Enabled        bool
	Reason         *string
	IdempotencyKey string
	// Batch is the administration batch context; a transport leaves it zero.
	Batch batchitem.Item
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
	if command.Enabled ||
		reason == "" ||
		utf8.RuneCountInString(reason) > MaximumAccountDisableReasonRunes ||
		!utf8.ValidString(reason) ||
		strings.ContainsFunc(reason, unicode.IsControl) {
		return "", apperror.Invalid("request.invalid", "reason")
	}

	return reason, nil
}

// SetUserEnabled enables or disables a User after authorizing account state
// management. Disabling revokes the User's Sessions and access tokens in the
// same Store operation; their revocation is published after commit.
func (s *Service) SetUserEnabled(
	ctx context.Context,
	invocation invocation.Invocation,
	command SetUserEnabledCommand,
) (*model.User, error) {
	userID := command.ID
	if !userID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}

	reason, err := accountDisableReason(command)
	if err != nil {
		return nil, err
	}
	if err = s.authorizer.AuthorizeAccountStateManage(ctx, invocation, userID); err != nil {
		return nil, err
	}
	if invocation.Principal().UserID == userID && !command.Enabled {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}

	current, err := s.users.Get(ctx, userID)
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

	batch := command.Batch.Fields()
	batchitem.BindOnboardingImport(idempotency, batch.OnboardingImportID, batch.OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, batch.Authorization)
	batchitem.BindBatch(idempotency, batch.Metadata)
	alreadySatisfied := current.DisabledAt.Valid == disabled
	if alreadySatisfied && idempotency == nil {
		return current, nil
	}

	now := model.TimeUTC(s.now())
	recipient := *current
	// Preparation freezes recipient fields only. SQL remains authoritative for
	// whether the state change is a no-op, so make the snapshot mail-eligible
	// even when a concurrent re-enable could turn a stale no-op into a mutation.
	recipient.DisabledAt = model.OptionalTime{}
	prepared := &store.PreparedMail{}
	if !batch.RetainedOutcome {
		prepared, err = s.mail.PrepareAccountStateChanged(mail.NoticePreparation{Recipient: &recipient, At: now}, command.Enabled)
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

	result, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionUserManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  operation,
			Value:      parameters,
			Prior:      current.Auditable(),
		},
		func() time.Time { return now },
		func(ctx context.Context, reference access.MutationReference) (*store.UserDisabledStateResult, error) {
			input := &store.UserDisabledStateChange{
				ID:               userID,
				ExpectedRevision: current.Revision,
				Disabled:         disabled,
				Capabilities:     AccessDeploymentCapabilities(s.capabilities.Snapshot()),
				Occurrence:       prepared.Occurrence,
				Delivery:         prepared.Delivery,
				DeliveryJob:      prepared.Job,
				ChangedAt:        reference.MutationAt,
				RevocationReason: model.SessionRevocationAccountDisabled,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
				Command:          idempotency,
			}
			value, storeErr := s.users.SetDisabled(ctx, input)
			if batch.Replayed != nil {
				*batch.Replayed = input.Replayed || input.NoOp
			}

			return value, storeErr
		},
		accountStateStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}

	if disabled && result != nil && len(result.RevokedSessions) > 0 {
		s.effects.SessionsRevoked(ctx, userID, sessionIDs(result.RevokedSessions), result.RevokedTokenHashes)
	}

	return result.User, nil
}

var accountStateStoreErrors = apperror.AdministrationStoreTable("user", "user.conflict", "user.invalid", map[string]apperror.Code{
	"users_last_system_admin": "user.last_system_admin",
})
