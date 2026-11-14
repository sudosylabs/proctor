// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/plugin"
)

const (
	// flushWait is how long the flush route waits for the owner.
	flushWait = 20 * time.Second
	// flushPoll is how often it looks for the answer.
	flushPoll = 100 * time.Millisecond
)

// flushRequest returns an unanswered flush request, if there is one.
func (o *owner) flushRequest(ctx context.Context) (*flushRecord, int64, error) {
	var request flushRecord
	revision, found, err := o.records.get(ctx, kindFlush, attemptKey(o.attemptID), &request)
	if err != nil || !found {
		return nil, 0, err
	}
	if request.Done {
		if !o.thawAt.IsZero() || request.FrozenAt.IsZero() {
			return nil, 0, nil
		}
		// A flush another owner answered: keep its thaw time.
		o.thawAt = request.FrozenAt.Add(flushThaw)
		return nil, 0, nil
	}
	return &request, revision, nil
}

// flush freezes the grant after one last harvest and answers with the
// Workspace cursor that holds everything the grant had written. execenv
// serves no observation while a grant is frozen, so the drain comes first
// and the freeze right after it.
func (o *owner) flush(ctx context.Context, request *flushRecord, revision int64) error {
	if o.control != execenv.ControlFrozen {
		if err := o.sync(ctx, true); err != nil {
			o.answerFlush(ctx, request, revision, false)
			return err
		}
		if len(o.pending) > 0 {
			o.answerFlush(ctx, request, revision, false)
			return nil
		}
		if err := o.setControl(ctx, execenv.ControlFrozen); err != nil {
			o.answerFlush(ctx, request, revision, false)
			return err
		}
	} else if environment, ok := o.currentBinding(); ok {
		if _, err := o.readJournal(ctx, environment); err != nil {
			o.answerFlush(ctx, request, revision, false)
			return err
		}
	}
	o.thawAt = time.Now().Add(flushThaw)
	o.answerFlush(ctx, request, revision, true)
	return nil
}

// answerFlush records the outcome of a flush request.
func (o *owner) answerFlush(ctx context.Context, request *flushRecord, revision int64, done bool) {
	answer := *request
	answer.Done, answer.Failed = true, !done
	if done {
		answer.WorkspaceCursor, answer.FrozenAt = o.current, time.Now().UTC()
	}
	if _, err := o.records.put(ctx, kindFlush, attemptKey(o.attemptID), answer, revision); err != nil &&
		!errors.Is(err, plugin.ErrRevisionConflict) {
		o.logger.Warn("execution flush answer was not recorded", "error", errorClass(err))
	}
}

// flushResponse is the flush route's answer.
type flushResponse struct {
	WorkspaceCursor int64 `json:"workspace_cursor"`
}

// flushRoute asks the grant's owner to freeze and drain, and waits for the
// Workspace cursor to submit with. A flush that cannot finish answers 503;
// the candidate can still submit what was saved.
func (p *Plugin) flushRoute(ctx context.Context, access plugin.Access, _ plugin.Request) (plugin.Response, error) {
	candidate, ok := access.(plugin.CandidateAttemptAccess)
	if !ok {
		return plugin.Response{}, plugin.ErrBadRequest
	}
	records := p.store()
	grant, found, err := records.getGrant(ctx, candidate.AttemptID)
	if err != nil {
		return plugin.Response{}, err
	}
	if !found || grant.Desired != desiredRunning || grant.State != stateLive {
		return plugin.Response{}, plugin.ErrRouteConflict
	}
	nonce := rand.Text()
	key := attemptKey(candidate.AttemptID)
	for range maxUpdateAttempts {
		var existing flushRecord
		revision, _, err := records.get(ctx, kindFlush, key, &existing)
		if err != nil {
			return plugin.Response{}, err
		}
		_, err = records.put(ctx, kindFlush, key, flushRecord{Nonce: nonce, RequestedAt: time.Now().UTC()}, revision)
		if err == nil {
			break
		}
		if !errors.Is(err, plugin.ErrRevisionConflict) {
			return plugin.Response{}, err
		}
	}
	deadline := time.NewTimer(flushWait)
	defer deadline.Stop()
	ticker := time.NewTicker(flushPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return plugin.Response{}, ctx.Err()
		case <-deadline.C:
			return plugin.Response{}, fmt.Errorf("%w: the execution environment did not flush in time", plugin.ErrUnavailable)
		case <-ticker.C:
		}
		var answer flushRecord
		if _, _, err := records.get(ctx, kindFlush, key, &answer); err != nil {
			return plugin.Response{}, err
		}
		if answer.Nonce != nonce || !answer.Done {
			continue
		}
		if answer.Failed {
			return plugin.Response{}, fmt.Errorf("%w: the execution environment could not flush", plugin.ErrUnavailable)
		}
		body, err := json.Marshal(flushResponse{WorkspaceCursor: answer.WorkspaceCursor})
		if err != nil {
			return plugin.Response{}, err
		}
		return plugin.Response{Status: http.StatusOK, Body: body}, nil
	}
}
