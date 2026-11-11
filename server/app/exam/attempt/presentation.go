// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/safemarkdown"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Presentation is what the candidate's live Attempt displays: the Exam title,
// sanitized Exam Instructions, capacity policy, Exam Resources, runtime
// capabilities, browser policy, and live corrections.
type Presentation struct {
	AttemptID            model.ExamAttemptID
	SittingID            model.ExamSittingID
	ClassID              model.ClassID
	Title                string
	InstructionsMarkdown string
	Capacity             model.ExamCapacityPolicy
	RuntimeCapabilities  store.CandidateRuntimeCapabilities
	BrowserPolicy        *store.CandidateBrowserPolicy
	LiveCorrections      []model.CandidateLiveCorrection
	Resources            []Resource
}

// Resource is the candidate-visible metadata of one published Exam Resource,
// in presentation order.
type Resource struct {
	ResourceID          model.ExamResourceID
	DisplayName         string
	DescriptionMarkdown string
	Position            int
	MediaType           model.ExamResourceMediaType
	SizeBytes           int64
	SHA256              string
}

// GetCandidateExamPresentation returns what the candidate's live Attempt
// displays: title, sanitized Exam Instructions, Exam Resources, runtime
// capabilities, browser policy, and live corrections.
func (s *Service) GetCandidateExamPresentation(
	ctx context.Context,
	inv invocation.Invocation,
	candidateAccess CandidateAccess,
) (_ Presentation, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := candidateSelector(inv, candidateAccess)
	if err != nil {
		return Presentation{}, err
	}

	stored, err := s.attempts.GetCandidatePresentation(ctx, selector)
	if err != nil {
		return Presentation{}, storeError(storeErrors, err)
	}

	if !stored.Consistent() {
		return Presentation{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("missing candidate presentation"))
	}

	result := Presentation{
		AttemptID:            stored.AttemptID,
		SittingID:            stored.SittingID,
		ClassID:              stored.ClassID,
		Title:                stored.Title,
		InstructionsMarkdown: safemarkdown.Sanitize(stored.InstructionsMarkdown),
		Capacity:             stored.Capacity,
		RuntimeCapabilities:  stored.RuntimeCapabilities,
		BrowserPolicy:        cloneCandidateBrowserPolicy(stored.BrowserPolicy),
		LiveCorrections:      model.CloneCandidateLiveCorrections(stored.LiveCorrections),
		Resources:            make([]Resource, len(stored.Resources)),
	}
	for index, item := range stored.Resources {
		result.Resources[index] = Resource{
			ResourceID:          item.ResourceID,
			DisplayName:         item.DisplayName,
			DescriptionMarkdown: safemarkdown.Sanitize(item.DescriptionMarkdown),
			Position:            item.Position,
			MediaType:           item.MediaType,
			SizeBytes:           item.SizeBytes,
			SHA256:              item.SHA256,
		}
	}

	return result, nil
}

// OpenResourceQuery names one published Resource through the candidate's
// Attempt access.
type OpenResourceQuery struct {
	Access     CandidateAccess
	ResourceID model.ExamResourceID
}

// OpenCandidateExamResource opens the content of one Exam Resource presented
// to the candidate's live Attempt.
func (s *Service) OpenCandidateExamResource(
	ctx context.Context,
	inv invocation.Invocation,
	query OpenResourceQuery,
) (_ OpenedContent, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := candidateSelector(inv, query.Access)
	if err != nil {
		return OpenedContent{}, err
	}
	if !query.ResourceID.IsValid() {
		return OpenedContent{}, apperror.Invalid("exam.attempt.invalid", "exam_resource_id")
	}

	resolved, err := s.attempts.ResolveCandidateResource(ctx, selector, query.ResourceID)
	if err != nil {
		return OpenedContent{}, storeError(storeErrors, err)
	}

	if !resolved.ConsistentWith(query.ResourceID) {
		return OpenedContent{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("incomplete candidate Resource selector"))
	}

	body, err := s.content.OpenExamResource(ctx, resolved.FileRevisionID, resolved.RenditionID)
	if err != nil {
		return OpenedContent{}, apperror.New("exam.attempt.unavailable").Wrap(err)
	}

	return OpenedContent{
		Body:      body,
		MediaType: string(resolved.Resource.MediaType),
		SizeBytes: resolved.Resource.SizeBytes,
		SHA256:    resolved.Resource.SHA256,
	}, nil
}

func cloneCandidateBrowserPolicy(value *store.CandidateBrowserPolicy) *store.CandidateBrowserPolicy {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Policy = value.Policy.Clone()
	return &clone
}
