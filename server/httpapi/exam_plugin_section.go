// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// replaceExamDraftPluginSectionRequest sets one plugin's section on the
// Draft. Both members are required; a null section removes it.
type replaceExamDraftPluginSectionRequest struct {
	ExpectedDraftRevision int64           `json:"expected_revision"`
	Section               json.RawMessage `json:"section"`
}

func (r *replaceExamDraftPluginSectionRequest) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateJSONObjectMembers(data, "plugin section request"); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if len(members) != 2 || members["expected_revision"] == nil || members["section"] == nil {
		return errors.New("plugin section request requires exactly expected_revision and section")
	}
	var revision int64
	if err := json.Unmarshal(members["expected_revision"], &revision); err != nil {
		return err
	}
	section := bytes.TrimSpace(members["section"])
	switch {
	case bytes.Equal(section, []byte("null")):
		section = nil
	case !bytes.HasPrefix(section, []byte("{")):
		return errors.New("plugin section must be a JSON object or null")
	}
	*r = replaceExamDraftPluginSectionRequest{ExpectedDraftRevision: revision, Section: slices.Clone(section)}
	return nil
}

// examDraftPluginSectionResponse is one plugin's section on the Draft and
// the schema its plugin publishes for the authoring form.
type examDraftPluginSectionResponse struct {
	ExamID        string          `json:"exam_id"`
	PluginID      string          `json:"plugin_id"`
	DraftRevision int64           `json:"draft_revision"`
	Section       json.RawMessage `json:"section,omitempty"`
	// Schema is the plugin's JSON Schema as the JSON text it published, as
	// the plugin administration API carries settings_schema.
	Schema string `json:"schema,omitempty"`
}

func (h examHandlers) getDraftPluginSection(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}
	pluginID, err := route.PathID[model.PluginID](request, "plugin_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.GetExamDraftPluginSection(request.Context, request.Invocation(), examID, pluginID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, examDraftPluginSectionResponse{
		ExamID:        view.ExamID.String(),
		PluginID:      view.PluginID.String(),
		DraftRevision: view.DraftRevision,
		Section:       slices.Clone(view.Section),
		Schema:        string(view.Schema),
	}), nil
}

func (h examHandlers) replaceDraftPluginSection(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}
	pluginID, err := route.PathID[model.PluginID](request, "plugin_id")
	if err != nil {
		return route.Result{}, err
	}
	var body replaceExamDraftPluginSectionRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedDraftRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}
	view, err := h.app.ReplaceExamDraftPluginSection(request.Context, request.Invocation(), exam.ReplaceDraftPluginSectionCommand{
		ExamID:                examID,
		PluginID:              pluginID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Section:               body.Section,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, examResponseFromView(view)), nil
}

// pluginSectionsResponse spells plugin sections as one JSON object keyed by
// plugin ID; no sections spell {}.
func pluginSectionsResponse(sections model.ExamPluginSections) map[string]json.RawMessage {
	response := make(map[string]json.RawMessage, len(sections))
	for id, section := range sections {
		response[id.String()] = slices.Clone(section)
	}
	return response
}
