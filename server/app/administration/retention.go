// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CreateRetentionPreviewCommand struct {
	ExpectedPolicyRevision int64
	IdempotencyKey         string
}

type ChangeRetentionControlCommand struct {
	ExpectedRevision       int64
	ExpectedPolicyRevision int64
	PreviewID              model.RetentionPreviewID
	State                  model.RetentionControlState
	IdempotencyKey         string
}

// RetentionRecordPage is one page of retention records. Paging counts
// Submissions, and each Submission contributes one record per retention
// category, so Items can hold up to four times the requested limit; HasMore
// means further Submissions follow. AsOf is the time eligibility was evaluated
// at and PolicyRevision the retention policy revision it was evaluated under.
type RetentionRecordPage struct {
	PolicyRevision int64
	AsOf           time.Time
	Items          []RetentionRecordView
	HasMore        bool
}

// RetentionRecordView pairs one retention record with its Eligibility at the
// page's AsOf time. Retirement is nil unless the Submission has a retirement
// for the record's category that has not been canceled.
type RetentionRecordView struct {
	Record      model.RetentionRecord
	Eligibility model.RetentionEligibility
	Retirement  *model.RetentionRetirement
}

// GetRetentionControl returns the retention cleanup control.
func (s *Service) GetRetentionControl(ctx context.Context, invocation invocation.Invocation) (*model.RetentionControl, error) {
	inst, _, err := s.authorizeRetentionPolicy(ctx, invocation, model.ActionRetentionPolicyView)
	if err != nil {
		return nil, err
	}
	control, err := s.retention.GetControl(ctx)
	if err != nil {
		return nil, retentionError(err)
	}
	if control.Validate() != nil || control.InstitutionID != inst.ID {
		return nil, apperror.New("retention.unavailable")
	}
	return control, nil
}

// GetRetentionPreview returns one retention preview.
func (s *Service) GetRetentionPreview(
	ctx context.Context,
	invocation invocation.Invocation,
	id model.RetentionPreviewID,
) (*model.RetentionPreview, error) {
	inst, _, err := s.authorizeRetentionPolicy(ctx, invocation, model.ActionRetentionPolicyView)
	if err != nil {
		return nil, err
	}
	if !id.IsValid() {
		return nil, apperror.New("retention.invalid")
	}

	preview, err := s.retention.GetPreview(ctx, id)
	if err != nil {
		return nil, retentionError(err)
	}
	if preview.Validate() != nil || preview.InstitutionID != inst.ID {
		return nil, apperror.New("retention.unavailable")
	}

	return preview, nil
}

// CreateRetentionPreview records an audited preview of what the expected
// Retention Policy revision would retire.
func (s *Service) CreateRetentionPreview(
	ctx context.Context,
	invocation invocation.Invocation,
	command CreateRetentionPreviewCommand,
) (*model.RetentionPreview, error) {
	inst, resource, err := s.authorizeRetentionPolicy(ctx, invocation, model.ActionRetentionPolicyView)
	if err != nil {
		return nil, err
	}
	if command.ExpectedPolicyRevision < 1 {
		return nil, apperror.New("retention.invalid")
	}
	if command.IdempotencyKey == "" {
		return nil, apperror.New("idempotency.key_required")
	}

	idempotency, err := idempotency.Prepare(
		invocation,
		store.RetentionPreviewOperation,
		command.IdempotencyKey,
		struct {
			InstitutionID  model.InstitutionID
			PolicyRevision int64
		}{inst.ID, command.ExpectedPolicyRevision},
	)
	if err != nil {
		return nil, err
	}

	preview, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionRetentionPolicyView,
			Resource:   resource,
			ScopeType:  model.RoleScopeInstitution,
			ScopeID:    inst.ID.String(),
			Operation:  "preview",
			Value:      map[string]any{"policy_revision": command.ExpectedPolicyRevision},
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*model.RetentionPreview, error) {
			return s.retention.CreatePreview(ctx, &store.RetentionPreviewCreation{
				RetentionMutation: store.RetentionMutation{
					Principal:    invocation.Principal(),
					AuditEventID: reference.ID,
					AuditAt:      reference.MutationAt,
				},
				PreviewID:              model.NewRetentionPreviewID(),
				ExpectedPolicyRevision: command.ExpectedPolicyRevision,
			}, idempotency)
		},
		retentionError,
	)
	if err != nil {
		return nil, err
	}
	if preview.Validate() != nil || preview.InstitutionID != inst.ID {
		return nil, apperror.New("retention.unavailable")
	}

	return preview, nil
}

// ChangeRetentionControl enables retention cleanup against a reviewed
// preview, or pauses it. It needs a strong, recent Session and
// retention_cleanup.manage.
func (s *Service) ChangeRetentionControl(
	ctx context.Context,
	invocation invocation.Invocation,
	command ChangeRetentionControlCommand,
) (*model.RetentionControl, error) {
	if err := access.RequireStrongRecentSession(invocation.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	inst, resource, err := s.authorizeRetentionPolicy(ctx, invocation, model.ActionRetentionCleanupManage)
	if err != nil {
		return nil, err
	}

	if command.ExpectedRevision < 1 ||
		command.ExpectedPolicyRevision < 1 ||
		(command.State != model.RetentionControlEnabled && command.State != model.RetentionControlPaused) ||
		(command.State == model.RetentionControlEnabled && !command.PreviewID.IsValid()) ||
		(command.State == model.RetentionControlPaused && !command.PreviewID.IsZero()) {
		return nil, apperror.New("retention.invalid")
	}

	if command.IdempotencyKey == "" {
		return nil, apperror.New("idempotency.key_required")
	}

	idempotency, err := idempotency.Prepare(
		invocation,
		store.RetentionControlOperation,
		command.IdempotencyKey,
		struct {
			InstitutionID  model.InstitutionID
			Revision       int64
			PolicyRevision int64
			State          model.RetentionControlState
			PreviewID      model.RetentionPreviewID
		}{
			inst.ID,
			command.ExpectedRevision,
			command.ExpectedPolicyRevision,
			command.State,
			command.PreviewID,
		},
	)
	if err != nil {
		return nil, err
	}

	result, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionRetentionCleanupManage,
			Resource:   resource,
			ScopeType:  model.RoleScopeInstitution,
			ScopeID:    inst.ID.String(),
			Operation:  "control",
			Value: map[string]any{
				"policy_revision":  command.ExpectedPolicyRevision,
				"control_revision": command.ExpectedRevision,
				"state":            string(command.State),
			},
		},
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.RetentionControlResult, error) {
			return s.retention.ChangeControl(
				ctx,
				&store.RetentionControlChange{
					RetentionMutation: store.RetentionMutation{
						Principal:               invocation.Principal(),
						AuditEventID:            reference.ID,
						AuditAt:                 reference.MutationAt,
						RecentAuthenticationTTL: s.recentAuthenticationTTL,
					},
					ExpectedRevision:       command.ExpectedRevision,
					ExpectedPolicyRevision: command.ExpectedPolicyRevision,
					State:                  command.State,
					PreviewID:              command.PreviewID,
				},
				idempotency,
			)
		},
		retentionError,
	)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Control.Validate() != nil || result.Control.InstitutionID != inst.ID {
		return nil, apperror.New("retention.unavailable")
	}

	return result.Control, nil
}

// ListRetentionRecords returns a page of retention records with their
// eligibility.
func (s *Service) ListRetentionRecords(
	ctx context.Context,
	invocation invocation.Invocation,
	after model.SubmissionID,
	limit int,
) (*RetentionRecordPage, error) {
	if _, _, err := s.authorizeRetentionPolicy(ctx, invocation, model.ActionRetentionPolicyView); err != nil {
		return nil, err
	}
	if limit < 1 || limit > model.RetentionMaximumPageSize || !after.IsZero() && !after.IsValid() {
		return nil, apperror.New("retention.invalid")
	}

	stored, err := s.retention.ListRecords(ctx, store.RetentionRecordListOptions{AfterSubmissionID: after, Limit: limit})
	if err != nil {
		return nil, retentionError(err)
	}
	if stored == nil || stored.PolicyRevision < 1 || stored.AsOf.IsZero() || len(stored.Items) > 4*limit {
		return nil, apperror.New("retention.unavailable")
	}

	page := &RetentionRecordPage{
		PolicyRevision: stored.PolicyRevision,
		AsOf:           stored.AsOf,
		Items:          make([]RetentionRecordView, len(stored.Items)),
		HasMore:        stored.HasMore,
	}
	for i, item := range stored.Items {
		page.Items[i] = RetentionRecordView{Record: item.Record, Eligibility: item.Eligibility, Retirement: item.Retirement}
	}

	return page, nil
}

func retentionError(err error) error {
	var policy *store.ErrRetentionPolicyRevisionConflict
	if errors.As(err, &policy) {
		return retentionPolicyError(err)
	}
	return retentionStoreErrors.Translate(err)
}

var retentionStoreErrors = apperror.StoreTable{
	Rules:       []apperror.StoreRule{{Kind: apperror.StoreConflict, Entity: "authorization", Code: "authorization.denied"}},
	NotFound:    "resource.not_found",
	Conflict:    "retention.conflict",
	Invalid:     "retention.invalid",
	Unavailable: "retention.unavailable",
}
