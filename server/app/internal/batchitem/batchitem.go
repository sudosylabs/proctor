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
	"maps"

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

// BindOnboardingImport records the onboarding import row a command belongs to
// on its idempotency input. An invalid import or row records nothing.
func BindOnboardingImport(idempotency *store.CommandIdempotency, id model.OnboardingImportID, rowNumber int) {
	if idempotency == nil || !id.IsValid() || rowNumber < 1 {
		return
	}
	idempotency.OnboardingImportID = id
	idempotency.OnboardingImportRowNumber = rowNumber
}

// AuditValue returns the audit value a command run for this Item begins its
// attempt with: value itself outside an onboarding import, and otherwise a
// copy that also names the import and row, as ImportAuditValue does.
func (i Item) AuditValue(value map[string]any) map[string]any {
	return ImportAuditValue(value, i.fields.OnboardingImportID, i.fields.OnboardingImportRowNumber)
}

// ImportAuditValue returns a copy of value that also names, as
// onboarding_import_id and onboarding_import_row, the onboarding import row a
// command runs for. The application begins the row's audit attempt with it,
// so the entry names its import and row whether the row succeeds or fails.
// An invalid import or row returns value unchanged.
func ImportAuditValue(value map[string]any, id model.OnboardingImportID, rowNumber int) map[string]any {
	if !id.IsValid() || rowNumber < 1 {
		return value
	}
	named := maps.Clone(value)
	if named == nil {
		named = make(map[string]any, 2)
	}
	named["onboarding_import_id"] = id
	named["onboarding_import_row"] = rowNumber
	return named
}

// BindAuthorization records the batch's authority on a command's idempotency
// input.
func BindAuthorization(command *store.CommandIdempotency, authority *store.CommandAuthorization) {
	if command != nil {
		command.Authorization = authority
	}
}

// BindBatch records the batch a command belongs to on its idempotency input.
func BindBatch(command *store.CommandIdempotency, batch *store.CommandBatch) {
	if command != nil {
		command.Batch = batch
	}
}
