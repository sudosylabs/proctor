// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

// ErrPluginSectionUnavailable is what PluginSections answers for a plugin
// that is off, has failed, or offers no section.
var ErrPluginSectionUnavailable = errors.New("plugin offers no active exam section")

// PluginSections is what Exam authoring and publication need from the
// plugins that offer a section of Exam policy. The composition root adapts
// the plugin host to it. Nothing after publication uses it: admission and
// delivery read the section frozen into the Revision.
type PluginSections interface {
	// PluginSectionSchema returns the JSON Schema the plugin publishes for
	// its section, and false when ValidatePluginSection would answer
	// ErrPluginSectionUnavailable.
	PluginSectionSchema(id model.PluginID) (json.RawMessage, bool)
	// ValidatePluginSection asks the plugin whether a canonical section is
	// acceptable, bounded in time and containing a panic. It answers
	// ErrPluginSectionUnavailable for a plugin that is off, has failed or
	// offers no section; any other error refuses the section.
	ValidatePluginSection(ctx context.Context, id model.PluginID, section json.RawMessage) error
}

// validatePluginSection asks the plugin about section and turns its answer
// into the author-visible code. The plugin's own reason stays in the cause.
func validatePluginSection(ctx context.Context, sections PluginSections, id model.PluginID, section json.RawMessage) error {
	if sections == nil {
		return apperror.New("exam.plugin_section.plugin_inactive").WithField("plugin_id", id.String())
	}
	err := sections.ValidatePluginSection(ctx, id, section)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrPluginSectionUnavailable):
		return apperror.New("exam.plugin_section.plugin_inactive").WithField("plugin_id", id.String()).Wrap(err)
	default:
		return apperror.New("exam.plugin_section.invalid").WithField("plugin_id", id.String()).Wrap(err)
	}
}

// validatePluginSections asks every plugin with a section in turn, in
// plugin ID order, and stops at the first refusal.
func validatePluginSections(ctx context.Context, sections PluginSections, draft model.ExamPluginSections) error {
	for _, id := range draft.PluginIDs() {
		if err := validatePluginSection(ctx, sections, id, draft[id]); err != nil {
			return err
		}
	}
	return nil
}
