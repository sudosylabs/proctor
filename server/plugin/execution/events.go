// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/plugin"
)

// eventHandler turns the core's lifecycle events into what each grant
// should be. Events arrive after commit and at least once, possibly out of
// order, so every change is an idempotent update that ignores anything
// older than what the grant already holds: a repeated event writes
// nothing new.
type eventHandler struct{ plugin *Plugin }

var topics = []plugin.EventTopic{
	plugin.TopicExamAttemptParticipationStarted,
	plugin.TopicExamAttemptParticipationEnded,
	plugin.TopicExamAttemptSubmitted,
	plugin.TopicExamSittingPaused,
	plugin.TopicExamSittingResumed,
	plugin.TopicExamSittingClosing,
}

func (h eventHandler) HandleEvent(ctx context.Context, event plugin.Event) error {
	records := h.plugin.store()
	switch event.Topic {
	case plugin.TopicExamAttemptParticipationStarted:
		var data plugin.ExamAttemptParticipationStarted
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil
		}
		return h.started(ctx, records, data)
	case plugin.TopicExamAttemptParticipationEnded:
		var data plugin.ExamAttemptParticipationEnded
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil
		}
		return h.ended(ctx, records, data)
	case plugin.TopicExamAttemptSubmitted:
		var data plugin.ExamAttemptSubmitted
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil
		}
		return revoke(ctx, records, data.AttemptID)
	case plugin.TopicExamSittingPaused, plugin.TopicExamSittingResumed:
		var data plugin.ExamSittingChanged
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil
		}
		return h.sitting(ctx, records, data.SittingID, event.Topic == plugin.TopicExamSittingPaused, event.OccurredAt)
	case plugin.TopicExamSittingClosing:
		// The Sitting's Attempts are about to be sealed. Each one's own
		// participation ended event revokes its grant; until then the owner
		// keeps harvesting.
		return nil
	}
	return nil
}

func (h eventHandler) started(ctx context.Context, records recordStore, data plugin.ExamAttemptParticipationStarted) error {
	document, err := h.plugin.host.ExamSections().Get(ctx, data.ExamRevisionID)
	if errors.Is(err, plugin.ErrExamSectionNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the frozen section: %w", err)
	}
	section, err := parseSection(document)
	if err != nil {
		// A frozen section the plugin no longer accepts gives no terminal;
		// retrying cannot change it.
		h.plugin.logger.Warn("frozen execution section does not decode", "revision_id", data.ExamRevisionID)
		return nil
	}
	if err := records.ensure(ctx, kindSitting, sittingKey(data.SittingID, data.AttemptID)); err != nil {
		return err
	}
	grant, err := records.updateGrant(ctx, data.AttemptID, func(grant *grantRecord, exists bool) (bool, error) {
		if !exists {
			*grant = grantRecord{
				AttemptID: data.AttemptID, SittingID: data.SittingID, RevisionID: data.ExamRevisionID,
				Image: section.Image, RecordInput: section.RecordInput, State: statePending,
			}
		}
		if grant.Revoked || data.Generation < grant.Generation ||
			(data.Generation == grant.Generation && exists) {
			return false, nil
		}
		grant.Generation, grant.GenerationEnded = data.Generation, false
		return true, nil
	})
	if err != nil {
		return err
	}
	if grant.State != stateDone {
		return records.ensure(ctx, kindLive, attemptKey(data.AttemptID))
	}
	return nil
}

func (h eventHandler) ended(ctx context.Context, records recordStore, data plugin.ExamAttemptParticipationEnded) error {
	switch data.Reason {
	case plugin.ParticipationEndSubmitted, plugin.ParticipationEndManagerEnded, plugin.ParticipationEndSittingClosed:
		return revoke(ctx, records, data.AttemptID)
	}
	_, err := records.updateGrant(ctx, data.AttemptID, func(grant *grantRecord, exists bool) (bool, error) {
		if !exists || grant.Revoked || data.Generation != grant.Generation || grant.GenerationEnded {
			return false, nil
		}
		grant.GenerationEnded = true
		return true, nil
	})
	return err
}

// revoke finishes a grant for good.
func revoke(ctx context.Context, records recordStore, attemptID string) error {
	_, err := records.updateGrant(ctx, attemptID, func(grant *grantRecord, exists bool) (bool, error) {
		if !exists || grant.Revoked {
			return false, nil
		}
		grant.Revoked = true
		return true, nil
	})
	return err
}

// sitting applies a Sitting's pause or resume to each of its grants.
func (h eventHandler) sitting(ctx context.Context, records recordStore, sittingID string, paused bool, at time.Time) error {
	prefix := "sitting/" + sittingID + "/attempt/"
	after := ""
	for {
		page, err := records.records.List(ctx, kindSitting, prefix, after, plugin.MaxRecordListLimit)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, entry := range page {
			attemptID := entry.Key[len(prefix):]
			if _, err := records.updateGrant(ctx, attemptID, func(grant *grantRecord, exists bool) (bool, error) {
				if !exists || grant.Revoked || at.Before(grant.SittingChangedAt) ||
					(grant.SittingPaused == paused && !grant.SittingChangedAt.IsZero()) {
					return false, nil
				}
				grant.SittingPaused, grant.SittingChangedAt = paused, at.UTC()
				return true, nil
			}); err != nil {
				return err
			}
		}
		after = page[len(page)-1].Key
	}
}
