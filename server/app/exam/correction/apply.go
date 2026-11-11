// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// OptionalInstructions replaces the Exam instructions when Present; absent,
// the correction keeps the current ones.
type OptionalInstructions struct {
	Present  bool
	Markdown string
}

// OptionalBrowserPolicy replaces the Browser Policy when Present; absent, the
// correction keeps the current one.
type OptionalBrowserPolicy struct {
	Present bool
	Policy  model.BrowserPolicy
}

// ResourceManifestItem is one Exam Resource of the corrected Revision, in
// manifest order. A valid StageID selects staged content for it; a zero
// StageID keeps the content the Resource already has.
type ResourceManifestItem struct {
	ResourceID          model.ExamResourceID
	DisplayName         string
	DescriptionMarkdown string
	StageID             model.ExamCorrectionResourceStageID
}

type ApplyCommand struct {
	ExamID                    model.ExamID
	SittingID                 model.ExamSittingID
	ExpectedSittingRevision   int64
	ExpectedCurrentRevisionID model.ExamRevisionID
	Instructions              OptionalInstructions
	BrowserPolicy             OptionalBrowserPolicy
	Resources                 []ResourceManifestItem
	CandidateSummary          string
	AcknowledgementRequired   bool
	AffectedCapabilities      []model.CandidateCapability
	PrivateReason             string
	IdempotencyKey            string
}

// Result is the committed correction as the after-commit effects need it:
// the AppliedCorrection facts plus whether candidates must acknowledge it and
// whether the Store replayed an earlier outcome.
type Result struct {
	ExamID                  model.ExamID
	SittingID               model.ExamSittingID
	PreviousRevisionID      model.ExamRevisionID
	RevisionID              model.ExamRevisionID
	RevisionNumber          int64
	SittingState            model.ExamSittingState
	SittingRevision         int64
	EffectiveAt             time.Time
	AcknowledgementRequired bool
	AffectedCapabilities    []model.CandidateCapability
	Replayed                bool
}

// AppliedCorrection is the public result of applying a live correction. It
// deliberately omits the replay and acknowledgement facts that only the
// after-commit effects consume.
type AppliedCorrection struct {
	ExamID               model.ExamID
	SittingID            model.ExamSittingID
	PreviousRevisionID   model.ExamRevisionID
	RevisionID           model.ExamRevisionID
	RevisionNumber       int64
	SittingState         model.ExamSittingState
	SittingRevision      int64
	EffectiveAt          time.Time
	AffectedCapabilities []model.CandidateCapability
}

func (r Result) applied() AppliedCorrection {
	return AppliedCorrection{
		ExamID:               r.ExamID,
		SittingID:            r.SittingID,
		PreviousRevisionID:   r.PreviousRevisionID,
		RevisionID:           r.RevisionID,
		RevisionNumber:       r.RevisionNumber,
		SittingState:         r.SittingState,
		SittingRevision:      r.SittingRevision,
		EffectiveAt:          r.EffectiveAt,
		AffectedCapabilities: slices.Clone(r.AffectedCapabilities),
	}
}

// ApplyExamSittingCorrection idempotently applies a live correction to a
// Sitting, publishing a new Exam Revision from changed Exam Instructions,
// browser policy, or staged Exam Resources. It is fenced by
// ExpectedSittingRevision and ExpectedCurrentRevisionID.
func (s *Service) ApplyExamSittingCorrection(
	ctx context.Context,
	inv invocation.Invocation,
	c ApplyCommand,
) (_ AppliedCorrection, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	manifest := make([]ResourceManifestItem, len(c.Resources))
	copy(manifest, c.Resources)
	c.Resources = manifest
	c.AffectedCapabilities = slices.Clone(c.AffectedCapabilities)
	if err := validateApply(c); err != nil {
		return AppliedCorrection{}, err
	}

	c.Resources = append([]ResourceManifestItem(nil), c.Resources...)
	if c.BrowserPolicy.Present {
		if err := c.BrowserPolicy.Policy.ValidateInstitutionOrigin(s.institutionOrigin); err != nil {
			return AppliedCorrection{}, apperror.Invalid("exam.sitting.correction.invalid", "browser_policy").Wrap(err)
		}
	}

	commandIdempotency, err := prepareApplyIdempotency(inv, c)
	if err != nil {
		return AppliedCorrection{}, err
	}

	auth, err := s.authorizeSitting(ctx, inv, c.ExamID, c.SittingID)
	if err != nil {
		return AppliedCorrection{}, err
	}

	at := model.TimeUTC(s.now())
	value := map[string]any{
		"exam_id":                      c.ExamID.String(),
		"exam_sitting_id":              c.SittingID.String(),
		"expected_sitting_revision":    c.ExpectedSittingRevision,
		"expected_current_revision_id": c.ExpectedCurrentRevisionID.String(),
		"instructions_present":         c.Instructions.Present,
		"browser_policy_present":       c.BrowserPolicy.Present,
		"resource_count":               len(c.Resources),
		"acknowledgement_required":     c.AcknowledgementRequired,
	}
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     auth.action,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: c.SittingID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    auth.academicUnitID.String(),
		Operation:  "exam_sitting_content_correct",
		Value:      value,
	})
	if err != nil {
		return AppliedCorrection{}, err
	}

	var instructions *string
	if c.Instructions.Present {
		v := c.Instructions.Markdown
		instructions = &v
	}

	var browserPolicy *model.BrowserPolicy
	if c.BrowserPolicy.Present {
		value := c.BrowserPolicy.Policy.Clone()
		browserPolicy = &value
	}

	resources := make([]store.ExamCorrectionResourceManifestItem, len(c.Resources))
	for i, r := range c.Resources {
		resources[i] = store.ExamCorrectionResourceManifestItem{
			ResourceID:          r.ResourceID,
			DisplayName:         strings.TrimSpace(r.DisplayName),
			DescriptionMarkdown: r.DescriptionMarkdown,
			StageID:             r.StageID,
		}
	}

	stored, err := s.corrections.Apply(
		ctx,
		&store.ExamCorrectionApplication{
			InstitutionOrigin:       s.institutionOrigin,
			RevisionID:              s.newExamRevisionID(),
			ExamID:                  c.ExamID,
			SittingID:               c.SittingID,
			CurrentRevisionID:       c.ExpectedCurrentRevisionID,
			ExpectedSittingRevision: c.ExpectedSittingRevision,
			ActorUserID:             inv.Principal().UserID,
			ManagerOverride:         auth.override,
			InstructionsMarkdown:    instructions,
			BrowserPolicy:           browserPolicy,
			Resources:               resources,
			CandidateSummary:        c.CandidateSummary,
			AcknowledgementRequired: c.AcknowledgementRequired,
			AffectedCapabilities:    append([]model.CandidateCapability{}, c.AffectedCapabilities...),
			PrivateReason:           c.PrivateReason,
			AppliedAt:               at,
			AuditEventID:            auditID,
			AuditAt:                 at,
		},
		commandIdempotency,
	)
	if err != nil {
		return AppliedCorrection{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectResult(stored, c)
	if err != nil {
		return AppliedCorrection{}, err
	}

	if !result.Replayed {
		if effectErr := s.publishCorrected(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_sitting_content_corrected", effectErr)
		}
	}

	return result.applied(), nil
}

func validateApply(c ApplyCommand) error {
	if !c.ExamID.IsValid() ||
		!c.SittingID.IsValid() ||
		!c.ExpectedCurrentRevisionID.IsValid() ||
		c.ExpectedSittingRevision < 1 ||
		len(c.Resources) > model.ExamResourceMaximumCount ||
		!utf8.ValidString(c.PrivateReason) ||
		strings.TrimSpace(c.PrivateReason) == "" ||
		strings.TrimSpace(c.PrivateReason) != c.PrivateReason ||
		utf8.RuneCountInString(c.PrivateReason) > 1000 ||
		len(c.PrivateReason) > 4000 {
		return apperror.Invalid("exam.sitting.correction.invalid", "command")
	}

	if !c.Instructions.Present && c.Instructions.Markdown != "" {
		return apperror.Invalid("exam.sitting.correction.invalid", "instructions_markdown")
	}

	if c.Instructions.Present &&
		(!utf8.ValidString(c.Instructions.Markdown) || len(c.Instructions.Markdown) > model.ExamInstructionsMarkdownMaxBytes) {
		return apperror.Invalid("exam.sitting.correction.invalid", "instructions_markdown")
	}

	if !c.BrowserPolicy.Present &&
		(c.BrowserPolicy.Policy.Enabled || c.BrowserPolicy.Policy.StartRuleID != "" || len(c.BrowserPolicy.Policy.Rules) != 0) ||
		c.BrowserPolicy.Present && c.BrowserPolicy.Policy.Validate() != nil {
		return apperror.Invalid("exam.sitting.correction.invalid", "browser_policy")
	}

	if err := model.ValidateCandidateCorrectionSelection(c.CandidateSummary, c.AffectedCapabilities); err != nil {
		return apperror.Invalid("exam.sitting.correction.invalid", "candidate_summary")
	}

	resourceIDs := map[model.ExamResourceID]struct{}{}
	stageIDs := map[model.ExamCorrectionResourceStageID]struct{}{}
	for _, r := range c.Resources {
		if !r.ResourceID.IsValid() ||
			strings.TrimSpace(r.DisplayName) != r.DisplayName ||
			r.DisplayName == "" ||
			!utf8.ValidString(r.DisplayName) ||
			utf8.RuneCountInString(r.DisplayName) > model.ExamResourceDisplayNameMaxRunes ||
			!utf8.ValidString(r.DescriptionMarkdown) ||
			len(r.DescriptionMarkdown) > model.ExamResourceDescriptionMaxBytes {
			return apperror.Invalid("exam.sitting.correction.invalid", "resources")
		}

		if _, ok := resourceIDs[r.ResourceID]; ok {
			return apperror.Invalid("exam.sitting.correction.invalid", "resources")
		}

		resourceIDs[r.ResourceID] = struct{}{}
		if !r.StageID.IsZero() && !r.StageID.IsValid() {
			return apperror.Invalid("exam.sitting.correction.invalid", "resources")
		}

		if r.StageID.IsValid() {
			if _, ok := stageIDs[r.StageID]; ok {
				return apperror.Invalid("exam.sitting.correction.invalid", "resources")
			}

			stageIDs[r.StageID] = struct{}{}
		}
	}

	return nil
}

func projectResult(stored *store.ExamCorrectionResult, c ApplyCommand) (Result, error) {
	s := stored.Sitting.Sitting
	if !stored.Applied(c.ExpectedCurrentRevisionID, c.ExpectedSittingRevision) {
		return Result{}, apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("inconsistent correction outcome"))
	}

	return Result{
		ExamID:                  c.ExamID,
		SittingID:               c.SittingID,
		PreviousRevisionID:      stored.PreviousRevisionID,
		RevisionID:              stored.Revision.ID,
		RevisionNumber:          stored.Revision.Number,
		SittingState:            s.State,
		SittingRevision:         s.Revision,
		EffectiveAt:             model.TimeUTC(stored.EffectiveAt),
		AcknowledgementRequired: c.AcknowledgementRequired,
		AffectedCapabilities:    append([]model.CandidateCapability{}, c.AffectedCapabilities...),
		Replayed:                stored.Replayed,
	}, nil
}
