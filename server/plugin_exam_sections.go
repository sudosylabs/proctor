// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// pluginSections presents the plugin host to Exam authoring and
// publication. A plugin that is off, has failed, offers no section, or does
// not answer in time reads as offering none, so the author learns the plugin
// is inactive rather than that the section is wrong.
type pluginSections struct {
	host *pluginhost.Host
}

var _ exam.PluginSections = pluginSections{}

func (s pluginSections) PluginSectionSchema(id model.PluginID) (json.RawMessage, bool) {
	if s.host == nil {
		return nil, false
	}
	return s.host.ExamSectionSchema(id.String())
}

func (s pluginSections) ValidatePluginSection(ctx context.Context, id model.PluginID, section json.RawMessage) error {
	if s.host == nil {
		return exam.ErrPluginSectionUnavailable
	}
	err := s.host.ValidateExamSection(ctx, id.String(), section)
	if errors.Is(err, pluginhost.ErrNoExamSection) || errors.Is(err, plugin.ErrUnavailable) || errors.Is(err, pluginhost.ErrTimedOut) {
		return fmt.Errorf("%w: %w", exam.ErrPluginSectionUnavailable, err)
	}
	return err
}

// storeExamSections adapts the Exam Revision Store to the plugin host's
// read of frozen sections, which may not import store. The host names the
// plugin; this adapter picks that plugin's section and answers with the
// plugin package's errors.
type storeExamSections struct {
	revisions store.ExamRevisionStore
}

var _ pluginhost.ExamSectionStore = storeExamSections{}

// newStoreExamSections returns the adapter, or nil without persistence so
// the host reports the sections unavailable.
func newStoreExamSections(revisions store.ExamRevisionStore) pluginhost.ExamSectionStore {
	if revisions == nil {
		return nil
	}
	return storeExamSections{revisions: revisions}
}

func (s storeExamSections) RevisionSection(ctx context.Context, pluginID, revisionID string) (json.RawMessage, error) {
	id, err := model.ParseExamRevisionID(revisionID)
	if err != nil {
		return nil, plugin.ErrExamSectionNotFound
	}
	sections, found, err := s.revisions.FindPluginSections(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read exam revision sections: %w", err)
	}
	if !found {
		return nil, plugin.ErrExamSectionNotFound
	}
	section, ok := sections.Section(model.PluginID(pluginID))
	if !ok {
		return nil, plugin.ErrExamSectionNotFound
	}
	return section, nil
}
