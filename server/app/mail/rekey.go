// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RekeyView identifies a started mail rekey Job and the keys it moves
// payloads between.
type RekeyView struct {
	JobID         model.JobID
	PrimaryKeyID  string
	RetiringKeyID string
	CreatedAt     time.Time
}

// PayloadKeyUsageView counts the active payloads still sealed with one key.
type PayloadKeyUsageView struct {
	KeyID            string
	ActiveReferences int64
}

// KeyStateView reports the configured and required primary mail payload keys
// and every key active payloads still reference.
type KeyStateView struct {
	PrimaryKeyID         string
	RequiredPrimaryKeyID string
	Active               []PayloadKeyUsageView
}

// GetMailKeyState reports the configured and required primary mail payload
// keys and the keys still referenced by active payloads.
func (s *Operations) GetMailKeyState(ctx context.Context, invocation invocation.Invocation) (KeyStateView, error) {
	if s == nil {
		return KeyStateView{}, apperror.New("mail.unavailable")
	}
	if err := access.RequireStrongRecentSession(invocation.Principal(), s.deps.Now(), s.deps.RecentAuthenticationTTL); err != nil {
		return KeyStateView{}, err
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailRekey); err != nil {
		return KeyStateView{}, err
	}
	if s.deps.Mail == nil || s.deps.Sealer == nil {
		return KeyStateView{}, apperror.New("mail.unavailable")
	}

	state, err := s.deps.Mail.InspectKeyState(ctx)
	if err != nil || state == nil {
		return KeyStateView{}, apperror.New("mail.unavailable").Wrap(err)
	}

	view := KeyStateView{
		PrimaryKeyID:         s.deps.Sealer.PrimaryKeyID(),
		RequiredPrimaryKeyID: state.RequiredPrimaryKeyID,
		Active:               make([]PayloadKeyUsageView, 0, len(state.Active)),
	}
	for _, usage := range state.Active {
		if usage.KeyID == "" || usage.ActiveReferences <= 0 {
			return KeyStateView{}, apperror.New("mail.unavailable").Wrap(errors.New("mail key state is invalid"))
		}

		view.Active = append(view.Active, PayloadKeyUsageView{KeyID: usage.KeyID, ActiveReferences: usage.ActiveReferences})
	}

	return view, nil
}

// StartMailRekey starts the Job that re-encrypts active mail payloads away
// from a retiring key to the configured primary key.
func (s *Operations) StartMailRekey(ctx context.Context, invocation invocation.Invocation, retiringKeyID string) (RekeyView, error) {
	if s == nil {
		return RekeyView{}, apperror.New("mail.unavailable")
	}
	if err := access.RequireStrongRecentSession(invocation.Principal(), s.deps.Now(), s.deps.RecentAuthenticationTTL); err != nil {
		return RekeyView{}, err
	}

	resource, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailRekey)
	if err != nil {
		return RekeyView{}, err
	}
	if s.deps.Mail == nil || s.rekeyAudit == nil || s.deps.Sealer == nil || s.wake == nil {
		return RekeyView{}, apperror.New("mail.unavailable")
	}

	primaryKeyID := s.deps.Sealer.PrimaryKeyID()
	if retiringKeyID == "" || retiringKeyID == primaryKeyID || !s.deps.Sealer.HasKey(retiringKeyID) {
		return RekeyView{}, apperror.New("mail.rekey.invalid")
	}

	command, err := json.Marshal(RekeyCommand{PrimaryKeyID: primaryKeyID, RetiringKeyID: retiringKeyID})
	if err != nil {
		return RekeyView{}, apperror.New("mail.unavailable").Wrap(err)
	}

	now := model.TimeUTC(s.deps.Now())
	job, err := model.NewJob(
		model.NewJobID(),
		model.JobTypeMailRekey,
		command,
		"mail-rekey:"+primaryKeyID+":"+retiringKeyID,
		now,
		now,
		10,
	)
	if err != nil {
		return RekeyView{}, apperror.New("mail.unavailable").Wrap(err)
	}

	job.CreatedByUserID = invocation.Principal().UserID
	operation, err := access.RunAudited(
		ctx,
		s.rekeyAudit,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionMailRekey,
			Resource:   resource,
			ScopeType:  model.RoleScopeInstitution,
			ScopeID:    resource.ID,
			Operation:  "start_rekey",
			Value:      map[string]any{"job_id": job.ID.String(), "primary_key_id": primaryKeyID, "retiring_key_id": retiringKeyID},
		},
		s.deps.Now,
		func(ctx context.Context, reference access.MutationReference) (*store.MailRekeyOperation, error) {
			return s.deps.Mail.StartRekey(ctx, &store.MailRekeyStart{
				PrimaryKeyID:  primaryKeyID,
				RetiringKeyID: retiringKeyID,
				Job:           job,
				AuditEventID:  reference.ID,
				AuditAt:       reference.MutationAt,
			})
		},
		rekeyStoreErrors.Translate,
	)
	if err != nil {
		return RekeyView{}, err
	}

	if operation == nil ||
		!operation.JobID.IsValid() ||
		operation.PrimaryKeyID != primaryKeyID ||
		operation.RetiringKeyID != retiringKeyID ||
		operation.CreatedAt.IsZero() {
		return RekeyView{}, apperror.New("mail.unavailable").Wrap(errors.New("mail rekey persistence returned an invalid operation"))
	}

	s.wake()
	return RekeyView{
		JobID:         operation.JobID,
		PrimaryKeyID:  operation.PrimaryKeyID,
		RetiringKeyID: operation.RetiringKeyID,
		CreatedAt:     operation.CreatedAt,
	}, nil
}

var rekeyStoreErrors = apperror.StoreTable{
	Conflict:    "mail.rekey.conflict",
	Unavailable: "mail.unavailable",
}
