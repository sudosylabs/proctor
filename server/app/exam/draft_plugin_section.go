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
	"slices"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DraftPluginSectionView is one plugin's section on an Exam Draft, with the
// schema its plugin publishes for the authoring form.
type DraftPluginSectionView struct {
	ExamID        model.ExamID
	PluginID      model.PluginID
	DraftRevision int64
	// Section is the canonical section, or nil when the Draft has none.
	Section json.RawMessage
	// Schema is the plugin's JSON Schema for the section, or nil while the
	// plugin is off, has failed or offers no section.
	Schema json.RawMessage
}

// ReplaceDraftPluginSectionCommand sets one plugin's section on the Draft,
// or removes it when Section is nil.
type ReplaceDraftPluginSectionCommand struct {
	ExamID                model.ExamID
	PluginID              model.PluginID
	ExpectedDraftRevision int64
	Section               json.RawMessage
	IdempotencyKey        string
}

// GetExamDraftPluginSection returns one plugin's Draft section and schema to
// a caller authorized to view the Exam.
func (a *Authoring) GetExamDraftPluginSection(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	pluginID model.PluginID,
) (_ DraftPluginSectionView, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !pluginID.IsValid() {
		return DraftPluginSectionView{}, apperror.Invalid("exam.invalid", "plugin_id")
	}
	view, err := a.GetExam(ctx, inv, examID)
	if err != nil {
		return DraftPluginSectionView{}, err
	}
	section, _ := view.Draft.PluginSections.Section(pluginID)
	result := DraftPluginSectionView{
		ExamID:        examID,
		PluginID:      pluginID,
		DraftRevision: view.Draft.Revision,
		Section:       section,
	}
	if a.pluginSections != nil {
		if schema, ok := a.pluginSections.PluginSectionSchema(pluginID); ok {
			result.Schema = slices.Clone(schema)
		}
	}
	return result, nil
}

// ReplaceExamDraftPluginSection sets or removes one plugin's section on the
// Draft. A section is saved only while its plugin is active, offers a
// section and accepts it; removing a section never asks the plugin.
// Publication, not this Draft edit, decides what open Sittings deliver.
func (a *Authoring) ReplaceExamDraftPluginSection(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplaceDraftPluginSectionCommand,
) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedDraftRevision < 1 {
		return View{}, apperror.Invalid("exam.invalid", "draft_revision")
	}
	if !command.PluginID.IsValid() {
		return View{}, apperror.Invalid("exam.invalid", "plugin_id")
	}

	var section json.RawMessage
	if command.Section != nil {
		canonical, err := model.CanonicalizeExamPluginSection(command.Section)
		if err != nil {
			return View{}, apperror.Invalid("exam.invalid", "section").Wrap(err)
		}
		section = canonical
	}

	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationReplacePluginSection, command.IdempotencyKey, struct {
		ExamID                string          `json:"exam_id"`
		PluginID              string          `json:"plugin_id"`
		ExpectedDraftRevision int64           `json:"expected_draft_revision"`
		Section               json.RawMessage `json:"section"`
	}{command.ExamID.String(), command.PluginID.String(), command.ExpectedDraftRevision, section})
	if err != nil {
		return View{}, err
	}

	at := model.TimeUTC(a.now())
	examAccess, err := a.exams.Access(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}

	action, err := a.actionForAccess(ctx, principal.UserID, examAccess, at, model.ActionExamManage, model.ActionExamManageOverride)
	if err != nil {
		return View{}, err
	}

	resource := model.Resource{Type: model.ResourceExam, ID: command.ExamID.String()}
	if err = a.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return View{}, err
	}

	if section != nil {
		if err = validatePluginSection(ctx, a.pluginSections, command.PluginID, section); err != nil {
			return View{}, err
		}
	}

	snapshot, err := a.exams.Get(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}

	candidate := *snapshot.Draft
	changed, err := candidate.ApplyPluginSection(command.PluginID, slices.Clone(section), at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "section").Wrap(err)
	}
	if !changed && snapshot.Draft.Revision == command.ExpectedDraftRevision && !snapshot.Exam.IsArchived() {
		return View{}, apperror.New("exam.draft.no_changes")
	}

	auditID, err := a.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    examAccess.Exam.AcademicUnitID.String(),
		Operation:  "replace_draft_plugin_section",
		Value: map[string]any{
			"exam_id":                 command.ExamID.String(),
			"plugin_id":               command.PluginID.String(),
			"expected_draft_revision": command.ExpectedDraftRevision,
			"draft_revision":          command.ExpectedDraftRevision + 1,
			"plugin_section_present":  section != nil,
		},
	})
	if err != nil {
		return View{}, err
	}

	result, err := a.exams.UpdateDraftPluginSection(
		ctx,
		&store.ExamDraftPluginSectionUpdate{
			ExamID:           command.ExamID,
			ActorUserID:      principal.UserID,
			ManagerOverride:  action == model.ActionExamManageOverride,
			ExpectedRevision: command.ExpectedDraftRevision,
			PluginID:         command.PluginID,
			Section:          section,
			UpdatedAt:        candidate.UpdatedAt,
			AuditEventID:     auditID,
			AuditAt:          at,
		},
		commandIdempotency,
	)
	if err != nil {
		mapped := examStoreErrors.Translate(err)
		if auditErr := a.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return View{}, errors.Join(mapped, auditErr)
		}
		return View{}, mapped
	}

	if !result.Replayed {
		if effectErr := a.publishDraftUpdated(ctx, result.Value.Exam.ID, result.Value.Draft.Revision); effectErr != nil {
			a.publisher.Report(ctx, "exam_draft_updated", effectErr)
		}
	}

	return projectView(result.Value), nil
}
