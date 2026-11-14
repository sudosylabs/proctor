// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/plugin"
)

// maxExamSectionSchemaBytes bounds an ExamSection's Schema.
const maxExamSectionSchemaBytes = 64 << 10

// ErrNoExamSection is returned for a plugin that is not active or offers no
// Exam section.
var ErrNoExamSection = errors.New("plugin offers no active exam section")

// ExamSectionStore reads the plugin sections frozen into Exam Revisions. The
// host names the plugin on each call, so no plugin reads another's section;
// the store answers with plugin.ErrExamSectionNotFound when there is none.
type ExamSectionStore interface {
	RevisionSection(ctx context.Context, pluginID, revisionID string) (json.RawMessage, error)
}

// AddExamSection records the section the plugin being activated offers.
func (r *registrar) AddExamSection(section plugin.ExamSection) error {
	if section == nil {
		return errors.New("exam section is nil")
	}
	var schema json.RawMessage
	if err := r.entry.call("exam section", func() error {
		schema = slices.Clone(section.Schema())
		return nil
	}); err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(schema)
	if len(schema) > maxExamSectionSchemaBytes || !json.Valid(trimmed) || !bytes.HasPrefix(trimmed, []byte("{")) {
		return errors.New("exam section schema must be a JSON object of at most 64 KiB")
	}
	guarded := &guardedExamSection{entry: r.entry, schema: schema, inner: section}
	return r.register("exam sections", func(c *contributions) error {
		for _, existing := range c.examSections {
			if existing.entry == r.entry {
				return fmt.Errorf("plugin %q already offers an exam section", r.entry.manifest.ID)
			}
		}
		c.examSections = append(c.examSections, guarded)
		return nil
	})
}

type guardedExamSection struct {
	entry  *entry
	schema json.RawMessage
	inner  plugin.ExamSection
}

// activeExamSection returns the section plugin id offers while it is active.
func (h *Host) activeExamSection(id string) (*guardedExamSection, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, section := range h.contributions.examSections {
		if section.entry.manifest.ID == id && section.entry.currentState() == StateActive {
			return section, true
		}
	}
	return nil, false
}

// ExamSectionSchema returns the schema of the section plugin id offers, and
// false when the plugin is not active or offers none.
func (h *Host) ExamSectionSchema(id string) (json.RawMessage, bool) {
	section, ok := h.activeExamSection(id)
	if !ok {
		return nil, false
	}
	return slices.Clone(section.schema), true
}

// ValidateExamSection asks plugin id whether section is acceptable, under
// the host's timeout and containing a panic. It returns ErrNoExamSection
// when the plugin is not active or offers no section, and
// plugin.ErrUnavailable when the plugin fails during the call.
func (h *Host) ValidateExamSection(ctx context.Context, id string, section json.RawMessage) error {
	guarded, ok := h.activeExamSection(id)
	if !ok {
		return fmt.Errorf("plugin %q: %w", id, ErrNoExamSection)
	}
	return guarded.entry.callWithin(ctx, h.timeout, "validate exam section", func(bounded context.Context) error {
		if guarded.entry.currentState() != StateActive {
			return fmt.Errorf("plugin %q: %w", id, plugin.ErrUnavailable)
		}
		return guarded.inner.ValidateSection(bounded, slices.Clone(section))
	})
}

// ExamSections returns the plugin's read of its frozen sections, scoped to
// it and usable while this activation lasts.
func (r *registrar) ExamSections() plugin.ExamSections {
	return pluginExamSections{store: r.host.examSections, activation: r.activation}
}

type pluginExamSections struct {
	store      ExamSectionStore
	activation *activation
}

var _ plugin.ExamSections = pluginExamSections{}

func (s pluginExamSections) Get(ctx context.Context, revisionID string) (json.RawMessage, error) {
	id := s.activation.entry.manifest.ID
	if s.store == nil || !s.activation.usable() {
		return nil, fmt.Errorf("plugin %q exam sections: %w", id, plugin.ErrUnavailable)
	}
	return s.store.RevisionSection(ctx, id, revisionID)
}
