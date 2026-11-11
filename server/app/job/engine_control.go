// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

var (
	// ErrCancelUnsupported reports that the Job's type is not Cancelable, or
	// that Cancel was given a target not prepared for cancellation or an
	// incomplete audit reference.
	ErrCancelUnsupported = errors.New("job cancellation is unsupported")
	// ErrRetryUnsupported reports that the Job's type does not allow an
	// operator retry from the Job's status, or that Retry was given a target
	// not prepared for retry or an incomplete audit reference.
	ErrRetryUnsupported = errors.New("job retry is unsupported")
)

// ControlTarget is an engine-validated transition target. Its transition kind
// and optimistic-concurrency revision cannot be constructed by callers.
type ControlTarget struct {
	Projection View
	id         model.JobID
	revision   int64
	kind       controlKind
}

// View returns a copy of the Job as it was when the target was prepared, for
// the caller's audit record.
func (t ControlTarget) View() View { return cloneView(t.Projection) }

type controlKind string

const (
	controlCancel controlKind = "cancel"
	controlRetry  controlKind = "retry"
)

// AuditReference identifies the durable audit attempt committed atomically by
// the Job Store transition.
type AuditReference struct {
	EventID model.AuditEventID
	At      time.Time
}

// PrepareCancellation reads an operator-visible Job and, when its type is
// Cancelable, returns the target Cancel applies. It changes nothing.
func (r *Engine) PrepareCancellation(ctx context.Context, id model.JobID) (ControlTarget, error) {
	record, err := r.operatorJob(ctx, id)
	if err != nil {
		return ControlTarget{}, err
	}
	descriptor, err := r.registry.Descriptor(record.Type)
	if err != nil || !descriptor.Cancelable {
		return ControlTarget{}, ErrCancelUnsupported
	}
	return ControlTarget{Projection: projectJob(record), id: record.ID, revision: record.Revision, kind: controlCancel}, nil
}

// PrepareRetry reads an operator-visible Job and, when its type allows an
// operator retry from the Job's current status, returns the target Retry
// applies. It changes nothing.
func (r *Engine) PrepareRetry(ctx context.Context, id model.JobID) (ControlTarget, error) {
	record, err := r.operatorJob(ctx, id)
	if err != nil {
		return ControlTarget{}, err
	}
	descriptor, err := r.registry.Descriptor(record.Type)
	if err != nil || !descriptor.SupportsExplicitRetry(record.Status) {
		return ControlTarget{}, ErrRetryUnsupported
	}
	return ControlTarget{Projection: projectJob(record), id: record.ID, revision: record.Revision, kind: controlRetry}, nil
}

// Cancel applies a target from PrepareCancellation. The store cancels the Job
// only if its revision is still the prepared one, and completes the audit
// attempt audit names in the same transaction.
func (r *Engine) Cancel(ctx context.Context, target ControlTarget, audit AuditReference) (View, error) {
	if target.kind != controlCancel || !validAuditReference(audit) {
		return View{}, ErrCancelUnsupported
	}

	record, err := r.deps.Store.Cancel(ctx, &store.JobMutation{
		ID:               target.id,
		ExpectedRevision: target.revision,
		AuditEventID:     audit.EventID,
		AuditAt:          audit.At,
	})
	if err != nil {
		return View{}, err
	}

	return projectJob(record), nil
}

// Retry applies a target from PrepareRetry. The store retries the Job only
// if its revision is still the prepared one, and completes the audit attempt
// audit names in the same transaction.
func (r *Engine) Retry(ctx context.Context, target ControlTarget, audit AuditReference) (View, error) {
	if target.kind != controlRetry || !validAuditReference(audit) {
		return View{}, ErrRetryUnsupported
	}

	record, err := r.deps.Store.Retry(ctx, &store.JobMutation{
		ID:               target.id,
		ExpectedRevision: target.revision,
		AuditEventID:     audit.EventID,
		AuditAt:          audit.At,
	})
	if err != nil {
		return View{}, err
	}

	return projectJob(record), nil
}

func validAuditReference(audit AuditReference) bool { return audit.EventID != "" && !audit.At.IsZero() }
