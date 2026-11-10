// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package batchitem carries the academic administration batch context of one
// membership command. It is internal to app so that only the application's
// own batch and onboarding code can build a non-zero Item: a transport can
// neither import this package nor set an Item's fields, so a transport call
// always runs as an ordinary, fully checked command.
package batchitem

import (
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Fields is the batch context. RetainedOutcome means the batch already
// decided the item and only its durable outcome is kept; the progression
// fields apply only to Class enrollment.
type Fields struct {
	// Replayed, when set, receives whether the Store replayed or skipped
	// the item.
	Replayed                      *bool
	Authorization                 *store.CommandAuthorization
	Metadata                      *store.CommandBatch
	RetainedOutcome               bool
	StudentProgression            bool
	ProgressionSourceAuditID      model.AuditEventID
	ProgressionDestinationAuditID model.AuditEventID
	OnboardingImportID            model.OnboardingImportID
	OnboardingImportRowNumber     int
}

// Item is an opaque batch context. Its zero value is an ordinary call.
type Item struct{ fields Fields }

// New returns the Item for fields.
func New(fields Fields) Item { return Item{fields: fields} }

// Fields returns a copy of the batch context.
func (i Item) Fields() Fields { return i.fields }
