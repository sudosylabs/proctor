// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"encoding/json"
	"errors"
)

// ExamSection is a part of an Exam's policy that a plugin owns, such as the
// terminal an Exam asks for. A plugin offers at most one, with
// Host.AddExamSection during Activate, and it is keyed by the plugin's ID.
//
// An Exam author sets the section on the Exam Draft as one JSON object of at
// most 64 KiB. The server keeps it in canonical form: keys sorted, no
// insignificant whitespace, and integer numbers only. Publishing the Draft
// freezes the section into the Exam Revision and its digest. The server asks
// the plugin to validate the section when it is saved and again when the
// Draft is published, and refuses both while the plugin is off or failed.
// After publication nothing asks the plugin again: admission and everything
// after it read the frozen section as stored.
type ExamSection interface {
	// Schema is a JSON Schema object describing the section, from which an
	// authoring UI draws its form. The server serves it unchanged;
	// ValidateSection, not the schema, decides what is accepted.
	Schema() json.RawMessage
	// ValidateSection checks a canonical section. An error refuses it; its
	// text is not shown to the author. It must return when ctx is done.
	ValidateSection(ctx context.Context, section json.RawMessage) error
}

// ErrExamSectionNotFound is returned by ExamSections.Get when the Exam
// Revision does not exist or carries no section of the calling plugin.
var ErrExamSectionNotFound = errors.New("exam revision has no section for this plugin")

// ExamSections reads the calling plugin's own sections frozen into Exam
// Revisions, which Host.ExamSections returns. It never reaches another
// plugin's section or the Draft, and it never writes.
type ExamSections interface {
	// Get returns the plugin's canonical section frozen into the Exam
	// Revision with the given ID, or ErrExamSectionNotFound.
	Get(ctx context.Context, revisionID string) (json.RawMessage, error)
}
