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
	"strings"
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

// SetUserEnabledCommand enables or disables a user. Reason is accepted only
// when disabling; it is stored on the User with who disabled it, and in the
// audit event.
type SetUserEnabledCommand struct {
	ID             model.UserID
	Enabled        bool
	Reason         *string
	IdempotencyKey string
	// Batch is zero outside an administration batch.
	Batch batchitem.Item
}

// MaximumAccountDisableReasonRunes is the longest disable reason, in runes.
const MaximumAccountDisableReasonRunes = model.UserDisableReasonMaxRunes

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

// SetUserEnabled enables or disables a user. Disabling revokes the user's
// sessions in the same store operation and publishes the revocation after
// commit.
func (s *Service) SetUserEnabled(
	ctx context.Context,
	inv invocation.Invocation,
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
	if err = s.authorizer.AuthorizeAccountStateManage(ctx, inv, userID); err != nil {
		return nil, err
	}
	if inv.Principal().UserID == userID && !command.Enabled {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}

	commandIdempotency, err := idempotency.Prepare(inv, "user.enabled_state", command.IdempotencyKey, struct {
		UserID  string `json:"user_id"`
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason,omitempty"`
	}{userID.String(), command.Enabled, reason})
	if err != nil {
		return nil, err
	}

	batch := command.Batch.Fields()
	batchitem.BindOnboardingImport(commandIdempotency, batch.OnboardingImportID, batch.OnboardingImportRowNumber)
	batchitem.BindAuthorization(commandIdempotency, batch.Authorization)
	batchitem.BindBatch(commandIdempotency, batch.Metadata)

	// The write is fenced on the revision it read. A change the command never
	// reads, such as the default profile picture attached just after an account
	// is created, can move that revision in between. The refused write rolls
	// back and records no outcome, so the command starts again from a fresh
	// read; each try is audited as what it was.
	for attempt := 1; ; attempt++ {
		user, stale, err := s.setUserEnabledFromCurrent(ctx, inv, command, reason, commandIdempotency)
		if !stale || attempt == userEnabledAttempts {
			return user, err
		}
	}
}

// userEnabledAttempts bounds how often SetUserEnabled starts again after its
// read went stale.
const userEnabledAttempts = 3

// setUserEnabledFromCurrent reads the user and applies the command to that
// read. stale reports that the user changed after the read, so the store
// refused the write.
func (s *Service) setUserEnabledFromCurrent(
	ctx context.Context,
	inv invocation.Invocation,
	command SetUserEnabledCommand,
	reason string,
	commandIdempotency *store.CommandIdempotency,
) (*model.User, bool, error) {
	userID := command.ID
	current, err := s.users.Get(ctx, userID)
	if err != nil {
		return nil, false, accountStateStoreErrors.Translate(err)
	}

	disabled := !command.Enabled
	batch := command.Batch.Fields()
	alreadySatisfied := current.DisabledAt.Valid == disabled
	if alreadySatisfied && commandIdempotency == nil {
		return current, false, nil
	}

	now := model.TimeUTC(s.now())
	recipient := *current
	// The store, not this snapshot, decides whether the change is a no-op, so
	// keep the recipient mail-eligible in case a concurrent re-enable turns a
	// stale no-op into a real change.
	recipient.DisabledAt = model.OptionalTime{}
	prepared := &store.PreparedMail{}
	if !batch.RetainedOutcome {
		prepared, err = s.mail.PrepareAccountStateChanged(mail.NoticePreparation{Recipient: &recipient, At: now}, command.Enabled)
		if err != nil {
			return nil, false, accountStateStoreErrors.Translate(err)
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

	var stale bool
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionUserManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  operation,
			Value:      command.Batch.AuditValue(parameters),
			Prior:      current.Auditable(),
			At:         now,
		},
		func(ctx context.Context, reference access.MutationReference) (*store.UserDisabledStateResult, error) {
			input := &store.UserDisabledStateChange{
				ID:               userID,
				ExpectedRevision: current.Revision,
				Disabled:         disabled,
				DisabledBy:       inv.Principal().UserID,
				DisabledReason:   reason,
				Capabilities:     AccessDeploymentCapabilities(s.capabilities.Snapshot()),
				Occurrence:       prepared.Occurrence,
				Delivery:         prepared.Delivery,
				DeliveryJob:      prepared.Job,
				ChangedAt:        reference.MutationAt,
				RevocationReason: model.SessionRevocationAccountDisabled,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
				Command:          commandIdempotency,
			}
			value, storeErr := s.users.SetDisabled(ctx, input)
			var conflict *store.ErrConflict
			stale = errors.As(storeErr, &conflict) && conflict.Resource == "user" && conflict.Constraint == "user_changed"
			if batch.Replayed != nil {
				*batch.Replayed = input.Replayed || input.NoOp
			}

			return value, storeErr
		},
		accountStateStoreErrors.Translate,
	)
	if err != nil {
		return nil, stale, err
	}
	if disabled && result != nil && len(result.RevokedSessions) > 0 {
		s.publisher.SessionsRevoked(ctx, userID, sessionIDs(result.RevokedSessions), result.RevokedTokenHashes)
	}
	s.publishUserChanged(ctx, userID)

	return result.User, false, nil
}

var accountStateStoreErrors = apperror.AdministrationStoreTable("user", "user.conflict", "user.invalid", map[string]apperror.Code{
	"users_last_system_admin": "user.last_system_admin",
})
