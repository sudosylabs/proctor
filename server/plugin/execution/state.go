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

// The plugin keeps all its state in its record store, under these kinds.
const (
	// kindGrant holds one Attempt's grant: what it should be and what the
	// owner last made it. Key attemptKey.
	kindGrant = "grant"
	// kindLive lists the grants that are not done, so the worker scans
	// only those. Key attemptKey; the document is empty.
	kindLive = "live"
	// kindSitting indexes grants by Sitting for Sitting-wide events. Key
	// sitting/<sitting>/attempt/<attempt>; the document is empty.
	kindSitting = "sitting"
	// kindProjection is the owner's record of what it projected into the
	// current epoch, so another node can continue it. Key attemptKey.
	kindProjection = "projection"
	// kindHarvest is a harvest batch the owner is committing, so a
	// takeover retries exactly it. Key attemptKey.
	kindHarvest = "harvest"
	// kindFlush is a flush request and its answer. Key attemptKey.
	kindFlush = "flush"
	// kindInput numbers terminal input evidence. Key attemptKey.
	kindInput = "input"
)

// The desired states of a grant.
const (
	desiredRunning = "running"
	desiredFrozen  = "frozen"
	desiredRevoked = "revoked"
)

// The states the owner reports.
const (
	statePending         = "pending"
	stateStarting        = "starting"
	stateLive            = "live"
	stateWaitingCapacity = "waiting_capacity"
	stateDone            = "done"
)

func attemptKey(attemptID string) string { return "attempt/" + attemptID }

func sittingKey(sittingID, attemptID string) string {
	return "sitting/" + sittingID + "/attempt/" + attemptID
}

// grantRecord is one Attempt's grant. Event handlers write what the core
// decided (generation, pause, revocation); the owner writes placement,
// identity and cursors. Both go through updateGrant, so neither loses the
// other's change.
type grantRecord struct {
	AttemptID  string `json:"attempt_id"`
	SittingID  string `json:"sitting_id"`
	RevisionID string `json:"revision_id"`
	// Generation is the newest Participation generation the core started,
	// and GenerationEnded whether it has ended since.
	Generation      int64 `json:"generation"`
	GenerationEnded bool  `json:"generation_ended,omitempty"`
	// SittingPaused is the Sitting's last pause or resume, as of
	// SittingChangedAt.
	SittingPaused    bool      `json:"sitting_paused,omitempty"`
	SittingChangedAt time.Time `json:"sitting_changed_at"`
	// Revoked is terminal: the Attempt is finished.
	Revoked     bool   `json:"revoked,omitempty"`
	Image       string `json:"image"`
	RecordInput bool   `json:"record_input,omitempty"`
	// Desired is derived from the fields above whenever they change.
	Desired string `json:"desired"`

	Host        string `json:"host,omitempty"`
	GrantID     string `json:"grant_id,omitempty"`
	Epoch       string `json:"epoch,omitempty"`
	Incarnation int64  `json:"incarnation"`
	// FenceRevision is the last fence revision an owner issued to the
	// grant. Each new owner and each control takes the next one.
	FenceRevision   uint64 `json:"fence_revision,omitempty"`
	BindingID       string `json:"binding_id,omitempty"`
	BoundGeneration int64  `json:"bound_generation,omitempty"`
	// WorkspaceCursor is the Attempt Workspace cursor projected into the
	// grant; ObservationCursor and ProcessCursor are the guest sequences
	// acknowledged.
	WorkspaceCursor   int64  `json:"workspace_cursor"`
	ObservationCursor uint64 `json:"observation_cursor"`
	ProcessCursor     uint64 `json:"process_cursor"`
	// Sequences numbers the evidence of each kind within the binding.
	Sequences map[string]int64 `json:"sequences,omitempty"`
	// PendingGaps are gaps that happened while no binding could take them.
	PendingGaps []gapNote `json:"pending_gaps,omitempty"`
	// WaitingSince starts the current wait for capacity.
	WaitingSince time.Time `json:"waiting_since"`
	State        string    `json:"state"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// gapNote is a gap recorded once a binding can carry it.
type gapNote struct {
	Kind       string          `json:"kind"`
	ObservedAt time.Time       `json:"observed_at"`
	Detail     json.RawMessage `json:"detail"`
}

// derive recomputes Desired.
func (g *grantRecord) derive() {
	switch {
	case g.Revoked:
		g.Desired = desiredRevoked
	case g.GenerationEnded || g.SittingPaused:
		g.Desired = desiredFrozen
	default:
		g.Desired = desiredRunning
	}
}

// bound is the environment the owner acts through, or false without a
// live binding.
func (g grantRecord) bound() (plugin.BoundEnvironment, bool) {
	if g.BindingID == "" || g.GrantID == "" || g.Epoch == "" {
		return plugin.BoundEnvironment{}, false
	}
	return plugin.BoundEnvironment{
		AttemptID:  g.AttemptID,
		BindingID:  g.BindingID,
		Generation: g.BoundGeneration,
		Identity: plugin.EnvironmentIdentity{
			GrantID: g.GrantID, Epoch: g.Epoch, Incarnation: g.Incarnation,
		},
	}, true
}

// projectionRecord is what the owner projected into one epoch: the cursor
// and each projected file's content version. With the journal after the
// cursor it is enough for another node to continue the epoch.
type projectionRecord struct {
	GrantID  string            `json:"grant_id"`
	Epoch    string            `json:"epoch"`
	Cursor   int64             `json:"cursor"`
	Versions map[string]string `json:"versions"`
}

// flushRecord is one flush request and, once Done, its answer.
type flushRecord struct {
	Nonce           string    `json:"nonce"`
	RequestedAt     time.Time `json:"requested_at"`
	Done            bool      `json:"done,omitempty"`
	Failed          bool      `json:"failed,omitempty"`
	WorkspaceCursor int64     `json:"workspace_cursor,omitempty"`
	FrozenAt        time.Time `json:"frozen_at"`
}

// inputRecord numbers terminal input evidence within a binding.
type inputRecord struct {
	BindingID string `json:"binding_id"`
	Next      int64  `json:"next"`
	Bytes     int64  `json:"bytes"`
}

// recordStore is the typed view of the plugin's records.
type recordStore struct{ records plugin.Records }

const maxUpdateAttempts = 16

func (s recordStore) get(ctx context.Context, kind, key string, target any) (int64, bool, error) {
	record, err := s.records.Get(ctx, kind, key)
	if errors.Is(err, plugin.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if err := json.Unmarshal(record.Document, target); err != nil {
		return 0, false, fmt.Errorf("decode %s record: %w", kind, err)
	}
	return record.Revision, true, nil
}

func (s recordStore) put(ctx context.Context, kind, key string, value any, revision int64) (int64, error) {
	document, err := json.Marshal(value)
	if err != nil {
		return 0, fmt.Errorf("encode %s record: %w", kind, err)
	}
	return s.records.Put(ctx, kind, key, document, revision)
}

// ensure writes an empty document if the record does not exist.
func (s recordStore) ensure(ctx context.Context, kind, key string) error {
	_, err := s.records.Put(ctx, kind, key, json.RawMessage(`{}`), 0)
	if errors.Is(err, plugin.ErrRevisionConflict) {
		return nil
	}
	return err
}

// remove deletes a record whatever its revision.
func (s recordStore) remove(ctx context.Context, kind, key string) error {
	for range maxUpdateAttempts {
		record, err := s.records.Get(ctx, kind, key)
		if errors.Is(err, plugin.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		err = s.records.Delete(ctx, kind, key, record.Revision)
		if err == nil || errors.Is(err, plugin.ErrRecordNotFound) {
			return nil
		}
		if !errors.Is(err, plugin.ErrRevisionConflict) {
			return err
		}
	}
	return plugin.ErrRevisionConflict
}

// getGrant reads one grant.
func (s recordStore) getGrant(ctx context.Context, attemptID string) (grantRecord, bool, error) {
	var grant grantRecord
	_, found, err := s.get(ctx, kindGrant, attemptKey(attemptID), &grant)
	return grant, found, err
}

// updateGrant applies change to the current grant and writes it back,
// retrying on a concurrent write. change sees exists false for a grant not
// yet written, and returns false to write nothing.
func (s recordStore) updateGrant(
	ctx context.Context,
	attemptID string,
	change func(grant *grantRecord, exists bool) (bool, error),
) (grantRecord, error) {
	for range maxUpdateAttempts {
		var grant grantRecord
		revision, found, err := s.get(ctx, kindGrant, attemptKey(attemptID), &grant)
		if err != nil {
			return grantRecord{}, err
		}
		write, err := change(&grant, found)
		if err != nil || !write {
			return grant, err
		}
		grant.derive()
		grant.UpdatedAt = time.Now().UTC()
		if _, err = s.put(ctx, kindGrant, attemptKey(attemptID), grant, revision); err == nil {
			return grant, nil
		} else if !errors.Is(err, plugin.ErrRevisionConflict) {
			return grantRecord{}, err
		}
	}
	return grantRecord{}, plugin.ErrRevisionConflict
}
