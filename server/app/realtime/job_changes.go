// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"sync"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// JobChangePublisher publishes job engine changes as job.changed. It exists
// because the engine may not import this package.
type JobChangePublisher struct {
	publisher    Publisher
	institutions store.InstitutionStore

	mu sync.Mutex
	// institutionID is cached once read: an installation's institution never
	// changes identity.
	institutionID model.InstitutionID
}

// NewJobChangePublisher reads the institution lazily, so it works before
// bootstrap.
func NewJobChangePublisher(publisher Publisher, institutions store.InstitutionStore) *JobChangePublisher {
	return &JobChangePublisher{publisher: publisher, institutions: institutions}
}

// JobChanged publishes change, best effort. Before bootstrap there is no
// institution and so no subscriber, and nothing is published.
func (p *JobChangePublisher) JobChanged(ctx context.Context, change job.Change) {
	institutionID, err := p.institution(ctx)
	if store.IsNotFound(err) {
		return
	}
	var event Event
	if err == nil {
		event, err = NewJobChangedEvent(institutionID, change.ID, change.Type, change.Status, change.Progress)
	}
	if err == nil {
		err = p.publisher.Publish(ctx, event)
	}
	if err != nil {
		p.publisher.Report(ctx, "job.changed", err)
	}
}

func (p *JobChangePublisher) institution(ctx context.Context) (model.InstitutionID, error) {
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

// MailDeliveryChanged publishes mail_delivery.changed for one delivery,
// best effort. The delivery Job holds no Publisher, so it reports through
// here; before bootstrap nothing is published.
func (p *JobChangePublisher) MailDeliveryChanged(ctx context.Context, deliveryID model.MailDeliveryID) {
	institutionID, err := p.institution(ctx)
	if store.IsNotFound(err) {
		return
	}
	var event Event
	if err == nil {
		event, err = NewMailDeliveryChangedEvent(institutionID, deliveryID)
	}
	if err == nil {
		err = p.publisher.Publish(ctx, event)
	}
	if err != nil {
		p.publisher.Report(ctx, "mail_delivery.changed", err)
	}
}

var _ job.ChangeListener = (*JobChangePublisher)(nil)
