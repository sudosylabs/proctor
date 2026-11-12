// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"sync"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// jobChangePublisher publishes the Job engine's Changes as job.changed for
// the operators subscribed to job.view on the Institution. The engine may not
// import app/realtime, so this seam turns its Change into the event.
type jobChangePublisher struct {
	publisher    realtime.Publisher
	institutions store.InstitutionStore

	mu sync.Mutex
	// institutionID is remembered once read: an installation's one
	// Institution never changes identity.
	institutionID model.InstitutionID
}

// JobChanged publishes change, best effort. Before bootstrap there is no
// Institution and so no subscriber, and nothing is published.
func (p *jobChangePublisher) JobChanged(ctx context.Context, change job.Change) {
	institutionID, err := p.institution(ctx)
	if store.IsNotFound(err) {
		return
	}
	var event realtime.Event
	if err == nil {
		event, err = realtime.NewJobChangedEvent(institutionID, change.ID, change.Type, change.Status, change.Progress)
	}
	if err == nil {
		err = p.publisher.Publish(ctx, event)
	}
	if err != nil {
		p.publisher.Report(ctx, "job.changed", err)
	}
}

func (p *jobChangePublisher) institution(ctx context.Context) (model.InstitutionID, error) {
	p.mu.Lock()
	known := p.institutionID
	p.mu.Unlock()
	if known != "" {
		return known, nil
	}
	institution, err := p.institutions.GetSingleton(ctx)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.institutionID = institution.ID
	p.mu.Unlock()
	return institution.ID, nil
}

var _ job.ChangeListener = (*jobChangePublisher)(nil)
